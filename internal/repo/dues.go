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

// ErrInvoiceNotPayable is returned when a payment is recorded against an
// invoice whose status can't accept one (waived or already fully paid):
// posting would drive the GL receivable wrong and break reconciliation.
var ErrInvoiceNotPayable = errors.New("invoice is not in a payable state")

// ErrExceedsRemaining / ErrExceedsPaid are the money guards' refusals; the
// repo returns the applicable limit alongside so handlers can say the number.
var (
	ErrExceedsRemaining = errors.New("payment exceeds the remaining balance")
	ErrExceedsPaid      = errors.New("refund exceeds the amount actually paid")
)

// DuesRepo provides PostgreSQL data access for invoices.
type DuesRepo struct {
	db *pgxpool.Pool
}

// NewDuesRepo constructs a DuesRepo backed by the given connection pool.
func NewDuesRepo(db *pgxpool.Pool) *DuesRepo {
	return &DuesRepo{db: db}
}

// InvoiceFilter holds the optional query parameters for listing invoices.
type InvoiceFilter struct {
	MemberID    string
	Status      string
	PeriodLabel string
	Limit       int
	Offset      int
}

// invoiceFilterWhere renders the filter's WHERE clause and args — shared by
// the page query and the summary so they can never disagree about the set.
func invoiceFilterWhere(f InvoiceFilter) (string, []any) {
	args := []any{}
	conds := []string{}
	idx := 1

	if f.MemberID != "" {
		conds = append(conds, fmt.Sprintf("di.member_id = $%d::uuid", idx))
		args = append(args, f.MemberID)
		idx++
	}
	if f.Status != "" {
		conds = append(conds, fmt.Sprintf("di.status = $%d", idx))
		args = append(args, f.Status)
		idx++
	}
	if f.PeriodLabel != "" {
		conds = append(conds, fmt.Sprintf("di.period_label = $%d", idx))
		args = append(args, f.PeriodLabel)
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	return where, args
}

// ListInvoices returns a page of invoices matching the filter, plus the total count.
func (r *DuesRepo) ListInvoices(ctx context.Context, f InvoiceFilter) ([]model.DuesInvoice, int, error) {
	where, args := invoiceFilterWhere(f)

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	query := fmt.Sprintf(`
		SELECT COUNT(*) OVER() AS total_count,
		       di.id::text, coalesce(di.member_id::text, ''), di.amount, di.currency, di.period_label,
		       di.due_date, di.status, di.notes, di.created_at, di.updated_at,
		       coalesce(m.display_name, c.name || ' (contact)'), di.contact_id::text,
		       coalesce((SELECT sum(t.amount) FROM transactions t
		                 WHERE t.invoice_id = di.id AND t.provider_status != 'failed'
		                   AND t.currency = di.currency), 0) AS paid_minor
		FROM dues_invoices di
		LEFT JOIN members m ON m.id = di.member_id
		LEFT JOIN contacts c ON c.id = di.contact_id
		%s
		ORDER BY di.due_date DESC, 12
		LIMIT $%d OFFSET $%d`, where, len(args)+1, len(args)+2)
	args = append(args, limit, f.Offset)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var invoices []model.DuesInvoice
	var total int
	for rows.Next() {
		var inv model.DuesInvoice
		if err := rows.Scan(&total, &inv.ID, &inv.MemberID, &inv.AmountMinor, &inv.Currency,
			&inv.PeriodLabel, &inv.DueDate, &inv.Status, &inv.Notes,
			&inv.CreatedAt, &inv.UpdatedAt, &inv.MemberName, &inv.ContactID, &inv.PaidMinor); err != nil {
			return nil, 0, err
		}
		invoices = append(invoices, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// COUNT(*) OVER() yields no rows on an empty page; fall back to a plain count.
	// LEFT JOIN (not INNER) so contact invoices — which have no member_id — are
	// still counted; an INNER join here undercounts the total when paging past
	// the end without a member filter.
	if len(invoices) == 0 && f.Offset > 0 {
		countQuery := "SELECT count(*) FROM dues_invoices di LEFT JOIN members m ON m.id = di.member_id " + where
		if err := r.db.QueryRow(ctx, countQuery, args[:len(args)-2]...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}
	return invoices, total, nil
}

// InvoiceSummary is the money answer the Dues page leads with: how many
// invoices the current filter matches and what is still owed on them,
// per currency (never summed across currencies).
type InvoiceSummary struct {
	Currency         string `json:"currency"`
	Count            int    `json:"count"`
	OutstandingMinor int64  `json:"outstanding_minor"`
}

// SummarizeInvoices computes the filtered set's outstanding balances with
// the same WHERE semantics as ListInvoices (minus paging).
func (r *DuesRepo) SummarizeInvoices(ctx context.Context, f InvoiceFilter) ([]InvoiceSummary, error) {
	where, args := invoiceFilterWhere(f)
	rows, err := r.db.Query(ctx, `
		SELECT di.currency, count(*),
		       coalesce(sum(CASE WHEN di.status IN ('paid','waived') THEN 0
		            ELSE greatest(di.amount - coalesce((SELECT sum(t.amount) FROM transactions t
		                  WHERE t.invoice_id = di.id AND t.provider_status != 'failed'
		                    AND t.currency = di.currency), 0), 0) END), 0)
		FROM dues_invoices di `+where+`
		GROUP BY di.currency ORDER BY di.currency`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InvoiceSummary
	for rows.Next() {
		var s InvoiceSummary
		if err := rows.Scan(&s.Currency, &s.Count, &s.OutstandingMinor); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetInvoice returns the invoice with the given id and its transactions, or pgx.ErrNoRows.
func (r *DuesRepo) GetInvoice(ctx context.Context, id string) (*model.DuesInvoice, error) {
	var inv model.DuesInvoice
	err := r.db.QueryRow(ctx, `
		SELECT di.id::text, coalesce(di.member_id::text, ''), di.amount, di.currency, di.period_label,
		       di.due_date, di.status, di.notes, di.created_at, di.updated_at,
		       coalesce(m.display_name, c.name || ' (contact)'), di.contact_id::text,
		       coalesce((SELECT sum(t.amount) FROM transactions t
		                 WHERE t.invoice_id = di.id AND t.provider_status != 'failed'
		                   AND t.currency = di.currency), 0),
		       di.reminder_stage, di.last_reminder_at
		FROM dues_invoices di
		LEFT JOIN members m ON m.id = di.member_id
		LEFT JOIN contacts c ON c.id = di.contact_id
		WHERE di.id = $1::uuid`, id).
		Scan(&inv.ID, &inv.MemberID, &inv.AmountMinor, &inv.Currency,
			&inv.PeriodLabel, &inv.DueDate, &inv.Status, &inv.Notes,
			&inv.CreatedAt, &inv.UpdatedAt, &inv.MemberName, &inv.ContactID, &inv.PaidMinor,
			&inv.ReminderStage, &inv.LastReminderAt)
	if err != nil {
		return nil, err
	}
	txs, _, err := r.ListTransactions(ctx, TransactionFilter{InvoiceID: id, Limit: 500})
	if err != nil {
		return nil, err
	}
	inv.Transactions = txs
	return &inv, nil
}

// CreateInvoiceBatch inserts one invoice per entry within a single transaction.
func (r *DuesRepo) CreateInvoiceBatch(ctx context.Context, invs []*model.DuesInvoice) ([]model.DuesInvoice, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	created := make([]model.DuesInvoice, 0, len(invs))
	for _, inv := range invs {
		var c model.DuesInvoice
		err := tx.QueryRow(ctx, `
			INSERT INTO dues_invoices (member_id, contact_id, amount, currency, period_label, due_date, status, notes)
			VALUES (nullif($1, '')::uuid, $8::uuid, $2, $3, $4, $5, $6, $7)
			RETURNING id::text, coalesce(member_id::text, ''), contact_id::text, amount, currency, period_label, due_date, status, notes, created_at, updated_at`,
			inv.MemberID, inv.AmountMinor, inv.Currency, inv.PeriodLabel, inv.DueDate, inv.Status, inv.Notes, inv.ContactID).
			Scan(&c.ID, &c.MemberID, &c.ContactID, &c.AmountMinor, &c.Currency, &c.PeriodLabel, &c.DueDate, &c.Status, &c.Notes, &c.CreatedAt, &c.UpdatedAt)
		if err != nil {
			return nil, err
		}
		created = append(created, c)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return created, nil
}

// BatchUpdateStatus sets the status on many invoices in one statement,
// returning the number changed. Used for bulk waive/re-open from the UI.
// Waiving skips invoices that are already paid (there is nothing left to
// write off), and any target other than 'waived' is immediately re-derived
// from the ledger so recorded payments always win over a bulk re-open.
func (r *DuesRepo) BatchUpdateStatus(ctx context.Context, ids []string, status string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `
		UPDATE dues_invoices SET status = $1, updated_at = now()
		WHERE id = ANY($2::uuid[]) AND NOT ($1 = 'waived' AND status = 'paid')`,
		status, ids)
	if err != nil {
		return 0, err
	}
	if status != "waived" {
		if _, err := tx.Exec(ctx, batchRecomputeStatusSQL, ids); err != nil {
			return 0, err
		}
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}

// batchRecomputeStatusSQL is recomputeInvoiceStatusSQL over a set of ids.
const batchRecomputeStatusSQL = `
	UPDATE dues_invoices SET
		status = CASE
			WHEN (SELECT coalesce(sum(amount),0) FROM transactions WHERE invoice_id = dues_invoices.id AND provider_status != 'failed' AND currency = dues_invoices.currency)
			     >= amount THEN 'paid'
			WHEN (SELECT coalesce(sum(amount),0) FROM transactions WHERE invoice_id = dues_invoices.id AND provider_status != 'failed' AND currency = dues_invoices.currency)
			     > 0 THEN 'partial'
			WHEN due_date < CURRENT_DATE THEN 'overdue'
			WHEN status = 'overdue' THEN 'overdue'
			ELSE 'pending'
		END,
		updated_at = now()
	WHERE id = ANY($1::uuid[]) AND status != 'waived'`

// PaidSum returns the invoice's net recorded total (payments minus refunds)
// in the given currency — the number both the overpayment guard and the
// refund cap reason about.
func (r *DuesRepo) PaidSum(ctx context.Context, invoiceID, currency string) (int64, error) {
	var sum int64
	err := r.db.QueryRow(ctx, `
		SELECT coalesce(sum(amount),0) FROM transactions
		WHERE invoice_id = $1::uuid AND provider_status != 'failed' AND currency = $2`,
		invoiceID, currency).Scan(&sum)
	return sum, err
}

// UpdateInvoiceStatus sets an invoice's status, returning pgx.ErrNoRows if absent.
func (r *DuesRepo) UpdateInvoiceStatus(ctx context.Context, id, status string, notes *string) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE dues_invoices SET status = $1, notes = coalesce($2, notes), updated_at = now()
		WHERE id = $3::uuid`, status, notes, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// recomputeInvoiceStatusSQL recomputes an invoice's status from its payments.
// Only transactions in the SAME currency as the invoice count toward the paid
// total — summing raw minor units across currencies would be meaningless (¥1000
// is not $1000), so a mismatched-currency payment must never flip the status.
// The status is a pure function of the ledger: paid when covered, partial when
// something has landed, overdue/pending otherwise — so a refund moves a paid
// invoice back to partial/pending instead of leaving it 'paid' forever. Only
// 'waived' is exempt: waiving is an explicit decision with its own GL posting,
// and un-waiving goes through the status endpoint, never through recompute.
const recomputeInvoiceStatusSQL = `
	UPDATE dues_invoices SET
		status = CASE
			WHEN (SELECT coalesce(sum(amount),0) FROM transactions WHERE invoice_id = $1::uuid AND provider_status != 'failed' AND currency = dues_invoices.currency)
			     >= amount THEN 'paid'
			WHEN (SELECT coalesce(sum(amount),0) FROM transactions WHERE invoice_id = $1::uuid AND provider_status != 'failed' AND currency = dues_invoices.currency)
			     > 0 THEN 'partial'
			WHEN due_date < CURRENT_DATE THEN 'overdue'
			-- An officer may mark an invoice overdue EARLY (dunning before the
			-- date); with no payments on file that judgment call stands.
			WHEN status = 'overdue' THEN 'overdue'
			ELSE 'pending'
		END,
		updated_at = now()
	WHERE id = $1::uuid AND status != 'waived'`

// RecomputeInvoiceStatus recomputes an invoice's status from its same-currency transactions.
func (r *DuesRepo) RecomputeInvoiceStatus(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, recomputeInvoiceStatusSQL, id)
	return err
}

// CreateGuardedTransaction records a manual payment (positive amount) or
// refund (negative) with its money guard evaluated UNDER THE INVOICE ROW
// LOCK, then recomputes the status — all in one transaction. The read-guard-
// insert sequences the handlers used to run were TOCTOU races: two
// concurrent full refunds could both pass the cap, and a payment landing
// beside a webhook could overshoot. Guards:
//   - waived invoice        → ErrInvoiceNotPayable (nothing may post)
//   - payment > remaining   → ErrExceedsRemaining unless allowOverpay
//   - refund > net paid     → ErrExceedsPaid, always
//
// The returned limit is the remaining balance (payments) or the refundable
// total (refunds) at the moment of the locked read.
func (r *DuesRepo) CreateGuardedTransaction(ctx context.Context, t *model.Transaction, allowOverpay bool) (*model.Transaction, int64, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var status string
	var amount int64
	if err := tx.QueryRow(ctx, `
		SELECT status, amount FROM dues_invoices WHERE id = $1::uuid FOR UPDATE`,
		*t.InvoiceID).Scan(&status, &amount); err != nil {
		return nil, 0, err
	}
	if status == "waived" {
		return nil, 0, ErrInvoiceNotPayable
	}
	var paid int64
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(sum(amount),0) FROM transactions
		WHERE invoice_id = $1::uuid AND provider_status != 'failed' AND currency = $2`,
		*t.InvoiceID, t.Currency).Scan(&paid); err != nil {
		return nil, 0, err
	}
	if t.AmountMinor > 0 {
		if remaining := amount - paid; t.AmountMinor > remaining && !allowOverpay {
			return nil, max(remaining, 0), ErrExceedsRemaining
		}
	} else if -t.AmountMinor > paid {
		return nil, max(paid, 0), ErrExceedsPaid
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO transactions
		    (invoice_id, member_id, amount, currency, provider, provider_reference_id,
		     provider_status, payment_method_type, recorded_by, occurred_at, notes, resource_id)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9::uuid, $10, $11, $12::uuid)
		RETURNING id::text, occurred_at`,
		t.InvoiceID, t.MemberID, t.AmountMinor, t.Currency, t.Provider,
		t.ProviderReferenceID, t.ProviderStatus, t.PaymentMethodType,
		t.RecordedBy, t.OccurredAt, t.Notes, t.ResourceID).Scan(&t.ID, &t.OccurredAt); err != nil {
		return nil, 0, err
	}
	if _, err := tx.Exec(ctx, recomputeInvoiceStatusSQL, *t.InvoiceID); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return t, 0, nil
}

// ReceiptInfo is what a payment-receipt email needs to say thank you.
type ReceiptInfo struct {
	MemberName     string
	MemberEmail    string
	PeriodLabel    string
	Currency       string
	RemainingMinor int64
}

// ReceiptInfoFor fetches receipt context; ok=false when the invoice has no
// reachable member (contact invoices, no email on file).
func (r *DuesRepo) ReceiptInfoFor(ctx context.Context, invoiceID string) (ReceiptInfo, bool, error) {
	var info ReceiptInfo
	var email *string
	err := r.db.QueryRow(ctx, `
		SELECT m.display_name, m.email, di.period_label, di.currency,
		       di.amount - coalesce((SELECT sum(t.amount) FROM transactions t
		                             WHERE t.invoice_id = di.id AND t.provider_status != 'failed'
		                               AND t.currency = di.currency), 0)
		FROM dues_invoices di
		JOIN members m ON m.id = di.member_id
		WHERE di.id = $1::uuid`,
		invoiceID).Scan(&info.MemberName, &email, &info.PeriodLabel, &info.Currency, &info.RemainingMinor)
	if errors.Is(err, pgx.ErrNoRows) {
		// Contact invoices have no member row: "no receipt to send", not a
		// database failure — honor the documented ok=false contract.
		return info, false, nil
	}
	if err != nil {
		return info, false, err
	}
	if email == nil || *email == "" {
		return info, false, nil
	}
	info.MemberEmail = *email
	return info, true, nil
}

// ApplyLateFees creates one flat-fee invoice per sufficiently-overdue member
// invoice that hasn't been charged yet, linking it back via
// late_fee_invoice_id so the nightly job is idempotent. Fees post in the
// ORIGINAL invoice's currency at face value (a flat amount, documented in
// settings); contact invoices and already-fee'd invoices are skipped.
func (r *DuesRepo) ApplyLateFees(ctx context.Context, feeMinor int64, graceDays int) (int, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// late_fee_for IS NULL is the anti-compounding rule: a fee invoice is
	// never itself a candidate, no matter how overdue it goes. Without it,
	// each fee spawned another 15+grace days later, forever.
	rows, err := tx.Query(ctx, `
		SELECT di.id::text, di.member_id::text, di.currency, di.period_label
		FROM dues_invoices di
		WHERE di.status IN ('overdue', 'partial')
		  AND di.due_date < CURRENT_DATE - $1::int
		  AND di.late_fee_invoice_id IS NULL
		  AND di.late_fee_for IS NULL
		  AND di.member_id IS NOT NULL
		  -- An agreed installment plan is a negotiated arrangement: the
		  -- reminder step already treats plan-current members as current,
		  -- and the fee step must not assert the opposite policy.
		  AND NOT EXISTS (SELECT 1 FROM invoice_installments ii WHERE ii.invoice_id = di.id)
		FOR UPDATE OF di SKIP LOCKED
		LIMIT 500`, graceDays)
	if err != nil {
		return 0, err
	}
	type cand struct{ id, member, currency, period string }
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.member, &c.currency, &c.period); err != nil {
			rows.Close()
			return 0, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, c := range cands {
		// The label is display-only; the machine link is late_fee_for.
		// Truncate to the schema's 200-char bound so one long period label
		// can't violate the CHECK and roll back the entire batch.
		label := "Late fee — " + c.period
		if r2 := []rune(label); len(r2) > 200 {
			label = string(r2[:200])
		}
		var feeID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO dues_invoices (member_id, amount, currency, period_label, due_date, status, late_fee_for)
			VALUES ($1::uuid, $2, $3, $4, CURRENT_DATE + 14, 'pending', $5::uuid)
			RETURNING id::text`,
			c.member, feeMinor, c.currency, label, c.id).Scan(&feeID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE dues_invoices SET late_fee_invoice_id = $2::uuid WHERE id = $1::uuid`,
			c.id, feeID); err != nil {
			return 0, err
		}
		n++
	}
	return n, tx.Commit(ctx)
}

// MarkOverdue flips past-due pending invoices to overdue and returns the count.
func (r *DuesRepo) MarkOverdue(ctx context.Context) (int64, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE dues_invoices SET status = 'overdue', updated_at = now()
		WHERE due_date < CURRENT_DATE AND status = 'pending'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// CountByStatus returns the number of invoices in the given status.
func (r *DuesRepo) CountByStatus(ctx context.Context, status string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT count(*) FROM dues_invoices WHERE status = $1`, status).Scan(&n)
	return n, err
}

// Transactions

// TransactionFilter holds the optional query parameters for listing transactions.
type TransactionFilter struct {
	InvoiceID string
	MemberID  string
	Limit     int
	Offset    int
}

// ListTransactions returns a page of transactions matching the filter, plus the total count.
func (r *DuesRepo) ListTransactions(ctx context.Context, f TransactionFilter) ([]model.Transaction, int, error) {
	args := []any{}
	conds := []string{}
	idx := 1

	if f.InvoiceID != "" {
		conds = append(conds, fmt.Sprintf("t.invoice_id = $%d::uuid", idx))
		args = append(args, f.InvoiceID)
		idx++
	}
	if f.MemberID != "" {
		conds = append(conds, fmt.Sprintf("t.member_id = $%d::uuid", idx))
		args = append(args, f.MemberID)
		idx++
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	limit := f.Limit
	// 500 is the ceiling, not 200: GetInvoice asks for 500 so the detail
	// modal's remaining-balance math sees EVERY transaction — a silent clamp
	// to 100 overstated the prefill on transaction-heavy invoices.
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	query := fmt.Sprintf(`
		SELECT COUNT(*) OVER() AS total_count,
		       t.id::text, t.invoice_id::text, t.member_id::text, t.amount, t.currency,
		       t.provider, t.provider_reference_id, t.provider_status, t.payment_method_type,
		       t.recorded_by::text, t.occurred_at, t.notes, t.resource_id::text,
		       coalesce(m.display_name, '') as member_name
		FROM transactions t
		LEFT JOIN members m ON m.id = t.member_id
		%s
		ORDER BY t.occurred_at DESC
		LIMIT $%d OFFSET $%d`, where, idx, idx+1)
	args = append(args, limit, f.Offset)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var txs []model.Transaction
	var total int
	for rows.Next() {
		var t model.Transaction
		if err := rows.Scan(&total, &t.ID, &t.InvoiceID, &t.MemberID, &t.AmountMinor, &t.Currency,
			&t.Provider, &t.ProviderReferenceID, &t.ProviderStatus, &t.PaymentMethodType,
			&t.RecordedBy, &t.OccurredAt, &t.Notes, &t.ResourceID, &t.MemberName); err != nil {
			return nil, 0, err
		}
		txs = append(txs, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// COUNT(*) OVER() yields no rows on an empty page; fall back to a plain count.
	if len(txs) == 0 && f.Offset > 0 {
		countQuery := "SELECT count(*) FROM transactions t " + where
		if err := r.db.QueryRow(ctx, countQuery, args[:len(args)-2]...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}
	return txs, total, nil
}

// CreateTransaction inserts a payment transaction and returns the stored row.
// Used for manual (officer-entered) payments; webhook payments go through
// RecordWebhookPayment, which also claims the event and recomputes status.
func (r *DuesRepo) CreateTransaction(ctx context.Context, t *model.Transaction) (*model.Transaction, error) {
	var created model.Transaction
	err := r.db.QueryRow(ctx, `
		INSERT INTO transactions
		    (invoice_id, member_id, amount, currency, provider, provider_reference_id,
		     provider_status, payment_method_type, recorded_by, occurred_at, notes)
		VALUES
		    ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9::uuid, $10, $11)
		RETURNING id::text, invoice_id::text, member_id::text, amount, currency,
		          provider, provider_reference_id, provider_status, payment_method_type,
		          recorded_by::text, occurred_at, notes`,
		t.InvoiceID, t.MemberID, t.AmountMinor, t.Currency, t.Provider,
		t.ProviderReferenceID, t.ProviderStatus, t.PaymentMethodType,
		t.RecordedBy, t.OccurredAt, t.Notes).
		Scan(&created.ID, &created.InvoiceID, &created.MemberID, &created.AmountMinor, &created.Currency,
			&created.Provider, &created.ProviderReferenceID, &created.ProviderStatus, &created.PaymentMethodType,
			&created.RecordedBy, &created.OccurredAt, &created.Notes)
	return &created, err
}

// FindInvoiceByProviderRef returns the invoice id linked to a provider reference, if any.
func (r *DuesRepo) FindInvoiceByProviderRef(ctx context.Context, providerRef string) (string, error) {
	var id string
	err := r.db.QueryRow(ctx, `
		SELECT invoice_id::text FROM transactions
		WHERE provider_reference_id = $1 AND invoice_id IS NOT NULL
		LIMIT 1`, providerRef).Scan(&id)
	return id, err
}

// RecordWebhookPayment atomically claims a provider event and records its
// transaction. The event claim, transaction insert, and invoice status
// recompute commit together: a failure leaves the event unclaimed so the
// provider's retry can succeed, and a concurrent duplicate delivery observes
// the claim and reports already=true without inserting anything.
func (r *DuesRepo) RecordWebhookPayment(ctx context.Context, eventID string, t *model.Transaction) (already bool, err error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tag, err := tx.Exec(ctx,
		`INSERT INTO processed_events (provider_event_id) VALUES ($1) ON CONFLICT DO NOTHING`, eventID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return true, nil
	}

	// A payment posting onto a waived invoice would credit a receivable that
	// gl_post_waive already wrote to zero, driving GL A/R negative and breaking
	// reconciliation permanently. Claim the event (so the provider stops
	// retrying) but refuse to record: this is an exceptional case an operator
	// must resolve (un-waive then re-apply, or refund). Committing keeps the
	// claim; the caller logs ErrInvoiceNotPayable for follow-up.
	//
	// FOR UPDATE is load-bearing: every writer that reads the invoice's paid
	// total (ConfirmAndPost, CreateGuardedTransaction, this path) must take
	// the invoice row lock FIRST, or two of them can interleave their reads
	// and double-post — the lock is the whole serialization story. The
	// member id rides along: webhook callers rarely know it, and a NULL
	// member_id makes the payment invisible on the member's year-end
	// statement (fetched strictly by member).
	if t.InvoiceID != nil {
		var st string
		var memberID *string
		err := tx.QueryRow(ctx,
			`SELECT status, member_id::text FROM dues_invoices WHERE id = $1::uuid FOR UPDATE`,
			*t.InvoiceID).Scan(&st, &memberID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// Metadata names an invoice this database doesn't have (another
			// environment's ID, hand-typed metadata). Record the money as an
			// UNLINKED payment rather than hitting the FK: a 500 here makes
			// the provider retry the same poison event until it disables the
			// endpoint, and the payment never lands anywhere.
			t.InvoiceID = nil
			if t.Notes == nil {
				n := "unmatched: invoice id from provider metadata not found"
				t.Notes = &n
			}
		case err != nil:
			// Real DB failure: the FOR UPDATE convention is load-bearing —
			// never proceed without the lock.
			return false, err
		default:
			if t.MemberID == nil {
				t.MemberID = memberID
			}
		}
		if st == "waived" {
			if cerr := tx.Commit(ctx); cerr != nil {
				return false, cerr
			}
			return false, ErrInvoiceNotPayable
		}
	}
	// Provider refunds arrive as negative amounts. Cap the posting at the
	// invoice's recorded net paid: if the original charge was never recorded
	// here (endpoint added later, outage), an uncapped refund drives paid
	// NEGATIVE — statements show a bare minus line and dunning asks for more
	// than face value. The bank already moved the full sum; the shortfall is
	// logged in notes for a manual entry.
	if t.AmountMinor < 0 && t.InvoiceID != nil {
		var netPaid int64
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(sum(amount), 0) FROM transactions
			WHERE invoice_id = $1::uuid AND provider_status != 'failed' AND currency = $2`,
			*t.InvoiceID, t.Currency).Scan(&netPaid); err != nil {
			return false, err
		}
		if netPaid < 0 {
			netPaid = 0
		}
		if -t.AmountMinor > netPaid {
			excess := -t.AmountMinor - netPaid
			t.AmountMinor = -netPaid
			n := fmt.Sprintf("refund clamped at net paid; %d minor units exceed recorded payments — enter manually", excess)
			if t.Notes != nil {
				n = *t.Notes + "; " + n
			}
			t.Notes = &n
		}
		if t.AmountMinor == 0 {
			// Nothing recorded to refund against: keep the claim (stop the
			// retries) but surface it for the operator.
			if cerr := tx.Commit(ctx); cerr != nil {
				return false, cerr
			}
			return false, ErrExceedsPaid
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO transactions
		    (invoice_id, member_id, amount, currency, provider, provider_reference_id,
		     provider_status, payment_method_type, recorded_by, occurred_at, notes)
		VALUES
		    ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9::uuid, $10, $11)`,
		t.InvoiceID, t.MemberID, t.AmountMinor, t.Currency, t.Provider,
		t.ProviderReferenceID, t.ProviderStatus, t.PaymentMethodType,
		t.RecordedBy, t.OccurredAt, t.Notes); err != nil {
		return false, err
	}

	if t.InvoiceID != nil {
		if _, err := tx.Exec(ctx, recomputeInvoiceStatusSQL, *t.InvoiceID); err != nil {
			return false, err
		}
	}
	return false, tx.Commit(ctx)
}

// RecordWebhookRefund handles a provider whose refund events carry the
// CUMULATIVE total refunded (Stripe's charge.refunded), atomically: claim
// the event, lock the invoice, subtract what this provider reference has
// already refunded, and record only the DELTA. Two partial refunds of $30
// then $40 therefore post -30 then -40, not -30 then -70.
func (r *DuesRepo) RecordWebhookRefund(ctx context.Context, eventID string, t *model.Transaction, cumulative int64) (already bool, err error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tag, err := tx.Exec(ctx,
		`INSERT INTO processed_events (provider_event_id) VALUES ($1) ON CONFLICT DO NOTHING`, eventID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return true, nil
	}
	var st string
	var memberID *string
	if err := tx.QueryRow(ctx,
		`SELECT status, member_id::text FROM dues_invoices WHERE id = $1::uuid FOR UPDATE`,
		*t.InvoiceID).Scan(&st, &memberID); err != nil {
		return false, err
	}
	if t.MemberID == nil {
		t.MemberID = memberID // statements fetch by member: attribute the refund
	}
	if st == "waived" {
		if cerr := tx.Commit(ctx); cerr != nil {
			return false, cerr
		}
		return false, ErrInvoiceNotPayable
	}
	var refunded int64
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(-sum(amount), 0) FROM transactions
		WHERE invoice_id = $1::uuid AND provider = $2
		  AND provider_reference_id = $3 AND amount < 0`,
		*t.InvoiceID, t.Provider, t.ProviderReferenceID).Scan(&refunded); err != nil {
		return false, err
	}
	delta := cumulative - refunded
	if delta <= 0 {
		// Everything in this event is already on the books (replays, or a
		// same-total re-send): keep the claim, post nothing.
		return false, tx.Commit(ctx)
	}
	// Same clamp the PayPal path has: never post a refund past the invoice's
	// recorded net paid. If the original charge predates the webhook endpoint
	// there may be NO payment on the books — an uncapped delta would drive
	// paid negative and dunning would ask for more than face value.
	var netPaid int64
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(sum(amount), 0) FROM transactions
		WHERE invoice_id = $1::uuid AND provider_status != 'failed' AND currency = $2`,
		*t.InvoiceID, t.Currency).Scan(&netPaid); err != nil {
		return false, err
	}
	if netPaid <= 0 {
		// Nothing recorded to refund against: keep the claim (stop retries),
		// surface for the operator.
		if cerr := tx.Commit(ctx); cerr != nil {
			return false, cerr
		}
		return false, ErrExceedsPaid
	}
	if delta > netPaid {
		excess := delta - netPaid
		delta = netPaid
		n := fmt.Sprintf("refund clamped at net paid; %d minor units exceed recorded payments — enter manually", excess)
		if t.Notes != nil {
			n = *t.Notes + "; " + n
		}
		t.Notes = &n
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO transactions
		    (invoice_id, member_id, amount, currency, provider, provider_reference_id,
		     provider_status, recorded_by, occurred_at, notes)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8::uuid, $9, $10)`,
		t.InvoiceID, t.MemberID, -delta, t.Currency, t.Provider,
		t.ProviderReferenceID, t.ProviderStatus, t.RecordedBy, t.OccurredAt, t.Notes); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, recomputeInvoiceStatusSQL, *t.InvoiceID); err != nil {
		return false, err
	}
	return false, tx.Commit(ctx)
}

// MarkEventProcessed records a webhook event id so provider retries are idempotent.
func (r *DuesRepo) MarkEventProcessed(ctx context.Context, eventID string) error {
	_, err := r.db.Exec(ctx, `INSERT INTO processed_events (provider_event_id) VALUES ($1) ON CONFLICT DO NOTHING`, eventID)
	return err
}

// OverdueReminder is an overdue invoice with the data needed to email its
// member a dues reminder and track escalation.
type OverdueReminder struct {
	InvoiceID      string
	MemberName     string
	MemberEmail    string
	AmountMinor    int64
	RemainingMinor int64 // amount minus same-currency net payments: what to dun for
	Currency       string
	PeriodLabel    string
	DueDate        time.Time
	ReminderStage  int
}

// OverdueForReminders returns overdue invoices whose member has an email on
// file, along with the reminder stage already sent, for the nightly escalation.
func (r *DuesRepo) OverdueForReminders(ctx context.Context) ([]OverdueReminder, error) {
	rows, err := r.db.Query(ctx, `
		SELECT di.id::text, m.display_name, m.email, di.amount,
		       di.amount - (SELECT coalesce(sum(t.amount),0) FROM transactions t
		                    WHERE t.invoice_id = di.id AND t.provider_status != 'failed'
		                      AND t.currency = di.currency) AS remaining,
		       di.currency, di.period_label, di.due_date, di.reminder_stage
		FROM dues_invoices di
		JOIN members m ON m.id = di.member_id
		WHERE (di.status = 'overdue' OR (di.status = 'partial' AND di.due_date < CURRENT_DATE))
		  AND m.email IS NOT NULL AND m.email <> ''
		ORDER BY di.due_date
		LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OverdueReminder
	for rows.Next() {
		var rem OverdueReminder
		if err := rows.Scan(&rem.InvoiceID, &rem.MemberName, &rem.MemberEmail,
			&rem.AmountMinor, &rem.RemainingMinor, &rem.Currency, &rem.PeriodLabel, &rem.DueDate, &rem.ReminderStage); err != nil {
			return nil, err
		}
		out = append(out, rem)
	}
	return out, rows.Err()
}

// AdvanceReminderStage records that the given reminder stage was sent for an
// invoice, stamping the send time so the next escalation is spaced correctly.
func (r *DuesRepo) AdvanceReminderStage(ctx context.Context, invoiceID string, stage int) error {
	_, err := r.db.Exec(ctx,
		`UPDATE dues_invoices SET reminder_stage = $2, last_reminder_at = now() WHERE id = $1::uuid`,
		invoiceID, stage)
	return err
}

// OverdueInvoicesForEmail returns overdue (and past-due partial) invoices for the admin digest.
func (r *DuesRepo) OverdueInvoicesForEmail(ctx context.Context) ([]model.DuesInvoice, error) {
	rows, err := r.db.Query(ctx, `
		SELECT di.id::text, di.member_id::text, di.amount, di.currency, di.period_label,
		       di.due_date, di.status, di.notes, di.created_at, di.updated_at,
		       m.display_name
		FROM dues_invoices di
		JOIN members m ON m.id = di.member_id
		WHERE di.status = 'overdue' OR (di.status = 'partial' AND di.due_date < CURRENT_DATE)
		ORDER BY di.due_date
		LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var invs []model.DuesInvoice
	for rows.Next() {
		var inv model.DuesInvoice
		if err := rows.Scan(&inv.ID, &inv.MemberID, &inv.AmountMinor, &inv.Currency,
			&inv.PeriodLabel, &inv.DueDate, &inv.Status, &inv.Notes,
			&inv.CreatedAt, &inv.UpdatedAt, &inv.MemberName); err != nil {
			return nil, err
		}
		invs = append(invs, inv)
	}
	return invs, rows.Err()
}

// SetInstallments replaces an invoice's installment schedule in one transaction.
// Each entry gets a sequential seq. An empty list clears the schedule.
func (r *DuesRepo) SetInstallments(ctx context.Context, invoiceID string, plan []model.InvoiceInstallment) error {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `DELETE FROM invoice_installments WHERE invoice_id = $1::uuid`, invoiceID); err != nil {
		return err
	}
	for i, p := range plan {
		if _, err := tx.Exec(ctx, `
			INSERT INTO invoice_installments (invoice_id, amount, due_date, seq)
			VALUES ($1::uuid, $2, $3::date, $4)`, invoiceID, p.AmountMinor, p.DueDate, i+1); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ListInstallments returns an invoice's schedule in order.
func (r *DuesRepo) ListInstallments(ctx context.Context, invoiceID string) ([]model.InvoiceInstallment, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id::text, invoice_id::text, amount, due_date, seq
		FROM invoice_installments WHERE invoice_id = $1::uuid ORDER BY seq`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.InvoiceInstallment{}
	for rows.Next() {
		var p model.InvoiceInstallment
		if err := rows.Scan(&p.ID, &p.InvoiceID, &p.AmountMinor, &p.DueDate, &p.Seq); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
