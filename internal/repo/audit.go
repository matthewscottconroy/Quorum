package repo

import (
	"context"
	"encoding/json"
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
// "" when the request targets no specific row (blank values are stored as SQL
// NULL). detail optionally records WHAT changed (e.g. {"set": {...}} or
// {"role_old": ..., "role_new": ...}); it is part of the hash chain, so it
// cannot later be rewritten undetected. Callers must never put personal
// profile data in detail — the log outlives erasure.
func (r *AuditRepo) Log(ctx context.Context, userID, action, entityType, entityID string, detail map[string]any) error {
	var eid, etype *string
	if entityID != "" {
		eid = &entityID
	}
	if entityType != "" {
		etype = &entityType
	}
	var detailArg any
	if len(detail) > 0 {
		b, err := json.Marshal(detail)
		if err != nil {
			return fmt.Errorf("audit detail: %w", err)
		}
		detailArg = b
	}
	_, err := r.db.Exec(ctx,
		`INSERT INTO audit_log (user_id, action, entity_type, entity_id, detail) VALUES ($1::uuid, $2, $3, $4, $5)`,
		userID, action, etype, eid, detailArg)
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
		// Escape LIKE metacharacters so a literal % or _ can be searched and a
		// user-supplied pattern can't turn into match-everything.
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Action)
		add("a.action ILIKE '%%' || $%d || '%%'", esc)
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

// ChainStatus is the result of verifying the audit log's hash chain.
type ChainStatus struct {
	OK        bool   `json:"ok"`
	Entries   int64  `json:"entries"`
	HeadSeq   int64  `json:"head_seq"`
	HeadHash  string `json:"head_hash"`
	BrokenSeq int64  `json:"broken_seq,omitempty"` // first bad row when !OK
}

// VerifyChain recomputes every row's digest (via the same audit_entry_digest
// SQL function the insert trigger uses) and checks each row's prev_hash against
// its predecessor's entry_hash. Any edit, mid-chain deletion, or unchained
// insert — including one made with triggers disabled — surfaces here. The
// oldest retained row's prev link is exempt: retention pruning removes the
// chain's prefix by design.
func (r *AuditRepo) VerifyChain(ctx context.Context) (*ChainStatus, error) {
	st := &ChainStatus{OK: true}
	err := r.db.QueryRow(ctx, `
		SELECT count(*), coalesce(max(seq), 0),
		       coalesce((SELECT entry_hash FROM audit_log ORDER BY seq DESC LIMIT 1), '')
		FROM audit_log`).Scan(&st.Entries, &st.HeadSeq, &st.HeadHash)
	if err != nil {
		return nil, err
	}
	var broken *int64
	err = r.db.QueryRow(ctx, `
		WITH chain AS (
			SELECT seq, entry_hash, prev_hash,
			       lag(entry_hash) OVER (ORDER BY seq) AS expected_prev,
			       audit_entry_digest(seq, user_id, action, entity_type, entity_id, detail, created_at, prev_hash) AS recomputed
			FROM audit_log
		)
		SELECT min(seq) FROM chain
		WHERE entry_hash <> recomputed
		   OR (expected_prev IS NOT NULL AND prev_hash IS DISTINCT FROM expected_prev)`).Scan(&broken)
	if err != nil {
		return nil, err
	}
	if broken != nil {
		st.OK = false
		st.BrokenSeq = *broken
	}
	return st, nil
}

// ExportRows streams the full retained audit log oldest-first with chain
// hashes, for the signed CSV export a third party can independently verify.
func (r *AuditRepo) ExportRows(ctx context.Context, fn func(e model.AuditEntry) error) error {
	rows, err := r.db.Query(ctx, `
		SELECT a.seq, a.id::text, a.user_id::text, u.email, a.action,
		       a.entity_type, a.entity_id, coalesce(a.detail::text, ''), a.created_at,
		       coalesce(a.prev_hash, ''), a.entry_hash
		FROM audit_log a LEFT JOIN users u ON u.id = a.user_id
		ORDER BY a.seq`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var e model.AuditEntry
		if err := rows.Scan(&e.Seq, &e.ID, &e.UserID, &e.UserEmail, &e.Action,
			&e.EntityType, &e.EntityID, &e.Detail, &e.CreatedAt, &e.PrevHash, &e.EntryHash); err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return rows.Err()
}
