package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
)

// ---- mockNotifyRepo ----

type mockNotifyRepo struct {
	ListForUserFn       func(ctx context.Context, userID string, unreadOnly bool, limit int) ([]model.Notification, error)
	UnreadCountFn       func(ctx context.Context, userID string) (int, error)
	MarkReadFn          func(ctx context.Context, userID, id string) error
	MarkAllReadFn       func(ctx context.Context, userID string) error
	GetPreferencesFn    func(ctx context.Context, userID string) (*model.NotificationPreferences, error)
	UpdatePreferencesFn func(ctx context.Context, userID string, p model.NotificationPreferences) error
}

func (m *mockNotifyRepo) ListForUser(ctx context.Context, userID string, unreadOnly bool, limit int) ([]model.Notification, error) {
	return m.ListForUserFn(ctx, userID, unreadOnly, limit)
}
func (m *mockNotifyRepo) UnreadCount(ctx context.Context, userID string) (int, error) {
	return m.UnreadCountFn(ctx, userID)
}
func (m *mockNotifyRepo) MarkRead(ctx context.Context, userID, id string) error {
	return m.MarkReadFn(ctx, userID, id)
}
func (m *mockNotifyRepo) MarkAllRead(ctx context.Context, userID string) error {
	return m.MarkAllReadFn(ctx, userID)
}
func (m *mockNotifyRepo) GetPreferences(ctx context.Context, userID string) (*model.NotificationPreferences, error) {
	return m.GetPreferencesFn(ctx, userID)
}
func (m *mockNotifyRepo) UpdatePreferences(ctx context.Context, userID string, p model.NotificationPreferences) error {
	return m.UpdatePreferencesFn(ctx, userID, p)
}

func TestNotifications_UnreadCount(t *testing.T) {
	h := NewNotificationsHandler(&mockNotifyRepo{
		UnreadCountFn: func(_ context.Context, _ string) (int, error) { return 3, nil },
	})
	req := withCtxUser(httptest.NewRequest("GET", "/notifications/unread-count", nil), "u1", "member")
	rr := httptest.NewRecorder()
	h.UnreadCount(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var resp map[string]int
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp["unread"] != 3 {
		t.Errorf("unread = %d, want 3", resp["unread"])
	}
}

func TestNotifications_ListScopedToCaller(t *testing.T) {
	var gotUser string
	var gotUnread bool
	h := NewNotificationsHandler(&mockNotifyRepo{
		ListForUserFn: func(_ context.Context, userID string, unreadOnly bool, _ int) ([]model.Notification, error) {
			gotUser, gotUnread = userID, unreadOnly
			return []model.Notification{{ID: "n1", Type: "motion.opened", Title: "x"}}, nil
		},
	})
	req := withCtxUser(httptest.NewRequest("GET", "/notifications?unread=true", nil), "u-42", "member")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	if gotUser != "u-42" {
		t.Errorf("list should be scoped to caller u-42, got %q", gotUser)
	}
	if !gotUnread {
		t.Error("unread=true not propagated")
	}
}

func TestNotifications_MarkRead_NotFoundIsScoped(t *testing.T) {
	// MarkRead on another user's (or missing) notification returns 404, not 500.
	h := NewNotificationsHandler(&mockNotifyRepo{
		MarkReadFn: func(_ context.Context, _, _ string) error { return pgx.ErrNoRows },
	})
	req := chiRequest("POST", "/notifications/x/read", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	req = withCtxUser(req, "u1", "member")
	rr := httptest.NewRecorder()
	h.MarkRead(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for another user's notification, got %d", rr.Code)
	}
}

func TestNotifications_UpdatePreferences(t *testing.T) {
	var saved model.NotificationPreferences
	h := NewNotificationsHandler(&mockNotifyRepo{
		UpdatePreferencesFn: func(_ context.Context, _ string, p model.NotificationPreferences) error {
			saved = p
			return nil
		},
	})
	body := `{"governance_email":false,"meetings_email":true,"dues_email":true,"assignments_email":false}`
	req := withCtxUser(httptest.NewRequest("PUT", "/notifications/preferences", strings.NewReader(body)), "u1", "member")
	rr := httptest.NewRecorder()
	h.UpdatePreferences(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if saved.GovernanceEmail || saved.AssignmentsEmail || !saved.MeetingsEmail || !saved.DuesEmail {
		t.Errorf("preferences not saved as sent: %+v", saved)
	}
}
