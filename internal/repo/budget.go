// Package repo — budget.go: scenario-based budget planning.
package repo

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// BudgetRepo provides PostgreSQL data access for budget scenarios and lines.
type BudgetRepo struct {
	db *pgxpool.Pool
}

// NewBudgetRepo constructs a BudgetRepo backed by the given pool.
func NewBudgetRepo(db *pgxpool.Pool) *BudgetRepo {
	return &BudgetRepo{db: db}
}

// scenarioSelectWithTotals lists scenarios with income/expense rolled up in one
// query (LEFT JOIN so scenarios with no lines still appear with zero totals).
const scenarioSelectWithTotals = `
	SELECT s.id::text, s.name, s.description, s.period_label, s.status, s.currency,
	       s.created_by::text, s.created_at, s.updated_at,
	       coalesce(sum(l.quantity * l.unit_amount_minor) FILTER (WHERE l.kind = 'income'), 0)  AS income,
	       coalesce(sum(l.quantity * l.unit_amount_minor) FILTER (WHERE l.kind = 'expense'), 0) AS expense
	FROM budget_scenarios s
	LEFT JOIN budget_lines l ON l.scenario_id = s.id`

func scanScenarioWithTotals(row scannable) (model.BudgetScenario, error) {
	var s model.BudgetScenario
	var income, expense int64
	err := row.Scan(&s.ID, &s.Name, &s.Description, &s.PeriodLabel, &s.Status, &s.Currency,
		&s.CreatedBy, &s.CreatedAt, &s.UpdatedAt, &income, &expense)
	if err != nil {
		return s, err
	}
	s.Totals = model.BudgetTotals{IncomeMinor: income, ExpenseMinor: expense, NetMinor: income - expense, Currency: s.Currency}
	return s, nil
}

// ListScenarios returns all scenarios with rolled-up totals, newest first.
func (r *BudgetRepo) ListScenarios(ctx context.Context) ([]model.BudgetScenario, error) {
	rows, err := r.db.Query(ctx, scenarioSelectWithTotals+`
		GROUP BY s.id ORDER BY s.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.BudgetScenario
	for rows.Next() {
		s, err := scanScenarioWithTotals(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CompareScenarios returns the given scenarios (by id) with their totals, for
// side-by-side comparison. Unknown ids are simply omitted.
func (r *BudgetRepo) CompareScenarios(ctx context.Context, ids []string) ([]model.BudgetScenario, error) {
	rows, err := r.db.Query(ctx, scenarioSelectWithTotals+`
		WHERE s.id = ANY($1::uuid[])
		GROUP BY s.id ORDER BY s.created_at`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.BudgetScenario
	for rows.Next() {
		s, err := scanScenarioWithTotals(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetScenario returns one scenario with its lines and computed totals.
func (r *BudgetRepo) GetScenario(ctx context.Context, id string) (*model.BudgetScenario, error) {
	var s model.BudgetScenario
	err := r.db.QueryRow(ctx, `
		SELECT id::text, name, description, period_label, status, currency,
		       created_by::text, created_at, updated_at
		FROM budget_scenarios WHERE id = $1::uuid`, id).
		Scan(&s.ID, &s.Name, &s.Description, &s.PeriodLabel, &s.Status, &s.Currency,
			&s.CreatedBy, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	lines, err := r.listLines(ctx, id)
	if err != nil {
		return nil, err
	}
	s.Lines = lines
	var income, expense int64
	for _, l := range lines {
		if l.Kind == "income" {
			income += l.AmountMinor
		} else {
			expense += l.AmountMinor
		}
	}
	s.Totals = model.BudgetTotals{IncomeMinor: income, ExpenseMinor: expense, NetMinor: income - expense, Currency: s.Currency}
	return &s, nil
}

func (r *BudgetRepo) listLines(ctx context.Context, scenarioID string) ([]model.BudgetLine, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id::text, scenario_id::text, kind, category, label, quantity, unit_amount_minor, note, sort_order, created_at
		FROM budget_lines WHERE scenario_id = $1::uuid
		ORDER BY kind DESC, sort_order, created_at`, scenarioID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.BudgetLine
	for rows.Next() {
		var l model.BudgetLine
		if err := rows.Scan(&l.ID, &l.ScenarioID, &l.Kind, &l.Category, &l.Label,
			&l.Quantity, &l.UnitAmountMinor, &l.Note, &l.SortOrder, &l.CreatedAt); err != nil {
			return nil, err
		}
		l.AmountMinor = l.Quantity * l.UnitAmountMinor
		out = append(out, l)
	}
	return out, rows.Err()
}

// CreateScenario inserts a new scenario.
func (r *BudgetRepo) CreateScenario(ctx context.Context, s *model.BudgetScenario, createdBy string) (*model.BudgetScenario, error) {
	var id string
	err := r.db.QueryRow(ctx, `
		INSERT INTO budget_scenarios (name, description, period_label, status, currency, created_by)
		VALUES ($1, $2, $3, $4, $5, $6::uuid) RETURNING id::text`,
		s.Name, s.Description, s.PeriodLabel, s.Status, s.Currency, createdBy).Scan(&id)
	if err != nil {
		return nil, err
	}
	return r.GetScenario(ctx, id)
}

// UpdateScenario edits scenario metadata.
func (r *BudgetRepo) UpdateScenario(ctx context.Context, id string, name, description, periodLabel, status, currency *string) (*model.BudgetScenario, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE budget_scenarios SET
			name         = coalesce($1, name),
			description  = coalesce($2, description),
			period_label = coalesce($3, period_label),
			status       = coalesce($4, status),
			currency     = coalesce($5, currency),
			updated_at   = now()
		WHERE id = $6::uuid`, name, description, periodLabel, status, currency, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, pgx.ErrNoRows
	}
	return r.GetScenario(ctx, id)
}

// DeleteScenario removes a scenario and its lines (cascade).
func (r *BudgetRepo) DeleteScenario(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM budget_scenarios WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// CloneScenario duplicates a scenario and all its lines under a new name (as a
// draft), returning the new scenario. The copy is a single transaction.
func (r *BudgetRepo) CloneScenario(ctx context.Context, id, newName, createdBy string) (*model.BudgetScenario, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var newID string
	err = tx.QueryRow(ctx, `
		INSERT INTO budget_scenarios (name, description, period_label, status, currency, created_by)
		SELECT $2, description, period_label, 'draft', currency, $3::uuid
		FROM budget_scenarios WHERE id = $1::uuid
		RETURNING id::text`, id, newName, createdBy).Scan(&newID)
	if err != nil {
		return nil, err // pgx.ErrNoRows if the source is missing
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO budget_lines (scenario_id, kind, category, label, quantity, unit_amount_minor, note, sort_order)
		SELECT $1::uuid, kind, category, label, quantity, unit_amount_minor, note, sort_order
		FROM budget_lines WHERE scenario_id = $2::uuid`, newID, id); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.GetScenario(ctx, newID)
}

