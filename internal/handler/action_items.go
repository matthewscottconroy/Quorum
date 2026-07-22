package handler

import (
	"net/http"
	"strconv"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

var validActionItemStatuses = map[string]bool{
	"open": true, "in_progress": true, "done": true, "cancelled": true,
}

var validActionItemPriorities = map[string]bool{
	"low": true, "normal": true, "high": true,
}

// ActionItemsHandler handles action item CRUD endpoints.
type ActionItemsHandler struct {
	repo     actionItemsRepo
	notifier deletionNotifier
}

// NewActionItemsHandler constructs an ActionItemsHandler.
func NewActionItemsHandler(r actionItemsRepo) *ActionItemsHandler {
	return &ActionItemsHandler{repo: r}
}

// SetNotifier attaches an optional notifier used on gated deletes.
func (h *ActionItemsHandler) SetNotifier(n deletionNotifier) { h.notifier = n }

// List handles GET requests for a paginated, filterable list of action items.
func (h *ActionItemsHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// UUID filters are validated up front so garbage input gets a 400 instead
	// of failing the ::uuid cast in Postgres and surfacing as a 500.
	for _, p := range []string{"assignee_id", "meeting_id", "plan_id"} {
		if v := q.Get(p); v != "" && !isValidUUID(v) {
			writeError(w, 400, p+" must be a UUID", "bad_request")
			return
		}
	}
	f := repo.ActionItemFilter{
		AssigneeID: q.Get("assignee_id"),
		MeetingID:  q.Get("meeting_id"),
		PlanID:     q.Get("plan_id"),
		Status:     q.Get("status"),
		Limit:      clampLimit(q.Get("limit"), 100),
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

// Create handles creating a action item.
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
	if !validActionItemPriorities[priority] {
		writeError(w, 400, "invalid priority: must be low, normal, or high", "bad_request")
		return
	}
	for name, v := range map[string]*string{
		"assignee_id": body.AssigneeID, "meeting_id": body.MeetingID, "plan_id": body.PlanID,
	} {
		if v != nil && !isValidUUID(*v) {
			writeError(w, 400, name+" must be a UUID", "bad_request")
			return
		}
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
		writeRepoError(w, err, "", "create error")
		return
	}
	writeJSON(w, 201, created)
}

// Update handles updating a action item.
func (h *ActionItemsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	if len(body) == 0 {
		writeError(w, 400, "no fields provided", "bad_request")
		return
	}
	if status, present := body["status"]; present {
		s, ok := status.(string)
		if !ok || !validActionItemStatuses[s] {
			writeError(w, 400, "invalid status: must be open, in_progress, done, or cancelled", "bad_request")
			return
		}
	}
	if priority, present := body["priority"]; present {
		s, ok := priority.(string)
		if !ok || !validActionItemPriorities[s] {
			writeError(w, 400, "invalid priority: must be low, normal, or high", "bad_request")
			return
		}
	}
	for _, p := range []string{"assignee_id", "meeting_id", "plan_id"} {
		if v, present := body[p]; present && v != nil {
			s, ok := v.(string)
			if !ok || !isValidUUID(s) {
				writeError(w, 400, p+" must be a UUID", "bad_request")
				return
			}
		}
	}
	if v, present := body["due_date"]; present && v != nil {
		s, ok := v.(string)
		if !ok {
			writeError(w, 400, "due_date must be YYYY-MM-DD", "bad_request")
			return
		}
		if _, err := time.Parse("2006-01-02", s); err != nil {
			writeError(w, 400, "due_date must be YYYY-MM-DD", "bad_request")
			return
		}
	}
	updated, err := h.repo.Update(r.Context(), id, body)
	if err != nil {
		writeRepoError(w, err, "action item not found", "update error")
		return
	}
	writeJSON(w, 200, updated)
}

// Delete handles deleting a action item.
func (h *ActionItemsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	item, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "action item not found", "query error")
		return
	}
	if !confirmMatches(w, r, item.Title) {
		return
	}
	var affected []string
	if email, _ := h.repo.AssigneeEmail(r.Context(), id); email != "" {
		affected = []string{email}
	}
	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeRepoError(w, err, "action item not found", "delete error")
		return
	}
	if h.notifier != nil {
		h.notifier.NotifyDeletion(r.Context(), userIDFromCtx(r), "action item", item.Title, affected)
	}
	w.WriteHeader(204)
}
