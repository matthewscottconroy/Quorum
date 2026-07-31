package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// Recording-secretary endpoints (Robert's Rules): a chronological journal per
// meeting plus a generated minutes document that weaves the journal together
// with attendance, motions (mover/second/discussion/votes/result), and
// recorded decisions. Once finalized, minutes are the official record and the
// database refuses further changes.

// minutesGovSource is the slice of governance data the minutes document needs.
// *repo.GovernanceRepo satisfies it.
type minutesGovSource interface {
	ListMotions(ctx context.Context, meetingID string) ([]model.Motion, error)
	GetVotes(ctx context.Context, motionID string) ([]model.MotionVote, error)
}

// SetGovernanceSource attaches the motion/vote reader used by the minutes
// document. Optional: without it the document omits the motions section.
func (h *MeetingsHandler) SetGovernanceSource(g minutesGovSource) { h.gov = g }

// ListMinutes returns the meeting's journal (member+).
func (h *MeetingsHandler) ListMinutes(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	entries, err := h.repo.ListMinutes(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// minutesEntryBody is the create/update payload for one journal line.
type minutesEntryBody struct {
	Kind     string  `json:"kind"`
	Body     string  `json:"body"`
	MotionID *string `json:"motion_id"`
}

func (b *minutesEntryBody) validate(w http.ResponseWriter) bool {
	if !model.ValidMinutesKinds[b.Kind] {
		writeError(w, http.StatusBadRequest, "invalid kind", "bad_request")
		return false
	}
	if strings.TrimSpace(b.Body) == "" {
		writeError(w, http.StatusBadRequest, "body is required", "bad_request")
		return false
	}
	if b.MotionID != nil && !isValidUUID(*b.MotionID) {
		writeError(w, http.StatusBadRequest, "motion_id must be a UUID", "bad_request")
		return false
	}
	return true
}

// AddMinutesEntry appends a journal line (officer+).
func (h *MeetingsHandler) AddMinutesEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body minutesEntryBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if !body.validate(w) {
		return
	}
	e, err := h.repo.AddMinutesEntry(r.Context(), id, body.Kind, strings.TrimSpace(body.Body), body.MotionID, userIDFromCtx(r))
	if err != nil {
		writeRepoError(w, err, "meeting not found", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"minutes_kind": body.Kind})
	writeJSON(w, http.StatusCreated, e)
}

// UpdateMinutesEntry corrects a journal line before finalization (officer+).
func (h *MeetingsHandler) UpdateMinutesEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	eid, ok := requireUUID(w, r, "eid")
	if !ok {
		return
	}
	var body minutesEntryBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if !body.validate(w) {
		return
	}
	e, err := h.repo.UpdateMinutesEntry(r.Context(), id, eid, body.Kind, strings.TrimSpace(body.Body), body.MotionID)
	if err != nil {
		writeRepoError(w, err, "minutes entry not found", "update error")
		return
	}
	setAuditDetail(r, map[string]any{"minutes_kind": body.Kind})
	writeJSON(w, http.StatusOK, e)
}

// DeleteMinutesEntry removes a journal line before finalization (officer+).
func (h *MeetingsHandler) DeleteMinutesEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	eid, ok := requireUUID(w, r, "eid")
	if !ok {
		return
	}
	if err := h.repo.DeleteMinutesEntry(r.Context(), id, eid); err != nil {
		writeRepoError(w, err, "minutes entry not found", "delete error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// FinalizeMinutes marks the minutes approved (officer+). It is irreversible —
// the journal locks at the database level — so it is confirm-gated on the
// meeting title like the other one-way actions.
func (h *MeetingsHandler) FinalizeMinutes(w http.ResponseWriter, r *http.Request) {
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
	if err := h.repo.FinalizeMinutes(r.Context(), id, userIDFromCtx(r)); err != nil {
		if errors.Is(err, repo.ErrMinutesFinalized) {
			writeError(w, http.StatusConflict, "minutes are already finalized", "conflict")
			return
		}
		writeRepoError(w, err, "meeting not found", "finalize error")
		return
	}
	setAuditDetail(r, map[string]any{"minutes": "finalized"})
	if h.events != nil {
		link := "#/meetings"
		body := "The approved minutes are available in Quorum."
		h.events.NotifyMembers("meeting.minutes_finalized", "Minutes finalized: "+mt.Title, &body, &link)
	}
	w.WriteHeader(http.StatusNoContent)
}

// MinutesDocument renders the full minutes as a Markdown document (member+):
// the deliverable a recording secretary would produce, generated from what was
// recorded. Draft minutes are watermarked as such.
func (h *MeetingsHandler) MinutesDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	mt, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "meeting not found", "not_found")
		return
	}
	entries, err := h.repo.ListMinutes(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	var motions []model.Motion
	if h.gov != nil {
		motions, err = h.gov.ListMotions(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "query error", "internal_error")
			return
		}
		for i := range motions {
			votes, err := h.gov.GetVotes(r.Context(), motions[i].ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "query error", "internal_error")
				return
			}
			motions[i].Votes = votes
		}
	}
	doc := buildMinutesDocument(mt, entries, motions)
	auditExport(r, h.audit, "meetings/"+id+"/minutes.md", map[string]any{"meeting": mt.Title})
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", "minutes-"+mt.ScheduledAt.Format("2006-01-02")+".md"))
	_, _ = w.Write([]byte(doc))
}

