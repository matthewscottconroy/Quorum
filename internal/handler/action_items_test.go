package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

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
	req := httptest.NewRequest("GET", "/action-items?status=open&assignee_id="+testUUID, nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if capturedFilter.Status != "open" {
		t.Errorf("status: got %q, want open", capturedFilter.Status)
	}
	if capturedFilter.AssigneeID != testUUID {
		t.Errorf("assignee_id: got %q, want %q", capturedFilter.AssigneeID, testUUID)
	}
}

func TestActionItemsList_BadUUIDFilters(t *testing.T) {
	// Non-UUID id filters must 400 before hitting the repo.
	for _, param := range []string{"assignee_id", "meeting_id", "plan_id"} {
		h := NewActionItemsHandler(&mockActionItemsRepo{})
		req := httptest.NewRequest("GET", "/action-items?"+param+"=garbage", nil)
		rr := httptest.NewRecorder()
		h.List(rr, req)
		if rr.Code != 400 {
			t.Errorf("%s=garbage: got %d, want 400", param, rr.Code)
		}
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

func TestActionItemsCreate_InvalidPriority(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{})
	body := `{"title":"Task","priority":"urgent"}`
	req := httptest.NewRequest("POST", "/action-items", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for invalid priority", rr.Code)
	}
}

func TestActionItemsCreate_BadUUIDRefs(t *testing.T) {
	for _, field := range []string{"assignee_id", "meeting_id", "plan_id"} {
		h := NewActionItemsHandler(&mockActionItemsRepo{})
		body := `{"title":"Task","` + field + `":"not-a-uuid"}`
		req := httptest.NewRequest("POST", "/action-items", strings.NewReader(body))
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		if rr.Code != 400 {
			t.Errorf("%s=not-a-uuid: got %d, want 400", field, rr.Code)
		}
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
	req := chiRequest("PATCH", "/action-items/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestActionItemsUpdate_BadJSON(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{})
	req := chiRequest("PATCH", "/action-items/"+testUUID, "{bad", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestActionItemsUpdate_BadUUID(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{})
	req := chiRequest("PATCH", "/action-items/ai1", `{"status":"done"}`, map[string]string{"id": "ai1"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID id", rr.Code)
	}
}

func TestActionItemsUpdate_EmptyBody(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{})
	req := chiRequest("PATCH", "/action-items/"+testUUID, `{}`, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for empty body", rr.Code)
	}
}

func TestActionItemsUpdate_InvalidStatus(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{})
	req := chiRequest("PATCH", "/action-items/"+testUUID, `{"status":"finished"}`, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for invalid status", rr.Code)
	}
}

func TestActionItemsUpdate_InvalidPriority(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{})
	req := chiRequest("PATCH", "/action-items/"+testUUID, `{"priority":"urgent"}`, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for invalid priority", rr.Code)
	}
}

func TestActionItemsUpdate_BadUUIDRefs(t *testing.T) {
	for _, field := range []string{"assignee_id", "meeting_id", "plan_id"} {
		h := NewActionItemsHandler(&mockActionItemsRepo{})
		body := `{"` + field + `":"not-a-uuid"}`
		req := chiRequest("PATCH", "/action-items/"+testUUID, body, map[string]string{"id": testUUID})
		rr := httptest.NewRecorder()
		h.Update(rr, req)
		if rr.Code != 400 {
			t.Errorf("%s=not-a-uuid: got %d, want 400", field, rr.Code)
		}
	}
}

func TestActionItemsUpdate_NotFound(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{
		UpdateFn: func(_ context.Context, _ string, _ map[string]any) (*model.ActionItem, error) {
			return nil, pgx.ErrNoRows
		},
	})
	req := chiRequest("PATCH", "/action-items/"+testUUID2, `{"status":"done"}`, map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404 for ErrNoRows", rr.Code)
	}
}

func TestActionItemsUpdate_RepoError(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{
		UpdateFn: func(_ context.Context, _ string, _ map[string]any) (*model.ActionItem, error) {
			return nil, errors.New("db error")
		},
	})
	body := `{"status":"done"}`
	req := chiRequest("PATCH", "/action-items/"+testUUID, body, map[string]string{"id": testUUID})
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
		GetFn: func(_ context.Context, id string) (*model.ActionItem, error) {
			return &model.ActionItem{ID: id, Title: "FileReport"}, nil
		},
		DeleteFn: func(_ context.Context, _ string) error { deleted = true; return nil },
	})
	req := chiRequest("DELETE", "/action-items/"+testUUID+"?confirm=FileReport", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 204 {
		t.Errorf("status: got %d, want 204; body: %s", rr.Code, rr.Body)
	}
	if !deleted {
		t.Error("expected Delete to be called")
	}
}

