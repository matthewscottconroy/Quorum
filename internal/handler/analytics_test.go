package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"quorum/internal/model"
)

func TestAnalyticsOverview(t *testing.T) {
	repo := &mockAnalyticsRepo{
		OverviewFn: func(_ context.Context) (*model.AnalyticsOverview, error) {
			return &model.AnalyticsOverview{ActiveMembers: 42, YTDPaymentsMinor: 500000, OutstandingMinor: 12000, OpenMotions: 1, UpcomingMeetings: 3, Currency: "USD"}, nil
		},
	}
	req := withCtxUser(httptest.NewRequest("GET", "/analytics/overview", nil), "u", "officer")
	rr := httptest.NewRecorder()
	NewAnalyticsHandler(repo).Overview(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var o model.AnalyticsOverview
	json.NewDecoder(rr.Body).Decode(&o)
	if o.ActiveMembers != 42 || o.YTDPaymentsMinor != 500000 {
		t.Errorf("unexpected overview: %+v", o)
	}
}

func TestAnalyticsMembership(t *testing.T) {
	repo := &mockAnalyticsRepo{
		MembershipFn: func(_ context.Context) (*model.MembershipAnalytics, error) {
			return &model.MembershipAnalytics{
				ByTier:      []model.CategoryValue{{Label: "standard", Value: 30}, {Label: "premium", Value: 12}},
				Growth:      []model.SeriesPoint{{X: "2026-06", Y: 4}, {X: "2026-07", Y: 8}},
				ActiveTotal: 42,
			}, nil
		},
	}
	req := withCtxUser(httptest.NewRequest("GET", "/analytics/membership", nil), "u", "officer")
	rr := httptest.NewRecorder()
	NewAnalyticsHandler(repo).Membership(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var m model.MembershipAnalytics
	json.NewDecoder(rr.Body).Decode(&m)
	if len(m.ByTier) != 2 || m.ByTier[0].Value != 30 || len(m.Growth) != 2 {
		t.Errorf("unexpected membership analytics: %+v", m)
	}
}
