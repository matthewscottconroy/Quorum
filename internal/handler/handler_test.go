package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quorum/internal/auth"
)

const testSecret = "test-jwt-secret-value"

// makeToken issues a signed JWT for use in test requests.
func makeToken(t *testing.T, userID, role string, ttl time.Duration) string {
	t.Helper()
	tok, err := auth.IssueAccessToken(userID, role, testSecret, ttl)
	if err != nil {
		t.Fatalf("makeToken: %v", err)
	}
	return tok
}

// withAuth adds an Authorization: Bearer header to a request.
func withAuth(r *http.Request, token string) *http.Request {
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// withCtxUser injects user_id and role into the request context directly,
// bypassing JWT parsing for handler tests that don't need to test middleware.
func withCtxUser(r *http.Request, userID, role string) *http.Request {
	ctx := context.WithValue(r.Context(), ctxUserID, userID)
	ctx = context.WithValue(ctx, ctxRole, role)
	return r.WithContext(ctx)
}

// ---- Middleware.Auth ----

func TestMiddlewareAuth_ValidToken(t *testing.T) {
	mw := NewMiddleware(testSecret)
	tok := makeToken(t, "user-1", "admin", time.Minute)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if userIDFromCtx(r) != "user-1" {
			t.Errorf("userID: got %q", userIDFromCtx(r))
		}
		if roleFromCtx(r) != "admin" {
			t.Errorf("role: got %q", roleFromCtx(r))
		}
	})

	req := httptest.NewRequest("GET", "/", nil)
	withAuth(req, tok)
	mw.Auth(next).ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Error("expected next handler to be called")
	}
}

func TestMiddlewareAuth_MissingToken(t *testing.T) {
	mw := NewMiddleware(testSecret)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next should not be called with missing token")
	})
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	mw.Auth(next).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

func TestMiddlewareAuth_ExpiredToken(t *testing.T) {
	mw := NewMiddleware(testSecret)
	tok := makeToken(t, "user-1", "member", -time.Second)
	req := withAuth(httptest.NewRequest("GET", "/", nil), tok)
	rr := httptest.NewRecorder()
	mw.Auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next should not be called with expired token")
	})).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

func TestMiddlewareAuth_WrongSecret(t *testing.T) {
	mw := NewMiddleware("correct-secret")
	tok, _ := auth.IssueAccessToken("u", "member", "wrong-secret", time.Minute)
	req := withAuth(httptest.NewRequest("GET", "/", nil), tok)
	rr := httptest.NewRecorder()
	mw.Auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not reach handler")
	})).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

// ---- Middleware.RequireRole ----

func TestRequireRole_Sufficient(t *testing.T) {
	mw := NewMiddleware(testSecret)
	cases := []struct {
		role     string
		required string
	}{
		{"admin", "member"},
		{"admin", "officer"},
		{"admin", "admin"},
		{"officer", "member"},
		{"officer", "officer"},
		{"member", "member"},
	}
	for _, tc := range cases {
		called := false
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
		req := withCtxUser(httptest.NewRequest("GET", "/", nil), "u1", tc.role)
		rr := httptest.NewRecorder()
		mw.RequireRole(tc.required)(next).ServeHTTP(rr, req)
		if !called {
			t.Errorf("role %q should satisfy required %q", tc.role, tc.required)
		}
	}
}

func TestRequireRole_Insufficient(t *testing.T) {
	mw := NewMiddleware(testSecret)
	cases := []struct {
		role     string
		required string
	}{
		{"member", "officer"},
		{"member", "admin"},
		{"officer", "admin"},
	}
	for _, tc := range cases {
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("role %q should NOT satisfy required %q", tc.role, tc.required)
		})
		req := withCtxUser(httptest.NewRequest("GET", "/", nil), "u1", tc.role)
		rr := httptest.NewRecorder()
		mw.RequireRole(tc.required)(next).ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("role %q / required %q: got %d, want 403", tc.role, tc.required, rr.Code)
		}
	}
}

// ---- SecurityHeaders ----

func TestSecurityHeaders(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	SecurityHeaders(next).ServeHTTP(rr, req)

	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("expected X-Content-Type-Options: nosniff")
	}
	if rr.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("expected X-Frame-Options: DENY")
	}
	csp := rr.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Error("expected Content-Security-Policy header")
	}
}

// ---- Rate limiter ----

func TestRateLimiter_Allow(t *testing.T) {
	rl := newRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !rl.Allow("key") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if rl.Allow("key") {
		t.Error("4th request should be rate-limited")
	}
}

func TestRateLimiter_DifferentKeys(t *testing.T) {
	rl := newRateLimiter(2, time.Minute)
	rl.Allow("a")
	rl.Allow("a")
	// "b" should have its own counter.
	if !rl.Allow("b") {
		t.Error("first request for key 'b' should be allowed")
	}
}

func TestRateLimiter_WindowExpiry(t *testing.T) {
	rl := newRateLimiter(2, 10*time.Millisecond)
	rl.Allow("x")
	rl.Allow("x")
	if rl.Allow("x") {
		t.Error("third request within window should be blocked")
	}
	time.Sleep(20 * time.Millisecond)
	if !rl.Allow("x") {
		t.Error("request after window expiry should be allowed")
	}
}

func TestMiddlewareLoginRateLimit_Blocks(t *testing.T) {
	mw := NewMiddleware(testSecret)
	// Override with a very low limit.
	mw.rateLimiter = newRateLimiter(2, time.Minute)

	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ })

	for i := 0; i < 4; i++ {
		req := httptest.NewRequest("POST", "/login", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		rr := httptest.NewRecorder()
		mw.LoginRateLimit(next).ServeHTTP(rr, req)
	}
	if calls != 2 {
		t.Errorf("expected 2 successful calls before rate limit, got %d", calls)
	}
}

// ---- roleAtLeast ----

func TestRoleAtLeast(t *testing.T) {
	cases := []struct {
		current  string
		required string
		want     bool
	}{
		{"admin", "admin", true},
		{"admin", "officer", true},
		{"admin", "member", true},
		{"officer", "admin", false},
		{"officer", "officer", true},
		{"officer", "member", true},
		{"member", "admin", false},
		{"member", "officer", false},
		{"member", "member", true},
		{"", "member", false},
		{"unknown", "member", false},
	}
	for _, tc := range cases {
		got := roleAtLeast(tc.current, tc.required)
		if got != tc.want {
			t.Errorf("roleAtLeast(%q, %q) = %v, want %v", tc.current, tc.required, got, tc.want)
		}
	}
}
