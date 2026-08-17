package handler

import (
	"net/http"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// ListSchedules returns all recurring dues schedules (officer+).
func (h *DuesHandler) ListSchedules(w http.ResponseWriter, r *http.Request) {
	schedules, err := h.repo.ListSchedules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if schedules == nil {
		schedules = []model.DuesSchedule{}
	}
	writeJSON(w, http.StatusOK, schedules)
}

// CreateSchedule adds a recurring dues schedule for a tier (officer+).
func (h *DuesHandler) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tier        string `json:"tier"`
		AmountMinor int64  `json:"amount_minor"`
		Currency    string `json:"currency"`
		Cadence     string `json:"cadence"`
		DueDays     *int   `json:"due_days"`
		Active      *bool  `json:"active"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if body.Tier == "" {
		writeError(w, http.StatusBadRequest, "tier is required", "bad_request")
		return
	}
	if body.AmountMinor <= 0 {
		writeError(w, http.StatusBadRequest, "amount_minor must be greater than 0", "bad_request")
		return
	}
	if !model.ValidCadences[body.Cadence] {
		writeError(w, http.StatusBadRequest, "cadence must be annual, quarterly, or monthly", "bad_request")
		return
	}
	if body.Currency == "" {
		body.Currency = "USD"
	}
	cur, ok := validCurrencyCode(body.Currency)
	if !ok {
		writeError(w, http.StatusBadRequest, "currency must be a 3-8 letter code", "bad_request")
		return
	}
	body.Currency = cur
	dueDays := 30
	if body.DueDays != nil {
		if *body.DueDays < 0 {
			writeError(w, http.StatusBadRequest, "due_days must be 0 or more", "bad_request")
			return
		}
		dueDays = *body.DueDays
	}
	active := true
	if body.Active != nil {
		active = *body.Active
	}
	s, err := h.repo.CreateSchedule(r.Context(), &model.DuesSchedule{
		Tier: body.Tier, AmountMinor: body.AmountMinor, Currency: body.Currency,
		Cadence: body.Cadence, DueDays: dueDays, Active: active,
	})
	if err != nil {
		if isUniqueViolation(err) {
			// One ACTIVE schedule per tier: two would bill every member in
			// the tier once per schedule.
			writeError(w, http.StatusConflict, "this tier already has an active schedule: deactivate it first", "conflict")
			return
		}
		writeError(w, http.StatusInternalServerError, "create error", "internal_error")
		return
	}
	writeJSON(w, http.StatusCreated, s)
}

// UpdateSchedule edits a recurring dues schedule (officer+).
func (h *DuesHandler) UpdateSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Tier        *string `json:"tier"`
		AmountMinor *int64  `json:"amount_minor"`
		Currency    *string `json:"currency"`
		Cadence     *string `json:"cadence"`
		DueDays     *int    `json:"due_days"`
		Active      *bool   `json:"active"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if body.AmountMinor != nil && *body.AmountMinor <= 0 {
		writeError(w, http.StatusBadRequest, "amount_minor must be greater than 0", "bad_request")
		return
	}
	if body.Cadence != nil && !model.ValidCadences[*body.Cadence] {
		writeError(w, http.StatusBadRequest, "invalid cadence", "bad_request")
		return
	}
	if body.DueDays != nil && *body.DueDays < 0 {
		writeError(w, http.StatusBadRequest, "due_days must be 0 or more", "bad_request")
		return
	}
	if body.Currency != nil {
		cur, ok := validCurrencyCode(*body.Currency)
		if !ok {
			writeError(w, http.StatusBadRequest, "currency must be a 3-8 letter code", "bad_request")
			return
		}
		body.Currency = &cur
	}
	s, err := h.repo.UpdateSchedule(r.Context(), id, body.Tier, body.AmountMinor, body.Currency, body.Cadence, body.DueDays, body.Active)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "this tier already has an active schedule: deactivate it first", "conflict")
			return
		}
		writeRepoError(w, err, "schedule not found", "update error")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// DeleteSchedule removes a recurring dues schedule (officer+).
func (h *DuesHandler) DeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.repo.DeleteSchedule(r.Context(), id); err != nil {
		writeRepoError(w, err, "schedule not found", "delete error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GenerateSchedule materializes invoices for the schedule's current period now,
// returning how many were created (officer+). Idempotent — re-running for the
// same period creates nothing further.
func (h *DuesHandler) GenerateSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	s, err := h.repo.GetSchedule(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "schedule not found", "query error")
		return
	}
	// An inactive schedule must not generate: the 0053 dedupe deactivated
	// duplicate tier schedules exactly so members stop being double-billed,
	// and "Generate now" on one of them re-opens that hole one click at a
	// time (different cadences → different period labels → no ON CONFLICT).
	if !s.Active {
		writeError(w, http.StatusConflict, "schedule is inactive: activate it before generating invoices", "conflict")
		return
	}
	label, _, due := repo.PeriodFor(s.Cadence, s.DueDays, time.Now())
	created, err := h.repo.GenerateInvoicesForSchedule(r.Context(), *s, label, due)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generation failed", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": created, "period_label": label})
}
