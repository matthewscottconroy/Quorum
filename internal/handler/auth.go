package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"quorum/internal/auth"
	"quorum/internal/config"
	"quorum/internal/model"
)

// normalizeEmail lowercases and trims an email so lookups and uniqueness are
// case-insensitive (matching the lower(email) unique index).
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// derefStr returns the pointed-to string, or "" if nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// mailer is the subset of the email service the auth handler needs to deliver
// password-reset links. Optional: when unset, reset emails are silently
// skipped (the flow still returns 200 so it cannot be used to probe accounts).
type mailer interface {
	Send(to []string, subject, body string) error
}

// AuthHandler handles authentication and user-management endpoints.
type AuthHandler struct {
	repo        authRepo
	cfg         *config.Config
	notifier    deletionNotifier
	mailer      mailer
	audit       auditRepo
	mfaThrottle *failureThrottle
	// pwThrottle bounds password re-verification attempts (change-password,
	// 2FA setup/disable, recovery-code regeneration). These endpoints sit behind
	// a session, not the login rate limiter, so without it a stolen session
	// could brute-force the account password at full HTTP speed and then strip
	// or replace the second factor.
	pwThrottle *failureThrottle
}

// mfaMaxFailures / mfaLockWindow bound how many failed two-factor attempts a
// single account tolerates before a temporary lockout. The IP rate limiter
// alone can't stop this: an attacker rotating IPs gets unlimited guesses at one
// account's 6-digit TOTP, so the throttle is keyed by user id instead.
const (
	mfaMaxFailures = 5
	mfaLockWindow  = 15 * time.Minute
)

// NewAuthHandler constructs an AuthHandler backed by the given repo and config.
func NewAuthHandler(r authRepo, cfg *config.Config) *AuthHandler {
	return &AuthHandler{
		repo:        r,
		cfg:         cfg,
		mfaThrottle: newFailureThrottle(mfaMaxFailures, mfaLockWindow),
		pwThrottle:  newFailureThrottle(mfaMaxFailures, mfaLockWindow),
	}
}

// failureThrottle enforces a temporary per-key lockout after too many failed
// attempts within a window. Unlike rateLimiter it counts only failures and is
// cleared on success, so a legitimate user is never locked out by their own
// successful logins. It prunes expired entries on access (no sweeper goroutine);
// the key space is bounded by the number of distinct accounts that fail.
type failureThrottle struct {
	mu     sync.Mutex
	fails  map[string][]time.Time
	max    int
	window time.Duration
}

func newFailureThrottle(max int, window time.Duration) *failureThrottle {
	return &failureThrottle{fails: make(map[string][]time.Time), max: max, window: window}
}

// recentLocked returns and re-stores the not-yet-expired failures for key.
// The caller must hold ft.mu.
func (ft *failureThrottle) recentLocked(key string) []time.Time {
	cutoff := time.Now().Add(-ft.window)
	recent := ft.fails[key][:0]
	for _, t := range ft.fails[key] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(ft.fails, key)
		return nil
	}
	ft.fails[key] = recent
	return recent
}

// blocked reports whether key has reached the failure ceiling within the window.
func (ft *failureThrottle) blocked(key string) bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return len(ft.recentLocked(key)) >= ft.max
}

// fail records one failed attempt for key.
func (ft *failureThrottle) fail(key string) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.fails[key] = append(ft.recentLocked(key), time.Now())
}

// reset clears a key's failures after a successful authentication.
func (ft *failureThrottle) reset(key string) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	delete(ft.fails, key)
}

// SetNotifier attaches an optional notifier used when a user account is deleted.
func (h *AuthHandler) SetNotifier(n deletionNotifier) { h.notifier = n }

// SetMailer attaches the email service used to deliver password-reset links.
func (h *AuthHandler) SetMailer(m mailer) { h.mailer = m }

// SetAuditLogger attaches the audit log. Auth endpoints live outside the
// AuditMiddleware group, so they record security-relevant events themselves.
func (h *AuthHandler) SetAuditLogger(a auditRepo) { h.audit = a }

// logAudit records an auth event (best-effort; never blocks the response).
func (h *AuthHandler) logAudit(r *http.Request, userID, action string) {
	if h.audit != nil && userID != "" {
		h.audit.Log(r.Context(), userID, action, "auth", userID, nil) //nolint:errcheck
	}
}

