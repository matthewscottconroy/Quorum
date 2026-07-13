package repo

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

type AuthRepo struct {
	db *pgxpool.Pool
}

func NewAuthRepo(db *pgxpool.Pool) *AuthRepo {
	return &AuthRepo{db: db}
}

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

func (r *AuthRepo) GetUserByID(ctx context.Context, id string) (*model.User, error) {
	var u model.User
	err := r.db.QueryRow(ctx, `
		SELECT id::text, email, role, member_id::text, created_at, last_login_at
		FROM users WHERE id = $1::uuid`, id).
		Scan(&u.ID, &u.Email, &u.Role, &u.MemberID, &u.CreatedAt, &u.LastLoginAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *AuthRepo) CreateUser(ctx context.Context, email, hash, role string) (*model.User, error) {
	var u model.User
	err := r.db.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, role)
		VALUES ($1, $2, $3)
		RETURNING id::text, email, role, member_id::text, created_at, last_login_at`,
		email, hash, role).
		Scan(&u.ID, &u.Email, &u.Role, &u.MemberID, &u.CreatedAt, &u.LastLoginAt)
	return &u, err
}

func (r *AuthRepo) UpdateLastLogin(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1::uuid`, id)
	return err
}

func (r *AuthRepo) StoreRefreshToken(ctx context.Context, userID, hash string, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO refresh_tokens (user_id, token_hash, expires_at)
		VALUES ($1::uuid, $2, $3)`, userID, hash, expiresAt)
	return err
}

func (r *AuthRepo) GetRefreshToken(ctx context.Context, hash string) (userID string, revoked bool, expiresAt time.Time, err error) {
	err = r.db.QueryRow(ctx, `
		SELECT user_id::text, revoked, expires_at
		FROM refresh_tokens WHERE token_hash = $1`, hash).
		Scan(&userID, &revoked, &expiresAt)
	return
}

func (r *AuthRepo) RevokeRefreshToken(ctx context.Context, hash string) error {
	_, err := r.db.Exec(ctx, `UPDATE refresh_tokens SET revoked = TRUE WHERE token_hash = $1`, hash)
	return err
}

func (r *AuthRepo) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (r *AuthRepo) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id::text, email, role, member_id::text, created_at, last_login_at
		FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []model.User
	for rows.Next() {
		var u model.User
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.MemberID, &u.CreatedAt, &u.LastLoginAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

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

func (r *AuthRepo) DeleteUser(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, id)
	return err
}

func (r *AuthRepo) GetPasswordHash(ctx context.Context, id string) (string, error) {
	var hash string
	err := r.db.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1::uuid`, id).Scan(&hash)
	return hash, err
}

func (r *AuthRepo) UpdatePasswordHash(ctx context.Context, id, hash string) error {
	_, err := r.db.Exec(ctx, `UPDATE users SET password_hash = $1 WHERE id = $2::uuid`, hash, id)
	return err
}

// CreateFirstUser atomically inserts the initial admin user only when the users
// table is empty. Returns pgx.ErrNoRows if a user already exists, which the
// bootstrap handler treats as "already bootstrapped" (403).
func (r *AuthRepo) CreateFirstUser(ctx context.Context, email, hash, role string) (*model.User, error) {
	var u model.User
	err := r.db.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, role)
		SELECT $1, $2, $3 WHERE NOT EXISTS (SELECT 1 FROM users)
		RETURNING id::text, email, role, member_id::text, created_at, last_login_at`,
		email, hash, role).
		Scan(&u.ID, &u.Email, &u.Role, &u.MemberID, &u.CreatedAt, &u.LastLoginAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *AuthRepo) RevokeAllRefreshTokensForUser(ctx context.Context, userID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE refresh_tokens SET revoked = TRUE WHERE user_id = $1::uuid AND revoked = FALSE`,
		userID)
	return err
}
