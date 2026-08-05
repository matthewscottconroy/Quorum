package handler

import (
	"context"
	"net/http"

	"quorum/internal/model"
)

// glRepo is satisfied by *repo.GLRepo.
type glRepo interface {
	TrialBalance(ctx context.Context) ([]model.GLBalance, error)
	Reconcile(ctx context.Context) ([]model.GLReconcileRow, error)
	RecentEntries(ctx context.Context, limit int) ([]model.GLEntry, error)
}

// AccountingHandler serves the read-only face of the general ledger
// (Phase A). Officer+: the same bar as the rest of the financial surface.
type AccountingHandler struct {
	repo glRepo
}

// NewAccountingHandler constructs the handler.
func NewAccountingHandler(r glRepo) *AccountingHandler {
	return &AccountingHandler{repo: r}
}

// TrialBalance returns per-account balances, the AR reconciliation check,
// and the most recent postings.
func (h *AccountingHandler) TrialBalance(w http.ResponseWriter, r *http.Request) {
	tb, err := h.repo.TrialBalance(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	rec, err := h.repo.Reconcile(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	entries, err := h.repo.RecentEntries(r.Context(), clampLimit(r.URL.Query().Get("limit"), 25))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if tb == nil {
		tb = []model.GLBalance{}
	}
	if entries == nil {
		entries = []model.GLEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts":   tb,
		"reconciled": len(rec) == 0,
		"mismatches": rec,
		"recent":     entries,
	})
}
