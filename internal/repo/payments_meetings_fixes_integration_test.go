//go:build integration

// The 2026-08 integrity round at the database boundary: refunds un-pay,
// payment-report confirms post only the remaining balance atomically, one
// active dues schedule per tier, bulk re-open defers to the ledger, and
// finalized minutes are REALLY final (attendees, decisions, and the meeting
// header all freeze with the journal).
package repo_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

func newInvoiceFor(t *testing.T, dues *repo.DuesRepo, memberID string, amount int64) model.DuesInvoice {
	t.Helper()
	created, err := dues.CreateInvoiceBatch(context.Background(), []*model.DuesInvoice{{
		MemberID: memberID, AmountMinor: amount, Currency: "USD",
		PeriodLabel: uniq("period"), DueDate: time.Now().Add(30 * 24 * time.Hour), Status: "pending",
	}})
	if err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	return created[0]
}

func payInvoice(t *testing.T, dues *repo.DuesRepo, inv model.DuesInvoice, amount int64, by string) {
	t.Helper()
	status := "succeeded"
	if _, err := dues.CreateTransaction(context.Background(), &model.Transaction{
		InvoiceID: &inv.ID, MemberID: &inv.MemberID, AmountMinor: amount, Currency: "USD",
		Provider: "manual", ProviderStatus: &status, RecordedBy: &by, OccurredAt: time.Now(),
	}); err != nil {
		t.Fatalf("pay: %v", err)
	}
	if err := dues.RecomputeInvoiceStatus(context.Background(), inv.ID); err != nil {
		t.Fatalf("recompute: %v", err)
	}
}

func invoiceStatus(t *testing.T, dues *repo.DuesRepo, id string) string {
	t.Helper()
	inv, err := dues.GetInvoice(context.Background(), id)
	if err != nil {
		t.Fatalf("get invoice: %v", err)
	}
	return inv.Status
}

// A full refund moves a paid invoice back off 'paid' — it used to stay paid
// forever because the recompute skipped paid rows entirely.
func TestIntegration_RefundUnpaysInvoice(t *testing.T) {
	pool := testPool(t)
	dues := repo.NewDuesRepo(pool)
	mr := repo.NewMembersRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)
	inv := newInvoiceFor(t, dues, newMember(t, mr, uniq("tier"), "active"), 5000)

	payInvoice(t, dues, inv, 5000, uid)
	if got := invoiceStatus(t, dues, inv.ID); got != "paid" {
		t.Fatalf("after payment: %s, want paid", got)
	}
	// Refund half → partial; refund the rest → pending (due date is future).
	status := "refunded"
	for _, amt := range []int64{-2500, -2500} {
		if _, err := dues.CreateTransaction(context.Background(), &model.Transaction{
			InvoiceID: &inv.ID, MemberID: &inv.MemberID, AmountMinor: amt, Currency: "USD",
			Provider: "manual", ProviderStatus: &status, RecordedBy: &uid, OccurredAt: time.Now(),
		}); err != nil {
			t.Fatalf("refund: %v", err)
		}
		if err := dues.RecomputeInvoiceStatus(context.Background(), inv.ID); err != nil {
			t.Fatalf("recompute: %v", err)
		}
	}
	if got := invoiceStatus(t, dues, inv.ID); got != "pending" {
		t.Fatalf("after full refund: %s, want pending", got)
	}
}

// Confirming a member payment report posts the REMAINING balance, not the
// invoice face value, in one transaction — and a report on a settled invoice
// clears without posting a duplicate cent.
func TestIntegration_ConfirmAndPost_RemainingOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dues := repo.NewDuesRepo(pool)
	prs := repo.NewPaymentReportsRepo(pool)
	mr := repo.NewMembersRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)
	memberID := newMember(t, mr, uniq("tier"), "active")

	// Half-paid invoice: confirm must post exactly the other half.
	inv := newInvoiceFor(t, dues, memberID, 8000)
	payInvoice(t, dues, inv, 4000, uid)
	rep, err := prs.Create(ctx, inv.ID, memberID, "zelle", "ref-1", "")
	if err != nil {
		t.Fatalf("file report: %v", err)
	}
	out, err := prs.ConfirmAndPost(ctx, rep.ID, uid)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !out.Posted || out.AmountMinor != 4000 {
		t.Fatalf("confirm posted=%v amount=%d, want posted 4000", out.Posted, out.AmountMinor)
	}
	if got := invoiceStatus(t, dues, inv.ID); got != "paid" {
		t.Fatalf("after confirm: %s, want paid (exactly settled)", got)
	}

	// Already-settled invoice: the report clears but posts nothing.
	inv2 := newInvoiceFor(t, dues, memberID, 3000)
	rep2, err := prs.Create(ctx, inv2.ID, memberID, "check", "ref-2", "")
	if err != nil {
		t.Fatalf("file report 2: %v", err)
	}
	payInvoice(t, dues, inv2, 3000, uid) // settles while the report waits
	out2, err := prs.ConfirmAndPost(ctx, rep2.ID, uid)
	if err != nil {
		t.Fatalf("confirm 2: %v", err)
	}
	if out2.Posted {
		t.Fatalf("confirm on settled invoice posted %d — double payment", out2.AmountMinor)
	}
}

