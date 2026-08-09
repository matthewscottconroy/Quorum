package repo

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// ActivityRepo assembles a member-safe "recent activity" feed from source
// tables (not the audit log, which carries raw URLs and money detail). Each
// query yields a short human summary and a timestamp; the handler merges and
// trims. Nothing here exposes amounts or personal data beyond display names
// already visible org-wide.
type ActivityRepo struct {
	db *pgxpool.Pool
}

// NewActivityRepo constructs the repo.
func NewActivityRepo(db *pgxpool.Pool) *ActivityRepo {
	return &ActivityRepo{db: db}
}

// Recent returns up to `limit` recent, org-visible events, newest first. Each
// sub-query is bounded and failures on any one source are skipped rather than
// failing the whole feed.
func (r *ActivityRepo) Recent(ctx context.Context, limit int) ([]model.ActivityEvent, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	var out []model.ActivityEvent
	collect := func(sql string) {
		rows, err := r.db.Query(ctx, sql, limit)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var e model.ActivityEvent
			if err := rows.Scan(&e.At, &e.Kind, &e.Summary); err == nil {
				out = append(out, e)
			}
		}
	}

	collect(`SELECT created_at, 'member',
		'New member: ' || display_name
		FROM members WHERE status = 'active' ORDER BY created_at DESC LIMIT $1`)
	collect(`SELECT created_at, 'meeting',
		'Meeting scheduled: ' || title
		FROM meetings ORDER BY created_at DESC LIMIT $1`)
	collect(`SELECT created_at, 'plan',
		'Plan added: ' || title
		FROM plans ORDER BY created_at DESC LIMIT $1`)
	// Only surface documents everyone can see: no minimum-role bar and not
	// restricted to a visibility group. The feed is member-wide, so a title
	// like "2026 layoff plan" must never leak from a role- or group-gated doc.
	collect(`SELECT created_at, 'resource',
		'Document added: ' || title
		FROM resources r
		WHERE r.visible_min_role IS NULL
		  AND NOT EXISTS (SELECT 1 FROM resource_groups rg WHERE rg.resource_id = r.id)
		ORDER BY created_at DESC LIMIT $1`)
	collect(`SELECT m.created_at, 'motion',
		'Motion ' || m.status || ': ' || m.title
		FROM motions m WHERE m.status IN ('open', 'carried', 'failed') ORDER BY m.created_at DESC LIMIT $1`)

	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > limit {
		out = out[:limit]
	}
	if out == nil {
		out = []model.ActivityEvent{}
	}
	return out, nil
}