// issueSession mints an access token, stores a rotated refresh token, sets the
// refresh cookie, and writes the standard login response. Shared by the
// single-step login path and the second-factor (TOTP) completion path.
func (h *AuthHandler) issueSession(w http.ResponseWriter, r *http.Request, user *model.User) {
	access, err := auth.IssueAccessToken(user.ID, user.Role, derefStr(user.MemberID), h.cfg.JWTSecret, h.cfg.JWTAccessTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token error", "internal_error")
		return
	}
	plain, hashed, err := auth.GenerateRefreshToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token error", "internal_error")
		return
	}
	expiresAt := time.Now().Add(h.cfg.JWTRefreshTTL)
	if err := h.repo.StoreRefreshToken(r.Context(), user.ID, hashed, expiresAt); err != nil {
		writeError(w, http.StatusInternalServerError, "token error", "internal_error")
		return
	}
	h.repo.UpdateLastLogin(r.Context(), user.ID) //nolint:errcheck
	h.logAudit(r, user.ID, "auth.login")
	http.SetCookie(w, &http.Cookie{
		Name:     "quorum_refresh",
		Value:    plain,
		Path:     "/api/v1/auth",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Expires:  expiresAt,
		Secure:   h.cfg.SecureCookies,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access,
		"expires_at":   time.Now().Add(h.cfg.JWTAccessTTL),
		"user":         user,
	})
}

// Login authenticates an email/password and issues an access token plus refresh cookie.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "bad_request")
		return
	}

	user, hash, err := h.repo.GetUserByEmail(r.Context(), normalizeEmail(body.Email))
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			// A DB outage must not masquerade as a wave of bad credentials.
			writeError(w, http.StatusInternalServerError, "login unavailable", "internal_error")
			return
		}
		// Unknown email: burn the same bcrypt work as a real comparison so
		// response timing does not reveal whether the email exists.
		auth.DummyCheckPassword(body.Password)
		writeError(w, http.StatusUnauthorized, "invalid credentials", "unauthorized")
		return
	}
	if !auth.CheckPassword(hash, body.Password) {
		// A failed login against a real account is an insider-threat signal
		// (credential guessing, a colleague trying a teammate's account); the
		// unknown-email path above is deliberately NOT logged, since it would
		// let an outsider spray garbage into the audit log.
		h.logAudit(r, user.ID, "auth.login_failed")
		writeError(w, http.StatusUnauthorized, "invalid credentials", "unauthorized")
		return
	}

	// If the account has two-factor enabled, do NOT issue a session yet. Return
	// an interim token (Purpose=mfa) that authorizes only the second step; the
	// API middleware rejects it for everything else. Fail CLOSED: a lookup error
	// must never skip the second factor and mint a full session.
	_, enabled, err := h.repo.GetTOTP(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "login unavailable", "internal_error")
		return
	}
	if enabled {
		mfaToken, err := auth.IssueMFAToken(user.ID, h.cfg.JWTSecret, 5*time.Minute)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "token error", "internal_error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"mfa_required": true,
			"mfa_token":    mfaToken,
		})
		return
	}

	h.issueSession(w, r, user)
}

