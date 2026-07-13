package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"quorum/internal/model"
	"quorum/internal/repo"
)

func testActionItem(id, title string) *model.ActionItem {
	return &model.ActionItem{ID: id, Title: title, Status: "open", Priority: "normal"}
}

// ---- List ----

func TestActionItemsList_ReturnsPage(t *testing.T) {
	items := []model.ActionItem{*testActionItem("ai1", "Draft charter"), *testActionItem("ai2", "Schedule review")}
	h := NewActionItemsHandler(&mockActionItemsRepo{
		ListFn: func(_ context.Context, _ repo.ActionItemFilter) ([]model.ActionItem, int, error) {
			return items, len(items), nil
		},
	})
	req := httptest.NewRequest("GET", "/action-items", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	var got model.Page[model.ActionItem]
	json.NewDecoder(rr.Body).Decode(&got)
	if len(got.Data) != 2 {
		t.Errorf("expected 2 items, got %d", len(got.Data))
	}
	if got.Total != 2 {
		t.Errorf("expected total=2, got %d", got.Total)
	}
}

func TestActionItemsList_PassesFilters(t *testing.T) {
	var capturedFilter repo.ActionItemFilter
	h := NewActionItemsHandler(&mockActionItemsRepo{
		ListFn: func(_ context.Context, f repo.ActionItemFilter) ([]model.ActionItem, int, error) {
			capturedFilter = f
			return nil, 0, nil
		},
	})
	req := httptest.NewRequest("GET", "/action-items?status=open&assignee_id=user-1", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if capturedFilter.Status != "open" {
		t.Errorf("status: got %q, want open", capturedFilter.Status)
	}
	if capturedFilter.AssigneeID != "user-1" {
		t.Errorf("assignee_id: got %q, want user-1", capturedFilter.AssigneeID)
	}
}

func TestActionItemsList_RepoError(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{
		ListFn: func(_ context.Context, _ repo.ActionItemFilter) ([]model.ActionItem, int, error) {
			return nil, 0, errors.New("db error")
		},
	})
	req := httptest.NewRequest("GET", "/action-items", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

// ---- Create ----

func TestActionItemsCreate_Success(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{
		CreateFn: func(_ context.Context, item *model.ActionItem, _ string) (*model.ActionItem, error) {
			item.ID = "new-ai"
			return item, nil
		},
	})
	body := `{"title":"Review financials","priority":"high"}`
	req := withCtxUser(httptest.NewRequest("POST", "/action-items", strings.NewReader(body)), "u1", "officer")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestActionItemsCreate_DefaultsToNormalPriority(t *testing.T) {
	var capturedItem model.ActionItem
	h := NewActionItemsHandler(&mockActionItemsRepo{
		CreateFn: func(_ context.Context, item *model.ActionItem, _ string) (*model.ActionItem, error) {
			capturedItem = *item
			return item, nil
		},
	})
	body := `{"title":"Some task"}`
	req := withCtxUser(httptest.NewRequest("POST", "/action-items", strings.NewReader(body)), "u1", "officer")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if capturedItem.Priority != "normal" {
		t.Errorf("expected default priority 'normal', got %q", capturedItem.Priority)
	}
}

func TestActionItemsCreate_SetsStatusOpen(t *testing.T) {
	var capturedItem model.ActionItem
	h := NewActionItemsHandler(&mockActionItemsRepo{
		CreateFn: func(_ context.Context, item *model.ActionItem, _ string) (*model.ActionItem, error) {
			capturedItem = *item
			return item, nil
		},
	})
	body := `{"title":"Some task"}`
	req := withCtxUser(httptest.NewRequest("POST", "/action-items", strings.NewReader(body)), "u1", "officer")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if capturedItem.Status != "open" {
		t.Errorf("expected status 'open', got %q", capturedItem.Status)
	}
}

func TestActionItemsCreate_MissingTitle(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{})
	body := `{"priority":"high"}`
	req := httptest.NewRequest("POST", "/action-items", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestActionItemsCreate_BadDueDate(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{})
	body := `{"title":"Task","due_date":"not-a-date"}`
	req := httptest.NewRequest("POST", "/action-items", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for bad due_date", rr.Code)
	}
}

// ---- Update ----

func TestActionItemsUpdate_Success(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{
		UpdateFn: func(_ context.Context, id string, fields map[string]any) (*model.ActionItem, error) {
			return testActionItem(id, "Updated"), nil
		},
	})
	body := `{"status":"done","title":"Updated"}`
	req := chiRequest("PATCH", "/action-items/ai1", body, map[string]string{"id": "ai1"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestActionItemsUpdate_BadJSON(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{})
	req := chiRequest("PATCH", "/action-items/ai1", "{bad", map[string]string{"id": "ai1"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestActionItemsUpdate_RepoError(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{
		UpdateFn: func(_ context.Context, _ string, _ map[string]any) (*model.ActionItem, error) {
			return nil, errors.New("db error")
		},
	})
	body := `{"status":"done"}`
	req := chiRequest("PATCH", "/action-items/ai1", body, map[string]string{"id": "ai1"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

// ---- Delete ----

func TestActionItemsDelete_Success(t *testing.T) {
	deleted := false
	h := NewActionItemsHandler(&mockActionItemsRepo{
		DeleteFn: func(_ context.Context, _ string) error { deleted = true; return nil },
	})
	req := chiRequest("DELETE", "/action-items/ai1", "", map[string]string{"id": "ai1"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 204 {
		t.Errorf("status: got %d, want 204", rr.Code)
	}
	if !deleted {
		t.Error("expected Delete to be called")
	}
}

func TestActionItemsDelete_RepoError(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{
		DeleteFn: func(_ context.Context, _ string) error { return errors.New("db error") },
	})
	req := chiRequest("DELETE", "/action-items/ai1", "", map[string]string{"id": "ai1"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}
