package handler

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// auditReader is the read side of the audit log, satisfied by *repo.AuditRepo.
type auditReader interface {
	List(ctx context.Context, f repo.AuditFilter) ([]model.AuditEntry, int, error)
}

// AuditHandler serves the admin-facing audit-log viewer. Read-only by design:
// the log is append-only, and nothing in the API can edit or delete an entry
// (retention pruning happens in the nightly job, not over HTTP).
type AuditHandler struct {
	repo auditReader
}

// NewAuditHandler constructs an AuditHandler.
func NewAuditHandler(r auditReader) *AuditHandler {
	return &AuditHandler{repo: r}
}

// List returns a filtered, paginated slice of the audit log (admin+).
func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := repo.AuditFilter{
		UserID:     q.Get("user_id"),
		Action:     q.Get("action"),
		EntityType: q.Get("entity_type"),
		EntityID:   q.Get("entity_id"),
		Limit:      clampLimit(q.Get("limit"), 50),
	}
	// A malformed user_id would make the ::uuid cast error out as a 500; reject
	// it as a bad request instead.
	if f.UserID != "" && !isValidUUID(f.UserID) {
		writeError(w, http.StatusBadRequest, "user_id must be a UUID", "bad_request")
		return
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		f.Offset = v
	}
	if s := q.Get("since"); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be YYYY-MM-DD", "bad_request")
			return
		}
		f.Since = &t
	}
	if s := q.Get("until"); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "until must be YYYY-MM-DD", "bad_request")
			return
		}
		// Inclusive of the whole day.
		end := t.Add(24*time.Hour - time.Second)
		f.Until = &end
	}

	entries, total, err := h.repo.List(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writePage(w, entries, total, f.Limit, f.Offset)
}
