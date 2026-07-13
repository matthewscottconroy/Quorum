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

func testContact(id, name string) *model.Contact {
	return &model.Contact{ID: id, Name: name, Tags: []string{}}
}

// ---- List ----

func TestContactsList_ReturnsPage(t *testing.T) {
	contacts := []model.Contact{*testContact("c1", "Alice"), *testContact("c2", "Bob")}
	h := NewContactsHandler(&mockContactsRepo{
		ListFn: func(_ context.Context, _ repo.ContactFilter) ([]model.Contact, int, error) {
			return contacts, len(contacts), nil
		},
	})
	req := httptest.NewRequest("GET", "/contacts", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	var got model.Page[model.Contact]
	json.NewDecoder(rr.Body).Decode(&got)
	if len(got.Data) != 2 {
		t.Errorf("expected 2 contacts, got %d", len(got.Data))
	}
	if got.Total != 2 {
		t.Errorf("expected total=2, got %d", got.Total)
	}
}

func TestContactsList_RepoError(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{
		ListFn: func(_ context.Context, _ repo.ContactFilter) ([]model.Contact, int, error) {
			return nil, 0, errors.New("db error")
		},
	})
	req := httptest.NewRequest("GET", "/contacts", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

func TestContactsList_PassesSearchFilter(t *testing.T) {
	var capturedFilter repo.ContactFilter
	h := NewContactsHandler(&mockContactsRepo{
		ListFn: func(_ context.Context, f repo.ContactFilter) ([]model.Contact, int, error) {
			capturedFilter = f
			return nil, 0, nil
		},
	})
	req := httptest.NewRequest("GET", "/contacts?search=alice&category=legal", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if capturedFilter.Search != "alice" {
		t.Errorf("search: got %q, want alice", capturedFilter.Search)
	}
	if capturedFilter.Category != "legal" {
		t.Errorf("category: got %q, want legal", capturedFilter.Category)
	}
}

// ---- Create ----

func TestContactsCreate_Success(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{
		CreateFn: func(_ context.Context, c *model.Contact, _ string) (*model.Contact, error) {
			c.ID = "new-c"
			return c, nil
		},
	})
	body := `{"name":"Charlie","email":"charlie@example.com"}`
	req := withCtxUser(httptest.NewRequest("POST", "/contacts", strings.NewReader(body)), "u1", "officer")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestContactsCreate_MissingName(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{})
	body := `{"email":"x@example.com"}`
	req := httptest.NewRequest("POST", "/contacts", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestContactsCreate_BadJSON(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{})
	req := httptest.NewRequest("POST", "/contacts", strings.NewReader("{bad"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

// ---- Get ----

func TestContactsGet_Success(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{
		GetFn: func(_ context.Context, id string) (*model.Contact, error) {
			return testContact(id, "Alice"), nil
		},
	})
	req := chiRequest("GET", "/contacts/c1", "", map[string]string{"id": "c1"})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestContactsGet_NotFound(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{
		GetFn: func(_ context.Context, _ string) (*model.Contact, error) {
			return nil, errors.New("not found")
		},
	})
	req := chiRequest("GET", "/contacts/missing", "", map[string]string{"id": "missing"})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404", rr.Code)
	}
}

// ---- Update ----

func TestContactsUpdate_Success(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{
		UpdateFn: func(_ context.Context, id string, c *model.Contact) (*model.Contact, error) {
			c.ID = id
			return c, nil
		},
	})
	body := `{"name":"Updated Name"}`
	req := chiRequest("PATCH", "/contacts/c1", body, map[string]string{"id": "c1"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestContactsUpdate_MissingName(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{})
	body := `{"email":"x@example.com"}`
	req := chiRequest("PATCH", "/contacts/c1", body, map[string]string{"id": "c1"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

// ---- Delete ----

func TestContactsDelete_Success(t *testing.T) {
	deleted := false
	h := NewContactsHandler(&mockContactsRepo{
		DeleteFn: func(_ context.Context, _ string) error { deleted = true; return nil },
	})
	req := chiRequest("DELETE", "/contacts/c1", "", map[string]string{"id": "c1"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 204 {
		t.Errorf("status: got %d, want 204", rr.Code)
	}
	if !deleted {
		t.Error("expected Delete to be called")
	}
}

func TestContactsDelete_RepoError(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{
		DeleteFn: func(_ context.Context, _ string) error { return errors.New("db error") },
	})
	req := chiRequest("DELETE", "/contacts/c1", "", map[string]string{"id": "c1"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}
