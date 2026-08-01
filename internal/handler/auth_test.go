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
	"github.com/jackc/pgx/v5/pgconn"

	"quorum/internal/auth"
	"quorum/internal/config"
	"quorum/internal/model"
)

func testConfig() *config.Config {
	return &config.Config{
		JWTSecret:          testSecret,
		JWTAccessTTL:       15 * time.Minute,
		JWTRefreshTTL:      168 * time.Hour,
		IdleTimeoutMinutes: 30,
	}
}

func testUser(id, role string) *model.User {
	return &model.User{ID: id, Email: "test@example.com", Role: role}
}

// ---- Login ----

func TestLogin_Success(t *testing.T) {
	hash, _ := auth.HashPassword("goodpassword1")
	repo := &mockAuthRepo{
		GetUserByEmailFn: func(_ context.Context, _ string) (*model.User, string, error) {
			return testUser("11111111-1111-1111-1111-111111111111", "admin"), hash, nil
		},
		StoreRefreshTokenFn: func(_ context.Context, _, _ string, _ time.Time) error { return nil },
		UpdateLastLoginFn:   func(_ context.Context, _ string) error { return nil },
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"email":"test@example.com","password":"goodpassword1"}`
	req := httptest.NewRequest("POST", "/auth/login", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rr.Code, rr.Body)
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["access_token"] == nil {
		t.Error("expected access_token in response")
	}
	// expires_at is the ACCESS token expiry (15m in testConfig), not the
	// refresh token's 168h lifetime.
	expStr, _ := resp["expires_at"].(string)
	exp, err := time.Parse(time.RFC3339Nano, expStr)
	if err != nil {
		t.Fatalf("expires_at %q not parseable: %v", expStr, err)
	}
	untilExp := time.Until(exp)
	if untilExp > 16*time.Minute || untilExp < 10*time.Minute {
		t.Errorf("expires_at should be ~15m out (access TTL), got %v", untilExp)
	}
	// refresh token is now delivered via HttpOnly cookie, not JSON body
	cookies := rr.Result().Cookies()
	var refreshCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "quorum_refresh" {
			refreshCookie = c
		}
	}
	if refreshCookie == nil {
		t.Error("expected quorum_refresh cookie to be set")
	} else if !refreshCookie.HttpOnly {
		t.Error("expected quorum_refresh cookie to be HttpOnly")
	}
}

func TestLogin_IssuesTokenWithMemberID(t *testing.T) {
	// A member-linked account's access token must carry its MemberID so the
	// Auth middleware can scope `restricted` reads.
	hash, _ := auth.HashPassword("goodpassword1")
	memberID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	u := testUser(testUUID, "restricted")
	u.MemberID = &memberID
	repo := &mockAuthRepo{
		GetUserByEmailFn:    func(_ context.Context, _ string) (*model.User, string, error) { return u, hash, nil },
		StoreRefreshTokenFn: func(_ context.Context, _, _ string, _ time.Time) error { return nil },
		UpdateLastLoginFn:   func(_ context.Context, _ string) error { return nil },
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"email":"test@example.com","password":"goodpassword1"}`
	req := httptest.NewRequest("POST", "/auth/login", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	access, _ := resp["access_token"].(string)
	claims, err := auth.ParseToken(access, testSecret)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if claims.MemberID != memberID {
		t.Errorf("token MemberID: got %q, want %q", claims.MemberID, memberID)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	hash, _ := auth.HashPassword("correct-password")
	repo := &mockAuthRepo{
		GetUserByEmailFn: func(_ context.Context, _ string) (*model.User, string, error) {
			return testUser("11111111-1111-1111-1111-111111111111", "member"), hash, nil
		},
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"email":"test@example.com","password":"wrong-password"}`
	req := httptest.NewRequest("POST", "/auth/login", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

func TestLogin_UserNotFound(t *testing.T) {
	repo := &mockAuthRepo{
		GetUserByEmailFn: func(_ context.Context, _ string) (*model.User, string, error) {
			return nil, "", pgx.ErrNoRows
		},
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"email":"nobody@example.com","password":"pass"}`
	req := httptest.NewRequest("POST", "/auth/login", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

func TestLogin_DBOutage(t *testing.T) {
	// A non-ErrNoRows error (DB down) must be 500, not a misleading 401.
	repo := &mockAuthRepo{
		GetUserByEmailFn: func(_ context.Context, _ string) (*model.User, string, error) {
			return nil, "", errors.New("connection refused")
		},
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"email":"x@example.com","password":"pass"}`
	req := httptest.NewRequest("POST", "/auth/login", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Login(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500 on DB outage", rr.Code)
	}
}

func TestLogin_NormalizesEmail(t *testing.T) {
	// The email must be lowercased and trimmed before the repo lookup.
	var capturedEmail string
	hash, _ := auth.HashPassword("goodpassword1")
	repo := &mockAuthRepo{
		GetUserByEmailFn: func(_ context.Context, email string) (*model.User, string, error) {
			capturedEmail = email
			return testUser(testUUID, "member"), hash, nil
		},
		StoreRefreshTokenFn: func(_ context.Context, _, _ string, _ time.Time) error { return nil },
		UpdateLastLoginFn:   func(_ context.Context, _ string) error { return nil },
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"email":"  Bob@X.com ","password":"goodpassword1"}`
	req := httptest.NewRequest("POST", "/auth/login", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if capturedEmail != "bob@x.com" {
		t.Errorf("email reaching repo: got %q, want %q", capturedEmail, "bob@x.com")
	}
}

func TestLogin_BadJSON(t *testing.T) {
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())
	req := httptest.NewRequest("POST", "/auth/login", strings.NewReader("{bad json"))
	rr := httptest.NewRecorder()
	h.Login(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

// ---- Bootstrap ----

func TestBootstrap_Success(t *testing.T) {
	var capturedRole string
	repo := &mockAuthRepo{
		CreateFirstUserFn: func(_ context.Context, email, hash, role string) (*model.User, error) {
			capturedRole = role
			return testUser("new-id", role), nil
		},
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"email":"admin@example.com","password":"longpassword1"}`
	req := httptest.NewRequest("POST", "/auth/bootstrap", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("status: got %d, want 201; body: %s", rr.Code, rr.Body)
	}
	// The founding account must be created as superadmin (top of the ladder).
	if capturedRole != "superadmin" {
		t.Errorf("bootstrap role reaching repo: got %q, want superadmin", capturedRole)
	}
}

func TestBootstrap_AlreadyHasUsers(t *testing.T) {
	// The CountUsers precheck runs before bcrypt: a non-empty table must 403
	// without CreateFirstUser ever being called (nil fn panics if it is).
	repo := &mockAuthRepo{
		CountUsersFn: func(_ context.Context) (int, error) { return 1, nil },
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"email":"admin@example.com","password":"longpassword1"}`
	req := httptest.NewRequest("POST", "/auth/bootstrap", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", rr.Code)
	}
}

func TestBootstrap_CountUsersError(t *testing.T) {
	repo := &mockAuthRepo{
		CountUsersFn: func(_ context.Context) (int, error) { return 0, errors.New("db down") },
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"email":"admin@example.com","password":"longpassword1"}`
	req := httptest.NewRequest("POST", "/auth/bootstrap", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

func TestBootstrap_LostRace(t *testing.T) {
	// CreateFirstUser returning pgx.ErrNoRows means the atomic INSERT found an
	// existing user (bootstrap race lost) → 403.
	repo := &mockAuthRepo{
		CreateFirstUserFn: func(_ context.Context, _, _, _ string) (*model.User, error) {
			return nil, pgx.ErrNoRows
		},
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"email":"admin@example.com","password":"longpassword1"}`
	req := httptest.NewRequest("POST", "/auth/bootstrap", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 for lost bootstrap race", rr.Code)
	}
}

func TestBootstrap_CreateError(t *testing.T) {
	repo := &mockAuthRepo{
		CreateFirstUserFn: func(_ context.Context, _, _, _ string) (*model.User, error) {
			return nil, errors.New("db error")
		},
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"email":"admin@example.com","password":"longpassword1"}`
	req := httptest.NewRequest("POST", "/auth/bootstrap", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

func TestBootstrap_NormalizesEmail(t *testing.T) {
	var capturedEmail string
	repo := &mockAuthRepo{
		CreateFirstUserFn: func(_ context.Context, email, _, role string) (*model.User, error) {
			capturedEmail = email
			return testUser("new-id", role), nil
		},
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"email":" Admin@Example.COM ","password":"longpassword1"}`
	req := httptest.NewRequest("POST", "/auth/bootstrap", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if capturedEmail != "admin@example.com" {
		t.Errorf("email reaching repo: got %q, want admin@example.com", capturedEmail)
	}
}

func TestBootstrap_PasswordTooShort(t *testing.T) {
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())

	body := `{"email":"a@b.com","password":"short"}`
	req := httptest.NewRequest("POST", "/auth/bootstrap", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestBootstrap_EmptyEmail(t *testing.T) {
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())

	body := `{"email":"","password":"longpassword1"}`
	req := httptest.NewRequest("POST", "/auth/bootstrap", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

// ---- Refresh ----

func TestRefresh_Success(t *testing.T) {
	plain, hashed, _ := auth.GenerateRefreshToken()
	userID := "user-abc"
	expires := time.Now().Add(time.Hour)

	repo := &mockAuthRepo{
		GetRefreshTokenFn: func(_ context.Context, h string) (string, bool, time.Time, time.Time, error) {
			if h != hashed {
				return "", false, time.Time{}, time.Now(), errors.New("not found")
			}
			return userID, false, expires, time.Now(), nil
		},
		RevokeRefreshTokenFn: func(_ context.Context, _ string) error { return nil },
		GetUserByIDFn:        func(_ context.Context, id string) (*model.User, error) { return testUser(id, "member"), nil },
		StoreRefreshTokenFn:  func(_ context.Context, _, _ string, _ time.Time) error { return nil },
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"refresh_token":"` + plain + `"}`
	req := httptest.NewRequest("POST", "/auth/refresh", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rr.Code, rr.Body)
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["access_token"] == nil {
		t.Error("expected access_token in response")
	}
	cookies := rr.Result().Cookies()
	var found bool
	for _, c := range cookies {
		if c.Name == "quorum_refresh" {
			found = true
		}
	}
	if !found {
		t.Error("expected new quorum_refresh cookie after rotation")
	}
}

func TestRefresh_StoresNewTokenBeforeRevokingOld(t *testing.T) {
	// Rotation order matters: a crash between the two calls must never leave
	// the user with zero valid tokens, so the new token is stored first.
	plain, _, _ := auth.GenerateRefreshToken()
	var calls []string
	repo := &mockAuthRepo{
		GetRefreshTokenFn: func(_ context.Context, _ string) (string, bool, time.Time, time.Time, error) {
			return "user-abc", false, time.Now().Add(time.Hour), time.Now(), nil
		},
		GetUserByIDFn: func(_ context.Context, id string) (*model.User, error) { return testUser(id, "member"), nil },
		StoreRefreshTokenFn: func(_ context.Context, _, _ string, _ time.Time) error {
			calls = append(calls, "store")
			return nil
		},
		RevokeRefreshTokenFn: func(_ context.Context, _ string) error {
			calls = append(calls, "revoke")
			return nil
		},
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"refresh_token":"` + plain + `"}`
	req := httptest.NewRequest("POST", "/auth/refresh", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if len(calls) != 2 || calls[0] != "store" || calls[1] != "revoke" {
		t.Errorf("call order: got %v, want [store revoke]", calls)
	}
}

func TestRefresh_StoreFailure(t *testing.T) {
	plain, _, _ := auth.GenerateRefreshToken()
	revokeCalled := false
	repo := &mockAuthRepo{
		GetRefreshTokenFn: func(_ context.Context, _ string) (string, bool, time.Time, time.Time, error) {
			return "user-abc", false, time.Now().Add(time.Hour), time.Now(), nil
		},
		GetUserByIDFn: func(_ context.Context, id string) (*model.User, error) { return testUser(id, "member"), nil },
		StoreRefreshTokenFn: func(_ context.Context, _, _ string, _ time.Time) error {
			return errors.New("db error")
		},
		RevokeRefreshTokenFn: func(_ context.Context, _ string) error {
			revokeCalled = true
			return nil
		},
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"refresh_token":"` + plain + `"}`
	req := httptest.NewRequest("POST", "/auth/refresh", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500 when storing the new token fails", rr.Code)
	}
	if revokeCalled {
		t.Error("old token must not be revoked when the new token could not be stored")
	}
}

func TestRefresh_RevokeFailure(t *testing.T) {
	plain, _, _ := auth.GenerateRefreshToken()
	repo := &mockAuthRepo{
		GetRefreshTokenFn: func(_ context.Context, _ string) (string, bool, time.Time, time.Time, error) {
			return "user-abc", false, time.Now().Add(time.Hour), time.Now(), nil
		},
		GetUserByIDFn:       func(_ context.Context, id string) (*model.User, error) { return testUser(id, "member"), nil },
		StoreRefreshTokenFn: func(_ context.Context, _, _ string, _ time.Time) error { return nil },
		RevokeRefreshTokenFn: func(_ context.Context, _ string) error {
			return errors.New("db error")
		},
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"refresh_token":"` + plain + `"}`
	req := httptest.NewRequest("POST", "/auth/refresh", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500 when revoking the old token fails", rr.Code)
	}
}

func TestRefresh_InvalidToken(t *testing.T) {
	repo := &mockAuthRepo{
		GetRefreshTokenFn: func(_ context.Context, _ string) (string, bool, time.Time, time.Time, error) {
			// Unknown token → no row.
			return "", false, time.Time{}, time.Now(), pgx.ErrNoRows
		},
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"refresh_token":"bad-token"}`
	req := httptest.NewRequest("POST", "/auth/refresh", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

// TestRefresh_LookupOutage verifies a database error (not a missing row) yields
// 500, so a transient outage does not force-log-out every session as 401 would.
func TestRefresh_LookupOutage(t *testing.T) {
	repo := &mockAuthRepo{
		GetRefreshTokenFn: func(_ context.Context, _ string) (string, bool, time.Time, time.Time, error) {
			return "", false, time.Time{}, time.Now(), errors.New("connection refused")
		},
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"refresh_token":"some-token"}`
	req := httptest.NewRequest("POST", "/auth/refresh", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500 on DB outage", rr.Code)
	}
}

func TestRefresh_RevokedToken(t *testing.T) {
	plain, hashed, _ := auth.GenerateRefreshToken()
	repo := &mockAuthRepo{
		GetRefreshTokenFn: func(_ context.Context, h string) (string, bool, time.Time, time.Time, error) {
			_ = hashed
			return "u", true, time.Now().Add(time.Hour), time.Now(), nil
		},
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"refresh_token":"` + plain + `"}`
	req := httptest.NewRequest("POST", "/auth/refresh", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401 for revoked token", rr.Code)
	}
}

func TestRefresh_ExpiredToken(t *testing.T) {
	plain, _, _ := auth.GenerateRefreshToken()
	repo := &mockAuthRepo{
		GetRefreshTokenFn: func(_ context.Context, _ string) (string, bool, time.Time, time.Time, error) {
			return "u", false, time.Now().Add(-time.Second), time.Now(), nil
		},
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"refresh_token":"` + plain + `"}`
	req := httptest.NewRequest("POST", "/auth/refresh", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401 for expired token", rr.Code)
	}
}

func TestRefresh_MissingToken(t *testing.T) {
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())
	req := httptest.NewRequest("POST", "/auth/refresh", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)
	// no cookie and no body token → 401 (missing credentials)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

// ---- Logout ----

func TestLogout_RevokesToken(t *testing.T) {
	revoked := false
	plain, _, _ := auth.GenerateRefreshToken()
	repo := &mockAuthRepo{
		RevokeRefreshTokenFn: func(_ context.Context, _ string) error { revoked = true; return nil },
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"refresh_token":"` + plain + `"}`
	req := httptest.NewRequest("POST", "/auth/logout", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Logout(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", rr.Code)
	}
	if !revoked {
		t.Error("expected RevokeRefreshToken to be called")
	}
}

func TestLogout_EmptyBody(t *testing.T) {
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())
	req := httptest.NewRequest("POST", "/auth/logout", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	h.Logout(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", rr.Code)
	}
}

// ---- Me ----

func TestMe_Success(t *testing.T) {
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, id string) (*model.User, error) { return testUser(id, "member"), nil },
	}
	h := NewAuthHandler(repo, testConfig())
	req := withCtxUser(httptest.NewRequest("GET", "/auth/me", nil), "user-1", "member")
	rr := httptest.NewRecorder()
	h.Me(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
}

func TestMe_NotFound(t *testing.T) {
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, _ string) (*model.User, error) { return nil, errors.New("not found") },
	}
	h := NewAuthHandler(repo, testConfig())
	req := withCtxUser(httptest.NewRequest("GET", "/auth/me", nil), "ghost", "member")
	rr := httptest.NewRecorder()
	h.Me(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", rr.Code)
	}
}

// ---- CreateUser ----

func TestCreateUser_Success(t *testing.T) {
	repo := &mockAuthRepo{
		CreateUserFn: func(_ context.Context, _, _, role string, _ *string) (*model.User, error) {
			return testUser("new", role), nil
		},
	}
	h := NewAuthHandler(repo, testConfig())

	body := `{"email":"new@example.com","password":"securepassword","role":"officer"}`
	req := withCtxUser(httptest.NewRequest("POST", "/users", strings.NewReader(body)), "admin-1", "admin")
	rr := httptest.NewRecorder()
	h.CreateUser(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("status: got %d, want 201; body: %s", rr.Code, rr.Body)
	}
}

func TestCreateUser_InvalidRole(t *testing.T) {
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())
	// "wizard" is not among the five valid roles → 400.
	body := `{"email":"a@b.com","password":"longpass123","role":"wizard"}`
	req := withCtxUser(httptest.NewRequest("POST", "/users", strings.NewReader(body)), "admin-1", "admin")
	rr := httptest.NewRecorder()
	h.CreateUser(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 for invalid role", rr.Code)
	}
}

func TestCreateUser_AdminCreatesMember(t *testing.T) {
	repo := &mockAuthRepo{
		CreateUserFn: func(_ context.Context, _, _, role string, _ *string) (*model.User, error) {
			return testUser("new", role), nil
		},
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"email":"m@b.com","password":"longpass123","role":"member"}`
	req := withCtxUser(httptest.NewRequest("POST", "/users", strings.NewReader(body)), "admin-1", "admin")
	rr := httptest.NewRecorder()
	h.CreateUser(rr, req)
	if rr.Code != http.StatusCreated {
		t.Errorf("status: got %d, want 201; body: %s", rr.Code, rr.Body)
	}
}

func TestCreateUser_AdminCreatingSuperadminForbidden(t *testing.T) {
	// superadmin is a valid role, but only a superadmin may assign it.
	repo := &mockAuthRepo{
		CreateUserFn: func(_ context.Context, _, _, _ string, _ *string) (*model.User, error) {
			t.Error("CreateUser must not be called when an admin attempts to mint a superadmin")
			return nil, nil
		},
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"email":"root@b.com","password":"longpass123","role":"superadmin"}`
	req := withCtxUser(httptest.NewRequest("POST", "/users", strings.NewReader(body)), "admin-1", "admin")
	rr := httptest.NewRecorder()
	h.CreateUser(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", rr.Code)
	}
	if code := errCode(t, rr); code != "forbidden" {
		t.Errorf("code: got %q, want forbidden", code)
	}
}

func TestCreateUser_SuperadminCreatesSuperadmin(t *testing.T) {
	repo := &mockAuthRepo{
		CreateUserFn: func(_ context.Context, _, _, role string, _ *string) (*model.User, error) {
			return testUser("new", role), nil
		},
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"email":"root@b.com","password":"longpass123","role":"superadmin"}`
	req := withCtxUser(httptest.NewRequest("POST", "/users", strings.NewReader(body)), "super-1", "superadmin")
	rr := httptest.NewRecorder()
	h.CreateUser(rr, req)
	if rr.Code != http.StatusCreated {
		t.Errorf("status: got %d, want 201; body: %s", rr.Code, rr.Body)
	}
}

func TestCreateUser_DefaultsToMember(t *testing.T) {
	var capturedRole string
	repo := &mockAuthRepo{
		CreateUserFn: func(_ context.Context, _, _, role string, _ *string) (*model.User, error) {
			capturedRole = role
			return testUser("id", role), nil
		},
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"email":"a@b.com","password":"longpass123"}`
	req := httptest.NewRequest("POST", "/users", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.CreateUser(rr, req)
	if capturedRole != "member" {
		t.Errorf("expected default role 'member', got %q", capturedRole)
	}
}

func TestCreateUser_NormalizesEmail(t *testing.T) {
	var capturedEmail string
	repo := &mockAuthRepo{
		CreateUserFn: func(_ context.Context, email, _, role string, _ *string) (*model.User, error) {
			capturedEmail = email
			return testUser("id", role), nil
		},
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"email":"  New@Example.COM ","password":"longpass123"}`
	req := httptest.NewRequest("POST", "/users", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.CreateUser(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if capturedEmail != "new@example.com" {
		t.Errorf("email reaching repo: got %q, want new@example.com", capturedEmail)
	}
}

func TestCreateUser_MissingEmail(t *testing.T) {
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())
	body := `{"password":"longpass123"}`
	req := httptest.NewRequest("POST", "/users", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.CreateUser(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

// ---- UpdateUserRole ----

func TestUpdateUserRole_Success(t *testing.T) {
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, id string) (*model.User, error) { return testUser(id, "member"), nil },
		UpdateUserRoleFn: func(_ context.Context, id, _ string) (*model.User, error) {
			return testUser(id, "officer"), nil
		},
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"role":"officer"}`
	req := withCtxUser(chiRequest("PATCH", "/users/u1", body, map[string]string{"id": testUUID}), "admin-1", "admin")
	rr := httptest.NewRecorder()
	h.UpdateUser(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200; body: %s", rr.Code, rr.Body)
	}
}

func TestUpdateUserRole_SelfChangeForbidden(t *testing.T) {
	const selfID = "11111111-1111-1111-1111-111111111111"
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())
	body := `{"role":"member"}`
	req := withCtxUser(chiRequest("PATCH", "/users/"+selfID, body, map[string]string{"id": selfID}), selfID, "admin")
	rr := httptest.NewRecorder()
	h.UpdateUser(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 for changing your own role", rr.Code)
	}
}

func TestUpdateUserRole_TargetSuperadminRequiresSuperadmin(t *testing.T) {
	// Demoting an existing superadmin is a superadmin-only action.
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, id string) (*model.User, error) { return testUser(id, "superadmin"), nil },
		UpdateUserRoleFn: func(_ context.Context, _, _ string) (*model.User, error) {
			t.Error("UpdateUserRole must not run when a non-superadmin targets a superadmin")
			return nil, nil
		},
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"role":"member"}`
	req := withCtxUser(chiRequest("PATCH", "/users/u1", body, map[string]string{"id": testUUID}), "admin-1", "admin")
	rr := httptest.NewRecorder()
	h.UpdateUser(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", rr.Code)
	}
}

func TestUpdateUserRole_PromoteToSuperadminRequiresSuperadmin(t *testing.T) {
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, id string) (*model.User, error) { return testUser(id, "member"), nil },
		UpdateUserRoleFn: func(_ context.Context, _, _ string) (*model.User, error) {
			t.Error("UpdateUserRole must not run when a non-superadmin grants superadmin")
			return nil, nil
		},
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"role":"superadmin"}`
	req := withCtxUser(chiRequest("PATCH", "/users/u1", body, map[string]string{"id": testUUID}), "admin-1", "admin")
	rr := httptest.NewRecorder()
	h.UpdateUser(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", rr.Code)
	}
}

func TestUpdateUserRole_SuperadminMayPromoteToSuperadmin(t *testing.T) {
	repo := &mockAuthRepo{
		GetUserByIDFn:    func(_ context.Context, id string) (*model.User, error) { return testUser(id, "member"), nil },
		UpdateUserRoleFn: func(_ context.Context, id, role string) (*model.User, error) { return testUser(id, role), nil },
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"role":"superadmin"}`
	req := withCtxUser(chiRequest("PATCH", "/users/u1", body, map[string]string{"id": testUUID}), "super-1", "superadmin")
	rr := httptest.NewRecorder()
	h.UpdateUser(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200; body: %s", rr.Code, rr.Body)
	}
}

func TestUpdateUserRole_InvalidRole(t *testing.T) {
	// The role is validated before the target is fetched, so "wizard" 400s
	// without GetUserByID being called.
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())
	body := `{"role":"wizard"}`
	req := withCtxUser(chiRequest("PATCH", "/users/u1", body, map[string]string{"id": testUUID}), "admin-1", "admin")
	rr := httptest.NewRecorder()
	h.UpdateUser(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestUpdateUserRole_NotFound(t *testing.T) {
	// The target fetch (GetUserByID) returning ErrNoRows surfaces as 404.
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, _ string) (*model.User, error) { return nil, pgx.ErrNoRows },
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"role":"officer"}`
	req := withCtxUser(chiRequest("PATCH", "/users/u1", body, map[string]string{"id": testUUID2}), "admin-1", "admin")
	rr := httptest.NewRecorder()
	h.UpdateUser(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404 for ErrNoRows", rr.Code)
	}
}

func TestUpdateUserRole_RepoError(t *testing.T) {
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, id string) (*model.User, error) { return testUser(id, "member"), nil },
		UpdateUserRoleFn: func(_ context.Context, _, _ string) (*model.User, error) {
			return nil, errors.New("db error")
		},
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"role":"officer"}`
	req := withCtxUser(chiRequest("PATCH", "/users/u1", body, map[string]string{"id": testUUID}), "admin-1", "admin")
	rr := httptest.NewRecorder()
	h.UpdateUser(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

func TestUpdateUserRole_BadJSON(t *testing.T) {
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())
	req := chiRequest("PATCH", "/users/u1", "{bad", map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.UpdateUser(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

// ---- DeleteUser ----

func TestDeleteUser_Success(t *testing.T) {
	deleted := false
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, id string) (*model.User, error) { return testUser(id, "member"), nil },
		DeleteUserFn:  func(_ context.Context, _ string) error { deleted = true; return nil },
	}
	h := NewAuthHandler(repo, testConfig())
	// Type-to-confirm gate: echo the target's email (testUser => test@example.com).
	req := chiRequest("DELETE", "/users/u1?confirm=test@example.com", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.DeleteUser(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("status: got %d, want 204; body: %s", rr.Code, rr.Body)
	}
	if !deleted {
		t.Error("expected DeleteUser to be called")
	}
}

func TestDeleteUser_NotifiesTargetUser(t *testing.T) {
	fn := &fakeNotifier{}
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, id string) (*model.User, error) { return testUser(id, "member"), nil },
		DeleteUserFn:  func(_ context.Context, _ string) error { return nil },
	}
	h := NewAuthHandler(repo, testConfig())
	h.SetNotifier(fn)
	req := withCtxUser(chiRequest("DELETE", "/users/u1?confirm=test@example.com", "", map[string]string{"id": testUUID}), "actor-1", "superadmin")
	rr := httptest.NewRecorder()
	h.DeleteUser(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	// The deleted user's own email is included as an affected party.
	if !fn.called || fn.entityType != "user account" || len(fn.affected) != 1 || fn.affected[0] != "test@example.com" {
		t.Errorf("notifier: called=%v type=%q affected=%v", fn.called, fn.entityType, fn.affected)
	}
}

func TestDeleteUser_MissingConfirm(t *testing.T) {
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, id string) (*model.User, error) { return testUser(id, "member"), nil },
		DeleteUserFn: func(_ context.Context, _ string) error {
			t.Error("delete must not run without confirmation")
			return nil
		},
	}
	h := NewAuthHandler(repo, testConfig())
	req := chiRequest("DELETE", "/users/u1", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.DeleteUser(rr, req)
	if rr.Code != http.StatusBadRequest || errCode(t, rr) != "confirmation_required" {
		t.Errorf("status/code: got %d/%s, want 400/confirmation_required", rr.Code, rr.Body)
	}
}

func TestDeleteUser_WrongConfirm(t *testing.T) {
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, id string) (*model.User, error) { return testUser(id, "member"), nil },
		DeleteUserFn:  func(_ context.Context, _ string) error { t.Error("delete must not run on mismatch"); return nil },
	}
	h := NewAuthHandler(repo, testConfig())
	req := chiRequest("DELETE", "/users/u1?confirm=wrong@example.com", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.DeleteUser(rr, req)
	if rr.Code != http.StatusBadRequest || errCode(t, rr) != "confirmation_mismatch" {
		t.Errorf("status/code: got %d/%s, want 400/confirmation_mismatch", rr.Code, rr.Body)
	}
}

func TestDeleteUser_SelfDelete(t *testing.T) {
	const selfID = "11111111-1111-1111-1111-111111111111"
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())
	req := withCtxUser(
		chiRequest("DELETE", "/users/"+selfID, "", map[string]string{"id": selfID}),
		selfID, "admin",
	)
	rr := httptest.NewRecorder()
	h.DeleteUser(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 for self-delete", rr.Code)
	}
}

func TestDeleteUser_NotFound(t *testing.T) {
	// The target fetch (GetUserByID) returning ErrNoRows surfaces as 404.
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, _ string) (*model.User, error) { return nil, pgx.ErrNoRows },
	}
	h := NewAuthHandler(repo, testConfig())
	req := chiRequest("DELETE", "/users/u1?confirm=test@example.com", "", map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.DeleteUser(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404 for ErrNoRows", rr.Code)
	}
}

func TestDeleteUser_FKViolation(t *testing.T) {
	// A user that still owns records surfaces as SQLSTATE 23503 → 409.
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, id string) (*model.User, error) { return testUser(id, "member"), nil },
		DeleteUserFn: func(_ context.Context, _ string) error {
			return &pgconn.PgError{Code: "23503"}
		},
	}
	h := NewAuthHandler(repo, testConfig())
	req := chiRequest("DELETE", "/users/u1?confirm=test@example.com", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.DeleteUser(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409 for FK violation", rr.Code)
	}
}

func TestDeleteUser_RepoError(t *testing.T) {
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, id string) (*model.User, error) { return testUser(id, "member"), nil },
		DeleteUserFn:  func(_ context.Context, _ string) error { return errors.New("db error") },
	}
	h := NewAuthHandler(repo, testConfig())
	req := chiRequest("DELETE", "/users/u1?confirm=test@example.com", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.DeleteUser(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

// ---- ChangePassword ----

func TestChangePassword_Success(t *testing.T) {
	hash, _ := auth.HashPassword("currentpass1!")
	revoked := false
	repo := &mockAuthRepo{
		GetPasswordHashFn:    func(_ context.Context, _ string) (string, error) { return hash, nil },
		UpdatePasswordHashFn: func(_ context.Context, _, _ string) error { return nil },
		RevokeAllRefreshTokensForUserFn: func(_ context.Context, _ string) error {
			revoked = true
			return nil
		},
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"current_password":"currentpass1!","new_password":"newpassword123"}`
	req := withCtxUser(httptest.NewRequest("PATCH", "/auth/me/password", strings.NewReader(body)), "11111111-1111-1111-1111-111111111111", "member")
	rr := httptest.NewRecorder()
	h.ChangePassword(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("status: got %d, want 204; body: %s", rr.Code, rr.Body)
	}
	if !revoked {
		t.Error("expected RevokeAllRefreshTokensForUser to be called after password change")
	}
}

func TestChangePassword_WrongCurrentPassword(t *testing.T) {
	hash, _ := auth.HashPassword("correctpass1")
	repo := &mockAuthRepo{
		GetPasswordHashFn: func(_ context.Context, _ string) (string, error) { return hash, nil },
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"current_password":"wrongpass","new_password":"newpassword123"}`
	req := withCtxUser(httptest.NewRequest("PATCH", "/auth/me/password", strings.NewReader(body)), "11111111-1111-1111-1111-111111111111", "member")
	rr := httptest.NewRecorder()
	h.ChangePassword(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

func TestChangePassword_NewPasswordTooShort(t *testing.T) {
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())
	body := `{"current_password":"anything","new_password":"short"}`
	req := withCtxUser(httptest.NewRequest("PATCH", "/auth/me/password", strings.NewReader(body)), "11111111-1111-1111-1111-111111111111", "member")
	rr := httptest.NewRecorder()
	h.ChangePassword(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestChangePassword_MissingFields(t *testing.T) {
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())
	body := `{"current_password":""}`
	req := withCtxUser(httptest.NewRequest("PATCH", "/auth/me/password", strings.NewReader(body)), "11111111-1111-1111-1111-111111111111", "member")
	rr := httptest.NewRecorder()
	h.ChangePassword(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

// ---- ListUsers ----

func TestListUsers_ReturnsUsers(t *testing.T) {
	users := []model.User{*testUser("11111111-1111-1111-1111-111111111111", "admin"), *testUser("u2", "member")}
	repo := &mockAuthRepo{
		ListUsersFn: func(_ context.Context) ([]model.User, error) { return users, nil },
	}
	h := NewAuthHandler(repo, testConfig())
	req := httptest.NewRequest("GET", "/users", nil)
	rr := httptest.NewRecorder()
	h.ListUsers(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
	var got []model.User
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 users, got %d", len(got))
	}
}

// ---- member-link provisioning (restricted role) ----

func TestCreateUser_WithMemberID(t *testing.T) {
	var captured *string
	repo := &mockAuthRepo{
		CreateUserFn: func(_ context.Context, _, _, role string, memberID *string) (*model.User, error) {
			captured = memberID
			return testUser("new", role), nil
		},
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"email":"r@example.com","password":"longpassword1","role":"restricted","member_id":"` + testUUID + `"}`
	req := withCtxUser(httptest.NewRequest("POST", "/users", strings.NewReader(body)), "admin-1", "admin")
	rr := httptest.NewRecorder()
	h.CreateUser(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if captured == nil || *captured != testUUID {
		t.Errorf("member_id reaching repo: got %v, want %s", captured, testUUID)
	}
}

func TestCreateUser_InvalidMemberID(t *testing.T) {
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())
	body := `{"email":"r@example.com","password":"longpassword1","member_id":"not-a-uuid"}`
	req := withCtxUser(httptest.NewRequest("POST", "/users", strings.NewReader(body)), "admin-1", "admin")
	rr := httptest.NewRecorder()
	h.CreateUser(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestCreateUser_MemberFKViolation(t *testing.T) {
	repo := &mockAuthRepo{
		CreateUserFn: func(_ context.Context, _, _, _ string, _ *string) (*model.User, error) {
			return nil, &pgconn.PgError{Code: "23503"}
		},
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"email":"r@example.com","password":"longpassword1","member_id":"` + testUUID + `"}`
	req := withCtxUser(httptest.NewRequest("POST", "/users", strings.NewReader(body)), "admin-1", "admin")
	rr := httptest.NewRecorder()
	h.CreateUser(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 for a bad member_id FK", rr.Code)
	}
}

func TestUpdateUser_SetsMemberLink(t *testing.T) {
	var captured *string
	linked := false
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, id string) (*model.User, error) { return testUser(id, "restricted"), nil },
		SetUserMemberFn: func(_ context.Context, _ string, memberID *string) error {
			linked = true
			captured = memberID
			return nil
		},
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"member_id":"` + testUUID2 + `"}`
	req := withCtxUser(chiRequest("PATCH", "/users/u1", body, map[string]string{"id": testUUID}), "admin-1", "admin")
	rr := httptest.NewRecorder()
	h.UpdateUser(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if !linked || captured == nil || *captured != testUUID2 {
		t.Errorf("SetUserMember: linked=%v captured=%v", linked, captured)
	}
}

func TestUpdateUser_UnlinksMember(t *testing.T) {
	captured := new(string)
	repo := &mockAuthRepo{
		GetUserByIDFn:   func(_ context.Context, id string) (*model.User, error) { return testUser(id, "restricted"), nil },
		SetUserMemberFn: func(_ context.Context, _ string, memberID *string) error { captured = memberID; return nil },
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"member_id":null}`
	req := withCtxUser(chiRequest("PATCH", "/users/u1", body, map[string]string{"id": testUUID}), "admin-1", "admin")
	rr := httptest.NewRecorder()
	h.UpdateUser(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if captured != nil {
		t.Errorf("unlink should pass nil member_id, got %v", *captured)
	}
}

func TestUpdateUser_NeitherField(t *testing.T) {
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())
	req := withCtxUser(chiRequest("PATCH", "/users/u1", `{}`, map[string]string{"id": testUUID}), "admin-1", "admin")
	rr := httptest.NewRecorder()
	h.UpdateUser(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 when neither role nor member_id present", rr.Code)
	}
}

// A refresh token older than the idle window is refused and revoked: rotation
// makes token age equal time-since-last-activity, so this IS the idle logout.
func TestRefresh_IdleTimeout(t *testing.T) {
	revoked := false
	repo := &mockAuthRepo{
		GetRefreshTokenFn: func(_ context.Context, _ string) (string, bool, time.Time, time.Time, error) {
			// Valid, unrevoked, but minted 31 minutes ago (idle window is 30).
			return "user-abc", false, time.Now().Add(6 * 24 * time.Hour), time.Now().Add(-31 * time.Minute), nil
		},
		RevokeRefreshTokenFn: func(_ context.Context, _ string) error { revoked = true; return nil },
	}
	h := NewAuthHandler(repo, testConfig())
	req := httptest.NewRequest("POST", "/auth/refresh", strings.NewReader(`{"refresh_token":"tok"}`))
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("idle session refresh should 401, got %d: %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), "inactivity") {
		t.Errorf("response should explain the idle logout: %s", rr.Body)
	}
	if !revoked {
		t.Error("the stale token must be revoked, not left usable")
	}
	// A recently-active session refreshes fine.
	repo2 := &mockAuthRepo{
		GetRefreshTokenFn: func(_ context.Context, _ string) (string, bool, time.Time, time.Time, error) {
			return testUserID, false, time.Now().Add(6 * 24 * time.Hour), time.Now().Add(-5 * time.Minute), nil
		},
		GetUserByIDFn:        func(_ context.Context, id string) (*model.User, error) { return testUser(id, "member"), nil },
		StoreRefreshTokenFn:  func(_ context.Context, _, _ string, _ time.Time) error { return nil },
		RevokeRefreshTokenFn: func(_ context.Context, _ string) error { return nil },
	}
	h2 := NewAuthHandler(repo2, testConfig())
	rr2 := httptest.NewRecorder()
	h2.Refresh(rr2, httptest.NewRequest("POST", "/auth/refresh", strings.NewReader(`{"refresh_token":"tok"}`)))
	if rr2.Code != http.StatusOK {
		t.Fatalf("active session should refresh, got %d: %s", rr2.Code, rr2.Body)
	}
}
