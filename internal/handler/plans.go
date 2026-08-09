package handler

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

var validPlanStatuses = map[string]bool{
	"draft": true, "active": true, "completed": true, "archived": true,
}

// planWorkSource lists the action items linked to a plan (satisfied by
// *repo.ActionItemsRepo).
type planWorkSource interface {
	List(ctx context.Context, f repo.ActionItemFilter) ([]model.ActionItem, int, error)
}

// PlansHandler handles strategic plan and plan decision endpoints.
type PlansHandler struct {
	repo     plansRepo
	notifier deletionNotifier
	work     planWorkSource
}

// NewPlansHandler constructs a PlansHandler.
func NewPlansHandler(r plansRepo) *PlansHandler {
	return &PlansHandler{repo: r}
}

// SetNotifier attaches an optional notifier used on gated deletes.
func (h *PlansHandler) SetNotifier(n deletionNotifier) { h.notifier = n }

// SetWork attaches the action-items source so plans can show their work.
func (h *PlansHandler) SetWork(w planWorkSource) { h.work = w }

// List handles GET requests for a paginated, filterable list of plans.
func (h *PlansHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := repo.PlanFilter{
		Status: q.Get("status"),
		Limit:  clampLimit(q.Get("limit"), 100),
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		f.Offset = v
	}
	plans, total, err := h.repo.List(r.Context(), f)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	writePage(w, plans, total, f.Limit, f.Offset)
}

