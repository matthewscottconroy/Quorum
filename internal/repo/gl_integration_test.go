//go:build integration

// The general ledger's guarantees, tested adversarially at the database
// boundary: every subledger event posts a balanced entry, the journal
// refuses edits and unbalanced entries, and the AR invariant holds through
// payments, corrections, waives, and un-waives.
package repo_test

import (
	"context"
	"testing"

	"quorum/internal/model"
	"quorum/internal/repo"
)

func TestIntegration_GeneralLedger(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	gl := repo.NewGLRepo(pool)
	mr := repo.NewMembersRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)
	member := newMember(t, mr, uniq("tier"), "active")

	reconciled := func(label string) {
		t.Helper()
		rows, err := gl.Reconcile(ctx)
		if err != nil {
			t.Fatalf("%s reconcile: %v", label, err)
		}
		if len(rows) != 0 {
			t.Fatalf("%s: GL and subledger disagree: %+v", label, rows)
		}
	}
	balanceOf := func(code, currency string) int64 {
		t.Helper()
		var bal int64
		err := pool.QueryRow(ctx, `
			SELECT coalesce(sum(l.debit) - sum(l.credit), 0)
			FROM journal_lines l JOIN accounts a ON a.id = l.account_id
			WHERE a.code = $1 AND l.currency = $2`, code, currency).Scan(&bal)
		if err != nil {
			t.Fatalf("balance %s: %v", code, err)
		}
		return bal
	}

	arBefore := balanceOf("1300", "USD")
	cashBefore := balanceOf("1000", "USD")
	incomeBefore := balanceOf("4000", "USD")

	// Invoice -> DR AR / CR Income.
	var invID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO dues_invoices (member_id, amount, currency, period_label, due_date)
		VALUES ($1::uuid, 10000, 'USD', 'GL-TEST', current_date + 30) RETURNING id::text`,
		member).Scan(&invID); err != nil {
		t.Fatalf("invoice: %v", err)
	}
	if got := balanceOf("1300", "USD") - arBefore; got != 10000 {
		t.Fatalf("AR after invoice: %d, want 10000", got)
	}
	reconciled("after invoice")

	// Partial payment -> DR Cash / CR AR.
	if _, err := pool.Exec(ctx, `
		INSERT INTO transactions (invoice_id, member_id, amount, currency, provider, recorded_by)
		VALUES ($1::uuid, $2::uuid, 4000, 'USD', 'zelle', $3::uuid)`, invID, member, uid); err != nil {
		t.Fatalf("payment: %v", err)
	}
	if got := balanceOf("1000", "USD") - cashBefore; got != 4000 {
		t.Fatalf("cash after payment: %d, want 4000", got)
	}
	reconciled("after payment")

	// Correction (negative) reverses.
	if _, err := pool.Exec(ctx, `
		INSERT INTO transactions (invoice_id, member_id, amount, currency, provider, recorded_by)
		VALUES ($1::uuid, $2::uuid, -1000, 'USD', 'zelle', $3::uuid)`, invID, member, uid); err != nil {
		t.Fatalf("correction: %v", err)
	}
	if got := balanceOf("1000", "USD") - cashBefore; got != 3000 {
		t.Fatalf("cash after correction: %d, want 3000", got)
	}
	reconciled("after correction")

	// Waive writes off the remainder (10000 - 3000 = 7000); un-waive reverses.
	if _, err := pool.Exec(ctx, `UPDATE dues_invoices SET status = 'waived' WHERE id = $1::uuid`, invID); err != nil {
		t.Fatalf("waive: %v", err)
	}
	if got := balanceOf("1300", "USD") - arBefore; got != 0 {
		t.Fatalf("AR after waive: %d, want 0", got)
	}
	reconciled("after waive")
	if _, err := pool.Exec(ctx, `UPDATE dues_invoices SET status = 'pending' WHERE id = $1::uuid`, invID); err != nil {
		t.Fatalf("unwaive: %v", err)
	}
	if got := balanceOf("1300", "USD") - arBefore; got != 7000 {
		t.Fatalf("AR after unwaive: %d, want 7000", got)
	}
	reconciled("after unwaive")

	// Income reflects the invoice (10000) plus nothing else net of writeoff pair.
	if got := balanceOf("4000", "USD") - incomeBefore; got != -10000 { // income is credit-normal: balance negative
		t.Fatalf("income delta: %d, want -10000", got)
	}

	// The journal is append-only...
	if _, err := pool.Exec(ctx, `UPDATE journal_entries SET memo = 'tampered' WHERE source_id = $1::uuid`, invID); err == nil {
		t.Fatal("journal_entries UPDATE must be refused")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM journal_lines WHERE entry_id IN
		(SELECT id FROM journal_entries WHERE source_id = $1::uuid)`, invID); err == nil {
		t.Fatal("journal_lines DELETE must be refused")
	}
	// ...and refuses unbalanced entries at commit.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	var eid string
	if err := tx.QueryRow(ctx, `
		INSERT INTO journal_entries (memo, source_type) VALUES ('lopsided', 'manual')
		RETURNING id::text`).Scan(&eid); err != nil {
		t.Fatalf("entry: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO journal_lines (entry_id, account_id, currency, debit)
		VALUES ($1::uuid, gl_account('1000'), 'USD', 500)`, eid); err != nil {
		t.Fatalf("line: %v", err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("unbalanced journal entry must fail at COMMIT")
	}

	// Trial balance is internally consistent: total debits == total credits.
	tb, err := gl.TrialBalance(ctx)
	if err != nil {
		t.Fatalf("trial balance: %v", err)
	}
	sums := map[string][2]int64{}
	for _, b := range tb {
		s := sums[b.Currency]
		s[0] += b.Debits
		s[1] += b.Credits
		sums[b.Currency] = s
	}
	for cur, s := range sums {
		if s[0] != s[1] {
			t.Fatalf("trial balance out of balance in %s: debits %d credits %d", cur, s[0], s[1])
		}
	}
	var _ = []model.GLEntry{} // keep model import
}

func TestIntegration_FundsAndPurchases(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	fr := repo.NewFundsRepo(pool)
	pr := repo.NewPurchasesRepo(pool)
	ar := repo.NewAuthRepo(pool)
	requester := newUser(t, ar)
	signerA := newUser(t, ar)
	signerB := newUser(t, ar)

	// Fund with policy: 1 approval overall, but BOTH named signers required.
	purpose := "scholarships only"
	fund, err := fr.CreateFund(ctx, uniq("Scholarship"), &purpose, 1, []string{signerA, signerB}, requester)
	if err != nil {
		t.Fatalf("fund: %v", err)
	}
	if len(fund.Signers) != 2 || fund.CashAccountCode < "1500" {
		t.Fatalf("fund shape: %+v", fund)
	}

	// Money in: operating cash needs funds first (GL smoke left some; add
	// deterministic income via an unlinked transaction -> DR 1000/CR 4000).
	if _, err := pool.Exec(ctx, `
		INSERT INTO transactions (amount, currency, provider, recorded_by)
		VALUES (100000, 'USD', 'manual', $1::uuid)`, requester); err != nil {
		t.Fatalf("seed cash: %v", err)
	}
	if err := fr.Transfer(ctx, fund.ID, "in", 50000, "USD", "allocation"); err != nil {
		t.Fatalf("transfer in: %v", err)
	}
	got, _ := fr.GetFund(ctx, fund.ID)
	if len(got.Balances) != 1 || got.Balances[0].Balance != 50000 {
		t.Fatalf("fund balance: %+v", got.Balances)
	}
	// Over-withdrawal refused.
	if err := fr.Transfer(ctx, fund.ID, "out", 60000, "USD", "raid"); err == nil {
		t.Fatal("overdraft transfer out must be refused")
	}

	// Purchase for 30000: one signer is not enough (named B missing).
	req, err := pr.Create(ctx, &model.PurchaseRequest{
		FundID: fund.ID, Amount: 30000, Currency: "USD", Payee: "Bookstore",
	}, requester)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	afterA, err := pr.Approve(ctx, req.ID, signerA, "203.0.113.1")
	if err != nil {
		t.Fatalf("approve A: %v", err)
	}
	if afterA.Status != "pending" || len(afterA.MissingSigners) != 1 {
		t.Fatalf("after A: status=%s missing=%v", afterA.Status, afterA.MissingSigners)
	}
	// Double-signing refused.
	if _, err := pr.Approve(ctx, req.ID, signerA, "203.0.113.1"); err != repo.ErrAlreadyApproved {
		t.Fatalf("double sign: got %v", err)
	}
	// Completing before approval refused.
	if _, err := pr.Complete(ctx, req.ID); err == nil {
		t.Fatal("complete before approval must be refused")
	}
	afterB, err := pr.Approve(ctx, req.ID, signerB, "203.0.113.2")
	if err != nil {
		t.Fatalf("approve B: %v", err)
	}
	if afterB.Status != "approved" {
		t.Fatalf("after B: %s", afterB.Status)
	}

	// Completion posts to the books and freezes the request.
	done, err := pr.Complete(ctx, req.ID)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if done.Status != "completed" || done.JournalEntryID == nil {
		t.Fatalf("done: %+v", done)
	}
	var entryLines int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM journal_lines WHERE entry_id = $1::uuid`, *done.JournalEntryID).Scan(&entryLines); err != nil || entryLines != 2 {
		t.Fatalf("journal entry lines: %d err=%v", entryLines, err)
	}
	got, _ = fr.GetFund(ctx, fund.ID)
	if got.Balances[0].Balance != 20000 {
		t.Fatalf("fund after purchase: %+v", got.Balances)
	}
	if _, err := pool.Exec(ctx, `UPDATE purchase_requests SET payee = 'tampered' WHERE id = $1::uuid`, req.ID); err == nil {
		t.Fatal("completed request must be frozen")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM purchase_requests WHERE id = $1::uuid`, req.ID); err == nil {
		t.Fatal("requests must never delete")
	}

	// Overspend guard: 25000 > remaining 20000 refuses at completion.
	big, _ := pr.Create(ctx, &model.PurchaseRequest{FundID: fund.ID, Amount: 25000, Currency: "USD", Payee: "Too Big"}, requester)
	if _, err := pr.Approve(ctx, big.ID, signerA, ""); err != nil {
		t.Fatalf("approve big A: %v", err)
	}
	if _, err := pr.Approve(ctx, big.ID, signerB, ""); err != nil {
		t.Fatalf("approve big B: %v", err)
	}
	if _, err := pr.Complete(ctx, big.ID); err == nil {
		t.Fatal("overspend completion must be refused")
	}

	// Policy change voids in-flight approvals on pending requests.
	small, _ := pr.Create(ctx, &model.PurchaseRequest{FundID: fund.ID, Amount: 1000, Currency: "USD", Payee: "Pens"}, requester)
	if _, err := pr.Approve(ctx, small.ID, signerA, ""); err != nil {
		t.Fatalf("approve small: %v", err)
	}
	three := 3
	voided, err := fr.UpdatePolicy(ctx, fund.ID, nil, &three, nil)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	if voided < 1 {
		t.Fatalf("expected voided approvals, got %d", voided)
	}
	fresh, _ := pr.Get(ctx, small.ID)
	if len(fresh.Approvals) != 0 {
		t.Fatalf("approvals must be voided: %+v", fresh.Approvals)
	}
}