// SeedDuesIncome adds one income line per member tier, projecting annualized dues
// from the active members in that tier and the tier's active dues schedule
// (quantity = member count, unit = annualized dues). Returns the number of lines
// added. Requires dues schedules to exist for the relevant tiers.
func (r *BudgetRepo) SeedDuesIncome(ctx context.Context, scenarioID string) (int, error) {
	tag, err := r.db.Exec(ctx, `
		INSERT INTO budget_lines (scenario_id, kind, category, label, quantity, unit_amount_minor, sort_order)
		SELECT $1::uuid, 'income', 'Dues', 'Dues — ' || m.tier, count(*),
		       s.amount_minor * CASE s.cadence WHEN 'monthly' THEN 12 WHEN 'quarterly' THEN 4 ELSE 1 END,
		       0
		FROM members m
		JOIN dues_schedules s ON s.tier = m.tier AND s.active
		WHERE m.status = 'active'
		GROUP BY m.tier, s.amount_minor, s.cadence`, scenarioID)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ---- Lines ----

// LineScenario returns the scenario id owning a line, for gating.
func (r *BudgetRepo) LineScenario(ctx context.Context, lineID string) (string, error) {
	var sid string
	err := r.db.QueryRow(ctx, `SELECT scenario_id::text FROM budget_lines WHERE id = $1::uuid`, lineID).Scan(&sid)
	return sid, err
}

// AddLine inserts a budget line and returns it (with computed amount).
func (r *BudgetRepo) AddLine(ctx context.Context, l *model.BudgetLine) (*model.BudgetLine, error) {
	var id string
	err := r.db.QueryRow(ctx, `
		INSERT INTO budget_lines (scenario_id, kind, category, label, quantity, unit_amount_minor, note, sort_order)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`,
		l.ScenarioID, l.Kind, l.Category, l.Label, l.Quantity, l.UnitAmountMinor, l.Note, l.SortOrder).Scan(&id)
	if err != nil {
		return nil, err
	}
	return r.getLine(ctx, id)
}

// UpdateLine edits a budget line and returns the updated row.
func (r *BudgetRepo) UpdateLine(ctx context.Context, id string, kind, category, label *string, quantity, unitAmountMinor *int64, note *string, sortOrder *int) (*model.BudgetLine, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE budget_lines SET
			kind              = coalesce($1, kind),
			category          = coalesce($2, category),
			label             = coalesce($3, label),
			quantity          = coalesce($4, quantity),
			unit_amount_minor = coalesce($5, unit_amount_minor),
			note              = coalesce($6, note),
			sort_order        = coalesce($7, sort_order)
		WHERE id = $8::uuid`, kind, category, label, quantity, unitAmountMinor, note, sortOrder, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, pgx.ErrNoRows
	}
	return r.getLine(ctx, id)
}

// DeleteLine removes a budget line.
func (r *BudgetRepo) DeleteLine(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM budget_lines WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *BudgetRepo) getLine(ctx context.Context, id string) (*model.BudgetLine, error) {
	var l model.BudgetLine
	err := r.db.QueryRow(ctx, `
		SELECT id::text, scenario_id::text, kind, category, label, quantity, unit_amount_minor, note, sort_order, created_at
		FROM budget_lines WHERE id = $1::uuid`, id).
		Scan(&l.ID, &l.ScenarioID, &l.Kind, &l.Category, &l.Label, &l.Quantity, &l.UnitAmountMinor, &l.Note, &l.SortOrder, &l.CreatedAt)
	if err != nil {
		return nil, err
	}
	l.AmountMinor = l.Quantity * l.UnitAmountMinor
	return &l, nil
}
