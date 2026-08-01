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
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
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
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.UpcomingMeetings == nil {
		t.Error("expected UpcomingMeetings to be empty slice, not nil")
	}
	if got.OpenActionItems == nil {
		t.Error("expected OpenActionItems to be empty slice, not nil")
	}
}

// healthyDashboardMocks returns mocks where every dependency succeeds; tests
// override a single one to make it fail.
func healthyDashboardMocks() (*mockDuesRepo, *mockMembersRepo, *mockMeetingsRepo, *mockActionItemsRepo) {
	return &mockDuesRepo{
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
		}
}

func TestDashboardSummary_RepoErrors(t *testing.T) {
	// Any failing dependency is a real error: zeroed counts behind a 200
	// would mislead users, so every failure path must surface as 500.
	dbErr := errors.New("db down")
	cases := []struct {
		name    string
		breakFn func(d *mockDuesRepo, m *mockMembersRepo, mt *mockMeetingsRepo, ai *mockActionItemsRepo)
	}{
		{"overdue count fails", func(d *mockDuesRepo, _ *mockMembersRepo, _ *mockMeetingsRepo, _ *mockActionItemsRepo) {
			d.CountByStatusFn = func(_ context.Context, status string) (int, error) {
				if status == "overdue" {
					return 0, dbErr
				}
				return 0, nil
			}
		}},
		{"pending count fails", func(d *mockDuesRepo, _ *mockMembersRepo, _ *mockMeetingsRepo, _ *mockActionItemsRepo) {
			d.CountByStatusFn = func(_ context.Context, status string) (int, error) {
				if status == "pending" {
					return 0, dbErr
				}
				return 0, nil
			}
		}},
		{"member count fails", func(_ *mockDuesRepo, m *mockMembersRepo, _ *mockMeetingsRepo, _ *mockActionItemsRepo) {
			m.CountFn = func(_ context.Context) (int, error) { return 0, dbErr }
		}},
		{"upcoming meetings fails", func(_ *mockDuesRepo, _ *mockMembersRepo, mt *mockMeetingsRepo, _ *mockActionItemsRepo) {
			mt.UpcomingFn = func(_ context.Context, _ int) ([]model.Meeting, error) { return nil, dbErr }
		}},
		{"open action items fails", func(_ *mockDuesRepo, _ *mockMembersRepo, _ *mockMeetingsRepo, ai *mockActionItemsRepo) {
			ai.ListFn = func(_ context.Context, _ repo.ActionItemFilter) ([]model.ActionItem, int, error) {
				return nil, 0, dbErr
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, m, mt, ai := healthyDashboardMocks()
			tc.breakFn(d, m, mt, ai)
			h := dashboardHandler(d, m, mt, ai)

			req := httptest.NewRequest("GET", "/dashboard", nil)
			rr := httptest.NewRecorder()
			h.Summary(rr, req)

			if rr.Code != 500 {
				t.Errorf("status: got %d, want 500 when a dependency fails", rr.Code)
			}
		})
	}
}
