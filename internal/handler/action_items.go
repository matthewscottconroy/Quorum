package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// ActionItemsHandler handles action item CRUD endpoints.
type ActionItemsHandler struct {
	repo actionItemsRepo
}

// NewActionItemsHandler constructs an ActionItemsHandler.
func NewActionItemsHandler(r actionItemsRepo) *ActionItemsHandler {
	return &ActionItemsHandler{repo: r}
}

func (h *ActionItemsHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := repo.ActionItemFilter{
		AssigneeID: q.Get("assignee_id"),
		MeetingID:  q.Get("meeting_id"),
		PlanID:     q.Get("plan_id"),
		Status:     q.Get("status"),
		Limit:      100,
	}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		f.Limit = v
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		f.Offset = v
	}
	items, total, err := h.repo.List(r.Context(), f)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	if items == nil {
		items = []model.ActionItem{}
	}
	writeJSON(w, 200, model.Page[model.ActionItem]{Data: items, Total: total, Limit: f.Limit, Offset: f.Offset})
}

func (h *ActionItemsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title       string  `json:"title"`
		Description *string `json:"description"`
		AssigneeID  *string `json:"assignee_id"`
		MeetingID   *string `json:"meeting_id"`
		PlanID      *string `json:"plan_id"`
		DueDate     *string `json:"due_date"`
		Priority    string  `json:"priority"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	if body.Title == "" {
		writeError(w, 400, "title required", "bad_request")
		return
	}
	priority := body.Priority
	if priority == "" {
		priority = "normal"
	}

	item := &model.ActionItem{
		Title:       body.Title,
		Description: body.Description,
		AssigneeID:  body.AssigneeID,
		MeetingID:   body.MeetingID,
		PlanID:      body.PlanID,
		Status:      "open",
		Priority:    priority,
	}
	if body.DueDate != nil {
		t, err := time.Parse("2006-01-02", *body.DueDate)
		if err != nil {
			writeError(w, 400, "due_date must be YYYY-MM-DD", "bad_request")
			return
		}
		item.DueDate = &t
	}

	created, err := h.repo.Create(r.Context(), item, userIDFromCtx(r))
	if err != nil {
		writeError(w, 500, "create error", "internal_error")
		return
	}
	writeJSON(w, 201, created)
}

func (h *ActionItemsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	updated, err := h.repo.Update(r.Context(), id, body)
	if err != nil {
		writeError(w, 500, "update error", "internal_error")
		return
	}
	writeJSON(w, 200, updated)
}

func (h *ActionItemsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeError(w, 500, "delete error", "internal_error")
		return
	}
	w.WriteHeader(204)
}
