package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// MeetingsHandler handles meeting, attendance, and decision endpoints.
type MeetingsHandler struct {
	repo     meetingsRepo
	notifier deletionNotifier
	events   eventNotifier
	gov      minutesGovSource
	audit    auditRepo
	// tzSource returns the org's display timezone (falls back to server-local).
	tzSource func(ctx context.Context) *time.Location
}

// SetTimezoneSource attaches the org-timezone lookup used wherever times are
// rendered server-side (minutes documents, notification emails).
func (h *MeetingsHandler) SetTimezoneSource(fn func(ctx context.Context) *time.Location) {
	h.tzSource = fn
}

// location resolves the org's display timezone, defaulting to server-local.
func (h *MeetingsHandler) location(ctx context.Context) *time.Location {
	if h.tzSource != nil {
		if loc := h.tzSource(ctx); loc != nil {
			return loc
		}
	}
	return time.Local
}

// SetAuditLogger attaches the audit log for export recording (.ics, minutes).
func (h *MeetingsHandler) SetAuditLogger(a auditRepo) { h.audit = a }

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
		Query:    strings.TrimSpace(q.Get("q")),
		Limit:    100,
	}
	// Truncate in RUNES: a byte slice can bisect a multibyte character and
	// hand Postgres invalid UTF-8.
	if runes := []rune(f.Query); len(runes) > 200 {
		f.Query = string(runes[:200])
	}
	// from/to (YYYY-MM-DD) bound scheduled_at for the calendar view: from is
	// inclusive, to is exclusive-end-of-day (i.e. includes the whole `to` day).
	if v := q.Get("from"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			writeError(w, 400, "from must be YYYY-MM-DD", "bad_request")
			return
		}
		f.From = &t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			writeError(w, 400, "to must be YYYY-MM-DD", "bad_request")
			return
		}
		end := t.Add(24 * time.Hour)
		f.To = &end
	}
	// The calendar grid fetches a whole 6-week window at once, so this list
	// accepts up to 500 (the repo's own cap) rather than the standard page
	// cap — a silent fall-back to 100 made the OLDEST meetings in a busy
	// window vanish from the grid with no error.
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 500 {
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
		EndsAt      *string `json:"ends_at"`
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
	var endsAt *time.Time
	if body.EndsAt != nil && *body.EndsAt != "" {
		t, err := time.Parse(time.RFC3339, *body.EndsAt)
		if err != nil {
			writeError(w, 400, "ends_at must be RFC3339", "bad_request")
			return
		}
		if !t.After(scheduledAt) {
			writeError(w, 400, "ends_at must be after scheduled_at", "bad_request")
			return
		}
		endsAt = &t
	}

	mt, err := h.repo.Create(r.Context(), &model.Meeting{
		Title:       body.Title,
		ScheduledAt: scheduledAt,
		EndsAt:      endsAt,
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
		body := "A new meeting has been scheduled for " + mt.ScheduledAt.In(h.location(r.Context())).Format("Mon 2 Jan 2006, 15:04 MST") + "."
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
		// Tri-state: absent = unchanged, null = clear, string = set.
		EndsAt   json.RawMessage `json:"ends_at"`
		Location *string         `json:"location"`
		Agenda   *string         `json:"agenda"`
		Notes    *string         `json:"notes"`
		Status   *string         `json:"status"`
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
	var endsAt *time.Time
	clearEndsAt := false
	if len(body.EndsAt) > 0 {
		if string(body.EndsAt) == "null" {
			clearEndsAt = true
		} else {
			var raw string
			if err := json.Unmarshal(body.EndsAt, &raw); err != nil {
				writeError(w, 400, "ends_at must be an RFC3339 string or null", "bad_request")
				return
			}
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				writeError(w, 400, "ends_at must be RFC3339", "bad_request")
				return
			}
			// Cross-validate here when the request carries both times; when only
			// one side changes, the database CHECK is the final arbiter below.
			if scheduledAt != nil && !t.After(*scheduledAt) {
				writeError(w, 400, "ends_at must be after scheduled_at", "bad_request")
				return
			}
			endsAt = &t
		}
	}
	if body.Status != nil {
		s := *body.Status
		if s != "scheduled" && s != "completed" && s != "cancelled" {
			writeError(w, 400, "invalid status: must be scheduled, completed, or cancelled", "bad_request")
			return
		}
	}

	mt, err := h.repo.Update(r.Context(), id,
		body.Title, scheduledAt, endsAt, clearEndsAt, body.Location, body.Agenda, body.Notes, body.Status)
	if err != nil {
		if isConstraint(err, "meetings_ends_after_start") {
			writeError(w, 400, "ends_at must be after scheduled_at", "bad_request")
			return
		}
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
	affected, err := h.repo.AttendeeEmails(r.Context(), id)
	if err != nil {
		log.Printf("attendee emails for meeting %s: %v (deletion notices skipped)", id, err)
	}
	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeRepoError(w, err, "meeting not found", "delete error")
		return
	}
	if h.notifier != nil {
		h.notifier.NotifyDeletion(r.Context(), userIDFromCtx(r), "meeting", mt.Title, affected)
	}
	w.WriteHeader(204)
}

// GetRSVP returns the RSVP tally for a meeting plus the caller's own response.
func (h *MeetingsHandler) GetRSVP(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	s, err := h.repo.RSVPSummary(r.Context(), id, memberIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// SetRSVP records the caller's RSVP (member+; requires a linked member record).
func (h *MeetingsHandler) SetRSVP(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	memberID := memberIDFromCtx(r)
	if memberID == "" {
		writeError(w, http.StatusForbidden, "your login isn't linked to a member record", "forbidden")
		return
	}
	var body struct {
		Response string `json:"response"`
	}
	if err := decodeJSON(r, &body); err != nil ||
		(body.Response != "yes" && body.Response != "no" && body.Response != "maybe") {
		writeError(w, http.StatusBadRequest, "response must be yes, no, or maybe", "bad_request")
		return
	}
	// RSVPs are pre-meeting intent: once the meeting is over (finalized
	// minutes) or cancelled, the historical tally must stop drifting.
	if mt, err := h.repo.Get(r.Context(), id); err == nil {
		if mt.MinutesFinalizedAt != nil || mt.Status == "cancelled" || mt.Status == "completed" {
			writeError(w, http.StatusConflict, "this meeting is over — RSVPs are closed", "conflict")
			return
		}
	}
	if err := h.repo.SetRSVP(r.Context(), id, memberID, body.Response); err != nil {
		writeRepoError(w, err, "meeting not found", "save error")
		return
	}
	s, _ := h.repo.RSVPSummary(r.Context(), id, memberID)
	writeJSON(w, http.StatusOK, s)
}

// RSVPNamesHandler returns per-response name lists (officer+).
func (h *MeetingsHandler) RSVPNamesHandler(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	names, err := h.repo.RSVPNames(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, names)
}

// RSVPYes returns the member IDs who said yes, to seed the roster (officer+).
func (h *MeetingsHandler) RSVPYes(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	ids, err := h.repo.RSVPYesMemberIDs(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"member_ids": ids})
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
		// A missing meeting/member (FK) or a duplicated member in the payload
		// (PK) is the caller's mistake, not a server fault.
		writeRepoError(w, err, "meeting not found", "update error")
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

// UpdateDecision edits an existing decision. The decision must belong to the
// meeting in the path: /meetings/A/decisions/{did-of-B} is a 404, not a
// cross-meeting edit with a lying audit trail.
func (h *MeetingsHandler) UpdateDecision(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
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
	d, err := h.repo.UpdateDecision(r.Context(), id, did,
		body.Summary, body.Detail, body.Outcome,
		body.VoteFor, body.VoteAgainst, body.VoteAbstain)
	if err != nil {
		writeRepoError(w, err, "decision not found", "update error")
		return
	}
	// Decisions are the minutes: record WHAT was changed in the chain-protected
	// audit detail, so a correction can never silently become a rewrite.
	changed := map[string]any{}
	for k, v := range map[string]*string{"summary": body.Summary, "detail": body.Detail, "outcome": body.Outcome} {
		if v != nil {
			changed[k] = *v
		}
	}
	for k, v := range map[string]*int{"vote_for": body.VoteFor, "vote_against": body.VoteAgainst, "vote_abstain": body.VoteAbstain} {
		if v != nil {
			changed[k] = *v
		}
	}
	if len(changed) > 0 {
		setAuditDetail(r, map[string]any{"set": changed})
	}
	writeJSON(w, 200, d)
}

// DeleteDecision removes a decision (scoped to the meeting in the path).
func (h *MeetingsHandler) DeleteDecision(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	did, ok := requireUUID(w, r, "did")
	if !ok {
		return
	}
	if err := h.repo.DeleteDecision(r.Context(), id, did); err != nil {
		writeRepoError(w, err, "decision not found", "delete error")
		return
	}
	w.WriteHeader(204)
}
