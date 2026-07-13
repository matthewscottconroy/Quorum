package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

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
	ctxUserID contextKey = "user_id"
	ctxRole   contextKey = "role"
)

// Middleware holds shared HTTP middleware.
type Middleware struct {
	jwtSecret   string
	rateLimiter *rateLimiter
}

func NewMiddleware(secret string) *Middleware {
	return &Middleware{
		jwtSecret:   secret,
		rateLimiter: newRateLimiter(10, time.Minute),
	}
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
		ctx := context.WithValue(r.Context(), ctxUserID, claims.UserID)
		ctx = context.WithValue(ctx, ctxRole, claims.Role)
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

// LoginRateLimit applies the IP-based rate limiter to the login endpoint.
func (m *Middleware) LoginRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// r.RemoteAddr is already set to the real client IP by chi's RealIP middleware.
		// Do not re-read X-Forwarded-For here — an attacker could inject a header
		// to bypass rate limiting.
		ip, _, _ := strings.Cut(r.RemoteAddr, ":")
		if !m.rateLimiter.Allow(ip) {
			writeError(w, http.StatusTooManyRequests, "too many login attempts", "rate_limited")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SecurityHeaders adds CSP and related headers to every response.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:")
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

func userIDFromCtx(r *http.Request) string {
	v, _ := r.Context().Value(ctxUserID).(string)
	return v
}

func roleFromCtx(r *http.Request) string {
	v, _ := r.Context().Value(ctxRole).(string)
	return v
}

func roleAtLeast(current, required string) bool {
	rank := map[string]int{"member": 1, "officer": 2, "admin": 3}
	return rank[current] >= rank[required]
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
			ar.Log(r.Context(), userID, action, "") //nolint:errcheck
		})
	}
}

// rateLimiter is a simple sliding-window rate limiter.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string][]time.Time
	limit   int
	window  time.Duration
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	rl := &rateLimiter{
		buckets: make(map[string][]time.Time),
		limit:   limit,
		window:  window,
	}
	go rl.cleanup()
	return rl
}

// cleanup sweeps stale buckets every 5 minutes to prevent unbounded memory growth.
func (rl *rateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
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
	if len(recent) == 0 {
		delete(rl.buckets, key)
	}
	rl.buckets[key] = append(recent, now)
	return true
}
