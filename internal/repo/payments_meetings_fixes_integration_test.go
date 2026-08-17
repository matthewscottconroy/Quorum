//go:build integration

// The 2026-08 integrity round at the database boundary: refunds un-pay,
// payment-report confirms post only the remaining balance atomically, one
// active dues schedule per tier, bulk re-open defers to the ledger, and
// finalized minutes are REALLY final (attendees, decisions, and the meeting
// header all freeze with the journal).
package repo_test

import (
	"context"
	"errors"
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

// ── second-pass round ────────────────────────────────────────────────────

// The money guards run under the invoice row lock in the repo.
func TestIntegration_GuardedTransaction(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dues := repo.NewDuesRepo(pool)
	mr := repo.NewMembersRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)
	inv := newInvoiceFor(t, dues, newMember(t, mr, uniq("tier"), "active"), 1000)

	status := "succeeded"
	mk := func(amount int64) *model.Transaction {
		return &model.Transaction{InvoiceID: &inv.ID, MemberID: &inv.MemberID, AmountMinor: amount,
			Currency: "USD", Provider: "manual", ProviderStatus: &status, RecordedBy: &uid, OccurredAt: time.Now()}
	}
	// Overpay without the flag: refused with the remaining balance.
	if _, limit, err := dues.CreateGuardedTransaction(ctx, mk(1500), false); !errors.Is(err, repo.ErrExceedsRemaining) || limit != 1000 {
		t.Fatalf("overpay: err=%v limit=%d, want ErrExceedsRemaining/1000", err, limit)
	}
	// Acknowledged overpay posts and settles the invoice.
	if _, _, err := dues.CreateGuardedTransaction(ctx, mk(1500), true); err != nil {
		t.Fatalf("acknowledged overpay: %v", err)
	}
	if got := invoiceStatus(t, dues, inv.ID); got != "paid" {
		t.Fatalf("after overpay: %s, want paid", got)
	}
	// Refund cap: net paid is 1500; 2000 back is refused, 1500 is fine.
	refund := "refunded"
	rk := func(amount int64) *model.Transaction {
		return &model.Transaction{InvoiceID: &inv.ID, MemberID: &inv.MemberID, AmountMinor: -amount,
			Currency: "USD", Provider: "manual", ProviderStatus: &refund, RecordedBy: &uid, OccurredAt: time.Now()}
	}
	if _, limit, err := dues.CreateGuardedTransaction(ctx, rk(2000), false); !errors.Is(err, repo.ErrExceedsPaid) || limit != 1500 {
		t.Fatalf("over-refund: err=%v limit=%d, want ErrExceedsPaid/1500", err, limit)
	}
	if _, _, err := dues.CreateGuardedTransaction(ctx, rk(1500), false); err != nil {
		t.Fatalf("full refund: %v", err)
	}
	if got := invoiceStatus(t, dues, inv.ID); got != "pending" {
		t.Fatalf("after full refund: %s, want pending", got)
	}
}

// Stripe's cumulative refund totals post as DELTAS: $30 then $70-cumulative
// record -30 then -40, never -30 then -70.
func TestIntegration_WebhookRefundDeltas(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dues := repo.NewDuesRepo(pool)
	mr := repo.NewMembersRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)
	inv := newInvoiceFor(t, dues, newMember(t, mr, uniq("tier"), "active"), 10000)
	payInvoice(t, dues, inv, 10000, uid)

	ref := "ch_" + uniq("charge")
	status := "refunded"
	base := &model.Transaction{InvoiceID: &inv.ID, Currency: "USD", Provider: "stripe",
		ProviderReferenceID: &ref, ProviderStatus: &status, OccurredAt: time.Now()}
	if _, err := dues.RecordWebhookRefund(ctx, uniq("evt"), base, 3000); err != nil {
		t.Fatalf("first refund: %v", err)
	}
	if _, err := dues.RecordWebhookRefund(ctx, uniq("evt"), base, 7000); err != nil {
		t.Fatalf("second refund: %v", err)
	}
	var net int64
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(-sum(amount),0) FROM transactions
		WHERE invoice_id = $1::uuid AND amount < 0`, inv.ID).Scan(&net); err != nil {
		t.Fatalf("sum refunds: %v", err)
	}
	if net != 7000 {
		t.Fatalf("net refunded %d, want 7000 (deltas, not cumulative re-posts)", net)
	}
	// A replay of the same cumulative total posts nothing further.
	if _, err := dues.RecordWebhookRefund(ctx, uniq("evt"), base, 7000); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(-sum(amount),0) FROM transactions
		WHERE invoice_id = $1::uuid AND amount < 0`, inv.ID).Scan(&net); err != nil {
		t.Fatalf("re-sum: %v", err)
	}
	if net != 7000 {
		t.Fatalf("after replay: %d, want 7000", net)
	}
}

