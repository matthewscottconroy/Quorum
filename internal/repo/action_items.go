package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// ActionItemsRepo provides PostgreSQL data access for action items.
type ActionItemsRepo struct {
	db *pgxpool.Pool
}

// NewActionItemsRepo constructs a ActionItemsRepo backed by the given connection pool.
func NewActionItemsRepo(db *pgxpool.Pool) *ActionItemsRepo {
	return &ActionItemsRepo{db: db}
}

// ActionItemFilter holds the optional query parameters for listing action items.
type ActionItemFilter struct {
	AssigneeID string
	MeetingID  string
	PlanID     string
	Status     string
	// SprintID filters by sprint; the special value "none" selects the backlog
	// (items with no sprint).
	SprintID string
	Limit    int
	Offset   int
}

// List returns a page of action items matching the filter, plus the total count.
func (r *ActionItemsRepo) List(ctx context.Context, f ActionItemFilter) ([]model.ActionItem, int, error) {
	args := []any{}
	conds := []string{}
	idx := 1

	if f.AssigneeID != "" {
		conds = append(conds, fmt.Sprintf("ai.assignee_id = $%d::uuid", idx))
		args = append(args, f.AssigneeID)
		idx++
	}
	if f.MeetingID != "" {
		conds = append(conds, fmt.Sprintf("ai.meeting_id = $%d::uuid", idx))
		args = append(args, f.MeetingID)
		idx++
	}
	if f.PlanID != "" {
		conds = append(conds, fmt.Sprintf("ai.plan_id = $%d::uuid", idx))
		args = append(args, f.PlanID)
		idx++
	}
	if f.Status != "" {
		conds = append(conds, fmt.Sprintf("ai.status = $%d", idx))
		args = append(args, f.Status)
		idx++
	}
	if f.SprintID == "none" {
		conds = append(conds, "ai.sprint_id IS NULL")
	} else if f.SprintID != "" {
		conds = append(conds, fmt.Sprintf("ai.sprint_id = $%d::uuid", idx))
		args = append(args, f.SprintID)
		idx++
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	query := fmt.Sprintf(`
		SELECT COUNT(*) OVER() AS total_count,
		       ai.id::text, ai.title, ai.description, ai.assignee_id::text,
		       ai.meeting_id::text, ai.plan_id::text, ai.due_date,
		       ai.status, ai.priority, ai.sprint_id::text, sp.name, ai.column_id::text,
		       ai.created_by::text, ai.created_at, ai.updated_at,
		       m.display_name,
		       (SELECT count(*) FROM action_item_comments c WHERE c.action_item_id = ai.id)::int,
		       ai.story_points, ai.card_type, ai.parent_id::text, par.title,
		       coalesce(rm.display_name, ru.email, ''),
		       (SELECT coalesce(json_agg(json_build_object('member_id', cb.member_id::text, 'member_name', cm.display_name) ORDER BY cm.display_name), '[]'::json)
		        FROM action_item_contributors cb JOIN members cm ON cm.id = cb.member_id
		        WHERE cb.action_item_id = ai.id)
		FROM action_items ai
		LEFT JOIN members m ON m.id = ai.assignee_id
		LEFT JOIN sprints sp ON sp.id = ai.sprint_id
		LEFT JOIN action_items par ON par.id = ai.parent_id
		LEFT JOIN users ru ON ru.id = ai.created_by
		LEFT JOIN members rm ON rm.id = ru.member_id
		%s
		ORDER BY
			CASE ai.priority WHEN 'high' THEN 0 WHEN 'normal' THEN 1 ELSE 2 END,
			ai.due_date NULLS LAST, ai.created_at
		LIMIT $%d OFFSET $%d`, where, idx, idx+1)
	args = append(args, limit, f.Offset)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var items []model.ActionItem
	var total int
	for rows.Next() {
		var item model.ActionItem
		if err := rows.Scan(&total,
			&item.ID, &item.Title, &item.Description, &item.AssigneeID,
			&item.MeetingID, &item.PlanID, &item.DueDate,
			&item.Status, &item.Priority, &item.SprintID, &item.SprintName, &item.ColumnID,
			&item.CreatedBy, &item.CreatedAt, &item.UpdatedAt,
			&item.AssigneeName, &item.CommentCount,
			&item.StoryPoints, &item.CardType, &item.ParentID, &item.ParentTitle,
			&item.ReporterName, &item.Contributors,
		); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// COUNT(*) OVER() yields no rows on an empty page; fall back to a plain count.
	if len(items) == 0 && f.Offset > 0 {
		countQuery := "SELECT count(*) FROM action_items ai " + where
		if err := r.db.QueryRow(ctx, countQuery, args[:len(args)-2]...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}
	return items, total, nil
}

// Get returns the action item with the given id, or pgx.ErrNoRows if none exists.
func (r *ActionItemsRepo) Get(ctx context.Context, id string) (*model.ActionItem, error) {
	row := r.db.QueryRow(ctx, `
		SELECT ai.id::text, ai.title, ai.description, ai.assignee_id::text,
		       ai.meeting_id::text, ai.plan_id::text, ai.due_date,
		       ai.status, ai.priority, ai.sprint_id::text, sp.name, ai.column_id::text,
		       ai.created_by::text, ai.created_at, ai.updated_at,
		       m.display_name,
		       (SELECT count(*) FROM action_item_comments c WHERE c.action_item_id = ai.id)::int,
		       ai.story_points, ai.card_type, ai.parent_id::text, par.title,
		       coalesce(rm.display_name, ru.email, ''),
		       (SELECT coalesce(json_agg(json_build_object('member_id', cb.member_id::text, 'member_name', cm.display_name) ORDER BY cm.display_name), '[]'::json)
		        FROM action_item_contributors cb JOIN members cm ON cm.id = cb.member_id
		        WHERE cb.action_item_id = ai.id)
		FROM action_items ai
		LEFT JOIN members m ON m.id = ai.assignee_id
		LEFT JOIN sprints sp ON sp.id = ai.sprint_id
		LEFT JOIN action_items par ON par.id = ai.parent_id
		LEFT JOIN users ru ON ru.id = ai.created_by
		LEFT JOIN members rm ON rm.id = ru.member_id
		WHERE ai.id = $1::uuid`, id)
	item, err := scanActionItem(row)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// Create inserts a new action item and returns the stored row.
func (r *ActionItemsRepo) Create(ctx context.Context, item *model.ActionItem, createdBy string) (*model.ActionItem, error) {
	if item.CardType == "" {
		item.CardType = "task"
	}
	// Insert, then read back through Get so derived fields (sprint name,
	// reporter name — resolved via joins RETURNING cannot express) are
	// populated identically to every other read path.
	var id string
	if err := r.db.QueryRow(ctx, `
		INSERT INTO action_items
		    (title, description, assignee_id, meeting_id, plan_id, due_date, status, priority, sprint_id,
		     story_points, card_type, parent_id, created_by)
		VALUES ($1, $2, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8, $9::uuid, $10, $11, $12::uuid, $13::uuid)
		RETURNING id::text`,
		item.Title, item.Description, item.AssigneeID, item.MeetingID, item.PlanID,
		item.DueDate, item.Status, item.Priority, item.SprintID,
		item.StoryPoints, item.CardType, item.ParentID, createdBy).Scan(&id); err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// Update applies the given field changes to the action item and returns the updated row.
func (r *ActionItemsRepo) Update(ctx context.Context, id string, fields map[string]any) (*model.ActionItem, error) {
	sets := []string{"updated_at = now()"}
	args := []any{}
	idx := 1

	allowed := map[string]bool{
		"title": true, "description": true, "assignee_id": true,
		"meeting_id": true, "plan_id": true, "sprint_id": true,
		"due_date": true, "status": true, "priority": true, "column_id": true,
		"story_points": true, "card_type": true, "parent_id": true,
	}
	uuidField := map[string]bool{"assignee_id": true, "meeting_id": true, "plan_id": true, "sprint_id": true, "column_id": true, "parent_id": true}
	for k, v := range fields {
		if !allowed[k] {
			continue
		}
		if uuidField[k] {
			sets = append(sets, fmt.Sprintf("%s = $%d::uuid", k, idx))
		} else {
			sets = append(sets, fmt.Sprintf("%s = $%d", k, idx))
		}
		args = append(args, v)
		idx++
	}
	args = append(args, id)

	query := fmt.Sprintf(`
		UPDATE action_items SET %s WHERE id = $%d::uuid`,
		strings.Join(sets, ", "), idx)
	if _, err := r.db.Exec(ctx, query, args...); err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// Delete removes the action item, returning pgx.ErrNoRows if no such row existed.
func (r *ActionItemsRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM action_items WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// AssigneeEmail returns the assignee's email, or "" if the item is unassigned
// or the assignee has no email on file. Used to notify the affected member on
// delete.
func (r *ActionItemsRepo) AssigneeEmail(ctx context.Context, id string) (string, error) {
	var email string
	err := r.db.QueryRow(ctx, `
		SELECT m.email FROM action_items ai
		JOIN members m ON m.id = ai.assignee_id
		WHERE ai.id = $1::uuid AND m.email IS NOT NULL AND m.email <> ''`, id).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return email, err
}

func scanActionItem(row scannable) (model.ActionItem, error) {
	var item model.ActionItem
	err := row.Scan(
		&item.ID, &item.Title, &item.Description, &item.AssigneeID,
		&item.MeetingID, &item.PlanID, &item.DueDate,
		&item.Status, &item.Priority, &item.SprintID, &item.SprintName, &item.ColumnID,
		&item.CreatedBy, &item.CreatedAt, &item.UpdatedAt,
		&item.AssigneeName, &item.CommentCount,
		&item.StoryPoints, &item.CardType, &item.ParentID, &item.ParentTitle,
		&item.ReporterName, &item.Contributors,
	)
	return item, err
}

// SetContributors replaces the card's contributor list and returns the
// updated card. Unknown member IDs fail the insert's FK.
func (r *ActionItemsRepo) SetContributors(ctx context.Context, id string, memberIDs []string) (*model.ActionItem, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// The card must exist; DELETE alone would silently "succeed" for a bogus id.
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT true FROM action_items WHERE id = $1::uuid`, id).Scan(&exists); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM action_item_contributors WHERE action_item_id = $1::uuid`, id); err != nil {
		return nil, err
	}
	for _, mid := range memberIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO action_item_contributors (action_item_id, member_id)
			VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, id, mid); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}
