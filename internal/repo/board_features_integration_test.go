//go:build integration

// Board columns, card conversations, and document uploads — the three
// board/library features proven at the database boundary: column fallback
// semantics survive deletion, comment authorship resolves through the
// user→member link, and uploaded bytes round-trip under the same visibility
// rules as their resource.
package repo_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

func TestIntegration_BoardColumns(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	bc := repo.NewBoardColumnsRepo(pool)
	ai := repo.NewActionItemsRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)

	// Seeded defaults exist and are ordered.
	cols, err := bc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(cols) < 3 {
		t.Fatalf("expected the three seeded columns, got %d", len(cols))
	}

	// A custom workflow lane appends to the right end.
	blocked, err := bc.Create(ctx, uniq("Blocked"), 0, nil)
	if err != nil {
		t.Fatalf("create column: %v", err)
	}
	if blocked.Position <= cols[len(cols)-1].Position {
		t.Errorf("appended column position %d not after %d", blocked.Position, cols[len(cols)-1].Position)
	}
	if blocked.MapsToStatus != nil {
		t.Error("workflow lane must not map a status")
	}

	// A card moved into the lane keeps its column across reads...
	item, err := ai.Create(ctx, &model.ActionItem{Title: uniq("task"), Status: "open", Priority: "normal"}, uid)
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	moved, err := ai.Update(ctx, item.ID, map[string]any{"column_id": blocked.ID})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if moved.ColumnID == nil || *moved.ColumnID != blocked.ID {
		t.Fatalf("column not persisted: %+v", moved.ColumnID)
	}
	if moved.Status != "open" {
		t.Errorf("moving into a workflow lane changed status to %q", moved.Status)
	}

	// ...and falls back to its status lane when the column is deleted.
	if err := bc.Delete(ctx, blocked.ID); err != nil {
		t.Fatalf("delete column: %v", err)
	}
	after, err := ai.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if after.ColumnID != nil {
		t.Error("column_id should be NULL after its column is deleted")
	}
	if after.Status != "open" {
		t.Errorf("status must survive column deletion, got %q", after.Status)
	}
}

