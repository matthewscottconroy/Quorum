// Package repo provides pgx-backed data access for each domain aggregate.
package repo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// newOpaqueToken returns a 256-bit URL-safe hex token for feed/subscription
// URLs — a scoped read credential, not a session token.
func newOpaqueToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// AuthRepo provides PostgreSQL data access for records.
type AuthRepo struct {
	db *pgxpool.Pool
}

// NewAuthRepo constructs a AuthRepo backed by the given connection pool.
func NewAuthRepo(db *pgxpool.Pool) *AuthRepo {
	return &AuthRepo{db: db}
}

// GetUserByEmail returns the user and password hash for an email, or pgx.ErrNoRows.
func (r *AuthRepo) GetUserByEmail(ctx context.Context, email string) (*model.User, string, error) {
	var u model.User
	var hash string
	err := r.db.QueryRow(ctx, `
		SELECT id::text, email, role, member_id::text, created_at, last_login_at, password_hash
		FROM users WHERE email = $1`, email).
		Scan(&u.ID, &u.Email, &u.Role, &u.MemberID, &u.CreatedAt, &u.LastLoginAt, &hash)
	if err != nil {
		return nil, "", err
	}
	return &u, hash, nil
}

// GetUserByID returns the user with the given id, or pgx.ErrNoRows.
func (r *AuthRepo) GetUserByID(ctx context.Context, id string) (*model.User, error) {
	var u model.User
	err := r.db.QueryRow(ctx, `
		SELECT id::text, email, role, member_id::text, totp_enabled, created_at, last_login_at
		FROM users WHERE id = $1::uuid`, id).
		Scan(&u.ID, &u.Email, &u.Role, &u.MemberID, &u.TOTPEnabled, &u.CreatedAt, &u.LastLoginAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser inserts a new user. memberID optionally links the account to a
// member record (required for a "restricted" user to see their own data); pass
// nil to leave it unlinked.
func (r *AuthRepo) CreateUser(ctx context.Context, email, hash, role string, memberID *string) (*model.User, error) {
	var u model.User
	err := r.db.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, role, member_id)
		VALUES ($1, $2, $3, $4::uuid)
		RETURNING id::text, email, role, member_id::text, created_at, last_login_at`,
		email, hash, role, memberID).
		Scan(&u.ID, &u.Email, &u.Role, &u.MemberID, &u.CreatedAt, &u.LastLoginAt)
	return &u, err
}

// SetUserMember links (or, with nil, unlinks) a user's member record. Returns
// pgx.ErrNoRows if no such user exists.
func (r *AuthRepo) SetUserMember(ctx context.Context, id string, memberID *string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE users SET member_id = $2::uuid WHERE id = $1::uuid`, id, memberID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// UpdateLastLogin stamps the user's last_login_at to the current time.
func (r *AuthRepo) UpdateLastLogin(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1::uuid`, id)
	return err
}

// StoreRefreshToken persists a hashed refresh token with its expiry.
func (r *AuthRepo) StoreRefreshToken(ctx context.Context, userID, hash string, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO refresh_tokens (user_id, token_hash, expires_at)
		VALUES ($1::uuid, $2, $3)`, userID, hash, expiresAt)
	return err
}

// GetRefreshToken returns the user id, revoked flag, and expiry for a token hash.
func (r *AuthRepo) GetRefreshToken(ctx context.Context, hash string) (userID string, revoked bool, expiresAt, createdAt time.Time, err error) {
	err = r.db.QueryRow(ctx, `
		SELECT user_id::text, revoked, expires_at, created_at
		FROM refresh_tokens WHERE token_hash = $1`, hash).
		Scan(&userID, &revoked, &expiresAt, &createdAt)
	return
}

// RevokeRefreshToken marks a single refresh token revoked.
func (r *AuthRepo) RevokeRefreshToken(ctx context.Context, hash string) error {
	_, err := r.db.Exec(ctx, `UPDATE refresh_tokens SET revoked = TRUE WHERE token_hash = $1`, hash)
	return err
}

// CountUsers returns the total number of user accounts.
func (r *AuthRepo) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// ListUsers returns all users ordered by email.
func (r *AuthRepo) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id::text, email, role, member_id::text, totp_enabled, created_at, last_login_at
		FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []model.User
	for rows.Next() {
		var u model.User
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.MemberID, &u.TOTPEnabled, &u.CreatedAt, &u.LastLoginAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// UpdateUserRole sets a user's role and returns the updated user.
func (r *AuthRepo) UpdateUserRole(ctx context.Context, id, role string) (*model.User, error) {
	var u model.User
	err := r.db.QueryRow(ctx, `
		UPDATE users SET role = $1 WHERE id = $2::uuid
		RETURNING id::text, email, role, member_id::text, created_at, last_login_at`,
		role, id).
		Scan(&u.ID, &u.Email, &u.Role, &u.MemberID, &u.CreatedAt, &u.LastLoginAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// DeleteUser removes a user, returning pgx.ErrNoRows if none existed.
func (r *AuthRepo) DeleteUser(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// GetPasswordHash returns the bcrypt password hash for a user id.
func (r *AuthRepo) GetPasswordHash(ctx context.Context, id string) (string, error) {
	var hash string
	err := r.db.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1::uuid`, id).Scan(&hash)
	return hash, err
}

// UpdatePasswordHash replaces a user's password hash.
func (r *AuthRepo) UpdatePasswordHash(ctx context.Context, id, hash string) error {
	_, err := r.db.Exec(ctx, `UPDATE users SET password_hash = $1 WHERE id = $2::uuid`, hash, id)
	return err
}

// CreateFirstUser atomically inserts the initial admin user only when the users
// table is empty. Returns pgx.ErrNoRows if a user already exists, which the
// bootstrap handler treats as "already bootstrapped" (403).
//
// A transaction-scoped advisory lock serializes concurrent bootstrap attempts:
// under READ COMMITTED the bare `WHERE NOT EXISTS` guard is not sufficient
// because two racing statements with different emails would each see an empty
// table and both insert. The lock forces the second caller to wait until the
// first commits, after which its NOT EXISTS check fails and it gets ErrNoRows.
func (r *AuthRepo) CreateFirstUser(ctx context.Context, email, hash, role string) (*model.User, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(8675310)`); err != nil {
		return nil, err
	}

	var u model.User
	err = tx.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, role)
		SELECT $1, $2, $3 WHERE NOT EXISTS (SELECT 1 FROM users)
		RETURNING id::text, email, role, member_id::text, created_at, last_login_at`,
		email, hash, role).
		Scan(&u.ID, &u.Email, &u.Role, &u.MemberID, &u.CreatedAt, &u.LastLoginAt)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &u, nil
}

// RevokeAllRefreshTokensForUser revokes every active refresh token for a user.
func (r *AuthRepo) RevokeAllRefreshTokensForUser(ctx context.Context, userID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE refresh_tokens SET revoked = TRUE WHERE user_id = $1::uuid AND revoked = FALSE`,
		userID)
	return err
}

// ---- Password reset tokens ----

// CreatePasswordResetToken stores a hashed, time-limited, single-use reset token.
func (r *AuthRepo) CreatePasswordResetToken(ctx context.Context, userID, hash string, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO password_reset_tokens (user_id, token_hash, expires_at) VALUES ($1::uuid, $2, $3)`,
		userID, hash, expiresAt)
	return err
}

// ConsumePasswordResetToken atomically validates and consumes a reset token,
// returning the associated user id. Returns pgx.ErrNoRows if the token is
// unknown, already used, or expired.
func (r *AuthRepo) ConsumePasswordResetToken(ctx context.Context, hash string) (string, error) {
	var userID string
	err := r.db.QueryRow(ctx, `
		UPDATE password_reset_tokens SET used = TRUE
		WHERE token_hash = $1 AND used = FALSE AND expires_at > now()
		RETURNING user_id::text`, hash).Scan(&userID)
	return userID, err
}

// ---- TOTP two-factor authentication ----

// GetTOTP returns a user's stored TOTP secret and whether 2FA is enabled.
func (r *AuthRepo) GetTOTP(ctx context.Context, userID string) (secret string, enabled bool, err error) {
	var s *string
	err = r.db.QueryRow(ctx,
		`SELECT totp_secret, totp_enabled FROM users WHERE id = $1::uuid`, userID).Scan(&s, &enabled)
	if s != nil {
		secret = *s
	}
	return secret, enabled, err
}

// SetTOTPSecret stores a (not-yet-confirmed) TOTP secret, leaving 2FA disabled
// until the user verifies a code.
func (r *AuthRepo) SetTOTPSecret(ctx context.Context, userID, secret string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE users SET totp_secret = $2, totp_enabled = FALSE WHERE id = $1::uuid`, userID, secret)
	return err
}

// EnableTOTP marks 2FA active for a user (after a code has been verified).
func (r *AuthRepo) EnableTOTP(ctx context.Context, userID string) error {
	_, err := r.db.Exec(ctx, `UPDATE users SET totp_enabled = TRUE WHERE id = $1::uuid`, userID)
	return err
}

// DisableTOTP turns off 2FA, clears the secret, and removes all recovery codes.
func (r *AuthRepo) DisableTOTP(ctx context.Context, userID string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx,
		`UPDATE users SET totp_secret = NULL, totp_enabled = FALSE WHERE id = $1::uuid`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM mfa_recovery_codes WHERE user_id = $1::uuid`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReplaceRecoveryCodes deletes any existing recovery codes and stores the given
// hashes.
func (r *AuthRepo) ReplaceRecoveryCodes(ctx context.Context, userID string, hashes []string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `DELETE FROM mfa_recovery_codes WHERE user_id = $1::uuid`, userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.Exec(ctx,
			`INSERT INTO mfa_recovery_codes (user_id, code_hash) VALUES ($1::uuid, $2)`, userID, h); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ConsumeRecoveryCode marks one matching unused recovery code as used, returning
// true if a code was consumed.
func (r *AuthRepo) ConsumeRecoveryCode(ctx context.Context, userID, hash string) (bool, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE mfa_recovery_codes SET used = TRUE
		WHERE id = (SELECT id FROM mfa_recovery_codes
		            WHERE user_id = $1::uuid AND code_hash = $2 AND used = FALSE LIMIT 1)`,
		userID, hash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ActiveSessionCount counts a user's live refresh tokens (not revoked, not
// expired) — i.e. how many devices could still mint new access tokens.
func (r *AuthRepo) ActiveSessionCount(ctx context.Context, userID string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `
		SELECT count(*) FROM refresh_tokens
		WHERE user_id = $1::uuid AND revoked = FALSE AND expires_at > now()`, userID).Scan(&n)
	return n, err
}

// RevokeOtherRefreshTokensForUser revokes every live session for the user
// EXCEPT the one identified by keepHash (the caller's current refresh token), so
// "sign out everywhere else" doesn't sign the caller out too. Passing an empty
// keepHash revokes all of them. Returns how many were revoked.
func (r *AuthRepo) RevokeOtherRefreshTokensForUser(ctx context.Context, userID, keepHash string) (int64, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE refresh_tokens SET revoked = TRUE
		WHERE user_id = $1::uuid AND revoked = FALSE AND token_hash <> $2`,
		userID, keepHash)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// EnsureCalendarToken returns the user's calendar-subscription token, minting
// one on first use. Idempotent.
func (r *AuthRepo) EnsureCalendarToken(ctx context.Context, userID string) (string, error) {
	var tok *string
	if err := r.db.QueryRow(ctx, `SELECT calendar_token FROM users WHERE id = $1::uuid`, userID).Scan(&tok); err != nil {
		return "", err
	}
	if tok != nil && *tok != "" {
		return *tok, nil
	}
	return r.RotateCalendarToken(ctx, userID)
}

// RotateCalendarToken issues a fresh token (invalidating any existing feed URL).
func (r *AuthRepo) RotateCalendarToken(ctx context.Context, userID string) (string, error) {
	t, err := newOpaqueToken()
	if err != nil {
		return "", err
	}
	if _, err := r.db.Exec(ctx, `UPDATE users SET calendar_token = $1 WHERE id = $2::uuid`, t, userID); err != nil {
		return "", err
	}
	return t, nil
}

// UserIDByCalendarToken resolves a calendar feed token to its user id, or
// pgx.ErrNoRows if the token is unknown/revoked. The role gate means a token
// stops working the moment its owner is downgraded to restricted (or erased,
// which also nulls the token) — the feed carries org-wide meeting data that
// restricted users may not see, so the token alone must not grant it.
func (r *AuthRepo) UserIDByCalendarToken(ctx context.Context, token string) (string, error) {
	var id string
	err := r.db.QueryRow(ctx,
		`SELECT id::text FROM users WHERE calendar_token = $1 AND role <> 'restricted'`, token).Scan(&id)
	return id, err
}
