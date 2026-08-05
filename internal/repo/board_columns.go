package repo

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// BoardColumnsRepo provides PostgreSQL data access for kanban board columns.
type BoardColumnsRepo struct {
	db *pgxpool.Pool
}

// NewBoardColumnsRepo constructs a BoardColumnsRepo backed by the given pool.
func NewBoardColumnsRepo(db *pgxpool.Pool) *BoardColumnsRepo {
	return &BoardColumnsRepo{db: db}
}

const boardColumnCols = `id::text, name, position, maps_to_status, created_at`

// List returns all columns in board order.
func (r *BoardColumnsRepo) List(ctx context.Context) ([]model.BoardColumn, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+boardColumnCols+` FROM board_columns ORDER BY position, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []model.BoardColumn
	for rows.Next() {
		var c model.BoardColumn
		if err := rows.Scan(&c.ID, &c.Name, &c.Position, &c.MapsToStatus, &c.CreatedAt); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// Get returns one column, or pgx.ErrNoRows.
func (r *BoardColumnsRepo) Get(ctx context.Context, id string) (*model.BoardColumn, error) {
	var c model.BoardColumn
	err := r.db.QueryRow(ctx,
		`SELECT `+boardColumnCols+` FROM board_columns WHERE id = $1::uuid`, id).
		Scan(&c.ID, &c.Name, &c.Position, &c.MapsToStatus, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// Create adds a column. Position 0 means "append to the right end".
func (r *BoardColumnsRepo) Create(ctx context.Context, name string, position int, mapsToStatus *string) (*model.BoardColumn, error) {
	var c model.BoardColumn
	err := r.db.QueryRow(ctx, `
		INSERT INTO board_columns (name, position, maps_to_status)
		VALUES ($1,
		        CASE WHEN $2 = 0
		             THEN (SELECT coalesce(max(position), 0) + 10 FROM board_columns)
		             ELSE $2 END,
		        $3)
		RETURNING `+boardColumnCols, name, position, mapsToStatus).
		Scan(&c.ID, &c.Name, &c.Position, &c.MapsToStatus, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

var boardColumnAllowedFields = map[string]bool{"name": true, "position": true}

// Update sets only the given fields (name, position).
func (r *BoardColumnsRepo) Update(ctx context.Context, id string, fields map[string]any) (*model.BoardColumn, error) {
	sets := []string{}
	args := []any{}
	idx := 1
	for k, v := range fields {
		if !boardColumnAllowedFields[k] {
			continue
		}
		sets = append(sets, fmt.Sprintf("%s = $%d", k, idx))
		args = append(args, v)
		idx++
	}
	if len(sets) == 0 {
		return r.Get(ctx, id)
	}
	args = append(args, id)
	var c model.BoardColumn
	err := r.db.QueryRow(ctx, fmt.Sprintf(
		`UPDATE board_columns SET %s WHERE id = $%d::uuid RETURNING `+boardColumnCols,
		strings.Join(sets, ", "), idx), args...).
		Scan(&c.ID, &c.Name, &c.Position, &c.MapsToStatus, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// Delete removes the column; cards in it fall back to the column matching
// their status (column_id is nulled by the FK).
func (r *BoardColumnsRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM board_columns WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