func TestIntegration_CardComments(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cc := repo.NewCardCommentsRepo(pool)
	ai := repo.NewActionItemsRepo(pool)
	ar := repo.NewAuthRepo(pool)
	mr := repo.NewMembersRepo(pool)

	uid := newUser(t, ar)
	item, err := ai.Create(ctx, &model.ActionItem{Title: uniq("card"), Status: "open", Priority: "normal"}, uid)
	if err != nil {
		t.Fatalf("create item: %v", err)
	}

	first, err := cc.Create(ctx, item.ID, uid, "Are we blocked on the vendor?")
	if err != nil {
		t.Fatalf("comment: %v", err)
	}
	if first.AuthorName == nil || *first.AuthorName == "" {
		t.Fatal("author name must resolve (email fallback)")
	}

	// Linking the author to a member upgrades the displayed name.
	memberID := newMember(t, mr, uniq("tier"), "active")
	if _, err := pool.Exec(ctx, `UPDATE users SET member_id = $1::uuid WHERE id = $2::uuid`, memberID, uid); err != nil {
		t.Fatalf("link member: %v", err)
	}
	if _, err := cc.Create(ctx, item.ID, uid, "Second message"); err != nil {
		t.Fatalf("comment 2: %v", err)
	}

	thread, err := cc.List(ctx, item.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(thread) != 2 {
		t.Fatalf("thread length: got %d, want 2", len(thread))
	}
	if thread[0].Body != "Are we blocked on the vendor?" {
		t.Error("thread must be oldest-first")
	}
	var display string
	if err := pool.QueryRow(ctx, `SELECT display_name FROM members WHERE id = $1::uuid`, memberID).Scan(&display); err != nil {
		t.Fatalf("member name: %v", err)
	}
	if thread[1].AuthorName == nil || *thread[1].AuthorName != display {
		t.Errorf("author name: got %v, want member name %q", thread[1].AuthorName, display)
	}

	// Comment count reaches the card list, and cascade removes the thread.
	got, err := ai.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CommentCount != 2 {
		t.Errorf("comment_count: got %d, want 2", got.CommentCount)
	}
	if err := ai.Delete(ctx, item.ID); err != nil {
		t.Fatalf("delete item: %v", err)
	}
	if _, err := cc.Get(ctx, first.ID); err == nil {
		t.Fatal("comments must cascade with their card")
	}
}

func TestIntegration_DocumentUploads(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	rr := repo.NewResourcesRepo(pool)
	fr := repo.NewFoldersRepo(pool)
	gr := repo.NewGroupsRepo(pool)
	mr := repo.NewMembersRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)

	folder, err := fr.Create(ctx, uniq("Bylaws"), nil)
	if err != nil {
		t.Fatalf("folder: %v", err)
	}
	res, err := rr.Create(ctx, &model.Resource{
		Title: uniq("charter"), Tags: []string{}, FolderID: &folder.ID,
	}, uid)
	if err != nil {
		t.Fatalf("resource: %v", err)
	}
	if res.FolderID == nil || *res.FolderID != folder.ID {
		t.Fatal("folder assignment lost on create")
	}

	// Upload round-trips bytes, size, hash, and content type.
	payload := []byte("Quorum charter v1 — final")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	if err := rr.SetFile(ctx, res.ID, "charter.txt", int64(len(payload)), digest, "text/plain", payload); err != nil {
		t.Fatalf("set file: %v", err)
	}
	ct, data, err := rr.GetFile(ctx, res.ID)
	if err != nil {
		t.Fatalf("get file: %v", err)
	}
	if ct != "text/plain" || string(data) != string(payload) {
		t.Fatalf("file round-trip mismatch: ct=%q len=%d", ct, len(data))
	}
	got, err := rr.Get(ctx, res.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.FileName == nil || *got.FileName != "charter.txt" || got.FileSHA256 == nil || *got.FileSHA256 != digest {
		t.Fatalf("file metadata not persisted: %+v", got)
	}

	// Replacing the document overwrites, not duplicates.
	if err := rr.SetFile(ctx, res.ID, "charter-v2.txt", 3, digest, "text/plain", []byte("v2!")); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if _, data, _ := rr.GetFile(ctx, res.ID); string(data) != "v2!" {
		t.Fatal("replacement did not overwrite bytes")
	}

	// Group restriction hides the resource — and with it, the document path.
	group, err := gr.Create(ctx, uniq("Officers"), nil, uid)
	if err != nil {
		t.Fatalf("group: %v", err)
	}
	if err := gr.SetResourceGroups(ctx, res.ID, []string{group.ID}); err != nil {
		t.Fatalf("restrict: %v", err)
	}
	outsider := newMember(t, mr, uniq("tier"), "active")
	if _, err := rr.GetVisible(ctx, res.ID, false, outsider, 2); err != pgx.ErrNoRows {
		t.Fatalf("outsider must see ErrNoRows, got %v", err)
	}
	insider := newMember(t, mr, uniq("tier"), "active")
	if err := gr.SetMembers(ctx, group.ID, []string{insider}); err != nil {
		t.Fatalf("set members: %v", err)
	}
	if _, err := rr.GetVisible(ctx, res.ID, false, insider, 2); err != nil {
		t.Fatalf("insider must see the document resource: %v", err)
	}

	// Folder deletion returns the resource to the root, visibility untouched.
	if err := fr.Delete(ctx, folder.ID); err != nil {
		t.Fatalf("delete folder: %v", err)
	}
	after, err := rr.Get(ctx, res.ID)
	if err != nil {
		t.Fatalf("get after folder delete: %v", err)
	}
	if after.FolderID != nil {
		t.Error("resource must return to root when its folder is deleted")
	}
	if after.FileName == nil {
		t.Error("document must survive folder deletion")
	}
}

func TestIntegration_NestedFoldersAndDownloadLedger(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	fr := repo.NewFoldersRepo(pool)
	rr := repo.NewResourcesRepo(pool)
	dl := repo.NewDocumentDownloadsRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)

	// Nesting: root -> child -> grandchild.
	root, err := fr.Create(ctx, uniq("Legal"), nil)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	child, err := fr.Create(ctx, uniq("Contracts"), &root.ID)
	if err != nil {
		t.Fatalf("child: %v", err)
	}
	grand, err := fr.Create(ctx, uniq("2026"), &child.ID)
	if err != nil {
		t.Fatalf("grandchild: %v", err)
	}
	if grand.ParentID == nil || *grand.ParentID != child.ID {
		t.Fatal("parent linkage lost")
	}

	// Cycle prevention: root cannot move under its own grandchild (or itself).
	if _, err := fr.Rename(ctx, root.ID, nil, &grand.ID, true); err != repo.ErrFolderCycle {
		t.Fatalf("cycle move: got %v, want ErrFolderCycle", err)
	}
	if _, err := fr.Rename(ctx, root.ID, nil, &root.ID, true); err != repo.ErrFolderCycle {
		t.Fatalf("self move: got %v, want ErrFolderCycle", err)
	}
	// Legal sideways move works.
	if _, err := fr.Rename(ctx, grand.ID, nil, &root.ID, true); err != nil {
		t.Fatalf("legal move: %v", err)
	}

	// Deleting the middle folder releases its children to the root.
	if err := fr.Delete(ctx, child.ID); err != nil {
		t.Fatalf("delete middle: %v", err)
	}

	// Preview-only flag round-trips.
	res, err := rr.Create(ctx, &model.Resource{Title: uniq("policy"), Tags: []string{}}, uid)
	if err != nil {
		t.Fatalf("resource: %v", err)
	}
	if _, err := rr.Update(ctx, res.ID, map[string]any{"file_preview_only": true}); err != nil {
		t.Fatalf("set preview-only: %v", err)
	}
	got, _ := rr.Get(ctx, res.ID)
	if !got.FilePreviewOnly {
		t.Fatal("file_preview_only did not persist")
	}

	// Download ledger: insert + lookup by hash, and originals resolve too.
	payload := []byte(uniq("stamped output bytes"))
	sum := sha256.Sum256(payload)
	sha := hex.EncodeToString(sum[:])
	id, err := repo.NewDownloadID()
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	rid := res.ID
	rec := &model.DownloadRecord{ID: id, ResourceID: &rid, UserID: &uid, FileName: "policy.txt", SHA256: sha, IP: "203.0.113.9"}
	if err := dl.Insert(ctx, rec); err != nil {
		t.Fatalf("insert: %v", err)
	}
	found, err := dl.FindBySHA(ctx, sha)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if found.IP != "203.0.113.9" || found.FileName != "policy.txt" || found.UserID == nil || *found.UserID != uid {
		t.Fatalf("ledger row mismatch: %+v", found)
	}
	hist, err := dl.ListByResource(ctx, res.ID, 10)
	if err != nil || len(hist) != 1 {
		t.Fatalf("history: %v len=%d", err, len(hist))
	}
	// An unknown hash is unknown.
	if _, err := dl.FindBySHA(ctx, "0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("unknown hash must not match")
	}
}

