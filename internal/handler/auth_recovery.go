package handler

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"quorum/internal/auth"
)

// resetTokenTTL bounds how long a password-reset link stays valid.
const resetTokenTTL = time.Hour

// recoveryCodeCount is how many one-time 2FA recovery codes are minted at enroll.
const recoveryCodeCount = 10

// ---- Two-factor (TOTP) enrollment ----

// Setup2FA begins TOTP enrollment for the caller: it generates a fresh secret,
// stores it (still disabled), and returns the secret plus an otpauth:// URI for
// the authenticator app. Enrollment is not complete until Enable2FA confirms a
// code. Re-calling regenerates the secret, which is safe because 2FA is not yet
// active.
//
// Enrolling requires the account password, matching Disable2FA: someone who
// hijacks a session must not be able to bind their own authenticator to the
// account (which would lock the real owner out behind an attacker's second
// factor).
func (h *AuthHandler) Setup2FA(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "bad_request")
		return
	}
	user, err := h.repo.GetUserByID(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found", "not_found")
		return
	}
	if !h.passwordMatches(r, userID, body.Password) {
		writeError(w, http.StatusUnauthorized, "password incorrect", "unauthorized")
		return
	}
	if _, enabled, _ := h.repo.GetTOTP(r.Context(), userID); enabled {
		writeError(w, http.StatusConflict, "two-factor is already enabled; disable it first to re-enroll", "conflict")
		return
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate secret", "internal_error")
		return
	}
	if err := h.repo.SetTOTPSecret(r.Context(), userID, secret); err != nil {
		writeError(w, http.StatusInternalServerError, "could not store secret", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"secret":           secret,
		"provisioning_uri": auth.TOTPProvisioningURI(secret, user.Email, "Quorum"),
	})
}

// Enable2FA confirms TOTP enrollment: the caller submits a current code proving
// their authenticator is in sync. On success it flips totp_enabled on and
// returns a fresh set of one-time recovery codes (shown exactly once).
func (h *AuthHandler) Enable2FA(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "bad_request")
		return
	}
	secret, enabled, err := h.repo.GetTOTP(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed", "internal_error")
		return
	}
	if enabled {
		writeError(w, http.StatusConflict, "two-factor is already enabled", "conflict")
		return
	}
	if secret == "" {
		writeError(w, http.StatusBadRequest, "call setup before enabling two-factor", "bad_request")
		return
	}
	if !auth.ValidateTOTP(secret, body.Code, time.Now()) {
		writeError(w, http.StatusBadRequest, "invalid code", "bad_request")
		return
	}
	plainCodes, hashes, err := auth.GenerateRecoveryCodes(recoveryCodeCount)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate recovery codes", "internal_error")
		return
	}
	if err := h.repo.ReplaceRecoveryCodes(r.Context(), userID, hashes); err != nil {
		writeError(w, http.StatusInternalServerError, "could not store recovery codes", "internal_error")
		return
	}
	if err := h.repo.EnableTOTP(r.Context(), userID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not enable two-factor", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":        true,
		"recovery_codes": plainCodes,
	})
}

// passwordMatches re-verifies the caller's account password. Used to gate
// changes to a user's second factor, so possession of a session alone is never
// enough to add, remove, or re-issue the factor that protects it.
// It is throttled per account (h.pwThrottle): after repeated failures further
// attempts are refused for a window, so a hijacked session cannot brute-force
// the password that guards the second factor.
func (h *AuthHandler) passwordMatches(r *http.Request, userID, password string) bool {
	if password == "" || h.pwThrottle.blocked(userID) {
		return false
	}
	hash, err := h.repo.GetPasswordHash(r.Context(), userID)
	if err != nil {
		return false
	}
	if !auth.CheckPassword(hash, password) {
		h.pwThrottle.fail(userID)
		return false
	}
	h.pwThrottle.reset(userID)
	return true
}

