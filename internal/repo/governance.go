package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// ErrMotionNotOpen is returned when a ballot is submitted for a motion whose
// voting is not open. The token is left unconsumed so the member can retry.
var ErrMotionNotOpen = errors.New("motion is not open for voting")

// GovernanceRepo provides PostgreSQL data access for quorum settings, motions,
// ballots, and meeting proxies.
type GovernanceRepo struct {
	db *pgxpool.Pool
}

// NewGovernanceRepo constructs a GovernanceRepo backed by the given pool.
func NewGovernanceRepo(db *pgxpool.Pool) *GovernanceRepo {
	return &GovernanceRepo{db: db}
}

// ---- Settings ----

// GetSettings returns the singleton governance settings row.
func (r *GovernanceRepo) GetSettings(ctx context.Context) (*model.GovernanceSettings, error) {
	var s model.GovernanceSettings
	err := r.db.QueryRow(ctx, `
		SELECT quorum_mode, quorum_value, proxies_count_toward_quorum, default_threshold, updated_at
		FROM governance_settings WHERE id = 1`).
		Scan(&s.QuorumMode, &s.QuorumValue, &s.ProxiesCountTowardQuorum, &s.DefaultThreshold, &s.UpdatedAt)
	return &s, err
}

// UpdateSettings writes the governance settings and returns the stored row.
func (r *GovernanceRepo) UpdateSettings(ctx context.Context, s *model.GovernanceSettings) (*model.GovernanceSettings, error) {
	var out model.GovernanceSettings
	err := r.db.QueryRow(ctx, `
		UPDATE governance_settings SET
			quorum_mode                 = $1,
			quorum_value                = $2,
			proxies_count_toward_quorum = $3,
			default_threshold           = $4,
			updated_at                  = now()
		WHERE id = 1
		RETURNING quorum_mode, quorum_value, proxies_count_toward_quorum, default_threshold, updated_at`,
		s.QuorumMode, s.QuorumValue, s.ProxiesCountTowardQuorum, s.DefaultThreshold).
		Scan(&out.QuorumMode, &out.QuorumValue, &out.ProxiesCountTowardQuorum, &out.DefaultThreshold, &out.UpdatedAt)
	return &out, err
}

// ---- Quorum ----

// RequiredQuorum computes the quorum threshold for the given active-member count
// under the configured mode. Exported so the outcome math can be unit-tested.
func RequiredQuorum(mode string, value, active int) int {
	switch mode {
	case "fixed":
		return value
	case "percent":
		// ceil(active * value / 100)
		return (active*value + 99) / 100
	default: // "majority"
		return active/2 + 1
	}
}