func TestActionItemsDelete_NotifiesAssignee(t *testing.T) {
	fn := &fakeNotifier{}
	h := NewActionItemsHandler(&mockActionItemsRepo{
		GetFn: func(_ context.Context, id string) (*model.ActionItem, error) {
			return &model.ActionItem{ID: id, Title: "FileReport"}, nil
		},
		AssigneeEmailFn: func(_ context.Context, _ string) (string, error) { return "assignee@example.com", nil },
		DeleteFn:        func(_ context.Context, _ string) error { return nil },
	})
	h.SetNotifier(fn)
	req := withCtxUser(chiRequest("DELETE", "/action-items/"+testUUID+"?confirm=FileReport", "", map[string]string{"id": testUUID}), "actor-1", "superadmin")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 204 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if !fn.called || fn.entityType != "action item" || len(fn.affected) != 1 || fn.affected[0] != "assignee@example.com" {
		t.Errorf("notifier: called=%v type=%q affected=%v", fn.called, fn.entityType, fn.affected)
	}
}

func TestActionItemsDelete_BadUUID(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{})
	req := chiRequest("DELETE", "/action-items/ai1", "", map[string]string{"id": "ai1"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID id", rr.Code)
	}
}

func TestActionItemsDelete_NotFound(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{
		GetFn: func(_ context.Context, _ string) (*model.ActionItem, error) { return nil, pgx.ErrNoRows },
	})
	req := chiRequest("DELETE", "/action-items/"+testUUID2+"?confirm=FileReport", "", map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404 for ErrNoRows", rr.Code)
	}
}

func TestActionItemsDelete_RepoError(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{
		GetFn: func(_ context.Context, id string) (*model.ActionItem, error) {
			return &model.ActionItem{ID: id, Title: "FileReport"}, nil
		},
		DeleteFn: func(_ context.Context, _ string) error { return errors.New("db error") },
	})
	req := chiRequest("DELETE", "/action-items/"+testUUID+"?confirm=FileReport", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

func TestActionItemsDelete_MissingConfirm(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{
		GetFn: func(_ context.Context, id string) (*model.ActionItem, error) {
			return &model.ActionItem{ID: id, Title: "FileReport"}, nil
		},
	})
	req := chiRequest("DELETE", "/action-items/"+testUUID, "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 400 || errCode(t, rr) != "confirmation_required" {
		t.Errorf("status/code: got %d/%s, want 400/confirmation_required", rr.Code, rr.Body)
	}
}

func TestActionItemsDelete_MismatchedConfirm(t *testing.T) {
	h := NewActionItemsHandler(&mockActionItemsRepo{
		GetFn: func(_ context.Context, id string) (*model.ActionItem, error) {
			return &model.ActionItem{ID: id, Title: "FileReport"}, nil
		},
	})
	req := chiRequest("DELETE", "/action-items/"+testUUID+"?confirm=Wrong", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 400 || errCode(t, rr) != "confirmation_mismatch" {
		t.Errorf("status/code: got %d/%s, want 400/confirmation_mismatch", rr.Code, rr.Body)
	}
}
