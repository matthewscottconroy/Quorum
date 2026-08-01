package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quorum/internal/model"
	"quorum/internal/repo"
)

type mockSprintsRepo struct {
	CreateFn func(ctx context.Context, s *model.Sprint, createdBy string) (*model.Sprint, error)
}

func (m *mockSprintsRepo) List(_ context.Context) ([]model.Sprint, error) { return nil, nil }
func (m *mockSprintsRepo) Get(_ context.Context, id string) (*model.Sprint, error) {
	return &model.Sprint{ID: id, Name: "S"}, nil
}
func (m *mockSprintsRepo) Create(ctx context.Context, s *model.Sprint, createdBy string) (*model.Sprint, error) {
	if m.CreateFn != nil {
		return m.CreateFn(ctx, s, createdBy)
	}
	return s, nil
}
func (m *mockSprintsRepo) Update(_ context.Context, id string, _, _, _, _, _ *string) (*model.Sprint, error) {
	return &model.Sprint{ID: id}, nil
}
func (m *mockSprintsRepo) Delete(_ context.Context, _ string) error { return nil }

func TestSprints_CreateValidation(t *testing.T) {
	h := NewSprintsHandler(&mockSprintsRepo{})
	cases := map[string]string{
		"missing name":   `{"starts_on":"2026-08-01","ends_on":"2026-08-14"}`,
		"bad date":       `{"name":"S","starts_on":"08/01/2026","ends_on":"2026-08-14"}`,
		"inverted range": `{"name":"S","starts_on":"2026-08-14","ends_on":"2026-08-01"}`,
		"bad status":     `{"name":"S","starts_on":"2026-08-01","ends_on":"2026-08-14","status":"paused"}`,
	}
	for name, body := range cases {
		rr := httptest.NewRecorder()
		h.Create(rr, withCtxUser(httptest.NewRequest("POST", "/sprints", strings.NewReader(body)), "u", "officer"))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", name, rr.Code)
		}
	}
	// Valid creation defaults to planned.
	var got *model.Sprint
	h2 := NewSprintsHandler(&mockSprintsRepo{CreateFn: func(_ context.Context, s *model.Sprint, _ string) (*model.Sprint, error) {
		got = s
		return s, nil
	}})
	rr := httptest.NewRecorder()
	h2.Create(rr, withCtxUser(httptest.NewRequest("POST", "/sprints",
		strings.NewReader(`{"name":"August iteration","starts_on":"2026-08-01","ends_on":"2026-08-14"}`)), "u", "officer"))
	if rr.Code != http.StatusCreated || got.Status != "planned" {
		t.Fatalf("valid create: %d, status %q", rr.Code, got.Status)
	}
}

// Every export endpoint must record who/what/when in the audit log.
func TestExports_AreAuditLogged(t *testing.T) {
	var actions []string
	ar := &mockAuditRepo{LogFn: func(_ context.Context, uid, action, etype, _ string, _ map[string]any) error {
		if uid != "u-1" || etype != "export" {
			t.Errorf("export log uid=%q etype=%q", uid, etype)
		}
		actions = append(actions, action)
		return nil
	}}
	h := NewMeetingsHandler(&mockMeetingsRepo{
		ListFn: func(_ context.Context, _ repo.MeetingFilter) ([]model.Meeting, int, error) { return nil, 0, nil },
	})
	h.SetAuditLogger(ar)
	req := withCtxUser(httptest.NewRequest("GET", "/export/meetings.ics", nil), "u-1", "member")
	rr := httptest.NewRecorder()
	h.ExportICS(rr, req)
	if len(actions) != 1 || actions[0] != "EXPORT meetings.ics" {
		t.Fatalf("ics export not logged: %v", actions)
	}
}
