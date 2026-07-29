package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"quorum/internal/auth"
	"quorum/internal/model"
)

const testUserID = "11111111-1111-1111-1111-111111111111"

// ---- Login with 2FA enabled ----

func TestLogin_2FAEnabled_ReturnsChallenge(t *testing.T) {
	hash, _ := auth.HashPassword("goodpassword1")
	repo := &mockAuthRepo{
		GetUserByEmailFn: func(_ context.Context, _ string) (*model.User, string, error) {
			return testUser(testUserID, "member"), hash, nil
		},
		GetTOTPFn: func(_ context.Context, _ string) (string, bool, error) { return "SECRET", true, nil },
	}
	h := NewAuthHandler(repo, testConfig())
	req := httptest.NewRequest("POST", "/auth/login", strings.NewReader(`{"email":"t@e.com","password":"goodpassword1"}`))
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["mfa_required"] != true {
		t.Errorf("expected mfa_required=true, got %v", resp)
	}
	if resp["access_token"] != nil {
		t.Error("must NOT issue an access token before the second factor")
	}
	tok, _ := resp["mfa_token"].(string)
	claims, err := auth.ParseToken(tok, testSecret)
	if err != nil || claims.Purpose != auth.PurposeMFA {
		t.Errorf("mfa_token should be a Purpose=mfa token, got %v err=%v", claims, err)
	}
	// The interim token must be rejected by the API middleware.
	if len(rr.Result().Cookies()) != 0 {
		t.Error("no refresh cookie should be set at the challenge step")
	}
}

// ---- LoginMFA ----

