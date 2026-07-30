package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

func testMeeting(id, title string) *model.Meeting {
	return &model.Meeting{
		ID:          id,
		Title:       title,
		ScheduledAt: time.Now().Add(24 * time.Hour),
		Status:      "scheduled",
	}
}

// ---- List ----

func TestMeetingsList_ReturnsMeetings(t *testing.T) {
	meetings := []model.Meeting{*testMeeting("mt1", "Board"), *testMeeting("mt2", "Annual")}
	h := NewMeetingsHandler(&mockMeetingsRepo{
		ListFn: func(_ context.Context, _ repo.MeetingFilter) ([]model.Meeting, int, error) {
			return meetings, len(meetings), nil
		},
	})
	req := httptest.NewRequest("GET", "/meetings", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d", rr.Code)
	}
	var got model.Page[model.Meeting]
	json.NewDecoder(rr.Body).Decode(&got)
	if len(got.Data) != 2 {
		t.Errorf("expected 2 meetings, got %d", len(got.Data))
	}
}

func TestMeetingsList_UpcomingFilter(t *testing.T) {
	var capturedFilter repo.MeetingFilter
	h := NewMeetingsHandler(&mockMeetingsRepo{
		ListFn: func(_ context.Context, f repo.MeetingFilter) ([]model.Meeting, int, error) {
			capturedFilter = f
			return nil, 0, nil
		},
	})
	req := httptest.NewRequest("GET", "/meetings?upcoming=true", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if !capturedFilter.Upcoming {
		t.Error("expected upcoming=true to be passed to repo")
	}
}

// ---- Create ----

func TestMeetingsCreate_Success(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{
		CreateFn: func(_ context.Context, mt *model.Meeting, _ string) (*model.Meeting, error) {
			mt.ID = "new-mt"
			return mt, nil
		},
	})
	body := `{"title":"Q1 Review","scheduled_at":"2025-03-01T10:00:00Z"}`
	req := withCtxUser(httptest.NewRequest("POST", "/meetings", strings.NewReader(body)), "user-1", "officer")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestMeetingsCreate_MissingTitle(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{})
	body := `{"scheduled_at":"2025-03-01T10:00:00Z"}`
	req := httptest.NewRequest("POST", "/meetings", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestMeetingsCreate_InvalidScheduledAt(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{})
	body := `{"title":"Mtg","scheduled_at":"not-a-date"}`
	req := httptest.NewRequest("POST", "/meetings", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestMeetingsCreate_DefaultStatusScheduled(t *testing.T) {
	var captured model.Meeting
	h := NewMeetingsHandler(&mockMeetingsRepo{
		CreateFn: func(_ context.Context, mt *model.Meeting, _ string) (*model.Meeting, error) {
			captured = *mt
			return mt, nil
		},
	})
	body := `{"title":"Mtg","scheduled_at":"2025-06-01T09:00:00Z"}`
	req := withCtxUser(httptest.NewRequest("POST", "/meetings", strings.NewReader(body)), "u1", "officer")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if captured.Status != "scheduled" {
		t.Errorf("default status: got %q, want 'scheduled'", captured.Status)
	}
}

// ---- Get ----

func TestMeetingsGet_Found(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{
		GetFn: func(_ context.Context, id string) (*model.Meeting, error) { return testMeeting(id, "Board"), nil },
	})
	req := chiRequest("GET", "/meetings/mt1", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d", rr.Code)
	}
}

func TestMeetingsGet_NotFound(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{
		GetFn: func(_ context.Context, _ string) (*model.Meeting, error) { return nil, errors.New("not found") },
	})
	req := chiRequest("GET", "/meetings/ghost", "", map[string]string{"id": "00000000-0000-0000-0000-000000000000"})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404", rr.Code)
	}
}

// ---- Update ----

func TestMeetingsUpdate_Success(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{
		UpdateFn: func(_ context.Context, id string, _ *string, _, _ *time.Time, _ bool, _, _, _, _ *string) (*model.Meeting, error) {
			return testMeeting(id, "Updated"), nil
		},
	})
	body := `{"title":"Updated","status":"completed"}`
	req := chiRequest("PATCH", "/meetings/mt1", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200; body: %s", rr.Code, rr.Body)
	}
}

func TestMeetingsUpdate_InvalidScheduledAt(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{})
	body := `{"scheduled_at":"bad-date"}`
	req := chiRequest("PATCH", "/meetings/mt1", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestMeetingsUpdate_InvalidStatus(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{})
	body := `{"status":"postponed"}`
	req := chiRequest("PATCH", "/meetings/mt1", body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for invalid status", rr.Code)
	}
}

