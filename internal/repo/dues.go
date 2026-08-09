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

// ListInvoices returns a page of invoices matching the filter, plus the total count.
func (r *DuesRepo) ListInvoices(ctx context.Context, f InvoiceFilter) ([]model.DuesInvoice, int, error) {
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
		idx++
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	query := fmt.Sprintf(`
		SELECT COUNT(*) OVER() AS total_count,
		       di.id::text, coalesce(di.member_id::text, ''), di.amount, di.currency, di.period_label,
		       di.due_date, di.status, di.notes, di.created_at, di.updated_at,
		       coalesce(m.display_name, c.name || ' (contact)'), di.contact_id::text
		FROM dues_invoices di
		LEFT JOIN members m ON m.id = di.member_id
		LEFT JOIN contacts c ON c.id = di.contact_id
		%s
		ORDER BY di.due_date DESC, 12
		LIMIT $%d OFFSET $%d`, where, idx, idx+1)
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
			&inv.CreatedAt, &inv.UpdatedAt, &inv.MemberName, &inv.ContactID); err != nil {
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

// GetInvoice returns the invoice with the given id and its transactions, or pgx.ErrNoRows.
func (r *DuesRepo) GetInvoice(ctx context.Context, id string) (*model.DuesInvoice, error) {
	var inv model.DuesInvoice
	err := r.db.QueryRow(ctx, `
		SELECT di.id::text, coalesce(di.member_id::text, ''), di.amount, di.currency, di.period_label,
		       di.due_date, di.status, di.notes, di.created_at, di.updated_at,
		       coalesce(m.display_name, c.name || ' (contact)'), di.contact_id::text
		FROM dues_invoices di
		LEFT JOIN members m ON m.id = di.member_id
		LEFT JOIN contacts c ON c.id = di.contact_id
		WHERE di.id = $1::uuid`, id).
		Scan(&inv.ID, &inv.MemberID, &inv.AmountMinor, &inv.Currency,
			&inv.PeriodLabel, &inv.DueDate, &inv.Status, &inv.Notes,
			&inv.CreatedAt, &inv.UpdatedAt, &inv.MemberName, &inv.ContactID)
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
func (r *DuesRepo) BatchUpdateStatus(ctx context.Context, ids []string, status string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := r.db.Exec(ctx,
		`UPDATE dues_invoices SET status = $1, updated_at = now() WHERE id = ANY($2::uuid[])`,
		status, ids)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
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
const recomputeInvoiceStatusSQL = `
	UPDATE dues_invoices SET
		status = CASE
			WHEN (SELECT coalesce(sum(amount),0) FROM transactions WHERE invoice_id = $1::uuid AND provider_status != 'failed' AND currency = dues_invoices.currency)
			     >= amount THEN 'paid'
			WHEN (SELECT coalesce(sum(amount),0) FROM transactions WHERE invoice_id = $1::uuid AND provider_status != 'failed' AND currency = dues_invoices.currency)
			     > 0 THEN 'partial'
			WHEN due_date < CURRENT_DATE AND status NOT IN ('paid','waived') THEN 'overdue'
			ELSE status
		END,
		updated_at = now()
	WHERE id = $1::uuid AND status NOT IN ('paid','waived')`

// RecomputeInvoiceStatus recomputes an invoice's status from its same-currency transactions.
func (r *DuesRepo) RecomputeInvoiceStatus(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, recomputeInvoiceStatusSQL, id)
	return err
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
	if limit <= 0 || limit > 200 {
		limit = 100
	}

	query := fmt.Sprintf(`
		SELECT COUNT(*) OVER() AS total_count,
		       t.id::text, t.invoice_id::text, t.member_id::text, t.amount, t.currency,
		       t.provider, t.provider_reference_id, t.provider_status, t.payment_method_type,
		       t.recorded_by::text, t.occurred_at, t.notes,
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
			&t.RecordedBy, &t.OccurredAt, &t.Notes, &t.MemberName); err != nil {
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
	if t.InvoiceID != nil {
		var st string
		if err := tx.QueryRow(ctx, `SELECT status FROM dues_invoices WHERE id = $1::uuid`, *t.InvoiceID).Scan(&st); err == nil && st == "waived" {
			if cerr := tx.Commit(ctx); cerr != nil {
				return false, cerr
			}
			return false, ErrInvoiceNotPayable
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

// MarkEventProcessed records a webhook event id so provider retries are idempotent.
func (r *DuesRepo) MarkEventProcessed(ctx context.Context, eventID string) error {
	_, err := r.db.Exec(ctx, `INSERT INTO processed_events (provider_event_id) VALUES ($1) ON CONFLICT DO NOTHING`, eventID)
	return err
}

// OverdueReminder is an overdue invoice with the data needed to email its
// member a dues reminder and track escalation.
type OverdueReminder struct {
	InvoiceID     string
	MemberName    string
	MemberEmail   string
	AmountMinor   int64
	Currency      string
	PeriodLabel   string
	DueDate       time.Time
	ReminderStage int
}

// OverdueForReminders returns overdue invoices whose member has an email on
// file, along with the reminder stage already sent, for the nightly escalation.
func (r *DuesRepo) OverdueForReminders(ctx context.Context) ([]OverdueReminder, error) {
	rows, err := r.db.Query(ctx, `
		SELECT di.id::text, m.display_name, m.email, di.amount, di.currency,
		       di.period_label, di.due_date, di.reminder_stage
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
			&rem.AmountMinor, &rem.Currency, &rem.PeriodLabel, &rem.DueDate, &rem.ReminderStage); err != nil {
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