func TestLoginMFA_ValidCode(t *testing.T) {
	secret, _ := auth.GenerateTOTPSecret()
	code, _ := totpNow(secret)
	repo := &mockAuthRepo{
		GetTOTPFn:           func(_ context.Context, _ string) (string, bool, error) { return secret, true, nil },
		GetUserByIDFn:       func(_ context.Context, _ string) (*model.User, error) { return testUser(testUserID, "member"), nil },
		StoreRefreshTokenFn: func(_ context.Context, _, _ string, _ time.Time) error { return nil },
		UpdateLastLoginFn:   func(_ context.Context, _ string) error { return nil },
	}
	h := NewAuthHandler(repo, testConfig())
	mfaTok, _ := auth.IssueMFAToken(testUserID, testSecret, 5*time.Minute)
	body := `{"mfa_token":"` + mfaTok + `","code":"` + code + `"}`
	req := httptest.NewRequest("POST", "/auth/login/2fa", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.LoginMFA(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["access_token"] == nil {
		t.Error("expected an access token after a valid second factor")
	}
}

func TestLoginMFA_BadCodeRejected(t *testing.T) {
	secret, _ := auth.GenerateTOTPSecret()
	repo := &mockAuthRepo{
		GetTOTPFn: func(_ context.Context, _ string) (string, bool, error) { return secret, true, nil },
	}
	h := NewAuthHandler(repo, testConfig())
	mfaTok, _ := auth.IssueMFAToken(testUserID, testSecret, 5*time.Minute)
	req := httptest.NewRequest("POST", "/auth/login/2fa", strings.NewReader(`{"mfa_token":"`+mfaTok+`","code":"000000"}`))
	rr := httptest.NewRecorder()
	h.LoginMFA(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body)
	}
}

func TestLoginMFA_RejectsAccessTokenAsMFAToken(t *testing.T) {
	repo := &mockAuthRepo{
		GetTOTPFn: func(_ context.Context, _ string) (string, bool, error) { return "S", true, nil },
	}
	h := NewAuthHandler(repo, testConfig())
	// A normal access token (Purpose empty) must not authorize the second step.
	access, _ := auth.IssueAccessToken(testUserID, "member", "", testSecret, time.Minute)
	req := httptest.NewRequest("POST", "/auth/login/2fa", strings.NewReader(`{"mfa_token":"`+access+`","code":"123456"}`))
	rr := httptest.NewRecorder()
	h.LoginMFA(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a non-mfa token, got %d", rr.Code)
	}
}

func TestLoginMFA_RecoveryCode(t *testing.T) {
	var consumedHash string
	repo := &mockAuthRepo{
		GetTOTPFn: func(_ context.Context, _ string) (string, bool, error) { return "SECRET", true, nil },
		ConsumeRecoveryCodeFn: func(_ context.Context, _, hash string) (bool, error) {
			consumedHash = hash
			return true, nil
		},
		GetUserByIDFn:       func(_ context.Context, _ string) (*model.User, error) { return testUser(testUserID, "member"), nil },
		StoreRefreshTokenFn: func(_ context.Context, _, _ string, _ time.Time) error { return nil },
		UpdateLastLoginFn:   func(_ context.Context, _ string) error { return nil },
	}
	h := NewAuthHandler(repo, testConfig())
	mfaTok, _ := auth.IssueMFAToken(testUserID, testSecret, 5*time.Minute)
	req := httptest.NewRequest("POST", "/auth/login/2fa", strings.NewReader(`{"mfa_token":"`+mfaTok+`","recovery_code":"ABCDE234"}`))
	rr := httptest.NewRecorder()
	h.LoginMFA(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if consumedHash != auth.HashRefreshToken("ABCDE234") {
		t.Errorf("recovery code should be looked up by its hash, got %q", consumedHash)
	}
}

// ---- 2FA enrollment ----

func TestSetupAndEnable2FA(t *testing.T) {
	var stored string
	enabled := false
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, _ string) (*model.User, error) { return testUser(testUserID, "member"), nil },
		GetTOTPFn:     func(_ context.Context, _ string) (string, bool, error) { return stored, enabled, nil },
		SetTOTPSecretFn: func(_ context.Context, _, secret string) error {
			stored = secret
			return nil
		},
		ReplaceRecoveryCodesFn: func(_ context.Context, _ string, _ []string) error { return nil },
		EnableTOTPFn:           func(_ context.Context, _ string) error { enabled = true; return nil },
	}
	h := NewAuthHandler(repo, testConfig())

	// Setup returns a secret + provisioning URI.
	rr := httptest.NewRecorder()
	h.Setup2FA(rr, withCtxUser(httptest.NewRequest("POST", "/auth/2fa/setup", nil), testUserID, "member"))
	if rr.Code != http.StatusOK {
		t.Fatalf("setup status %d: %s", rr.Code, rr.Body)
	}
	var setupResp map[string]string
	json.NewDecoder(rr.Body).Decode(&setupResp)
	if setupResp["secret"] == "" || !strings.HasPrefix(setupResp["provisioning_uri"], "otpauth://") {
		t.Fatalf("bad setup response: %v", setupResp)
	}

	// Enable with a valid code returns recovery codes and flips enabled.
	code, _ := totpNow(setupResp["secret"])
	rr2 := httptest.NewRecorder()
	h.Enable2FA(rr2, withCtxUser(httptest.NewRequest("POST", "/auth/2fa/enable", strings.NewReader(`{"code":"`+code+`"}`)), testUserID, "member"))
	if rr2.Code != http.StatusOK {
		t.Fatalf("enable status %d: %s", rr2.Code, rr2.Body)
	}
	var enResp struct {
		Enabled       bool     `json:"enabled"`
		RecoveryCodes []string `json:"recovery_codes"`
	}
	json.NewDecoder(rr2.Body).Decode(&enResp)
	if !enResp.Enabled || len(enResp.RecoveryCodes) != recoveryCodeCount {
		t.Errorf("expected enabled with %d recovery codes, got %+v", recoveryCodeCount, enResp)
	}
	if !enabled {
		t.Error("EnableTOTP should have been called")
	}
}

func TestEnable2FA_BadCode(t *testing.T) {
	secret, _ := auth.GenerateTOTPSecret()
	repo := &mockAuthRepo{
		GetTOTPFn: func(_ context.Context, _ string) (string, bool, error) { return secret, false, nil },
	}
	h := NewAuthHandler(repo, testConfig())
	rr := httptest.NewRecorder()
	h.Enable2FA(rr, withCtxUser(httptest.NewRequest("POST", "/auth/2fa/enable", strings.NewReader(`{"code":"000000"}`)), testUserID, "member"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body)
	}
}

