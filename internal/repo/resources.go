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
	// FolderID filters by folder; "none" selects resources outside any folder.
	FolderID string
	Limit    int
	Offset   int

	ViewerSeesAll  bool
	ViewerMemberID string
	// ViewerRoleRank gates visible_min_role: member=2, officer=3, admin=4,
	// superadmin=5 (matches the handler's role ladder). Applies to every
	// viewer — a min_role of admin hides the resource from officers too.
	ViewerRoleRank int
}

// minRoleCond hides rows whose visible_min_role exceeds the viewer's rank.
func minRoleCond(rank int, args *[]any, idx *int) string {
	cond := fmt.Sprintf(`(r.visible_min_role IS NULL OR
		CASE r.visible_min_role WHEN 'member' THEN 2 WHEN 'officer' THEN 3 WHEN 'admin' THEN 4 END <= $%d)`, *idx)
	*args = append(*args, rank)
	*idx++
	return cond
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
	if f.FolderID == "none" {
		conds = append(conds, "r.folder_id IS NULL")
	} else if f.FolderID != "" {
		conds = append(conds, fmt.Sprintf("r.folder_id = $%d::uuid", idx))
		args = append(args, f.FolderID)
		idx++
	}

	if vc := visibilityCond(f, &args, &idx); vc != "" {
		conds = append(conds, vc)
	}
	conds = append(conds, minRoleCond(f.ViewerRoleRank, &args, &idx))

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
		       r.id::text, r.title, r.description, r.url, r.category, r.tags,
		       coalesce((SELECT array_agg(g.name ORDER BY g.name)
		                 FROM resource_groups rg JOIN groups g ON g.id = rg.group_id
		                 WHERE rg.resource_id = r.id), '{}'),
		       r.folder_id::text, fo.name, r.file_name, r.file_size, r.file_sha256, r.file_preview_only, r.visible_min_role,
		       r.added_by::text, r.created_at, r.updated_at
		FROM resources r
		LEFT JOIN folders fo ON fo.id = r.folder_id
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
			&res.Category, &res.Tags, &res.GroupNames,
			&res.FolderID, &res.FolderName, &res.FileName, &res.FileSize, &res.FileSHA256, &res.FilePreviewOnly, &res.VisibleMinRole,
			&res.AddedBy, &res.CreatedAt, &res.UpdatedAt); err != nil {
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

const resourceCols = `id::text, title, description, url, category, tags,
	       folder_id::text, file_name, file_size, file_sha256, file_preview_only, visible_min_role,
	       added_by::text, created_at, updated_at`

// Get returns the resource with the given id, or pgx.ErrNoRows if none exists.
// Visibility is NOT applied here — use GetVisible for viewer-facing reads;
// Get serves the officer-gated edit/delete paths.
func (r *ResourcesRepo) Get(ctx context.Context, id string) (*model.Resource, error) {
	row := r.db.QueryRow(ctx, `SELECT `+resourceCols+` FROM resources WHERE id = $1::uuid`, id)
	res, err := scanResource(row)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// GetVisible returns the resource only if the viewer may see it; a restricted
// resource outside the viewer's groups behaves exactly like a missing one
// (pgx.ErrNoRows), so its existence is not disclosed.
func (r *ResourcesRepo) GetVisible(ctx context.Context, id string, viewerSeesAll bool, viewerMemberID string, viewerRoleRank int) (*model.Resource, error) {
	args := []any{id}
	idx := 2
	f := ResourceFilter{ViewerSeesAll: viewerSeesAll, ViewerMemberID: viewerMemberID}
	vis := visibilityCond(f, &args, &idx)
	q := `SELECT ` + resourceCols + ` FROM resources r WHERE id = $1::uuid`
	if vis != "" {
		q += " AND " + vis
	}
	q += " AND " + minRoleCond(viewerRoleRank, &args, &idx)
	res, err := scanResource(r.db.QueryRow(ctx, q, args...))
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Create inserts a new resource and returns the stored row.
func (r *ResourcesRepo) Create(ctx context.Context, res *model.Resource, addedBy string) (*model.Resource, error) {
	row := r.db.QueryRow(ctx, `
		INSERT INTO resources (title, description, url, category, tags, folder_id, visible_min_role, added_by)
		VALUES ($1, $2, $3, $4, $5, $6::uuid, $7, $8::uuid)
		RETURNING `+resourceCols,
		res.Title, res.Description, res.URL, res.Category, res.Tags, res.FolderID, res.VisibleMinRole, addedBy)
	created, err := scanResource(row)
	if err != nil {
		return nil, err
	}
	return &created, nil
}

var resourceAllowedFields = map[string]bool{
	"title": true, "description": true, "url": true, "category": true, "tags": true,
	"folder_id": true, "file_preview_only": true, "visible_min_role": true,
}

var resourceUUIDFields = map[string]bool{"folder_id": true}

// Update sets only the given fields, preserving PATCH semantics.
func (r *ResourcesRepo) Update(ctx context.Context, id string, fields map[string]any) (*model.Resource, error) {
	sets := []string{}
	args := []any{}
	idx := 1
	for k, v := range fields {
		if !resourceAllowedFields[k] {
			continue
		}
		if resourceUUIDFields[k] {
			sets = append(sets, fmt.Sprintf("%s = $%d::uuid", k, idx))
		} else {
			sets = append(sets, fmt.Sprintf("%s = $%d", k, idx))
		}
		args = append(args, v)
		idx++
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, id)

	query := fmt.Sprintf(`
		UPDATE resources SET %s WHERE id = $%d::uuid
		RETURNING `+resourceCols, strings.Join(sets, ", "), idx)

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
	err := row.Scan(&res.ID, &res.Title, &res.Description, &res.URL, &res.Category, &res.Tags,
		&res.FolderID, &res.FileName, &res.FileSize, &res.FileSHA256, &res.FilePreviewOnly, &res.VisibleMinRole,
		&res.AddedBy, &res.CreatedAt, &res.UpdatedAt)
	if res.Tags == nil {
		res.Tags = []string{}
	}
	return res, err
}

// SetFile attaches (or replaces) a resource's uploaded document: metadata on
// the resources row, bytes in resource_files, atomically.
func (r *ResourcesRepo) SetFile(ctx context.Context, id, fileName string, size int64, sha256Hex, contentType string, data []byte) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tag, err := tx.Exec(ctx, `
		UPDATE resources SET file_name = $1, file_size = $2, file_sha256 = $3, updated_at = now()
		WHERE id = $4::uuid`, fileName, size, sha256Hex, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO resource_files (resource_id, content_type, data)
		VALUES ($1::uuid, $2, $3)
		ON CONFLICT (resource_id) DO UPDATE SET content_type = $2, data = $3`,
		id, contentType, data); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetFile returns the stored bytes and content type for a resource's
// document. Callers MUST have already authorized the read via GetVisible.
func (r *ResourcesRepo) GetFile(ctx context.Context, id string) (contentType string, data []byte, err error) {
	err = r.db.QueryRow(ctx,
		`SELECT content_type, data FROM resource_files WHERE resource_id = $1::uuid`, id).
		Scan(&contentType, &data)
	return contentType, data, err
}
