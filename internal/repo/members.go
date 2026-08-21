package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// ErrErasureLinkedAdmin is returned by Erase when the member's linked user
// account is an admin or superadmin: retiring such an account as a side effect
// could lock the organization out, so it must be demoted or unlinked first.
var ErrErasureLinkedAdmin = errors.New("member's linked account is an admin; demote or unlink it before erasing")

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
	// 500 matches the governance dropdowns' need: an officer recording a
	// roll-call vote must see EVERY active member, not the first page.
	if limit <= 0 || limit > 500 {
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

// BatchUpdate applies the same allowlisted field changes to many members in
// one transaction. Returns the number of rows updated. Unknown fields are
// ignored (matching Update); an empty id list or field set is a no-op.
func (r *MembersRepo) BatchUpdate(ctx context.Context, ids []string, fields map[string]any) (int64, error) {
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
	if len(sets) == 0 || len(ids) == 0 {
		return 0, nil
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, ids)
	tag, err := r.db.Exec(ctx, fmt.Sprintf(
		`UPDATE members SET %s WHERE id = ANY($%d::uuid[])`, strings.Join(sets, ", "), idx), args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
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

// IDsByMinRole returns active member IDs whose linked user account holds a
// role at or above the given rank (roleRank ladder). Members with no user
// account never match: role is a property of the login, not the person.
func (r *MembersRepo) IDsByMinRole(ctx context.Context, minRank int) ([]string, error) {
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT u.member_id::text
		FROM users u
		JOIN members m ON m.id = u.member_id
		WHERE m.status = 'active'
		  AND CASE u.role WHEN 'restricted' THEN 1 WHEN 'member' THEN 2
		                  WHEN 'officer' THEN 3 WHEN 'admin' THEN 4
		                  WHEN 'superadmin' THEN 5 ELSE 0 END >= $1`, minRank)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
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
// It also fully retires any linked login: the account's email (PII) is replaced
// with a placeholder, its password hash is set to an unusable sentinel (bcrypt
// can never verify against it, so login is impossible), 2FA material and
// recovery codes are cleared, sessions are revoked, the role drops to
// restricted, and the account is unlinked. The account row is anonymized rather
// than deleted because created_by foreign keys (meetings, plans, …) RESTRICT
// deletion. Unused ballot tokens are removed so an emailed ballot link cannot
// outlive the erasure.
//
// Erasing a member whose linked account is an admin or superadmin is refused
// (ErrErasureLinkedAdmin): retiring such an account as a side effect of a member
// operation could lock the org out — demote or unlink it first, deliberately.
// Returns pgx.ErrNoRows if no such member exists.
// regexpEscape neutralizes POSIX-regex metacharacters in user text so a
// display name like "R. (Bob) O'Neil++" can ride inside regexp_replace.
func regexpEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (r *MembersRepo) Erase(ctx context.Context, id string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Refuse before touching anything if the linked account holds admin power.
	var adminLinked bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM users
		              WHERE member_id = $1::uuid AND role IN ('admin', 'superadmin'))`, id).
		Scan(&adminLinked); err != nil {
		return err
	}
	if adminLinked {
		return ErrErasureLinkedAdmin
	}

	// Right-to-erasure vs the immutable minutes snapshot: the snapshot is
	// the one place a finalized document preserves the member's real name
	// verbatim (attendance, ballots by name), and legal erasure outranks
	// finality. Scrub the display name from every snapshot BEFORE the
	// member row is anonymized, using the same placeholder — the document
	// stays structurally intact, the identity goes. (The core-guard trigger
	// only protects the meeting HEADER columns, so this write is allowed.)
	var oldName string
	if err := tx.QueryRow(ctx,
		`SELECT display_name FROM members WHERE id = $1::uuid`, id).Scan(&oldName); err != nil {
		return err
	}
	if oldName != "" {
		// Word-boundary regex, not raw replace(): erasing "Ann" must not
		// mangle "Annette Wu" or the word "May" in prose — this rewrites the
		// permanent official record, so collateral damage is forever. The
		// name is regex-escaped (names are arbitrary user text) and the
		// placeholder matches the member-row spelling exactly (lowercased id).
		placeholder := "Erased member " + strings.ToLower(id)[:8]
		if _, err := tx.Exec(ctx, `
			UPDATE meetings
			SET minutes_snapshot = regexp_replace(minutes_snapshot, '\m' || $1 || '\M', $2, 'g')
			WHERE minutes_snapshot ~ ('\m' || $1 || '\M')`,
			regexpEscape(oldName), placeholder); err != nil {
			return err
		}
	}

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

	// Retire the linked account(s) while still linked: revoke sessions, clear
	// 2FA + recovery codes, replace the email, and disable the password. The
	// '!erased' sentinel is not a valid bcrypt hash, so CheckPassword can never
	// succeed against it.
	if _, err := tx.Exec(ctx, `
		UPDATE refresh_tokens SET revoked = TRUE
		WHERE revoked = FALSE AND user_id IN (SELECT id FROM users WHERE member_id = $1::uuid)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM mfa_recovery_codes
		WHERE user_id IN (SELECT id FROM users WHERE member_id = $1::uuid)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE users SET
			email          = 'erased-' || left(id::text, 8) || '@erased.invalid',
			password_hash  = '!erased',
			totp_secret    = NULL,
			totp_enabled   = FALSE,
			role           = 'restricted',
			member_id      = NULL,
			calendar_token = NULL
		WHERE member_id = $1::uuid`, id); err != nil {
		return err
	}

	// An unused emailed ballot link must not let an erased member keep voting.
	if _, err := tx.Exec(ctx, `
		DELETE FROM ballot_tokens WHERE member_id = $1::uuid AND used = FALSE`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ExistingEmails returns the set of member emails already on file (lowercased),
// so an import can flag duplicates before inserting.
func (r *MembersRepo) ExistingEmails(ctx context.Context) (map[string]bool, error) {
	rows, err := r.db.Query(ctx, `SELECT lower(email) FROM members WHERE email IS NOT NULL AND email <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		set[e] = true
	}
	return set, rows.Err()
}

// BatchCreate inserts many members in one transaction, returning the count.
// Used by CSV import after the handler has validated and de-duplicated rows.
func (r *MembersRepo) BatchCreate(ctx context.Context, members []*model.Member) (int, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	n := 0
	for _, m := range members {
		if _, err := tx.Exec(ctx, `
			INSERT INTO members (display_name, email, phone, address, tier, status, joined_at, notes)
			VALUES ($1, nullif($2,''), nullif($3,''), nullif($4,''), $5, $6, $7, nullif($8,''))`,
			m.DisplayName, deref(m.Email), deref(m.Phone), deref(m.Address),
			m.Tier, m.Status, m.JoinedAt, deref(m.Notes)); err != nil {
			return 0, err
		}
		n++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return n, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// LapseOverdueMembers moves active members to 'inactive' when they have an
// invoice overdue by more than `days` days. Returns the count changed. Used by
// the nightly renewal-lapse automation; a no-op when days <= 0.
func (r *MembersRepo) LapseOverdueMembers(ctx context.Context, days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE members SET status = 'inactive', updated_at = now()
		WHERE status = 'active' AND id IN (
			SELECT DISTINCT member_id FROM dues_invoices
			WHERE member_id IS NOT NULL AND status = 'overdue'
			  AND due_date < current_date - ($1 || ' days')::interval
		)`, days)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
