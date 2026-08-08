//go:build integration

// Counterparty billing (migration 0036) at the database boundary: contact
// invoices post to income.services and keep the AR invariant, vendor bills
// accrue/pay/void with balanced entries, paid bills freeze, funds refuse to
// overdraw, and AP aging buckets open bills.
package repo_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/model"
	"quorum/internal/repo"
)

func newContact(t *testing.T, pool *pgxpool.Pool, createdBy string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO contacts (name, category, created_by)
		VALUES ($1, 'vendor', $2::uuid) RETURNING id::text`,
		uniq("Contact"), createdBy).Scan(&id); err != nil {
		t.Fatalf("create contact: %v", err)
	}
	return id
}

func accountIDByRule(t *testing.T, pool *pgxpool.Pool, key string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `SELECT gl_rule($1)::text`, key).Scan(&id); err != nil {
		t.Fatalf("gl_rule(%s): %v", key, err)
	}
	return id
}

func acctBalance(t *testing.T, pool *pgxpool.Pool, accountID, currency string) int64 {
	t.Helper()
	var bal int64
	if err := pool.QueryRow(context.Background(),
		`SELECT gl_balance($1::uuid, $2)`, accountID, currency).Scan(&bal); err != nil {
		t.Fatalf("gl_balance: %v", err)
	}
	return bal
}

func TestIntegration_ContactInvoices(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	gl := repo.NewGLRepo(pool)
	dues := repo.NewDuesRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)
	contact := newContact(t, pool, uid)

	svcAcct := accountIDByRule(t, pool, "income.services")
	duesAcct := accountIDByRule(t, pool, "income.dues")
	recvAcct := accountIDByRule(t, pool, "receivable")
	svcBefore := acctBalance(t, pool, svcAcct, "USD")
	duesBefore := acctBalance(t, pool, duesAcct, "USD")
	recvBefore := acctBalance(t, pool, recvAcct, "USD")

	// A contact invoice goes through the same batch path as member invoices.
	period := uniq("SVC")
	created, err := dues.CreateInvoiceBatch(ctx, []*model.DuesInvoice{{
		ContactID: &contact, AmountMinor: 25000, Currency: "USD",
		PeriodLabel: period, DueDate: time.Date(2030, 1, 31, 0, 0, 0, 0, time.UTC), Status: "pending",
	}})
	if err != nil {
		t.Fatalf("contact invoice: %v", err)
	}
	if len(created) != 1 || created[0].ContactID == nil || *created[0].ContactID != contact {
		t.Fatalf("created = %+v, want contact-attached invoice", created)
	}
	if created[0].MemberID != "" {
		t.Fatalf("member_id = %q, want empty for contact invoice", created[0].MemberID)
	}

	// Posted to income.services, NOT income.dues.
	if got := acctBalance(t, pool, svcAcct, "USD") - svcBefore; got != -25000 {
		t.Fatalf("service income delta = %d, want -25000 (credit)", got)
	}
	if got := acctBalance(t, pool, duesAcct, "USD") - duesBefore; got != 0 {
		t.Fatalf("dues income moved by %d for a contact invoice", got)
	}
	if got := acctBalance(t, pool, recvAcct, "USD") - recvBefore; got != 25000 {
		t.Fatalf("receivable delta = %d, want 25000 (debit)", got)
	}

	// The AR invariant still holds with contact invoices in the subledger.
	rows, err := gl.Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("reconcile mismatch with contact invoice: %+v", rows)
	}

	// ListInvoices labels the counterparty and carries contact_id.
	got, _, err := dues.ListInvoices(ctx, repo.InvoiceFilter{PeriodLabel: period, Limit: 5})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, inv := range got {
		if inv.ID == created[0].ID {
			found = true
			if inv.ContactID == nil || !strings.HasSuffix(inv.MemberName, "(contact)") {
				t.Fatalf("listed invoice = %+v, want contact-labeled", inv)
			}
		}
	}
	if !found {
		t.Fatalf("contact invoice missing from list")
	}

	// Exactly-one-counterparty is a DB-level guarantee.
	if _, err := pool.Exec(ctx, `
		INSERT INTO dues_invoices (member_id, contact_id, amount, currency, period_label, due_date)
		VALUES (NULL, NULL, 100, 'USD', 'BAD', current_date)`); err == nil {
		t.Fatal("invoice with no counterparty was accepted")
	}
}

func TestIntegration_VendorBills(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	bills := repo.NewBillsRepo(pool)
	gl := repo.NewGLRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)
	vendor := newContact(t, pool, uid)

	// A dedicated expense account so balances are isolated from other runs.
	var expAcct, expCode string
	if err := pool.QueryRow(ctx, `
		INSERT INTO accounts (code, name, type)
		VALUES ((SELECT (max(code::int) + 1)::text FROM accounts WHERE code ~ '^[0-9]+$'), $1, 'expense')
		RETURNING id::text, code`, uniq("Test Expense")).Scan(&expAcct, &expCode); err != nil {
		t.Fatalf("expense account: %v", err)
	}
	apAcct := accountIDByRule(t, pool, "payable")
	apBefore := acctBalance(t, pool, apAcct, "USD")

	memo := "printer toner"
	bill, err := bills.Create(ctx, &model.Bill{
		ContactID: vendor, Amount: 7500, Currency: "USD", Memo: &memo,
		ExpenseAccountID: expAcct, DueDateStr: "2030-06-30",
	}, uid)
	if err != nil {
		t.Fatalf("create bill: %v", err)
	}
	if bill.Status != "open" {
		t.Fatalf("status = %s, want open", bill.Status)
	}

	// Accrual: DR expense / CR A/P — no cash touched yet.
	if got := acctBalance(t, pool, expAcct, "USD"); got != 7500 {
		t.Fatalf("expense after accrual = %d, want 7500", got)
	}
	if got := acctBalance(t, pool, apAcct, "USD") - apBefore; got != -7500 {
		t.Fatalf("A/P after accrual = %d, want -7500 (credit)", got)
	}

	// The unpaid bill's expense must NOT appear on the cash-basis statement
	// (accrual entry touches no cash account).
	cashStmt, err := gl.StatementCash(ctx, "1900-01-01", "2100-01-01")
	if err != nil {
		t.Fatalf("cash statement: %v", err)
	}
	for _, row := range cashStmt {
		if row.Code == expCode && row.Balance != 0 {
			t.Fatalf("unpaid bill leaked into cash-basis statement: %+v", row)
		}
	}

	// AP aging sees the open bill as current (due 2030).
	aging, err := bills.APAging(ctx, "2026-01-01")
	if err != nil {
		t.Fatalf("aging: %v", err)
	}
	var sawCurrent bool
	for _, a := range aging {
		if a.Currency == "USD" && a.Bucket == "current" {
			sawCurrent = true
		}
	}
	if !sawCurrent {
		t.Fatalf("aging missing current bucket: %+v", aging)
	}

	// Pay from provider-routed operating cash.
	prov := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, strings.ToLower(uniq("prov")))
	paid, err := bills.Pay(ctx, bill.ID, "", prov)
	if err != nil {
		t.Fatalf("pay: %v", err)
	}
	if paid.Status != "paid" || paid.PaidAt == nil {
		t.Fatalf("paid bill = %+v", paid)
	}
	if got := acctBalance(t, pool, apAcct, "USD") - apBefore; got != 0 {
		t.Fatalf("A/P after payment = %d, want 0", got)
	}

	// Paid bills are frozen by trigger and refuse a second payment.
	if _, err := pool.Exec(ctx, `UPDATE bills SET memo = 'tamper' WHERE id = $1::uuid`, bill.ID); err == nil {
		t.Fatal("paid bill accepted an update")
	}
	if _, err := bills.Pay(ctx, bill.ID, "", prov); err == nil {
		t.Fatal("paid bill accepted a second payment")
	}

	// Deleting any bill is refused outright.
	if _, err := pool.Exec(ctx, `DELETE FROM bills WHERE id = $1::uuid`, bill.ID); err == nil {
		t.Fatal("bill delete was accepted")
	}
}

func TestIntegration_VendorBills_FundPayAndVoid(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	bills := repo.NewBillsRepo(pool)
	funds := repo.NewFundsRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)
	vendor := newContact(t, pool, uid)

	var expAcct string
	if err := pool.QueryRow(ctx, `
		INSERT INTO accounts (code, name, type)
		VALUES ((SELECT (max(code::int) + 1)::text FROM accounts WHERE code ~ '^[0-9]+$'), $1, 'expense')
		RETURNING id::text`, uniq("Fund Expense")).Scan(&expAcct); err != nil {
		t.Fatalf("expense account: %v", err)
	}

	fund, err := funds.CreateFund(ctx, uniq("Fund"), nil, 1, nil, uid)
	if err != nil {
		t.Fatalf("fund: %v", err)
	}
	if err := funds.Transfer(ctx, fund.ID, "in", 5000, "USD", "seed"); err != nil {
		t.Fatalf("fund seed: %v", err)
	}

	// A 6000 bill cannot be paid from a 5000 fund.
	bill, err := bills.Create(ctx, &model.Bill{
		ContactID: vendor, Amount: 6000, Currency: "USD", ExpenseAccountID: expAcct,
	}, uid)
	if err != nil {
		t.Fatalf("bill: %v", err)
	}
	if _, err := bills.Pay(ctx, bill.ID, fund.ID, ""); err == nil {
		t.Fatal("overdraft fund payment was accepted")
	}

	// A 4000 bill can, and the fund balance drops.
	small, err := bills.Create(ctx, &model.Bill{
		ContactID: vendor, Amount: 4000, Currency: "USD", ExpenseAccountID: expAcct,
	}, uid)
	if err != nil {
		t.Fatalf("small bill: %v", err)
	}
	if _, err := bills.Pay(ctx, small.ID, fund.ID, ""); err != nil {
		t.Fatalf("fund pay: %v", err)
	}
	got, err := funds.GetFund(ctx, fund.ID)
	if err != nil {
		t.Fatalf("get fund: %v", err)
	}
	var usd int64
	for _, b := range got.Balances {
		if b.Currency == "USD" {
			usd = b.Balance
		}
	}
	if usd != 1000 {
		t.Fatalf("fund balance after payment = %d, want 1000", usd)
	}

	// Void reverses the 6000 accrual and closes the bill.
	expBefore := acctBalance(t, pool, expAcct, "USD")
	voided, err := bills.Void(ctx, bill.ID)
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	if voided.Status != "void" {
		t.Fatalf("status = %s, want void", voided.Status)
	}
	if got := expBefore - acctBalance(t, pool, expAcct, "USD"); got != 6000 {
		t.Fatalf("void reversed %d of expense, want 6000", got)
	}
	if _, err := bills.Void(ctx, bill.ID); err == nil {
		t.Fatal("void of a void bill was accepted")
	}
}
