package handler

// The calendar feed had ZERO tests despite being the only unauthenticated
// meeting read. These pin the shown-once token contract and the feed gate.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

type mockCalTokens struct {
	has    bool
	minted int
}

func (m *mockCalTokens) HasCalendarToken(context.Context, string) (bool, error) {
	return m.has, nil
}
func (m *mockCalTokens) RotateCalendarToken(context.Context, string) (string, error) {
	m.minted++
	m.has = true
	return strings.Repeat("a", 64), nil
}
func (m *mockCalTokens) UserIDByCalendarToken(_ context.Context, token string) (string, error) {
	if token == strings.Repeat("a", 64) {
		return "u1", nil
	}
	return "", pgx.ErrNoRows
}

type mockCalMeetings struct{}

func (mockCalMeetings) List(context.Context, repo.MeetingFilter) ([]model.Meeting, int, error) {
	return []model.Meeting{}, 0, nil
}

// First call mints and reveals the URL; later calls confirm active WITHOUT
// the URL — tokens are hashed at rest and shown exactly once.
func TestCalendarSubscription_ShownOnce(t *testing.T) {
	toks := &mockCalTokens{}
	h := NewCalendarHandler(toks, mockCalMeetings{}, "https://q.example")
	req := withCtxUser(httptest.NewRequest("POST", "/calendar/subscription", nil), "u1", "member")
	rr := httptest.NewRecorder()
	h.Subscription(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "/api/v1/calendar/") {
		t.Fatalf("first mint: got %d %s, want URL", rr.Code, rr.Body)
	}
	rr2 := httptest.NewRecorder()
	h.Subscription(rr2, withCtxUser(httptest.NewRequest("POST", "/calendar/subscription", nil), "u1", "member"))
	if rr2.Code != 200 || strings.Contains(rr2.Body.String(), "url") {
		t.Fatalf("existing feed: got %d %s, want active with NO url", rr2.Code, rr2.Body)
	}
	if toks.minted != 1 {
		t.Fatalf("minted %d times, want 1", toks.minted)
	}
}

// A bad or wrong-length token is a 404, never a feed.
func TestCalendarFeed_TokenGate(t *testing.T) {
	h := NewCalendarHandler(&mockCalTokens{}, mockCalMeetings{}, "https://q.example")
	for _, tok := range []string{"short", strings.Repeat("b", 64)} {
		req := reqWithParam("GET", "/calendar/"+tok+".ics", "", map[string]string{"token": tok + ".ics"})
		rr := httptest.NewRecorder()
		h.Feed(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("token %q: got %d, want 404", tok, rr.Code)
		}
	}
	// The real token serves a calendar.
	good := strings.Repeat("a", 64)
	req := reqWithParam("GET", "/calendar/"+good+".ics", "", map[string]string{"token": good + ".ics"})
	rr := httptest.NewRecorder()
	h.Feed(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "BEGIN:VCALENDAR") {
		t.Fatalf("valid token: got %d, want an ICS body", rr.Code)
	}
}