func TestMeetingsUpdate_NotFound(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{
		UpdateFn: func(_ context.Context, _ string, _ *string, _, _ *time.Time, _ bool, _, _, _, _ *string) (*model.Meeting, error) {
			return nil, pgx.ErrNoRows
		},
	})
	body := `{"title":"X"}`
	req := chiRequest("PATCH", "/meetings/mt1", body, map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404 for ErrNoRows", rr.Code)
	}
}

func TestMeetingsUpdate_RepoError(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{
		UpdateFn: func(_ context.Context, _ string, _ *string, _, _ *time.Time, _ bool, _, _, _, _ *string) (*model.Meeting, error) {
			return nil, errors.New("db error")
		},
	})
	body := `{"title":"X"}`
	req := chiRequest("PATCH", "/meetings/mt1", body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

// ---- Delete ----

func TestMeetingsDelete_Success(t *testing.T) {
	deleted := false
	h := NewMeetingsHandler(&mockMeetingsRepo{
		GetFn: func(_ context.Context, id string) (*model.Meeting, error) {
			return &model.Meeting{ID: id, Title: "Standup"}, nil
		},
		DeleteFn: func(_ context.Context, _ string) error { deleted = true; return nil },
	})
	req := chiRequest("DELETE", "/meetings/mt1?confirm=Standup", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 204 {
		t.Errorf("status: got %d, want 204; body: %s", rr.Code, rr.Body)
	}
	if !deleted {
		t.Error("expected Delete to be called")
	}
}

func TestMeetingsDelete_NotifiesAttendees(t *testing.T) {
	// Attendee emails are gathered before delete and passed to the notifier as
	// the affected parties.
	var emailsQueriedBeforeDelete bool
	deleted := false
	fn := &fakeNotifier{}
	h := NewMeetingsHandler(&mockMeetingsRepo{
		GetFn: func(_ context.Context, id string) (*model.Meeting, error) {
			return &model.Meeting{ID: id, Title: "Board Sync"}, nil
		},
		AttendeeEmailsFn: func(_ context.Context, _ string) ([]string, error) {
			emailsQueriedBeforeDelete = !deleted // must run before Delete
			return []string{"a@example.com", "b@example.com"}, nil
		},
		DeleteFn: func(_ context.Context, _ string) error { deleted = true; return nil },
	})
	h.SetNotifier(fn)

	req := withCtxUser(chiRequest("DELETE", "/meetings/x?confirm=Board%20Sync", "", map[string]string{"id": testUUID}), "actor-1", "superadmin")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != 204 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if !emailsQueriedBeforeDelete {
		t.Error("attendee emails must be gathered before the meeting is deleted")
	}
	if !fn.called || fn.entityType != "meeting" || fn.entityName != "Board Sync" {
		t.Errorf("notifier args: called=%v type=%q name=%q", fn.called, fn.entityType, fn.entityName)
	}
	if len(fn.affected) != 2 {
		t.Errorf("expected 2 affected attendee emails, got %v", fn.affected)
	}
}

func TestMeetingsDelete_NotFound(t *testing.T) {
	// Get returns ErrNoRows before the confirm gate → 404.
	h := NewMeetingsHandler(&mockMeetingsRepo{
		GetFn: func(_ context.Context, _ string) (*model.Meeting, error) { return nil, pgx.ErrNoRows },
	})
	req := chiRequest("DELETE", "/meetings/mt1?confirm=Standup", "", map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404 for ErrNoRows", rr.Code)
	}
}

func TestMeetingsDelete_MissingConfirm(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{
		GetFn: func(_ context.Context, id string) (*model.Meeting, error) {
			return &model.Meeting{ID: id, Title: "Standup"}, nil
		},
	})
	req := chiRequest("DELETE", "/meetings/mt1", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 without ?confirm", rr.Code)
	}
	if code := errCode(t, rr); code != "confirmation_required" {
		t.Errorf("code: got %q, want confirmation_required", code)
	}
}

func TestMeetingsDelete_MismatchedConfirm(t *testing.T) {
	deleted := false
	h := NewMeetingsHandler(&mockMeetingsRepo{
		GetFn: func(_ context.Context, id string) (*model.Meeting, error) {
			return &model.Meeting{ID: id, Title: "Standup"}, nil
		},
		DeleteFn: func(_ context.Context, _ string) error { deleted = true; return nil },
	})
	req := chiRequest("DELETE", "/meetings/mt1?confirm=WrongName", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for mismatched confirm", rr.Code)
	}
	if code := errCode(t, rr); code != "confirmation_mismatch" {
		t.Errorf("code: got %q, want confirmation_mismatch", code)
	}
	if deleted {
		t.Error("Delete must not run when confirmation does not match")
	}
}

// ---- SetAttendees ----

func TestMeetingsSetAttendees_Success(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{
		SetAttendeesFn: func(_ context.Context, _ string, _ []model.MeetingAttendee) error { return nil },
		GetAttendeesFn: func(_ context.Context, _ string) ([]model.MeetingAttendee, error) {
			return []model.MeetingAttendee{{MemberID: "m1", Present: true}}, nil
		},
	})
	body := `{"attendees":[{"member_id":"m1","present":true}]}`
	req := chiRequest("PUT", "/meetings/mt1/attendees", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.SetAttendees(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200; body: %s", rr.Code, rr.Body)
	}
}

func TestMeetingsSetAttendees_BadJSON(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{})
	req := chiRequest("PUT", "/meetings/mt1/attendees", "{bad", map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.SetAttendees(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

// ---- CreateDecision ----

func TestMeetingsCreateDecision_Success(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{
		CreateDecisionFn: func(_ context.Context, d *model.MeetingDecision) (*model.MeetingDecision, error) {
			d.ID = "dec1"
			return d, nil
		},
	})
	body := `{"summary":"Approved budget","outcome":"passed"}`
	req := chiRequest("POST", "/meetings/mt1/decisions", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.CreateDecision(rr, req)
	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestMeetingsCreateDecision_MissingSummary(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{})
	body := `{"outcome":"passed"}`
	req := chiRequest("POST", "/meetings/mt1/decisions", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.CreateDecision(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestMeetingsCreateDecision_DefaultOutcome(t *testing.T) {
	var captured model.MeetingDecision
	h := NewMeetingsHandler(&mockMeetingsRepo{
		CreateDecisionFn: func(_ context.Context, d *model.MeetingDecision) (*model.MeetingDecision, error) {
			captured = *d
			return d, nil
		},
	})
	body := `{"summary":"Some vote"}`
	req := chiRequest("POST", "/meetings/mt1/decisions", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.CreateDecision(rr, req)
	if captured.Outcome != "passed" {
		t.Errorf("default outcome: got %q, want 'passed'", captured.Outcome)
	}
}

// ---- UpdateDecision ----

func TestMeetingsUpdateDecision_Success(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{
		UpdateDecisionFn: func(_ context.Context, id string, _, _, _ *string, _, _, _ *int) (*model.MeetingDecision, error) {
			return &model.MeetingDecision{ID: id}, nil
		},
	})
	body := `{"summary":"Revised summary"}`
	req := chiRequest("PATCH", "/meetings/mt1/decisions/dec1", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111", "did": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"})
	rr := httptest.NewRecorder()
	h.UpdateDecision(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d", rr.Code)
	}
}

// ---- DeleteDecision ----

func TestMeetingsDeleteDecision_Success(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{
		DeleteDecisionFn: func(_ context.Context, _ string) error { return nil },
	})
	req := chiRequest("DELETE", "/meetings/mt1/decisions/dec1", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111", "did": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"})
	rr := httptest.NewRecorder()
	h.DeleteDecision(rr, req)
	if rr.Code != 204 {
		t.Errorf("status: got %d, want 204", rr.Code)
	}
}

func TestMeetingsDeleteDecision_Error(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{
		DeleteDecisionFn: func(_ context.Context, _ string) error { return errors.New("not found") },
	})
	req := chiRequest("DELETE", "/meetings/mt1/decisions/ghost", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111", "did": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"})
	rr := httptest.NewRecorder()
	h.DeleteDecision(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

// ---- CreatedBy propagation ----

func TestMeetingsCreate_PassesUserIDAsCreatedBy(t *testing.T) {
	var capturedCreatedBy string
	h := NewMeetingsHandler(&mockMeetingsRepo{
		CreateFn: func(_ context.Context, mt *model.Meeting, createdBy string) (*model.Meeting, error) {
			capturedCreatedBy = createdBy
			mt.ID = "mt1"
			return mt, nil
		},
	})
	body := `{"title":"Mtg","scheduled_at":"2025-01-01T00:00:00Z"}`
	req := withCtxUser(httptest.NewRequest("POST", "/meetings", strings.NewReader(body)), "user-xyz", "officer")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if capturedCreatedBy != "user-xyz" {
		t.Errorf("created_by: got %q, want user-xyz", capturedCreatedBy)
	}
}

// Silence unused import warning.
var _ = http.StatusOK

func TestMeetingsDelete_BlockedByGovernanceHistory(t *testing.T) {
	h := NewMeetingsHandler(&mockMeetingsRepo{
		GetFn: func(_ context.Context, _ string) (*model.Meeting, error) {
			return &model.Meeting{ID: "mt1", Title: "Board"}, nil
		},
		HasGovernanceHistoryFn: func(_ context.Context, _ string) (bool, error) { return true, nil },
	})
	req := chiRequest("DELETE", "/meetings/mt1?confirm=Board", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 when the meeting has governance history, got %d: %s", rr.Code, rr.Body)
	}
}
