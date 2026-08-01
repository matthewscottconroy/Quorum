package handler

import (
	"context"
	"net/http"
	"time"
)

// pinger is satisfied by *pgxpool.Pool.
type pinger interface {
	Ping(ctx context.Context) error
}

// Healthz is the liveness probe: it returns 200 whenever the process is up.
func Healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readyz returns the readiness probe handler: 200 when the database responds
// to a ping, 503 otherwise, so load balancers stop routing to broken pods.
func Readyz(db pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database unavailable", "not_ready")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}
