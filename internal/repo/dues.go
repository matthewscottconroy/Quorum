package repo

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
)

type DuesRepo struct {
	db *pgxpool.Pool
}

func NewDuesRepo(db *pgxpool.Pool) *DuesRepo {
	return &DuesRepo{db: db}
}

type InvoiceFilter struct {
	MemberID    string
	Status      string
	PeriodLabel string
	Limit       int
	Offset      int
}

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
		       di.id::text, di.member_id::text, di.amount, di.currency, di.period_label,
		       di.due_date, di.status, di.notes, di.created_at, di.updated_at,
		       m.display_name
		FROM dues_invoices di
		JOIN members m ON m.id = di.member_id
		%s
		ORDER BY di.due_date DESC, m.display_name
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
		if err := rows.Scan(&total, &inv.ID, &inv.MemberID, &inv.Amount, &inv.Currency,
			&inv.PeriodLabel, &inv.DueDate, &inv.Status, &inv.Notes,
			&inv.CreatedAt, &inv.UpdatedAt, &inv.MemberName); err != nil {
			return nil, 0, err
		}
		invoices = append(invoices, inv)
	}
	return invoices, total, rows.Err()
}

func (r *DuesRepo) GetInvoice(ctx context.Context, id string) (*model.DuesInvoice, error) {
	var inv model.DuesInvoice
	err := r.db.QueryRow(ctx, `
		SELECT di.id::text, di.member_id::text, di.amount, di.currency, di.period_label,
		       di.due_date, di.status, di.notes, di.created_at, di.updated_at,
		       m.display_name
		FROM dues_invoices di
		JOIN members m ON m.id = di.member_id
		WHERE di.id = $1::uuid`, id).
		Scan(&inv.ID, &inv.MemberID, &inv.Amount, &inv.Currency,
			&inv.PeriodLabel, &inv.DueDate, &inv.Status, &inv.Notes,
			&inv.CreatedAt, &inv.UpdatedAt, &inv.MemberName)
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

func (r *DuesRepo) CreateInvoice(ctx context.Context, inv *model.DuesInvoice) (*model.DuesInvoice, error) {
	var created model.DuesInvoice
	err := r.db.QueryRow(ctx, `
		INSERT INTO dues_invoices (member_id, amount, currency, period_label, due_date, status, notes)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
		RETURNING id::text, member_id::text, amount, currency, period_label, due_date, status, notes, created_at, updated_at`,
		inv.MemberID, inv.Amount, inv.Currency, inv.PeriodLabel, inv.DueDate, inv.Status, inv.Notes).
		Scan(&created.ID, &created.MemberID, &created.Amount, &created.Currency,
			&created.PeriodLabel, &created.DueDate, &created.Status, &created.Notes,
			&created.CreatedAt, &created.UpdatedAt)
	return &created, err
}

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
			INSERT INTO dues_invoices (member_id, amount, currency, period_label, due_date, status, notes)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
			RETURNING id::text, member_id::text, amount, currency, period_label, due_date, status, notes, created_at, updated_at`,
			inv.MemberID, inv.Amount, inv.Currency, inv.PeriodLabel, inv.DueDate, inv.Status, inv.Notes).
			Scan(&c.ID, &c.MemberID, &c.Amount, &c.Currency, &c.PeriodLabel, &c.DueDate, &c.Status, &c.Notes, &c.CreatedAt, &c.UpdatedAt)
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

func (r *DuesRepo) UpdateInvoiceStatus(ctx context.Context, id, status string, notes *string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE dues_invoices SET status = $1, notes = coalesce($2, notes), updated_at = now()
		WHERE id = $3::uuid`, status, notes, id)
	return err
}

