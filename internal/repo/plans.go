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

// PlansRepo provides PostgreSQL data access for plans.
type PlansRepo struct {
	db *pgxpool.Pool
}

// NewPlansRepo constructs a PlansRepo backed by the given connection pool.
func NewPlansRepo(db *pgxpool.Pool) *PlansRepo {
	return &PlansRepo{db: db}
}

// PlanFilter holds the optional query parameters for listing plans.
type PlanFilter struct {
	Status string
	Limit  int
	Offset int
}

// List returns a page of plans matching the filter, plus the total count.
func (r *PlansRepo) List(ctx context.Context, f PlanFilter) ([]model.Plan, int, error) {
	where := ""
	args := []any{}
	if f.Status != "" {
		where = "WHERE p.status = $1"
		args = append(args, f.Status)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	limitIdx := len(args) + 1
	args = append(args, limit, f.Offset)

	query := fmt.Sprintf(`
		SELECT COUNT(*) OVER() AS total_count,
		       p.id::text, p.title, p.description, p.status, p.owner_id::text,
		       p.target_date, p.created_by::text, p.created_at, p.updated_at,
		       m.display_name, p.estimated_cost_minor, p.cost_currency
		FROM plans p
		LEFT JOIN members m ON m.id = p.owner_id
		%s
		ORDER BY p.updated_at DESC
		LIMIT $%d OFFSET $%d`, where, limitIdx, limitIdx+1)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var plans []model.Plan
	var total int
	for rows.Next() {
		var p model.Plan
		if err := rows.Scan(&total,
			&p.ID, &p.Title, &p.Description, &p.Status, &p.OwnerID,
			&p.TargetDate, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.OwnerName,
			&p.EstimatedCostMinor, &p.CostCurrency,
		); err != nil {
			return nil, 0, err
		}
		plans = append(plans, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// COUNT(*) OVER() yields no rows on an empty page; fall back to a plain count.
	if len(plans) == 0 && f.Offset > 0 {
		countQuery := "SELECT count(*) FROM plans p " + where
		if err := r.db.QueryRow(ctx, countQuery, args[:len(args)-2]...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}
	return plans, total, nil
}

// Get returns the plan with the given id, or pgx.ErrNoRows if none exists.
func (r *PlansRepo) Get(ctx context.Context, id string) (*model.Plan, error) {
	row := r.db.QueryRow(ctx, `
		SELECT p.id::text, p.title, p.description, p.status, p.owner_id::text,
		       p.target_date, p.created_by::text, p.created_at, p.updated_at,
		       m.display_name, p.estimated_cost_minor, p.cost_currency
		FROM plans p
		LEFT JOIN members m ON m.id = p.owner_id
		WHERE p.id = $1::uuid`, id)
	pl, err := scanPlan(row)
	if err != nil {
		return nil, err
	}
	pl.Decisions, err = r.GetDecisions(ctx, id)
	if err != nil {
		return nil, err
	}
	return &pl, nil
}

// Create inserts a new plan and returns the stored row.
func (r *PlansRepo) Create(ctx context.Context, p *model.Plan, createdBy string) (*model.Plan, error) {
	row := r.db.QueryRow(ctx, `
		INSERT INTO plans (title, description, status, owner_id, target_date, estimated_cost_minor, cost_currency, created_by)
		VALUES ($1, $2, $3, $4::uuid, $5, $6, $7, $8::uuid)
		RETURNING id::text, title, description, status, owner_id::text,
		          target_date, created_by::text, created_at, updated_at, NULL::text,
		          estimated_cost_minor, cost_currency`,
		p.Title, p.Description, p.Status, p.OwnerID, p.TargetDate, p.EstimatedCostMinor, p.CostCurrency, createdBy)
	pl, err := scanPlan(row)
	if err != nil {
		return nil, err
	}
	return &pl, nil
}

// Update applies the given field changes to the plan and returns the updated row.
func (r *PlansRepo) Update(ctx context.Context, id string, fields map[string]any) (*model.Plan, error) {
	sets := []string{"updated_at = now()"}
	args := []any{}
	idx := 1

	allowed := map[string]bool{
		"title": true, "description": true, "status": true, "owner_id": true, "target_date": true,
		"estimated_cost_minor": true, "cost_currency": true,
	}
	for k, v := range fields {
		if !allowed[k] {
			continue
		}
		if k == "owner_id" {
			sets = append(sets, fmt.Sprintf("owner_id = $%d::uuid", idx))
		} else {
			sets = append(sets, fmt.Sprintf("%s = $%d", k, idx))
		}
		args = append(args, v)
		idx++
	}
	if len(sets) == 1 {
		return r.Get(ctx, id)
	}
	args = append(args, id)

	query := fmt.Sprintf(`UPDATE plans SET %s WHERE id = $%d::uuid`,
		strings.Join(sets, ", "), idx)
	if _, err := r.db.Exec(ctx, query, args...); err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// Delete removes the plan, returning pgx.ErrNoRows if no such row existed.
func (r *PlansRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM plans WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// OwnerEmail returns the plan owner's email, or "" if the plan has no owner or
// the owner has no email on file. Used to notify the affected member on delete.
func (r *PlansRepo) OwnerEmail(ctx context.Context, planID string) (string, error) {
	var email string
	err := r.db.QueryRow(ctx, `
		SELECT m.email FROM plans p
		JOIN members m ON m.id = p.owner_id
		WHERE p.id = $1::uuid AND m.email IS NOT NULL AND m.email <> ''`, planID).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return email, err
}

// GetDecisions returns the plan's decision log.
func (r *PlansRepo) GetDecisions(ctx context.Context, planID string) ([]model.PlanDecision, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id::text, plan_id::text, summary, rationale, decided_by::text, decided_at
		FROM plan_decisions WHERE plan_id = $1::uuid ORDER BY decided_at`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var decisions []model.PlanDecision
	for rows.Next() {
		var d model.PlanDecision
		if err := rows.Scan(&d.ID, &d.PlanID, &d.Summary, &d.Rationale, &d.DecidedBy, &d.DecidedAt); err != nil {
			return nil, err
		}
		decisions = append(decisions, d)
	}
	return decisions, rows.Err()
}

// CreateDecision records a decision on the plan.
func (r *PlansRepo) CreateDecision(ctx context.Context, d *model.PlanDecision, decidedBy string) (*model.PlanDecision, error) {
	var created model.PlanDecision
	err := r.db.QueryRow(ctx, `
		INSERT INTO plan_decisions (plan_id, summary, rationale, decided_by)
		VALUES ($1::uuid, $2, $3, $4::uuid)
		RETURNING id::text, plan_id::text, summary, rationale, decided_by::text, decided_at`,
		d.PlanID, d.Summary, d.Rationale, decidedBy).
		Scan(&created.ID, &created.PlanID, &created.Summary, &created.Rationale, &created.DecidedBy, &created.DecidedAt)
	return &created, err
}

// UpdateDecision edits an existing decision, returning pgx.ErrNoRows if absent.
func (r *PlansRepo) UpdateDecision(ctx context.Context, id string, summary, rationale *string) (*model.PlanDecision, error) {
	var d model.PlanDecision
	err := r.db.QueryRow(ctx, `
		UPDATE plan_decisions SET
			summary   = coalesce($1, summary),
			rationale = coalesce($2, rationale)
		WHERE id = $3::uuid
		RETURNING id::text, plan_id::text, summary, rationale, decided_by::text, decided_at`,
		summary, rationale, id).
		Scan(&d.ID, &d.PlanID, &d.Summary, &d.Rationale, &d.DecidedBy, &d.DecidedAt)
	return &d, err
}

// DeleteDecision removes a decision, returning pgx.ErrNoRows if absent.
func (r *PlansRepo) DeleteDecision(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM plan_decisions WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func scanPlan(row scannable) (model.Plan, error) {
	var p model.Plan
	err := row.Scan(&p.ID, &p.Title, &p.Description, &p.Status, &p.OwnerID,
		&p.TargetDate, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.OwnerName,
		&p.EstimatedCostMinor, &p.CostCurrency)
	return p, err
}

// PlanCounts returns the active-plan total and how many of those are past
// their target date — the dashboard's stalled-initiative signal.
func (r *PlansRepo) PlanCounts(ctx context.Context) (active, overdue int, err error) {
	err = r.db.QueryRow(ctx, `
		SELECT count(*)::int,
		       count(*) FILTER (WHERE target_date IS NOT NULL AND target_date < CURRENT_DATE)::int
		FROM plans WHERE status = 'active'`).Scan(&active, &overdue)
	return
}
