package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

type mockAuditReader struct {
	ListFn func(ctx context.Context, f repo.AuditFilter) ([]model.AuditEntry, int, error)
}

func (m *mockAuditReader) List(ctx context.Context, f repo.AuditFilter) ([]model.AuditEntry, int, error) {
	return m.ListFn(ctx, f)
}

func TestAuditList_PassesFilters(t *testing.T) {
	var got repo.AuditFilter
	h := NewAuditHandler(&mockAuditReader{ListFn: func(_ context.Context, f repo.AuditFilter) ([]model.AuditEntry, int, error) {
		got = f
		return []model.AuditEntry{{ID: "a1", Action: "POST /api/v1/members"}}, 1, nil
	}})
	req := withCtxUser(httptest.NewRequest("GET",
		"/audit?action=DELETE&entity_type=members&since=2026-01-01&until=2026-01-31&limit=25&offset=50", nil), "u", "admin")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if got.Action != "DELETE" || got.EntityType != "members" {
		t.Errorf("filters not propagated: %+v", got)
	}
	if got.Limit != 25 || got.Offset != 50 {
		t.Errorf("pagination not propagated: limit=%d offset=%d", got.Limit, got.Offset)
	}
	if got.Since == nil || got.Since.Format("2006-01-02") != "2026-01-01" {
		t.Errorf("since not parsed: %v", got.Since)
	}
	// `until` must cover the whole day, not midnight.
	if got.Until == nil || got.Until.Before(time.Date(2026, 1, 31, 23, 0, 0, 0, time.UTC)) {
		t.Errorf("until should span the full day, got %v", got.Until)
	}
	var page model.Page[model.AuditEntry]
	if err := json.NewDecoder(rr.Body).Decode(&page); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if page.Total != 1 || len(page.Data) != 1 {
		t.Errorf("unexpected page: %+v", page)
	}
}

func TestAuditList_RejectsBadInput(t *testing.T) {
	h := NewAuditHandler(&mockAuditReader{ListFn: func(_ context.Context, _ repo.AuditFilter) ([]model.AuditEntry, int, error) {
		t.Error("repo should not be called on invalid input")
		return nil, 0, nil
	}})
	for _, q := range []string{"?user_id=not-a-uuid", "?since=01-01-2026", "?until=nope"} {
		rr := httptest.NewRecorder()
		h.List(rr, withCtxUser(httptest.NewRequest("GET", "/audit"+q, nil), "u", "admin"))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", q, rr.Code)
		}
	}
}

func TestSessions_CountAndRevokeOthers(t *testing.T) {
	var keptHash string
	repoMock := &mockAuthRepo{
		ActiveSessionCountFn: func(_ context.Context, _ string) (int, error) { return 3, nil },
		RevokeOtherRefreshTokensFn: func(_ context.Context, _, keep string) (int64, error) {
			keptHash = keep
			return 2, nil
		},
	}
	h := NewAuthHandler(repoMock, testConfig())

	rr := httptest.NewRecorder()
	h.Sessions(rr, withCtxUser(httptest.NewRequest("GET", "/auth/me/sessions", nil), testUserID, "member"))
	var counts map[string]int
	if err := json.NewDecoder(rr.Body).Decode(&counts); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if counts["active_sessions"] != 3 {
		t.Errorf("active_sessions = %d, want 3", counts["active_sessions"])
	}

	// With a refresh cookie present, that session must be preserved.
	req := withCtxUser(httptest.NewRequest("POST", "/auth/me/sessions/revoke-others", nil), testUserID, "member")
	req.AddCookie(&http.Cookie{Name: "quorum_refresh", Value: "my-current-token"})
	rr2 := httptest.NewRecorder()
	h.RevokeOtherSessions(rr2, req)
	if rr2.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr2.Code, rr2.Body)
	}
	if keptHash == "" || keptHash == "my-current-token" {
		t.Errorf("current session should be preserved by token HASH, got %q", keptHash)
	}
	var res map[string]int64
	if err := json.NewDecoder(rr2.Body).Decode(&res); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if res["revoked"] != 2 {
		t.Errorf("revoked = %d, want 2", res["revoked"])
	}
}

func TestRevokeOtherSessions_NoCookieRevokesAll(t *testing.T) {
	var keptHash = "unset"
	h := NewAuthHandler(&mockAuthRepo{
		RevokeOtherRefreshTokensFn: func(_ context.Context, _, keep string) (int64, error) {
			keptHash = keep
			return 5, nil
		},
	}, testConfig())
	rr := httptest.NewRecorder()
	h.RevokeOtherSessions(rr, withCtxUser(httptest.NewRequest("POST", "/x", nil), testUserID, "member"))
	if keptHash != "" {
		t.Errorf("without a cookie every session should be revoked (empty keep hash), got %q", keptHash)
	}
}
