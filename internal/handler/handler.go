// Package handler implements the chi HTTP handlers, middleware, authentication
// and role gating, and rate limiting for the Quorum API.
package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
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

// sensitiveQueryParams are redacted from request logs so single-use secrets
// passed in the query string (ballot / reset tokens) never reach access logs.
var sensitiveQueryParams = []string{"token", "code"}

// RequestLogger logs one line per request (method, path, status, duration) with
// sensitive query-parameter values redacted. It replaces chi's default Logger,
// which would write the raw query string — and thus any ballot token — to stdout.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &auditResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(lw, r)
		attrs := []any{
			slog.String("method", r.Method),
			slog.String("path", redactURI(r.URL)),
			slog.Int("status", lw.status),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		}
		if id, _ := r.Context().Value(ctxRequestID).(string); id != "" {
			attrs = append(attrs, slog.String("request_id", id))
		}
		if uid, _ := r.Context().Value(ctxUserID).(string); uid != "" {
			attrs = append(attrs, slog.String("user_id", uid))
		}
		// 5xx is a server-side failure; surface it at a higher level for alerting.
		level := slog.LevelInfo
		if lw.status >= 500 {
			level = slog.LevelError
		}
		slog.LogAttrs(r.Context(), level, "http_request", toLogAttrs(attrs)...)
	})
}

// toLogAttrs narrows a []any of slog.Attr to []slog.Attr for LogAttrs.
func toLogAttrs(vals []any) []slog.Attr {
	out := make([]slog.Attr, 0, len(vals))
	for _, v := range vals {
		if a, ok := v.(slog.Attr); ok {
			out = append(out, a)
		}
	}
	return out
}

// redactURI returns the request URI with sensitive query-param values masked.
func redactURI(u *url.URL) string {
	if u.RawQuery == "" {
		return u.Path
	}
	q := u.Query()
	changed := false
	for _, k := range sensitiveQueryParams {
		if q.Has(k) {
			q.Set(k, "REDACTED")
			changed = true
		}
	}
	if !changed {
		return u.RequestURI()
	}
	clone := *u
	clone.RawQuery = q.Encode()
	return clone.RequestURI()
}

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
	ctxUserID    contextKey = "user_id"
	ctxRole      contextKey = "role"
	ctxMemberID  contextKey = "member_id"
	ctxRequestID contextKey = "request_id"
	// ctxAuditDetail carries a per-request holder that handlers fill with
	// "what changed" context for the audit entry (see setAuditDetail).
	ctxAuditDetail contextKey = "audit_detail"
)

// auditDetailHolder is injected by AuditMiddleware before the handler runs, so
// the handler can attach change details that the middleware then records in the
// same audit entry as the action itself.
type auditDetailHolder struct {
	mu sync.Mutex
	m  map[string]any
}

// setAuditDetail merges key/value pairs into the current request's audit
// detail. No-op outside AuditMiddleware (e.g. unauthenticated routes). Values
// become chain-protected JSONB: never put personal profile data here — the
// audit log outlives erasure.
func setAuditDetail(r *http.Request, kv map[string]any) {
	h, _ := r.Context().Value(ctxAuditDetail).(*auditDetailHolder)
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m == nil {
		h.m = map[string]any{}
	}
	for k, v := range kv {
		h.m[k] = v
	}
}

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
	// apiLimiter caps total authenticated API calls per user. It sits far above
	// any legitimate SPA burst; its only job is to stop a pathological client
	// (e.g. a loop hammering above-role endpoints, each denial writing an audit
	// row) from amplifying load or the audit chain without bound.
	apiLimiter *rateLimiter
	// trustProxy makes the limiter key on the proxy-supplied client IP header
	// instead of the socket address. Enable ONLY when a trusted reverse proxy
	// (e.g. the k8s ingress) sets X-Real-IP / X-Forwarded-For, otherwise all
	// clients behind the proxy share one bucket.
	trustProxy bool
	mfaPolicy  func() string
}

// NewMiddleware constructs the shared middleware with separate login and refresh rate limiters.
func NewMiddleware(secret string, trustProxy bool) *Middleware {
	return &Middleware{
		jwtSecret:      secret,
		loginLimiter:   newRateLimiter(10, time.Minute),
		refreshLimiter: newRateLimiter(60, time.Minute),
		apiLimiter:     newRateLimiter(1200, time.Minute),
		trustProxy:     trustProxy,
	}
}

