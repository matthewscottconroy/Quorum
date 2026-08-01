package repo

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// GroupsRepo manages visibility groups and their membership.
type GroupsRepo struct {
	db *pgxpool.Pool
}

// NewGroupsRepo constructs a GroupsRepo.
func NewGroupsRepo(db *pgxpool.Pool) *GroupsRepo {
	return &GroupsRepo{db: db}
}

// List returns all groups with member counts, alphabetical.
func (r *GroupsRepo) List(ctx context.Context) ([]model.Group, error) {
	rows, err := r.db.Query(ctx, `
		SELECT g.id::text, g.name, g.description, g.created_at,
		       (SELECT count(*) FROM group_members gm WHERE gm.group_id = g.id)
		FROM groups g ORDER BY g.name LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.Group, 0)
	for rows.Next() {
		var g model.Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.MemberCount); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Get returns one group including its member ids.
func (r *GroupsRepo) Get(ctx context.Context, id string) (*model.Group, error) {
	var g model.Group
	err := r.db.QueryRow(ctx, `
		SELECT g.id::text, g.name, g.description, g.created_at,
		       (SELECT count(*) FROM group_members gm WHERE gm.group_id = g.id)
		FROM groups g WHERE g.id = $1::uuid`, id).
		Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.MemberCount)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx,
		`SELECT member_id::text FROM group_members WHERE group_id = $1::uuid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	g.MemberIDs = []string{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		g.MemberIDs = append(g.MemberIDs, m)
	}
	return &g, rows.Err()
}

// Create inserts a group.
func (r *GroupsRepo) Create(ctx context.Context, name string, description *string, createdBy string) (*model.Group, error) {
	var id string
	if err := r.db.QueryRow(ctx, `
		INSERT INTO groups (name, description, created_by)
		VALUES ($1, $2, $3::uuid) RETURNING id::text`,
		name, description, createdBy).Scan(&id); err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// Update patches name/description.
func (r *GroupsRepo) Update(ctx context.Context, id string, name, description *string) (*model.Group, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE groups SET name = coalesce($1, name), description = coalesce($2, description)
		WHERE id = $3::uuid`, name, description, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, pgx.ErrNoRows
	}
	return r.Get(ctx, id)
}

// Delete removes a group; resources restricted only by it become visible to
// all members again (resource_groups rows cascade away).
func (r *GroupsRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM groups WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// SetMembers replaces a group's membership atomically.
func (r *GroupsRepo) SetMembers(ctx context.Context, groupID string, memberIDs []string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `DELETE FROM group_members WHERE group_id = $1::uuid`, groupID); err != nil {
		return err
	}
	if len(memberIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO group_members (group_id, member_id)
			SELECT $1::uuid, m FROM unnest($2::uuid[]) AS m
			ON CONFLICT DO NOTHING`, groupID, memberIDs); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SetResourceGroups replaces the set of groups restricting a resource.
func (r *GroupsRepo) SetResourceGroups(ctx context.Context, resourceID string, groupIDs []string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `DELETE FROM resource_groups WHERE resource_id = $1::uuid`, resourceID); err != nil {
		return err
	}
	if len(groupIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO resource_groups (resource_id, group_id)
			SELECT $1::uuid, g FROM unnest($2::uuid[]) AS g
			ON CONFLICT DO NOTHING`, resourceID, groupIDs); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ResourceGroupIDs returns the group ids restricting a resource.
func (r *GroupsRepo) ResourceGroupIDs(ctx context.Context, resourceID string) ([]string, error) {
	rows, err := r.db.Query(ctx,
		`SELECT group_id::text FROM resource_groups WHERE resource_id = $1::uuid`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
