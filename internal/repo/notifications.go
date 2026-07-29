package repo

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// NotifyRepo provides data access for in-app notifications, per-user email
// preferences, and the recipient lookups used to fan an event out to users.
type NotifyRepo struct {
	db *pgxpool.Pool
}

// NewNotifyRepo constructs a NotifyRepo.
func NewNotifyRepo(db *pgxpool.Pool) *NotifyRepo {
	return &NotifyRepo{db: db}
}

// Create records one in-app notification per recipient user in a single
// statement. A blank/empty recipient set is a no-op.
func (r *NotifyRepo) Create(ctx context.Context, userIDs []string, notifType, title string, body, link *string) error {
	if len(userIDs) == 0 {
		return nil
	}
	_, err := r.db.Exec(ctx, `
		INSERT INTO notifications (user_id, type, title, body, link)
		SELECT uid::uuid, $2, $3, $4, $5 FROM unnest($1::uuid[]) AS uid`,
		userIDs, notifType, title, body, link)
	return err
}

// ListForUser returns a user's notifications newest-first, optionally only
// unread, capped at limit.
func (r *NotifyRepo) ListForUser(ctx context.Context, userID string, unreadOnly bool, limit int) ([]model.Notification, error) {
	query := `
		SELECT id::text, type, title, body, link, read_at, created_at
		FROM notifications
		WHERE user_id = $1::uuid`
	if unreadOnly {
		query += ` AND read_at IS NULL`
	}
	query += ` ORDER BY created_at DESC LIMIT $2`
	rows, err := r.db.Query(ctx, query, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.Notification, 0)
	for rows.Next() {
		var n model.Notification
		if err := rows.Scan(&n.ID, &n.Type, &n.Title, &n.Body, &n.Link, &n.ReadAt, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadCount returns how many unread notifications a user has.
func (r *NotifyRepo) UnreadCount(ctx context.Context, userID string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE user_id = $1::uuid AND read_at IS NULL`, userID).Scan(&n)
	return n, err
}

// MarkRead marks one notification read, scoped to its owner so a user cannot
// touch another's rows. Returns pgx.ErrNoRows if it isn't the user's.
func (r *NotifyRepo) MarkRead(ctx context.Context, userID, id string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE notifications SET read_at = now() WHERE id = $1::uuid AND user_id = $2::uuid AND read_at IS NULL`,
		id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// MarkAllRead marks all of a user's unread notifications read.
func (r *NotifyRepo) MarkAllRead(ctx context.Context, userID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE notifications SET read_at = now() WHERE user_id = $1::uuid AND read_at IS NULL`, userID)
	return err
}

// GetPreferences returns a user's email preferences, defaulting to all-enabled
// when no row exists (the common case — rows are created lazily on first update).
func (r *NotifyRepo) GetPreferences(ctx context.Context, userID string) (*model.NotificationPreferences, error) {
	p := &model.NotificationPreferences{
		GovernanceEmail: true, MeetingsEmail: true, DuesEmail: true, AssignmentsEmail: true,
	}
	err := r.db.QueryRow(ctx, `
		SELECT governance_email, meetings_email, dues_email, assignments_email
		FROM notification_preferences WHERE user_id = $1::uuid`, userID).
		Scan(&p.GovernanceEmail, &p.MeetingsEmail, &p.DuesEmail, &p.AssignmentsEmail)
	if err == pgx.ErrNoRows {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// UpdatePreferences upserts a user's email preferences.
func (r *NotifyRepo) UpdatePreferences(ctx context.Context, userID string, p model.NotificationPreferences) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO notification_preferences (user_id, governance_email, meetings_email, dues_email, assignments_email, updated_at)
		VALUES ($1::uuid, $2, $3, $4, $5, now())
		ON CONFLICT (user_id) DO UPDATE SET
			governance_email  = EXCLUDED.governance_email,
			meetings_email    = EXCLUDED.meetings_email,
			dues_email        = EXCLUDED.dues_email,
			assignments_email = EXCLUDED.assignments_email,
			updated_at        = now()`,
		userID, p.GovernanceEmail, p.MeetingsEmail, p.DuesEmail, p.AssignmentsEmail)
	return err
}

// Recipient pairs a user id with their email address for fan-out.
type Recipient struct {
	UserID string
	Email  string
}

// MemberRecipients returns every user at member level or above (i.e. excluding
// 'restricted' accounts), for org-wide notices like motions and meetings.
func (r *NotifyRepo) MemberRecipients(ctx context.Context) ([]Recipient, error) {
	return r.scanRecipients(ctx, `
		SELECT id::text, email FROM users
		WHERE role IN ('member', 'officer', 'admin', 'superadmin') AND email <> ''`)
}

// RecipientForMember returns the user account linked to a member, if any.
func (r *NotifyRepo) RecipientForMember(ctx context.Context, memberID string) (*Recipient, error) {
	var rec Recipient
	err := r.db.QueryRow(ctx,
		`SELECT id::text, email FROM users WHERE member_id = $1::uuid LIMIT 1`, memberID).
		Scan(&rec.UserID, &rec.Email)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// prefColumn maps a preference category to its column, or "" if unknown.
func prefColumn(category string) string {
	return map[string]string{
		"governance":  "governance_email",
		"meetings":    "meetings_email",
		"dues":        "dues_email",
		"assignments": "assignments_email",
	}[category]
}

// EmailOptedIn returns the subset of userIDs whose email preference for the
// category is enabled. A user with no preferences row counts as opted in, so
// existing users receive email until they explicitly opt out.
func (r *NotifyRepo) EmailOptedIn(ctx context.Context, userIDs []string, category string) ([]string, error) {
	col := prefColumn(category)
	if col == "" || len(userIDs) == 0 {
		return userIDs, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT u FROM unnest($1::uuid[]) AS u
		LEFT JOIN notification_preferences p ON p.user_id = u
		WHERE coalesce(p.`+col+`, true)`, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, len(userIDs))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *NotifyRepo) scanRecipients(ctx context.Context, query string, args ...any) ([]Recipient, error) {
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Recipient, 0)
	for rows.Next() {
		var rec Recipient
		if err := rows.Scan(&rec.UserID, &rec.Email); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