func TestIntegration_AgileHierarchyLinksAnalytics(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ai := repo.NewActionItemsRepo(pool)
	cl := repo.NewCardLinksRepo(pool)
	sr := repo.NewSprintsRepo(pool)
	ar := repo.NewAuthRepo(pool)
	mr := repo.NewMembersRepo(pool)
	uid := newUser(t, ar)

	sprint, err := sr.Create(ctx, &model.Sprint{Name: uniq("S"), StartsOn: "2026-08-01", EndsOn: "2026-08-14", Status: "active"}, uid)
	if err != nil {
		t.Fatalf("sprint: %v", err)
	}
	sid := sprint.ID
	mk := func(title, typ string, pts *int, parent *string, status string) *model.ActionItem {
		it, err := ai.Create(ctx, &model.ActionItem{
			Title: uniq(title), CardType: typ, StoryPoints: pts, ParentID: parent,
			SprintID: &sid, Status: status, Priority: "normal",
		}, uid)
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		return it
	}
	p3, p5, p8 := 3, 5, 8

	epic := mk("epic", "epic", nil, nil, "open")
	story := mk("story", "story", &p5, &epic.ID, "open")
	task := mk("task", "task", &p3, &epic.ID, "done")
	sub := mk("sub", "sub_task", &p3, &task.ID, "open")
	spike := mk("spike", "spike", &p8, nil, "in_progress")

	// Hierarchy violations are rejected by the database trigger.
	if _, err := ai.Create(ctx, &model.ActionItem{Title: uniq("bad"), CardType: "sub_task", ParentID: &epic.ID, SprintID: &sid, Status: "open", Priority: "normal"}, uid); err == nil {
		t.Fatal("sub_task under epic must be rejected")
	}
	if _, err := ai.Create(ctx, &model.ActionItem{Title: uniq("bad"), CardType: "epic", ParentID: &epic.ID, SprintID: &sid, Status: "open", Priority: "normal"}, uid); err == nil {
		t.Fatal("epic under epic must be rejected")
	}
	if _, err := ai.Create(ctx, &model.ActionItem{Title: uniq("bad"), CardType: "story", ParentID: &task.ID, SprintID: &sid, Status: "open", Priority: "normal"}, uid); err == nil {
		t.Fatal("story under task must be rejected")
	}
	// Type change that would strand children is rejected.
	if _, err := ai.Update(ctx, task.ID, map[string]any{"card_type": "sub_task"}); err == nil {
		t.Fatal("task->sub_task with a sub-task child must be rejected")
	}
	// Parent title resolves on reads.
	got, _ := ai.Get(ctx, sub.ID)
	if got.ParentTitle == nil || *got.ParentTitle != task.Title {
		t.Fatalf("parent title: %+v", got.ParentTitle)
	}

	// Links: create, no self, no dup, both-side listing.
	if _, err := cl.Create(ctx, story.ID, spike.ID, "blocked_by", uid); err != nil {
		t.Fatalf("link: %v", err)
	}
	if _, err := cl.Create(ctx, story.ID, spike.ID, "blocked_by", uid); err == nil {
		t.Fatal("duplicate link must be rejected")
	}
	if _, err := cl.Create(ctx, story.ID, story.ID, "related_to", uid); err == nil {
		t.Fatal("self link must be rejected")
	}
	fromSide, _ := cl.ListForCard(ctx, story.ID)
	toSide, _ := cl.ListForCard(ctx, spike.ID)
	if len(fromSide) != 1 || len(toSide) != 1 {
		t.Fatalf("link visibility: from=%d to=%d", len(fromSide), len(toSide))
	}

	// Analytics: story(5,open) task(3,done) sub(3,open) spike(8,in_progress) epic(0,open)
	member := newMember(t, mr, uniq("tier"), "active")
	if _, err := ai.Update(ctx, spike.ID, map[string]any{"assignee_id": member}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	a, err := cl.SprintAnalytics(ctx, sid)
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if a.Cards != 5 || a.Points != 19 || a.DoneCards != 1 || a.DonePoints != 3 {
		t.Fatalf("totals: %+v", a)
	}
	if a.BlockedCards != 1 { // story blocked by in-progress spike
		t.Fatalf("blocked: %d", a.BlockedCards)
	}
	if a.UnpointedCards != 1 { // the epic
		t.Fatalf("unpointed: %d", a.UnpointedCards)
	}
	var epicBucket *model.SprintBucket
	for i := range a.ByType {
		if a.ByType[i].Key == "epic" {
			epicBucket = &a.ByType[i]
		}
	}
	if epicBucket == nil || epicBucket.Cards != 1 {
		t.Fatalf("by_type epic bucket: %+v", a.ByType)
	}

	// Done blocker unblocks.
	if _, err := ai.Update(ctx, spike.ID, map[string]any{"status": "done"}); err != nil {
		t.Fatalf("finish spike: %v", err)
	}
	a2, _ := cl.SprintAnalytics(ctx, sid)
	if a2.BlockedCards != 0 {
		t.Fatalf("blocked after done: %d", a2.BlockedCards)
	}
	_ = p3
}

func TestIntegration_ResourceMinRole(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	rr := repo.NewResourcesRepo(pool)
	gr := repo.NewGroupsRepo(pool)
	mr := repo.NewMembersRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)

	officer, admin := "officer", "admin"
	open, _ := rr.Create(ctx, &model.Resource{Title: uniq("open"), Tags: []string{}}, uid)
	offOnly, _ := rr.Create(ctx, &model.Resource{Title: uniq("officers"), Tags: []string{}, VisibleMinRole: &officer}, uid)
	admOnly, _ := rr.Create(ctx, &model.Resource{Title: uniq("admins"), Tags: []string{}, VisibleMinRole: &admin}, uid)

	sees := func(rank int, seesAll bool) map[string]bool {
		items, _, err := rr.List(ctx, repo.ResourceFilter{Limit: 500, ViewerSeesAll: seesAll, ViewerRoleRank: rank})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		m := map[string]bool{}
		for _, it := range items {
			m[it.ID] = true
		}
		return m
	}

	member := sees(2, false)
	if !member[open.ID] || member[offOnly.ID] || member[admOnly.ID] {
		t.Fatalf("member visibility wrong: %v", member)
	}
	off := sees(3, true)
	if !off[open.ID] || !off[offOnly.ID] || off[admOnly.ID] {
		t.Fatal("officer must see officer-level but NOT admin-level, even with group bypass")
	}
	adm := sees(4, true)
	if !adm[admOnly.ID] {
		t.Fatal("admin must see admin-level")
	}

	// GetVisible: role bar hides like a missing row.
	if _, err := rr.GetVisible(ctx, admOnly.ID, true, "", 3); err != pgx.ErrNoRows {
		t.Fatalf("officer GetVisible on admin-level: got %v, want ErrNoRows", err)
	}
	if _, err := rr.GetVisible(ctx, admOnly.ID, true, "", 4); err != nil {
		t.Fatalf("admin GetVisible: %v", err)
	}

	// Role and groups combine as AND: officer-level + group; a group member
	// below the role bar still cannot see it.
	grp, _ := gr.Create(ctx, uniq("Board"), nil, uid)
	insider := newMember(t, mr, uniq("t"), "active")
	if err := gr.SetMembers(ctx, grp.ID, []string{insider}); err != nil {
		t.Fatalf("members: %v", err)
	}
	if err := gr.SetResourceGroups(ctx, offOnly.ID, []string{grp.ID}); err != nil {
		t.Fatalf("restrict: %v", err)
	}
	if _, err := rr.GetVisible(ctx, offOnly.ID, false, insider, 2); err != pgx.ErrNoRows {
		t.Fatalf("group member below role bar must not see: got %v", err)
	}
}