// ComputeQuorum returns the live quorum status for a meeting: the active roster
// size, how many attendees are present, how many absent members are represented
// by a present proxy holder, and whether quorum is met.
func (r *GovernanceRepo) ComputeQuorum(ctx context.Context, meetingID string) (*model.QuorumStatus, error) {
	settings, err := r.GetSettings(ctx)
	if err != nil {
		return nil, err
	}

	var active int
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM members WHERE status = 'active'`).Scan(&active); err != nil {
		return nil, err
	}

	// Present member ids for this meeting — active members only, since quorum is
	// a fraction of the active roster (an inactive attendee must not count).
	present := map[string]bool{}
	rows, err := r.db.Query(ctx, `
		SELECT ma.member_id::text FROM meeting_attendees ma
		JOIN members m ON m.id = ma.member_id
		WHERE ma.meeting_id = $1::uuid AND ma.present = TRUE AND m.status = 'active'`, meetingID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		present[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Proxies: an absent (active) grantor whose holder is present is "represented".
	// Only proxies granted by an active member count toward quorum.
	proxyRepresented := 0
	prows, err := r.db.Query(ctx, `
		SELECT p.grantor_id::text, p.holder_id::text FROM meeting_proxies p
		JOIN members g ON g.id = p.grantor_id AND g.status = 'active'
		WHERE p.meeting_id = $1::uuid`, meetingID)
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		var grantor, holder string
		if err := prows.Scan(&grantor, &holder); err != nil {
			prows.Close()
			return nil, err
		}
		if present[holder] && !present[grantor] {
			proxyRepresented++
		}
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return nil, err
	}

	presentCount := len(present)
	effective := presentCount
	if settings.ProxiesCountTowardQuorum {
		effective += proxyRepresented
	}
	required := RequiredQuorum(settings.QuorumMode, settings.QuorumValue, active)

	return &model.QuorumStatus{
		Mode:               settings.QuorumMode,
		Required:           required,
		ActiveMembers:      active,
		PresentCount:       presentCount,
		ProxiesRepresented: proxyRepresented,
		EffectivePresent:   effective,
		Met:                effective >= required,
	}, nil
}

// ---- Motions ----

// motionCarried applies the threshold to a for/against tally. Abstentions are
// excluded from the base. Exported logic lives in ComputeMotionOutcome.
func ComputeMotionOutcome(threshold string, votesFor, against int) bool {
	if votesFor == 0 {
		return false
	}
	switch threshold {
	case "unanimous":
		return against == 0
	case "two_thirds":
		// for / (for+against) >= 2/3  ⇔  3*for >= 2*(for+against)
		return 3*votesFor >= 2*(votesFor+against)
	default: // "majority"
		return votesFor > against
	}
}

// ListMotions returns all motions for a meeting with their tallies, newest last.
func (r *GovernanceRepo) ListMotions(ctx context.Context, meetingID string) ([]model.Motion, error) {
	rows, err := r.db.Query(ctx, `
		SELECT mo.id::text, mo.meeting_id::text, mo.title, mo.detail,
		       mo.mover_id::text, mv.display_name,
		       mo.seconder_id::text, sc.display_name,
		       mo.threshold, mo.business, mo.status, mo.plan_id::text, pl.title,
		       mo.created_by::text,
		       mo.opened_at, mo.closed_at, mo.created_at, mo.updated_at
		FROM motions mo
		LEFT JOIN members mv ON mv.id = mo.mover_id
		LEFT JOIN members sc ON sc.id = mo.seconder_id
		LEFT JOIN plans pl ON pl.id = mo.plan_id
		WHERE mo.meeting_id = $1::uuid
		ORDER BY mo.created_at
		LIMIT 500`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var motions []model.Motion
	var ids []string
	for rows.Next() {
		m, err := scanMotion(rows)
		if err != nil {
			return nil, err
		}
		motions = append(motions, m)
		ids = append(ids, m.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(motions) == 0 {
		return motions, nil
	}

	// One pass to tally every motion's ballots, avoiding N+1.
	tallies, err := r.tallyMotions(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range motions {
		t := tallies[motions[i].ID]
		t.Carried = ComputeMotionOutcome(motions[i].Threshold, t.For, t.Against)
		motions[i].Tally = t
	}

	// Attach any conflict-of-interest recusals in one batched pass.
	recusals, err := r.recusalsBySubject(ctx, "motion", ids)
	if err != nil {
		return nil, err
	}
	for i := range motions {
		motions[i].Recusals = recusals[motions[i].ID]
	}
	return motions, nil
}

// recusalsBySubject loads recusals for a set of subject ids in a single query.
func (r *GovernanceRepo) recusalsBySubject(ctx context.Context, subjectType string, ids []string) (map[string][]model.Recusal, error) {
	out := map[string][]model.Recusal{}
	rows, err := r.db.Query(ctx, `
		SELECT rc.subject_id::text, rc.member_id::text, m.display_name, coalesce(rc.reason,''), rc.created_at
		FROM recusals rc JOIN members m ON m.id = rc.member_id
		WHERE rc.subject_type = $1 AND rc.subject_id = ANY($2::uuid[])
		ORDER BY rc.created_at`, subjectType, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sid string
		var rc model.Recusal
		if err := rows.Scan(&sid, &rc.MemberID, &rc.MemberName, &rc.Reason, &rc.CreatedAt); err != nil {
			return nil, err
		}
		out[sid] = append(out[sid], rc)
	}
	return out, rows.Err()
}

// tallyMotions counts ballots for a set of motion ids in a single query.
func (r *GovernanceRepo) tallyMotions(ctx context.Context, ids []string) (map[string]model.MotionTally, error) {
	out := map[string]model.MotionTally{}
	rows, err := r.db.Query(ctx, `
		SELECT motion_id::text, choice, count(*)
		FROM motion_votes
		WHERE motion_id = ANY($1::uuid[])
		GROUP BY motion_id, choice`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, choice string
		var n int
		if err := rows.Scan(&id, &choice, &n); err != nil {
			return nil, err
		}
		t := out[id]
		switch choice {
		case "for":
			t.For = n
		case "against":
			t.Against = n
		case "abstain":
			t.Abstain = n
		}
		t.Total = t.For + t.Against + t.Abstain
		out[id] = t
	}
	return out, rows.Err()
}

// GetMotion returns a single motion with its tally and full ballot list.
func (r *GovernanceRepo) GetMotion(ctx context.Context, id string) (*model.Motion, error) {
	row := r.db.QueryRow(ctx, `
		SELECT mo.id::text, mo.meeting_id::text, mo.title, mo.detail,
		       mo.mover_id::text, mv.display_name,
		       mo.seconder_id::text, sc.display_name,
		       mo.threshold, mo.business, mo.status, mo.plan_id::text, pl.title,
		       mo.created_by::text,
		       mo.opened_at, mo.closed_at, mo.created_at, mo.updated_at
		FROM motions mo
		LEFT JOIN members mv ON mv.id = mo.mover_id
		LEFT JOIN members sc ON sc.id = mo.seconder_id
		LEFT JOIN plans pl ON pl.id = mo.plan_id
		WHERE mo.id = $1::uuid`, id)
	m, err := scanMotion(row)
	if err != nil {
		return nil, err
	}
	m.Votes, err = r.GetVotes(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, v := range m.Votes {
		switch v.Choice {
		case "for":
			m.Tally.For++
		case "against":
			m.Tally.Against++
		case "abstain":
			m.Tally.Abstain++
		}
	}
	m.Tally.Total = m.Tally.For + m.Tally.Against + m.Tally.Abstain
	m.Tally.Carried = ComputeMotionOutcome(m.Threshold, m.Tally.For, m.Tally.Against)
	return &m, nil
}

// CreateMotion inserts a new draft motion. An unset business class defaults to
// new business, matching the column default — callers that predate the field
// (and tests) stay valid.
func (r *GovernanceRepo) CreateMotion(ctx context.Context, m *model.Motion, createdBy string) (*model.Motion, error) {
	if m.Business == "" {
		m.Business = "new"
	}
	var id string
	err := r.db.QueryRow(ctx, `
		INSERT INTO motions (meeting_id, title, detail, mover_id, seconder_id, threshold, business, status, plan_id, created_by)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5::uuid, $6, $7, $8, $9::uuid, $10::uuid)
		RETURNING id::text`,
		m.MeetingID, m.Title, m.Detail, m.MoverID, m.SeconderID, m.Threshold, m.Business, m.Status, m.PlanID, createdBy).Scan(&id)
	if err != nil {
		return nil, err
	}
	return r.GetMotion(ctx, id)
}

// UpdateMotion edits an editable motion's fields (title/detail/threshold/mover/
// seconder). Callers gate this to non-terminal motions.
func (r *GovernanceRepo) UpdateMotion(ctx context.Context, id string, title, detail *string, moverID, seconderID *string, threshold, business, planID *string) (*model.Motion, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE motions SET
			title       = coalesce($1, title),
			detail      = coalesce($2, detail),
			mover_id    = coalesce($3::uuid, mover_id),
			seconder_id = coalesce($4::uuid, seconder_id),
			threshold   = coalesce($5, threshold),
			business    = coalesce($6, business),
			plan_id     = CASE WHEN $7::text IS NULL THEN plan_id ELSE nullif($7::text, '')::uuid END,
			updated_at  = now()
		WHERE id = $8::uuid`,
		title, detail, moverID, seconderID, threshold, business, planID, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, pgx.ErrNoRows
	}
	return r.GetMotion(ctx, id)
}