// Disable2FA turns off TOTP for the caller. It re-verifies the account password
// (a stolen session alone must not be able to strip a second factor) and clears
// the stored secret and recovery codes.
func (h *AuthHandler) Disable2FA(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "bad_request")
		return
	}
	if !h.passwordMatches(r, userID, body.Password) {
		writeError(w, http.StatusUnauthorized, "password incorrect", "unauthorized")
		return
	}
	if err := h.repo.DisableTOTP(r.Context(), userID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not disable two-factor", "internal_error")
		return
	}
	h.logAudit(r, userID, "auth.2fa_disabled")
	w.WriteHeader(http.StatusNoContent)
}

// RegenerateRecoveryCodes issues a fresh set of one-time recovery codes,
// invalidating every previously-issued code. Requires the account password and
// that 2FA is actually enabled. Used when codes are lost, or after some have
// been spent. The new codes are returned exactly once.
func (h *AuthHandler) RegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "bad_request")
		return
	}
	if !h.passwordMatches(r, userID, body.Password) {
		writeError(w, http.StatusUnauthorized, "password incorrect", "unauthorized")
		return
	}
	_, enabled, err := h.repo.GetTOTP(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed", "internal_error")
		return
	}
	if !enabled {
		writeError(w, http.StatusBadRequest, "two-factor is not enabled", "bad_request")
		return
	}
	plainCodes, hashes, err := auth.GenerateRecoveryCodes(recoveryCodeCount)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate recovery codes", "internal_error")
		return
	}
	// ReplaceRecoveryCodes deletes the old set, so any code from a previous
	// batch stops working the moment this succeeds.
	if err := h.repo.ReplaceRecoveryCodes(r.Context(), userID, hashes); err != nil {
		writeError(w, http.StatusInternalServerError, "could not store recovery codes", "internal_error")
		return
	}
	h.logAudit(r, userID, "auth.recovery_codes_regenerated")
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": plainCodes})
}

// ---- Password recovery ----

// ForgotPassword starts the self-service reset flow. It ALWAYS returns 200 with
// the same body, whether or not the email is registered, so it cannot be used
// to enumerate accounts. When the email exists a single-use, time-limited token
// is created and emailed as a link. Rate-limited at the route.
func (h *AuthHandler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "bad_request")
		return
	}
	const ok = "if that email is registered, a reset link has been sent"
	email := normalizeEmail(body.Email)
	if email == "" {
		writeJSON(w, http.StatusOK, map[string]string{"message": ok})
		return
	}
	user, _, err := h.repo.GetUserByEmail(r.Context(), email)
	if err != nil {
		// Unknown email (or a transient DB error): reveal nothing.
		writeJSON(w, http.StatusOK, map[string]string{"message": ok})
		return
	}
	plain, hashed, err := auth.GenerateResetToken()
	if err == nil {
		if serr := h.repo.CreatePasswordResetToken(r.Context(), user.ID, hashed, time.Now().Add(resetTokenTTL)); serr == nil && h.mailer != nil {
			link := fmt.Sprintf("%s/#/reset-password?token=%s", strings.TrimRight(h.cfg.BaseURL, "/"), plain)
			subject := "Reset your Quorum password"
			text := fmt.Sprintf(
				"A password reset was requested for your Quorum account.\n\n"+
					"Open this link to choose a new password (valid for 1 hour):\n\n%s\n\n"+
					"If you did not request this, you can safely ignore this email.\n", link)
			// Send in the background so the response time does not depend on
			// whether the email exists (a synchronous SMTP round-trip only on the
			// known-account path would leak account existence via timing).
			to, mailer := user.Email, h.mailer
			go func() {
				if serr := mailer.Send([]string{to}, subject, text); serr != nil {
					log.Printf("password-reset email error: %v", serr)
				}
			}()
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": ok})
}

// ResetPassword completes the self-service flow: it atomically consumes a valid
// reset token, sets the new password, and revokes all of that user's refresh
// tokens so any session opened with the old password is cut off.
func (h *AuthHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "bad_request")
		return
	}
	if body.Token == "" {
		writeError(w, http.StatusBadRequest, "token required", "bad_request")
		return
	}
	if len(body.NewPassword) < 10 {
		writeError(w, http.StatusBadRequest, "password must be at least 10 characters", "bad_request")
		return
	}
	userID, err := h.repo.ConsumePasswordResetToken(r.Context(), auth.HashResetToken(body.Token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "invalid or expired reset token", "bad_request")
			return
		}
		writeError(w, http.StatusInternalServerError, "reset failed", "internal_error")
		return
	}
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
	h.logAudit(r, userID, "auth.password_reset")
	w.WriteHeader(http.StatusNoContent)
}