// Corrections: draft minutes refuse them, finalized minutes accept them, and
// the rows are append-only at the database level.
func TestIntegration_MinutesCorrections(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	mtRepo := repo.NewMeetingsRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)
	mt, err := mtRepo.Create(ctx, &model.Meeting{
		Title: uniq("Meeting"), ScheduledAt: time.Now().Add(-2 * time.Hour), Status: "completed",
	}, uid)
	if err != nil {
		t.Fatalf("create meeting: %v", err)
	}
	if _, err := mtRepo.AddCorrection(ctx, mt.ID, "too early", uid); !errors.Is(err, repo.ErrMinutesNotFinal) {
		t.Fatalf("correction on draft: err=%v, want ErrMinutesNotFinal", err)
	}
	if _, err := mtRepo.AddMinutesEntry(ctx, mt.ID, "note", "x", nil, uid); err != nil {
		t.Fatalf("journal: %v", err)
	}
	if err := mtRepo.FinalizeMinutes(ctx, mt.ID, uid); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	c, err := mtRepo.AddCorrection(ctx, mt.ID, "The vote was 7-2, not 6-2.", uid)
	if err != nil {
		t.Fatalf("correction: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE meeting_corrections SET body = 'rewritten' WHERE id = $1::uuid`, c.ID); err == nil {
		t.Fatal("correction UPDATE succeeded; the trigger must refuse")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM meeting_corrections WHERE id = $1::uuid`, c.ID); err == nil {
		t.Fatal("correction DELETE succeeded; the trigger must refuse")
	}
	list, err := mtRepo.ListCorrections(ctx, mt.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list corrections: %v n=%d", err, len(list))
	}
}

// Calendar tokens are hashed at rest; the plaintext still authorizes the feed.
func TestIntegration_CalendarTokenHashedAtRest(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)
	plain, err := ar.RotateCalendarToken(ctx, uid)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT calendar_token FROM users WHERE id = $1::uuid`, uid).Scan(&stored); err != nil {
		t.Fatalf("read stored: %v", err)
	}
	if stored == plain {
		t.Fatal("token stored in PLAINTEXT — a backup leak would yield working feed URLs")
	}
	if got, err := ar.UserIDByCalendarToken(ctx, plain); err != nil || got != uid {
		t.Fatalf("plaintext lookup: got %q err=%v, want the owner", got, err)
	}
	if _, err := ar.UserIDByCalendarToken(ctx, stored); err == nil {
		t.Fatal("the stored HASH must not itself authorize the feed")
	}
}

// Close decides from the ballots as frozen under the motion's lock, and a
// vote against a closed motion is refused at the repo layer.
func TestIntegration_CloseAndDecide(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	gov := repo.NewGovernanceRepo(pool)
	mtRepo := repo.NewMeetingsRepo(pool)
	mr := repo.NewMembersRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)
	mt, err := mtRepo.Create(ctx, &model.Meeting{Title: uniq("Meeting"), ScheduledAt: time.Now(), Status: "scheduled"}, uid)
	if err != nil {
		t.Fatalf("meeting: %v", err)
	}
	mo, err := gov.CreateMotion(ctx, &model.Motion{MeetingID: mt.ID, Title: uniq("Motion"), Threshold: "majority", Status: "draft"}, uid)
	if err != nil {
		t.Fatalf("motion: %v", err)
	}
	if _, err := gov.SetMotionStatus(ctx, mo.ID, "open", nil); err != nil {
		t.Fatalf("open: %v", err)
	}
	yes1 := newMember(t, mr, uniq("tier"), "active")
	yes2 := newMember(t, mr, uniq("tier"), "active")
	no1 := newMember(t, mr, uniq("tier"), "active")
	for m2, choice := range map[string]string{yes1: "for", yes2: "for", no1: "against"} {
		if err := gov.CastVote(ctx, mo.ID, m2, choice, false, uid); err != nil {
			t.Fatalf("vote: %v", err)
		}
	}
	out, final, err := gov.CloseAndDecide(ctx, mo.ID, "")
	if err != nil || final != "carried" {
		t.Fatalf("close: final=%q err=%v, want carried (2-1 majority)", final, err)
	}
	if out.Tally.For != 2 || out.Tally.Against != 1 {
		t.Fatalf("recorded tally %d-%d, want 2-1", out.Tally.For, out.Tally.Against)
	}
	// The door is closed: late ballots bounce at the repo layer.
	if err := gov.CastVote(ctx, mo.ID, no1, "for", false, uid); !errors.Is(err, repo.ErrMotionNotOpen) {
		t.Fatalf("late vote: err=%v, want ErrMotionNotOpen", err)
	}
	// And a second close is refused.
	if _, _, err := gov.CloseAndDecide(ctx, mo.ID, ""); !errors.Is(err, repo.ErrMotionDecided) {
		t.Fatalf("re-close: err=%v, want ErrMotionDecided", err)
	}
}
