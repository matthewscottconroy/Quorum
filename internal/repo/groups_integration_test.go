//go:build integration

// Visibility-group semantics — the access-control property itself, proven at
// the database boundary: unrestricted resources are visible to everyone,
// restricted ones only to members of an attached group (or officers), and a
// hidden resource is indistinguishable from a missing one.
package repo_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

func TestIntegration_ResourceVisibilityGroups(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	gr := repo.NewGroupsRepo(pool)
	rr := repo.NewResourcesRepo(pool)
	mr := repo.NewMembersRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)

	insider := newMember(t, mr, uniq("gtier"), "active")
	outsider := newMember(t, mr, uniq("gtier"), "active")

	board, err := gr.Create(ctx, uniq("Board"), nil, uid)
	if err != nil {
		t.Fatalf("group: %v", err)
	}
	if err := gr.SetMembers(ctx, board.ID, []string{insider}); err != nil {
		t.Fatalf("set members: %v", err)
	}

	open, err := rr.Create(ctx, &model.Resource{Title: uniq("open-doc"), Tags: []string{}}, uid)
	if err != nil {
		t.Fatalf("open resource: %v", err)
	}
	secret, err := rr.Create(ctx, &model.Resource{Title: uniq("board-only-doc"), Tags: []string{}}, uid)
	if err != nil {
		t.Fatalf("secret resource: %v", err)
	}
	if err := gr.SetResourceGroups(ctx, secret.ID, []string{board.ID}); err != nil {
		t.Fatalf("restrict: %v", err)
	}

	visible := func(seesAll bool, memberID string) map[string]bool {
		items, _, err := rr.List(ctx, repo.ResourceFilter{Limit: 500, ViewerSeesAll: seesAll, ViewerMemberID: memberID})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		out := map[string]bool{}
		for _, it := range items {
			out[it.ID] = true
		}
		return out
	}

	// Group member sees both; outsider and unlinked accounts see only the open one.
	if v := visible(false, insider); !v[open.ID] || !v[secret.ID] {
		t.Fatal("group member must see both resources")
	}
	if v := visible(false, outsider); !v[open.ID] || v[secret.ID] {
		t.Fatal("non-member must see only the unrestricted resource")
	}
	if v := visible(false, ""); !v[open.ID] || v[secret.ID] {
		t.Fatal("account with no linked member must see only unrestricted resources")
	}
	// Officers see everything.
	if v := visible(true, ""); !v[open.ID] || !v[secret.ID] {
		t.Fatal("officer scope must see everything")
	}

	// Direct fetch: hidden == missing.
	if _, err := rr.GetVisible(ctx, secret.ID, false, outsider); err != pgx.ErrNoRows {
		t.Fatalf("outsider GetVisible on restricted resource: got %v, want ErrNoRows", err)
	}
	if _, err := rr.GetVisible(ctx, secret.ID, false, insider); err != nil {
		t.Fatalf("insider GetVisible: %v", err)
	}

	// Group names surface for badges.
	items, _, _ := rr.List(ctx, repo.ResourceFilter{Limit: 500, ViewerSeesAll: true})
	for _, it := range items {
		if it.ID == secret.ID && (len(it.GroupNames) != 1 || it.GroupNames[0] != board.Name) {
			t.Fatalf("restricted resource should carry its group name, got %v", it.GroupNames)
		}
	}

	// Deleting the group widens visibility back to all members.
	if err := gr.Delete(ctx, board.ID); err != nil {
		t.Fatalf("delete group: %v", err)
	}
	if v := visible(false, outsider); !v[secret.ID] {
		t.Fatal("after group deletion the resource must be visible to all members")
	}
}
