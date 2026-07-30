package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// MembersRepo provides PostgreSQL data access for members.
type MembersRepo struct {
	db *pgxpool.Pool
}

// NewMembersRepo constructs a MembersRepo backed by the given connection pool.
func NewMembersRepo(db *pgxpool.Pool) *MembersRepo {
	return &MembersRepo{db: db}
}

// MemberFilter holds the optional query parameters for listing members.
type MemberFilter struct {
	Search string
	Status string
	Tier   string
	Limit  int
	Offset int
}

// List returns a page of members matching the filter, plus the total count.
func (r *MembersRepo) List(ctx context.Context, f MemberFilter) ([]model.Member, int, error) {
	args := []any{}
	conds := []string{}
	idx := 1

	if f.Search != "" {
		conds = append(conds, fmt.Sprintf(
			"to_tsvector('english', m.display_name || ' ' || coalesce(m.email,'')) @@ plainto_tsquery('english', $%d)", idx))
		args = append(args, f.Search)
		idx++
	}
	if f.Status != "" {
		conds = append(conds, fmt.Sprintf("m.status = $%d", idx))
		args = append(args, f.Status)
		idx++
	}
	if f.Tier != "" {
		conds = append(conds, fmt.Sprintf("m.tier = $%d", idx))
		args = append(args, f.Tier)
		idx++
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	query := fmt.Sprintf(`
		SELECT COUNT(*) OVER() AS total_count,
		       m.id::text, m.display_name, m.email, m.phone, m.address,
		       m.tier, m.status, m.joined_at, m.notes, m.metadata,
		       m.created_at, m.updated_at,
		       coalesce((
		           SELECT di.status FROM dues_invoices di
		           WHERE di.member_id = m.id ORDER BY di.due_date DESC LIMIT 1
		       ), 'none') AS dues_status
		FROM members m
		%s
		ORDER BY m.display_name
		LIMIT $%d OFFSET $%d`, where, idx, idx+1)
	args = append(args, limit, f.Offset)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var members []model.Member
	var total int
	for rows.Next() {
		var m model.Member
		var metaBytes []byte
		if err := rows.Scan(&total,
			&m.ID, &m.DisplayName, &m.Email, &m.Phone, &m.Address,
			&m.Tier, &m.Status, &m.JoinedAt, &m.Notes, &metaBytes,
			&m.CreatedAt, &m.UpdatedAt, &m.DuesStatus,
		); err != nil {
			return nil, 0, err
		}
		if metaBytes != nil {
			json.Unmarshal(metaBytes, &m.Metadata) //nolint:errcheck
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// COUNT(*) OVER() yields no rows on an empty page, which would report
	// total=0 for an offset past the end; fall back to a plain count.
	if len(members) == 0 && f.Offset > 0 {
		countQuery := "SELECT count(*) FROM members m " + where
		if err := r.db.QueryRow(ctx, countQuery, args[:len(args)-2]...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}
	return members, total, nil
}

// Get returns the member with the given id, or pgx.ErrNoRows if none exists.
func (r *MembersRepo) Get(ctx context.Context, id string) (*model.Member, error) {
	row := r.db.QueryRow(ctx, `
		SELECT m.id::text, m.display_name, m.email, m.phone, m.address,
		       m.tier, m.status, m.joined_at, m.notes, m.metadata,
		       m.created_at, m.updated_at,
		       coalesce((
		           SELECT di.status FROM dues_invoices di
		           WHERE di.member_id = m.id ORDER BY di.due_date DESC LIMIT 1
		       ), 'none') AS dues_status
		FROM members m WHERE m.id = $1::uuid`, id)

	m, err := scanMember(row)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// Create inserts a new member and returns the stored row.
func (r *MembersRepo) Create(ctx context.Context, m *model.Member) (*model.Member, error) {
	var metaBytes []byte
	if m.Metadata != nil {
		var err error
		metaBytes, err = json.Marshal(m.Metadata)
		if err != nil {
			return nil, err
		}
	}

	row := r.db.QueryRow(ctx, `
		INSERT INTO members (display_name, email, phone, address, tier, status, joined_at, notes, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id::text, display_name, email, phone, address, tier, status, joined_at, notes, metadata, created_at, updated_at, 'none'`,
		m.DisplayName, m.Email, m.Phone, m.Address, m.Tier, m.Status, m.JoinedAt, m.Notes, metaBytes)

	created, err := scanMember(row)
	if err != nil {
		return nil, err
	}
	return &created, nil
}

var memberAllowedFields = map[string]bool{
	"display_name": true, "email": true, "phone": true, "address": true,
	"tier": true, "status": true, "joined_at": true, "notes": true, "metadata": true,
}

// Update applies the given field changes to the member and returns the updated row.
func (r *MembersRepo) Update(ctx context.Context, id string, fields map[string]any) (*model.Member, error) {
	sets := []string{}
	args := []any{}
	idx := 1
	for k, v := range fields {
		if !memberAllowedFields[k] {
			continue
		}
		sets = append(sets, fmt.Sprintf("%s = $%d", k, idx))
		args = append(args, v)
		idx++
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, id)

	query := fmt.Sprintf(`
		UPDATE members SET %s WHERE id = $%d::uuid
		RETURNING id::text, display_name, email, phone, address, tier, status, joined_at, notes, metadata, created_at, updated_at, 'none'`,
		strings.Join(sets, ", "), idx)

	created, err := scanMember(r.db.QueryRow(ctx, query, args...))
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// Delete removes the member, returning pgx.ErrNoRows if no such row existed.
func (r *MembersRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `UPDATE members SET status = 'inactive', updated_at = now() WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// Count returns the number of active members.
func (r *MembersRepo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT count(*) FROM members WHERE status = 'active'`).Scan(&n)
	return n, err
}

type scannable interface {
	Scan(dest ...any) error
}

func scanMember(row scannable) (model.Member, error) {
	var m model.Member
	var metaBytes []byte
	err := row.Scan(
		&m.ID, &m.DisplayName, &m.Email, &m.Phone, &m.Address,
		&m.Tier, &m.Status, &m.JoinedAt, &m.Notes, &metaBytes,
		&m.CreatedAt, &m.UpdatedAt, &m.DuesStatus,
	)
	if err != nil {
		return m, err
	}
	if metaBytes != nil {
		json.Unmarshal(metaBytes, &m.Metadata) //nolint:errcheck
	}
	return m, nil
}

// Erase implements a GDPR-style right-to-erasure for one member: it strips
// personal data from the member row (name, email, phone, address, notes,
// metadata) in place rather than deleting it.
//
// Deleting the row is not the right primitive here — dues invoices, payments,
// attendance, and votes reference the member, and an organization has a
// legitimate (often statutory) interest in keeping its financial and governance
// records intact. Anonymizing severs the link to a natural person while leaving
// the ledger and the meeting minutes consistent.
//
// It also unlinks and revokes any login: the account's member_id is cleared and
// its refresh tokens are revoked, so the erased person retains no access.
// Returns pgx.ErrNoRows if no such member exists.
func (r *MembersRepo) Erase(ctx context.Context, id string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// A stable, non-identifying placeholder keeps display_name's NOT NULL
	// constraint satisfied and keeps rows distinguishable in listings.
	tag, err := tx.Exec(ctx, `
		UPDATE members SET
			display_name = 'Erased member ' || left(id::text, 8),
			email        = NULL,
			phone        = NULL,
			address      = NULL,
			notes        = NULL,
			metadata     = NULL,
			status       = 'inactive',
			updated_at   = now()
		WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}

	// Revoke any sessions belonging to the linked account, then unlink it.
	if _, err := tx.Exec(ctx, `
		UPDATE refresh_tokens SET revoked = TRUE
		WHERE revoked = FALSE AND user_id IN (SELECT id FROM users WHERE member_id = $1::uuid)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET member_id = NULL WHERE member_id = $1::uuid`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