func TestIntegration_Discussions(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ch := repo.NewChannelsRepo(pool)
	rr := repo.NewResourcesRepo(pool)
	ar := repo.NewAuthRepo(pool)
	alice := newUser(t, ar)
	bob := newUser(t, ar)

	// Create: creator auto-joins; names are case-insensitively unique.
	c, err := ch.Create(ctx, uniq("Ideas"), nil, alice)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !c.IsMember || c.MemberCount != 1 {
		t.Fatalf("creator not auto-joined: %+v", c)
	}
	if _, err := ch.Create(ctx, strings.ToUpper(c.Name), nil, alice); err == nil {
		t.Fatal("case-insensitive duplicate name must be rejected")
	}

	// Membership: bob out, then in (idempotent), visible in roster.
	if ok, _ := ch.IsMember(ctx, c.ID, bob); ok {
		t.Fatal("bob should not be a member yet")
	}
	if err := ch.AddMember(ctx, c.ID, bob, alice); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := ch.AddMember(ctx, c.ID, bob, alice); err != nil {
		t.Fatalf("re-add must be idempotent: %v", err)
	}
	got, _ := ch.Get(ctx, c.ID, bob)
	if !got.IsMember || got.MemberCount != 2 || len(got.Members) != 2 {
		t.Fatalf("roster wrong: %+v", got)
	}

	// Messages, threads, and the no-nesting guard.
	res, _ := rr.Create(ctx, &model.Resource{Title: uniq("linked-doc"), Tags: []string{}}, alice)
	root, err := ch.PostMessage(ctx, c.ID, "", alice, "What about a raffle?", res.ID)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if root.ResourceID == nil || *root.ResourceID != res.ID {
		t.Fatal("resource link lost")
	}
	reply, err := ch.PostMessage(ctx, c.ID, root.ID, bob, "Love it. Prizes?", "")
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	if _, err := ch.PostMessage(ctx, c.ID, reply.ID, alice, "nested", ""); err == nil {
		t.Fatal("nested replies must be rejected by the trigger")
	}
	roots, _ := ch.Messages(ctx, c.ID, "", 50)
	if len(roots) != 1 || roots[0].ReplyCount != 1 {
		t.Fatalf("roots: %+v", roots)
	}
	thread, _ := ch.Messages(ctx, c.ID, root.ID, 50)
	if len(thread) != 1 || thread[0].Body != "Love it. Prizes?" {
		t.Fatalf("thread: %+v", thread)
	}

	// Deleting the root cascades its thread; channel delete cascades all.
	if err := ch.DeleteMessage(ctx, root.ID); err != nil {
		t.Fatalf("delete root: %v", err)
	}
	if _, err := ch.GetMessage(ctx, reply.ID); err == nil {
		t.Fatal("replies must cascade with their root")
	}
	if err := ch.RemoveMember(ctx, c.ID, bob); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if err := ch.Delete(ctx, c.ID); err != nil {
		t.Fatalf("delete channel: %v", err)
	}
}

