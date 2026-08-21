package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// Errors surfaced to handlers as domain conditions (mapped to 409).
var (
	ErrInsufficientFunds = errors.New("insufficient fund balance")
	ErrNotApprovable     = errors.New("request is not in an approvable state")
	ErrAlreadyApproved   = errors.New("you have already approved this request")
	// ErrRecusedApprover refuses a signature from someone who filed a
	// conflict-of-interest recusal on this purchase.
	ErrRecusedApprover = errors.New("you recused from this purchase")
)

// FundsRepo manages purpose-restricted funds and the purchase workflow
// (Phase B). Every money movement goes through gl_post inside the same
// transaction as its state change.
type FundsRepo struct {
	db *pgxpool.Pool
}

// NewFundsRepo constructs the repo.
func NewFundsRepo(db *pgxpool.Pool) *FundsRepo {
	return &FundsRepo{db: db}
}

// CreateFund makes the fund plus its dedicated GL cash account atomically.
func (r *FundsRepo) CreateFund(ctx context.Context, name string, purpose *string, approvalsRequired int, signerIDs []string, createdBy string) (*model.Fund, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var acctID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO accounts (code, name, type)
		VALUES (gl_next_fund_code(), left('Cash - Fund: ' || $1, 80), 'asset')
		RETURNING id::text`, name).Scan(&acctID); err != nil {
		return nil, err
	}
	var fundID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO funds (name, purpose, cash_account_id, approvals_required, created_by)
		VALUES ($1, $2, $3::uuid, $4, $5::uuid) RETURNING id::text`,
		name, purpose, acctID, approvalsRequired, createdBy).Scan(&fundID); err != nil {
		return nil, err
	}
	for _, uid := range signerIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO fund_signers (fund_id, user_id) VALUES ($1::uuid, $2::uuid)
			ON CONFLICT DO NOTHING`, fundID, uid); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.GetFund(ctx, fundID)
}

func (r *FundsRepo) fundQuery(ctx context.Context, where string, args ...any) ([]model.Fund, error) {
	rows, err := r.db.Query(ctx, `
		SELECT f.id::text, f.name, f.purpose, a.code, f.approvals_required, f.active,
		       f.created_at,
		       (SELECT count(*) FROM purchase_requests pr
		        WHERE pr.fund_id = f.id AND pr.status IN ('pending','approved'))::int
		FROM funds f JOIN accounts a ON a.id = f.cash_account_id `+where+`
		ORDER BY lower(f.name)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Fund
	for rows.Next() {
		var f model.Fund
		if err := rows.Scan(&f.ID, &f.Name, &f.Purpose, &f.CashAccountCode,
			&f.ApprovalsRequired, &f.Active, &f.CreatedAt, &f.OpenRequests); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// decorate loads signers and per-currency balances for a set of funds in two
// set-based queries (not per-fund), then groups the rows in Go.
func (r *FundsRepo) decorate(ctx context.Context, funds []model.Fund) error {
	if len(funds) == 0 {
		return nil
	}
	ids := make([]string, len(funds))
	byID := make(map[string]*model.Fund, len(funds))
	for i := range funds {
		ids[i] = funds[i].ID
		byID[funds[i].ID] = &funds[i]
	}

	srows, err := r.db.Query(ctx, `
		SELECT fs.fund_id::text, fs.user_id::text, coalesce(m.display_name, u.email)
		FROM fund_signers fs
		JOIN users u ON u.id = fs.user_id
		LEFT JOIN members m ON m.id = u.member_id
		WHERE fs.fund_id = ANY($1::uuid[]) ORDER BY 3`, ids)
	if err != nil {
		return err
	}
	for srows.Next() {
		var fundID string
		var s model.FundSigner
		if err := srows.Scan(&fundID, &s.UserID, &s.Name); err != nil {
			srows.Close()
			return err
		}
		if f := byID[fundID]; f != nil {
			f.Signers = append(f.Signers, s)
		}
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return err
	}

	brows, err := r.db.Query(ctx, `
		SELECT fu.id::text, l.currency, sum(l.debit) - sum(l.credit)
		FROM journal_lines l
		JOIN funds fu ON fu.cash_account_id = l.account_id
		WHERE fu.id = ANY($1::uuid[])
		GROUP BY fu.id, l.currency ORDER BY l.currency`, ids)
	if err != nil {
		return err
	}
	for brows.Next() {
		var fundID string
		var b model.FundBalance
		if err := brows.Scan(&fundID, &b.Currency, &b.Balance); err != nil {
			brows.Close()
			return err
		}
		if f := byID[fundID]; f != nil {
			f.Balances = append(f.Balances, b)
		}
	}
	brows.Close()
	return brows.Err()
}

// ListFunds returns all funds with signers and derived balances.
func (r *FundsRepo) ListFunds(ctx context.Context) ([]model.Fund, error) {
	funds, err := r.fundQuery(ctx, "")
	if err != nil {
		return nil, err
	}
	if err := r.decorate(ctx, funds); err != nil {
		return nil, err
	}
	return funds, nil
}

// GetFund returns one fund with signers and balances, or pgx.ErrNoRows.
func (r *FundsRepo) GetFund(ctx context.Context, id string) (*model.Fund, error) {
	funds, err := r.fundQuery(ctx, "WHERE f.id = $1::uuid", id)
	if err != nil {
		return nil, err
	}
	if len(funds) == 0 {
		return nil, pgx.ErrNoRows
	}
	if err := r.decorate(ctx, funds); err != nil {
		return nil, err
	}
	return &funds[0], nil
}

// UpdatePolicy changes a fund's policy; any change to the approval bar or
// the signer set voids in-flight approvals on pending requests (they must
// be re-collected under the new policy). Returns how many were voided.
func (r *FundsRepo) UpdatePolicy(ctx context.Context, id string, purpose *string, approvalsRequired *int, signerIDs *[]string) (int, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the fund row first. This does three jobs at once: a nonexistent
	// fund surfaces as ErrNoRows (a 404, not a downstream 500); a Complete
	// in flight (which locks pr AND f) serializes against us so we can never
	// delete the approval evidence of a purchase mid-completion; and every
	// later statement in this transaction sees a stable policy.
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM funds WHERE id = $1::uuid FOR UPDATE`, id).Scan(&one); err != nil {
		return 0, err
	}

	policyChanged := false
	if purpose != nil {
		if _, err := tx.Exec(ctx, `UPDATE funds SET purpose = $1 WHERE id = $2::uuid`, *purpose, id); err != nil {
			return 0, err
		}
	}
	if approvalsRequired != nil {
		tag, err := tx.Exec(ctx, `
			UPDATE funds SET approvals_required = $1
			WHERE id = $2::uuid AND approvals_required <> $1`, *approvalsRequired, id)
		if err != nil {
			return 0, err
		}
		policyChanged = policyChanged || tag.RowsAffected() > 0
	}
	if signerIDs != nil {
		// Diff before declaring a policy change: the UI sends the full
		// signer list on every fund edit, and an UNCHANGED roster must not
		// void every approval collected so far.
		var same bool
		if err := tx.QueryRow(ctx, `
			SELECT NOT EXISTS (
			    SELECT user_id FROM fund_signers WHERE fund_id = $1::uuid
			    EXCEPT SELECT unnest($2::uuid[])
			) AND NOT EXISTS (
			    SELECT unnest($2::uuid[])
			    EXCEPT SELECT user_id FROM fund_signers WHERE fund_id = $1::uuid
			)`, id, *signerIDs).Scan(&same); err != nil {
			return 0, err
		}
		if !same {
			if _, err := tx.Exec(ctx, `DELETE FROM fund_signers WHERE fund_id = $1::uuid`, id); err != nil {
				return 0, err
			}
			for _, uid := range *signerIDs {
				if _, err := tx.Exec(ctx, `
					INSERT INTO fund_signers (fund_id, user_id) VALUES ($1::uuid, $2::uuid)
					ON CONFLICT DO NOTHING`, id, uid); err != nil {
					return 0, err
				}
			}
			policyChanged = true
		}
	}

	voided := 0
	if policyChanged {
		// Approvals gathered under the OLD policy don't carry: void them on
		// every request that hasn't actually spent money yet — including
		// 'approved' ones, which would otherwise stay completable at the old,
		// weaker bar after the org raised it.
		tag, err := tx.Exec(ctx, `
			DELETE FROM purchase_approvals pa USING purchase_requests pr
			WHERE pa.request_id = pr.id AND pr.fund_id = $1::uuid AND pr.status IN ('pending','approved')`, id)
		if err != nil {
			return 0, err
		}
		voided = int(tag.RowsAffected())
		if _, err := tx.Exec(ctx, `
			UPDATE purchase_requests SET status = 'pending', decided_at = NULL
			WHERE fund_id = $1::uuid AND status = 'approved'`, id); err != nil {
			return 0, err
		}
	}
	return voided, tx.Commit(ctx)
}

// Transfer moves money between the operating account and a fund
// ('in' funds the fund, 'out' returns money). Outbound transfers cannot
// overdraw the fund; inbound cannot overdraw operating cash.
func (r *FundsRepo) Transfer(ctx context.Context, fundID, direction string, amount int64, currency, memo string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var fundAcct string
	if err := tx.QueryRow(ctx,
		`SELECT cash_account_id::text FROM funds WHERE id = $1::uuid FOR UPDATE`, fundID).Scan(&fundAcct); err != nil {
		return err
	}
	var srcAcct string
	if direction == "in" {
		// Serialize operating-cash draws (transfers into different funds lock
		// different fund rows, so without this two inbound transfers could both
		// pass the operating-balance check and overdraw it).
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('quorum.cash.operating'))`); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT gl_rule('cash.operating')::text`).Scan(&srcAcct); err != nil {
			return err
		}
		var opBal int64
		if err := tx.QueryRow(ctx, `SELECT gl_balance(gl_rule('cash.operating'), $1)`, currency).Scan(&opBal); err != nil {
			return err
		}
		if opBal < amount {
			return fmt.Errorf("%w: operating cash has %d %s", ErrInsufficientFunds, opBal, currency)
		}
		_, err = tx.Exec(ctx, `SELECT gl_post(current_date, $1, 'fund_transfer', $2::uuid, $3::uuid, gl_rule('cash.operating'), $4, $5)`,
			memo, fundID, fundAcct, amount, currency)
	} else {
		var bal int64
		if err := tx.QueryRow(ctx, `SELECT gl_balance($1::uuid, $2)`, fundAcct, currency).Scan(&bal); err != nil {
			return err
		}
		if bal < amount {
			return fmt.Errorf("%w: fund has %d %s", ErrInsufficientFunds, bal, currency)
		}
		_, err = tx.Exec(ctx, `SELECT gl_post(current_date, $1, 'fund_transfer', $2::uuid, gl_rule('cash.operating'), $3::uuid, $4, $5)`,
			memo, fundID, fundAcct, amount, currency)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
