package repo

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

// PaymentReportsRepo manages member self-reported payments: a "I sent it via
// Zelle/check" signal that becomes an officer confirmation queue. Confirming
// records the real transaction (elsewhere); this repo tracks the queue state.
type PaymentReportsRepo struct {
	db *pgxpool.Pool
}

// NewPaymentReportsRepo constructs the repo.
func NewPaymentReportsRepo(db *pgxpool.Pool) *PaymentReportsRepo {
	return &PaymentReportsRepo{db: db}
}

// Create files a pending report for an invoice. The partial unique index means
// a second open report for the same invoice raises a conflict, which the
// handler maps to a friendly "already reported".
func (r *PaymentReportsRepo) Create(ctx context.Context, invoiceID, memberID, method, reference, note string) (*model.PaymentReport, error) {
	var id string
	err := r.db.QueryRow(ctx, `
		INSERT INTO payment_reports (invoice_id, member_id, method, reference, note)
		VALUES ($1::uuid, nullif($2,'')::uuid, $3, nullif($4,''), nullif($5,''))
		RETURNING id::text`,
		invoiceID, memberID, method, reference, note).Scan(&id)
	if err != nil {
		return nil, err
	}
	return r.get(ctx, id)
}

func (r *PaymentReportsRepo) get(ctx context.Context, id string) (*model.PaymentReport, error) {
	var p model.PaymentReport
	err := r.db.QueryRow(ctx, `
		SELECT pr.id::text, pr.invoice_id::text, coalesce(pr.member_id::text,''),
		       coalesce(m.display_name,''), pr.method, coalesce(pr.reference,''),
		       coalesce(pr.note,''), pr.status, pr.reported_at,
		       di.amount, di.currency, di.period_label
		FROM payment_reports pr
		JOIN dues_invoices di ON di.id = pr.invoice_id
		LEFT JOIN members m ON m.id = pr.member_id
		WHERE pr.id = $1::uuid`, id).
		Scan(&p.ID, &p.InvoiceID, &p.MemberID, &p.MemberName, &p.Method, &p.Reference,
			&p.Note, &p.Status, &p.ReportedAt, &p.AmountMinor, &p.Currency, &p.PeriodLabel)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListPending returns the officer queue: open reports, oldest first.
func (r *PaymentReportsRepo) ListPending(ctx context.Context, limit int) ([]model.PaymentReport, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := r.db.Query(ctx, `
		SELECT pr.id::text, pr.invoice_id::text, coalesce(pr.member_id::text,''),
		       coalesce(m.display_name,''), pr.method, coalesce(pr.reference,''),
		       coalesce(pr.note,''), pr.status, pr.reported_at,
		       di.amount, di.currency, di.period_label
		FROM payment_reports pr
		JOIN dues_invoices di ON di.id = pr.invoice_id
		LEFT JOIN members m ON m.id = pr.member_id
		WHERE pr.status = 'pending'
		ORDER BY pr.reported_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PaymentReport
	for rows.Next() {
		var p model.PaymentReport
		if err := rows.Scan(&p.ID, &p.InvoiceID, &p.MemberID, &p.MemberName, &p.Method,
			&p.Reference, &p.Note, &p.Status, &p.ReportedAt, &p.AmountMinor, &p.Currency, &p.PeriodLabel); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PendingCount is the queue depth, for the dashboard/nav badge.
func (r *PaymentReportsRepo) PendingCount(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT count(*) FROM payment_reports WHERE status = 'pending'`).Scan(&n)
	return n, err
}

// Resolve marks a report confirmed or dismissed. Returns the invoice id so the
// handler can record the real transaction on confirm.
// ConfirmOutcome is what ConfirmAndPost did, for the handler's response.
type ConfirmOutcome struct {
	InvoiceID   string
	Posted      bool   // false: confirmed but nothing owed / invoice waived
	Reason      string // why nothing posted ("invoice is waived", "nothing owed")
	AmountMinor int64  // the amount actually recorded
}

// ConfirmAndPost claims the pending report and records the payment in ONE
// transaction: claim → lock the invoice → compute the remaining same-currency
// balance → insert a transaction for exactly that remainder → recompute the
// invoice status. A report against a waived or already-settled invoice is
// confirmed (cleared from the queue) without posting anything, and any
// failure rolls the claim back so the report stays in the queue for a retry.
func (r *PaymentReportsRepo) ConfirmAndPost(ctx context.Context, id, resolvedBy string) (*ConfirmOutcome, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	out := &ConfirmOutcome{}
	if err := tx.QueryRow(ctx, `
		UPDATE payment_reports
		SET status = 'confirmed', resolved_at = now(), resolved_by = $2::uuid
		WHERE id = $1::uuid AND status = 'pending'
		RETURNING invoice_id::text`, id, resolvedBy).Scan(&out.InvoiceID); err != nil {
		return nil, err // pgx.ErrNoRows → no longer pending
	}
	var (
		status, currency string
		amount           int64
		memberID         *string
	)
	// Lock the invoice row so a webhook landing mid-confirm can't double-post.
	if err := tx.QueryRow(ctx, `
		SELECT status, currency, amount, member_id::text
		FROM dues_invoices WHERE id = $1::uuid FOR UPDATE`,
		out.InvoiceID).Scan(&status, &currency, &amount, &memberID); err != nil {
		return nil, err
	}
	if status == "waived" {
		// Posting onto a waived invoice would corrupt A/R; clear the report.
		out.Reason = "invoice is waived"
		return out, tx.Commit(ctx)
	}
	var paid int64
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(sum(amount),0) FROM transactions
		WHERE invoice_id = $1::uuid AND provider_status != 'failed' AND currency = $2`,
		out.InvoiceID, currency).Scan(&paid); err != nil {
		return nil, err
	}
	remaining := amount - paid
	if remaining <= 0 {
		// Settled while the report sat in the queue (webhook, manual entry):
		// confirming must not double-pay it.
		out.Reason = "nothing owed"
		return out, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO transactions (invoice_id, member_id, amount, currency, provider, provider_status, recorded_by, occurred_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, 'reported', 'succeeded', $5::uuid, now())`,
		out.InvoiceID, memberID, remaining, currency, resolvedBy); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, recomputeInvoiceStatusSQL, out.InvoiceID); err != nil {
		return nil, err
	}
	out.Posted = true
	out.AmountMinor = remaining
	return out, tx.Commit(ctx)
}

func (r *PaymentReportsRepo) Resolve(ctx context.Context, id, status, resolvedBy string) (invoiceID string, err error) {
	err = r.db.QueryRow(ctx, `
		UPDATE payment_reports
		SET status = $2, resolved_at = now(), resolved_by = $3::uuid
		WHERE id = $1::uuid AND status = 'pending'
		RETURNING invoice_id::text`, id, status, resolvedBy).Scan(&invoiceID)
	if err == pgx.ErrNoRows {
		return "", pgx.ErrNoRows
	}
	return invoiceID, err
}
