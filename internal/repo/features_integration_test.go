//go:build integration

// Integration tests for the governance / dues-automation / budget repos against
// a real PostgreSQL instance, covering the invariants the mocked handler tests
// cannot reach: generation idempotency, quorum proxy/active-member counting,
// atomic single-use ballots, and budget seed idempotency + clone.
//
// Run with a throwaway database (same harness as integration_test.go):
//
//	QUORUM_TEST_DATABASE_URL=postgres://quorum:test@localhost:5432/quorum?sslmode=disable \
//	  go test -tags integration ./internal/repo/...
package repo_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

var uniqCounter int64

func uniq(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, atomic.AddInt64(&uniqCounter, 1))
}

func newMember(t *testing.T, mr *repo.MembersRepo, tier, status string) string {
	t.Helper()
	email := uniq("m") + "@example.com"
	m, err := mr.Create(context.Background(), &model.Member{
		DisplayName: uniq("Member"), Email: &email, Tier: tier, Status: status, JoinedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	return m.ID
}

func newUser(t *testing.T, ar *repo.AuthRepo) string {
	t.Helper()
	u, err := ar.CreateUser(context.Background(), uniq("u")+"@example.com", "x", "officer", nil)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u.ID
}

func TestIntegration_GenerateInvoices_Idempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dues := repo.NewDuesRepo(pool)
	mr := repo.NewMembersRepo(pool)

	tier := uniq("tier")
	for i := 0; i < 3; i++ {
		newMember(t, mr, tier, "active")
	}
	newMember(t, mr, tier, "inactive") // must NOT be billed

	sched := model.DuesSchedule{Tier: tier, AmountMinor: 5000, Currency: "USD", Cadence: "annual", DueDays: 30}
	label := uniq("period")

	n, err := dues.GenerateInvoicesForSchedule(ctx, sched, label, time.Now())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if n != 3 {
		t.Fatalf("first generate: got %d invoices, want 3 (active only)", n)
	}
	// Idempotent: a second run for the same period creates nothing (ON CONFLICT).
	n2, err := dues.GenerateInvoicesForSchedule(ctx, sched, label, time.Now())
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second generate: got %d, want 0 (idempotent)", n2)
	}
}

func TestIntegration_ComputeQuorum_ProxiesAndActive(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	gov := repo.NewGovernanceRepo(pool)
	mr := repo.NewMembersRepo(pool)
	mtRepo := repo.NewMeetingsRepo(pool)
	ar := repo.NewAuthRepo(pool)

	// Fixed quorum of 2 so the assertion doesn't depend on the global roster size.
	if _, err := gov.UpdateSettings(ctx, &model.GovernanceSettings{
		QuorumMode: "fixed", QuorumValue: 2, ProxiesCountTowardQuorum: true, DefaultThreshold: "majority",
	}); err != nil {
		t.Fatalf("settings: %v", err)
	}

	a := newMember(t, mr, "standard", "active")   // present
	b := newMember(t, mr, "standard", "active")   // absent, proxy → A (counts)
	c := newMember(t, mr, "standard", "inactive") // absent, proxy → A (must NOT count)
	d := newMember(t, mr, "standard", "inactive") // present but inactive (must NOT count)

	mt, err := mtRepo.Create(ctx, &model.Meeting{Title: uniq("mt"), ScheduledAt: time.Now(), Status: "scheduled"}, newUser(t, ar))
	if err != nil {
		t.Fatalf("meeting: %v", err)
	}
	if err := mtRepo.SetAttendees(ctx, mt.ID, []model.MeetingAttendee{
		{MemberID: a, Present: true},
		{MemberID: d, Present: true}, // inactive present
	}); err != nil {
		t.Fatalf("attendees: %v", err)
	}
	if _, err := gov.CreateProxy(ctx, mt.ID, b, a); err != nil { // active grantor
		t.Fatalf("proxy b: %v", err)
	}
	if _, err := gov.CreateProxy(ctx, mt.ID, c, a); err != nil { // inactive grantor
		t.Fatalf("proxy c: %v", err)
	}

	q, err := gov.ComputeQuorum(ctx, mt.ID)
	if err != nil {
		t.Fatalf("quorum: %v", err)
	}
	if q.PresentCount != 1 {
		t.Errorf("present_count = %d, want 1 (inactive present D excluded)", q.PresentCount)
	}
	if q.ProxiesRepresented != 1 {
		t.Errorf("proxies_represented = %d, want 1 (inactive grantor C excluded)", q.ProxiesRepresented)
	}
	if q.EffectivePresent != 2 || !q.Met {
		t.Errorf("effective=%d met=%v, want 2/true", q.EffectivePresent, q.Met)
	}
}