// AdminResetPassword lets an admin set a new password for another account (for
// users who cannot receive email, or to force a rotation). If new_password is
// omitted a strong random one is generated and returned to the admin exactly
// once to relay out-of-band. Resetting a superadmin requires a superadmin
// actor. All of the target's sessions are revoked.
func (h *AuthHandler) AdminResetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		NewPassword string `json:"new_password"`
	}
	_ = decodeJSON(r, &body) // body is optional

	target, err := h.repo.GetUserByID(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "user not found", "query error")
		return
	}
	if target.Role == "superadmin" && roleFromCtx(r) != "superadmin" {
		writeError(w, http.StatusForbidden, "only a superadmin may reset a superadmin's password", "forbidden")
		return
	}

	newPassword := strings.TrimSpace(body.NewPassword)
	generated := false
	if newPassword == "" {
		gen, _, err := auth.GenerateRecoveryCodes(3) // 3 x 8 base32 chars = strong temp password
		if err != nil || len(gen) < 3 {
			writeError(w, http.StatusInternalServerError, "could not generate password", "internal_error")
			return
		}
		newPassword = strings.Join(gen, "-")
		generated = true
	} else if len(newPassword) < 10 {
		writeError(w, http.StatusBadRequest, "password must be at least 10 characters", "bad_request")
		return
	}

	newHash, err := auth.HashPassword(newPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash error", "internal_error")
		return
	}
	if err := h.repo.UpdatePasswordHash(r.Context(), id, newHash); err != nil {
		writeError(w, http.StatusInternalServerError, "update error", "internal_error")
		return
	}
	h.repo.RevokeAllRefreshTokensForUser(r.Context(), id) //nolint:errcheck

	resp := map[string]any{"reset": true}
	if generated {
		// Returned once so the admin can relay it; never stored in plaintext.
		resp["temporary_password"] = newPassword
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- Session management ----

// Sessions reports how many live sessions (unrevoked, unexpired refresh tokens)
// the caller has, so the UI can offer "sign out everywhere else" meaningfully.
func (h *AuthHandler) Sessions(w http.ResponseWriter, r *http.Request) {
	n, err := h.repo.ActiveSessionCount(r.Context(), userIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"active_sessions": n})
}

// RevokeOtherSessions signs the caller out of every device except the one
// making this request — the standard "I left myself logged in somewhere"
// control. The caller's own refresh token is identified by its cookie and
// preserved; if there is no cookie (a pure bearer client) every session is
// revoked, which is the safe direction.
func (h *AuthHandler) RevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	keepHash := ""
	if c, err := r.Cookie("quorum_refresh"); err == nil && c.Value != "" {
		keepHash = auth.HashRefreshToken(c.Value)
	}
	n, err := h.repo.RevokeOtherRefreshTokensForUser(r.Context(), userIDFromCtx(r), keepHash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not revoke sessions", "internal_error")
		return
	}
	h.logAudit(r, userIDFromCtx(r), "auth.sessions_revoked")
	writeJSON(w, http.StatusOK, map[string]int64{"revoked": n})
}