func (r *DuesRepo) RecomputeInvoiceStatus(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE dues_invoices SET
			status = CASE
				WHEN (SELECT coalesce(sum(amount),0) FROM transactions WHERE invoice_id = $1::uuid AND provider_status != 'failed')
				     >= amount THEN 'paid'
				WHEN (SELECT coalesce(sum(amount),0) FROM transactions WHERE invoice_id = $1::uuid AND provider_status != 'failed')
				     > 0 THEN 'partial'
				WHEN due_date < CURRENT_DATE AND status NOT IN ('paid','waived') THEN 'overdue'
				ELSE status
			END,
			updated_at = now()
		WHERE id = $1::uuid AND status NOT IN ('paid','waived')`, id)
	return err
}

func (r *DuesRepo) MarkOverdue(ctx context.Context) (int64, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE dues_invoices SET status = 'overdue', updated_at = now()
		WHERE due_date < CURRENT_DATE AND status = 'pending'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (r *DuesRepo) CountByStatus(ctx context.Context, status string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT count(*) FROM dues_invoices WHERE status = $1`, status).Scan(&n)
	return n, err
}

// Transactions

type TransactionFilter struct {
	InvoiceID string
	MemberID  string
	Limit     int
	Offset    int
}

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
		if err := rows.Scan(&total, &t.ID, &t.InvoiceID, &t.MemberID, &t.Amount, &t.Currency,
			&t.Provider, &t.ProviderReferenceID, &t.ProviderStatus, &t.PaymentMethodType,
			&t.RecordedBy, &t.OccurredAt, &t.Notes, &t.MemberName); err != nil {
			return nil, 0, err
		}
		txs = append(txs, t)
	}
	return txs, total, rows.Err()
}

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
		t.InvoiceID, t.MemberID, t.Amount, t.Currency, t.Provider,
		t.ProviderReferenceID, t.ProviderStatus, t.PaymentMethodType,
		t.RecordedBy, t.OccurredAt, t.Notes).
		Scan(&created.ID, &created.InvoiceID, &created.MemberID, &created.Amount, &created.Currency,
			&created.Provider, &created.ProviderReferenceID, &created.ProviderStatus, &created.PaymentMethodType,
			&created.RecordedBy, &created.OccurredAt, &created.Notes)
	return &created, err
}

func (r *DuesRepo) FindInvoiceByProviderRef(ctx context.Context, providerRef string) (string, error) {
	var id string
	err := r.db.QueryRow(ctx, `
		SELECT invoice_id::text FROM transactions
		WHERE provider_reference_id = $1 AND invoice_id IS NOT NULL
		LIMIT 1`, providerRef).Scan(&id)
	return id, err
}

func (r *DuesRepo) EventProcessed(ctx context.Context, eventID string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM processed_events WHERE provider_event_id = $1)`, eventID).Scan(&exists)
	return exists, err
}

func (r *DuesRepo) MarkEventProcessed(ctx context.Context, eventID string) error {
	_, err := r.db.Exec(ctx, `INSERT INTO processed_events (provider_event_id) VALUES ($1) ON CONFLICT DO NOTHING`, eventID)
	return err
}

func (r *DuesRepo) OverdueInvoicesForEmail(ctx context.Context) ([]model.DuesInvoice, error) {
	rows, err := r.db.Query(ctx, `
		SELECT di.id::text, di.member_id::text, di.amount, di.currency, di.period_label,
		       di.due_date, di.status, di.notes, di.created_at, di.updated_at,
		       m.display_name
		FROM dues_invoices di
		JOIN members m ON m.id = di.member_id
		WHERE di.status = 'overdue'
		ORDER BY di.due_date
		LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var invs []model.DuesInvoice
	for rows.Next() {
		var inv model.DuesInvoice
		if err := rows.Scan(&inv.ID, &inv.MemberID, &inv.Amount, &inv.Currency,
			&inv.PeriodLabel, &inv.DueDate, &inv.Status, &inv.Notes,
			&inv.CreatedAt, &inv.UpdatedAt, &inv.MemberName); err != nil {
			return nil, err
		}
		invs = append(invs, inv)
	}
	return invs, rows.Err()
}
