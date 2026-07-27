// Package handler implements the chi HTTP handlers, middleware, authentication
// and role gating, and rate limiting for the Quorum API.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"quorum/internal/auth"
)

// maxPageSize caps all paginated list endpoints.
const maxPageSize = 200

var uuidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func isValidUUID(s string) bool { return uuidRE.MatchString(s) }

// requireUUID reads a chi URL param and writes 400 if it is not a valid UUID.
// Returns the value and true on success, or ("", false) after writing the error.
func requireUUID(w http.ResponseWriter, r *http.Request, param string) (string, bool) {
	id := chi.URLParam(r, param)
	if !isValidUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid id", "bad_request")
		return "", false
	}
	return id, true
}

type contextKey string

const (
	ctxUserID   contextKey = "user_id"
	ctxRole     contextKey = "role"
	ctxMemberID contextKey = "member_id"
)

// roleRank orders the privilege ladder. restricted sees only its own record;
// member and up have full read access; superadmin adds destructive deletes.
var roleRank = map[string]int{
	"restricted": 1,
	"member":     2,
	"officer":    3,
	"admin":      4,
	"superadmin": 5,
}

// validRoles is the set assignable to a user.
var validRoles = map[string]bool{
	"restricted": true, "member": true, "officer": true, "admin": true, "superadmin": true,
}

// deletionNotifier is invoked after a gated destructive delete so that affected
// parties — always the admins/superadmins, plus any directly-affected members —
// are informed. Implementations must be safe to call with a nil affected slice.
// Optional dependency: handlers no-op when it is unset.
type deletionNotifier interface {
	NotifyDeletion(ctx context.Context, actorUserID, entityType, entityName string, affectedEmails []string)
}

// Middleware holds shared HTTP middleware.
type Middleware struct {
	jwtSecret string
	// loginLimiter guards the brute-forceable credential endpoints
	// (login, bootstrap). refreshLimiter is separate and far more permissive:
	// refresh is not a guessing target (the token is a 256-bit secret) and a
	// normal SPA hits it routinely as access tokens expire, so it must not
	// share login's tight budget.
	loginLimiter   *rateLimiter
	refreshLimiter *rateLimiter
	// trustProxy makes the limiter key on the proxy-supplied client IP header
	// instead of the socket address. Enable ONLY when a trusted reverse proxy
	// (e.g. the k8s ingress) sets X-Real-IP / X-Forwarded-For, otherwise all
	// clients behind the proxy share one bucket.
	trustProxy bool
}

// NewMiddleware constructs the shared middleware with separate login and refresh rate limiters.
func NewMiddleware(secret string, trustProxy bool) *Middleware {
	return &Middleware{
		jwtSecret:      secret,
		loginLimiter:   newRateLimiter(10, time.Minute),
		refreshLimiter: newRateLimiter(60, time.Minute),
		trustProxy:     trustProxy,
	}
}

