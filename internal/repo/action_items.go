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
	Limit      int
	Offset     int
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
		       ai.status, ai.priority, ai.created_by::text, ai.created_at, ai.updated_at,
		       m.display_name
		FROM action_items ai
		LEFT JOIN members m ON m.id = ai.assignee_id
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
			&item.Status, &item.Priority, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt,
			&item.AssigneeName,
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
		       ai.status, ai.priority, ai.created_by::text, ai.created_at, ai.updated_at,
		       m.display_name
		FROM action_items ai
		LEFT JOIN members m ON m.id = ai.assignee_id
		WHERE ai.id = $1::uuid`, id)
	item, err := scanActionItem(row)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// Create inserts a new action item and returns the stored row.
func (r *ActionItemsRepo) Create(ctx context.Context, item *model.ActionItem, createdBy string) (*model.ActionItem, error) {
	row := r.db.QueryRow(ctx, `
		INSERT INTO action_items
		    (title, description, assignee_id, meeting_id, plan_id, due_date, status, priority, created_by)
		VALUES ($1, $2, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8, $9::uuid)
		RETURNING id::text, title, description, assignee_id::text,
		          meeting_id::text, plan_id::text, due_date,
		          status, priority, created_by::text, created_at, updated_at, NULL::text`,
		item.Title, item.Description, item.AssigneeID, item.MeetingID, item.PlanID,
		item.DueDate, item.Status, item.Priority, createdBy)
	created, err := scanActionItem(row)
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// Update applies the given field changes to the action item and returns the updated row.
func (r *ActionItemsRepo) Update(ctx context.Context, id string, fields map[string]any) (*model.ActionItem, error) {
	sets := []string{"updated_at = now()"}
	args := []any{}
	idx := 1

	allowed := map[string]bool{
		"title": true, "description": true, "assignee_id": true,
		"meeting_id": true, "plan_id": true,
		"due_date": true, "status": true, "priority": true,
	}
	uuidField := map[string]bool{"assignee_id": true, "meeting_id": true, "plan_id": true}
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
		&item.Status, &item.Priority, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt,
		&item.AssigneeName,
	)
	return item, err
}
