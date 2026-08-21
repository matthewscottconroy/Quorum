package repo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// PurchasesRepo runs the multi-signature purchase workflow. State changes
// and postings share one transaction; the purchase_guard trigger freezes
// terminal rows regardless of what this code does.
type PurchasesRepo struct {
	db *pgxpool.Pool
}

// NewPurchasesRepo constructs the repo.
func NewPurchasesRepo(db *pgxpool.Pool) *PurchasesRepo {
	return &PurchasesRepo{db: db}
}

const purchaseSelect = `
	SELECT pr.id::text, pr.fund_id::text, f.name, pr.requester_id::text,
	       coalesce(m.display_name, u.email), pr.amount, pr.currency, pr.payee, pr.memo,
	       pr.resource_id::text, pr.status, pr.journal_entry_id::text,
	       pr.decided_at, pr.completed_at, pr.created_at, f.approvals_required
	FROM purchase_requests pr
	JOIN funds f ON f.id = pr.fund_id
	LEFT JOIN users u ON u.id = pr.requester_id
	LEFT JOIN members m ON m.id = u.member_id`

func scanPurchase(row scannable) (model.PurchaseRequest, error) {
	var p model.PurchaseRequest
	err := row.Scan(&p.ID, &p.FundID, &p.FundName, &p.RequesterID, &p.RequesterName,
		&p.Amount, &p.Currency, &p.Payee, &p.Memo, &p.ResourceID, &p.Status,
		&p.JournalEntryID, &p.DecidedAt, &p.CompletedAt, &p.CreatedAt, &p.ApprovalsRequired)
	return p, err
}

