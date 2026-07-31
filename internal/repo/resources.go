package repo

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// ResourcesRepo provides PostgreSQL data access for resources.
type ResourcesRepo struct {
	db *pgxpool.Pool
}

// NewResourcesRepo constructs a ResourcesRepo backed by the given connection pool.
func NewResourcesRepo(db *pgxpool.Pool) *ResourcesRepo {
	return &ResourcesRepo{db: db}
}

// ResourceFilter holds the optional query parameters for listing resources,
// plus the viewer scope: unless ViewerSeesAll (officer and above), a resource
// restricted to visibility groups is returned only when ViewerMemberID belongs
// to one of them; unrestricted resources are visible to everyone.
type ResourceFilter struct {
	Search   string
	Category string
	Tag      string
	Limit    int
	Offset   int

	ViewerSeesAll  bool
	ViewerMemberID string
}

// visibilityCond builds the group-visibility predicate for the viewer. It
// appends to args when the viewer has a linked member.
func visibilityCond(f ResourceFilter, args *[]any, idx *int) string {
	if f.ViewerSeesAll {
		return ""
	}
	unrestricted := "NOT EXISTS (SELECT 1 FROM resource_groups rg WHERE rg.resource_id = r.id)"
	if f.ViewerMemberID == "" {
		// No linked member: only unrestricted resources are visible.
		return unrestricted
	}
	cond := fmt.Sprintf(`(%s OR EXISTS (
		SELECT 1 FROM resource_groups rg
		JOIN group_members gm ON gm.group_id = rg.group_id
		WHERE rg.resource_id = r.id AND gm.member_id = $%d::uuid))`, unrestricted, *idx)
	*args = append(*args, f.ViewerMemberID)
	*idx++
	return cond
}

// List returns a page of resources matching the filter, plus the total count.
func (r *ResourcesRepo) List(ctx context.Context, f ResourceFilter) ([]model.Resource, int, error) {
	args := []any{}
	conds := []string{}
	idx := 1

	if f.Search != "" {
		conds = append(conds, fmt.Sprintf(
			"to_tsvector('english', r.title || ' ' || coalesce(r.description,'')) @@ plainto_tsquery('english', $%d)", idx))
		args = append(args, f.Search)
		idx++
	}
	if f.Category != "" {
		conds = append(conds, fmt.Sprintf("r.category = $%d", idx))
		args = append(args, f.Category)
		idx++
	}
	if f.Tag != "" {
		conds = append(conds, fmt.Sprintf("$%d = ANY(r.tags)", idx))
		args = append(args, f.Tag)
		idx++
	}

	if vc := visibilityCond(f, &args, &idx); vc != "" {
		conds = append(conds, vc)
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
		       id::text, title, description, url, category, tags,
		       coalesce((SELECT array_agg(g.name ORDER BY g.name)
		                 FROM resource_groups rg JOIN groups g ON g.id = rg.group_id
		                 WHERE rg.resource_id = r.id), '{}'),
		       added_by::text, created_at, updated_at
		FROM resources r
		%s
		ORDER BY r.title
		LIMIT $%d OFFSET $%d`, where, idx, idx+1)
	args = append(args, limit, f.Offset)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var resources []model.Resource
	var total int
	for rows.Next() {
		var res model.Resource
		if err := rows.Scan(&total, &res.ID, &res.Title, &res.Description, &res.URL,
			&res.Category, &res.Tags, &res.GroupNames, &res.AddedBy, &res.CreatedAt, &res.UpdatedAt); err != nil {
			return nil, 0, err
		}
		if res.Tags == nil {
			res.Tags = []string{}
		}
		resources = append(resources, res)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// COUNT(*) OVER() yields no rows on an empty page; fall back to a plain count.
	if len(resources) == 0 && f.Offset > 0 {
		countQuery := "SELECT count(*) FROM resources r " + where
		if err := r.db.QueryRow(ctx, countQuery, args[:len(args)-2]...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}
	return resources, total, nil
}

// Get returns the resource with the given id, or pgx.ErrNoRows if none exists.
// Visibility is NOT applied here — use GetVisible for viewer-facing reads;
// Get serves the officer-gated edit/delete paths.
func (r *ResourcesRepo) Get(ctx context.Context, id string) (*model.Resource, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id::text, title, description, url, category, tags,
		       added_by::text, created_at, updated_at
		FROM resources WHERE id = $1::uuid`, id)
	res, err := scanResource(row)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// GetVisible returns the resource only if the viewer may see it; a restricted
// resource outside the viewer's groups behaves exactly like a missing one
// (pgx.ErrNoRows), so its existence is not disclosed.
func (r *ResourcesRepo) GetVisible(ctx context.Context, id string, viewerSeesAll bool, viewerMemberID string) (*model.Resource, error) {
	args := []any{id}
	idx := 2
	f := ResourceFilter{ViewerSeesAll: viewerSeesAll, ViewerMemberID: viewerMemberID}
	vis := visibilityCond(f, &args, &idx)
	q := `
		SELECT id::text, title, description, url, category, tags,
		       added_by::text, created_at, updated_at
		FROM resources r WHERE id = $1::uuid`
	if vis != "" {
		q += " AND " + vis
	}
	res, err := scanResource(r.db.QueryRow(ctx, q, args...))
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Create inserts a new resource and returns the stored row.
func (r *ResourcesRepo) Create(ctx context.Context, res *model.Resource, addedBy string) (*model.Resource, error) {
	row := r.db.QueryRow(ctx, `
		INSERT INTO resources (title, description, url, category, tags, added_by)
		VALUES ($1, $2, $3, $4, $5, $6::uuid)
		RETURNING id::text, title, description, url, category, tags, added_by::text, created_at, updated_at`,
		res.Title, res.Description, res.URL, res.Category, res.Tags, addedBy)
	created, err := scanResource(row)
	if err != nil {
		return nil, err
	}
	return &created, nil
}

var resourceAllowedFields = map[string]bool{
	"title": true, "description": true, "url": true, "category": true, "tags": true,
}

// Update sets only the given fields, preserving PATCH semantics.
func (r *ResourcesRepo) Update(ctx context.Context, id string, fields map[string]any) (*model.Resource, error) {
	sets := []string{}
	args := []any{}
	idx := 1
	for k, v := range fields {
		if !resourceAllowedFields[k] {
			continue
		}
		sets = append(sets, fmt.Sprintf("%s = $%d", k, idx))
		args = append(args, v)
		idx++
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, id)

	query := fmt.Sprintf(`
		UPDATE resources SET %s WHERE id = $%d::uuid
		RETURNING id::text, title, description, url, category, tags, added_by::text, created_at, updated_at`,
		strings.Join(sets, ", "), idx)

	updated, err := scanResource(r.db.QueryRow(ctx, query, args...))
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// Delete removes the resource, returning pgx.ErrNoRows if no such row existed.
func (r *ResourcesRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM resources WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func scanResource(row scannable) (model.Resource, error) {
	var res model.Resource
	err := row.Scan(&res.ID, &res.Title, &res.Description, &res.URL, &res.Category,
		&res.Tags, &res.AddedBy, &res.CreatedAt, &res.UpdatedAt)
	if res.Tags == nil {
		res.Tags = []string{}
	}
	return res, err
}