// ClientIP exposes the middleware's client-address resolution (proxy-aware)
// for handlers that record request origins, e.g. the download ledger.
func (m *Middleware) ClientIP(r *http.Request) string { return m.clientIP(r) }

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

// APIRateLimit throttles authenticated API calls per user (keyed on the user id
// Auth placed in context, so it can't be evaded by rotating IPs and doesn't
// punish colleagues sharing one NAT). Apply it right after Auth. The ceiling is
// generous — it targets abuse loops, not normal use.
func (m *Middleware) APIRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, _ := r.Context().Value(ctxUserID).(string)
		if key == "" {
			key = m.clientIP(r)
		}
		if !m.apiLimiter.Allow(key) {
			writeError(w, http.StatusTooManyRequests, "too many requests: slow down", "rate_limited")
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
		// Org-mandated two-factor: when policy requires it for this role and
		// the token says not enrolled, everything except the enrollment path
		// is refused. Stateless (claim-based); enrollment lifts it at the
		// next token refresh.
		if m.mfaPolicy != nil && !claims.MFA {
			if pol := m.mfaPolicy(); pol != "" && pol != "off" &&
				roleRank[claims.Role] >= roleRank[pol] && !mfaExemptPath(r.Method, r.URL.Path) {
				writeError(w, http.StatusForbidden,
					"your organization requires two-factor authentication: set it up to continue", "mfa_required")
				return
			}
		}
		ctx := context.WithValue(r.Context(), ctxUserID, claims.UserID)
		ctx = context.WithValue(ctx, ctxRole, claims.Role)
		ctx = context.WithValue(ctx, ctxMemberID, claims.MemberID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// mfaExemptPath lists the walled garden a not-yet-enrolled user may reach
// under a 2FA mandate: see who they are, enroll, and leave. Method-aware:
// /settings/org is readable (the enrollment screen shows the policy) but a
// write there could flip require_2fa off, which would let a single-factor
// admin lift the mandate without ever enrolling.
func mfaExemptPath(method, p string) bool {
	switch strings.TrimPrefix(p, "/api/v1") {
	case "/auth/me", "/auth/logout", "/auth/2fa/setup", "/auth/2fa/enable":
		return true
	case "/settings/org":
		return method == http.MethodGet
	}
	return false
}

// SetMFAPolicy wires the require-2FA policy provider (a cached org-settings
// lookup returning off|admin|officer|member).
func (m *Middleware) SetMFAPolicy(fn func() string) { m.mfaPolicy = fn }

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
		// security-critical directive. 'wasm-unsafe-eval' additionally permits
		// compiling same-origin WebAssembly (the arcade cartridges); despite the
		// scary token name it does NOT enable JS eval()/Function(), so the XSS
		// posture is unchanged. style-src allows 'unsafe-inline' because
		// the vanilla-JS components set element style="" attributes for layout;
		// inline styles cannot execute code, so this does not weaken XSS defense.
		// blob: entries exist for the in-app document viewer: previews are
		// fetched with auth headers and rendered from object URLs (images,
		// PDFs, sandboxed HTML frames). Scripts stay 'self'-only.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data: blob:; frame-src 'self' blob:; object-src 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
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

// isDBRule reports whether err is a RAISE EXCEPTION (SQLSTATE P0001) from one
// of our integrity triggers; dbRuleMessage extracts its human-readable text.
func isDBRule(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "P0001"
}

func dbRuleMessage(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Message
	}
	return "operation not allowed"
}

// isConstraint reports whether err is a violation of the named constraint, so
// a handler can turn a specific database rule into a specific message.
func isConstraint(err error, name string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.ConstraintName == name
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (SQLSTATE 23505) — e.g. inserting a duplicate key.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// writeRepoError maps common repository errors to HTTP responses: missing rows
// → 404; a check-constraint violation (e.g. a too-long field) or a foreign-key
// violation → 400 (client's fault); everything else → 500 with fallbackMsg.
func writeRepoError(w http.ResponseWriter, err error, notFoundMsg, fallbackMsg string) {
	switch {
	case isNotFound(err):
		writeError(w, http.StatusNotFound, notFoundMsg, "not_found")
	case isDBRule(err):
		// A RAISE EXCEPTION from one of our integrity triggers (append-only
		// ledger, finalized minutes, …) is a business rule, not a server fault:
		// surface its message as a conflict.
		writeError(w, http.StatusConflict, dbRuleMessage(err), "conflict")
	case isCheckViolation(err):
		writeError(w, http.StatusBadRequest, "a field value is invalid or exceeds the maximum length", "bad_request")
	case isFKViolation(err):
		writeError(w, http.StatusBadRequest, "a referenced record does not exist", "bad_request")
	case isUniqueViolation(err):
		writeError(w, http.StatusConflict, "a record with these values already exists", "conflict")
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

// MaxRequestBody limits every request body to 1 MiB. Document uploads
// (POST .../file) are the one legitimately large body and get 25 MiB.
func MaxRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(1 << 20)
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/file") {
			limit = maxUploadBytes
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

// auditResponseWriter captures the HTTP status for the audit log, and — when
// captureBody is set — a bounded prefix of the response body so the audit
// middleware can recover the id of a newly created (POST) resource.
type auditResponseWriter struct {
	http.ResponseWriter
	status      int
	captureBody bool
	body        []byte
}

func (a *auditResponseWriter) WriteHeader(status int) {
	a.status = status
	a.ResponseWriter.WriteHeader(status)
}

// Hijack passes WebSocket upgrades through the observability wrappers —
// gorilla/websocket needs the raw TCP connection, and without this the
// arcade's /arcade/ws route dies with a 500 inside the upgrader.
func (a *auditResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := a.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("underlying ResponseWriter does not support hijacking")
}

// auditBodyCap bounds how much of the response body we buffer to find a
// created-row id — a created resource's id appears well within this.
const auditBodyCap = 4096

func (a *auditResponseWriter) Write(b []byte) (int, error) {
	if a.captureBody && len(a.body) < auditBodyCap {
		a.body = append(a.body, b[:min(len(b), auditBodyCap-len(a.body))]...)
	}
	return a.ResponseWriter.Write(b)
}

// AuditMiddleware logs successful (2xx) non-GET requests to the audit log.
func AuditMiddleware(ar auditRepo) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Buffer the body only for creates (POST), where the new row's id is
			// in the response rather than the URL.
			aw := &auditResponseWriter{ResponseWriter: w, status: http.StatusOK, captureBody: r.Method == http.MethodPost}
			holder := &auditDetailHolder{}
			r = r.WithContext(context.WithValue(r.Context(), ctxAuditDetail, holder))
			next.ServeHTTP(aw, r)
			if r.Method == http.MethodGet {
				return
			}
			userID, _ := r.Context().Value(ctxUserID).(string)
			if userID == "" {
				return
			}
			// Denied mutating attempts by a KNOWN user are an insider-threat
			// signal (probing endpoints above one's role, replaying stale
			// sessions) and are recorded as such. Other failures (400s, 404s,
			// 409s) are ordinary operation and would only add noise.
			if aw.status == http.StatusUnauthorized || aw.status == http.StatusForbidden {
				ar.Log(r.Context(), userID, fmt.Sprintf("DENIED(%d) %s %s", aw.status, r.Method, r.URL.Path), auditEntityType(r), chi.URLParam(r, "id"), nil) //nolint:errcheck
				return
			}
			if aw.status < 200 || aw.status >= 300 {
				return
			}
			action := r.Method + " " + r.URL.Path
			// Record the primary resource id (the {id} route param) as the
			// entity, so the audit log can answer "who changed which record".
			// A create carries no id in its URL, so recover it from the response.
			entityID := chi.URLParam(r, "id")
			if entityID == "" && r.Method == http.MethodPost {
				entityID = auditCreatedID(aw.body)
			}
			holder.mu.Lock()
			detail := holder.m
			holder.mu.Unlock()
			ar.Log(r.Context(), userID, action, auditEntityType(r), entityID, detail) //nolint:errcheck
		})
	}
}

// auditEntityType extracts the resource name from an API path for the audit
// log, e.g. /api/v1/members/{id} -> "members", /api/v1/dues/invoices -> "dues".
// It returns the segment following the "v1" version marker.
func auditEntityType(r *http.Request) string {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for i, p := range parts {
		if p == "v1" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	if n := len(parts); n > 0 {
		return parts[n-1]
	}
	return ""
}

// auditCreatedID pulls the top-level "id" field out of a JSON response body so
// the audit log can attribute a POST create to the row it produced. It returns
// "" when the body is not a JSON object or carries no string id.
func auditCreatedID(body []byte) string {
	var obj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &obj); err != nil {
		return ""
	}
	return obj.ID
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
