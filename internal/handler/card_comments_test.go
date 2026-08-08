package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"quorum/internal/auth"
	"quorum/internal/model"
)

type mockCardCommentsRepo struct {
	ListFn   func(ctx context.Context, actionItemID string) ([]model.CardComment, error)
	CreateFn func(ctx context.Context, actionItemID, authorID, body string) (*model.CardComment, error)
	GetFn    func(ctx context.Context, id string) (*model.CardComment, error)
	DeleteFn func(ctx context.Context, id string) error
}

func (m *mockCardCommentsRepo) List(ctx context.Context, id string) ([]model.CardComment, error) {
	return m.ListFn(ctx, id)
}
func (m *mockCardCommentsRepo) Create(ctx context.Context, itemID, authorID, body string) (*model.CardComment, error) {
	return m.CreateFn(ctx, itemID, authorID, body)
}
func (m *mockCardCommentsRepo) Get(ctx context.Context, id string) (*model.CardComment, error) {
	return m.GetFn(ctx, id)
}
func (m *mockCardCommentsRepo) Delete(ctx context.Context, id string) error {
	return m.DeleteFn(ctx, id)
}

const (
	commentID = "5f9f1a2b-0000-4000-8000-000000000001"
	authorUID = "5f9f1a2b-0000-4000-8000-0000000000aa"
	otherUID  = "5f9f1a2b-0000-4000-8000-0000000000bb"
)

func commentDeleteReq(t *testing.T, userID, role string, repo *mockCardCommentsRepo) *httptest.ResponseRecorder {
	t.Helper()
	h := NewCardCommentsHandler(repo, nil)
	req := reqWithParam("DELETE", "/action-items/x/comments/"+commentID, "", map[string]string{"cid": commentID})
	ctx := context.WithValue(req.Context(), ctxUserID, userID)
	ctx = context.WithValue(ctx, ctxRole, role)
	rr := httptest.NewRecorder()
	h.Delete(rr, req.WithContext(ctx))
	return rr
}

func ownComment() *mockCardCommentsRepo {
	author := authorUID
	deleted := false
	return &mockCardCommentsRepo{
		GetFn: func(_ context.Context, id string) (*model.CardComment, error) {
			if deleted {
				return nil, pgx.ErrNoRows
			}
			return &model.CardComment{ID: id, AuthorID: &author, Body: "hi"}, nil
		},
		DeleteFn: func(_ context.Context, _ string) error { deleted = true; return nil },
	}
}

func TestCardCommentDelete_AuthorMay(t *testing.T) {
	rr := commentDeleteReq(t, authorUID, "member", ownComment())
	if rr.Code != http.StatusNoContent {
		t.Fatalf("author delete: got %d, want 204 (%s)", rr.Code, rr.Body)
	}
}

func TestCardCommentDelete_OtherMemberForbidden(t *testing.T) {
	repo := ownComment()
	rr := commentDeleteReq(t, otherUID, "member", repo)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("other member delete: got %d, want 403 (%s)", rr.Code, rr.Body)
	}
	// And the comment must still exist.
	if _, err := repo.Get(context.Background(), commentID); err != nil {
		t.Fatal("comment was deleted despite 403")
	}
}

func TestCardCommentDelete_OfficerForbidden_AdminModerates(t *testing.T) {
	if rr := commentDeleteReq(t, otherUID, "officer", ownComment()); rr.Code != http.StatusForbidden {
		t.Fatalf("officer moderating: got %d, want 403", rr.Code)
	}
	if rr := commentDeleteReq(t, otherUID, "admin", ownComment()); rr.Code != http.StatusNoContent {
		t.Fatalf("admin moderating: got %d, want 204", rr.Code)
	}
}

