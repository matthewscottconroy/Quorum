package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quorum/internal/auth"
)

// errCode decodes a JSON error body and returns its "code" field.
func errCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rr.Body.String(), err)
	}
	return body.Code
}

const testSecret = "test-jwt-secret-value"

// Shared valid UUIDs for path-parameter tests (handlers now reject non-UUID ids).
const (
	testUUID  = "11111111-1111-1111-1111-111111111111"
	testUUID2 = "22222222-2222-2222-2222-222222222222"
)

// makeToken issues a signed JWT for use in test requests.
func makeToken(t *testing.T, userID, role string, ttl time.Duration) string {
	t.Helper()
	tok, err := auth.IssueAccessToken(userID, role, "", testSecret, ttl)
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

// withCtxUserMember injects user_id, role, and the linked member_id into the
// request context, mirroring what Middleware.Auth derives from JWT claims.
func withCtxUserMember(r *http.Request, userID, role, memberID string) *http.Request {
	ctx := context.WithValue(r.Context(), ctxUserID, userID)
	ctx = context.WithValue(ctx, ctxRole, role)
	ctx = context.WithValue(ctx, ctxMemberID, memberID)
	return r.WithContext(ctx)
}

// ---- Middleware.Auth ----

func TestMiddlewareAuth_ValidToken(t *testing.T) {
	mw := NewMiddleware(testSecret, false)
	defer mw.Close()
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
	mw := NewMiddleware(testSecret, false)
	defer mw.Close()
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
	mw := NewMiddleware(testSecret, false)
	defer mw.Close()
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
	mw := NewMiddleware("correct-secret", false)
	defer mw.Close()
	tok, _ := auth.IssueAccessToken("u", "member", "", "wrong-secret", time.Minute)
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
	mw := NewMiddleware(testSecret, false)
	defer mw.Close()
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
		{"member", "restricted"},
		{"superadmin", "admin"},
		{"superadmin", "superadmin"},
		{"restricted", "restricted"},
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
	mw := NewMiddleware(testSecret, false)
	defer mw.Close()
	cases := []struct {
		role     string
		required string
	}{
		{"member", "officer"},
		{"member", "admin"},
		{"officer", "admin"},
		{"restricted", "member"},
		{"admin", "superadmin"},
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
	mw := NewMiddleware(testSecret, false)
	// Override with a very low limit (stop the original limiter's goroutine first).
	close(mw.loginLimiter.stop)
	mw.loginLimiter = newRateLimiter(2, time.Minute)
	defer mw.Close()

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

// TestMiddlewareRefreshLimiterIsSeparate verifies the refresh endpoint does not
// drain the tight login bucket: exhausting login must not block refresh.
func TestMiddlewareRefreshLimiterIsSeparate(t *testing.T) {
	mw := NewMiddleware(testSecret, false)
	defer mw.Close()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	// Exhaust the 10/min login bucket for one IP.
	login := mw.LoginRateLimit(next)
	for i := 0; i < 12; i++ {
		req := httptest.NewRequest("POST", "/login", nil)
		req.RemoteAddr = "10.0.0.9:1"
		login.ServeHTTP(httptest.NewRecorder(), req)
	}
	// Refresh from the same IP must still be allowed (separate, higher bucket).
	req := httptest.NewRequest("POST", "/refresh", nil)
	req.RemoteAddr = "10.0.0.9:1"
	rr := httptest.NewRecorder()
	mw.RefreshRateLimit(next).ServeHTTP(rr, req)
	if rr.Code == http.StatusTooManyRequests {
		t.Error("refresh must not be blocked by an exhausted login bucket")
	}
}

// TestMiddlewareTrustProxyKeying verifies that with trustProxy on, the limiter
// keys on the forwarded client IP (so two clients behind one proxy socket get
// separate buckets) and ignores forwarded headers when trustProxy is off.
func TestMiddlewareTrustProxyKeying(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	// trustProxy on: same socket IP, different X-Real-IP → separate buckets.
	mw := NewMiddleware(testSecret, true)
	close(mw.loginLimiter.stop)
	mw.loginLimiter = newRateLimiter(1, time.Minute)
	defer mw.Close()
	h := mw.LoginRateLimit(next)

	req1 := httptest.NewRequest("POST", "/login", nil)
	req1.RemoteAddr = "10.0.0.1:1" // ingress socket
	req1.Header.Set("X-Real-IP", "203.0.113.1")
	rr1 := httptest.NewRecorder()
	h.ServeHTTP(rr1, req1)

	req2 := httptest.NewRequest("POST", "/login", nil)
	req2.RemoteAddr = "10.0.0.1:1" // same ingress socket
	req2.Header.Set("X-Real-IP", "203.0.113.2")
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	if rr2.Code == http.StatusTooManyRequests {
		t.Error("distinct forwarded client IPs must not share a bucket when trustProxy is on")
	}

	// trustProxy off: forwarded header ignored, both requests share the socket bucket.
	mw2 := NewMiddleware(testSecret, false)
	close(mw2.loginLimiter.stop)
	mw2.loginLimiter = newRateLimiter(1, time.Minute)
	defer mw2.Close()
	h2 := mw2.LoginRateLimit(next)

	r1 := httptest.NewRequest("POST", "/login", nil)
	r1.RemoteAddr = "10.0.0.1:1"
	r1.Header.Set("X-Real-IP", "203.0.113.1")
	h2.ServeHTTP(httptest.NewRecorder(), r1)

	r2 := httptest.NewRequest("POST", "/login", nil)
	r2.RemoteAddr = "10.0.0.1:1"
	r2.Header.Set("X-Real-IP", "203.0.113.2")
	rr := httptest.NewRecorder()
	h2.ServeHTTP(rr, r2)
	if rr.Code != http.StatusTooManyRequests {
		t.Error("forwarded headers must be ignored when trustProxy is off; second request should be blocked")
	}
}

func TestMiddlewareLoginRateLimit_IPv6SeparateBuckets(t *testing.T) {
	// The limiter keys via net.SplitHostPort, so bracketed IPv6 addresses must
	// be parsed correctly and distinct addresses must get separate buckets.
	mw := NewMiddleware(testSecret, false) // default limit: 10/min
	defer mw.Close()

	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ })
	handler := mw.LoginRateLimit(next)

	// Exhaust the first IPv6 address's bucket: 10 allowed, 11th blocked.
	for i := 0; i < 11; i++ {
		req := httptest.NewRequest("POST", "/login", nil)
		req.RemoteAddr = "[2001:db8::1]:1234"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if i < 10 && rr.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d from ::1 should be allowed", i+1)
		}
		if i == 10 && rr.Code != http.StatusTooManyRequests {
			t.Fatalf("11th request from ::1 should be rate-limited, got %d", rr.Code)
		}
	}
	if calls != 10 {
		t.Errorf("expected 10 successful calls from ::1, got %d", calls)
	}

	// A different IPv6 address must have its own bucket.
	req := httptest.NewRequest("POST", "/login", nil)
	req.RemoteAddr = "[2001:db8::2]:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code == http.StatusTooManyRequests {
		t.Error("first request from ::2 should not share ::1's bucket")
	}
	if calls != 11 {
		t.Errorf("expected 11 total successful calls, got %d", calls)
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
		// restricted is the lowest rung: below member, but satisfies restricted.
		{"restricted", "member", false},
		{"restricted", "restricted", true},
		{"member", "restricted", true},
		// superadmin tops the ladder.
		{"superadmin", "admin", true},
		{"superadmin", "superadmin", true},
		{"admin", "superadmin", false},
		// unknown roles rank 0, below even restricted.
		{"unknown", "restricted", false},
	}
	for _, tc := range cases {
		got := roleAtLeast(tc.current, tc.required)
		if got != tc.want {
			t.Errorf("roleAtLeast(%q, %q) = %v, want %v", tc.current, tc.required, got, tc.want)
		}
	}
}

