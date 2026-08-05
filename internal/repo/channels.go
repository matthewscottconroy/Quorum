package repo

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// ChannelsRepo provides data access for discussion channels, membership,
// and threaded messages.
type ChannelsRepo struct {
	db *pgxpool.Pool
}

// NewChannelsRepo constructs the repo.
func NewChannelsRepo(db *pgxpool.Pool) *ChannelsRepo {
	return &ChannelsRepo{db: db}
}

// List returns every channel with counts, flagging the viewer's memberships.
func (r *ChannelsRepo) List(ctx context.Context, viewerID string) ([]model.Channel, error) {
	rows, err := r.db.Query(ctx, `
		SELECT c.id::text, c.name, c.topic, c.created_by::text, c.created_at,
		       (SELECT count(*) FROM channel_members cm WHERE cm.channel_id = c.id)::int,
		       (SELECT count(*) FROM channel_messages m WHERE m.channel_id = c.id)::int,
		       EXISTS (SELECT 1 FROM channel_members cm WHERE cm.channel_id = c.id AND cm.user_id = $1::uuid)
		FROM channels c ORDER BY lower(c.name)`, viewerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Channel
	for rows.Next() {
		var c model.Channel
		if err := rows.Scan(&c.ID, &c.Name, &c.Topic, &c.CreatedBy, &c.CreatedAt,
			&c.MemberCount, &c.MessageCount, &c.IsMember); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get returns one channel with its member roster (names resolved).
func (r *ChannelsRepo) Get(ctx context.Context, id, viewerID string) (*model.Channel, error) {
	var c model.Channel
	err := r.db.QueryRow(ctx, `
		SELECT c.id::text, c.name, c.topic, c.created_by::text, c.created_at,
		       (SELECT count(*) FROM channel_members cm WHERE cm.channel_id = c.id)::int,
		       (SELECT count(*) FROM channel_messages m WHERE m.channel_id = c.id)::int,
		       EXISTS (SELECT 1 FROM channel_members cm WHERE cm.channel_id = c.id AND cm.user_id = $2::uuid)
		FROM channels c WHERE c.id = $1::uuid`, id, viewerID).
		Scan(&c.ID, &c.Name, &c.Topic, &c.CreatedBy, &c.CreatedAt,
			&c.MemberCount, &c.MessageCount, &c.IsMember)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `
		SELECT cm.user_id::text, coalesce(m.display_name, u.email)
		FROM channel_members cm
		JOIN users u ON u.id = cm.user_id
		LEFT JOIN members m ON m.id = u.member_id
		WHERE cm.channel_id = $1::uuid ORDER BY 2`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var mem model.ChannelMember
		if err := rows.Scan(&mem.UserID, &mem.Name); err != nil {
			return nil, err
		}
		c.Members = append(c.Members, mem)
	}
	return &c, rows.Err()
}

// Create adds a channel; the creator becomes its first member.
func (r *ChannelsRepo) Create(ctx context.Context, name string, topic *string, createdBy string) (*model.Channel, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO channels (name, topic, created_by) VALUES ($1, $2, $3::uuid)
		RETURNING id::text`, name, topic, createdBy).Scan(&id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO channel_members (channel_id, user_id, added_by)
		VALUES ($1::uuid, $2::uuid, $2::uuid)`, id, createdBy); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.Get(ctx, id, createdBy)
}

// Update renames or retopics a channel.
func (r *ChannelsRepo) Update(ctx context.Context, id string, name, topic *string) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE channels SET name = coalesce($1, name), topic = coalesce($2, topic)
		WHERE id = $3::uuid`, name, topic, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// Delete removes a channel and (via cascades) its membership and history.
func (r *ChannelsRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM channels WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// IsMember reports channel membership.
func (r *ChannelsRepo) IsMember(ctx context.Context, channelID, userID string) (bool, error) {
	var ok bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM channel_members WHERE channel_id = $1::uuid AND user_id = $2::uuid)`,
		channelID, userID).Scan(&ok)
	return ok, err
}

