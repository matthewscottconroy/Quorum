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

	"quorum/internal/auth"
	"quorum/internal/config"
	"quorum/internal/model"
)

func testConfig() *config.Config {
	return &config.Config{
		JWTSecret:     testSecret,
		JWTAccessTTL:  15 * time.Minute,
		JWTRefreshTTL: 168 * time.Hour,
	}
}

func testUser(id, role string) *model.User {
	return &model.User{ID: id, Email: "test@example.com", Role: role}
}

// ---- Login ----

func TestLogin_Success(t *testing.T) {
	hash, _ := auth.HashPassword("goodpassword1")
	repo := &mockAuthRepo{
		GetUserByEmailFn:    func(_ context.Context, _ string) (*model.User, string, error) { return testUser("11111111-1111-1111-1111-111111111111", "admin"), hash, nil },
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
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["access_token"] == nil {
		t.Error("expected access_token in response")
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

func TestLogin_WrongPassword(t *testing.T) {
	hash, _ := auth.HashPassword("correct-password")
	repo := &mockAuthRepo{
		GetUserByEmailFn: func(_ context.Context, _ string) (*model.User, string, error) { return testUser("11111111-1111-1111-1111-111111111111", "member"), hash, nil },
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
			return nil, "", errors.New("no rows")
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
	repo := &mockAuthRepo{
		CreateFirstUserFn: func(_ context.Context, email, hash, role string) (*model.User, error) {
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
}

func TestBootstrap_AlreadyHasUsers(t *testing.T) {
	repo := &mockAuthRepo{
		CreateFirstUserFn: func(_ context.Context, email, hash, role string) (*model.User, error) {
			return nil, errors.New("users table not empty")
		},
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
		GetRefreshTokenFn: func(_ context.Context, h string) (string, bool, time.Time, error) {
			if h != hashed {
				return "", false, time.Time{}, errors.New("not found")
			}
			return userID, false, expires, nil
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
	json.NewDecoder(rr.Body).Decode(&resp)
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

func TestRefresh_InvalidToken(t *testing.T) {
	repo := &mockAuthRepo{
		GetRefreshTokenFn: func(_ context.Context, _ string) (string, bool, time.Time, error) {
			return "", false, time.Time{}, errors.New("not found")
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

func TestRefresh_RevokedToken(t *testing.T) {
	plain, hashed, _ := auth.GenerateRefreshToken()
	repo := &mockAuthRepo{
		GetRefreshTokenFn: func(_ context.Context, h string) (string, bool, time.Time, error) {
			_ = hashed
			return "u", true, time.Now().Add(time.Hour), nil
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
		GetRefreshTokenFn: func(_ context.Context, _ string) (string, bool, time.Time, error) {
			return "u", false, time.Now().Add(-time.Second), nil
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
		CreateUserFn: func(_ context.Context, _, _, role string) (*model.User, error) {
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
	body := `{"email":"a@b.com","password":"pass","role":"superadmin"}`
	req := httptest.NewRequest("POST", "/users", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.CreateUser(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 for invalid role", rr.Code)
	}
}

func TestCreateUser_DefaultsToMember(t *testing.T) {
	var capturedRole string
	repo := &mockAuthRepo{
		CreateUserFn: func(_ context.Context, _, _, role string) (*model.User, error) {
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
		UpdateUserRoleFn: func(_ context.Context, id, _ string) (*model.User, error) {
			return testUser(id, "officer"), nil
		},
	}
	h := NewAuthHandler(repo, testConfig())
	body := `{"role":"officer"}`
	req := chiRequest("PATCH", "/users/u1", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.UpdateUserRole(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200; body: %s", rr.Code, rr.Body)
	}
}

func TestUpdateUserRole_InvalidRole(t *testing.T) {
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())
	body := `{"role":"superadmin"}`
	req := chiRequest("PATCH", "/users/u1", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.UpdateUserRole(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestUpdateUserRole_BadJSON(t *testing.T) {
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())
	req := chiRequest("PATCH", "/users/u1", "{bad", map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.UpdateUserRole(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

// ---- DeleteUser ----

func TestDeleteUser_Success(t *testing.T) {
	deleted := false
	repo := &mockAuthRepo{
		DeleteUserFn: func(_ context.Context, _ string) error { deleted = true; return nil },
	}
	h := NewAuthHandler(repo, testConfig())
	req := chiRequest("DELETE", "/users/u1", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.DeleteUser(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", rr.Code)
	}
	if !deleted {
		t.Error("expected DeleteUser to be called")
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

func TestDeleteUser_RepoError(t *testing.T) {
	repo := &mockAuthRepo{
		DeleteUserFn: func(_ context.Context, _ string) error { return errors.New("db error") },
	}
	h := NewAuthHandler(repo, testConfig())
	req := chiRequest("DELETE", "/users/u1", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
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
		GetPasswordHashFn: func(_ context.Context, _ string) (string, error) { return hash, nil },
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
	json.NewDecoder(rr.Body).Decode(&got)
	if len(got) != 2 {
		t.Errorf("expected 2 users, got %d", len(got))
	}
}
