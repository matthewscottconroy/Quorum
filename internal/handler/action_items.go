package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

var validActionItemStatuses = map[string]bool{
	"open": true, "in_progress": true, "done": true, "cancelled": true,
}

var validCardTypes = map[string]bool{"epic": true, "story": true, "task": true, "sub_task": true, "spike": true}

var validActionItemPriorities = map[string]bool{
	"low": true, "normal": true, "high": true,
}

// ActionItemsHandler handles action item CRUD endpoints.
type ActionItemsHandler struct {
	repo     actionItemsRepo
	notifier deletionNotifier
	events   eventNotifier
}

// NewActionItemsHandler constructs an ActionItemsHandler.
func NewActionItemsHandler(r actionItemsRepo) *ActionItemsHandler {
	return &ActionItemsHandler{repo: r}
}

// SetEventNotifier attaches the notification service used to alert an assignee
// when an action item is assigned to them. Optional (no-op when unset).
func (h *ActionItemsHandler) SetEventNotifier(n eventNotifier) { h.events = n }

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
	// sprint_id additionally accepts the sentinel "none" (the backlog).
	if v := q.Get("sprint_id"); v != "" && v != "none" && !isValidUUID(v) {
		writeError(w, 400, "sprint_id must be a UUID or \"none\"", "bad_request")
		return
	}
	f := repo.ActionItemFilter{
		AssigneeID: q.Get("assignee_id"),
		MeetingID:  q.Get("meeting_id"),
		PlanID:     q.Get("plan_id"),
		Status:     q.Get("status"),
		SprintID:   q.Get("sprint_id"),
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
	writePage(w, items, total, f.Limit, f.Offset)
}

// Create handles creating a action item.
func (h *ActionItemsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title       string  `json:"title"`
		Description *string `json:"description"`
		AssigneeID  *string `json:"assignee_id"`
		MeetingID   *string `json:"meeting_id"`
		PlanID      *string `json:"plan_id"`
		SprintID    *string `json:"sprint_id"`
		DueDate     *string `json:"due_date"`
		Priority    string  `json:"priority"`
		StoryPoints *int    `json:"story_points"`
		CardType    string  `json:"card_type"`
		ParentID    *string `json:"parent_id"`
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
	if body.CardType == "" {
		body.CardType = "task"
	}
	if !validCardTypes[body.CardType] {
		writeError(w, 400, "card_type must be one of epic, story, task, sub_task, spike", "bad_request")
		return
	}
	if body.StoryPoints != nil && (*body.StoryPoints < 0 || *body.StoryPoints > 100) {
		writeError(w, 400, "story_points must be between 0 and 100", "bad_request")
		return
	}
	for name, v := range map[string]*string{
		"assignee_id": body.AssigneeID, "meeting_id": body.MeetingID, "plan_id": body.PlanID,
		"sprint_id": body.SprintID, "parent_id": body.ParentID,
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
		SprintID:    body.SprintID,
		Status:      "open",
		Priority:    priority,
		StoryPoints: body.StoryPoints,
		CardType:    body.CardType,
		ParentID:    body.ParentID,
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
	// Notify the assignee's linked user account, if any.
	if h.events != nil && created.AssigneeID != nil {
		link := "#/dashboard"
		body := "You have been assigned a new action item in Quorum."
		h.events.NotifyMember(*created.AssigneeID, "action_item.assigned", "Assigned: "+created.Title, &body, &link)
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
	if t, present := body["title"]; present {
		s, ok := t.(string)
		if !ok || strings.TrimSpace(s) == "" {
			writeError(w, 400, "title must be a non-empty string", "bad_request")
			return
		}
	}
	for _, p := range []string{"assignee_id", "meeting_id", "plan_id", "sprint_id", "parent_id", "column_id"} {
		if v, present := body[p]; present && v != nil {
			s, ok := v.(string)
			if !ok || !isValidUUID(s) {
				writeError(w, 400, p+" must be a UUID", "bad_request")
				return
			}
		}
	}
	if v, present := body["card_type"]; present {
		s, ok := v.(string)
		if !ok || !validCardTypes[s] {
			writeError(w, 400, "card_type must be one of epic, story, task, sub_task, spike", "bad_request")
			return
		}
	}
	if v, present := body["story_points"]; present && v != nil {
		f, ok := v.(float64)
		if !ok || f != float64(int(f)) || f < 0 || f > 100 {
			writeError(w, 400, "story_points must be an integer between 0 and 100", "bad_request")
			return
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
// SetContributors replaces a card's contributor roster (officer+).
func (h *ActionItemsHandler) SetContributors(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		MemberIDs []string `json:"member_ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	for _, mid := range body.MemberIDs {
		if !isValidUUID(mid) {
			writeError(w, 400, "member_ids must be UUIDs", "bad_request")
			return
		}
	}
	// Snapshot the prior roster so only newly-added contributors are notified,
	// not everyone on every edit.
	prior := map[string]bool{}
	if before, err := h.repo.Get(r.Context(), id); err == nil {
		for _, c := range before.Contributors {
			prior[c.MemberID] = true
		}
	}
	item, err := h.repo.SetContributors(r.Context(), id, body.MemberIDs)
	if err != nil {
		writeRepoError(w, err, "action item or member not found", "update error")
		return
	}
	if h.events != nil {
		link := "#/board"
		body := "You have been added as a contributor to a card in Quorum."
		for _, c := range item.Contributors {
			if !prior[c.MemberID] {
				h.events.NotifyMember(c.MemberID, "action_item.contributor_added", "Added to: "+item.Title, &body, &link)
			}
		}
	}
	setAuditDetail(r, map[string]any{"title": item.Title, "contributors": len(body.MemberIDs)})
	writeJSON(w, 200, item)
}

func (h *ActionItemsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	crudDelete(w, r, deleteSpec[model.ActionItem]{
		entity:      "action item",
		notFoundMsg: "action item not found",
		get:         h.repo.Get,
		name:        func(item *model.ActionItem) string { return item.Title },
		del:         h.repo.Delete,
		notifier:    h.notifier,
		affected:    singleEmail(h.repo.AssigneeEmail),
	})
}
