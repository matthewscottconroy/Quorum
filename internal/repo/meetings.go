package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	// From/To bound scheduled_at (inclusive start, exclusive end) — the
	// calendar fetches one visible month at a time with these.
	From   *time.Time
	To     *time.Time
	Limit  int
	Offset int
}

// List returns a page of meetings matching the filter, plus the total count.
func (r *MeetingsRepo) List(ctx context.Context, f MeetingFilter) ([]model.Meeting, int, error) {
	var conds []string
	var args []any
	if f.Upcoming {
		conds = append(conds, "m.scheduled_at >= now() - interval '1 hour'")
	}
	if f.From != nil {
		args = append(args, *f.From)
		conds = append(conds, fmt.Sprintf("m.scheduled_at >= $%d", len(args)))
	}
	if f.To != nil {
		args = append(args, *f.To)
		conds = append(conds, fmt.Sprintf("m.scheduled_at < $%d", len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args = append(args, limit, f.Offset)
	rows, err := r.db.Query(ctx, fmt.Sprintf(`
		SELECT COUNT(*) OVER() AS total_count,
		       m.id::text, m.title, m.scheduled_at, m.ends_at, m.location, m.agenda, m.notes,
		       m.status, m.created_by::text, m.minutes_finalized_at, m.minutes_finalized_by::text,
		       m.created_at, m.updated_at
		FROM meetings m
		%s
		ORDER BY m.scheduled_at DESC
		LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var meetings []model.Meeting
	var total int
	for rows.Next() {
		var mt model.Meeting
		if err := rows.Scan(&total, &mt.ID, &mt.Title, &mt.ScheduledAt, &mt.EndsAt, &mt.Location,
			&mt.Agenda, &mt.Notes, &mt.Status, &mt.CreatedBy, &mt.MinutesFinalizedAt, &mt.MinutesFinalizedBy,
			&mt.CreatedAt, &mt.UpdatedAt); err != nil {
			return nil, 0, err
		}
		meetings = append(meetings, mt)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// COUNT(*) OVER() yields no rows on an empty page; fall back to a plain
	// count — with the SAME filter args bound (minus limit/offset), or the
	// from/to placeholders would make Postgres reject the query outright.
	if len(meetings) == 0 && f.Offset > 0 {
		countQuery := "SELECT count(*) FROM meetings m " + where
		if err := r.db.QueryRow(ctx, countQuery, args[:len(args)-2]...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}
	return meetings, total, nil
}

// Get returns the meeting with the given id, or pgx.ErrNoRows if none exists.
func (r *MeetingsRepo) Get(ctx context.Context, id string) (*model.Meeting, error) {
	row := r.db.QueryRow(ctx, `
		SELECT m.id::text, m.title, m.scheduled_at, m.ends_at, m.location, m.agenda, m.notes,
		       m.status, m.created_by::text, m.minutes_finalized_at, m.minutes_finalized_by::text,
		       m.created_at, m.updated_at
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
		INSERT INTO meetings (title, scheduled_at, ends_at, location, agenda, notes, status, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::uuid)
		RETURNING id::text, title, scheduled_at, ends_at, location, agenda, notes, status, created_by::text, minutes_finalized_at, minutes_finalized_by::text, created_at, updated_at`,
		mt.Title, mt.ScheduledAt, mt.EndsAt, mt.Location, mt.Agenda, mt.Notes, mt.Status, createdBy)
	created, err := scanMeeting(row)
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// Update applies the given field changes to the meeting and returns the
// updated row. endsAt is tri-state via clearEndsAt; the free-text fields
// (location/agenda/notes) are tri-state by VALUE: nil leaves the field
// unchanged, "" clears it, anything else sets it — plain coalesce made a
// deleted agenda quietly come back on save.
func (r *MeetingsRepo) Update(ctx context.Context, id string, title *string, scheduledAt, endsAt *time.Time, clearEndsAt bool, location, agenda, notes, status *string) (*model.Meeting, error) {
	_, err := r.db.Exec(ctx, `
		UPDATE meetings SET
			title        = coalesce($1, title),
			scheduled_at = coalesce($2, scheduled_at),
			ends_at      = CASE WHEN $3 THEN NULL ELSE coalesce($4, ends_at) END,
			location     = CASE WHEN $5::text IS NULL THEN location ELSE nullif($5, '') END,
			agenda       = CASE WHEN $6::text IS NULL THEN agenda   ELSE nullif($6, '') END,
			notes        = CASE WHEN $7::text IS NULL THEN notes    ELSE nullif($7, '') END,
			status       = coalesce($8, status),
			updated_at   = now()
		WHERE id = $9::uuid`,
		title, scheduledAt, clearEndsAt, endsAt, location, agenda, notes, status, id)
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// HasGovernanceHistory reports whether a meeting holds governance history a
// hard delete would silently erase: recorded decisions, motions that reached
// a decision, OPEN motions with ballots already cast, any minutes journal
// entries, or finalized minutes. (All of it cascades away with the meeting.)
func (r *MeetingsRepo) HasGovernanceHistory(ctx context.Context, meetingID string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM meeting_decisions WHERE meeting_id = $1::uuid)
		    OR EXISTS(SELECT 1 FROM motions WHERE meeting_id = $1::uuid
		              AND status IN ('carried', 'failed', 'tabled', 'withdrawn'))
		    OR EXISTS(SELECT 1 FROM motions mo JOIN motion_votes v ON v.motion_id = mo.id
		              WHERE mo.meeting_id = $1::uuid)
		    OR EXISTS(SELECT 1 FROM meeting_minutes_entries WHERE meeting_id = $1::uuid)
		    OR EXISTS(SELECT 1 FROM meetings WHERE id = $1::uuid
		              AND minutes_finalized_at IS NOT NULL)`, meetingID).Scan(&exists)
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
		       vote_for, vote_against, vote_abstain, outcome, motion_id::text, recorded_at
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
			&d.VoteFor, &d.VoteAgainst, &d.VoteAbstain, &d.Outcome, &d.MotionID, &d.RecordedAt); err != nil {
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
func (r *MeetingsRepo) UpdateDecision(ctx context.Context, meetingID, id string, summary, detail, outcome *string, voteFor, voteAgainst, voteAbstain *int) (*model.MeetingDecision, error) {
	var d model.MeetingDecision
	err := r.db.QueryRow(ctx, `
		UPDATE meeting_decisions SET
			summary      = coalesce($1, summary),
			detail       = coalesce($2, detail),
			vote_for     = coalesce($3, vote_for),
			vote_against = coalesce($4, vote_against),
			vote_abstain = coalesce($5, vote_abstain),
			outcome      = coalesce($6, outcome)
		WHERE id = $7::uuid AND meeting_id = $8::uuid
		RETURNING id::text, meeting_id::text, summary, detail, vote_for, vote_against, vote_abstain, outcome, recorded_at`,
		summary, detail, voteFor, voteAgainst, voteAbstain, outcome, id, meetingID).
		Scan(&d.ID, &d.MeetingID, &d.Summary, &d.Detail,
			&d.VoteFor, &d.VoteAgainst, &d.VoteAbstain, &d.Outcome, &d.RecordedAt)
	return &d, err
}

// DeleteDecision removes a decision, returning pgx.ErrNoRows if absent.
func (r *MeetingsRepo) DeleteDecision(ctx context.Context, meetingID, id string) error {
	tag, err := r.db.Exec(ctx,
		`DELETE FROM meeting_decisions WHERE id = $1::uuid AND meeting_id = $2::uuid`, id, meetingID)
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
		SELECT m.id::text, m.title, m.scheduled_at, m.ends_at, m.location, m.agenda, m.notes,
		       m.status, m.created_by::text, m.minutes_finalized_at, m.minutes_finalized_by::text,
		       m.created_at, m.updated_at
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
	err := row.Scan(&mt.ID, &mt.Title, &mt.ScheduledAt, &mt.EndsAt, &mt.Location, &mt.Agenda,
		&mt.Notes, &mt.Status, &mt.CreatedBy, &mt.MinutesFinalizedAt, &mt.MinutesFinalizedBy,
		&mt.CreatedAt, &mt.UpdatedAt)
	return mt, err
}

// ---- Minutes (recording secretary) ----

// ErrMinutesFinalized is returned when attempting to finalize minutes twice.
var ErrMinutesFinalized = errors.New("minutes are already finalized")

// ErrMinutesNotFinal marks a correction against a draft — drafts are edited
// directly; corrections exist only for the frozen official record.
var ErrMinutesNotFinal = errors.New("minutes are not finalized")

// ListMinutes returns a meeting's journal in chronological order, with the
// linked motion's title and the recorder's email resolved for display.
func (r *MeetingsRepo) ListMinutes(ctx context.Context, meetingID string) ([]model.MinutesEntry, error) {
	rows, err := r.db.Query(ctx, `
		SELECT e.id::text, e.meeting_id::text, e.seq, e.kind, e.body,
		       e.motion_id::text, mo.title, e.recorded_by::text, u.email, e.recorded_at
		FROM meeting_minutes_entries e
		LEFT JOIN motions mo ON mo.id = e.motion_id
		LEFT JOIN users u ON u.id = e.recorded_by
		WHERE e.meeting_id = $1::uuid
		ORDER BY e.seq`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.MinutesEntry, 0)
	for rows.Next() {
		var e model.MinutesEntry
		if err := rows.Scan(&e.ID, &e.MeetingID, &e.Seq, &e.Kind, &e.Body,
			&e.MotionID, &e.MotionTitle, &e.RecordedBy, &e.RecordedByName, &e.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddMinutesEntry appends one journal line. The database refuses if the
// meeting's minutes are finalized.
func (r *MeetingsRepo) AddMinutesEntry(ctx context.Context, meetingID, kind, body string, motionID *string, recordedBy string) (*model.MinutesEntry, error) {
	var id string
	err := r.db.QueryRow(ctx, `
		INSERT INTO meeting_minutes_entries (meeting_id, kind, body, motion_id, recorded_by)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5::uuid)
		RETURNING id::text`, meetingID, kind, body, motionID, recordedBy).Scan(&id)
	if err != nil {
		return nil, err
	}
	return r.getMinutesEntry(ctx, id)
}

func (r *MeetingsRepo) getMinutesEntry(ctx context.Context, id string) (*model.MinutesEntry, error) {
	var e model.MinutesEntry
	err := r.db.QueryRow(ctx, `
		SELECT e.id::text, e.meeting_id::text, e.seq, e.kind, e.body,
		       e.motion_id::text, mo.title, e.recorded_by::text, u.email, e.recorded_at
		FROM meeting_minutes_entries e
		LEFT JOIN motions mo ON mo.id = e.motion_id
		LEFT JOIN users u ON u.id = e.recorded_by
		WHERE e.id = $1::uuid`, id).
		Scan(&e.ID, &e.MeetingID, &e.Seq, &e.Kind, &e.Body,
			&e.MotionID, &e.MotionTitle, &e.RecordedBy, &e.RecordedByName, &e.RecordedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// UpdateMinutesEntry replaces an entry's kind/body/motion link (a correction
// during the meeting). meetingID scopes the update so an entry id from another
// meeting cannot be targeted. Refused by the database once finalized.
func (r *MeetingsRepo) UpdateMinutesEntry(ctx context.Context, meetingID, entryID, kind, body string, motionID *string) (*model.MinutesEntry, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE meeting_minutes_entries
		SET kind = $1, body = $2, motion_id = $3::uuid
		WHERE id = $4::uuid AND meeting_id = $5::uuid`,
		kind, body, motionID, entryID, meetingID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, pgx.ErrNoRows
	}
	return r.getMinutesEntry(ctx, entryID)
}

// DeleteMinutesEntry removes an entry (before finalization only; the database
// enforces that).
func (r *MeetingsRepo) DeleteMinutesEntry(ctx context.Context, meetingID, entryID string) error {
	tag, err := r.db.Exec(ctx, `
		DELETE FROM meeting_minutes_entries WHERE id = $1::uuid AND meeting_id = $2::uuid`,
		entryID, meetingID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// FinalizeMinutes marks a meeting's minutes approved: from then on the journal
// is immutable (database trigger) and finalization cannot be undone. Returns
// ErrMinutesFinalized if already done, pgx.ErrNoRows if the meeting is missing.
func (r *MeetingsRepo) FinalizeMinutes(ctx context.Context, meetingID, userID string) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE meetings SET minutes_finalized_at = now(), minutes_finalized_by = $2::uuid, updated_at = now()
		WHERE id = $1::uuid AND minutes_finalized_at IS NULL`, meetingID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM meetings WHERE id = $1::uuid)`, meetingID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return pgx.ErrNoRows
		}
		return ErrMinutesFinalized
	}
	return nil
}

// SetMinutesSnapshot stores the rendered minutes document captured at
// finalization; it is the copy served forever after.
func (r *MeetingsRepo) SetMinutesSnapshot(ctx context.Context, meetingID, doc string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE meetings SET minutes_snapshot = $2 WHERE id = $1::uuid`, meetingID, doc)
	return err
}

// GetMinutesSnapshot returns the finalized document, or "" when none exists
// (never finalized, or finalized before snapshots existed).
func (r *MeetingsRepo) GetMinutesSnapshot(ctx context.Context, meetingID string) (string, error) {
	var doc *string
	err := r.db.QueryRow(ctx,
		`SELECT minutes_snapshot FROM meetings WHERE id = $1::uuid`, meetingID).Scan(&doc)
	if err != nil {
		return "", err
	}
	if doc == nil {
		return "", nil
	}
	return *doc, nil
}

// AddCorrection appends an erratum to FINALIZED minutes (the DB trigger
// makes rows immutable once written). Refused with ErrMinutesNotFinal on a
// draft — drafts are edited directly, corrections exist for the frozen record.
func (r *MeetingsRepo) AddCorrection(ctx context.Context, meetingID, body, createdBy string) (*model.MeetingCorrection, error) {
	var finalized *time.Time
	if err := r.db.QueryRow(ctx,
		`SELECT minutes_finalized_at FROM meetings WHERE id = $1::uuid`, meetingID).Scan(&finalized); err != nil {
		return nil, err
	}
	if finalized == nil {
		return nil, ErrMinutesNotFinal
	}
	var c model.MeetingCorrection
	err := r.db.QueryRow(ctx, `
		INSERT INTO meeting_corrections (meeting_id, body, created_by)
		VALUES ($1::uuid, $2, $3::uuid)
		RETURNING id::text, meeting_id::text, body, created_at`,
		meetingID, body, createdBy).Scan(&c.ID, &c.MeetingID, &c.Body, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ListCorrections returns a meeting's errata, oldest first, with author names.
func (r *MeetingsRepo) ListCorrections(ctx context.Context, meetingID string) ([]model.MeetingCorrection, error) {
	rows, err := r.db.Query(ctx, `
		SELECT c.id::text, c.meeting_id::text, c.body, c.created_at,
		       coalesce(m.display_name, u.email, 'former user')
		FROM meeting_corrections c
		LEFT JOIN users u ON u.id = c.created_by
		LEFT JOIN members m ON m.id = u.member_id
		WHERE c.meeting_id = $1::uuid
		ORDER BY c.created_at, c.id`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.MeetingCorrection
	for rows.Next() {
		var c model.MeetingCorrection
		if err := rows.Scan(&c.ID, &c.MeetingID, &c.Body, &c.CreatedAt, &c.AuthorName); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetRSVP records or updates a member's RSVP for a meeting ('yes'/'no'/'maybe').
func (r *MeetingsRepo) SetRSVP(ctx context.Context, meetingID, memberID, response string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO meeting_rsvps (meeting_id, member_id, response, updated_at)
		VALUES ($1::uuid, $2::uuid, $3, now())
		ON CONFLICT (meeting_id, member_id)
		DO UPDATE SET response = EXCLUDED.response, updated_at = now()`,
		meetingID, memberID, response)
	return err
}

// RSVPSummary returns per-response counts plus the caller's own response
// (empty if they haven't RSVP'd). memberID may be "" for a caller with no
// linked member record.
func (r *MeetingsRepo) RSVPSummary(ctx context.Context, meetingID, memberID string) (model.RSVPSummary, error) {
	var s model.RSVPSummary
	rows, err := r.db.Query(ctx,
		`SELECT response, count(*) FROM meeting_rsvps WHERE meeting_id = $1::uuid GROUP BY response`, meetingID)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var resp string
		var n int
		if err := rows.Scan(&resp, &n); err != nil {
			rows.Close()
			return s, err
		}
		switch resp {
		case "yes":
			s.Yes = n
		case "no":
			s.No = n
		case "maybe":
			s.Maybe = n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s, err
	}
	if memberID != "" {
		_ = r.db.QueryRow(ctx,
			`SELECT response FROM meeting_rsvps WHERE meeting_id = $1::uuid AND member_id = $2::uuid`,
			meetingID, memberID).Scan(&s.Mine)
	}
	return s, nil
}

// RSVPYesMemberIDs returns member IDs who RSVP'd yes — used to seed the
// attendance roster from expected attendees.
func (r *MeetingsRepo) RSVPYesMemberIDs(ctx context.Context, meetingID string) ([]string, error) {
	rows, err := r.db.Query(ctx,
		`SELECT member_id::text FROM meeting_rsvps WHERE meeting_id = $1::uuid AND response = 'yes'`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