// ---- canViewMemberScoped ----

func TestCanViewMemberScoped(t *testing.T) {
	const targetID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	cases := []struct {
		name     string
		role     string
		memberID string // ctx member id for the caller
		want     bool
	}{
		{"member sees any record", "member", "", true},
		{"officer sees any record", "officer", "someone-else", true},
		{"admin sees any record", "admin", "", true},
		{"restricted sees own record", "restricted", targetID, true},
		{"restricted denied other record", "restricted", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", false},
		{"restricted with no linked member denied", "restricted", "", false},
		// A sub-member role only passes when its linked member id matches; a
		// non-matching (or empty) id is denied regardless of the role string.
		{"unknown role, non-matching member denied", "", "cccccccc-cccc-cccc-cccc-cccccccccccc", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := withCtxUserMember(httptest.NewRequest("GET", "/", nil), "u1", tc.role, tc.memberID)
			rr := httptest.NewRecorder()
			got := canViewMemberScoped(rr, req, targetID)
			if got != tc.want {
				t.Errorf("canViewMemberScoped = %v, want %v", got, tc.want)
			}
			if !tc.want && rr.Code != http.StatusForbidden {
				t.Errorf("denied case should write 403, got %d", rr.Code)
			}
		})
	}
}

// ---- confirmMatches ----

func TestConfirmMatches(t *testing.T) {
	cases := []struct {
		name     string
		query    string // raw query string (without leading ?)
		expected string
		want     bool
		wantCode string
	}{
		{"exact match", "confirm=Budget", "Budget", true, ""},
		{"case-insensitive match", "confirm=budget", "Budget", true, ""},
		{"whitespace trimmed", "confirm=%20Budget%20", "Budget", true, ""},
		{"missing confirm", "", "Budget", false, "confirmation_required"},
		{"empty confirm", "confirm=", "Budget", false, "confirmation_required"},
		{"mismatch", "confirm=Wrong", "Budget", false, "confirmation_mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("DELETE", "/x?"+tc.query, nil)
			rr := httptest.NewRecorder()
			got := confirmMatches(rr, req, tc.expected)
			if got != tc.want {
				t.Errorf("confirmMatches = %v, want %v", got, tc.want)
			}
			if !tc.want {
				if rr.Code != http.StatusBadRequest {
					t.Errorf("denied case should write 400, got %d", rr.Code)
				}
				if code := errCode(t, rr); code != tc.wantCode {
					t.Errorf("code: got %q, want %q", code, tc.wantCode)
				}
			}
		})
	}
}