// LoginMFA completes a two-factor login: it validates the interim MFA token
// (issued by Login) plus a TOTP code or a one-time recovery code, then issues a
// full session. Rate-limited like Login.
func (h *AuthHandler) LoginMFA(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MFAToken     string `json:"mfa_token"`
		Code         string `json:"code"`
		RecoveryCode string `json:"recovery_code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "bad_request")
		return
	}
	claims, err := auth.ParseToken(body.MFAToken, h.cfg.JWTSecret)
	if err != nil || claims.Purpose != auth.PurposeMFA || claims.UserID == "" {
		writeError(w, http.StatusUnauthorized, "invalid or expired two-factor session", "unauthorized")
		return
	}
	secret, enabled, err := h.repo.GetTOTP(r.Context(), claims.UserID)
	if err != nil || !enabled {
		writeError(w, http.StatusUnauthorized, "two-factor is not enabled", "unauthorized")
		return
	}
	// Blunt online brute-forcing of this account's codes: after too many recent
	// failures, reject further attempts (even correct ones) until the window passes.
	if h.mfaThrottle.blocked(claims.UserID) {
		writeError(w, http.StatusTooManyRequests, "too many failed two-factor attempts; try again later", "rate_limited")
		return
	}

	ok := false
	switch {
	case strings.TrimSpace(body.Code) != "":
		ok = auth.ValidateTOTP(secret, body.Code, time.Now())
	case strings.TrimSpace(body.RecoveryCode) != "":
		// Recovery codes are single-use; consuming marks it spent atomically.
		consumed, cerr := h.repo.ConsumeRecoveryCode(r.Context(), claims.UserID, auth.HashRefreshToken(strings.TrimSpace(body.RecoveryCode)))
		if cerr != nil {
			writeError(w, http.StatusInternalServerError, "verification failed", "internal_error")
			return
		}
		ok = consumed
	}
	if !ok {
		h.mfaThrottle.fail(claims.UserID)
		h.logAudit(r, claims.UserID, "auth.2fa_failed")
		writeError(w, http.StatusUnauthorized, "invalid two-factor code", "unauthorized")
		return
	}
	h.mfaThrottle.reset(claims.UserID)

	user, err := h.repo.GetUserByID(r.Context(), claims.UserID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "user not found", "unauthorized")
		return
	}
	h.issueSession(w, r, user)
}

// Refresh rotates the refresh cookie and issues a new access token.
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	plain := ""
	if c, err := r.Cookie("quorum_refresh"); err == nil {
		plain = c.Value
	}
	if plain == "" {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := decodeJSON(r, &body); err == nil {
			plain = body.RefreshToken
		}
	}
	if plain == "" {
		writeError(w, http.StatusUnauthorized, "refresh token required", "unauthorized")
		return
	}

	hashed := auth.HashRefreshToken(plain)
	userID, revoked, expiresAt, createdAt, err := h.repo.GetRefreshToken(r.Context(), hashed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Unknown token: genuinely unauthenticated.
			writeError(w, http.StatusUnauthorized, "invalid or expired refresh token", "unauthorized")
			return
		}
		// A database outage must not masquerade as an auth failure — that would
		// force-log-out every active session during a transient blip.
		writeError(w, http.StatusInternalServerError, "token lookup failed", "internal_error")
		return
	}
	if revoked || time.Now().After(expiresAt) {
		writeError(w, http.StatusUnauthorized, "invalid or expired refresh token", "unauthorized")
		return
	}
	// Idle logout, enforced server-side: rotation mints a fresh token on every
	// refresh, so this token's age IS the time since the session's last
	// activity. Too old → the session sat idle past the window; revoke it and
	// require a fresh sign-in (a stolen cookie from an abandoned machine is
	// useless after the idle window, regardless of the 7-day hard expiry).
	if h.cfg.IdleTimeoutMinutes > 0 &&
		time.Since(createdAt) > time.Duration(h.cfg.IdleTimeoutMinutes)*time.Minute {
		h.repo.RevokeRefreshToken(r.Context(), hashed) //nolint:errcheck
		h.logAudit(r, userID, "auth.session_idle_timeout")
		writeError(w, http.StatusUnauthorized, "session expired after inactivity; please sign in again", "unauthorized")
		return
	}

	user, err := h.repo.GetUserByID(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "user not found", "unauthorized")
		return
	}

	access, err := auth.IssueAccessToken(user.ID, user.Role, derefStr(user.MemberID), h.cfg.JWTSecret, h.cfg.JWTAccessTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token error", "internal_error")
		return
	}

	newPlain, newHashed, err := auth.GenerateRefreshToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token error", "internal_error")
		return
	}

	// Rotate: store the new token first, then revoke the old one, checking
	// both errors. Storing first means a failure can never strand the user
	// with no valid token; a failed revoke aborts the response so the client
	// retries rather than silently keeping two live tokens.
	newExpiresAt := time.Now().Add(h.cfg.JWTRefreshTTL)
	if err := h.repo.StoreRefreshToken(r.Context(), user.ID, newHashed, newExpiresAt); err != nil {
		writeError(w, http.StatusInternalServerError, "token error", "internal_error")
		return
	}
	if err := h.repo.RevokeRefreshToken(r.Context(), hashed); err != nil {
		writeError(w, http.StatusInternalServerError, "token error", "internal_error")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "quorum_refresh",
		Value:    newPlain,
		Path:     "/api/v1/auth",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Expires:  newExpiresAt,
		Secure:   h.cfg.SecureCookies,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access,
		"expires_at":   time.Now().Add(h.cfg.JWTAccessTTL),
		"user":         user,
	})
}

// Logout revokes the refresh token and clears the cookie.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("quorum_refresh"); err == nil && c.Value != "" {
		hashed := auth.HashRefreshToken(c.Value)
		h.repo.RevokeRefreshToken(r.Context(), hashed) //nolint:errcheck
	}
	// Also accept token in body for API clients.
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSON(r, &body); err == nil && body.RefreshToken != "" {
		hashed := auth.HashRefreshToken(body.RefreshToken)
		h.repo.RevokeRefreshToken(r.Context(), hashed) //nolint:errcheck
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "quorum_refresh",
		Value:    "",
		Path:     "/api/v1/auth",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

// Me returns the authenticated user's profile.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	user, err := h.repo.GetUserByID(r.Context(), userIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found", "not_found")
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// Bootstrap creates the first admin user atomically. The INSERT runs only when
// the users table is empty, so concurrent requests cannot both succeed.
func (h *AuthHandler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Email == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password required", "bad_request")
		return
	}
	if len(body.Password) < 10 {
		writeError(w, http.StatusBadRequest, "password must be at least 10 characters", "bad_request")
		return
	}
	// Cheap precheck before the ~250ms bcrypt hash: this is an unauthenticated
	// endpoint, so hashing first would be an easy CPU-exhaustion target once
	// the instance is already bootstrapped.
	count, err := h.repo.CountUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error", "internal_error")
		return
	}
	if count > 0 {
		writeError(w, http.StatusForbidden, "bootstrap not available", "forbidden")
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash error", "internal_error")
		return
	}
	// The founding account gets the top role so it can manage users and perform
	// gated destructive operations.
	user, err := h.repo.CreateFirstUser(r.Context(), normalizeEmail(body.Email), hash, "superadmin")
	if err != nil {
		if isNotFound(err) {
			// The atomic INSERT found an existing user (lost a bootstrap race).
			writeError(w, http.StatusForbidden, "bootstrap not available", "forbidden")
			return
		}
		writeError(w, http.StatusInternalServerError, "db error", "internal_error")
		return
	}
	h.logAudit(r, user.ID, "auth.bootstrap")
	writeJSON(w, http.StatusCreated, user)
}

// CreateUser allows admins to add new users. An optional member_id links the
// account to a member record — required for a "restricted" user to see their
// own data.
func (h *AuthHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string  `json:"email"`
		Password string  `json:"password"`
		Role     string  `json:"role"`
		MemberID *string `json:"member_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "bad_request")
		return
	}
	if body.Email == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password required", "bad_request")
		return
	}
	// Same password policy as bootstrap / change / reset, so accounts created by
	// an admin aren't a weaker-password back door.
	if len(body.Password) < 10 {
		writeError(w, http.StatusBadRequest, "password must be at least 10 characters", "bad_request")
		return
	}
	role := body.Role
	if role == "" {
		role = "member"
	}
	if !validRoles[role] {
		writeError(w, http.StatusBadRequest, "invalid role", "bad_request")
		return
	}
	// Only a superadmin may mint another superadmin.
	if role == "superadmin" && roleFromCtx(r) != "superadmin" {
		writeError(w, http.StatusForbidden, "only a superadmin may assign the superadmin role", "forbidden")
		return
	}
	if body.MemberID != nil && !isValidUUID(*body.MemberID) {
		writeError(w, http.StatusBadRequest, "member_id must be a UUID", "bad_request")
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash error", "internal_error")
		return
	}
	user, err := h.repo.CreateUser(r.Context(), normalizeEmail(body.Email), hash, role, body.MemberID)
	if err != nil {
		if isFKViolation(err) {
			writeError(w, http.StatusBadRequest, "member_id does not reference an existing member", "bad_request")
			return
		}
		writeError(w, http.StatusConflict, "email already in use", "conflict")
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

// ListUsers returns all user accounts (admin only).
func (h *AuthHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.repo.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, users)
}

