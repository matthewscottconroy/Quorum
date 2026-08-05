package repo

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// GLRepo reads the general ledger (Phase A of roadmap/cpa-accounting.md).
// All writes happen inside the database via posting triggers; Go only reads.
type GLRepo struct {
	db *pgxpool.Pool
}

// NewGLRepo constructs the repo.
func NewGLRepo(db *pgxpool.Pool) *GLRepo {
	return &GLRepo{db: db}
}

// TrialBalance returns per-account, per-currency debit/credit totals for
// every account that has activity, in chart order.
func (r *GLRepo) TrialBalance(ctx context.Context) ([]model.GLBalance, error) {
	rows, err := r.db.Query(ctx, `
		SELECT a.code, a.name, a.type, l.currency,
		       sum(l.debit), sum(l.credit), sum(l.debit) - sum(l.credit)
		FROM journal_lines l
		JOIN accounts a ON a.id = l.account_id
		GROUP BY a.code, a.name, a.type, l.currency
		ORDER BY a.code, l.currency`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.GLBalance
	for rows.Next() {
		var b model.GLBalance
		if err := rows.Scan(&b.Code, &b.Name, &b.Type, &b.Currency, &b.Debits, &b.Credits, &b.Balance); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Reconcile returns AR mismatches between the GL and the dues subledger,
// per currency. Empty = the books reconcile (the invariant holds).
func (r *GLRepo) Reconcile(ctx context.Context) ([]model.GLReconcileRow, error) {
	rows, err := r.db.Query(ctx, `SELECT currency, gl_ar, sub_ar FROM gl_reconcile_ar ORDER BY currency`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.GLReconcileRow
	for rows.Next() {
		var x model.GLReconcileRow
		if err := rows.Scan(&x.Currency, &x.GLAR, &x.SubledgerAR); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// RecentEntries returns the latest journal entries with their lines, newest
// first — the human-readable face of the books.
func (r *GLRepo) RecentEntries(ctx context.Context, limit int) ([]model.GLEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.Query(ctx, `
		SELECT e.id::text, e.seq, e.entry_date, e.memo, e.source_type, e.created_at,
		       a.code, a.name, l.currency, l.debit, l.credit
		FROM journal_entries e
		JOIN journal_lines l ON l.entry_id = e.id
		JOIN accounts a ON a.id = l.account_id
		WHERE e.seq > (SELECT coalesce(max(seq), 0) - $1 FROM journal_entries)
		ORDER BY e.seq DESC, a.code`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.GLEntry
	var cur *model.GLEntry
	for rows.Next() {
		var e model.GLEntry
		var ln model.GLLine
		if err := rows.Scan(&e.ID, &e.Seq, &e.EntryDate, &e.Memo, &e.SourceType, &e.CreatedAt,
			&ln.AccountCode, &ln.AccountName, &ln.Currency, &ln.Debit, &ln.Credit); err != nil {
			return nil, err
		}
		if cur == nil || cur.ID != e.ID {
			out = append(out, e)
			cur = &out[len(out)-1]
		}
		cur.Lines = append(cur.Lines, ln)
	}
	return out, rows.Err()
}

// ---- Phase C: periods, manual entries, statements ----

// Periods returns closed months, newest first.
func (r *GLRepo) Periods(ctx context.Context) ([]model.AccountingPeriod, error) {
	rows, err := r.db.Query(ctx, `
		SELECT p.month, p.closed_at, coalesce(m.display_name, u.email, '')
		FROM accounting_periods p
		LEFT JOIN users u ON u.id = p.closed_by
		LEFT JOIN members m ON m.id = u.member_id
		ORDER BY p.month DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AccountingPeriod
	for rows.Next() {
		var p model.AccountingPeriod
		if err := rows.Scan(&p.Month, &p.ClosedAt, &p.ClosedBy); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ClosePeriod locks a month (YYYY-MM-01) for posting.
func (r *GLRepo) ClosePeriod(ctx context.Context, month string, closedBy string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO accounting_periods (month, closed_by)
		VALUES (date_trunc('month', $1::date)::date, $2::uuid)
		ON CONFLICT (month) DO NOTHING`, month, closedBy)
	return err
}

// ReopenPeriod unlocks a month (admin correction workflow; audited upstream).
func (r *GLRepo) ReopenPeriod(ctx context.Context, month string) error {
	tag, err := r.db.Exec(ctx,
		`DELETE FROM accounting_periods WHERE month = date_trunc('month', $1::date)::date`, month)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ManualEntry posts an adjusting entry (admin). The database enforces
// balance, append-only, and closed periods regardless.
func (r *GLRepo) ManualEntry(ctx context.Context, entryDate, memo, createdBy string, lines []model.GLLineInput) (string, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var eid string
	if err := tx.QueryRow(ctx, `
		INSERT INTO journal_entries (entry_date, memo, source_type, created_by)
		VALUES ($1::date, $2, 'manual', $3::uuid) RETURNING id::text`,
		entryDate, memo, createdBy).Scan(&eid); err != nil {
		return "", err
	}
	for _, ln := range lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO journal_lines (entry_id, account_id, currency, debit, credit)
			VALUES ($1::uuid, (SELECT id FROM accounts WHERE code = $2), $3, $4, $5)`,
			eid, ln.AccountCode, ln.Currency, ln.Debit, ln.Credit); err != nil {
			return "", err
		}
	}
	return eid, tx.Commit(ctx)
}

// Statement returns per-account totals within a date range for the given
// account types (income statement: income+expense over a range; balance
// sheet: asset/liability/net_assets cumulative to a date).
func (r *GLRepo) Statement(ctx context.Context, from, to string, types []string) ([]model.GLBalance, error) {
	rows, err := r.db.Query(ctx, `
		SELECT a.code, a.name, a.type, l.currency, sum(l.debit), sum(l.credit), sum(l.debit) - sum(l.credit)
		FROM journal_lines l
		JOIN journal_entries e ON e.id = l.entry_id
		JOIN accounts a ON a.id = l.account_id
		WHERE e.entry_date >= $1::date AND e.entry_date <= $2::date AND a.type = ANY($3)
		GROUP BY a.code, a.name, a.type, l.currency
		HAVING sum(l.debit) <> sum(l.credit) OR sum(l.debit) <> 0
		ORDER BY a.code, l.currency`, from, to, types)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.GLBalance
	for rows.Next() {
		var b model.GLBalance
		if err := rows.Scan(&b.Code, &b.Name, &b.Type, &b.Currency, &b.Debits, &b.Credits, &b.Balance); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ARAging buckets open receivables by days overdue as of a date.
func (r *GLRepo) ARAging(ctx context.Context, asOf string) ([]model.ARAgingRow, error) {
	rows, err := r.db.Query(ctx, `
		SELECT i.currency,
		       CASE WHEN i.due_date >= $1::date THEN 'current'
		            WHEN i.due_date >= $1::date - 30 THEN '1-30'
		            WHEN i.due_date >= $1::date - 60 THEN '31-60'
		            WHEN i.due_date >= $1::date - 90 THEN '61-90'
		            ELSE '90+' END AS bucket,
		       count(*), sum(gl_invoice_remaining(i.id))
		FROM dues_invoices i
		WHERE i.status NOT IN ('paid', 'waived') AND gl_invoice_remaining(i.id) > 0
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

// Accounts lists the chart; CreateAccount and UpdateAccount edit it (the
// account_guard trigger freezes code/type once postings exist).
func (r *GLRepo) Accounts(ctx context.Context) ([]model.GLAccount, error) {
	rows, err := r.db.Query(ctx, `
		SELECT a.id::text, a.code, a.name, a.type, a.active,
		       EXISTS (SELECT 1 FROM journal_lines l WHERE l.account_id = a.id)
		FROM accounts a ORDER BY a.code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.GLAccount
	for rows.Next() {
		var a model.GLAccount
		if err := rows.Scan(&a.ID, &a.Code, &a.Name, &a.Type, &a.Active, &a.HasPostings); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateAccount adds a chart account.
func (r *GLRepo) CreateAccount(ctx context.Context, code, name, typ string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO accounts (code, name, type) VALUES ($1, $2, $3)`, code, name, typ)
	return err
}

// UpdateAccount renames/toggles an account.
func (r *GLRepo) UpdateAccount(ctx context.Context, id string, name *string, active *bool) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE accounts SET name = coalesce($1, name), active = coalesce($2, active)
		WHERE id = $3::uuid`, name, active, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// LedgerCSVRows streams every entry+line in a range for the export pack.
func (r *GLRepo) LedgerCSVRows(ctx context.Context, from, to string) ([][]string, error) {
	rows, err := r.db.Query(ctx, `
		SELECT e.seq::text, e.entry_date::text, e.source_type, coalesce(e.source_id::text, ''), e.memo,
		       a.code, a.name, l.currency, l.debit::text, l.credit::text
		FROM journal_entries e
		JOIN journal_lines l ON l.entry_id = e.id
		JOIN accounts a ON a.id = l.account_id
		WHERE e.entry_date >= $1::date AND e.entry_date <= $2::date
		ORDER BY e.seq, a.code`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][]string{{"seq", "date", "source_type", "source_id", "memo", "account_code", "account_name", "currency", "debit_minor", "credit_minor"}}
	for rows.Next() {
		rec := make([]string, 10)
		ptrs := make([]any, 10)
		for i := range rec {
			ptrs[i] = &rec[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// PostingRules returns the configured mappings with account labels.
func (r *GLRepo) PostingRules(ctx context.Context) ([]model.PostingRule, error) {
	rows, err := r.db.Query(ctx, `
		SELECT pr.key, a.id::text, a.code, a.name
		FROM posting_rules pr JOIN accounts a ON a.id = pr.account_id
		ORDER BY pr.key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PostingRule
	for rows.Next() {
		var p model.PostingRule
		if err := rows.Scan(&p.Key, &p.AccountID, &p.AccountCode, &p.AccountName); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPostingRule upserts one mapping to an account code.
func (r *GLRepo) SetPostingRule(ctx context.Context, key, accountCode, updatedBy string) error {
	tag, err := r.db.Exec(ctx, `
		INSERT INTO posting_rules (key, account_id, updated_by)
		SELECT $1, a.id, $3::uuid FROM accounts a WHERE a.code = $2
		ON CONFLICT (key) DO UPDATE SET account_id = excluded.account_id,
			updated_by = excluded.updated_by, updated_at = now()`, key, accountCode, updatedBy)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows // unknown account code
	}
	return nil
}

// StatementCash is the cash-basis view: only entries that touch a cash
// account count, and the receivable counterpart is presented as dues income
// (collections). Everything else reports under its own account.
func (r *GLRepo) StatementCash(ctx context.Context, from, to string) ([]model.GLBalance, error) {
	rows, err := r.db.Query(ctx, `
		WITH cashset AS (SELECT gl_cash_accounts() AS id),
		cash_entries AS (
			SELECT DISTINCT l.entry_id FROM journal_lines l
			WHERE l.account_id IN (SELECT id FROM cashset)
		)
		SELECT a2.code, a2.name, a2.type, l.currency,
		       sum(l.debit), sum(l.credit), sum(l.debit) - sum(l.credit)
		FROM journal_lines l
		JOIN journal_entries e ON e.id = l.entry_id
		JOIN cash_entries ce ON ce.entry_id = l.entry_id
		JOIN accounts a ON a.id = l.account_id
		JOIN LATERAL (
			SELECT * FROM accounts ax
			WHERE ax.id = CASE WHEN a.id = gl_rule('receivable')
			                   THEN gl_rule('income.dues') ELSE a.id END
		) a2 ON true
		WHERE e.entry_date >= $1::date AND e.entry_date <= $2::date
		  AND l.account_id NOT IN (SELECT id FROM cashset)
		GROUP BY a2.code, a2.name, a2.type, l.currency
		ORDER BY a2.code, l.currency`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.GLBalance
	for rows.Next() {
		var b model.GLBalance
		if err := rows.Scan(&b.Code, &b.Name, &b.Type, &b.Currency, &b.Debits, &b.Credits, &b.Balance); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// PurchasesCSVRows exports the purchase evidence for the CPA pack.
func (r *GLRepo) PurchasesCSVRows(ctx context.Context, from, to string) ([][]string, error) {
	rows, err := r.db.Query(ctx, `
		SELECT pr.created_at::date::text, f.name, pr.payee, coalesce(pr.memo, ''),
		       pr.amount::text, pr.currency, pr.status,
		       coalesce(m.display_name, u.email, ''), coalesce(pr.journal_entry_id::text, ''),
		       coalesce(ea.code, ''),
		       coalesce((SELECT string_agg(coalesce(m2.display_name, u2.email, 'former user')
		                 || ' @ ' || pa.approved_at::text || ' from ' || pa.ip, '; ' ORDER BY pa.approved_at)
		                 FROM purchase_approvals pa
		                 LEFT JOIN users u2 ON u2.id = pa.approver_id
		                 LEFT JOIN members m2 ON m2.id = u2.member_id
		                 WHERE pa.request_id = pr.id), '')
		FROM purchase_requests pr
		JOIN funds f ON f.id = pr.fund_id
		LEFT JOIN users u ON u.id = pr.requester_id
		LEFT JOIN members m ON m.id = u.member_id
		LEFT JOIN accounts ea ON ea.id = pr.expense_account_id
		WHERE pr.created_at::date >= $1::date AND pr.created_at::date <= $2::date
		ORDER BY pr.created_at`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][]string{{"date", "fund", "payee", "memo", "amount_minor", "currency", "status", "requester", "journal_entry", "expense_account", "approvals"}}
	for rows.Next() {
		rec := make([]string, 11)
		ptrs := make([]any, 11)
		for i := range rec {
			ptrs[i] = &rec[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