func TestDisable2FA_RequiresPassword(t *testing.T) {
	hash, _ := auth.HashPassword("goodpassword1")
	disabled := false
	repo := &mockAuthRepo{
		GetPasswordHashFn: func(_ context.Context, _ string) (string, error) { return hash, nil },
		DisableTOTPFn:     func(_ context.Context, _ string) error { disabled = true; return nil },
	}
	h := NewAuthHandler(repo, testConfig())

	// Wrong password → 401, no disable.
	rr := httptest.NewRecorder()
	h.Disable2FA(rr, withCtxUser(httptest.NewRequest("POST", "/auth/2fa/disable", strings.NewReader(`{"password":"wrong"}`)), testUserID, "member"))
	if rr.Code != http.StatusUnauthorized || disabled {
		t.Fatalf("wrong password should 401 and not disable; code=%d disabled=%v", rr.Code, disabled)
	}
	// Correct password → 204, disabled.
	rr2 := httptest.NewRecorder()
	h.Disable2FA(rr2, withCtxUser(httptest.NewRequest("POST", "/auth/2fa/disable", strings.NewReader(`{"password":"goodpassword1"}`)), testUserID, "member"))
	if rr2.Code != http.StatusNoContent || !disabled {
		t.Fatalf("correct password should 204 and disable; code=%d disabled=%v", rr2.Code, disabled)
	}
}

// ---- Password recovery ----

