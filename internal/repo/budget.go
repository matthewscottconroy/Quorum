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
	fx fxConverterSource
}

// NewBudgetRepo constructs a BudgetRepo backed by the given pool. fx supplies the
// currency converter used to fold dues schedules in other currencies into a
// scenario's currency when seeding dues income.
func NewBudgetRepo(db *pgxpool.Pool, fx fxConverterSource) *BudgetRepo {
	return &BudgetRepo{db: db, fx: fx}
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
		GROUP BY s.id ORDER BY s.created_at DESC LIMIT 500`)
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

// SeedDuesIncome (re)projects annualized dues income into the scenario: one line
// per member tier, quantity = active-member count, unit = the tier's annualized
// per-member dues in the scenario currency. It is idempotent — prior "Dues"
// lines are cleared first, so re-seeding replaces rather than doubles. Schedules
// denominated in another currency are converted into the scenario currency via
// the current FX rates; a schedule whose currency has no rate is skipped (rather
// than silently summed at par). Returns the number of lines seeded, or
// pgx.ErrNoRows if the scenario does not exist.
func (r *BudgetRepo) SeedDuesIncome(ctx context.Context, scenarioID string) (int, error) {
	var currency string
	if err := r.db.QueryRow(ctx, `SELECT currency FROM budget_scenarios WHERE id = $1::uuid`, scenarioID).Scan(&currency); err != nil {
		return 0, err // pgx.ErrNoRows when the scenario is missing
	}
	conv, err := r.fx.ConverterFor(ctx, currency)
	if err != nil {
		return 0, err
	}

	// Gather each tier's active-member count and its active schedule (amount,
	// currency, cadence). Convert the annualized per-member amount into the
	// scenario currency in Go so cross-currency schedules fold in exactly.
	rows, err := r.db.Query(ctx, `
		SELECT m.tier, count(*), s.amount_minor, s.currency, s.cadence
		FROM members m
		JOIN dues_schedules s ON s.tier = m.tier AND s.active
		WHERE m.status = 'active'
		GROUP BY m.tier, s.amount_minor, s.currency, s.cadence
		ORDER BY m.tier`)
	if err != nil {
		return 0, err
	}
	type seedLine struct {
		tier      string
		count     int64
		unitMinor int64
	}
	var lines []seedLine
	for rows.Next() {
		var tier, cur, cadence string
		var count, amountMinor int64
		if err := rows.Scan(&tier, &count, &amountMinor, &cur, &cadence); err != nil {
			rows.Close()
			return 0, err
		}
		annualized := amountMinor * cadenceMultiplier(cadence)
		unit, ok := conv.Convert(annualized, cur)
		if !ok {
			continue // no rate into the scenario currency — skip rather than mis-sum
		}
		lines = append(lines, seedLine{tier: tier, count: count, unitMinor: unit})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `DELETE FROM budget_lines WHERE scenario_id = $1::uuid AND category = 'Dues'`, scenarioID); err != nil {
		return 0, err
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO budget_lines (scenario_id, kind, category, label, quantity, unit_amount_minor, sort_order)
			VALUES ($1::uuid, 'income', 'Dues', $2, $3, $4, 0)`,
			scenarioID, "Dues — "+l.tier, l.count, l.unitMinor); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(lines), nil
}

// cadenceMultiplier annualizes a per-period dues amount.
func cadenceMultiplier(cadence string) int64 {
	switch cadence {
	case "monthly":
		return 12
	case "quarterly":
		return 4
	default:
		return 1
	}
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