// The partial unique index allows exactly one ACTIVE schedule per tier.
func TestIntegration_OneActiveSchedulePerTier(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dues := repo.NewDuesRepo(pool)
	tier := uniq("tier")
	if _, err := dues.CreateSchedule(ctx, &model.DuesSchedule{
		Tier: tier, AmountMinor: 1000, Currency: "USD", Cadence: "annual", DueDays: 30, Active: true,
	}); err != nil {
		t.Fatalf("first schedule: %v", err)
	}
	_, err := dues.CreateSchedule(ctx, &model.DuesSchedule{
		Tier: tier, AmountMinor: 2000, Currency: "USD", Cadence: "monthly", DueDays: 30, Active: true,
	})
	if err == nil || !strings.Contains(err.Error(), "idx_dues_schedules_one_active_per_tier") {
		t.Fatalf("second active schedule: err=%v, want unique violation", err)
	}
	// An inactive second schedule is fine.
	if _, err := dues.CreateSchedule(ctx, &model.DuesSchedule{
		Tier: tier, AmountMinor: 2000, Currency: "USD", Cadence: "monthly", DueDays: 30, Active: false,
	}); err != nil {
		t.Fatalf("inactive schedule: %v", err)
	}
}

// Bulk waive skips paid invoices; bulk re-open defers to the ledger.
func TestIntegration_BatchStatus_LedgerWins(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dues := repo.NewDuesRepo(pool)
	mr := repo.NewMembersRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)
	memberID := newMember(t, mr, uniq("tier"), "active")

	paid := newInvoiceFor(t, dues, memberID, 1000)
	payInvoice(t, dues, paid, 1000, uid)
	open := newInvoiceFor(t, dues, memberID, 1000)

	// Waive both: only the open one actually waives.
	if _, err := dues.BatchUpdateStatus(ctx, []string{paid.ID, open.ID}, "waived"); err != nil {
		t.Fatalf("batch waive: %v", err)
	}
	if got := invoiceStatus(t, dues, paid.ID); got != "paid" {
		t.Fatalf("paid invoice after bulk waive: %s, want paid (nothing to waive)", got)
	}
	if got := invoiceStatus(t, dues, open.ID); got != "waived" {
		t.Fatalf("open invoice after bulk waive: %s, want waived", got)
	}
	// Re-open both: the paid one snaps back to paid (the ledger wins).
	if _, err := dues.BatchUpdateStatus(ctx, []string{paid.ID, open.ID}, "pending"); err != nil {
		t.Fatalf("batch reopen: %v", err)
	}
	if got := invoiceStatus(t, dues, paid.ID); got != "paid" {
		t.Fatalf("paid invoice after bulk reopen: %s, want paid", got)
	}
	if got := invoiceStatus(t, dues, open.ID); got != "pending" {
		t.Fatalf("waived invoice after bulk reopen: %s, want pending", got)
	}
}

// Finalized minutes freeze EVERYTHING the document is generated from.
func TestIntegration_MinutesFinality_FreezesSources(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	mtRepo := repo.NewMeetingsRepo(pool)
	mr := repo.NewMembersRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)
	memberID := newMember(t, mr, uniq("tier"), "active")

	mt, err := mtRepo.Create(ctx, &model.Meeting{
		Title: uniq("Meeting"), ScheduledAt: time.Now().Add(-2 * time.Hour), Status: "completed",
	}, uid)
	if err != nil {
		t.Fatalf("create meeting: %v", err)
	}
	if _, err := mtRepo.AddMinutesEntry(ctx, mt.ID, "call_to_order", "Called to order.", nil, uid); err != nil {
		t.Fatalf("journal: %v", err)
	}
	if err := mtRepo.SetAttendees(ctx, mt.ID, []model.MeetingAttendee{{MemberID: memberID, Present: true}}); err != nil {
		t.Fatalf("attendees: %v", err)
	}
	if _, err := mtRepo.CreateDecision(ctx, &model.MeetingDecision{
		MeetingID: mt.ID, Summary: "Budget approved", Outcome: "passed",
	}); err != nil {
		t.Fatalf("decision: %v", err)
	}
	if err := mtRepo.FinalizeMinutes(ctx, mt.ID, uid); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := mtRepo.SetMinutesSnapshot(ctx, mt.ID, "# Minutes snapshot"); err != nil {
		t.Fatalf("snapshot write should be allowed post-finalize: %v", err)
	}
	if snap, err := mtRepo.GetMinutesSnapshot(ctx, mt.ID); err != nil || snap != "# Minutes snapshot" {
		t.Fatalf("snapshot read: %q, %v", snap, err)
	}

	wantFrozen := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "finalized") {
			t.Fatalf("%s after finalize: err=%v, want finalized refusal", what, err)
		}
	}
	// The roster is part of the record.
	wantFrozen("attendees", mtRepo.SetAttendees(ctx, mt.ID, []model.MeetingAttendee{}))
	// So is the decision log.
	_, derr := mtRepo.CreateDecision(ctx, &model.MeetingDecision{
		MeetingID: mt.ID, Summary: "Sneaky edit", Outcome: "passed",
	})
	wantFrozen("decision insert", derr)
	// So is the meeting header.
	newTitle := "Rewritten history"
	_, uerr := mtRepo.Update(ctx, mt.ID, &newTitle, nil, nil, false, nil, nil, nil, nil)
	wantFrozen("meeting title", uerr)
	// Status may still change (cancelling isn't rewriting minutes).
	st := "completed"
	if _, err := mtRepo.Update(ctx, mt.ID, nil, nil, nil, false, nil, nil, nil, &st); err != nil {
		t.Fatalf("status update should survive finalization: %v", err)
	}
}