func TestIntegration_ConsumeBallotAndVote_Atomic(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	gov := repo.NewGovernanceRepo(pool)
	mr := repo.NewMembersRepo(pool)
	mtRepo := repo.NewMeetingsRepo(pool)
	ar := repo.NewAuthRepo(pool)

	member := newMember(t, mr, "standard", "active")
	mt, _ := mtRepo.Create(ctx, &model.Meeting{Title: uniq("mt"), ScheduledAt: time.Now(), Status: "scheduled"}, newUser(t, ar))
	mo, err := gov.CreateMotion(ctx, &model.Motion{MeetingID: mt.ID, Title: uniq("motion"), Threshold: "majority", Status: "open"}, newUser(t, ar))
	if err != nil {
		t.Fatalf("motion: %v", err)
	}

	hash := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	tok := uniq("ballot")
	if err := gov.UpsertBallotToken(ctx, mo.ID, member, hash(tok), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("token: %v", err)
	}

	mid, err := gov.ConsumeBallotAndVote(ctx, hash(tok), "for")
	if err != nil || mid != mo.ID {
		t.Fatalf("consume: mid=%q err=%v", mid, err)
	}
	// The vote landed.
	got, err := gov.GetMotion(ctx, mo.ID)
	if err != nil || got.Tally.For != 1 {
		t.Fatalf("tally after ballot: %+v err=%v", got.Tally, err)
	}
	// Single-use: a second consume fails with ErrNoRows.
	if _, err := gov.ConsumeBallotAndVote(ctx, hash(tok), "against"); err != pgx.ErrNoRows {
		t.Fatalf("reuse: got %v, want ErrNoRows", err)
	}

	// A closed motion rejects with ErrMotionNotOpen and does NOT consume the token.
	tok2 := uniq("ballot")
	member2 := newMember(t, mr, "standard", "active")
	if err := gov.UpsertBallotToken(ctx, mo.ID, member2, hash(tok2), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("token2: %v", err)
	}
	if _, err := gov.SetMotionStatus(ctx, mo.ID, "carried", nil); err != nil {
		t.Fatalf("close motion: %v", err)
	}
	if _, err := gov.ConsumeBallotAndVote(ctx, hash(tok2), "for"); err != repo.ErrMotionNotOpen {
		t.Fatalf("closed motion: got %v, want ErrMotionNotOpen", err)
	}
	// Token was left unconsumed, so re-opening would let it work — prove it's unused
	// by reopening and consuming.
	if _, err := gov.SetMotionStatus(ctx, mo.ID, "open", nil); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := gov.ConsumeBallotAndVote(ctx, hash(tok2), "for"); err != nil {
		t.Fatalf("token2 should be unspent after the closed attempt: %v", err)
	}
}

func TestIntegration_Budget_SeedIdempotentAndClone(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	bud := repo.NewBudgetRepo(pool)
	dues := repo.NewDuesRepo(pool)
	mr := repo.NewMembersRepo(pool)
	ar := repo.NewAuthRepo(pool)
	user := newUser(t, ar)

	tier := uniq("btier")
	for i := 0; i < 3; i++ {
		newMember(t, mr, tier, "active")
	}
	if _, err := dues.CreateSchedule(ctx, &model.DuesSchedule{Tier: tier, AmountMinor: 5000, Currency: "USD", Cadence: "annual", DueDays: 30, Active: true}); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	sc, err := bud.CreateScenario(ctx, &model.BudgetScenario{Name: uniq("budget"), Status: "draft", Currency: "USD"}, user)
	if err != nil {
		t.Fatalf("scenario: %v", err)
	}
	// Seed once, then again — the tier's dues line must not double.
	if _, err := bud.SeedDuesIncome(ctx, sc.ID); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := bud.SeedDuesIncome(ctx, sc.ID); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	if _, err := bud.AddLine(ctx, &model.BudgetLine{ScenarioID: sc.ID, Kind: "expense", Label: "Venue", Quantity: 1, UnitAmountMinor: 10000}); err != nil {
		t.Fatalf("expense: %v", err)
	}

	full, err := bud.GetScenario(ctx, sc.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Only ONE dues line (idempotent), income 3×5000 annualized = 15000, net = 5000.
	duesLines := 0
	for _, l := range full.Lines {
		if l.Category != nil && *l.Category == "Dues" {
			duesLines++
		}
	}
	if duesLines != 1 {
		t.Errorf("expected 1 dues line after double seed, got %d", duesLines)
	}
	if full.Totals.IncomeMinor != 15000 || full.Totals.NetMinor != 5000 {
		t.Errorf("totals: income=%d net=%d, want 15000/5000", full.Totals.IncomeMinor, full.Totals.NetMinor)
	}

	// Clone copies lines and totals.
	clone, err := bud.CloneScenario(ctx, sc.ID, uniq("clone"), user)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if clone.Totals.IncomeMinor != 15000 || clone.Totals.NetMinor != 5000 {
		t.Errorf("clone totals: income=%d net=%d, want 15000/5000", clone.Totals.IncomeMinor, clone.Totals.NetMinor)
	}
	if len(clone.Lines) != len(full.Lines) {
		t.Errorf("clone copied %d lines, want %d", len(clone.Lines), len(full.Lines))
	}
}
