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
