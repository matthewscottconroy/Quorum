package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quorum/internal/model"
)

func TestMinutes_AddValidatesKind(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{})
	req := chiRequest("POST", "/meetings/x/minutes", `{"kind":"karaoke","body":"..."}`,
		map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.AddMinutesEntry(rr, withCtxUser(req, "u", "officer"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid kind should 400, got %d", rr.Code)
	}
	// Empty body rejected too.
	req2 := chiRequest("POST", "/meetings/x/minutes", `{"kind":"note","body":"   "}`,
		map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr2 := httptest.NewRecorder()
	h.AddMinutesEntry(rr2, withCtxUser(req2, "u", "officer"))
	if rr2.Code != http.StatusBadRequest {
		t.Fatalf("blank body should 400, got %d", rr2.Code)
	}
}

func TestMinutes_FinalizeRequiresConfirm(t *testing.T) {
	finalized := false
	h := NewMeetingsHandler(&mockMeetingsRepo{
		GetFn: func(_ context.Context, _ string) (*model.Meeting, error) {
			return &model.Meeting{ID: "m1", Title: "October General Meeting"}, nil
		},
		FinalizeMinutesFn: func(_ context.Context, _, _ string) error { finalized = true; return nil },
	})
	req := chiRequest("POST", "/meetings/m1/minutes/finalize", "",
		map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.FinalizeMinutes(rr, withCtxUser(req, "u", "officer"))
	if rr.Code != http.StatusBadRequest || finalized {
		t.Fatalf("finalize without confirm must 400 and not run, got %d (ran=%v)", rr.Code, finalized)
	}
	req2 := chiRequest("POST", "/meetings/m1/minutes/finalize?confirm=October%20General%20Meeting", "",
		map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr2 := httptest.NewRecorder()
	h.FinalizeMinutes(rr2, withCtxUser(req2, "u", "officer"))
	if rr2.Code != http.StatusNoContent || !finalized {
		t.Fatalf("confirmed finalize should 204 and run, got %d (ran=%v)", rr2.Code, finalized)
	}
}

func TestMinutesDocument_Assembly(t *testing.T) {
	loc := "Union Hall"
	det := "As distributed."
	mover, seconder := "Ada", "Alan"
	h := NewMeetingsHandler(&mockMeetingsRepo{
		GetFn: func(_ context.Context, _ string) (*model.Meeting, error) {
			return &model.Meeting{
				ID: "m1", Title: "October General Meeting", Location: &loc, Status: "completed",
				Attendees: []model.MeetingAttendee{
					{MemberID: "a", MemberName: "Ada", Present: true},
					{MemberID: "b", MemberName: "Alan", Present: false},
				},
			}, nil
		},
		ListMinutesFn: func(_ context.Context, _ string) ([]model.MinutesEntry, error) {
			return []model.MinutesEntry{{Seq: 1, Kind: "call_to_order", Body: "Called to order."}}, nil
		},
	})
	h.SetGovernanceSource(&mockGovernanceRepo{
		ListMotionsFn: func(_ context.Context, _ string) ([]model.Motion, error) {
			return []model.Motion{{ID: "mo1", Title: "Ratify bylaws", Business: "old",
				Threshold: "two_thirds", Status: "carried", Detail: &det,
				MoverName: &mover, SeconderName: &seconder,
				Tally: model.MotionTally{For: 2, Total: 2}}}, nil
		},
		GetVotesFn: func(_ context.Context, _ string) ([]model.MotionVote, error) {
			return []model.MotionVote{{MemberName: "Ada", Choice: "for"}, {MemberName: "Alan", Choice: "for", IsProxy: true}}, nil
		},
	})
	req := chiRequest("GET", "/meetings/m1/minutes.md", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.MinutesDocument(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"# Minutes — October General Meeting",
		"DRAFT — these minutes have not been finalized",
		"**Present (1):** Ada", "**Absent (1):** Alan",
		"**Call to order** — Called to order.",
		"### Ratify bylaws", "Old business", "**Moved by:** Ada", "**Seconded by:** Alan",
		"**CARRIED**", "Alan (by proxy)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("document missing %q", want)
		}
	}
}
