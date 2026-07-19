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
	req := chiRequest("GET", "/contacts/"+testUUID, "", map[string]string{"id": testUUID})
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
	req := chiRequest("GET", "/contacts/"+testUUID2, "", map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404", rr.Code)
	}
}

func TestContactsGet_BadUUID(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{})
	req := chiRequest("GET", "/contacts/c1", "", map[string]string{"id": "c1"})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID id", rr.Code)
	}
}

// ---- Update ----

func TestContactsUpdate_Success(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{
		UpdateFn: func(_ context.Context, id string, fields map[string]any) (*model.Contact, error) {
			return testContact(id, "Updated Name"), nil
		},
	})
	body := `{"name":"Updated Name"}`
	req := chiRequest("PATCH", "/contacts/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestContactsUpdate_PartialEmailOnly(t *testing.T) {
	// A body with only "email" is a valid partial update: omitted fields stay put.
	var capturedFields map[string]any
	h := NewContactsHandler(&mockContactsRepo{
		UpdateFn: func(_ context.Context, id string, fields map[string]any) (*model.Contact, error) {
			capturedFields = fields
			return testContact(id, "Alice"), nil
		},
	})
	body := `{"email":"x@example.com"}`
	req := chiRequest("PATCH", "/contacts/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d, want 200; body: %s", rr.Code, rr.Body)
	}
	if len(capturedFields) != 1 {
		t.Errorf("fields: got %v, want only email", capturedFields)
	}
	if capturedFields["email"] != "x@example.com" {
		t.Errorf("email field: got %v, want x@example.com", capturedFields["email"])
	}
}

func TestContactsUpdate_NoValidFields(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{})
	body := `{"unknown_field":"value"}`
	req := chiRequest("PATCH", "/contacts/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 when no valid fields", rr.Code)
	}
}

func TestContactsUpdate_EmptyName(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{})
	body := `{"name":""}`
	req := chiRequest("PATCH", "/contacts/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for empty name", rr.Code)
	}
}

func TestContactsUpdate_NonStringName(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{})
	body := `{"name":42}`
	req := chiRequest("PATCH", "/contacts/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-string name", rr.Code)
	}
}

func TestContactsUpdate_BadTags(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{})
	body := `{"tags":[1,2,3]}`
	req := chiRequest("PATCH", "/contacts/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-string tags", rr.Code)
	}
}

func TestContactsUpdate_TagsConverted(t *testing.T) {
	var capturedFields map[string]any
	h := NewContactsHandler(&mockContactsRepo{
		UpdateFn: func(_ context.Context, id string, fields map[string]any) (*model.Contact, error) {
			capturedFields = fields
			return testContact(id, "Alice"), nil
		},
	})
	body := `{"tags":["a","b"]}`
	req := chiRequest("PATCH", "/contacts/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	tags, ok := capturedFields["tags"].([]string)
	if !ok || len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Errorf("tags field: got %#v, want []string{a, b}", capturedFields["tags"])
	}
}

func TestContactsUpdate_BadUUID(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{})
	req := chiRequest("PATCH", "/contacts/c1", `{"name":"X"}`, map[string]string{"id": "c1"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID id", rr.Code)
	}
}

func TestContactsUpdate_NotFound(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{
		UpdateFn: func(_ context.Context, _ string, _ map[string]any) (*model.Contact, error) {
			return nil, pgx.ErrNoRows
		},
	})
	req := chiRequest("PATCH", "/contacts/"+testUUID2, `{"name":"X"}`, map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404 for ErrNoRows", rr.Code)
	}
}

func TestContactsUpdate_RepoError(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{
		UpdateFn: func(_ context.Context, _ string, _ map[string]any) (*model.Contact, error) {
			return nil, errors.New("db error")
		},
	})
	req := chiRequest("PATCH", "/contacts/"+testUUID, `{"name":"X"}`, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

// ---- Delete ----

func TestContactsDelete_Success(t *testing.T) {
	deleted := false
	h := NewContactsHandler(&mockContactsRepo{
		GetFn: func(_ context.Context, id string) (*model.Contact, error) {
			return &model.Contact{ID: id, Name: "Acme"}, nil
		},
		DeleteFn: func(_ context.Context, _ string) error { deleted = true; return nil },
	})
	req := chiRequest("DELETE", "/contacts/"+testUUID+"?confirm=Acme", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 204 {
		t.Errorf("status: got %d, want 204; body: %s", rr.Code, rr.Body)
	}
	if !deleted {
		t.Error("expected Delete to be called")
	}
}

func TestContactsDelete_BadUUID(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{})
	req := chiRequest("DELETE", "/contacts/c1", "", map[string]string{"id": "c1"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID id", rr.Code)
	}
}

func TestContactsDelete_NotFound(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{
		GetFn: func(_ context.Context, _ string) (*model.Contact, error) { return nil, pgx.ErrNoRows },
	})
	req := chiRequest("DELETE", "/contacts/"+testUUID2+"?confirm=Acme", "", map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404 for ErrNoRows", rr.Code)
	}
}

func TestContactsDelete_RepoError(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{
		GetFn: func(_ context.Context, id string) (*model.Contact, error) {
			return &model.Contact{ID: id, Name: "Acme"}, nil
		},
		DeleteFn: func(_ context.Context, _ string) error { return errors.New("db error") },
	})
	req := chiRequest("DELETE", "/contacts/"+testUUID+"?confirm=Acme", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

func TestContactsDelete_MissingConfirm(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{
		GetFn: func(_ context.Context, id string) (*model.Contact, error) {
			return &model.Contact{ID: id, Name: "Acme"}, nil
		},
	})
	req := chiRequest("DELETE", "/contacts/"+testUUID, "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 400 || errCode(t, rr) != "confirmation_required" {
		t.Errorf("status/code: got %d/%s, want 400/confirmation_required", rr.Code, rr.Body)
	}
}

func TestContactsDelete_MismatchedConfirm(t *testing.T) {
	h := NewContactsHandler(&mockContactsRepo{
		GetFn: func(_ context.Context, id string) (*model.Contact, error) {
			return &model.Contact{ID: id, Name: "Acme"}, nil
		},
	})
	req := chiRequest("DELETE", "/contacts/"+testUUID+"?confirm=Wrong", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 400 || errCode(t, rr) != "confirmation_mismatch" {
		t.Errorf("status/code: got %d/%s, want 400/confirmation_mismatch", rr.Code, rr.Body)
	}
}