// UpdateUser changes a user's role and/or their linked member record. The body
// may contain "role" and/or "member_id" (a UUID string, or null to unlink); at
// least one must be present.
func (h *AuthHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	roleRaw, hasRole := raw["role"]
	memberRaw, hasMember := raw["member_id"]
	if !hasRole && !hasMember {
		writeError(w, http.StatusBadRequest, "role or member_id required", "bad_request")
		return
	}
	// Prevent self-demotion (which could strip the last superadmin) before any
	// DB work — it's a pure id comparison.
	if hasRole && id == userIDFromCtx(r) {
		writeError(w, http.StatusForbidden, "cannot change your own role", "forbidden")
		return
	}
	// Validate all field formats before touching the database.
	var role string
	if hasRole {
		if err := json.Unmarshal(roleRaw, &role); err != nil || !validRoles[role] {
			writeError(w, http.StatusBadRequest, "invalid role", "bad_request")
			return
		}
	}
	var memberID *string
	if hasMember {
		if err := json.Unmarshal(memberRaw, &memberID); err != nil {
			writeError(w, http.StatusBadRequest, "member_id must be a string or null", "bad_request")
			return
		}
		if memberID != nil && !isValidUUID(*memberID) {
			writeError(w, http.StatusBadRequest, "member_id must be a UUID", "bad_request")
			return
		}
	}

	target, err := h.repo.GetUserByID(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "user not found", "query error")
		return
	}

	if hasRole {
		// Granting or revoking superadmin is a superadmin-only action.
		if (role == "superadmin" || target.Role == "superadmin") && roleFromCtx(r) != "superadmin" {
			writeError(w, http.StatusForbidden, "only a superadmin may grant or revoke the superadmin role", "forbidden")
			return
		}
		if _, err := h.repo.UpdateUserRole(r.Context(), id, role); err != nil {
			writeRepoError(w, err, "user not found", "update error")
			return
		}
		// A privilege change is the canonical insider event: record old -> new
		// in the chain-protected audit detail.
		if target.Role != role {
			setAuditDetail(r, map[string]any{"role_old": target.Role, "role_new": role})
		}
	}

	if hasMember {
		oldMember := ""
		if target.MemberID != nil {
			oldMember = *target.MemberID
		}
		newMember := ""
		if memberID != nil {
			newMember = *memberID
		}
		if oldMember != newMember {
			setAuditDetail(r, map[string]any{"member_link_old": oldMember, "member_link_new": newMember})
		}
		if err := h.repo.SetUserMember(r.Context(), id, memberID); err != nil {
			if isFKViolation(err) {
				writeError(w, http.StatusBadRequest, "member_id does not reference an existing member", "bad_request")
				return
			}
			writeRepoError(w, err, "user not found", "update error")
			return
		}
	}

	updated, err := h.repo.GetUserByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// DeleteUser permanently deletes a user account (superadmin; type-to-confirm).