// Create files a purchase request (status pending).
func (r *PurchasesRepo) Create(ctx context.Context, p *model.PurchaseRequest, requester string) (*model.PurchaseRequest, error) {
	var id string
	err := r.db.QueryRow(ctx, `
		INSERT INTO purchase_requests (fund_id, requester_id, amount, currency, payee, memo, resource_id, expense_account_id)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7::uuid, $8::uuid) RETURNING id::text`,
		p.FundID, requester, p.Amount, p.Currency, p.Payee, p.Memo, p.ResourceID, p.ExpenseAccountID).Scan(&id)
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// Get returns one request with its approval evidence and outstanding
// requirements.
func (r *PurchasesRepo) Get(ctx context.Context, id string) (*model.PurchaseRequest, error) {
	p, err := scanPurchase(r.db.QueryRow(ctx, purchaseSelect+` WHERE pr.id = $1::uuid`, id))
	if err != nil {
		return nil, err
	}
	if err := r.loadEvidence(ctx, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *PurchasesRepo) loadEvidence(ctx context.Context, p *model.PurchaseRequest) error {
	rows, err := r.db.Query(ctx, `
		SELECT pa.approver_id::text, coalesce(m.display_name, u.email, 'former user'), pa.ip, pa.approved_at
		FROM purchase_approvals pa
		LEFT JOIN users u ON u.id = pa.approver_id
		LEFT JOIN members m ON m.id = u.member_id
		WHERE pa.request_id = $1::uuid ORDER BY pa.approved_at`, p.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a model.PurchaseApproval
		if err := rows.Scan(&a.ApproverID, &a.ApproverName, &a.IP, &a.ApprovedAt); err != nil {
			return err
		}
		p.Approvals = append(p.Approvals, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Named signers still missing.
	mrows, err := r.db.Query(ctx, `
		SELECT coalesce(m.display_name, u.email)
		FROM fund_signers fs
		JOIN users u ON u.id = fs.user_id
		LEFT JOIN members m ON m.id = u.member_id
		WHERE fs.fund_id = $1::uuid
		  AND NOT EXISTS (SELECT 1 FROM purchase_approvals pa
		                  WHERE pa.request_id = $2::uuid AND pa.approver_id = fs.user_id)
		  AND NOT EXISTS (SELECT 1 FROM recusals rc JOIN users u2 ON u2.member_id = rc.member_id
		                  WHERE rc.subject_type = 'purchase' AND rc.subject_id = $2::uuid AND u2.id = fs.user_id)
		ORDER BY 1`, p.FundID, p.ID)
	if err != nil {
		return err
	}
	defer mrows.Close()
	for mrows.Next() {
		var name string
		if err := mrows.Scan(&name); err != nil {
			return err
		}
		p.MissingSigners = append(p.MissingSigners, name)
	}
	if err := mrows.Err(); err != nil {
		return err
	}
	// Conflict-of-interest recusals recorded against this purchase.
	rrows, err := r.db.Query(ctx, `
		SELECT rc.member_id::text, m.display_name, coalesce(rc.reason,''), rc.created_at
		FROM recusals rc JOIN members m ON m.id = rc.member_id
		WHERE rc.subject_type = 'purchase' AND rc.subject_id = $1::uuid
		ORDER BY rc.created_at`, p.ID)
	if err != nil {
		return err
	}
	defer rrows.Close()
	for rrows.Next() {
		var rc model.Recusal
		if err := rrows.Scan(&rc.MemberID, &rc.MemberName, &rc.Reason, &rc.CreatedAt); err != nil {
			return err
		}
		p.Recusals = append(p.Recusals, rc)
	}
	return rrows.Err()
}

// List returns requests, optionally filtered by fund and/or status.
func (r *PurchasesRepo) List(ctx context.Context, fundID, status string, limit int) ([]model.PurchaseRequest, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := purchaseSelect
	args := []any{}
	conds := ""
	idx := 1
	if fundID != "" {
		conds += fmt.Sprintf(" WHERE pr.fund_id = $%d::uuid", idx)
		args = append(args, fundID)
		idx++
	}
	if status != "" {
		if conds == "" {
			conds = " WHERE "
		} else {
			conds += " AND "
		}
		conds += fmt.Sprintf("pr.status = $%d", idx)
		args = append(args, status)
		idx++
	}
	args = append(args, limit)
	rows, err := r.db.Query(ctx, q+conds+fmt.Sprintf(" ORDER BY pr.created_at DESC LIMIT $%d", idx), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PurchaseRequest
	for rows.Next() {
		p, err := scanPurchase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := r.loadEvidenceBatch(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// loadEvidenceBatch fills approvals, missing signers, and recusals for a
// whole page in three set-based queries — the per-row loadEvidence gave the
// funds page ~3N queries for N purchases.
func (r *PurchasesRepo) loadEvidenceBatch(ctx context.Context, out []model.PurchaseRequest) error {
	if len(out) == 0 {
		return nil
	}
	ids := make([]string, len(out))
	byID := make(map[string]*model.PurchaseRequest, len(out))
	for i := range out {
		ids[i] = out[i].ID
		byID[out[i].ID] = &out[i]
	}
	arows, err := r.db.Query(ctx, `
		SELECT pa.request_id::text, pa.approver_id::text,
		       coalesce(m.display_name, u.email, 'former user'), pa.ip, pa.approved_at
		FROM purchase_approvals pa
		LEFT JOIN users u ON u.id = pa.approver_id
		LEFT JOIN members m ON m.id = u.member_id
		WHERE pa.request_id = ANY($1::uuid[]) ORDER BY pa.approved_at`, ids)
	if err != nil {
		return err
	}
	for arows.Next() {
		var rid string
		var a model.PurchaseApproval
		if err := arows.Scan(&rid, &a.ApproverID, &a.ApproverName, &a.IP, &a.ApprovedAt); err != nil {
			arows.Close()
			return err
		}
		if p := byID[rid]; p != nil {
			p.Approvals = append(p.Approvals, a)
		}
	}
	if err := arows.Err(); err != nil {
		return err
	}
	mrows, err := r.db.Query(ctx, `
		SELECT pr.id::text, coalesce(m.display_name, u.email)
		FROM purchase_requests pr
		JOIN fund_signers fs ON fs.fund_id = pr.fund_id
		JOIN users u ON u.id = fs.user_id
		LEFT JOIN members m ON m.id = u.member_id
		WHERE pr.id = ANY($1::uuid[])
		  AND NOT EXISTS (SELECT 1 FROM purchase_approvals pa
		                  WHERE pa.request_id = pr.id AND pa.approver_id = fs.user_id)
		  AND NOT EXISTS (SELECT 1 FROM recusals rc JOIN users u2 ON u2.member_id = rc.member_id
		                  WHERE rc.subject_type = 'purchase' AND rc.subject_id = pr.id AND u2.id = fs.user_id)
		ORDER BY 2`, ids)
	if err != nil {
		return err
	}
	for mrows.Next() {
		var rid, name string
		if err := mrows.Scan(&rid, &name); err != nil {
			mrows.Close()
			return err
		}
		if p := byID[rid]; p != nil {
			p.MissingSigners = append(p.MissingSigners, name)
		}
	}
	if err := mrows.Err(); err != nil {
		return err
	}
	rrows, err := r.db.Query(ctx, `
		SELECT rc.subject_id::text, rc.member_id::text, m.display_name, coalesce(rc.reason,''), rc.created_at
		FROM recusals rc JOIN members m ON m.id = rc.member_id
		WHERE rc.subject_type = 'purchase' AND rc.subject_id = ANY($1::uuid[])
		ORDER BY rc.created_at`, ids)
	if err != nil {
		return err
	}
	for rrows.Next() {
		var rid string
		var rc model.Recusal
		if err := rrows.Scan(&rid, &rc.MemberID, &rc.MemberName, &rc.Reason, &rc.CreatedAt); err != nil {
			rrows.Close()
			return err
		}
		if p := byID[rid]; p != nil {
			p.Recusals = append(p.Recusals, rc)
		}
	}
	return rrows.Err()
}

// IsSigner reports whether the user is a named signer for the fund.
func (r *PurchasesRepo) IsSigner(ctx context.Context, fundID, userID string) (bool, error) {
	var ok bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM fund_signers WHERE fund_id = $1::uuid AND user_id = $2::uuid)`,
		fundID, userID).Scan(&ok)
	return ok, err
}

// Approve records one person's approval and promotes the request to
// 'approved' when the fund's policy is satisfied: at least
// approvals_required distinct approvals AND every named signer present.
func (r *PurchasesRepo) Approve(ctx context.Context, requestID, approverID, ip string) (*model.PurchaseRequest, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var status, fundID string
	var required int
	if err := tx.QueryRow(ctx, `
		SELECT pr.status, pr.fund_id::text, f.approvals_required
		FROM purchase_requests pr JOIN funds f ON f.id = pr.fund_id
		WHERE pr.id = $1::uuid FOR UPDATE OF pr, f`, requestID).Scan(&status, &fundID, &required); err != nil {
		// (Locking f as well as pr: approvals_required must be stable against
		// a concurrent UpdatePolicy, or an approval could count toward — or
		// promote at — a bar the org just abolished.)
		return nil, err
	}
	if status != "pending" {
		return nil, ErrNotApprovable
	}
	// A recorded conflict-of-interest recusal is binding, not decorative:
	// the recused signer's ink must not count toward the bar.
	var recused bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM recusals rc JOIN users u ON u.member_id = rc.member_id
		               WHERE rc.subject_type = 'purchase' AND rc.subject_id = $1::uuid AND u.id = $2::uuid)`,
		requestID, approverID).Scan(&recused); err != nil {
		return nil, err
	}
	if recused {
		return nil, ErrRecusedApprover
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO purchase_approvals (request_id, approver_id, ip)
		VALUES ($1::uuid, $2::uuid, $3) ON CONFLICT DO NOTHING`, requestID, approverID, ip)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrAlreadyApproved
	}

	var have, missingNamed int
	// Recused ink never counts toward the bar — approve-then-recuse must
	// subtract, matching the missing-signers exclusion below.
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM purchase_approvals pa
		WHERE pa.request_id = $1::uuid
		  AND NOT EXISTS (SELECT 1 FROM recusals rc JOIN users u ON u.member_id = rc.member_id
		                  WHERE rc.subject_type = 'purchase' AND rc.subject_id = $1::uuid
		                    AND u.id = pa.approver_id)`, requestID).Scan(&have); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM fund_signers fs
		WHERE fs.fund_id = $1::uuid
		  AND NOT EXISTS (SELECT 1 FROM purchase_approvals pa
		                  WHERE pa.request_id = $2::uuid AND pa.approver_id = fs.user_id)
		  AND NOT EXISTS (SELECT 1 FROM recusals rc JOIN users u2 ON u2.member_id = rc.member_id
		                  WHERE rc.subject_type = 'purchase' AND rc.subject_id = $2::uuid AND u2.id = fs.user_id)`,
		fundID, requestID).Scan(&missingNamed); err != nil {
		return nil, err
	}
	if have >= required && missingNamed == 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE purchase_requests SET status = 'approved', decided_at = now()
			WHERE id = $1::uuid`, requestID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.Get(ctx, requestID)
}

// SetTerminal moves a pending/approved request to rejected or cancelled.
func (r *PurchasesRepo) SetTerminal(ctx context.Context, requestID, newStatus string) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE purchase_requests SET status = $1, decided_at = coalesce(decided_at, now())
		WHERE id = $2::uuid AND status IN ('pending', 'approved')`, newStatus, requestID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// Complete executes an approved purchase: refuses overdraft, posts
// DR General Expenses / CR fund cash, and freezes the request — all in one
// transaction.
func (r *PurchasesRepo) Complete(ctx context.Context, requestID string) (*model.PurchaseRequest, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var status, fundID, fundAcct, currency, payee string
	var amount int64
	if err := tx.QueryRow(ctx, `
		SELECT pr.status, pr.fund_id::text, f.cash_account_id::text, pr.currency, pr.payee, pr.amount
		FROM purchase_requests pr JOIN funds f ON f.id = pr.fund_id
		WHERE pr.id = $1::uuid FOR UPDATE OF pr, f`, requestID).
		Scan(&status, &fundID, &fundAcct, &currency, &payee, &amount); err != nil {
		return nil, err
	}
	if status != "approved" {
		return nil, fmt.Errorf("%w: status is %s, needs approved", ErrNotApprovable, status)
	}
	var bal int64
	if err := tx.QueryRow(ctx, `SELECT gl_balance($1::uuid, $2)`, fundAcct, currency).Scan(&bal); err != nil {
		return nil, err
	}
	if bal < amount {
		return nil, fmt.Errorf("%w: fund holds %d %s, purchase needs %d", ErrInsufficientFunds, bal, currency, amount)
	}
	var entryID string
	if err := tx.QueryRow(ctx, `
		SELECT gl_post(current_date, $1, 'purchase', $2::uuid,
		       coalesce((SELECT expense_account_id FROM purchase_requests WHERE id = $2::uuid), gl_rule('expense.default')),
		       $3::uuid, $4, $5)::text`,
		"Purchase: "+payee, requestID, fundAcct, amount, currency).Scan(&entryID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE purchase_requests SET status = 'completed', journal_entry_id = $1::uuid, completed_at = now()
		WHERE id = $2::uuid`, entryID, requestID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.Get(ctx, requestID)
}