// AddMember is idempotent (already-a-member is not an error).
func (r *ChannelsRepo) AddMember(ctx context.Context, channelID, userID, addedBy string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO channel_members (channel_id, user_id, added_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid) ON CONFLICT DO NOTHING`, channelID, userID, addedBy)
	return err
}

// RemoveMember drops a user from the channel.
func (r *ChannelsRepo) RemoveMember(ctx context.Context, channelID, userID string) error {
	tag, err := r.db.Exec(ctx,
		`DELETE FROM channel_members WHERE channel_id = $1::uuid AND user_id = $2::uuid`, channelID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

const channelMessageSelect = `
	SELECT msg.id::text, msg.channel_id::text, msg.parent_id::text, msg.author_id::text,
	       coalesce(m.display_name, u.email), msg.body, msg.resource_id::text, msg.created_at,
	       (SELECT count(*) FROM channel_messages r WHERE r.parent_id = msg.id)::int
	FROM channel_messages msg
	LEFT JOIN users u ON u.id = msg.author_id
	LEFT JOIN members m ON m.id = u.member_id`

func scanChannelMessage(row scannable) (model.ChannelMessage, error) {
	var msg model.ChannelMessage
	err := row.Scan(&msg.ID, &msg.ChannelID, &msg.ParentID, &msg.AuthorID, &msg.AuthorName,
		&msg.Body, &msg.ResourceID, &msg.CreatedAt, &msg.ReplyCount)
	return msg, err
}

// Messages returns a channel's root messages (parentID "") or one thread's
// replies (parentID set), oldest first, capped.
func (r *ChannelsRepo) Messages(ctx context.Context, channelID, parentID string, limit int) ([]model.ChannelMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := channelMessageSelect + ` WHERE msg.channel_id = $1::uuid AND `
	args := []any{channelID}
	if parentID == "" {
		q += `msg.parent_id IS NULL`
	} else {
		q += `msg.parent_id = $2::uuid`
		args = append(args, parentID)
	}
	q += ` ORDER BY msg.created_at DESC LIMIT ` + itoaSafe(limit)
	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ChannelMessage
	for rows.Next() {
		msg, err := scanChannelMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	// Reverse to oldest-first for display.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// PostMessage appends a message (optionally a reply, optionally referencing
// a library resource) and returns it resolved.
func (r *ChannelsRepo) PostMessage(ctx context.Context, channelID, parentID, authorID, body, resourceID string) (*model.ChannelMessage, error) {
	var pid, rid *string
	if parentID != "" {
		pid = &parentID
	}
	if resourceID != "" {
		rid = &resourceID
	}
	var id string
	if err := r.db.QueryRow(ctx, `
		INSERT INTO channel_messages (channel_id, parent_id, author_id, body, resource_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid) RETURNING id::text`,
		channelID, pid, authorID, body, rid).Scan(&id); err != nil {
		return nil, err
	}
	msg, err := scanChannelMessage(r.db.QueryRow(ctx, channelMessageSelect+` WHERE msg.id = $1::uuid`, id))
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

// GetMessage returns one message (for delete authorization).
func (r *ChannelsRepo) GetMessage(ctx context.Context, id string) (*model.ChannelMessage, error) {
	msg, err := scanChannelMessage(r.db.QueryRow(ctx, channelMessageSelect+` WHERE msg.id = $1::uuid`, id))
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

// DeleteMessage removes a message (replies cascade with a root).
func (r *ChannelsRepo) DeleteMessage(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM channel_messages WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ListChatUsers returns addable accounts (role above restricted) with
// display names resolved — the "add people" picker.
func (r *ChannelsRepo) ListChatUsers(ctx context.Context) ([]model.ChannelMember, error) {
	rows, err := r.db.Query(ctx, `
		SELECT u.id::text, coalesce(m.display_name, u.email)
		FROM users u
		LEFT JOIN members m ON m.id = u.member_id
		WHERE u.role <> 'restricted'
		ORDER BY 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ChannelMember
	for rows.Next() {
		var mem model.ChannelMember
		if err := rows.Scan(&mem.UserID, &mem.Name); err != nil {
			return nil, err
		}
		out = append(out, mem)
	}
	return out, rows.Err()
}

// itoaSafe formats a pre-clamped positive int for LIMIT interpolation.
func itoaSafe(n int) string {
	if n < 1 {
		n = 1
	}
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var b [4]byte
	i := len(b)
	for n > 0 && i > 0 {
		i--
		b[i] = digits[n%10]
		n /= 10
	}
	return string(b[i:])
}
