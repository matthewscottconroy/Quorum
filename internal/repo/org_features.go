package repo

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// OrgFeaturesRepo backs the org-maturity constructs that are pure records:
// office terms (with history), committees, and conflict-of-interest recusals.
// None of these grant access — the permission role stays the security boundary.
type OrgFeaturesRepo struct {
	db *pgxpool.Pool
}

// NewOrgFeaturesRepo constructs the repo.
func NewOrgFeaturesRepo(db *pgxpool.Pool) *OrgFeaturesRepo {
	return &OrgFeaturesRepo{db: db}
}

// ---- office terms ----

// AddOfficeTerm records a member taking an office. Closing the previous open
// term of the same title is the caller's job (StartOffice does both).
func (r *OrgFeaturesRepo) AddOfficeTerm(ctx context.Context, memberID, title, startedOn string) (*model.OfficeTerm, error) {
	var id string
	if err := r.db.QueryRow(ctx, `
		INSERT INTO office_terms (member_id, title, started_on)
		VALUES ($1::uuid, $2, coalesce($3::date, current_date))
		RETURNING id::text`, memberID, title, nilIfEmpty(startedOn)).Scan(&id); err != nil {
		return nil, err
	}
	return r.getOfficeTerm(ctx, id)
}

// EndOfficeTerm closes an open term as of a date (defaults today).
func (r *OrgFeaturesRepo) EndOfficeTerm(ctx context.Context, id, endedOn string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE office_terms SET ended_on = coalesce($2::date, current_date)
		WHERE id = $1::uuid AND ended_on IS NULL`, id, nilIfEmpty(endedOn))
	return err
}

func (r *OrgFeaturesRepo) getOfficeTerm(ctx context.Context, id string) (*model.OfficeTerm, error) {
	var t model.OfficeTerm
	err := r.db.QueryRow(ctx, `
		SELECT ot.id::text, ot.member_id::text, m.display_name, ot.title, ot.started_on, ot.ended_on
		FROM office_terms ot JOIN members m ON m.id = ot.member_id WHERE ot.id = $1::uuid`, id).
		Scan(&t.ID, &t.MemberID, &t.MemberName, &t.Title, &t.StartedOn, &t.EndedOn)
	return &t, err
}

// ListOfficeTerms returns terms, current (open) first then most recent.
// current=true limits to open terms (the present office holders).
func (r *OrgFeaturesRepo) ListOfficeTerms(ctx context.Context, current bool) ([]model.OfficeTerm, error) {
	where := ""
	if current {
		where = "WHERE ot.ended_on IS NULL"
	}
	rows, err := r.db.Query(ctx, `
		SELECT ot.id::text, ot.member_id::text, m.display_name, ot.title, ot.started_on, ot.ended_on
		FROM office_terms ot JOIN members m ON m.id = ot.member_id `+where+`
		ORDER BY (ot.ended_on IS NULL) DESC, ot.started_on DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.OfficeTerm{}
	for rows.Next() {
		var t model.OfficeTerm
		if err := rows.Scan(&t.ID, &t.MemberID, &t.MemberName, &t.Title, &t.StartedOn, &t.EndedOn); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---- committees ----

// CreateCommittee makes a committee.
func (r *OrgFeaturesRepo) CreateCommittee(ctx context.Context, name string, purpose, chairID *string) (*model.Committee, error) {
	var id string
	if err := r.db.QueryRow(ctx, `
		INSERT INTO committees (name, purpose, chair_id)
		VALUES ($1, $2, nullif($3,'')::uuid) RETURNING id::text`,
		name, purpose, derefOrEmpty(chairID)).Scan(&id); err != nil {
		return nil, err
	}
	return r.GetCommittee(ctx, id)
}

// GetCommittee returns a committee with its members.
func (r *OrgFeaturesRepo) GetCommittee(ctx context.Context, id string) (*model.Committee, error) {
	var c model.Committee
	err := r.db.QueryRow(ctx, `
		SELECT c.id::text, c.name, c.purpose, c.chair_id::text, coalesce(m.display_name,'')
		FROM committees c LEFT JOIN members m ON m.id = c.chair_id WHERE c.id = $1::uuid`, id).
		Scan(&c.ID, &c.Name, &c.Purpose, &c.ChairID, &c.ChairName)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `
		SELECT cm.member_id::text, m.display_name FROM committee_members cm
		JOIN members m ON m.id = cm.member_id WHERE cm.committee_id = $1::uuid ORDER BY m.display_name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	c.Members = []model.CommitteeMember{}
	for rows.Next() {
		var cm model.CommitteeMember
		if err := rows.Scan(&cm.MemberID, &cm.MemberName); err != nil {
			return nil, err
		}
		c.Members = append(c.Members, cm)
	}
	return &c, rows.Err()
}

// ListCommittees returns all committees (without member rosters).
func (r *OrgFeaturesRepo) ListCommittees(ctx context.Context) ([]model.Committee, error) {
	rows, err := r.db.Query(ctx, `
		SELECT c.id::text, c.name, c.purpose, c.chair_id::text, coalesce(m.display_name,''),
		       (SELECT count(*) FROM committee_members cm WHERE cm.committee_id = c.id)::int
		FROM committees c LEFT JOIN members m ON m.id = c.chair_id ORDER BY c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Committee{}
	for rows.Next() {
		var c model.Committee
		if err := rows.Scan(&c.ID, &c.Name, &c.Purpose, &c.ChairID, &c.ChairName, &c.MemberCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateCommittee changes name/purpose/chair.
func (r *OrgFeaturesRepo) UpdateCommittee(ctx context.Context, id, name string, purpose, chairID *string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE committees SET name = $2, purpose = $3, chair_id = nullif($4,'')::uuid WHERE id = $1::uuid`,
		id, name, purpose, derefOrEmpty(chairID))
	return err
}

// DeleteCommittee removes a committee (members cascade).
func (r *OrgFeaturesRepo) DeleteCommittee(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM committees WHERE id = $1::uuid`, id)
	return err
}

// SetCommitteeMembers replaces the roster.
func (r *OrgFeaturesRepo) SetCommitteeMembers(ctx context.Context, id string, memberIDs []string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `DELETE FROM committee_members WHERE committee_id = $1::uuid`, id); err != nil {
		return err
	}
	for _, mid := range memberIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO committee_members (committee_id, member_id) VALUES ($1::uuid, $2::uuid)
			ON CONFLICT DO NOTHING`, id, mid); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ---- recusals ----

// AddRecusal records a member recusing from a motion or purchase.
func (r *OrgFeaturesRepo) AddRecusal(ctx context.Context, subjectType, subjectID, memberID, reason string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `
		INSERT INTO recusals (subject_type, subject_id, member_id, reason)
		VALUES ($1, $2::uuid, $3::uuid, nullif($4,''))
		ON CONFLICT (subject_type, subject_id, member_id) DO UPDATE SET reason = EXCLUDED.reason`,
		subjectType, subjectID, memberID, reason); err != nil {
		return err
	}
	if subjectType == "purchase" {
		// A recusal changes the approval math (the recused signer is neither
		// counted nor waited on) — and nothing else re-runs it. Without this,
		// a named signer recusing AFTER others approved left the purchase
		// stuck pending forever: further Approve calls hit "already approved"
		// before ever reaching the recount.
		if _, err := tx.Exec(ctx, `
			UPDATE purchase_requests pr SET status = 'approved', decided_at = now()
			FROM funds f
			WHERE pr.id = $1::uuid AND pr.status = 'pending' AND f.id = pr.fund_id
			  AND (SELECT count(*) FROM purchase_approvals pa
			       WHERE pa.request_id = pr.id
			         AND NOT EXISTS (SELECT 1 FROM recusals rc JOIN users u ON u.member_id = rc.member_id
			                         WHERE rc.subject_type = 'purchase' AND rc.subject_id = pr.id
			                           AND u.id = pa.approver_id)) >= f.approvals_required
			  AND NOT EXISTS (
			      SELECT 1 FROM fund_signers fs
			      WHERE fs.fund_id = f.id
			        AND NOT EXISTS (SELECT 1 FROM purchase_approvals pa2
			                        WHERE pa2.request_id = pr.id AND pa2.approver_id = fs.user_id)
			        AND NOT EXISTS (SELECT 1 FROM recusals rc2 JOIN users u2 ON u2.member_id = rc2.member_id
			                        WHERE rc2.subject_type = 'purchase' AND rc2.subject_id = pr.id
			                          AND u2.id = fs.user_id))`,
			subjectID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ListRecusals returns who has recused from a subject.
func (r *OrgFeaturesRepo) ListRecusals(ctx context.Context, subjectType, subjectID string) ([]model.Recusal, error) {
	rows, err := r.db.Query(ctx, `
		SELECT rc.member_id::text, m.display_name, coalesce(rc.reason,''), rc.created_at
		FROM recusals rc JOIN members m ON m.id = rc.member_id
		WHERE rc.subject_type = $1 AND rc.subject_id = $2::uuid ORDER BY rc.created_at`,
		subjectType, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Recusal{}
	for rows.Next() {
		var rc model.Recusal
		if err := rows.Scan(&rc.MemberID, &rc.MemberName, &rc.Reason, &rc.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, rc)
	}
	return out, rows.Err()
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---- join requests ----

// CreateJoinRequest files a public membership application.
func (r *OrgFeaturesRepo) CreateJoinRequest(ctx context.Context, name, email, message string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO join_requests (name, email, message) VALUES ($1, $2, nullif($3,''))`,
		name, email, message)
	return err
}

// ListJoinRequests returns applications by status (default pending).
func (r *OrgFeaturesRepo) ListJoinRequests(ctx context.Context, status string, limit int) ([]model.JoinRequest, error) {
	if status == "" {
		status = "pending"
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.Query(ctx, `
		SELECT id::text, name, email, coalesce(message,''), status, created_at
		FROM join_requests WHERE status = $1 ORDER BY created_at LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.JoinRequest{}
	for rows.Next() {
		var j model.JoinRequest
		if err := rows.Scan(&j.ID, &j.Name, &j.Email, &j.Message, &j.Status, &j.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// PendingJoinCount is the queue depth for the dashboard/nav.
func (r *OrgFeaturesRepo) PendingJoinCount(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT count(*) FROM join_requests WHERE status = 'pending'`).Scan(&n)
	return n, err
}

// ApproveJoinRequest creates a member from a pending application and marks it
// approved, atomically. Returns the new member id.
func (r *OrgFeaturesRepo) ApproveJoinRequest(ctx context.Context, id, tier, resolvedBy string) (memberID string, err error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var name, email string
	if err := tx.QueryRow(ctx,
		`SELECT name, email FROM join_requests WHERE id = $1::uuid AND status = 'pending' FOR UPDATE`, id).
		Scan(&name, &email); err != nil {
		return "", err
	}
	if tier == "" {
		tier = "standard"
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO members (display_name, email, tier, status)
		VALUES ($1, nullif($2,''), $3, 'active') RETURNING id::text`, name, email, tier).Scan(&memberID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE join_requests SET status = 'approved', resolved_at = now(),
		    resolved_by = $2::uuid, member_id = $3::uuid WHERE id = $1::uuid`,
		id, resolvedBy, memberID); err != nil {
		return "", err
	}
	return memberID, tx.Commit(ctx)
}

// RejectJoinRequest marks an application rejected.
func (r *OrgFeaturesRepo) RejectJoinRequest(ctx context.Context, id, resolvedBy string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE join_requests SET status = 'rejected', resolved_at = now(), resolved_by = $2::uuid
		WHERE id = $1::uuid AND status = 'pending'`, id, resolvedBy)
	return err
}