// minutesKindLabels renders journal kinds as document headings.
var minutesKindLabels = map[string]string{
	"call_to_order": "Call to order", "previous_minutes": "Previous minutes",
	"report": "Report", "old_business": "Old business", "new_business": "New business",
	"discussion": "Discussion", "point_of_order": "Point of order",
	"recess": "Recess", "adjournment": "Adjournment", "note": "Note",
}

func buildMinutesDocument(mt *model.Meeting, entries []model.MinutesEntry, motions []model.Motion) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Minutes — %s\n\n", mt.Title)
	if mt.MinutesFinalizedAt == nil {
		b.WriteString("> **DRAFT — these minutes have not been finalized.**\n\n")
	}
	fmt.Fprintf(&b, "- **Date:** %s\n", mt.ScheduledAt.Format("Monday, 2 January 2006, 15:04 MST"))
	if mt.EndsAt != nil {
		fmt.Fprintf(&b, "- **Adjourned/ends:** %s\n", mt.EndsAt.Format("15:04 MST"))
	}
	if mt.Location != nil && *mt.Location != "" {
		fmt.Fprintf(&b, "- **Location:** %s\n", *mt.Location)
	}
	fmt.Fprintf(&b, "- **Status:** %s\n", mt.Status)
	if mt.MinutesFinalizedAt != nil {
		fmt.Fprintf(&b, "- **Minutes finalized:** %s\n", mt.MinutesFinalizedAt.Format("2006-01-02 15:04 MST"))
	}
	b.WriteString("\n")

	// Roll call from recorded attendance.
	if len(mt.Attendees) > 0 {
		b.WriteString("## Attendance\n\n")
		var present, absent []string
		for _, a := range mt.Attendees {
			if a.Present {
				present = append(present, a.MemberName)
			} else {
				absent = append(absent, a.MemberName)
			}
		}
		sort.Strings(present)
		sort.Strings(absent)
		if len(present) > 0 {
			fmt.Fprintf(&b, "**Present (%d):** %s\n\n", len(present), strings.Join(present, ", "))
		}
		if len(absent) > 0 {
			fmt.Fprintf(&b, "**Absent (%d):** %s\n\n", len(absent), strings.Join(absent, ", "))
		}
	}

	if len(entries) > 0 {
		b.WriteString("## Proceedings\n\n")
		for _, e := range entries {
			label := minutesKindLabels[e.Kind]
			fmt.Fprintf(&b, "%d. **%s**", e.Seq, label)
			if e.MotionTitle != nil {
				fmt.Fprintf(&b, " _(re: motion “%s”)_", *e.MotionTitle)
			}
			fmt.Fprintf(&b, " — %s", e.Body)
			fmt.Fprintf(&b, " _[%s]_\n", e.RecordedAt.Format("15:04"))
		}
		b.WriteString("\n")
	}

	if len(motions) > 0 {
		b.WriteString("## Motions\n\n")
		for _, m := range motions {
			biz := "New business"
			if m.Business == "old" {
				biz = "Old business"
			}
			fmt.Fprintf(&b, "### %s\n\n", m.Title)
			fmt.Fprintf(&b, "- **Class:** %s · **Threshold:** %s\n", biz, strings.ReplaceAll(m.Threshold, "_", " "))
			if m.MoverName != nil {
				fmt.Fprintf(&b, "- **Moved by:** %s\n", *m.MoverName)
			}
			if m.SeconderName != nil {
				fmt.Fprintf(&b, "- **Seconded by:** %s\n", *m.SeconderName)
			}
			if m.Detail != nil && *m.Detail != "" {
				fmt.Fprintf(&b, "- **Text:** %s\n", *m.Detail)
			}
			fmt.Fprintf(&b, "- **Result:** **%s**", strings.ToUpper(m.Status))
			if m.Tally.Total > 0 {
				fmt.Fprintf(&b, " — %d for, %d against, %d abstain", m.Tally.For, m.Tally.Against, m.Tally.Abstain)
			}
			b.WriteString("\n")
			if len(m.Votes) > 0 {
				byChoice := map[string][]string{}
				for _, v := range m.Votes {
					name := v.MemberName
					if v.IsProxy {
						name += " (by proxy)"
					}
					byChoice[v.Choice] = append(byChoice[v.Choice], name)
				}
				for _, choice := range []string{"for", "against", "abstain"} {
					if names := byChoice[choice]; len(names) > 0 {
						sort.Strings(names)
						fmt.Fprintf(&b, "  - _%s:_ %s\n", choice, strings.Join(names, ", "))
					}
				}
			}
			b.WriteString("\n")
		}
	}

	if len(mt.Decisions) > 0 {
		b.WriteString("## Decisions recorded\n\n")
		for _, d := range mt.Decisions {
			fmt.Fprintf(&b, "- **%s** — %s", strings.ToUpper(d.Outcome), d.Summary)
			if d.VoteFor != nil || d.VoteAgainst != nil || d.VoteAbstain != nil {
				deref := func(p *int) int {
					if p == nil {
						return 0
					}
					return *p
				}
				fmt.Fprintf(&b, " (%d for, %d against, %d abstain)",
					deref(d.VoteFor), deref(d.VoteAgainst), deref(d.VoteAbstain))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "---\n_Generated by Quorum on %s._\n", time.Now().Format("2006-01-02 15:04 MST"))
	return b.String()
}
