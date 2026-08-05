package repo

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// FoldersRepo provides PostgreSQL data access for resource-library folders.
type FoldersRepo struct {
	db *pgxpool.Pool
}

// NewFoldersRepo constructs a FoldersRepo backed by the given pool.
func NewFoldersRepo(db *pgxpool.Pool) *FoldersRepo {
	return &FoldersRepo{db: db}
}

// List returns all folders alphabetically, with the number of resources each
// holds (regardless of the caller's visibility — counts are not sensitive,
// the resources themselves stay group-filtered).
func (r *FoldersRepo) List(ctx context.Context) ([]model.Folder, error) {
	rows, err := r.db.Query(ctx, `
		SELECT f.id::text, f.name,
		       (SELECT count(*) FROM resources res WHERE res.folder_id = f.id)::int,
		       f.created_at
		FROM folders f ORDER BY lower(f.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Folder
	for rows.Next() {
		var f model.Folder
		if err := rows.Scan(&f.ID, &f.Name, &f.ResourceCount, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Get returns one folder, or pgx.ErrNoRows.
func (r *FoldersRepo) Get(ctx context.Context, id string) (*model.Folder, error) {
	var f model.Folder
	err := r.db.QueryRow(ctx, `
		SELECT f.id::text, f.name,
		       (SELECT count(*) FROM resources res WHERE res.folder_id = f.id)::int,
		       f.created_at
		FROM folders f WHERE f.id = $1::uuid`, id).
		Scan(&f.ID, &f.Name, &f.ResourceCount, &f.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// Create adds a folder.
func (r *FoldersRepo) Create(ctx context.Context, name string) (*model.Folder, error) {
	var id string
	if err := r.db.QueryRow(ctx,
		`INSERT INTO folders (name) VALUES ($1) RETURNING id::text`, name).Scan(&id); err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// Rename changes a folder's name.
func (r *FoldersRepo) Rename(ctx context.Context, id, name string) (*model.Folder, error) {
	tag, err := r.db.Exec(ctx, `UPDATE folders SET name = $1 WHERE id = $2::uuid`, name, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, pgx.ErrNoRows
	}
	return r.Get(ctx, id)
}

// Delete removes the folder; its resources return to the root (folder_id is
// nulled by the FK). Visibility of the resources is unchanged.
func (r *FoldersRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM folders WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
