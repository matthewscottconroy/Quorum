package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

func TestICSEscape(t *testing.T) {
	got := icsEscape("Budget; vote, part 2\nBring \\ docs")
	want := `Budget\; vote\, part 2\nBring \\ docs`
	if got != want {
		t.Errorf("escape = %q, want %q", got, want)
	}
}

func TestICSFold_LongLinesAndUTF8(t *testing.T) {
	var b strings.Builder
	icsFold(&b, "SUMMARY:"+strings.Repeat("é", 100)) // 2-byte runes force boundary care
	out := b.String()
	for i, line := range strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n") {
		if len(line) > 76 { // 75 + leading space on continuations
			t.Errorf("line %d exceeds fold width: %d bytes", i, len(line))
		}
	}
	// Re-unfold and confirm nothing was lost or corrupted.
	unfolded := strings.ReplaceAll(out, "\r\n ", "")
	if !strings.Contains(unfolded, strings.Repeat("é", 100)) {
		t.Error("folding corrupted UTF-8 content")
	}
}

func TestMeetingICS_SingleEvent(t *testing.T) {
	loc := "Room 4; East wing"
	agenda := "Line one\nLine two"
	h := NewMeetingsHandler(&mockMeetingsRepo{
		GetFn: func(_ context.Context, _ string) (*model.Meeting, error) {
			return &model.Meeting{
				ID: "abc-123", Title: "Board, Q3", ScheduledAt: time.Date(2026, 9, 1, 18, 0, 0, 0, time.UTC),
				Location: &loc, Agenda: &agenda, Status: "scheduled",
			}, nil
		},
	})
	req := chiRequest("GET", "/meetings/x/ics", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.MeetingICS(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	body := rr.Body.String()
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/calendar") {
		t.Errorf("content-type = %q", ct)
	}
	for _, want := range []string{
		"BEGIN:VCALENDAR\r\n", "BEGIN:VEVENT\r\n",
		"UID:abc-123@quorum\r\n",
		"DTSTART:20260901T180000Z\r\n",
		`SUMMARY:Board\, Q3` + "\r\n",
		`LOCATION:Room 4\; East wing` + "\r\n",
		`DESCRIPTION:Line one\nLine two` + "\r\n",
		"STATUS:CONFIRMED\r\n", "END:VCALENDAR\r\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ics missing %q\n%s", want, body)
		}
	}
}

func TestExportICS_CancelledStatusAndRange(t *testing.T) {
	var gotFrom *time.Time
	h := NewMeetingsHandler(&mockMeetingsRepo{
		ListFn: func(_ context.Context, f repo.MeetingFilter) ([]model.Meeting, int, error) {
			gotFrom = f.From
			return []model.Meeting{
				{ID: "m1", Title: "Old one", ScheduledAt: time.Now(), Status: "cancelled"},
			}, 1, nil
		},
	})
	rr := httptest.NewRecorder()
	h.ExportICS(rr, httptest.NewRequest("GET", "/export/meetings.ics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	if gotFrom == nil || time.Since(*gotFrom) < 29*24*time.Hour {
		t.Errorf("export should look back ~30 days, got from=%v", gotFrom)
	}
	if !strings.Contains(rr.Body.String(), "STATUS:CANCELLED\r\n") {
		t.Error("cancelled meeting must carry STATUS:CANCELLED")
	}
}

func TestMeetingsList_FromToFilter(t *testing.T) {
	var got repo.MeetingFilter
	h := NewMeetingsHandler(&mockMeetingsRepo{
		ListFn: func(_ context.Context, f repo.MeetingFilter) ([]model.Meeting, int, error) {
			got = f
			return nil, 0, nil
		},
	})
	rr := httptest.NewRecorder()
	h.List(rr, httptest.NewRequest("GET", "/meetings?from=2026-09-01&to=2026-09-30", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	if got.From == nil || got.From.Format("2006-01-02") != "2026-09-01" {
		t.Errorf("from not parsed: %v", got.From)
	}
	// `to` is exclusive end-of-day: the whole Sep 30 is included.
	if got.To == nil || got.To.Format("2006-01-02") != "2026-10-01" {
		t.Errorf("to should be exclusive end of day: %v", got.To)
	}
	// Malformed dates are rejected.
	rr2 := httptest.NewRecorder()
	h.List(rr2, httptest.NewRequest("GET", "/meetings?from=09/01/2026", nil))
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("bad from should 400, got %d", rr2.Code)
	}
}
