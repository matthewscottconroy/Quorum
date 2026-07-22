//go:build integration

// Integration tests exercise the repo layer against a real PostgreSQL instance,
// covering correctness invariants that the mocked handler/service tests cannot
// reach (transactional webhook idempotency, the empty-page COUNT fallback, the
// RowsAffected→ErrNoRows mapping, and the migration runner).
//
// They are excluded from the default `go test` by the `integration` build tag.
// Run with a throwaway database, e.g.:
//
//	QUORUM_TEST_DATABASE_URL=postgres://quorum:test@localhost:5432/quorum?sslmode=disable \
//	  go test -tags integration ./internal/repo/...
//
// If QUORUM_TEST_DATABASE_URL is unset the tests skip.
package repo_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/db"
	"quorum/internal/model"
	"quorum/internal/repo"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("QUORUM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("QUORUM_TEST_DATABASE_URL not set; skipping integration tests")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// makeInvoice creates a fresh member and a single pending invoice, returning the
// member id and invoice id. Each call uses a unique period label so tests filter
// only their own rows.
func makeInvoice(t *testing.T, pool *pgxpool.Pool, amountMinor int64, currency, period string) (memberID, invoiceID string) {
	t.Helper()
	ctx := context.Background()
	m, err := repo.NewMembersRepo(pool).Create(ctx, &model.Member{
		DisplayName: "Integration Test Member",
		Tier:        "standard",
		Status:      "active",
		JoinedAt:    time.Now(),
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	created, err := repo.NewDuesRepo(pool).CreateInvoiceBatch(ctx, []*model.DuesInvoice{{
		MemberID:    m.ID,
		AmountMinor: amountMinor,
		Currency:    currency,
		PeriodLabel: period,
		DueDate:     time.Now(),
		Status:      "pending",
	}})
	if err != nil || len(created) != 1 {
		t.Fatalf("create invoice: %v (n=%d)", err, len(created))
	}
	return m.ID, created[0].ID
}

// TestIntegration_RecordWebhookPayment_Dedup proves the payment-recording
// transaction is idempotent: a duplicate delivery (same claim key) records the
// payment exactly once and reports already=true, and the first recording flips
// the invoice to paid.
func TestIntegration_RecordWebhookPayment_Dedup(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dues := repo.NewDuesRepo(pool)

	_, invID := makeInvoice(t, pool, 10000, "USD", "dedup-"+t.Name())
	succeeded := "succeeded"
	tx := &model.Transaction{
		InvoiceID:      &invID,
		AmountMinor:    10000,
		Currency:       "USD",
		Provider:       "stripe",
		ProviderStatus: &succeeded,
		OccurredAt:     time.Now(),
	}
	claimKey := "evt_integration_" + invID

	already, err := dues.RecordWebhookPayment(ctx, claimKey, tx)
	if err != nil {
		t.Fatalf("first record: %v", err)
	}
	if already {
		t.Error("first delivery should not be already-processed")
	}

	// The invoice should now be paid.
	inv, err := dues.GetInvoice(ctx, invID)
	if err != nil {
		t.Fatalf("get invoice: %v", err)
	}
	if inv.Status != "paid" {
		t.Errorf("invoice status after full payment: got %q, want paid", inv.Status)
	}
	if len(inv.Transactions) != 1 {
		t.Fatalf("expected 1 transaction, got %d", len(inv.Transactions))
	}

	// Duplicate delivery: reported as already-processed, no second transaction.
	already, err = dues.RecordWebhookPayment(ctx, claimKey, tx)
	if err != nil {
		t.Fatalf("second record: %v", err)
	}
	if !already {
		t.Error("duplicate delivery must report already=true")
	}
	inv, _ = dues.GetInvoice(ctx, invID)
	if len(inv.Transactions) != 1 {
		t.Errorf("duplicate delivery must not add a transaction: got %d", len(inv.Transactions))
	}
}

// TestIntegration_RecordWebhookPayment_CrossCurrencyIgnored proves a payment in
// a different currency than the invoice does not count toward its paid status.
func TestIntegration_RecordWebhookPayment_CrossCurrencyIgnored(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dues := repo.NewDuesRepo(pool)

	_, invID := makeInvoice(t, pool, 10000, "USD", "xcur-"+t.Name())
	succeeded := "succeeded"
	// A JPY payment of 10000 minor units against a USD invoice.
	_, err := dues.RecordWebhookPayment(ctx, "evt_xcur_"+invID, &model.Transaction{
		InvoiceID:      &invID,
		AmountMinor:    10000,
		Currency:       "JPY",
		Provider:       "stripe",
		ProviderStatus: &succeeded,
		OccurredAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	inv, _ := dues.GetInvoice(ctx, invID)
	if inv.Status == "paid" {
		t.Error("a mismatched-currency payment must not mark the invoice paid")
	}
}

// TestIntegration_EmptyPageCountFallback proves the COUNT(*) OVER() fallback
// returns the correct total when the offset is past the end of the result set.
func TestIntegration_EmptyPageCountFallback(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dues := repo.NewDuesRepo(pool)
	period := "count-" + t.Name()

	memberID, _ := makeInvoice(t, pool, 100, "USD", period)
	// Two more invoices for the same member/period.
	_, err := dues.CreateInvoiceBatch(ctx, []*model.DuesInvoice{
		{MemberID: memberID, AmountMinor: 100, Currency: "USD", PeriodLabel: period, DueDate: time.Now(), Status: "pending"},
		{MemberID: memberID, AmountMinor: 100, Currency: "USD", PeriodLabel: period, DueDate: time.Now(), Status: "pending"},
	})
	if err != nil {
		t.Fatalf("create invoices: %v", err)
	}

	invs, total, err := dues.ListInvoices(ctx, repo.InvoiceFilter{MemberID: memberID, PeriodLabel: period, Limit: 10, Offset: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(invs) != 0 {
		t.Errorf("offset past end should return 0 rows, got %d", len(invs))
	}
	if total != 3 {
		t.Errorf("total from COUNT fallback: got %d, want 3", total)
	}
}

// TestIntegration_UpdateInvoiceStatus_NoRows proves the RowsAffected==0 →
// pgx.ErrNoRows mapping that the handler relies on to return 404.
func TestIntegration_UpdateInvoiceStatus_NoRows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dues := repo.NewDuesRepo(pool)

	err := dues.UpdateInvoiceStatus(ctx, "11111111-1111-1111-1111-111111111111", "waived", nil)
	if err != pgx.ErrNoRows {
		t.Errorf("updating a nonexistent invoice: got %v, want pgx.ErrNoRows", err)
	}
}

// TestIntegration_MigrateIdempotent proves the migration runner is safe to run
// repeatedly (testPool already ran it once) and records every migration.
func TestIntegration_MigrateIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate must be a no-op, got: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if n < 7 {
		t.Errorf("expected at least 7 recorded migrations, got %d", n)
	}
}