// SetMotionStatus transitions a motion's status, stamping opened_at when it goes
// to "open" and closed_at when it reaches a terminal state. Returns the motion.
func (r *GovernanceRepo) SetMotionStatus(ctx context.Context, id, status string, seconderID *string) (*model.Motion, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE motions SET
			status      = $1,
			seconder_id = coalesce($2::uuid, seconder_id),
			opened_at   = CASE WHEN $1 = 'open' AND opened_at IS NULL THEN now() ELSE opened_at END,
			closed_at   = CASE WHEN $1 IN ('carried','failed','tabled','withdrawn') THEN now() ELSE closed_at END,
			updated_at  = now()
		WHERE id = $3::uuid`, status, seconderID, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, pgx.ErrNoRows
	}
	return r.GetMotion(ctx, id)
}

// VotesByMeeting returns every ballot for every motion of one meeting in a
// single query, keyed by motion id — the minutes document used to run one
// query per motion.
func (r *GovernanceRepo) VotesByMeeting(ctx context.Context, meetingID string) (map[string][]model.MotionVote, error) {
	rows, err := r.db.Query(ctx, `
		SELECT v.motion_id::text, v.member_id::text, m.display_name, v.choice, v.is_proxy, v.cast_at
		FROM motion_votes v
		JOIN motions mo ON mo.id = v.motion_id
		JOIN members m ON m.id = v.member_id
		WHERE mo.meeting_id = $1::uuid
		ORDER BY m.display_name`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]model.MotionVote{}
	for rows.Next() {
		var mid string
		var v model.MotionVote
		if err := rows.Scan(&mid, &v.MemberID, &v.MemberName, &v.Choice, &v.IsProxy, &v.CastAt); err != nil {
			return nil, err
		}
		out[mid] = append(out[mid], v)
	}
	return out, rows.Err()
}

// DeleteMotion removes a motion (and its ballots via cascade).
func (r *GovernanceRepo) DeleteMotion(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM motions WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// MotionStatus returns just the status and meeting id of a motion, for gating.
func (r *GovernanceRepo) MotionStatus(ctx context.Context, id string) (status, meetingID string, err error) {
	err = r.db.QueryRow(ctx, `SELECT status, meeting_id::text FROM motions WHERE id = $1::uuid`, id).
		Scan(&status, &meetingID)
	return
}

// ---- Votes ----

// CastVote records or replaces a member's ballot on a motion (one per member).
func (r *GovernanceRepo) CastVote(ctx context.Context, motionID, memberID, choice string, isProxy bool, castBy string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO motion_votes (motion_id, member_id, choice, is_proxy, cast_by)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5::uuid)
		ON CONFLICT (motion_id, member_id)
		DO UPDATE SET choice = excluded.choice, is_proxy = excluded.is_proxy,
		              cast_by = excluded.cast_by, cast_at = now()`,
		motionID, memberID, choice, isProxy, castBy)
	return err
}

