package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// PlansHandler handles strategic plan and plan decision endpoints.
type PlansHandler struct {
	repo plansRepo
}

// NewPlansHandler constructs a PlansHandler.
func NewPlansHandler(r plansRepo) *PlansHandler {
	return &PlansHandler{repo: r}
}

func (h *PlansHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := repo.PlanFilter{
		Status: q.Get("status"),
		Limit:  100,
	}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		f.Limit = v
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		f.Offset = v
	}
	plans, total, err := h.repo.List(r.Context(), f)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	if plans == nil {
		plans = []model.Plan{}
	}
	writeJSON(w, 200, model.Page[model.Plan]{Data: plans, Total: total, Limit: f.Limit, Offset: f.Offset})
}

func (h *PlansHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title       string  `json:"title"`
		Description *string `json:"description"`
		Status      string  `json:"status"`
		OwnerID     *string `json:"owner_id"`
		TargetDate  *string `json:"target_date"`
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
	var targetDate *time.Time
	if body.TargetDate != nil {
		t, err := time.Parse("2006-01-02", *body.TargetDate)
		if err != nil {
			writeError(w, 400, "target_date must be YYYY-MM-DD", "bad_request")
			return
		}
		targetDate = &t
	}

	pl, err := h.repo.Create(r.Context(), &model.Plan{
		Title:       body.Title,
		Description: body.Description,
		Status:      status,
		OwnerID:     body.OwnerID,
		TargetDate:  targetDate,
	}, userIDFromCtx(r))
	if err != nil {
		writeError(w, 500, "create error", "internal_error")
		return
	}
	writeJSON(w, 201, pl)
}

func (h *PlansHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	pl, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeError(w, 404, "plan not found", "not_found")
		return
	}
	writeJSON(w, 200, pl)
}

func (h *PlansHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	allowed := map[string]bool{"title": true, "description": true, "status": true, "owner_id": true, "target_date": true}
	fields := map[string]any{}
	for k, v := range body {
		if allowed[k] {
			fields[k] = v
		}
	}
	pl, err := h.repo.Update(r.Context(), id, fields)
	if err != nil {
		writeError(w, 500, "update error", "internal_error")
		return
	}
	writeJSON(w, 200, pl)
}

func (h *PlansHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeError(w, 500, "delete error", "internal_error")
		return
	}
	w.WriteHeader(204)
}

func (h *PlansHandler) CreateDecision(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
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
		writeError(w, 500, "create error", "internal_error")
		return
	}
	writeJSON(w, 201, d)
}

func (h *PlansHandler) UpdateDecision(w http.ResponseWriter, r *http.Request) {
	did := chi.URLParam(r, "did")
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
		writeError(w, 500, "update error", "internal_error")
		return
	}
	writeJSON(w, 200, d)
}

func (h *PlansHandler) DeleteDecision(w http.ResponseWriter, r *http.Request) {
	did := chi.URLParam(r, "did")
	if err := h.repo.DeleteDecision(r.Context(), did); err != nil {
		writeError(w, 500, "delete error", "internal_error")
		return
	}
	w.WriteHeader(204)
}
