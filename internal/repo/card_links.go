package repo

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// CardLinksRepo provides data access for typed card relationships, plus the
// sprint analytics aggregation (it lives here because "blocked" is a
// link-derived metric).
type CardLinksRepo struct {
	db *pgxpool.Pool
}

// NewCardLinksRepo constructs the repo.
func NewCardLinksRepo(db *pgxpool.Pool) *CardLinksRepo {
	return &CardLinksRepo{db: db}
}

const cardLinkSelect = `
	SELECT l.id::text, l.from_id::text, l.to_id::text, l.kind,
	       f.title, t.title, l.created_at
	FROM card_links l
	JOIN action_items f ON f.id = l.from_id
	JOIN action_items t ON t.id = l.to_id`

func scanCardLink(row scannable) (model.CardLink, error) {
	var l model.CardLink
	err := row.Scan(&l.ID, &l.FromID, &l.ToID, &l.Kind, &l.FromTitle, &l.ToTitle, &l.CreatedAt)
	return l, err
}

// ListForCard returns every link touching the card, from either side.
func (r *CardLinksRepo) ListForCard(ctx context.Context, cardID string) ([]model.CardLink, error) {
	rows, err := r.db.Query(ctx,
		cardLinkSelect+` WHERE l.from_id = $1::uuid OR l.to_id = $1::uuid ORDER BY l.created_at`, cardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.CardLink
	for rows.Next() {
		l, err := scanCardLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Create adds a link and returns it with titles resolved.
func (r *CardLinksRepo) Create(ctx context.Context, fromID, toID, kind, createdBy string) (*model.CardLink, error) {
	var id string
	if err := r.db.QueryRow(ctx, `
		INSERT INTO card_links (from_id, to_id, kind, created_by)
		VALUES ($1::uuid, $2::uuid, $3, $4::uuid) RETURNING id::text`,
		fromID, toID, kind, createdBy).Scan(&id); err != nil {
		return nil, err
	}
	l, err := scanCardLink(r.db.QueryRow(ctx, cardLinkSelect+` WHERE l.id = $1::uuid`, id))
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// Delete removes a link, returning pgx.ErrNoRows if it did not exist.
func (r *CardLinksRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM card_links WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// SprintAnalytics computes the sprint health picture in one round trip per
// aggregate. Cancelled cards are excluded from totals but reported.
// "Blocked" = the card has a blocked_by link whose blocker is not done, or a
// depends_on link whose dependency is not done.
func (r *CardLinksRepo) SprintAnalytics(ctx context.Context, sprintID string) (*model.SprintAnalytics, error) {
	a := &model.SprintAnalytics{}

	err := r.db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status <> 'cancelled'),
		       coalesce(sum(story_points) FILTER (WHERE status <> 'cancelled'), 0),
		       count(*) FILTER (WHERE status = 'done'),
		       coalesce(sum(story_points) FILTER (WHERE status = 'done'), 0),
		       count(*) FILTER (WHERE status = 'cancelled'),
		       count(*) FILTER (WHERE status <> 'cancelled' AND story_points IS NULL)
		FROM action_items WHERE sprint_id = $1::uuid`, sprintID).
		Scan(&a.Cards, &a.Points, &a.DoneCards, &a.DonePoints, &a.CancelledCards, &a.UnpointedCards)
	if err != nil {
		return nil, err
	}

	err = r.db.QueryRow(ctx, `
		SELECT count(DISTINCT ai.id)
		FROM action_items ai
		JOIN card_links l ON (l.kind = 'blocked_by' AND l.from_id = ai.id)
		                  OR (l.kind = 'depends_on' AND l.from_id = ai.id)
		JOIN action_items other ON other.id = l.to_id
		WHERE ai.sprint_id = $1::uuid
		  AND ai.status NOT IN ('done', 'cancelled')
		  AND other.status NOT IN ('done', 'cancelled')`, sprintID).Scan(&a.BlockedCards)
	if err != nil {
		return nil, err
	}

	bucket := func(expr, order string) ([]model.SprintBucket, error) {
		rows, err := r.db.Query(ctx, `
			SELECT `+expr+` AS key,
			       count(*), coalesce(sum(ai.story_points), 0),
			       count(*) FILTER (WHERE ai.status = 'done'),
			       coalesce(sum(ai.story_points) FILTER (WHERE ai.status = 'done'), 0)
			FROM action_items ai
			LEFT JOIN members m ON m.id = ai.assignee_id
			WHERE ai.sprint_id = $1::uuid AND ai.status <> 'cancelled'
			GROUP BY 1 ORDER BY `+order, sprintID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []model.SprintBucket
		for rows.Next() {
			var b model.SprintBucket
			if err := rows.Scan(&b.Key, &b.Cards, &b.Points, &b.DoneCards, &b.DonePoints); err != nil {
				return nil, err
			}
			out = append(out, b)
		}
		return out, rows.Err()
	}

	if a.ByType, err = bucket(`ai.card_type`, `3 DESC, 2 DESC`); err != nil {
		return nil, err
	}
	if a.ByStatus, err = bucket(`ai.status`, `1`); err != nil {
		return nil, err
	}
	if a.ByAssignee, err = bucket(`coalesce(m.display_name, 'unassigned')`, `3 DESC, 2 DESC`); err != nil {
		return nil, err
	}
	return a, nil
}