// clientIP returns the key the rate limiter buckets on. When trustProxy is set
// it prefers X-Real-IP (a single value set by the ingress), then the leftmost
// X-Forwarded-For entry; otherwise it uses the raw socket address. Without
// trustProxy, forwarded headers are ignored so a client cannot spoof its key.
func (m *Middleware) clientIP(r *http.Request) string {
	if m.trustProxy {
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if first = strings.TrimSpace(first); first != "" {
				return first
			}
		}
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

func (m *Middleware) rateLimit(limiter *rateLimiter, msg string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow(m.clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, msg, "rate_limited")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Auth validates the JWT and injects user_id and role into the request context.
func (m *Middleware) Auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := auth.TokenFromHeader(r.Header.Get("Authorization"))
		claims, err := auth.ParseToken(token, m.jwtSecret)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired token", "unauthorized")
			return
		}
		// An interim two-factor token authorizes only the second login step, not
		// the API.
		if claims.Purpose == auth.PurposeMFA {
			writeError(w, http.StatusUnauthorized, "two-factor authentication incomplete", "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), ctxUserID, claims.UserID)
		ctx = context.WithValue(ctx, ctxRole, claims.Role)
		ctx = context.WithValue(ctx, ctxMemberID, claims.MemberID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole blocks requests whose role is less than the required level.
func (m *Middleware) RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			current := roleFromCtx(r)
			if !roleAtLeast(current, role) {
				writeError(w, http.StatusForbidden, "insufficient permissions", "forbidden")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// LoginRateLimit applies the tight credential-endpoint limiter (login, bootstrap).
func (m *Middleware) LoginRateLimit(next http.Handler) http.Handler {
	return m.rateLimit(m.loginLimiter, "too many login attempts", next)
}

// RefreshRateLimit applies the permissive token-refresh limiter.
func (m *Middleware) RefreshRateLimit(next http.Handler) http.Handler {
	return m.rateLimit(m.refreshLimiter, "too many refresh attempts", next)
}

// SecurityHeaders adds CSP and related headers to every response.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// script-src stays strict ('self', no inline/eval) — that is the
		// security-critical directive. style-src allows 'unsafe-inline' because
		// the vanilla-JS components set element style="" attributes for layout;
		// inline styles cannot execute code, so this does not weaken XSS defense.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// Helper functions shared across handlers.

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg, code string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// isNotFound reports whether err means the target row does not exist.
func isNotFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// isFKViolation reports whether err is a PostgreSQL foreign-key violation
// (SQLSTATE 23503), i.e. the row is still referenced by other records.
func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// isCheckViolation reports whether err is a PostgreSQL check-constraint
// violation (SQLSTATE 23514) — e.g. a text field exceeding its length bound.
func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

// writeRepoError maps common repository errors to HTTP responses: missing rows
// → 404; a check-constraint violation (e.g. a too-long field) or a foreign-key
// violation → 400 (client's fault); everything else → 500 with fallbackMsg.
func writeRepoError(w http.ResponseWriter, err error, notFoundMsg, fallbackMsg string) {
	switch {
	case isNotFound(err):
		writeError(w, http.StatusNotFound, notFoundMsg, "not_found")
	case isCheckViolation(err):
		writeError(w, http.StatusBadRequest, "a field value is invalid or exceeds the maximum length", "bad_request")
	case isFKViolation(err):
		writeError(w, http.StatusBadRequest, "a referenced record does not exist", "bad_request")
	default:
		writeError(w, http.StatusInternalServerError, fallbackMsg, "internal_error")
	}
}

// toStringSlice converts a decoded JSON array into []string, reporting
// whether every element was a string.
func toStringSlice(v any) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// clampLimit parses a positive limit bounded by maxPageSize, or returns def.
func clampLimit(raw string, def int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > maxPageSize {
		return maxPageSize
	}
	return n
}

func userIDFromCtx(r *http.Request) string {
	v, _ := r.Context().Value(ctxUserID).(string)
	return v
}

func roleFromCtx(r *http.Request) string {
	v, _ := r.Context().Value(ctxRole).(string)
	return v
}

func memberIDFromCtx(r *http.Request) string {
	v, _ := r.Context().Value(ctxMemberID).(string)
	return v
}

func roleAtLeast(current, required string) bool {
	return roleRank[current] >= roleRank[required]
}

// canViewMemberScoped reports whether the caller may read the member-scoped
// resource identified by memberID. Roles at `member` and above see everyone;
// a `restricted` user sees only their own linked record. Writes 403 and
// returns false when denied.
func canViewMemberScoped(w http.ResponseWriter, r *http.Request, memberID string) bool {
	if roleAtLeast(roleFromCtx(r), "member") {
		return true
	}
	// restricted: allowed only for their own linked member record.
	if mid := memberIDFromCtx(r); mid != "" && mid == memberID {
		return true
	}
	writeError(w, http.StatusForbidden, "you may only view your own record", "forbidden")
	return false
}

// confirmMatches enforces a type-to-confirm gate on destructive actions: the
// request must carry `?confirm=<text>` exactly matching expected (the record's
// name/title), case-insensitively. Writes 400 and returns false on mismatch.
func confirmMatches(w http.ResponseWriter, r *http.Request, expected string) bool {
	got := strings.TrimSpace(r.URL.Query().Get("confirm"))
	if got == "" {
		writeError(w, http.StatusBadRequest,
			"deletion requires confirmation: resend with ?confirm=<exact name>", "confirmation_required")
		return false
	}
	if !strings.EqualFold(got, strings.TrimSpace(expected)) {
		writeError(w, http.StatusBadRequest,
			"confirmation text does not match the record name", "confirmation_mismatch")
		return false
	}
	return true
}

func decodeJSON(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large", "payload_too_large")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid request body", "bad_request")
}

// MaxRequestBody limits every request body to 1 MiB.
func MaxRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		next.ServeHTTP(w, r)
	})
}

// auditResponseWriter captures the HTTP status for the audit log.
type auditResponseWriter struct {
	http.ResponseWriter
	status int
}

func (a *auditResponseWriter) WriteHeader(status int) {
	a.status = status
	a.ResponseWriter.WriteHeader(status)
}

// AuditMiddleware logs successful (2xx) non-GET requests to the audit log.
func AuditMiddleware(ar auditRepo) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			aw := &auditResponseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(aw, r)
			if r.Method == http.MethodGet {
				return
			}
			if aw.status < 200 || aw.status >= 300 {
				return
			}
			userID, _ := r.Context().Value(ctxUserID).(string)
			if userID == "" {
				return
			}
			action := r.Method + " " + r.URL.Path
			// Record the primary resource id (the {id} route param) as the
			// entity, so the audit log can answer "who changed which record".
			entityID := chi.URLParam(r, "id")
			ar.Log(r.Context(), userID, action, entityID) //nolint:errcheck
		})
	}
}

// rateLimiter is a simple sliding-window rate limiter.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string][]time.Time
	limit   int
	window  time.Duration
	stop    chan struct{}
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	rl := &rateLimiter{
		buckets: make(map[string][]time.Time),
		limit:   limit,
		window:  window,
		stop:    make(chan struct{}),
	}
	go rl.cleanup()
	return rl
}

// cleanup sweeps stale buckets every 5 minutes to prevent unbounded memory growth.
func (rl *rateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stop:
			return
		case <-ticker.C:
		}
		rl.mu.Lock()
		cutoff := time.Now().Add(-rl.window)
		for key, times := range rl.buckets {
			var recent []time.Time
			for _, t := range times {
				if t.After(cutoff) {
					recent = append(recent, t)
				}
			}
			if len(recent) == 0 {
				delete(rl.buckets, key)
			} else {
				rl.buckets[key] = recent
			}
		}
		rl.mu.Unlock()
	}
}

// Close stops the rate limiters' background cleanup goroutines. Only needed
// when a Middleware is created and discarded repeatedly (e.g. tests).
func (m *Middleware) Close() {
	close(m.loginLimiter.stop)
	close(m.refreshLimiter.stop)
}

func (rl *rateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)
	var recent []time.Time
	for _, t := range rl.buckets[key] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= rl.limit {
		rl.buckets[key] = recent
		return false
	}
	rl.buckets[key] = append(recent, now)
	return true
}