func TestCardCommentCreate_RejectsEmptyAndOversized(t *testing.T) {
	h := NewCardCommentsHandler(&mockCardCommentsRepo{}, &mockActionItemsRepo{
		GetFn: func(_ context.Context, id string) (*model.ActionItem, error) {
			return &model.ActionItem{ID: id}, nil
		},
	})
	for _, body := range []string{`{"body":"  "}`, `{"body":""}`} {
		req := reqWithParam("POST", "/action-items/"+commentID+"/comments", body, map[string]string{"id": commentID})
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("body %q: got %d, want 400", body, rr.Code)
		}
	}
}

// ---- document stamping & preview-only (resources) ----

func TestStampDownload_FormatsAndFidelity(t *testing.T) {
	base := []byte("hello world")
	at := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		stamped bool
		marker  string
	}{
		{"notes.txt", true, "-- Quorum provenance"},
		{"doc.md", true, "<!-- Quorum provenance"},
		{"diagram.mmd", true, "%% Quorum provenance"},
		{"photo.png", false, ""},
		{"book.pdf", false, ""},
	} {
		out, stamped := stampDownload(base, tc.name, "a@b.c", "198.51.100.7", "rec-1", at)
		if stamped != tc.stamped {
			t.Fatalf("%s: stamped=%v want %v", tc.name, stamped, tc.stamped)
		}
		if !stamped {
			if string(out) != string(base) {
				t.Fatalf("%s: unstamped bytes must be identical", tc.name)
			}
			continue
		}
		s := string(out)
		if !strings.HasPrefix(s, "hello world") || !strings.Contains(s, tc.marker) ||
			!strings.Contains(s, "a@b.c") || !strings.Contains(s, "198.51.100.7") || !strings.Contains(s, "rec-1") {
			t.Fatalf("%s: stamp malformed: %q", tc.name, s)
		}
	}
}

func TestDownloadFile_PreviewOnlyRefused(t *testing.T) {
	name := "secret.pdf"
	repo := &mockResourcesRepo{
		GetVisibleFn: func(_ context.Context, id string, _ bool, _ string) (*model.Resource, error) {
			return &model.Resource{ID: id, Title: "Secret", FileName: &name, FilePreviewOnly: true, Tags: []string{}}, nil
		},
	}
	h := NewResourcesHandler(repo)
	req := reqWithParam("GET", "/resources/"+commentID+"/file", "", map[string]string{"id": commentID})
	rr := httptest.NewRecorder()
	h.DownloadFile(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("preview-only download: got %d, want 403 (%s)", rr.Code, rr.Body)
	}
}

// ---- org-mandated 2FA gate ----

func TestMFAGate(t *testing.T) {
	mw := NewMiddleware(testSecret, false)
	policy := "member"
	mw.SetMFAPolicy(func() string { return policy })
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	callPath := func(token, path string) int {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		mw.Auth(okHandler).ServeHTTP(rr, req)
		return rr.Code
	}

	unenrolled, _ := auth.IssueAccessTokenFor("u1", "member", "", false, testSecret, time.Minute)
	enrolled, _ := auth.IssueAccessTokenFor("u1", "member", "", true, testSecret, time.Minute)

	if got := callPath(unenrolled, "/api/v1/members"); got != 403 {
		t.Fatalf("unenrolled under mandate: got %d, want 403", got)
	}
	for _, p := range []string{"/api/v1/auth/2fa/setup", "/api/v1/auth/2fa/enable", "/api/v1/auth/me", "/api/v1/auth/logout"} {
		if got := callPath(unenrolled, p); got != 200 {
			t.Fatalf("exempt path %s: got %d, want 200", p, got)
		}
	}
	if got := callPath(enrolled, "/api/v1/members"); got != 200 {
		t.Fatalf("enrolled: got %d, want 200", got)
	}
	policy = "admin" // mandate above this user's role: not gated
	if got := callPath(unenrolled, "/api/v1/members"); got != 200 {
		t.Fatalf("below mandate role: got %d, want 200", got)
	}
	policy = "off"
	if got := callPath(unenrolled, "/api/v1/members"); got != 200 {
		t.Fatalf("policy off: got %d, want 200", got)
	}
}
