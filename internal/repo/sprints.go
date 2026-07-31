package repo

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// SprintsRepo provides PostgreSQL data access for sprints.
type SprintsRepo struct {
	db *pgxpool.Pool
}

// NewSprintsRepo constructs a SprintsRepo.
func NewSprintsRepo(db *pgxpool.Pool) *SprintsRepo {
	return &SprintsRepo{db: db}
}

const sprintCols = `id::text, name, goal, to_char(starts_on, 'YYYY-MM-DD'),
	to_char(ends_on, 'YYYY-MM-DD'), status, created_by::text, created_at, updated_at`

func scanSprint(row scannable) (model.Sprint, error) {
	var s model.Sprint
	err := row.Scan(&s.ID, &s.Name, &s.Goal, &s.StartsOn, &s.EndsOn,
		&s.Status, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

// List returns all sprints: active first, then planned, then completed, newest
// start date first within each group (boards want the current iteration on top).
func (r *SprintsRepo) List(ctx context.Context) ([]model.Sprint, error) {
	rows, err := r.db.Query(ctx, `
		SELECT `+sprintCols+` FROM sprints
		ORDER BY CASE status WHEN 'active' THEN 0 WHEN 'planned' THEN 1 ELSE 2 END,
		         starts_on DESC
		LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.Sprint, 0)
	for rows.Next() {
		s, err := scanSprint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Get returns one sprint, or pgx.ErrNoRows.
func (r *SprintsRepo) Get(ctx context.Context, id string) (*model.Sprint, error) {
	s, err := scanSprint(r.db.QueryRow(ctx,
		`SELECT `+sprintCols+` FROM sprints WHERE id = $1::uuid`, id))
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Create inserts a sprint and returns the stored row.
func (r *SprintsRepo) Create(ctx context.Context, s *model.Sprint, createdBy string) (*model.Sprint, error) {
	created, err := scanSprint(r.db.QueryRow(ctx, `
		INSERT INTO sprints (name, goal, starts_on, ends_on, status, created_by)
		VALUES ($1, $2, $3::date, $4::date, $5, $6::uuid)
		RETURNING `+sprintCols,
		s.Name, s.Goal, s.StartsOn, s.EndsOn, s.Status, createdBy))
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// Update patches the provided fields (nil = unchanged) and returns the row.
func (r *SprintsRepo) Update(ctx context.Context, id string, name, goal, startsOn, endsOn, status *string) (*model.Sprint, error) {
	updated, err := scanSprint(r.db.QueryRow(ctx, `
		UPDATE sprints SET
			name       = coalesce($1, name),
			goal       = coalesce($2, goal),
			starts_on  = coalesce($3::date, starts_on),
			ends_on    = coalesce($4::date, ends_on),
			status     = coalesce($5, status),
			updated_at = now()
		WHERE id = $6::uuid
		RETURNING `+sprintCols,
		name, goal, startsOn, endsOn, status, id))
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// Delete removes a sprint; its action items fall back to the backlog
// (sprint_id ON DELETE SET NULL). Returns pgx.ErrNoRows if absent.
func (r *SprintsRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM sprints WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
