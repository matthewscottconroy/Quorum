package repo

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

type ContactsRepo struct {
	db *pgxpool.Pool
}

func NewContactsRepo(db *pgxpool.Pool) *ContactsRepo {
	return &ContactsRepo{db: db}
}

type ContactFilter struct {
	Search   string
	Category string
	Tag      string
	Limit    int
	Offset   int
}

func (r *ContactsRepo) List(ctx context.Context, f ContactFilter) ([]model.Contact, int, error) {
	args := []any{}
	conds := []string{}
	idx := 1

	if f.Search != "" {
		conds = append(conds, fmt.Sprintf(
			"to_tsvector('english', c.name || ' ' || coalesce(c.organization,'')) @@ plainto_tsquery('english', $%d)", idx))
		args = append(args, f.Search)
		idx++
	}
	if f.Category != "" {
		conds = append(conds, fmt.Sprintf("c.category = $%d", idx))
		args = append(args, f.Category)
		idx++
	}
	if f.Tag != "" {
		conds = append(conds, fmt.Sprintf("$%d = ANY(c.tags)", idx))
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
		       id::text, name, organization, email, phone, address,
		       category, tags, notes, created_by::text, created_at, updated_at
		FROM contacts c
		%s
		ORDER BY c.name
		LIMIT $%d OFFSET $%d`, where, idx, idx+1)
	args = append(args, limit, f.Offset)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var contacts []model.Contact
	var total int
	for rows.Next() {
		var c model.Contact
		if err := rows.Scan(&total, &c.ID, &c.Name, &c.Organization, &c.Email, &c.Phone,
			&c.Address, &c.Category, &c.Tags, &c.Notes, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, 0, err
		}
		if c.Tags == nil {
			c.Tags = []string{}
		}
		contacts = append(contacts, c)
	}
	return contacts, total, rows.Err()
}

func (r *ContactsRepo) Get(ctx context.Context, id string) (*model.Contact, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id::text, name, organization, email, phone, address,
		       category, tags, notes, created_by::text, created_at, updated_at
		FROM contacts WHERE id = $1::uuid`, id)
	c, err := scanContact(row)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *ContactsRepo) Create(ctx context.Context, c *model.Contact, createdBy string) (*model.Contact, error) {
	row := r.db.QueryRow(ctx, `
		INSERT INTO contacts (name, organization, email, phone, address, category, tags, notes, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::uuid)
		RETURNING id::text, name, organization, email, phone, address,
		          category, tags, notes, created_by::text, created_at, updated_at`,
		c.Name, c.Organization, c.Email, c.Phone, c.Address, c.Category, c.Tags, c.Notes, createdBy)
	created, err := scanContact(row)
	if err != nil {
		return nil, err
	}
	return &created, nil
}

func (r *ContactsRepo) Update(ctx context.Context, id string, c *model.Contact) (*model.Contact, error) {
	row := r.db.QueryRow(ctx, `
		UPDATE contacts SET
			name         = $1,
			organization = $2,
			email        = $3,
			phone        = $4,
			address      = $5,
			category     = $6,
			tags         = $7,
			notes        = $8,
			updated_at   = now()
		WHERE id = $9::uuid
		RETURNING id::text, name, organization, email, phone, address,
		          category, tags, notes, created_by::text, created_at, updated_at`,
		c.Name, c.Organization, c.Email, c.Phone, c.Address, c.Category, c.Tags, c.Notes, id)
	updated, err := scanContact(row)
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func (r *ContactsRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM contacts WHERE id = $1::uuid`, id)
	return err
}

func scanContact(row scannable) (model.Contact, error) {
	var c model.Contact
	err := row.Scan(&c.ID, &c.Name, &c.Organization, &c.Email, &c.Phone, &c.Address,
		&c.Category, &c.Tags, &c.Notes, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	if c.Tags == nil {
		c.Tags = []string{}
	}
	return c, err
}
