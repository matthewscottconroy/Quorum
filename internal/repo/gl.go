package repo

import (
	"context"

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
