package handler

import (
	"net/http"
	"strconv"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// MeetingsHandler handles meeting, attendance, and decision endpoints.
type MeetingsHandler struct {
	repo     meetingsRepo
	notifier deletionNotifier
	events   eventNotifier
}

// NewMeetingsHandler constructs a MeetingsHandler.
func NewMeetingsHandler(r meetingsRepo) *MeetingsHandler {
	return &MeetingsHandler{repo: r}
}

// SetNotifier attaches an optional notifier used on gated deletes.
func (h *MeetingsHandler) SetNotifier(n deletionNotifier) { h.notifier = n }

// SetEventNotifier attaches the notification service used to alert members when
// a meeting is scheduled. Optional (no-op when unset).
func (h *MeetingsHandler) SetEventNotifier(n eventNotifier) { h.events = n }

// List handles GET requests for a paginated, filterable list of meetings.
func (h *MeetingsHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := repo.MeetingFilter{
		Upcoming: q.Get("upcoming") == "true",
		Limit:    100,
	}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= maxPageSize {
		f.Limit = v
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		f.Offset = v
	}
	meetings, total, err := h.repo.List(r.Context(), f)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	writePage(w, meetings, total, f.Limit, f.Offset)
}

// Create handles creating a meeting.
func (h *MeetingsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title       string  `json:"title"`
		ScheduledAt string  `json:"scheduled_at"`
		Location    *string `json:"location"`
		Agenda      *string `json:"agenda"`
		Notes       *string `json:"notes"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	if body.Title == "" || body.ScheduledAt == "" {
		writeError(w, 400, "title and scheduled_at required", "bad_request")
		return
	}
	scheduledAt, err := time.Parse(time.RFC3339, body.ScheduledAt)
	if err != nil {
		writeError(w, 400, "scheduled_at must be RFC3339", "bad_request")
		return
	}

	mt, err := h.repo.Create(r.Context(), &model.Meeting{
		Title:       body.Title,
		ScheduledAt: scheduledAt,
		Location:    body.Location,
		Agenda:      body.Agenda,
		Notes:       body.Notes,
		Status:      "scheduled",
	}, userIDFromCtx(r))
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}
	if h.events != nil {
		link := "#/meetings"
		body := "A new meeting has been scheduled for " + mt.ScheduledAt.Format("Mon 2 Jan 2006, 15:04 MST") + "."
		h.events.NotifyMembers("meeting.scheduled", "Meeting scheduled: "+mt.Title, &body, &link)
	}
	writeJSON(w, 201, mt)
}

// Get handles fetching a single meeting by id.
func (h *MeetingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	genericGet(w, r, h.repo.Get, "meeting not found")
}

// Update handles updating a meeting.
func (h *MeetingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Title       *string `json:"title"`
		ScheduledAt *string `json:"scheduled_at"`
		Location    *string `json:"location"`
		Agenda      *string `json:"agenda"`
		Notes       *string `json:"notes"`
		Status      *string `json:"status"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}

	var scheduledAt *time.Time
	if body.ScheduledAt != nil {
		t, err := time.Parse(time.RFC3339, *body.ScheduledAt)
		if err != nil {
			writeError(w, 400, "scheduled_at must be RFC3339", "bad_request")
			return
		}
		scheduledAt = &t
	}
	if body.Status != nil {
		s := *body.Status
		if s != "scheduled" && s != "completed" && s != "cancelled" {
			writeError(w, 400, "invalid status: must be scheduled, completed, or cancelled", "bad_request")
			return
		}
	}

	mt, err := h.repo.Update(r.Context(), id,
		body.Title, scheduledAt, body.Location, body.Agenda, body.Notes, body.Status)
	if err != nil {
		writeRepoError(w, err, "meeting not found", "update error")
		return
	}
	writeJSON(w, 200, mt)
}

// Delete handles deleting a meeting.
func (h *MeetingsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	mt, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "meeting not found", "query error")
		return
	}
	if !confirmMatches(w, r, mt.Title) {
		return
	}
	// A meeting that recorded decisions or decided motions carries governance
	// history that a hard delete would erase (it cascades to motions, votes, and
	// decisions). Refuse — the meeting can be cancelled instead.
	if hist, err := h.repo.HasGovernanceHistory(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	} else if hist {
		writeError(w, http.StatusConflict,
			"this meeting has recorded decisions or votes and cannot be deleted; set its status to cancelled instead",
			"conflict")
		return
	}
	// Gather affected members' emails before deleting — the attendee rows
	// cascade away with the meeting.
	affected, _ := h.repo.AttendeeEmails(r.Context(), id)
	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeRepoError(w, err, "meeting not found", "delete error")
		return
	}
	if h.notifier != nil {
		h.notifier.NotifyDeletion(r.Context(), userIDFromCtx(r), "meeting", mt.Title, affected)
	}
	w.WriteHeader(204)
}

// SetAttendees replaces a meeting's attendance roster.
func (h *MeetingsHandler) SetAttendees(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Attendees []model.MeetingAttendee `json:"attendees"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	if err := h.repo.SetAttendees(r.Context(), id, body.Attendees); err != nil {
		writeError(w, 500, "update error", "internal_error")
		return
	}
	attendees, err := h.repo.GetAttendees(r.Context(), id)
	if err != nil {
		writeError(w, 500, "fetch error", "internal_error")
		return
	}
	if attendees == nil {
		attendees = []model.MeetingAttendee{}
	}
	writeJSON(w, 200, attendees)
}

// CreateDecision records a decision.
func (h *MeetingsHandler) CreateDecision(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Summary     string  `json:"summary"`
		Detail      *string `json:"detail"`
		VoteFor     *int    `json:"vote_for"`
		VoteAgainst *int    `json:"vote_against"`
		VoteAbstain *int    `json:"vote_abstain"`
		Outcome     string  `json:"outcome"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	if body.Summary == "" {
		writeError(w, 400, "summary required", "bad_request")
		return
	}
	outcome := body.Outcome
	if outcome == "" {
		outcome = "passed"
	}
	d, err := h.repo.CreateDecision(r.Context(), &model.MeetingDecision{
		MeetingID:   id,
		Summary:     body.Summary,
		Detail:      body.Detail,
		VoteFor:     body.VoteFor,
		VoteAgainst: body.VoteAgainst,
		VoteAbstain: body.VoteAbstain,
		Outcome:     outcome,
	})
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}
	writeJSON(w, 201, d)
}

// UpdateDecision edits an existing decision.
func (h *MeetingsHandler) UpdateDecision(w http.ResponseWriter, r *http.Request) {
	did, ok := requireUUID(w, r, "did")
	if !ok {
		return
	}
	var body struct {
		Summary     *string `json:"summary"`
		Detail      *string `json:"detail"`
		VoteFor     *int    `json:"vote_for"`
		VoteAgainst *int    `json:"vote_against"`
		VoteAbstain *int    `json:"vote_abstain"`
		Outcome     *string `json:"outcome"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	d, err := h.repo.UpdateDecision(r.Context(), did,
		body.Summary, body.Detail, body.Outcome,
		body.VoteFor, body.VoteAgainst, body.VoteAbstain)
	if err != nil {
		writeRepoError(w, err, "decision not found", "update error")
		return
	}
	writeJSON(w, 200, d)
}

// DeleteDecision removes a decision.
func (h *MeetingsHandler) DeleteDecision(w http.ResponseWriter, r *http.Request) {
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
