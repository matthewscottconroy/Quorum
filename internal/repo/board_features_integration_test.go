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

	folder, err := fr.Create(ctx, uniq("Bylaws"))
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
	if _, err := rr.GetVisible(ctx, res.ID, false, outsider); err != pgx.ErrNoRows {
		t.Fatalf("outsider must see ErrNoRows, got %v", err)
	}
	insider := newMember(t, mr, uniq("tier"), "active")
	if err := gr.SetMembers(ctx, group.ID, []string{insider}); err != nil {
		t.Fatalf("set members: %v", err)
	}
	if _, err := rr.GetVisible(ctx, res.ID, false, insider); err != nil {
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
