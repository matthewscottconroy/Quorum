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

// ErrFolderCycle is returned when a re-parenting would make a folder its own
// ancestor.
var ErrFolderCycle = errors.New("folder cannot be moved inside itself")

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
		SELECT f.id::text, f.name, f.parent_id::text,
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
		if err := rows.Scan(&f.ID, &f.Name, &f.ParentID, &f.ResourceCount, &f.CreatedAt); err != nil {
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
		SELECT f.id::text, f.name, f.parent_id::text,
		       (SELECT count(*) FROM resources res WHERE res.folder_id = f.id)::int,
		       f.created_at
		FROM folders f WHERE f.id = $1::uuid`, id).
		Scan(&f.ID, &f.Name, &f.ParentID, &f.ResourceCount, &f.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// Create adds a folder, optionally inside a parent.
func (r *FoldersRepo) Create(ctx context.Context, name string, parentID *string) (*model.Folder, error) {
	var id string
	if err := r.db.QueryRow(ctx,
		`INSERT INTO folders (name, parent_id) VALUES ($1, $2::uuid) RETURNING id::text`,
		name, parentID).Scan(&id); err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// isAncestor reports whether `candidate` is `folderID` itself or one of its
// descendants — i.e. whether parenting folderID under candidate would cycle.
func (r *FoldersRepo) isAncestor(ctx context.Context, folderID, candidate string) (bool, error) {
	var cycles bool
	err := r.db.QueryRow(ctx, `
		WITH RECURSIVE up AS (
			SELECT id, parent_id FROM folders WHERE id = $1::uuid
			UNION ALL
			SELECT f.id, f.parent_id FROM folders f JOIN up ON f.id = up.parent_id
		)
		SELECT EXISTS (SELECT 1 FROM up WHERE id = $2::uuid)`, candidate, folderID).Scan(&cycles)
	return cycles, err
}

// Rename changes a folder's name and/or moves it (parent semantics:
// moveParent=false leaves the parent alone; parentID=nil moves to the root).
// Moving into the folder's own subtree returns ErrFolderCycle.
func (r *FoldersRepo) Rename(ctx context.Context, id string, name *string, parentID *string, moveParent bool) (*model.Folder, error) {
	if moveParent && parentID != nil {
		if *parentID == id {
			return nil, ErrFolderCycle
		}
		cycle, err := r.isAncestor(ctx, id, *parentID)
		if err != nil {
			return nil, err
		}
		if cycle {
			return nil, ErrFolderCycle
		}
	}
	sets, args, idx := []string{}, []any{}, 1
	if name != nil {
		sets = append(sets, "name = $1")
		args = append(args, *name)
		idx++
	}
	if moveParent {
		sets = append(sets, fmt.Sprintf("parent_id = $%d::uuid", idx))
		args = append(args, parentID)
		idx++
	}
	if len(sets) == 0 {
		return r.Get(ctx, id)
	}
	args = append(args, id)
	tag, err := r.db.Exec(ctx,
		fmt.Sprintf("UPDATE folders SET %s WHERE id = $%d::uuid", strings.Join(sets, ", "), idx), args...)
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
