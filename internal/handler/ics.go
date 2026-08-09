package handler

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// iCalendar (RFC 5545) export for meetings, so scheduling lands in the
// calendars people actually live in (Google/Outlook/Apple). Two shapes:
//
//   GET /export/meetings.ics — every meeting from 30 days back onward, for a
//                              one-shot import of the org's schedule;
//   GET /meetings/{id}/ics   — a single event ("Add to calendar" button).
//
// A meeting with an end time exports DTEND; one without falls back to
// DURATION:PT1H as a sane calendar-block default.

// icsEscape escapes text per RFC 5545 §3.3.11 (backslash, semicolon, comma,
// newline).
func icsEscape(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		";", `\;`,
		",", `\,`,
		"\r\n", `\n`,
		"\n", `\n`,
	).Replace(s)
}

// icsFold writes one content line folded at 75 octets with CRLF line endings,
// per RFC 5545 §3.1. Folding is byte-based but never splits a UTF-8 rune.
func icsFold(b *strings.Builder, line string) {
	const width = 75
	for len(line) > width {
		cut := width
		// Back up to a rune boundary so multi-byte characters survive folding.
		for cut > 0 && line[cut]&0xC0 == 0x80 {
			cut--
		}
		b.WriteString(line[:cut])
		b.WriteString("\r\n ") // continuation lines start with a single space
		line = line[cut:]
	}
	b.WriteString(line)
	b.WriteString("\r\n")
}

// icsTime renders a time in UTC basic format (20260901T180000Z).
func icsTime(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// writeICS renders a complete VCALENDAR for the given meetings.
func writeICS(w http.ResponseWriter, meetings []model.Meeting, filename string) {
	var b strings.Builder
	icsFold(&b, "BEGIN:VCALENDAR")
	icsFold(&b, "VERSION:2.0")
	icsFold(&b, "PRODID:-//Quorum//Meetings//EN")
	icsFold(&b, "CALSCALE:GREGORIAN")
	icsFold(&b, "METHOD:PUBLISH")
	icsFold(&b, "X-WR-CALNAME:Quorum meetings")
	now := icsTime(time.Now())
	for _, m := range meetings {
		icsFold(&b, "BEGIN:VEVENT")
		icsFold(&b, "UID:"+m.ID+"@quorum")
		icsFold(&b, "DTSTAMP:"+now)
		icsFold(&b, "DTSTART:"+icsTime(m.ScheduledAt))
		if m.EndsAt != nil {
			icsFold(&b, "DTEND:"+icsTime(*m.EndsAt))
		} else {
			icsFold(&b, "DURATION:PT1H")
		}
		icsFold(&b, "SUMMARY:"+icsEscape(m.Title))
		if m.Location != nil && *m.Location != "" {
			icsFold(&b, "LOCATION:"+icsEscape(*m.Location))
		}
		if m.Agenda != nil && *m.Agenda != "" {
			icsFold(&b, "DESCRIPTION:"+icsEscape(*m.Agenda))
		}
		// Calendar apps grey out cancelled events instead of hiding them —
		// exactly the right rendering for a cancelled meeting.
		if m.Status == "cancelled" {
			icsFold(&b, "STATUS:CANCELLED")
		} else {
			icsFold(&b, "STATUS:CONFIRMED")
		}
		icsFold(&b, "END:VEVENT")
	}
	icsFold(&b, "END:VCALENDAR")

	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	_, _ = w.Write([]byte(b.String()))
}

// writeICSInline renders a VCALENDAR for live subscription feeds: same body
// as writeICS but Content-Disposition inline, so a subscribing calendar app
// renders it instead of downloading a file.
func writeICSInline(w http.ResponseWriter, meetings []model.Meeting) {
	rec := &icsRecorder{}
	writeICS(rec, meetings, "")
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline")
	_, _ = w.Write(rec.body)
}

// icsRecorder captures writeICS output (which writes Content-Disposition:
// attachment) so writeICSInline can re-send it inline. Only the body matters.
type icsRecorder struct {
	body   []byte
	header http.Header
}

func (r *icsRecorder) Header() http.Header {
	if r.header == nil {
		r.header = http.Header{}
	}
	return r.header
}
func (r *icsRecorder) Write(b []byte) (int, error) { r.body = append(r.body, b...); return len(b), nil }
func (r *icsRecorder) WriteHeader(int)             {}

// ExportICS serves the org's meeting schedule as an importable .ics file:
// everything from 30 days back onward (older history is noise in a calendar).
func (h *MeetingsHandler) ExportICS(w http.ResponseWriter, r *http.Request) {
	from := time.Now().AddDate(0, 0, -30)
	meetings, _, err := h.repo.List(r.Context(), repo.MeetingFilter{From: &from, Limit: 500})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	auditExport(r, h.audit, "meetings.ics", map[string]any{"events": len(meetings)})
	writeICS(w, meetings, "quorum-meetings.ics")
}

// MeetingICS serves one meeting as a single-event .ics ("Add to calendar").
func (h *MeetingsHandler) MeetingICS(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	mt, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "meeting not found", "not_found")
		return
	}
	auditExport(r, h.audit, "meetings/"+id+"/meeting.ics", nil)
	writeICS(w, []model.Meeting{*mt}, "meeting.ics")
}
