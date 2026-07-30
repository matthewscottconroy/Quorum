package handler

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5"

	"quorum/internal/metrics"
)

// RequestID assigns each request a short id, stores it in the context for
// logs, and echoes it back in the response header so a client can quote it in
// a bug report. An inbound X-Request-Id is honored only when trustProxy is set
// (i.e. a trusted reverse proxy controls the header) — otherwise any client
// could stamp its requests with another user's id and pollute log correlation.
func RequestID(trustProxy bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := ""
			if trustProxy {
				id = r.Header.Get("X-Request-Id")
			}
			if id == "" || len(id) > 64 {
				id = newRequestID()
			}
			w.Header().Set("X-Request-Id", id)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
		})
	}
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A time-based fallback keeps requests distinguishable if the RNG fails.
		return "req-" + time.Now().Format("150405.000000")
	}
	return hex.EncodeToString(b[:])
}

// Metrics records request count, latency, and in-flight gauge into the registry.
// It labels by the chi route pattern (e.g. /api/v1/members/{id}) rather than the
// raw path, so metric cardinality stays bounded.
func Metrics(reg *metrics.Registry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reg.IncInFlight()
			defer reg.DecInFlight()
			start := time.Now()
			mw := &auditResponseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(mw, r)
			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "other" // unmatched routes collapse to one series
			}
			reg.ObserveRequest(r.Method, route, mw.status, time.Since(start))
		})
	}
}

// Recoverer recovers from a panic in a downstream handler, counts it, logs it
// with the request id and a stack, and returns 500 — replacing chi's Recoverer
// so panics feed both the structured log and the metrics.
func Recoverer(reg *metrics.Registry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil && rec != http.ErrAbortHandler {
					reg.IncPanic()
					reqID, _ := r.Context().Value(ctxRequestID).(string)
					slog.LogAttrs(r.Context(), slog.LevelError, "http_panic",
						slog.Any("panic", rec),
						slog.String("method", r.Method),
						slog.String("path", redactURI(r.URL)),
						slog.String("request_id", reqID),
						slog.String("stack", string(debug.Stack())),
					)
					writeError(w, http.StatusInternalServerError, "internal error", "internal_error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// MetricsEndpoint returns a handler that renders the Prometheus exposition,
// gated by a bearer token. When token is empty the endpoint is disabled (404),
// so metrics never leak from a service exposed without one.
func MetricsEndpoint(reg *metrics.Registry, token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			http.NotFound(w, r)
			return
		}
		if !metricsAuthorized(r, token) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "metrics token required", "unauthorized")
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		reg.Render(w)
	}
}

// metricsAuthorized accepts the token via Authorization: Bearer only. A query
// parameter would leak the secret into proxy/CDN access logs and browser
// history, and the comparison is constant-time so the static secret can't be
// recovered byte-by-byte through a timing side channel.
func metricsAuthorized(r *http.Request, token string) bool {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(h[len(prefix):]), []byte(token)) == 1
}
