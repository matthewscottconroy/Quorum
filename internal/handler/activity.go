package handler

import (
	"context"
	"net/http"

	"quorum/internal/model"
)

// activityRepo is satisfied by *repo.ActivityRepo.
type activityRepo interface {
	Recent(ctx context.Context, limit int) ([]model.ActivityEvent, error)
}

// ActivityHandler serves the dashboard's recent-activity feed (member+).
type ActivityHandler struct {
	repo activityRepo
}

// NewActivityHandler constructs the handler.
func NewActivityHandler(r activityRepo) *ActivityHandler {
	return &ActivityHandler{repo: r}
}

// Recent returns org-visible recent events, newest first.
func (h *ActivityHandler) Recent(w http.ResponseWriter, r *http.Request) {
	events, err := h.repo.Recent(r.Context(), clampLimit(r.URL.Query().Get("limit"), 20))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, events)
}
