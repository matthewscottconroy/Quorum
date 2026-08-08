//go:build integration

// Attendance roster sources: bulk-select by minimum linked-user role and by
// visibility-group membership.
package repo_test

import (
	"context"
	"testing"

	"quorum/internal/repo"
)

func TestIntegration_AttendanceRosterSources(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	mr := repo.NewMembersRepo(pool)
	ar := repo.NewAuthRepo(pool)
	gr := repo.NewGroupsRepo(pool)

	officerMember := newMember(t, mr, uniq("tier"), "active")
	plainMember := newMember(t, mr, uniq("tier"), "active")
	if _, err := ar.CreateUser(ctx, uniq("off")+"@example.com", "x", "officer", &officerMember); err != nil {
		t.Fatalf("officer user: %v", err)
	}
	if _, err := ar.CreateUser(ctx, uniq("pm")+"@example.com", "x", "member", &plainMember); err != nil {
		t.Fatalf("member user: %v", err)
	}

	// officer rank = 3: catches the officer-linked member, not the plain one.
	ids, err := mr.IDsByMinRole(ctx, 3)
	if err != nil {
		t.Fatalf("IDsByMinRole: %v", err)
	}
	has := func(list []string, want string) bool {
		for _, id := range list {
			if id == want {
				return true
			}
		}
		return false
	}
	if !has(ids, officerMember) {
		t.Fatalf("officer-linked member missing from min-role list")
	}
	if has(ids, plainMember) {
		t.Fatalf("member-role account leaked into officer+ list")
	}

	// Group membership resolves to member IDs.
	creator := newUser(t, ar)
	g, err := gr.Create(ctx, uniq("Group"), nil, creator)
	if err != nil {
		t.Fatalf("group: %v", err)
	}
	if err := gr.SetMembers(ctx, g.ID, []string{plainMember}); err != nil {
		t.Fatalf("set members: %v", err)
	}
	got, err := gr.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	if len(got.MemberIDs) != 1 || got.MemberIDs[0] != plainMember {
		t.Fatalf("group members = %v, want [%s]", got.MemberIDs, plainMember)
	}
}