func (h *AuthHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if id == userIDFromCtx(r) {
		writeError(w, http.StatusForbidden, "cannot delete your own account", "forbidden")
		return
	}
	target, err := h.repo.GetUserByID(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "user not found", "query error")
		return
	}
	// Type-to-confirm: the caller must echo the target's email.
	if !confirmMatches(w, r, target.Email) {
		return
	}
	if err := h.repo.DeleteUser(r.Context(), id); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "user not found", "not_found")
			return
		}
		if isFKViolation(err) {
			writeError(w, http.StatusConflict,
				"this account has recorded history (audit entries, payments, or owned records) that must keep its author; "+
					"retire the account instead — demote it to restricted and reset its credentials",
				"conflict")
			return
		}
		writeError(w, http.StatusInternalServerError, "delete error", "internal_error")
		return
	}
	if h.notifier != nil {
		// Notify the governance body and, as a courtesy/security signal, the
		// person whose account was removed.
		h.notifier.NotifyDeletion(r.Context(), userIDFromCtx(r), "user account", target.Email, []string{target.Email})
	}
	w.WriteHeader(http.StatusNoContent)
}

// ChangePassword changes the caller's own password and revokes their other sessions.
func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if body.CurrentPassword == "" || body.NewPassword == "" {
		writeError(w, http.StatusBadRequest, "current_password and new_password required", "bad_request")
		return
	}
	if len(body.NewPassword) < 10 {
		writeError(w, http.StatusBadRequest, "password must be at least 10 characters", "bad_request")
		return
	}
	hash, err := h.repo.GetPasswordHash(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "user error", "internal_error")
		return
	}
	if h.pwThrottle.blocked(userID) {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts; try again later", "rate_limited")
		return
	}
	if !auth.CheckPassword(hash, body.CurrentPassword) {
		h.pwThrottle.fail(userID)
		writeError(w, http.StatusUnauthorized, "current password incorrect", "unauthorized")
		return
	}
	h.pwThrottle.reset(userID)
	newHash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash error", "internal_error")
		return
	}
	if err := h.repo.UpdatePasswordHash(r.Context(), userID, newHash); err != nil {
		writeError(w, http.StatusInternalServerError, "update error", "internal_error")
		return
	}
	h.repo.RevokeAllRefreshTokensForUser(r.Context(), userID) //nolint:errcheck
	w.WriteHeader(http.StatusNoContent)
}
