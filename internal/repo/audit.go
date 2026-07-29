package repo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// AuditRepo writes entries to the audit_log table.
type AuditRepo struct {
	db *pgxpool.Pool
}

// NewAuditRepo constructs an AuditRepo backed by the given pool.
func NewAuditRepo(db *pgxpool.Pool) *AuditRepo {
	return &AuditRepo{db: db}
}

// Log records a mutating action by a user against a typed entity. entityType
// is the resource kind (e.g. "members"); entityID is the affected row's id, or
// "" when the request targets no specific row. A blank entityID is stored as
// SQL NULL so the column carries only real ids.
func (r *AuditRepo) Log(ctx context.Context, userID, action, entityType, entityID string) error {
	var eid, etype *string
	if entityID != "" {
		eid = &entityID
	}
	if entityType != "" {
		etype = &entityType
	}
	_, err := r.db.Exec(ctx,
		`INSERT INTO audit_log (user_id, action, entity_type, entity_id) VALUES ($1::uuid, $2, $3, $4)`,
		userID, action, etype, eid)
	return err
}

// AuditFilter narrows an audit-log query. All fields are optional.
type AuditFilter struct {
	UserID     string // exact actor
	Action     string // substring match, e.g. "DELETE" or "auth."
	EntityType string // exact resource kind, e.g. "members"
	EntityID   string // exact affected row
	Since      *time.Time
	Until      *time.Time
	Limit      int
	Offset     int
}

// List returns audit entries newest-first with the actor's email resolved, plus
// the total matching count for pagination. The log is append-only and read by
// admins to answer "who changed which record, and when".
func (r *AuditRepo) List(ctx context.Context, f AuditFilter) ([]model.AuditEntry, int, error) {
	var conds []string
	var args []any
	add := func(cond string, val any) {
		args = append(args, val)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.UserID != "" {
		add("a.user_id = $%d::uuid", f.UserID)
	}
	if f.Action != "" {
		add("a.action ILIKE '%%' || $%d || '%%'", f.Action)
	}
	if f.EntityType != "" {
		add("a.entity_type = $%d", f.EntityType)
	}
	if f.EntityID != "" {
		add("a.entity_id = $%d", f.EntityID)
	}
	if f.Since != nil {
		add("a.created_at >= $%d", *f.Since)
	}
	if f.Until != nil {
		add("a.created_at <= $%d", *f.Until)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM audit_log a`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit, f.Offset)
	query := `
		SELECT a.id::text, a.user_id::text, u.email, a.action, a.entity_type, a.entity_id, a.created_at
		FROM audit_log a
		LEFT JOIN users u ON u.id = a.user_id` + where +
		fmt.Sprintf(" ORDER BY a.created_at DESC, a.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]model.AuditEntry, 0)
	for rows.Next() {
		var e model.AuditEntry
		if err := rows.Scan(&e.ID, &e.UserID, &e.UserEmail, &e.Action, &e.EntityType, &e.EntityID, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// PruneOlderThan deletes audit entries older than the cutoff, returning how many
// were removed. Retention is a policy decision (see PRODUCTION_READINESS §3);
// this is the mechanism the nightly job uses to enforce it.
func (r *AuditRepo) PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM audit_log WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
