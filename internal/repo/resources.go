package repo

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

type ResourcesRepo struct {
	db *pgxpool.Pool
}

func NewResourcesRepo(db *pgxpool.Pool) *ResourcesRepo {
	return &ResourcesRepo{db: db}
}

type ResourceFilter struct {
	Search   string
	Category string
	Tag      string
	Limit    int
	Offset   int
}

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
			&res.Category, &res.Tags, &res.AddedBy, &res.CreatedAt, &res.UpdatedAt); err != nil {
			return nil, 0, err
		}
		if res.Tags == nil {
			res.Tags = []string{}
		}
		resources = append(resources, res)
	}
	return resources, total, rows.Err()
}

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

func (r *ResourcesRepo) Update(ctx context.Context, id string, res *model.Resource) (*model.Resource, error) {
	row := r.db.QueryRow(ctx, `
		UPDATE resources SET
			title       = $1,
			description = $2,
			url         = $3,
			category    = $4,
			tags        = $5,
			updated_at  = now()
		WHERE id = $6::uuid
		RETURNING id::text, title, description, url, category, tags, added_by::text, created_at, updated_at`,
		res.Title, res.Description, res.URL, res.Category, res.Tags, id)
	updated, err := scanResource(row)
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func (r *ResourcesRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM resources WHERE id = $1::uuid`, id)
	return err
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
