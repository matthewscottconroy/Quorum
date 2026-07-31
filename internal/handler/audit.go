package handler

import (
	"context"
	"encoding/csv"
	"fmt"
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
	repo     auditReader
	verifier chainVerifier
	audit    auditRepo
}

// SetAuditLogger lets the audit viewer itself record exports of the log.
func (h *AuditHandler) SetAuditLogger(a auditRepo) { h.audit = a }

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

// chainVerifier is the integrity side of the audit repo.
type chainVerifier interface {
	VerifyChain(ctx context.Context) (*repo.ChainStatus, error)
	ExportRows(ctx context.Context, fn func(e model.AuditEntry) error) error
}

// SetVerifier attaches the chain-verification capability (admin endpoints).
func (h *AuditHandler) SetVerifier(v chainVerifier) { h.verifier = v }

// Verify recomputes the audit log's hash chain and reports its status: entry
// count, head hash, and — if the chain is broken — the first bad sequence
// number. Admin+. A third party can record the head hash from this endpoint and
// later prove no retained history was rewritten.
func (h *AuditHandler) Verify(w http.ResponseWriter, r *http.Request) {
	if h.verifier == nil {
		writeError(w, http.StatusNotFound, "not available", "not_found")
		return
	}
	st, err := h.verifier.VerifyChain(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "verification error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// ExportCSV streams the full retained audit log with chain hashes as CSV, for
// handing to an accountant or lawyer. Verification instructions live in
// COMPLIANCE.md; the hashes let the recipient independently confirm the export
// matches the chain and that the chain is intact.
func (h *AuditHandler) ExportCSV(w http.ResponseWriter, r *http.Request) {
	if h.verifier == nil {
		writeError(w, http.StatusNotFound, "not available", "not_found")
		return
	}
	// Verify first, and stamp the result into the file: an export from a broken
	// chain must say so on its face.
	st, err := h.verifier.VerifyChain(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "verification error", "internal_error")
		return
	}
	auditExport(r, h.audit, "audit.csv", map[string]any{"entries": st.Entries})
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="quorum-audit-log.csv"`)
	cw := csv.NewWriter(w)
	status := "INTACT"
	if !st.OK {
		status = fmt.Sprintf("BROKEN_AT_SEQ_%d", st.BrokenSeq)
	}
	_ = cw.Write([]string{"# chain_status", status, "entries", strconv.FormatInt(st.Entries, 10), "head_seq", strconv.FormatInt(st.HeadSeq, 10), "head_hash", st.HeadHash})
	_ = cw.Write([]string{"seq", "id", "user_id", "user_email", "action", "entity_type", "entity_id", "detail", "created_at_utc", "prev_hash", "entry_hash"})
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	err = h.verifier.ExportRows(r.Context(), func(e model.AuditEntry) error {
		return cw.Write([]string{
			strconv.FormatInt(e.Seq, 10), e.ID, deref(e.UserID), deref(e.UserEmail), e.Action,
			deref(e.EntityType), deref(e.EntityID), e.Detail,
			e.CreatedAt.UTC().Format("2006-01-02 15:04:05.000000"),
			e.PrevHash, e.EntryHash,
		})
	})
	if err != nil {
		// Headers are already sent; the truncated file will fail hash checks,
		// which is the correct failure mode for an evidence export.
		return
	}
	cw.Flush()
}