// MemberIsActive reports whether the member exists and has status 'active'.
// A missing member returns (false, nil).
func (r *GovernanceRepo) MemberIsActive(ctx context.Context, memberID string) (bool, error) {
	var active bool
	err := r.db.QueryRow(ctx, `SELECT status = 'active' FROM members WHERE id = $1::uuid`, memberID).Scan(&active)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return active, err
}

// GetVotes returns the ballots on a motion, ordered by member name.
func (r *GovernanceRepo) GetVotes(ctx context.Context, motionID string) ([]model.MotionVote, error) {
	rows, err := r.db.Query(ctx, `
		SELECT v.member_id::text, m.display_name, v.choice, v.is_proxy, v.cast_at
		FROM motion_votes v
		JOIN members m ON m.id = v.member_id
		WHERE v.motion_id = $1::uuid
		ORDER BY m.display_name`, motionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var votes []model.MotionVote
	for rows.Next() {
		var v model.MotionVote
		if err := rows.Scan(&v.MemberID, &v.MemberName, &v.Choice, &v.IsProxy, &v.CastAt); err != nil {
			return nil, err
		}
		votes = append(votes, v)
	}
	return votes, rows.Err()
}

// ---- Proxies ----

// ListProxies returns the proxy assignments for a meeting.
func (r *GovernanceRepo) ListProxies(ctx context.Context, meetingID string) ([]model.MeetingProxy, error) {
	rows, err := r.db.Query(ctx, `
		SELECT p.id::text, p.meeting_id::text,
		       p.grantor_id::text, g.display_name,
		       p.holder_id::text, h.display_name, p.created_at
		FROM meeting_proxies p
		JOIN members g ON g.id = p.grantor_id
		JOIN members h ON h.id = p.holder_id
		WHERE p.meeting_id = $1::uuid
		ORDER BY g.display_name
		LIMIT 1000`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var proxies []model.MeetingProxy
	for rows.Next() {
		var p model.MeetingProxy
		if err := rows.Scan(&p.ID, &p.MeetingID, &p.GrantorID, &p.GrantorName,
			&p.HolderID, &p.HolderName, &p.CreatedAt); err != nil {
			return nil, err
		}
		proxies = append(proxies, p)
	}
	return proxies, rows.Err()
}

// CreateProxy assigns a proxy for a meeting (one grantor may grant once).
func (r *GovernanceRepo) CreateProxy(ctx context.Context, meetingID, grantorID, holderID string) (*model.MeetingProxy, error) {
	var id string
	err := r.db.QueryRow(ctx, `
		INSERT INTO meeting_proxies (meeting_id, grantor_id, holder_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid)
		RETURNING id::text`, meetingID, grantorID, holderID).Scan(&id)
	if err != nil {
		return nil, err
	}
	var p model.MeetingProxy
	err = r.db.QueryRow(ctx, `
		SELECT p.id::text, p.meeting_id::text,
		       p.grantor_id::text, g.display_name,
		       p.holder_id::text, h.display_name, p.created_at
		FROM meeting_proxies p
		JOIN members g ON g.id = p.grantor_id
		JOIN members h ON h.id = p.holder_id
		WHERE p.id = $1::uuid`, id).
		Scan(&p.ID, &p.MeetingID, &p.GrantorID, &p.GrantorName, &p.HolderID, &p.HolderName, &p.CreatedAt)
	return &p, err
}

// DeleteProxy removes a proxy assignment.
func (r *GovernanceRepo) DeleteProxy(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM meeting_proxies WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ---- Async ballot tokens ----

// BallotRecipient is an eligible async-ballot recipient.
type BallotRecipient struct {
	MemberID string
	Email    string
	Name     string
}

// EligibleBallotMembers returns active members with an email who have not yet
// voted on the motion — the recipients for an emailed ballot round.
func (r *GovernanceRepo) EligibleBallotMembers(ctx context.Context, motionID string) ([]BallotRecipient, error) {
	rows, err := r.db.Query(ctx, `
		SELECT m.id::text, m.email, m.display_name
		FROM members m
		WHERE m.status = 'active' AND m.email IS NOT NULL AND m.email <> ''
		  AND NOT EXISTS (SELECT 1 FROM motion_votes v WHERE v.motion_id = $1::uuid AND v.member_id = m.id)
		ORDER BY m.display_name`, motionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BallotRecipient
	for rows.Next() {
		var b BallotRecipient
		if err := rows.Scan(&b.MemberID, &b.Email, &b.Name); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// UpsertBallotToken stores (or replaces) a member's ballot token for a motion,
// resetting it to unused so a resend supersedes any prior link.
func (r *GovernanceRepo) UpsertBallotToken(ctx context.Context, motionID, memberID, hash string, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO ballot_tokens (motion_id, member_id, token_hash, expires_at, used)
		VALUES ($1::uuid, $2::uuid, $3, $4, FALSE)
		ON CONFLICT (motion_id, member_id)
		DO UPDATE SET token_hash = excluded.token_hash, expires_at = excluded.expires_at, used = FALSE`,
		motionID, memberID, hash, expiresAt)
	return err
}

// GetBallotContext resolves an unused, unexpired ballot token to the motion and
// member it authorizes, without consuming it (for rendering the ballot page).
func (r *GovernanceRepo) GetBallotContext(ctx context.Context, hash string) (*model.BallotContext, error) {
	var b model.BallotContext
	err := r.db.QueryRow(ctx, `
		SELECT mo.id::text, mem.id::text, mem.display_name, mt.title,
		       mo.title, mo.detail, mo.threshold, mo.status
		FROM ballot_tokens t
		JOIN motions mo  ON mo.id = t.motion_id
		JOIN meetings mt ON mt.id = mo.meeting_id
		JOIN members mem ON mem.id = t.member_id
		WHERE t.token_hash = $1 AND t.used = FALSE AND t.expires_at > now()`, hash).
		Scan(&b.MotionID, &b.MemberID, &b.MemberName, &b.MeetingTitle,
			&b.Title, &b.Detail, &b.Threshold, &b.Status)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// ConsumeBallotAndVote atomically, in one transaction: consumes an unused,
// unexpired ballot token and records the member's vote — but only if the motion
// is open. Returns the motion id on success. Returns pgx.ErrNoRows when the token
// is unknown/used/expired, and ErrMotionNotOpen (leaving the token unconsumed)
// when voting has closed. Recording the vote and burning the token now succeed
// or fail together, so a token can never be spent without a vote landing.
func (r *GovernanceRepo) ConsumeBallotAndVote(ctx context.Context, hash, choice string) (motionID string, err error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var memberID, status string
	err = tx.QueryRow(ctx, `
		UPDATE ballot_tokens t SET used = TRUE
		FROM motions mo
		WHERE t.token_hash = $1 AND t.used = FALSE AND t.expires_at > now() AND mo.id = t.motion_id
		RETURNING t.motion_id::text, t.member_id::text, mo.status`, hash).
		Scan(&motionID, &memberID, &status)
	if err != nil {
		return "", err // pgx.ErrNoRows when the token is invalid
	}
	if status != "open" {
		return "", ErrMotionNotOpen // Rollback leaves the token unconsumed
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO motion_votes (motion_id, member_id, choice, is_proxy)
		VALUES ($1::uuid, $2::uuid, $3, FALSE)
		ON CONFLICT (motion_id, member_id)
		DO UPDATE SET choice = excluded.choice, is_proxy = FALSE, cast_at = now()`,
		motionID, memberID, choice); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return motionID, nil
}

// scanMotion scans a motion row (without tally/votes, which are filled by callers).
// MyVotes returns the given member's ballot choice per motion for a meeting,
// so the UI can show a voter what they already chose.
func (r *GovernanceRepo) MyVotes(ctx context.Context, meetingID, memberID string) (map[string]string, error) {
	rows, err := r.db.Query(ctx, `
		SELECT v.motion_id::text, v.choice
		FROM motion_votes v
		JOIN motions mo ON mo.id = v.motion_id
		WHERE mo.meeting_id = $1::uuid AND v.member_id = $2::uuid`, meetingID, memberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var motionID, choice string
		if err := rows.Scan(&motionID, &choice); err != nil {
			return nil, err
		}
		out[motionID] = choice
	}
	return out, rows.Err()
}

// NoteMinutes appends a system-written journal line (best-effort caller side:
// once the minutes are finalized the database trigger refuses the insert, and
// that refusal is the correct outcome — the record is closed).
func (r *GovernanceRepo) NoteMinutes(ctx context.Context, meetingID, body string, motionID *string, recordedBy string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO meeting_minutes_entries (meeting_id, kind, body, motion_id, recorded_by)
		VALUES ($1::uuid, 'note', $2, $3::uuid, $4::uuid)`,
		meetingID, body, motionID, recordedBy)
	return err
}

// RecordMotionOutcome writes the paper trail for a decided motion: a meeting
// decision-log row linked back to the motion (inserted at most once per
// motion), a minutes journal line, and — when the motion is tied to a plan
// and carried — that plan's decision log. Each write is independent and the
// caller treats failures as best-effort: a closed motion stands regardless.
func (r *GovernanceRepo) RecordMotionOutcome(ctx context.Context, m *model.Motion, final string, actorUserID string) error {
	outcome := "failed"
	if final == "carried" {
		outcome = "passed"
	}
	var firstErr error
	_, err := r.db.Exec(ctx, `
		INSERT INTO meeting_decisions (meeting_id, summary, detail, outcome, vote_for, vote_against, vote_abstain, motion_id)
		SELECT $1::uuid, $2, $3, $4, $5, $6, $7, $8::uuid
		WHERE NOT EXISTS (SELECT 1 FROM meeting_decisions WHERE motion_id = $8::uuid)`,
		m.MeetingID, m.Title, m.Detail, outcome,
		m.Tally.For, m.Tally.Against, m.Tally.Abstain, m.ID)
	if err != nil {
		firstErr = err
	}
	body := fmt.Sprintf("Motion %q %s %d–%d–%d (%s required).",
		m.Title, final, m.Tally.For, m.Tally.Against, m.Tally.Abstain, m.Threshold)
	if err := r.NoteMinutes(ctx, m.MeetingID, body, &m.ID, actorUserID); err != nil && firstErr == nil {
		firstErr = err
	}
	if m.PlanID != nil && final == "carried" {
		rationale := fmt.Sprintf("Motion carried %d–%d–%d in the meeting record.",
			m.Tally.For, m.Tally.Against, m.Tally.Abstain)
		if _, err := r.db.Exec(ctx, `
			INSERT INTO plan_decisions (plan_id, summary, rationale, decided_by)
			VALUES ($1::uuid, $2, $3, $4::uuid)`,
			*m.PlanID, "Approved: "+m.Title, rationale, actorUserID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func scanMotion(row scannable) (model.Motion, error) {
	var m model.Motion
	err := row.Scan(&m.ID, &m.MeetingID, &m.Title, &m.Detail,
		&m.MoverID, &m.MoverName, &m.SeconderID, &m.SeconderName,
		&m.Threshold, &m.Business, &m.Status, &m.PlanID, &m.PlanTitle,
		&m.CreatedBy,
		&m.OpenedAt, &m.ClosedAt, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}
