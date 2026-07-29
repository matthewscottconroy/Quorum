package repo

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// MeetingsRepo provides PostgreSQL data access for meetings.
type MeetingsRepo struct {
	db *pgxpool.Pool
}

// NewMeetingsRepo constructs a MeetingsRepo backed by the given connection pool.
func NewMeetingsRepo(db *pgxpool.Pool) *MeetingsRepo {
	return &MeetingsRepo{db: db}
}

// MeetingFilter holds the optional query parameters for listing meetings.
type MeetingFilter struct {
	Upcoming bool
	Limit    int
	Offset   int
}

// List returns a page of meetings matching the filter, plus the total count.
func (r *MeetingsRepo) List(ctx context.Context, f MeetingFilter) ([]model.Meeting, int, error) {
	where := ""
	if f.Upcoming {
		where = "WHERE m.scheduled_at >= now() - interval '1 hour'"
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.Query(ctx, `
		SELECT COUNT(*) OVER() AS total_count,
		       m.id::text, m.title, m.scheduled_at, m.location, m.agenda, m.notes,
		       m.status, m.created_by::text, m.created_at, m.updated_at
		FROM meetings m
		`+where+`
		ORDER BY m.scheduled_at DESC
		LIMIT $1 OFFSET $2`, limit, f.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var meetings []model.Meeting
	var total int
	for rows.Next() {
		var mt model.Meeting
		if err := rows.Scan(&total, &mt.ID, &mt.Title, &mt.ScheduledAt, &mt.Location,
			&mt.Agenda, &mt.Notes, &mt.Status, &mt.CreatedBy, &mt.CreatedAt, &mt.UpdatedAt); err != nil {
			return nil, 0, err
		}
		meetings = append(meetings, mt)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// COUNT(*) OVER() yields no rows on an empty page; fall back to a plain count.
	if len(meetings) == 0 && f.Offset > 0 {
		countQuery := "SELECT count(*) FROM meetings m " + where
		if err := r.db.QueryRow(ctx, countQuery).Scan(&total); err != nil {
			return nil, 0, err
		}
	}
	return meetings, total, nil
}

// Get returns the meeting with the given id, or pgx.ErrNoRows if none exists.
func (r *MeetingsRepo) Get(ctx context.Context, id string) (*model.Meeting, error) {
	row := r.db.QueryRow(ctx, `
		SELECT m.id::text, m.title, m.scheduled_at, m.location, m.agenda, m.notes,
		       m.status, m.created_by::text, m.created_at, m.updated_at
		FROM meetings m WHERE m.id = $1::uuid`, id)
	mt, err := scanMeeting(row)
	if err != nil {
		return nil, err
	}

	mt.Attendees, err = r.GetAttendees(ctx, id)
	if err != nil {
		return nil, err
	}
	mt.Decisions, err = r.GetDecisions(ctx, id)
	if err != nil {
		return nil, err
	}
	return &mt, nil
}

// Create inserts a new meeting and returns the stored row.
func (r *MeetingsRepo) Create(ctx context.Context, mt *model.Meeting, createdBy string) (*model.Meeting, error) {
	row := r.db.QueryRow(ctx, `
		INSERT INTO meetings (title, scheduled_at, location, agenda, notes, status, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7::uuid)
		RETURNING id::text, title, scheduled_at, location, agenda, notes, status, created_by::text, created_at, updated_at`,
		mt.Title, mt.ScheduledAt, mt.Location, mt.Agenda, mt.Notes, mt.Status, createdBy)
	created, err := scanMeeting(row)
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// Update applies the given field changes to the meeting and returns the updated row.
func (r *MeetingsRepo) Update(ctx context.Context, id string, title *string, scheduledAt *time.Time, location, agenda, notes, status *string) (*model.Meeting, error) {
	_, err := r.db.Exec(ctx, `
		UPDATE meetings SET
			title        = coalesce($1, title),
			scheduled_at = coalesce($2, scheduled_at),
			location     = coalesce($3, location),
			agenda       = coalesce($4, agenda),
			notes        = coalesce($5, notes),
			status       = coalesce($6, status),
			updated_at   = now()
		WHERE id = $7::uuid`,
		title, scheduledAt, location, agenda, notes, status, id)
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// HasGovernanceHistory reports whether a meeting has recorded decisions or any
// motion that reached a decision (carried/failed/tabled/withdrawn) — history a
// hard delete would silently erase (both cascade away with the meeting).
func (r *MeetingsRepo) HasGovernanceHistory(ctx context.Context, meetingID string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM meeting_decisions WHERE meeting_id = $1::uuid)
		    OR EXISTS(SELECT 1 FROM motions WHERE meeting_id = $1::uuid
		              AND status IN ('carried', 'failed', 'tabled', 'withdrawn'))`, meetingID).Scan(&exists)
	return exists, err
}

// Delete removes the meeting, returning pgx.ErrNoRows if no such row existed.
func (r *MeetingsRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM meetings WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// AttendeeEmails returns the on-file email addresses of a meeting's attendees,
// used to notify affected members before the meeting (and its attendee rows) is
// deleted. Must be called before Delete, which cascade-removes attendees.
func (r *MeetingsRepo) AttendeeEmails(ctx context.Context, meetingID string) ([]string, error) {
	rows, err := r.db.Query(ctx, `
		SELECT m.email FROM meeting_attendees ma
		JOIN members m ON m.id = ma.member_id
		WHERE ma.meeting_id = $1::uuid AND m.email IS NOT NULL AND m.email <> ''`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var emails []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		emails = append(emails, e)
	}
	return emails, rows.Err()
}

// GetAttendees returns the meeting's attendee list.
func (r *MeetingsRepo) GetAttendees(ctx context.Context, meetingID string) ([]model.MeetingAttendee, error) {
	rows, err := r.db.Query(ctx, `
		SELECT ma.member_id::text, m.display_name, ma.present
		FROM meeting_attendees ma
		JOIN members m ON m.id = ma.member_id
		WHERE ma.meeting_id = $1::uuid
		ORDER BY m.display_name`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var attendees []model.MeetingAttendee
	for rows.Next() {
		var a model.MeetingAttendee
		if err := rows.Scan(&a.MemberID, &a.MemberName, &a.Present); err != nil {
			return nil, err
		}
		attendees = append(attendees, a)
	}
	return attendees, rows.Err()
}

// SetAttendees replaces the meeting's attendee list.
func (r *MeetingsRepo) SetAttendees(ctx context.Context, meetingID string, attendees []model.MeetingAttendee) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `DELETE FROM meeting_attendees WHERE meeting_id = $1::uuid`, meetingID); err != nil {
		return err
	}
	for _, a := range attendees {
		if _, err := tx.Exec(ctx, `
			INSERT INTO meeting_attendees (meeting_id, member_id, present)
			VALUES ($1::uuid, $2::uuid, $3)`,
			meetingID, a.MemberID, a.Present); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// GetDecisions returns the meeting's decision log.
func (r *MeetingsRepo) GetDecisions(ctx context.Context, meetingID string) ([]model.MeetingDecision, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id::text, meeting_id::text, summary, detail,
		       vote_for, vote_against, vote_abstain, outcome, recorded_at
		FROM meeting_decisions WHERE meeting_id = $1::uuid
		ORDER BY recorded_at`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var decisions []model.MeetingDecision
	for rows.Next() {
		var d model.MeetingDecision
		if err := rows.Scan(&d.ID, &d.MeetingID, &d.Summary, &d.Detail,
			&d.VoteFor, &d.VoteAgainst, &d.VoteAbstain, &d.Outcome, &d.RecordedAt); err != nil {
			return nil, err
		}
		decisions = append(decisions, d)
	}
	return decisions, rows.Err()
}

// CreateDecision records a decision on the meeting.
func (r *MeetingsRepo) CreateDecision(ctx context.Context, d *model.MeetingDecision) (*model.MeetingDecision, error) {
	var created model.MeetingDecision
	err := r.db.QueryRow(ctx, `
		INSERT INTO meeting_decisions (meeting_id, summary, detail, vote_for, vote_against, vote_abstain, outcome)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
		RETURNING id::text, meeting_id::text, summary, detail, vote_for, vote_against, vote_abstain, outcome, recorded_at`,
		d.MeetingID, d.Summary, d.Detail, d.VoteFor, d.VoteAgainst, d.VoteAbstain, d.Outcome).
		Scan(&created.ID, &created.MeetingID, &created.Summary, &created.Detail,
			&created.VoteFor, &created.VoteAgainst, &created.VoteAbstain, &created.Outcome, &created.RecordedAt)
	return &created, err
}

// UpdateDecision edits an existing decision, returning pgx.ErrNoRows if absent.
func (r *MeetingsRepo) UpdateDecision(ctx context.Context, id string, summary, detail, outcome *string, voteFor, voteAgainst, voteAbstain *int) (*model.MeetingDecision, error) {
	var d model.MeetingDecision
	err := r.db.QueryRow(ctx, `
		UPDATE meeting_decisions SET
			summary      = coalesce($1, summary),
			detail       = coalesce($2, detail),
			vote_for     = coalesce($3, vote_for),
			vote_against = coalesce($4, vote_against),
			vote_abstain = coalesce($5, vote_abstain),
			outcome      = coalesce($6, outcome)
		WHERE id = $7::uuid
		RETURNING id::text, meeting_id::text, summary, detail, vote_for, vote_against, vote_abstain, outcome, recorded_at`,
		summary, detail, voteFor, voteAgainst, voteAbstain, outcome, id).
		Scan(&d.ID, &d.MeetingID, &d.Summary, &d.Detail,
			&d.VoteFor, &d.VoteAgainst, &d.VoteAbstain, &d.Outcome, &d.RecordedAt)
	return &d, err
}

// DeleteDecision removes a decision, returning pgx.ErrNoRows if absent.
func (r *MeetingsRepo) DeleteDecision(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM meeting_decisions WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// Upcoming returns up to n meetings scheduled from now onward.
func (r *MeetingsRepo) Upcoming(ctx context.Context, n int) ([]model.Meeting, error) {
	rows, err := r.db.Query(ctx, `
		SELECT m.id::text, m.title, m.scheduled_at, m.location, m.agenda, m.notes,
		       m.status, m.created_by::text, m.created_at, m.updated_at
		FROM meetings m
		WHERE m.scheduled_at >= now() AND m.status = 'scheduled'
		ORDER BY m.scheduled_at
		LIMIT $1`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var meetings []model.Meeting
	for rows.Next() {
		mt, err := scanMeeting(rows)
		if err != nil {
			return nil, err
		}
		meetings = append(meetings, mt)
	}
	return meetings, rows.Err()
}

func scanMeeting(row scannable) (model.Meeting, error) {
	var mt model.Meeting
	err := row.Scan(&mt.ID, &mt.Title, &mt.ScheduledAt, &mt.Location, &mt.Agenda,
		&mt.Notes, &mt.Status, &mt.CreatedBy, &mt.CreatedAt, &mt.UpdatedAt)
	return mt, err
}
