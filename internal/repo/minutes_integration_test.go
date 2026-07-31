//go:build integration

// Recording-secretary integrity: once minutes are finalized they are the
// official record — the journal must be immutable at the database level and
// finalization must be one-way, against direct SQL, not just the API.
package repo_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

func TestIntegration_MinutesLifecycleAndImmutability(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	mr := repo.NewMeetingsRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)

	mt, err := mr.Create(ctx, &model.Meeting{Title: uniq("min-mt"), ScheduledAt: time.Now(), Status: "scheduled"}, uid)
	if err != nil {
		t.Fatalf("meeting: %v", err)
	}

	// Journal: add, correct, list in order.
	e1, err := mr.AddMinutesEntry(ctx, mt.ID, "call_to_order", "Called to order at 18:03.", nil, uid)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := mr.AddMinutesEntry(ctx, mt.ID, "new_business", "Discussed the annual picnic.", nil, uid); err != nil {
		t.Fatalf("add2: %v", err)
	}
	if _, err := mr.UpdateMinutesEntry(ctx, mt.ID, e1.ID, "call_to_order", "Called to order at 18:05 by the chair.", nil); err != nil {
		t.Fatalf("update: %v", err)
	}
	entries, err := mr.ListMinutes(ctx, mt.ID)
	if err != nil || len(entries) != 2 {
		t.Fatalf("list: %v (%d entries)", err, len(entries))
	}
	if entries[0].Seq >= entries[1].Seq {
		t.Fatal("entries must come back in chronological seq order")
	}

	// Finalize: second call reports already-finalized.
	if err := mr.FinalizeMinutes(ctx, mt.ID, uid); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := mr.FinalizeMinutes(ctx, mt.ID, uid); err != repo.ErrMinutesFinalized {
		t.Fatalf("double finalize: got %v, want ErrMinutesFinalized", err)
	}

	// The journal is now locked — INSERT, UPDATE, and DELETE all refuse.
	if _, err := mr.AddMinutesEntry(ctx, mt.ID, "note", "sneaky addition", nil, uid); err == nil {
		t.Fatal("INSERT after finalize must be refused")
	} else if !strings.Contains(err.Error(), "finalized") {
		t.Fatalf("unexpected insert refusal: %v", err)
	}
	if _, err := mr.UpdateMinutesEntry(ctx, mt.ID, e1.ID, "note", "rewrite history", nil); err == nil {
		t.Fatal("UPDATE after finalize must be refused")
	}
	if err := mr.DeleteMinutesEntry(ctx, mt.ID, e1.ID); err == nil {
		t.Fatal("DELETE after finalize must be refused")
	}

	// Finalization is one-way even via direct SQL.
	if _, err := pool.Exec(ctx, `UPDATE meetings SET minutes_finalized_at = NULL WHERE id = $1::uuid`, mt.ID); err == nil {
		t.Fatal("un-finalizing must be refused")
	} else if !strings.Contains(err.Error(), "cannot be undone") {
		t.Fatalf("unexpected unfinalize refusal: %v", err)
	}

	// Finalized minutes count as governance history: the meeting can't be deleted.
	hist, err := mr.HasGovernanceHistory(ctx, mt.ID)
	if err != nil || !hist {
		t.Fatalf("finalized minutes must count as governance history (got %v, %v)", hist, err)
	}
}