func TestIntegration_CardContributors(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ai := repo.NewActionItemsRepo(pool)
	mr := repo.NewMembersRepo(pool)
	ar := repo.NewAuthRepo(pool)
	uid := newUser(t, ar)

	m1 := newMember(t, mr, uniq("tier"), "active")
	m2 := newMember(t, mr, uniq("tier"), "active")

	card, err := ai.Create(ctx, &model.ActionItem{Title: uniq("Card"), Status: "open", Priority: "normal"}, uid)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(card.Contributors) != 0 {
		t.Fatalf("new card contributors = %+v, want none", card.Contributors)
	}

	// Set two, read them back with names, on both Get and List.
	got, err := ai.SetContributors(ctx, card.ID, []string{m1, m2})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if len(got.Contributors) != 2 {
		t.Fatalf("contributors = %+v, want 2", got.Contributors)
	}
	for _, c := range got.Contributors {
		if c.MemberName == "" || (c.MemberID != m1 && c.MemberID != m2) {
			t.Fatalf("bad contributor row: %+v", c)
		}
	}
	listed, _, err := ai.List(ctx, repo.ActionItemFilter{Limit: 500})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, it := range listed {
		if it.ID == card.ID {
			found = true
			if len(it.Contributors) != 2 {
				t.Fatalf("listed contributors = %+v, want 2", it.Contributors)
			}
		}
	}
	if !found {
		t.Fatal("card missing from list")
	}

	// Replace shrinks the roster; duplicates collapse; unknown members refuse.
	got, err = ai.SetContributors(ctx, card.ID, []string{m2, m2})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(got.Contributors) != 1 || got.Contributors[0].MemberID != m2 {
		t.Fatalf("after replace = %+v, want just m2", got.Contributors)
	}
	if _, err := ai.SetContributors(ctx, card.ID, []string{"00000000-0000-0000-0000-000000000001"}); err == nil {
		t.Fatal("unknown member accepted as contributor")
	}
	if _, err := ai.SetContributors(ctx, "00000000-0000-0000-0000-000000000002", nil); err == nil {
		t.Fatal("unknown card accepted")
	}

	// Deleting the card cascades its contributor rows.
	if err := ai.Delete(ctx, card.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM action_item_contributors WHERE action_item_id = $1::uuid`, card.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("contributor rows survived card deletion: %d", n)
	}
}

func TestIntegration_CardReporter(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ai := repo.NewActionItemsRepo(pool)
	mr := repo.NewMembersRepo(pool)
	ar := repo.NewAuthRepo(pool)

	// A creator whose login IS linked to a member: reporter shows the name.
	member := newMember(t, mr, uniq("tier"), "active")
	named, err := ar.CreateUser(ctx, uniq("rep")+"@example.com", "x", "officer", &member)
	if err != nil {
		t.Fatalf("named user: %v", err)
	}
	card, err := ai.Create(ctx, &model.ActionItem{Title: uniq("Card"), Status: "open", Priority: "normal"}, named.ID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	m, err := mr.Get(ctx, member)
	if err != nil {
		t.Fatalf("get member: %v", err)
	}
	if card.ReporterName != m.DisplayName {
		t.Fatalf("reporter = %q, want member name %q", card.ReporterName, m.DisplayName)
	}

	// Present identically on the list read path.
	listed, _, err := ai.List(ctx, repo.ActionItemFilter{Limit: 500})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var seen bool
	for _, it := range listed {
		if it.ID == card.ID {
			seen = true
			if it.ReporterName != m.DisplayName {
				t.Fatalf("listed reporter = %q, want %q", it.ReporterName, m.DisplayName)
			}
		}
	}
	if !seen {
		t.Fatal("card missing from list")
	}

	// Reporter is immutable: Update never touches created_by even if asked.
	if _, err := ai.Update(ctx, card.ID, map[string]any{"created_by": member, "title": "renamed"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, err := ai.Get(ctx, card.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if after.CreatedBy != named.ID || after.ReporterName != m.DisplayName {
		t.Fatalf("reporter changed after update: created_by=%s name=%q", after.CreatedBy, after.ReporterName)
	}

	// A creator whose login is NOT member-linked falls back to the account email.
	plainUID := newUser(t, ar) // officer, memberID nil
	card2, err := ai.Create(ctx, &model.ActionItem{Title: uniq("Card"), Status: "open", Priority: "normal"}, plainUID)
	if err != nil {
		t.Fatalf("create2: %v", err)
	}
	if !strings.Contains(card2.ReporterName, "@example.com") {
		t.Fatalf("unlinked reporter = %q, want an email fallback", card2.ReporterName)
	}
}
