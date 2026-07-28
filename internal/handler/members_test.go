package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

func testMember(id, name string) *model.Member {
	return &model.Member{ID: id, DisplayName: name, Tier: "standard", Status: "active", JoinedAt: time.Now()}
}

// chiRequest wraps a request and injects chi URL params via chi's route context.
func chiRequest(method, url, body string, params map[string]string) *http.Request {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, url, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, url, nil)
	}
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func membersHandler(mr membersRepo) *MembersHandler {
	return NewMembersHandler(mr, &mockActionItemsRepo{
		ListFn: func(_ context.Context, _ repo.ActionItemFilter) ([]model.ActionItem, int, error) {
			return nil, 0, nil
		},
	}, &mockDuesRepo{
		ListInvoicesFn: func(_ context.Context, _ repo.InvoiceFilter) ([]model.DuesInvoice, int, error) {
			return nil, 0, nil
		},
	})
}

// ---- List ----

func TestMembersList_ReturnsMembers(t *testing.T) {
	members := []model.Member{*testMember("11111111-1111-1111-1111-111111111111", "Alice"), *testMember("m2", "Bob")}
	h := membersHandler(&mockMembersRepo{
		ListFn: func(_ context.Context, _ repo.MemberFilter) ([]model.Member, int, error) {
			return members, len(members), nil
		},
	})
	req := httptest.NewRequest("GET", "/members", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	var got model.Page[model.Member]
	json.NewDecoder(rr.Body).Decode(&got)
	if len(got.Data) != 2 {
		t.Errorf("expected 2 members, got %d", len(got.Data))
	}
	if got.Total != 2 {
		t.Errorf("expected total=2, got %d", got.Total)
	}
}

func TestMembersList_RepoError(t *testing.T) {
	h := membersHandler(&mockMembersRepo{
		ListFn: func(_ context.Context, _ repo.MemberFilter) ([]model.Member, int, error) {
			return nil, 0, errors.New("db error")
		},
	})
	req := httptest.NewRequest("GET", "/members", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

func TestMembersList_SearchFilter(t *testing.T) {
	var captured repo.MemberFilter
	h := membersHandler(&mockMembersRepo{
		ListFn: func(_ context.Context, f repo.MemberFilter) ([]model.Member, int, error) {
			captured = f
			return nil, 0, nil
		},
	})
	req := httptest.NewRequest("GET", "/members?search=alice&status=active&tier=premium", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if captured.Search != "alice" {
		t.Errorf("Search: got %q", captured.Search)
	}
	if captured.Status != "active" {
		t.Errorf("Status: got %q", captured.Status)
	}
	if captured.Tier != "premium" {
		t.Errorf("Tier: got %q", captured.Tier)
	}
}

// ---- Create ----

func TestMembersCreate_Success(t *testing.T) {
	h := membersHandler(&mockMembersRepo{
		CreateFn: func(_ context.Context, m *model.Member) (*model.Member, error) {
			m.ID = "new-id"
			return m, nil
		},
	})
	body := `{"display_name":"Charlie","tier":"premium","status":"active"}`
	req := httptest.NewRequest("POST", "/members", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestMembersCreate_MissingDisplayName(t *testing.T) {
	h := membersHandler(&mockMembersRepo{})
	body := `{"tier":"standard"}`
	req := httptest.NewRequest("POST", "/members", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestMembersCreate_DefaultsApplied(t *testing.T) {
	var captured model.Member
	h := membersHandler(&mockMembersRepo{
		CreateFn: func(_ context.Context, m *model.Member) (*model.Member, error) {
			captured = *m
			return m, nil
		},
	})
	body := `{"display_name":"Dave"}`
	req := httptest.NewRequest("POST", "/members", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if captured.Tier != "standard" {
		t.Errorf("default tier: got %q, want 'standard'", captured.Tier)
	}
	if captured.Status != "active" {
		t.Errorf("default status: got %q, want 'active'", captured.Status)
	}
}

func TestMembersCreate_BadJSON(t *testing.T) {
	h := membersHandler(&mockMembersRepo{})
	req := httptest.NewRequest("POST", "/members", strings.NewReader("{bad"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestMembersCreate_CustomJoinedAt(t *testing.T) {
	var captured model.Member
	h := membersHandler(&mockMembersRepo{
		CreateFn: func(_ context.Context, m *model.Member) (*model.Member, error) {
			captured = *m
			return m, nil
		},
	})
	body := `{"display_name":"Eve","joined_at":"2023-01-15"}`
	req := httptest.NewRequest("POST", "/members", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if captured.JoinedAt.Format("2006-01-02") != "2023-01-15" {
		t.Errorf("joined_at: got %s", captured.JoinedAt.Format("2006-01-02"))
	}
}

// ---- Get ----

func TestMembersGet_Success(t *testing.T) {
	h := membersHandler(&mockMembersRepo{
		GetFn: func(_ context.Context, id string) (*model.Member, error) { return testMember(id, "Alice"), nil },
	})
	req := withCtxUser(chiRequest("GET", "/members/m1", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111"}), "u1", "member")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d", rr.Code)
	}
}

func TestMembersGet_NotFound(t *testing.T) {
	h := membersHandler(&mockMembersRepo{
		GetFn: func(_ context.Context, _ string) (*model.Member, error) { return nil, errors.New("not found") },
	})
	req := withCtxUser(chiRequest("GET", "/members/ghost", "", map[string]string{"id": "00000000-0000-0000-0000-000000000000"}), "u1", "officer")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404", rr.Code)
	}
}

// ---- Update ----

func TestMembersUpdate_Success(t *testing.T) {
	h := membersHandler(&mockMembersRepo{
		UpdateFn: func(_ context.Context, id string, fields map[string]any) (*model.Member, error) {
			return testMember(id, "Updated"), nil
		},
	})
	req := chiRequest("PATCH", "/members/m1", `{"display_name":"Updated","status":"inactive"}`, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200; body: %s", rr.Code, rr.Body)
	}
}

func TestMembersUpdate_NoValidFields(t *testing.T) {
	h := membersHandler(&mockMembersRepo{})
	req := chiRequest("PATCH", "/members/m1", `{"unknown_field":"value"}`, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestMembersUpdate_BadUUID(t *testing.T) {
	h := membersHandler(&mockMembersRepo{})
	req := chiRequest("PATCH", "/members/m1", `{"display_name":"X"}`, map[string]string{"id": "m1"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID id", rr.Code)
	}
}

func TestMembersUpdate_InvalidStatus(t *testing.T) {
	h := membersHandler(&mockMembersRepo{})
	req := chiRequest("PATCH", "/members/m1", `{"status":"retired"}`, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for invalid status", rr.Code)
	}
}

func TestMembersUpdate_BadJoinedAt(t *testing.T) {
	h := membersHandler(&mockMembersRepo{})
	req := chiRequest("PATCH", "/members/m1", `{"joined_at":"01/15/2023"}`, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for bad joined_at", rr.Code)
	}
}

func TestMembersUpdate_AllowsJoinedAtAndMetadata(t *testing.T) {
	var capturedFields map[string]any
	h := membersHandler(&mockMembersRepo{
		UpdateFn: func(_ context.Context, _ string, fields map[string]any) (*model.Member, error) {
			capturedFields = fields
			return testMember(testUUID, "X"), nil
		},
	})
	body := `{"joined_at":"2023-01-15","metadata":{"badge":"gold"}}`
	req := chiRequest("PATCH", "/members/m1", body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d, want 200; body: %s", rr.Code, rr.Body)
	}
	if capturedFields["joined_at"] != "2023-01-15" {
		t.Errorf("joined_at: got %v", capturedFields["joined_at"])
	}
	if _, ok := capturedFields["metadata"]; !ok {
		t.Error("metadata should be in the allowlist and passed through")
	}
}

func TestMembersUpdate_NotFound(t *testing.T) {
	h := membersHandler(&mockMembersRepo{
		UpdateFn: func(_ context.Context, _ string, _ map[string]any) (*model.Member, error) {
			return nil, pgx.ErrNoRows
		},
	})
	req := chiRequest("PATCH", "/members/m1", `{"display_name":"X"}`, map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404 for ErrNoRows", rr.Code)
	}
}

func TestMembersUpdate_RepoError(t *testing.T) {
	h := membersHandler(&mockMembersRepo{
		UpdateFn: func(_ context.Context, _ string, _ map[string]any) (*model.Member, error) {
			return nil, errors.New("db error")
		},
	})
	req := chiRequest("PATCH", "/members/m1", `{"display_name":"X"}`, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

func TestMembersUpdate_FiltersUnknownFields(t *testing.T) {
	var capturedFields map[string]any
	h := membersHandler(&mockMembersRepo{
		UpdateFn: func(_ context.Context, _ string, fields map[string]any) (*model.Member, error) {
			capturedFields = fields
			return testMember("11111111-1111-1111-1111-111111111111", ""), nil
		},
	})
	req := chiRequest("PATCH", "/members/m1", `{"display_name":"X","unknown":"y","email":"e@mail.com"}`, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if _, ok := capturedFields["unknown"]; ok {
		t.Error("unknown field should be filtered out")
	}
	if _, ok := capturedFields["display_name"]; !ok {
		t.Error("display_name should be in fields")
	}
}

// ---- Delete ----

func TestMembersDelete_Success(t *testing.T) {
	h := membersHandler(&mockMembersRepo{
		GetFn: func(_ context.Context, _ string) (*model.Member, error) {
			return &model.Member{ID: "m1", DisplayName: "Test"}, nil
		},
		DeleteFn: func(_ context.Context, _ string) error { return nil },
	})
	req := chiRequest("DELETE", "/members/m1?confirm=Test", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 204 {
		t.Errorf("status: got %d, want 204", rr.Code)
	}
}

func TestMembersDelete_ConfirmMismatch(t *testing.T) {
	h := membersHandler(&mockMembersRepo{
		GetFn: func(_ context.Context, _ string) (*model.Member, error) {
			return &model.Member{ID: "m1", DisplayName: "Test"}, nil
		},
	})
	// Missing/wrong ?confirm must be rejected before any delete.
	req := chiRequest("DELETE", "/members/m1", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 without confirmation", rr.Code)
	}
}

func TestMembersDelete_NotFound(t *testing.T) {
	h := membersHandler(&mockMembersRepo{
		GetFn: func(_ context.Context, _ string) (*model.Member, error) { return nil, pgx.ErrNoRows },
	})
	req := chiRequest("DELETE", "/members/m1", "", map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404 for ErrNoRows", rr.Code)
	}
}

func TestMembersDelete_Error(t *testing.T) {
	h := membersHandler(&mockMembersRepo{
		GetFn: func(_ context.Context, _ string) (*model.Member, error) {
			return &model.Member{ID: "m1", DisplayName: "Test"}, nil
		},
		DeleteFn: func(_ context.Context, _ string) error { return errors.New("constraint") },
	})
	req := chiRequest("DELETE", "/members/m1?confirm=Test", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

// ---- GetDues / GetActionItems ----

func TestMembersGetDues(t *testing.T) {
	dois := &mockDuesRepo{
		ListInvoicesFn: func(_ context.Context, f repo.InvoiceFilter) ([]model.DuesInvoice, int, error) {
			if f.MemberID != "11111111-1111-1111-1111-111111111111" {
				t.Errorf("filter member_id: got %q, want m1", f.MemberID)
			}
			return []model.DuesInvoice{{ID: "inv1"}}, 1, nil
		},
	}
	h := NewMembersHandler(&mockMembersRepo{}, &mockActionItemsRepo{}, dois)
	req := withCtxUser(chiRequest("GET", "/members/m1/dues", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111"}), "u1", "member")
	rr := httptest.NewRecorder()
	h.GetDues(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d", rr.Code)
	}
}

func TestMembersGetActionItems(t *testing.T) {
	ais := &mockActionItemsRepo{
		ListFn: func(_ context.Context, f repo.ActionItemFilter) ([]model.ActionItem, int, error) {
			if f.AssigneeID != "11111111-1111-1111-1111-111111111111" {
				t.Errorf("filter assignee_id: got %q, want m1", f.AssigneeID)
			}
			return []model.ActionItem{{ID: "ai1"}}, 1, nil
		},
	}
	h := NewMembersHandler(&mockMembersRepo{}, ais, &mockDuesRepo{})
	req := withCtxUser(chiRequest("GET", "/members/m1/action-items", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111"}), "u1", "member")
	rr := httptest.NewRecorder()
	h.GetActionItems(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d", rr.Code)
	}
}

// ---- restricted read-scoping ----

// restrictedMembersHandler returns a handler whose sub-resource repos succeed,
// so any non-200 must originate from the scoping gate rather than the repo.
func restrictedMembersHandler() *MembersHandler {
	return NewMembersHandler(
		&mockMembersRepo{
			GetFn: func(_ context.Context, id string) (*model.Member, error) { return testMember(id, "Self"), nil },
		},
		&mockActionItemsRepo{
			ListFn: func(_ context.Context, _ repo.ActionItemFilter) ([]model.ActionItem, int, error) { return nil, 0, nil },
		},
		&mockDuesRepo{
			ListInvoicesFn: func(_ context.Context, _ repo.InvoiceFilter) ([]model.DuesInvoice, int, error) { return nil, 0, nil },
		},
	)
}

func TestMembersGet_RestrictedOwnRecordAllowed(t *testing.T) {
	h := restrictedMembersHandler()
	req := withCtxUserMember(chiRequest("GET", "/members/"+testUUID, "", map[string]string{"id": testUUID}), "u1", "restricted", testUUID)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 200 {
		t.Errorf("restricted caller viewing own record: got %d, want 200", rr.Code)
	}
}

func TestMembersGet_RestrictedOtherRecordForbidden(t *testing.T) {
	h := restrictedMembersHandler()
	req := withCtxUserMember(chiRequest("GET", "/members/"+testUUID, "", map[string]string{"id": testUUID}), "u1", "restricted", testUUID2)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 403 {
		t.Errorf("restricted caller viewing another record: got %d, want 403", rr.Code)
	}
}

func TestMembersGet_RestrictedNoLinkedMemberForbidden(t *testing.T) {
	h := restrictedMembersHandler()
	req := withCtxUserMember(chiRequest("GET", "/members/"+testUUID, "", map[string]string{"id": testUUID}), "u1", "restricted", "")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 403 {
		t.Errorf("restricted caller with no linked member: got %d, want 403", rr.Code)
	}
}

func TestMembersGetDues_RestrictedScoping(t *testing.T) {
	h := restrictedMembersHandler()
	// own record → allowed
	req := withCtxUserMember(chiRequest("GET", "/members/"+testUUID+"/dues", "", map[string]string{"id": testUUID}), "u1", "restricted", testUUID)
	rr := httptest.NewRecorder()
	h.GetDues(rr, req)
	if rr.Code != 200 {
		t.Errorf("restricted own dues: got %d, want 200", rr.Code)
	}
	// other record → 403
	req = withCtxUserMember(chiRequest("GET", "/members/"+testUUID+"/dues", "", map[string]string{"id": testUUID}), "u1", "restricted", testUUID2)
	rr = httptest.NewRecorder()
	h.GetDues(rr, req)
	if rr.Code != 403 {
		t.Errorf("restricted other dues: got %d, want 403", rr.Code)
	}
}

func TestMembersGetActionItems_RestrictedScoping(t *testing.T) {
	h := restrictedMembersHandler()
	req := withCtxUserMember(chiRequest("GET", "/members/"+testUUID+"/action-items", "", map[string]string{"id": testUUID}), "u1", "restricted", testUUID)
	rr := httptest.NewRecorder()
	h.GetActionItems(rr, req)
	if rr.Code != 200 {
		t.Errorf("restricted own action items: got %d, want 200", rr.Code)
	}
	req = withCtxUserMember(chiRequest("GET", "/members/"+testUUID+"/action-items", "", map[string]string{"id": testUUID}), "u1", "restricted", testUUID2)
	rr = httptest.NewRecorder()
	h.GetActionItems(rr, req)
	if rr.Code != 403 {
		t.Errorf("restricted other action items: got %d, want 403", rr.Code)
	}
}

func TestMembersGetDues_OfficerSeesAny(t *testing.T) {
	h := restrictedMembersHandler()
	req := withCtxUserMember(chiRequest("GET", "/members/"+testUUID+"/dues", "", map[string]string{"id": testUUID}), "u1", "officer", "")
	rr := httptest.NewRecorder()
	h.GetDues(rr, req)
	if rr.Code != 200 {
		t.Errorf("officer viewing any member's dues: got %d, want 200", rr.Code)
	}
}
