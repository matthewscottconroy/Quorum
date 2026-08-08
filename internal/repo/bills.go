package repo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// BillsRepo runs vendor bills (accounts payable): accrue on creation, pay
// or void later — each step posting balanced journal entries in the same
// transaction. Cash-basis presentation is naturally unaffected until
// payment (the accrual entry touches no cash account).
type BillsRepo struct {
	db *pgxpool.Pool
}

// NewBillsRepo constructs the repo.
func NewBillsRepo(db *pgxpool.Pool) *BillsRepo {
	return &BillsRepo{db: db}
}

const billSelect = `
	SELECT b.id::text, b.contact_id::text, c.name, b.amount, b.currency,
	       b.memo, b.expense_account_id::text, a.code, a.name,
	       b.bill_date, b.due_date, b.status, b.paid_at, b.created_at
	FROM bills b
	JOIN contacts c ON c.id = b.contact_id
	JOIN accounts a ON a.id = b.expense_account_id`

func scanBill(row scannable) (model.Bill, error) {
	var b model.Bill
	err := row.Scan(&b.ID, &b.ContactID, &b.ContactName, &b.Amount, &b.Currency,
		&b.Memo, &b.ExpenseAccountID, &b.ExpenseAccountCode, &b.ExpenseAccountName,
		&b.BillDate, &b.DueDate, &b.Status, &b.PaidAt, &b.CreatedAt)
	return b, err
}

// Create inserts the bill and posts its accrual (DR expense / CR A/P).
func (r *BillsRepo) Create(ctx context.Context, b *model.Bill, createdBy string) (*model.Bill, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO bills (contact_id, amount, currency, memo, expense_account_id, bill_date, due_date, created_by)
		VALUES ($1::uuid, $2, $3, $4, $5::uuid, coalesce($6::date, current_date), $7::date, $8::uuid)
		RETURNING id::text`,
		b.ContactID, b.Amount, b.Currency, b.Memo, b.ExpenseAccountID,
		nilIfEmpty(b.BillDateStr), nilIfEmpty(b.DueDateStr), createdBy).Scan(&id); err != nil {
		return nil, err
	}
	var eid string
	if err := tx.QueryRow(ctx, `
		SELECT gl_post((SELECT bill_date FROM bills WHERE id = $1::uuid), $2, 'bill', $1::uuid,
		       $3::uuid, gl_rule('payable'), $4, $5)::text`,
		id, "Bill: "+billMemoLabel(b), b.ExpenseAccountID, b.Amount, b.Currency).Scan(&eid); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE bills SET accrual_entry_id = $1::uuid WHERE id = $2::uuid`, eid, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

func billMemoLabel(b *model.Bill) string {
	if b.Memo != nil && *b.Memo != "" {
		return *b.Memo
	}
	return "vendor invoice"
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Get returns one bill, or pgx.ErrNoRows.
func (r *BillsRepo) Get(ctx context.Context, id string) (*model.Bill, error) {
	b, err := scanBill(r.db.QueryRow(ctx, billSelect+` WHERE b.id = $1::uuid`, id))
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// List returns bills, optionally by status, newest first.
func (r *BillsRepo) List(ctx context.Context, status string, limit int) ([]model.Bill, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := billSelect
	args := []any{}
	if status != "" {
		q += ` WHERE b.status = $1`
		args = append(args, status)
	}
	args = append(args, limit)
	rows, err := r.db.Query(ctx, q+fmt.Sprintf(` ORDER BY b.created_at DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Bill
	for rows.Next() {
		b, err := scanBill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Pay settles an open bill: DR A/P / CR cash. The cash side comes from a
// fund (balance-guarded) or from the provider-routed operating accounts.
func (r *BillsRepo) Pay(ctx context.Context, id, fundID, provider string) (*model.Bill, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var status, currency string
	var amount int64
	if err := tx.QueryRow(ctx,
		`SELECT status, currency, amount FROM bills WHERE id = $1::uuid FOR UPDATE`, id).
		Scan(&status, &currency, &amount); err != nil {
		return nil, err
	}
	if status != "open" {
		return nil, fmt.Errorf("%w: bill is %s", ErrNotApprovable, status)
	}

	var cashAcct string
	if fundID != "" {
		if err := tx.QueryRow(ctx,
			`SELECT cash_account_id::text FROM funds WHERE id = $1::uuid`, fundID).Scan(&cashAcct); err != nil {
			return nil, err
		}
		var bal int64
		if err := tx.QueryRow(ctx, `SELECT gl_balance($1::uuid, $2)`, cashAcct, currency).Scan(&bal); err != nil {
			return nil, err
		}
		if bal < amount {
			return nil, fmt.Errorf("%w: fund holds %d %s", ErrInsufficientFunds, bal, currency)
		}
	} else {
		if err := tx.QueryRow(ctx, `SELECT gl_cash_account($1)::text`, provider).Scan(&cashAcct); err != nil {
			return nil, err
		}
	}
	var eid string
	if err := tx.QueryRow(ctx, `
		SELECT gl_post(current_date, 'Bill payment', 'bill_payment', $1::uuid,
		       gl_rule('payable'), $2::uuid, $3, $4)::text`,
		id, cashAcct, amount, currency).Scan(&eid); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bills SET status = 'paid', payment_entry_id = $1::uuid, paid_at = now()
		WHERE id = $2::uuid`, eid, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// Void reverses an open bill's accrual and closes it.
func (r *BillsRepo) Void(ctx context.Context, id string) (*model.Bill, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var status, currency string
	var amount int64
	var expenseAcct string
	if err := tx.QueryRow(ctx, `
		SELECT status, currency, amount, expense_account_id::text
		FROM bills WHERE id = $1::uuid FOR UPDATE`, id).
		Scan(&status, &currency, &amount, &expenseAcct); err != nil {
		return nil, err
	}
	if status != "open" {
		return nil, fmt.Errorf("%w: bill is %s", ErrNotApprovable, status)
	}
	if _, err := tx.Exec(ctx, `
		SELECT gl_post(current_date, 'Bill voided', 'bill_void', $1::uuid,
		       gl_rule('payable'), $2::uuid, $3, $4)`,
		id, expenseAcct, amount, currency); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE bills SET status = 'void' WHERE id = $1::uuid`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// APAging buckets open bills by days overdue as of a date.
func (r *BillsRepo) APAging(ctx context.Context, asOf string) ([]model.ARAgingRow, error) {
	rows, err := r.db.Query(ctx, `
		SELECT b.currency,
		       CASE WHEN b.due_date IS NULL OR b.due_date >= $1::date THEN 'current'
		            WHEN b.due_date >= $1::date - 30 THEN '1-30'
		            WHEN b.due_date >= $1::date - 60 THEN '31-60'
		            WHEN b.due_date >= $1::date - 90 THEN '61-90'
		            ELSE '90+' END,
		       count(*), sum(b.amount)
		FROM bills b WHERE b.status = 'open'
		GROUP BY 1, 2 ORDER BY 1, 2`, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ARAgingRow
	for rows.Next() {
		var a model.ARAgingRow
		if err := rows.Scan(&a.Currency, &a.Bucket, &a.Invoices, &a.Amount); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
