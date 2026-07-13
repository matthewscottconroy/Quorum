package handler

import (
	"net/http"
	"time"

	"quorum/internal/auth"
	"quorum/internal/config"
)

// AuthHandler handles authentication and user-management endpoints.
type AuthHandler struct {
	repo authRepo
	cfg  *config.Config
}

// NewAuthHandler constructs an AuthHandler backed by the given repo and config.
func NewAuthHandler(r authRepo, cfg *config.Config) *AuthHandler {
	return &AuthHandler{repo: r, cfg: cfg}
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "bad_request")
		return
	}

	user, hash, err := h.repo.GetUserByEmail(r.Context(), body.Email)
	if err != nil || !auth.CheckPassword(hash, body.Password) {
		writeError(w, http.StatusUnauthorized, "invalid credentials", "unauthorized")
		return
	}

	access, err := auth.IssueAccessToken(user.ID, user.Role, h.cfg.JWTSecret, h.cfg.JWTAccessTTL)
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
		"expires_at":   expiresAt,
		"user":         user,
	})
}

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
	userID, revoked, expiresAt, err := h.repo.GetRefreshToken(r.Context(), hashed)
	if err != nil || revoked || time.Now().After(expiresAt) {
		writeError(w, http.StatusUnauthorized, "invalid or expired refresh token", "unauthorized")
		return
	}

	// Rotate: revoke old token before issuing new one to prevent indefinite reuse.
	h.repo.RevokeRefreshToken(r.Context(), hashed) //nolint:errcheck

	user, err := h.repo.GetUserByID(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "user not found", "unauthorized")
		return
	}

	access, err := auth.IssueAccessToken(user.ID, user.Role, h.cfg.JWTSecret, h.cfg.JWTAccessTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token error", "internal_error")
		return
	}

	newPlain, newHashed, err := auth.GenerateRefreshToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token error", "internal_error")
		return
	}

	newExpiresAt := time.Now().Add(h.cfg.JWTRefreshTTL)
	if err := h.repo.StoreRefreshToken(r.Context(), user.ID, newHashed, newExpiresAt); err != nil {
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
		"expires_at":   newExpiresAt,
		"user":         user,
	})
}

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
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash error", "internal_error")
		return
	}
	user, err := h.repo.CreateFirstUser(r.Context(), body.Email, hash, "admin")
	if err != nil {
		// pgx.ErrNoRows means a user already existed; any DB error also means unavailable.
		writeError(w, http.StatusForbidden, "bootstrap not available", "forbidden")
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

// CreateUser allows admins to add new users.
func (h *AuthHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "bad_request")
		return
	}
	if body.Email == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password required", "bad_request")
		return
	}
	role := body.Role
	if role == "" {
		role = "member"
	}
	if role != "admin" && role != "officer" && role != "member" {
		writeError(w, http.StatusBadRequest, "invalid role", "bad_request")
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash error", "internal_error")
		return
	}
	user, err := h.repo.CreateUser(r.Context(), body.Email, hash, role)
	if err != nil {
		writeError(w, http.StatusConflict, "email already in use", "conflict")
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (h *AuthHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.repo.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (h *AuthHandler) UpdateUserRole(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if body.Role != "admin" && body.Role != "officer" && body.Role != "member" {
		writeError(w, http.StatusBadRequest, "invalid role", "bad_request")
		return
	}
	user, err := h.repo.UpdateUserRole(r.Context(), id, body.Role)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found", "not_found")
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (h *AuthHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if id == userIDFromCtx(r) {
		writeError(w, http.StatusForbidden, "cannot delete your own account", "forbidden")
		return
	}
	if err := h.repo.DeleteUser(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "delete error", "internal_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

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
	if !auth.CheckPassword(hash, body.CurrentPassword) {
		writeError(w, http.StatusUnauthorized, "current password incorrect", "unauthorized")
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
	w.WriteHeader(http.StatusNoContent)
}
