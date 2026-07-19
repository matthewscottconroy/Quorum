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

func testResource(id, title string) *model.Resource {
	return &model.Resource{ID: id, Title: title, Tags: []string{}}
}

// ---- List ----

func TestResourcesList_ReturnsPage(t *testing.T) {
	resources := []model.Resource{*testResource("r1", "Policy Doc"), *testResource("r2", "Bylaws")}
	h := NewResourcesHandler(&mockResourcesRepo{
		ListFn: func(_ context.Context, _ repo.ResourceFilter) ([]model.Resource, int, error) {
			return resources, len(resources), nil
		},
	})
	req := httptest.NewRequest("GET", "/resources", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	var got model.Page[model.Resource]
	json.NewDecoder(rr.Body).Decode(&got)
	if len(got.Data) != 2 {
		t.Errorf("expected 2 resources, got %d", len(got.Data))
	}
	if got.Total != 2 {
		t.Errorf("expected total=2, got %d", got.Total)
	}
}

func TestResourcesList_PassesFilters(t *testing.T) {
	var capturedFilter repo.ResourceFilter
	h := NewResourcesHandler(&mockResourcesRepo{
		ListFn: func(_ context.Context, f repo.ResourceFilter) ([]model.Resource, int, error) {
			capturedFilter = f
			return nil, 0, nil
		},
	})
	req := httptest.NewRequest("GET", "/resources?search=policy&category=legal&tag=governance", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if capturedFilter.Search != "policy" {
		t.Errorf("search: got %q", capturedFilter.Search)
	}
	if capturedFilter.Category != "legal" {
		t.Errorf("category: got %q", capturedFilter.Category)
	}
	if capturedFilter.Tag != "governance" {
		t.Errorf("tag: got %q", capturedFilter.Tag)
	}
}

func TestResourcesList_RepoError(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{
		ListFn: func(_ context.Context, _ repo.ResourceFilter) ([]model.Resource, int, error) {
			return nil, 0, errors.New("db error")
		},
	})
	req := httptest.NewRequest("GET", "/resources", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

// ---- Create ----

func TestResourcesCreate_Success(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{
		CreateFn: func(_ context.Context, res *model.Resource, _ string) (*model.Resource, error) {
			res.ID = "new-r"
			return res, nil
		},
	})
	body := `{"title":"Annual Report","url":"https://example.com/report"}`
	req := withCtxUser(httptest.NewRequest("POST", "/resources", strings.NewReader(body)), "u1", "officer")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestResourcesCreate_MissingTitle(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{})
	body := `{"url":"https://example.com"}`
	req := httptest.NewRequest("POST", "/resources", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestResourcesCreate_BadJSON(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{})
	req := httptest.NewRequest("POST", "/resources", strings.NewReader("{bad"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

// ---- Get ----

func TestResourcesGet_Success(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{
		GetFn: func(_ context.Context, id string) (*model.Resource, error) {
			return testResource(id, "Policy Doc"), nil
		},
	})
	req := chiRequest("GET", "/resources/"+testUUID, "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestResourcesGet_NotFound(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{
		GetFn: func(_ context.Context, _ string) (*model.Resource, error) {
			return nil, errors.New("not found")
		},
	})
	req := chiRequest("GET", "/resources/"+testUUID2, "", map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404", rr.Code)
	}
}

func TestResourcesGet_BadUUID(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{})
	req := chiRequest("GET", "/resources/r1", "", map[string]string{"id": "r1"})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID id", rr.Code)
	}
}

// ---- Update ----

func TestResourcesUpdate_Success(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{
		UpdateFn: func(_ context.Context, id string, fields map[string]any) (*model.Resource, error) {
			return testResource(id, "Updated Title"), nil
		},
	})
	body := `{"title":"Updated Title"}`
	req := chiRequest("PATCH", "/resources/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestResourcesUpdate_PartialDescriptionOnly(t *testing.T) {
	// A body with only "description" is a valid partial update.
	var capturedFields map[string]any
	h := NewResourcesHandler(&mockResourcesRepo{
		UpdateFn: func(_ context.Context, id string, fields map[string]any) (*model.Resource, error) {
			capturedFields = fields
			return testResource(id, "Policy Doc"), nil
		},
	})
	body := `{"description":"An updated description"}`
	req := chiRequest("PATCH", "/resources/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d, want 200; body: %s", rr.Code, rr.Body)
	}
	if len(capturedFields) != 1 {
		t.Errorf("fields: got %v, want only description", capturedFields)
	}
	if capturedFields["description"] != "An updated description" {
		t.Errorf("description field: got %v", capturedFields["description"])
	}
}

func TestResourcesUpdate_NoValidFields(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{})
	body := `{"unknown_field":"value"}`
	req := chiRequest("PATCH", "/resources/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 when no valid fields", rr.Code)
	}
}

