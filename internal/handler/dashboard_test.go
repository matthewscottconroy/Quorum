package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

func dashboardHandler(dues duesRepo, members membersRepo, meetings meetingsRepo, ai actionItemsRepo) *DashboardHandler {
	return NewDashboardHandler(dues, members, meetings, ai)
}

func TestDashboardSummary_Success(t *testing.T) {
	upcoming := []model.Meeting{
		{ID: "mt1", Title: "Board", ScheduledAt: time.Now().Add(24 * time.Hour), Status: "scheduled"},
	}
	openItems := []model.ActionItem{
		{ID: "ai1", Title: "Review charter", Status: "open", Priority: "high"},
	}

	h := dashboardHandler(
		&mockDuesRepo{
			CountByStatusFn: func(_ context.Context, status string) (int, error) {
				if status == "overdue" {
					return 3, nil
				}
				return 7, nil
			},
		},
		&mockMembersRepo{
			CountFn: func(_ context.Context) (int, error) { return 42, nil },
		},
		&mockMeetingsRepo{
			UpcomingFn: func(_ context.Context, n int) ([]model.Meeting, error) { return upcoming, nil },
		},
		&mockActionItemsRepo{
			ListFn: func(_ context.Context, _ repo.ActionItemFilter) ([]model.ActionItem, int, error) {
				return openItems, 1, nil
			},
		},
	)

	req := withCtxUser(httptest.NewRequest("GET", "/dashboard", nil), "u1", "member")
	rr := httptest.NewRecorder()
	h.Summary(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	var got model.DashboardSummary
	json.NewDecoder(rr.Body).Decode(&got)
	if got.OverdueDuesCount != 3 {
		t.Errorf("overdue: got %d, want 3", got.OverdueDuesCount)
	}
	if got.PendingDuesCount != 7 {
		t.Errorf("pending: got %d, want 7", got.PendingDuesCount)
	}
	if got.ActiveMemberCount != 42 {
		t.Errorf("members: got %d, want 42", got.ActiveMemberCount)
	}
	if len(got.UpcomingMeetings) != 1 {
		t.Errorf("upcoming meetings: got %d, want 1", len(got.UpcomingMeetings))
	}
	if len(got.OpenActionItems) != 1 {
		t.Errorf("open action items: got %d, want 1", len(got.OpenActionItems))
	}
}

func TestDashboardSummary_EmptySlices(t *testing.T) {
	h := dashboardHandler(
		&mockDuesRepo{
			CountByStatusFn: func(_ context.Context, _ string) (int, error) { return 0, nil },
		},
		&mockMembersRepo{
			CountFn: func(_ context.Context) (int, error) { return 0, nil },
		},
		&mockMeetingsRepo{
			UpcomingFn: func(_ context.Context, _ int) ([]model.Meeting, error) { return nil, nil },
		},
		&mockActionItemsRepo{
			ListFn: func(_ context.Context, _ repo.ActionItemFilter) ([]model.ActionItem, int, error) {
				return nil, 0, nil
			},
		},
	)

	req := httptest.NewRequest("GET", "/dashboard", nil)
	rr := httptest.NewRecorder()
	h.Summary(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: got %d", rr.Code)
	}
	var got model.DashboardSummary
	json.NewDecoder(rr.Body).Decode(&got)
	if got.UpcomingMeetings == nil {
		t.Error("expected UpcomingMeetings to be empty slice, not nil")
	}
	if got.OpenActionItems == nil {
		t.Error("expected OpenActionItems to be empty slice, not nil")
	}
}

func TestDashboardSummary_PartialErrors(t *testing.T) {
	// Repo errors should be logged and not cause a 500; partial data is returned.
	h := dashboardHandler(
		&mockDuesRepo{
			CountByStatusFn: func(_ context.Context, _ string) (int, error) {
				return 0, errors.New("db down")
			},
		},
		&mockMembersRepo{
			CountFn: func(_ context.Context) (int, error) { return 0, errors.New("db down") },
		},
		&mockMeetingsRepo{
			UpcomingFn: func(_ context.Context, _ int) ([]model.Meeting, error) {
				return nil, errors.New("db down")
			},
		},
		&mockActionItemsRepo{
			ListFn: func(_ context.Context, _ repo.ActionItemFilter) ([]model.ActionItem, int, error) {
				return nil, 0, errors.New("db down")
			},
		},
	)

	req := httptest.NewRequest("GET", "/dashboard", nil)
	rr := httptest.NewRecorder()
	h.Summary(rr, req)

	// Should still return 200 with zero values — dashboard is best-effort.
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200 even on partial errors", rr.Code)
	}
}
