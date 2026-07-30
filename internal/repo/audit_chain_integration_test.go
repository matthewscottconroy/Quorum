//go:build integration

// Evidence-integrity tests: the audit log's hash chain and the append-only
// triggers, against real PostgreSQL. These are the guarantees COMPLIANCE.md
// makes to third parties, so they are tested at the database boundary, not
// through mocks.
package repo_test

import (
	"context"
	"strings"
	"testing"

	"quorum/internal/repo"
)

func TestIntegration_AuditChain(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ar := repo.NewAuditRepo(pool)
	uid := newUser(t, repo.NewAuthRepo(pool))

	// Append three entries and verify the chain holds.
	for _, action := range []string{uniq("chain.a"), uniq("chain.b"), uniq("chain.c")} {
		if err := ar.Log(ctx, uid, action, "test", uid); err != nil {
			t.Fatalf("log: %v", err)
		}
	}
	st, err := ar.VerifyChain(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !st.OK || st.Entries < 3 || st.HeadHash == "" {
		t.Fatalf("chain should be intact: %+v", st)
	}

	// Direct tampering is refused by the triggers.
	if _, err := pool.Exec(ctx, `UPDATE audit_log SET action = 'forged' WHERE seq = $1`, st.HeadSeq); err == nil {
		t.Fatal("UPDATE on audit_log must be refused")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("unexpected refusal message: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_log WHERE seq = $1`, st.HeadSeq); err == nil {
		t.Fatal("DELETE of a fresh audit row must be refused")
	}

	// A bypass (trigger disabled — what a table owner or superuser could do)
	// must still be DETECTED by chain verification.
	if _, err := pool.Exec(ctx, `ALTER TABLE audit_log DISABLE TRIGGER trg_audit_log_no_update`); err != nil {
		t.Fatalf("disable trigger: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_log SET action = 'forged' WHERE seq = $1`, st.HeadSeq); err != nil {
		t.Fatalf("tamper update: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE audit_log ENABLE TRIGGER trg_audit_log_no_update`); err != nil {
		t.Fatalf("re-enable trigger: %v", err)
	}
	st2, err := ar.VerifyChain(ctx)
	if err != nil {
		t.Fatalf("verify after tamper: %v", err)
	}
	if st2.OK {
		t.Fatal("verification must detect the trigger-bypassed edit")
	}
	if st2.BrokenSeq != st.HeadSeq {
		t.Errorf("broken seq = %d, want %d", st2.BrokenSeq, st.HeadSeq)
	}

	// Repair for subsequent tests/runs: recompute this row's hash the same way
	// the trigger would (the tampered action stays, but the chain is re-linked).
	if _, err := pool.Exec(ctx, `ALTER TABLE audit_log DISABLE TRIGGER trg_audit_log_no_update`); err != nil {
		t.Fatalf("disable for repair: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE audit_log SET entry_hash = audit_entry_digest(seq, user_id, action, entity_type, entity_id, created_at, prev_hash)
		WHERE seq = $1`, st.HeadSeq); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE audit_log ENABLE TRIGGER trg_audit_log_no_update`); err != nil {
		t.Fatalf("re-enable after repair: %v", err)
	}
	if st3, err := ar.VerifyChain(ctx); err != nil || !st3.OK {
		t.Fatalf("chain should verify after repair: %+v err=%v", st3, err)
	}
}

func TestIntegration_LedgerImmutable(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	mr := repo.NewMembersRepo(pool)
	member := newMember(t, mr, uniq("ltier"), "active")

	if _, err := pool.Exec(ctx, `
		INSERT INTO transactions (member_id, amount, currency, provider)
		VALUES ($1::uuid, 5000, 'USD', 'manual')`, member); err != nil {
		t.Fatalf("insert txn: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE transactions SET amount = 1 WHERE member_id = $1::uuid`, member); err == nil {
		t.Fatal("transactions UPDATE must be refused")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM transactions WHERE member_id = $1::uuid`, member); err == nil {
		t.Fatal("transactions DELETE must be refused")
	}

	var invID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO dues_invoices (member_id, amount, currency, period_label, due_date)
		VALUES ($1::uuid, 5000, 'USD', $2, '2026-12-01') RETURNING id::text`, member, uniq("inv")).Scan(&invID); err != nil {
		t.Fatalf("insert invoice: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE dues_invoices SET amount = 999 WHERE id = $1::uuid`, invID); err == nil {
		t.Fatal("invoice amount change must be refused")
	}
	if _, err := pool.Exec(ctx, `UPDATE dues_invoices SET status = 'paid' WHERE id = $1::uuid`, invID); err != nil {
		t.Fatalf("invoice status change must stay allowed: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM dues_invoices WHERE id = $1::uuid`, invID); err == nil {
		t.Fatal("invoice DELETE must be refused")
	}
}
