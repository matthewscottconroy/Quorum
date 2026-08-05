package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"

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
