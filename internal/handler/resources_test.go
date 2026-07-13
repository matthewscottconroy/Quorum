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
	req := chiRequest("GET", "/resources/r1", "", map[string]string{"id": "r1"})
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
	req := chiRequest("GET", "/resources/missing", "", map[string]string{"id": "missing"})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404", rr.Code)
	}
}

// ---- Update ----

func TestResourcesUpdate_Success(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{
		UpdateFn: func(_ context.Context, id string, res *model.Resource) (*model.Resource, error) {
			res.ID = id
			return res, nil
		},
	})
	body := `{"title":"Updated Title"}`
	req := chiRequest("PATCH", "/resources/r1", body, map[string]string{"id": "r1"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestResourcesUpdate_MissingTitle(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{})
	body := `{"url":"https://example.com"}`
	req := chiRequest("PATCH", "/resources/r1", body, map[string]string{"id": "r1"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

// ---- Delete ----

func TestResourcesDelete_Success(t *testing.T) {
	deleted := false
	h := NewResourcesHandler(&mockResourcesRepo{
		DeleteFn: func(_ context.Context, _ string) error { deleted = true; return nil },
	})
	req := chiRequest("DELETE", "/resources/r1", "", map[string]string{"id": "r1"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 204 {
		t.Errorf("status: got %d, want 204", rr.Code)
	}
	if !deleted {
		t.Error("expected Delete to be called")
	}
}

func TestResourcesDelete_RepoError(t *testing.T) {
	h := NewResourcesHandler(&mockResourcesRepo{
		DeleteFn: func(_ context.Context, _ string) error { return errors.New("db error") },
	})
	req := chiRequest("DELETE", "/resources/r1", "", map[string]string{"id": "r1"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}