func TestForgotPassword_AlwaysOK_SendsWhenKnown(t *testing.T) {
	m := &captureMailer{}
	repo := &mockAuthRepo{
		GetUserByEmailFn: func(_ context.Context, _ string) (*model.User, string, error) {
			return testUser(testUserID, "member"), "hash", nil
		},
		CreatePasswordResetTokenFn: func(_ context.Context, _, _ string, _ time.Time) error { return nil },
	}
	h := NewAuthHandler(repo, testConfig())
	h.SetMailer(m)
	rr := httptest.NewRecorder()
	h.ForgotPassword(rr, httptest.NewRequest("POST", "/auth/forgot-password", strings.NewReader(`{"email":"known@e.com"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	if !m.waitForCount(1) {
		t.Fatalf("expected one email, got %d", m.count())
	}
	if first, _ := m.first(); !strings.Contains(first.body, "/#/reset-password?token=") {
		t.Errorf("email should carry a reset link, got: %s", first.body)
	}
}

func TestForgotPassword_UnknownEmail_NoLeak(t *testing.T) {
	m := &captureMailer{}
	repo := &mockAuthRepo{
		GetUserByEmailFn: func(_ context.Context, _ string) (*model.User, string, error) {
			return nil, "", pgx.ErrNoRows
		},
	}
	h := NewAuthHandler(repo, testConfig())
	h.SetMailer(m)
	rr := httptest.NewRecorder()
	h.ForgotPassword(rr, httptest.NewRequest("POST", "/auth/forgot-password", strings.NewReader(`{"email":"nobody@e.com"}`)))
	// Same 200 as the known-email path, and no email sent.
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d — must not reveal that the account is unknown", rr.Code)
	}
	time.Sleep(30 * time.Millisecond) // give any (erroneous) async send a chance to fire
	if m.count() != 0 {
		t.Errorf("must not send mail for an unknown account")
	}
}

func TestResetPassword_ConsumesTokenAndRevokes(t *testing.T) {
	var (
		gotHash    string
		newHash    string
		revoked    bool
		wantUserID = testUserID
	)
	repo := &mockAuthRepo{
		ConsumePasswordResetTokenFn: func(_ context.Context, hash string) (string, error) {
			gotHash = hash
			return wantUserID, nil
		},
		UpdatePasswordHashFn: func(_ context.Context, _, h string) error { newHash = h; return nil },
		RevokeAllRefreshTokensForUserFn: func(_ context.Context, uid string) error {
			if uid == wantUserID {
				revoked = true
			}
			return nil
		},
	}
	h := NewAuthHandler(repo, testConfig())
	rr := httptest.NewRecorder()
	h.ResetPassword(rr, httptest.NewRequest("POST", "/auth/reset-password", strings.NewReader(`{"token":"plaintoken","new_password":"brandnewpass1"}`)))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if gotHash != auth.HashResetToken("plaintoken") {
		t.Errorf("token should be consumed by its hash")
	}
	if !auth.CheckPassword(newHash, "brandnewpass1") {
		t.Errorf("new password hash should verify")
	}
	if !revoked {
		t.Errorf("all sessions should be revoked after reset")
	}
}

func TestResetPassword_InvalidToken(t *testing.T) {
	repo := &mockAuthRepo{
		ConsumePasswordResetTokenFn: func(_ context.Context, _ string) (string, error) { return "", pgx.ErrNoRows },
	}
	h := NewAuthHandler(repo, testConfig())
	rr := httptest.NewRecorder()
	h.ResetPassword(rr, httptest.NewRequest("POST", "/auth/reset-password", strings.NewReader(`{"token":"bad","new_password":"brandnewpass1"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestResetPassword_ShortPassword(t *testing.T) {
	h := NewAuthHandler(&mockAuthRepo{}, testConfig())
	rr := httptest.NewRecorder()
	h.ResetPassword(rr, httptest.NewRequest("POST", "/auth/reset-password", strings.NewReader(`{"token":"x","new_password":"short"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a short password, got %d", rr.Code)
	}
}

func TestAdminResetPassword_GeneratesTempPassword(t *testing.T) {
	var revoked bool
	repo := &mockAuthRepo{
		GetUserByIDFn:                   func(_ context.Context, _ string) (*model.User, error) { return testUser(testUserID, "member"), nil },
		UpdatePasswordHashFn:            func(_ context.Context, _, _ string) error { return nil },
		RevokeAllRefreshTokensForUserFn: func(_ context.Context, _ string) error { revoked = true; return nil },
	}
	h := NewAuthHandler(repo, testConfig())
	req := reqWithParam("POST", "/users/"+testUserID+"/reset-password", "", map[string]string{"id": testUserID})
	req = withCtxUser(req, "admin-id", "admin")
	rr := httptest.NewRecorder()
	h.AdminResetPassword(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if tp, _ := resp["temporary_password"].(string); len(tp) < 10 {
		t.Errorf("expected a strong temporary password, got %q", tp)
	}
	if !revoked {
		t.Error("target sessions should be revoked")
	}
}

func TestAdminResetPassword_SuperadminTargetGated(t *testing.T) {
	repo := &mockAuthRepo{
		GetUserByIDFn: func(_ context.Context, _ string) (*model.User, error) { return testUser(testUserID, "superadmin"), nil },
	}
	h := NewAuthHandler(repo, testConfig())
	req := reqWithParam("POST", "/users/"+testUserID+"/reset-password", "", map[string]string{"id": testUserID})
	req = withCtxUser(req, "admin-id", "admin") // actor is only admin
	rr := httptest.NewRecorder()
	h.AdminResetPassword(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("an admin must not reset a superadmin's password; got %d", rr.Code)
	}
}

// ---- helpers ----

// totpNow returns a currently-valid TOTP for secret.
func totpNow(secret string) (string, error) {
	return auth.CurrentTOTP(secret, time.Now())
}

// reqWithParam builds a request carrying chi URL params.
func reqWithParam(method, url, body string, params map[string]string) *http.Request {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, url, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, url, nil)
	}
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

type sentMail struct {
	to      []string
	subject string
	body    string
}

// captureMailer records sends. ForgotPassword dispatches email on a goroutine,
// so access is mutex-guarded and tests wait via count()/waitFor().
type captureMailer struct {
	mu   sync.Mutex
	sent []sentMail
}

func (m *captureMailer) Send(to []string, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMail{to, subject, body})
	return nil
}

func (m *captureMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func (m *captureMailer) first() (sentMail, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		return sentMail{}, false
	}
	return m.sent[0], true
}

// waitForCount waits up to ~1s for the mailer to reach n sends.
func (m *captureMailer) waitForCount(n int) bool {
	for i := 0; i < 100; i++ {
		if m.count() >= n {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return m.count() >= n
}

// After mfaMaxFailures bad codes, the account is temporarily locked out — even a
// subsequently correct code is rejected with 429 until the window elapses.
func TestLoginMFA_LocksOutAfterRepeatedFailures(t *testing.T) {
	secret, _ := auth.GenerateTOTPSecret()
	repo := &mockAuthRepo{
		GetTOTPFn:           func(_ context.Context, _ string) (string, bool, error) { return secret, true, nil },
		GetUserByIDFn:       func(_ context.Context, _ string) (*model.User, error) { return testUser(testUserID, "member"), nil },
		StoreRefreshTokenFn: func(_ context.Context, _, _ string, _ time.Time) error { return nil },
		UpdateLastLoginFn:   func(_ context.Context, _ string) error { return nil },
	}
	h := NewAuthHandler(repo, testConfig())
	mfaTok, _ := auth.IssueMFAToken(testUserID, testSecret, 30*time.Minute)

	badReq := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/auth/login/2fa", strings.NewReader(`{"mfa_token":"`+mfaTok+`","code":"000000"}`))
		rr := httptest.NewRecorder()
		h.LoginMFA(rr, req)
		return rr
	}
	for i := 0; i < mfaMaxFailures; i++ {
		if rr := badReq(); rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i+1, rr.Code)
		}
	}
	// The ceiling is reached: the next attempt is throttled, not merely rejected.
	if rr := badReq(); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after %d failures, got %d: %s", mfaMaxFailures, rr.Code, rr.Body)
	}
	// A correct code is still refused while locked out.
	code, _ := totpNow(secret)
	req := httptest.NewRequest("POST", "/auth/login/2fa", strings.NewReader(`{"mfa_token":"`+mfaTok+`","code":"`+code+`"}`))
	rr := httptest.NewRecorder()
	h.LoginMFA(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("a correct code during lockout must be refused with 429, got %d", rr.Code)
	}
}

// A successful two-factor login clears the failure counter so earlier misfires
// don't count toward a later lockout.
func TestLoginMFA_SuccessResetsThrottle(t *testing.T) {
	secret, _ := auth.GenerateTOTPSecret()
	repo := &mockAuthRepo{
		GetTOTPFn:           func(_ context.Context, _ string) (string, bool, error) { return secret, true, nil },
		GetUserByIDFn:       func(_ context.Context, _ string) (*model.User, error) { return testUser(testUserID, "member"), nil },
		StoreRefreshTokenFn: func(_ context.Context, _, _ string, _ time.Time) error { return nil },
		UpdateLastLoginFn:   func(_ context.Context, _ string) error { return nil },
	}
	h := NewAuthHandler(repo, testConfig())
	mfaTok, _ := auth.IssueMFAToken(testUserID, testSecret, 30*time.Minute)

	// A couple of failures, then a success.
	for i := 0; i < mfaMaxFailures-1; i++ {
		req := httptest.NewRequest("POST", "/auth/login/2fa", strings.NewReader(`{"mfa_token":"`+mfaTok+`","code":"000000"}`))
		h.LoginMFA(httptest.NewRecorder(), req)
	}
	code, _ := totpNow(secret)
	req := httptest.NewRequest("POST", "/auth/login/2fa", strings.NewReader(`{"mfa_token":"`+mfaTok+`","code":"`+code+`"}`))
	rr := httptest.NewRecorder()
	h.LoginMFA(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 on valid code, got %d: %s", rr.Code, rr.Body)
	}
	if h.mfaThrottle.blocked(testUserID) {
		t.Fatal("throttle should be cleared after a successful login")
	}
}