// Create handles creating a plan.
func (h *PlansHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title              string  `json:"title"`
		Description        *string `json:"description"`
		Status             string  `json:"status"`
		OwnerID            *string `json:"owner_id"`
		TargetDate         *string `json:"target_date"`
		EstimatedCostMinor *int64  `json:"estimated_cost_minor"`
		CostCurrency       *string `json:"cost_currency"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	if body.Title == "" {
		writeError(w, 400, "title required", "bad_request")
		return
	}
	status := body.Status
	if status == "" {
		status = "draft"
	}
	if !validPlanStatuses[status] {
		writeError(w, 400, "invalid status: must be draft, active, completed, or archived", "bad_request")
		return
	}
	if body.OwnerID != nil && !isValidUUID(*body.OwnerID) {
		writeError(w, 400, "owner_id must be a UUID", "bad_request")
		return
	}
	var targetDate *time.Time
	if body.TargetDate != nil {
		t, err := time.Parse("2006-01-02", *body.TargetDate)
		if err != nil {
			writeError(w, 400, "target_date must be YYYY-MM-DD", "bad_request")
			return
		}
		targetDate = &t
	}

	if msg := validatePlanCost(body.EstimatedCostMinor, body.CostCurrency); msg != "" {
		writeError(w, 400, msg, "bad_request")
		return
	}
	pl, err := h.repo.Create(r.Context(), &model.Plan{
		Title:              body.Title,
		Description:        body.Description,
		Status:             status,
		OwnerID:            body.OwnerID,
		TargetDate:         targetDate,
		EstimatedCostMinor: body.EstimatedCostMinor,
		CostCurrency:       body.CostCurrency,
	}, userIDFromCtx(r))
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}
	writeJSON(w, 201, pl)
}

// Get returns a plan with its decisions and — when the work source is wired
// — the action items linked to it, so a plan page shows the actual work.
func (h *PlansHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	pl, err := h.repo.Get(r.Context(), id)
	if err != nil {
		// Matches the old genericGet contract: any lookup error reads as 404.
		writeError(w, http.StatusNotFound, "plan not found", "not_found")
		return
	}
	if h.work != nil {
		items, _, err := h.work.List(r.Context(), repo.ActionItemFilter{PlanID: id, Limit: 200})
		if err == nil {
			if items == nil {
				items = []model.ActionItem{}
			}
			pl.ActionItems = items
		}
	}
	writeJSON(w, 200, pl)
}

// validatePlanCost sanity-checks the optional cost estimate.
func validatePlanCost(minor *int64, currency *string) string {
	if minor != nil && (*minor < 0 || *minor > 1_000_000_000_000) {
		return "estimated_cost_minor is out of range"
	}
	if currency != nil && *currency != "" && len(*currency) != 3 {
		return "cost_currency must be a 3-letter code"
	}
	return ""
}

// Update handles updating a plan.
func (h *PlansHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	allowed := map[string]bool{
		"title": true, "description": true, "status": true, "owner_id": true, "target_date": true,
		"estimated_cost_minor": true, "cost_currency": true,
	}
	fields := filterAllowedFields(body, allowed)
	if len(fields) == 0 {
		writeError(w, 400, "no valid fields provided", "bad_request")
		return
	}
	if status, present := fields["status"]; present {
		s, ok := status.(string)
		if !ok || !validPlanStatuses[s] {
			writeError(w, 400, "invalid status: must be draft, active, completed, or archived", "bad_request")
			return
		}
	}
	if ownerID, present := fields["owner_id"]; present && ownerID != nil {
		s, ok := ownerID.(string)
		if !ok || !isValidUUID(s) {
			writeError(w, 400, "owner_id must be a UUID", "bad_request")
			return
		}
	}
	if targetDate, present := fields["target_date"]; present && targetDate != nil {
		s, ok := targetDate.(string)
		if !ok {
			writeError(w, 400, "target_date must be YYYY-MM-DD", "bad_request")
			return
		}
		if _, err := time.Parse("2006-01-02", s); err != nil {
			writeError(w, 400, "target_date must be YYYY-MM-DD", "bad_request")
			return
		}
	}
	pl, err := h.repo.Update(r.Context(), id, fields)
	if err != nil {
		writeRepoError(w, err, "plan not found", "update error")
		return
	}
	writeJSON(w, 200, pl)
}

// Delete handles deleting a plan.
func (h *PlansHandler) Delete(w http.ResponseWriter, r *http.Request) {
	crudDelete(w, r, deleteSpec[model.Plan]{
		entity:      "plan",
		notFoundMsg: "plan not found",
		get:         h.repo.Get,
		name:        func(pl *model.Plan) string { return pl.Title },
		del:         h.repo.Delete,
		notifier:    h.notifier,
		affected:    singleEmail(h.repo.OwnerEmail),
	})
}

// CreateDecision records a decision.
func (h *PlansHandler) CreateDecision(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Summary   string  `json:"summary"`
		Rationale *string `json:"rationale"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Summary == "" {
		writeError(w, 400, "summary required", "bad_request")
		return
	}
	d, err := h.repo.CreateDecision(r.Context(), &model.PlanDecision{
		PlanID:    id,
		Summary:   body.Summary,
		Rationale: body.Rationale,
	}, userIDFromCtx(r))
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}
	writeJSON(w, 201, d)
}

// UpdateDecision edits an existing decision.
func (h *PlansHandler) UpdateDecision(w http.ResponseWriter, r *http.Request) {
	did, ok := requireUUID(w, r, "did")
	if !ok {
		return
	}
	var body struct {
		Summary   *string `json:"summary"`
		Rationale *string `json:"rationale"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	d, err := h.repo.UpdateDecision(r.Context(), did, body.Summary, body.Rationale)
	if err != nil {
		writeRepoError(w, err, "decision not found", "update error")
		return
	}
	changed := map[string]any{}
	for k, v := range map[string]*string{"summary": body.Summary, "rationale": body.Rationale} {
		if v != nil {
			changed[k] = *v
		}
	}
	if len(changed) > 0 {
		setAuditDetail(r, map[string]any{"set": changed})
	}
	writeJSON(w, 200, d)
}

// DeleteDecision removes a decision.
func (h *PlansHandler) DeleteDecision(w http.ResponseWriter, r *http.Request) {
	did, ok := requireUUID(w, r, "did")
	if !ok {
		return
	}
	if err := h.repo.DeleteDecision(r.Context(), did); err != nil {
		writeRepoError(w, err, "decision not found", "delete error")
		return
	}
	w.WriteHeader(204)
}