func TestResourcesUpdate_EmptyTitle(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{})
	body := `{"title":""}`
	req := chiRequest("PATCH", "/resources/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for empty title", rr.Code)
	}
}

func TestResourcesUpdate_NonStringTitle(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{})
	body := `{"title":123}`
	req := chiRequest("PATCH", "/resources/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-string title", rr.Code)
	}
}

func TestResourcesUpdate_BadTags(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{})
	body := `{"tags":"not-an-array"}`
	req := chiRequest("PATCH", "/resources/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-array tags", rr.Code)
	}
}

func TestResourcesUpdate_BadUUID(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{})
	req := chiRequest("PATCH", "/resources/r1", `{"title":"X"}`, map[string]string{"id": "r1"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID id", rr.Code)
	}
}

func TestResourcesUpdate_NotFound(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{
		UpdateFn: func(_ context.Context, _ string, _ map[string]any) (*model.Resource, error) {
			return nil, pgx.ErrNoRows
		},
	})
	req := chiRequest("PATCH", "/resources/"+testUUID2, `{"title":"X"}`, map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404 for ErrNoRows", rr.Code)
	}
}

func TestResourcesUpdate_RepoError(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{
		UpdateFn: func(_ context.Context, _ string, _ map[string]any) (*model.Resource, error) {
			return nil, errors.New("db error")
		},
	})
	req := chiRequest("PATCH", "/resources/"+testUUID, `{"title":"X"}`, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

// ---- Delete ----

func TestResourcesDelete_Success(t *testing.T) {
	deleted := false
	h := NewResourcesHandler(&mockResourcesRepo{
		GetFn: func(_ context.Context, id string) (*model.Resource, error) {
			return &model.Resource{ID: id, Title: "Bylaws"}, nil
		},
		DeleteFn: func(_ context.Context, _ string) error { deleted = true; return nil },
	})
	req := chiRequest("DELETE", "/resources/"+testUUID+"?confirm=Bylaws", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 204 {
		t.Errorf("status: got %d, want 204; body: %s", rr.Code, rr.Body)
	}
	if !deleted {
		t.Error("expected Delete to be called")
	}
}

func TestResourcesDelete_BadUUID(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{})
	req := chiRequest("DELETE", "/resources/r1", "", map[string]string{"id": "r1"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID id", rr.Code)
	}
}

func TestResourcesDelete_NotFound(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{
		GetFn: func(_ context.Context, _ string) (*model.Resource, error) { return nil, pgx.ErrNoRows },
	})
	req := chiRequest("DELETE", "/resources/"+testUUID2+"?confirm=Bylaws", "", map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404 for ErrNoRows", rr.Code)
	}
}

func TestResourcesDelete_RepoError(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{
		GetFn: func(_ context.Context, id string) (*model.Resource, error) {
			return &model.Resource{ID: id, Title: "Bylaws"}, nil
		},
		DeleteFn: func(_ context.Context, _ string) error { return errors.New("db error") },
	})
	req := chiRequest("DELETE", "/resources/"+testUUID+"?confirm=Bylaws", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

func TestResourcesDelete_MissingConfirm(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{
		GetFn: func(_ context.Context, id string) (*model.Resource, error) {
			return &model.Resource{ID: id, Title: "Bylaws"}, nil
		},
	})
	req := chiRequest("DELETE", "/resources/"+testUUID, "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 400 || errCode(t, rr) != "confirmation_required" {
		t.Errorf("status/code: got %d/%s, want 400/confirmation_required", rr.Code, rr.Body)
	}
}

func TestResourcesDelete_MismatchedConfirm(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{
		GetFn: func(_ context.Context, id string) (*model.Resource, error) {
			return &model.Resource{ID: id, Title: "Bylaws"}, nil
		},
	})
	req := chiRequest("DELETE", "/resources/"+testUUID+"?confirm=Wrong", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 400 || errCode(t, rr) != "confirmation_mismatch" {
		t.Errorf("status/code: got %d/%s, want 400/confirmation_mismatch", rr.Code, rr.Body)
	}
}
