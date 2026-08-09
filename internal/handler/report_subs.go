package handler

import (
	"context"
	"net/http"

	"quorum/internal/model"
)

// reportSubsRepo is satisfied by *repo.ReportSubsRepo.
type reportSubsRepo interface {
	ForUser(ctx context.Context, userID string) ([]model.ReportSubscription, error)
	Set(ctx context.Context, userID, report, cadence string) error
	Delete(ctx context.Context, userID, report string) error
}

// ReportSubsHandler manages a user's scheduled-report subscriptions (officer+;
// these are financial digests). The nightly job reads them and emails.
type ReportSubsHandler struct {
	repo reportSubsRepo
}

// NewReportSubsHandler constructs the handler.
func NewReportSubsHandler(r reportSubsRepo) *ReportSubsHandler {
	return &ReportSubsHandler{repo: r}
}

var validReport = map[string]bool{"ar_aging": true, "ap_aging": true, "income_statement": true}
var validCadence = map[string]bool{"weekly": true, "monthly": true}

// List returns the caller's subscriptions.
func (h *ReportSubsHandler) List(w http.ResponseWriter, r *http.Request) {
	subs, err := h.repo.ForUser(r.Context(), userIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, subs)
}

// Set adds or updates a subscription. Body: {report, cadence}. A cadence of
// "off" (or absent) removes the subscription.
func (h *ReportSubsHandler) Set(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Report  string `json:"report"`
		Cadence string `json:"cadence"`
	}
	if err := decodeJSON(r, &body); err != nil || !validReport[body.Report] {
		writeError(w, http.StatusBadRequest, "report must be ar_aging, ap_aging, or income_statement", "bad_request")
		return
	}
	uid := userIDFromCtx(r)
	if body.Cadence == "off" || body.Cadence == "" {
		if err := h.repo.Delete(r.Context(), uid, body.Report); err != nil {
			writeError(w, http.StatusInternalServerError, "save error", "internal_error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"report": body.Report, "cadence": "off"})
		return
	}
	if !validCadence[body.Cadence] {
		writeError(w, http.StatusBadRequest, "cadence must be weekly, monthly, or off", "bad_request")
		return
	}
	if err := h.repo.Set(r.Context(), uid, body.Report, body.Cadence); err != nil {
		writeError(w, http.StatusInternalServerError, "save error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"report": body.Report, "cadence": body.Cadence})
}
