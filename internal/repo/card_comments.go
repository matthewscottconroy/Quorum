package repo

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// CardCommentsRepo provides PostgreSQL data access for work-card comments.
type CardCommentsRepo struct {
	db *pgxpool.Pool
}

// NewCardCommentsRepo constructs a CardCommentsRepo backed by the given pool.
func NewCardCommentsRepo(db *pgxpool.Pool) *CardCommentsRepo {
	return &CardCommentsRepo{db: db}
}

// authorName resolves to the author's linked member name, else their email.
const cardCommentSelect = `
	SELECT c.id::text, c.action_item_id::text, c.author_id::text,
	       coalesce(m.display_name, u.email), c.body, c.created_at
	FROM action_item_comments c
	LEFT JOIN users u ON u.id = c.author_id
	LEFT JOIN members m ON m.id = u.member_id`

func scanCardComment(row scannable) (model.CardComment, error) {
	var c model.CardComment
	err := row.Scan(&c.ID, &c.ActionItemID, &c.AuthorID, &c.AuthorName, &c.Body, &c.CreatedAt)
	return c, err
}

// List returns a card's conversation, oldest first.
func (r *CardCommentsRepo) List(ctx context.Context, actionItemID string) ([]model.CardComment, error) {
	rows, err := r.db.Query(ctx,
		cardCommentSelect+` WHERE c.action_item_id = $1::uuid ORDER BY c.created_at, c.id`, actionItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.CardComment
	for rows.Next() {
		c, err := scanCardComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Create appends a comment and returns it with the author name resolved.
func (r *CardCommentsRepo) Create(ctx context.Context, actionItemID, authorID, body string) (*model.CardComment, error) {
	var id string
	err := r.db.QueryRow(ctx, `
		INSERT INTO action_item_comments (action_item_id, author_id, body)
		VALUES ($1::uuid, $2::uuid, $3) RETURNING id::text`,
		actionItemID, authorID, body).Scan(&id)
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// Get returns one comment (used for delete authorization), or pgx.ErrNoRows.
func (r *CardCommentsRepo) Get(ctx context.Context, id string) (*model.CardComment, error) {
	c, err := scanCardComment(r.db.QueryRow(ctx, cardCommentSelect+` WHERE c.id = $1::uuid`, id))
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// Delete removes a comment, returning pgx.ErrNoRows if it did not exist.
func (r *CardCommentsRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM action_item_comments WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
