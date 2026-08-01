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

func testPlan(id, title string) *model.Plan {
	return &model.Plan{ID: id, Title: title, Status: "draft"}
}

func testPlanDecision(id, planID, summary string) *model.PlanDecision {
	return &model.PlanDecision{ID: id, PlanID: planID, Summary: summary}
}

// ---- List ----

func TestPlansList_ReturnsPage(t *testing.T) {
	plans := []model.Plan{*testPlan("p1", "Expansion"), *testPlan("p2", "Sustainability")}
	h := NewPlansHandler(&mockPlansRepo{
		ListFn: func(_ context.Context, _ repo.PlanFilter) ([]model.Plan, int, error) {
			return plans, len(plans), nil
		},
	})
	req := httptest.NewRequest("GET", "/plans", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	var got model.Page[model.Plan]
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Data) != 2 {
		t.Errorf("expected 2 plans, got %d", len(got.Data))
	}
}

func TestPlansList_StatusFilter(t *testing.T) {
	var capturedFilter repo.PlanFilter
	h := NewPlansHandler(&mockPlansRepo{
		ListFn: func(_ context.Context, f repo.PlanFilter) ([]model.Plan, int, error) {
			capturedFilter = f
			return nil, 0, nil
		},
	})
	req := httptest.NewRequest("GET", "/plans?status=active", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if capturedFilter.Status != "active" {
		t.Errorf("status filter: got %q, want active", capturedFilter.Status)
	}
}

func TestPlansList_RepoError(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{
		ListFn: func(_ context.Context, _ repo.PlanFilter) ([]model.Plan, int, error) {
			return nil, 0, errors.New("db error")
		},
	})
	req := httptest.NewRequest("GET", "/plans", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

// ---- Create ----

func TestPlansCreate_Success(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{
		CreateFn: func(_ context.Context, p *model.Plan, _ string) (*model.Plan, error) {
			p.ID = "new-p"
			return p, nil
		},
	})
	body := `{"title":"Five-Year Plan","status":"draft"}`
	req := withCtxUser(httptest.NewRequest("POST", "/plans", strings.NewReader(body)), "u1", "officer")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestPlansCreate_DefaultsStatusToDraft(t *testing.T) {
	var capturedPlan model.Plan
	h := NewPlansHandler(&mockPlansRepo{
		CreateFn: func(_ context.Context, p *model.Plan, _ string) (*model.Plan, error) {
			capturedPlan = *p
			return p, nil
		},
	})
	body := `{"title":"No Status Plan"}`
	req := withCtxUser(httptest.NewRequest("POST", "/plans", strings.NewReader(body)), "u1", "officer")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if capturedPlan.Status != "draft" {
		t.Errorf("expected default status 'draft', got %q", capturedPlan.Status)
	}
}

func TestPlansCreate_MissingTitle(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{})
	body := `{"status":"active"}`
	req := httptest.NewRequest("POST", "/plans", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestPlansCreate_BadTargetDate(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{})
	body := `{"title":"Plan","target_date":"not-a-date"}`
	req := httptest.NewRequest("POST", "/plans", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for bad target_date", rr.Code)
	}
}

func TestPlansCreate_InvalidStatus(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{})
	body := `{"title":"Plan","status":"bogus"}`
	req := httptest.NewRequest("POST", "/plans", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for invalid status", rr.Code)
	}
}

func TestPlansCreate_BadOwnerID(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{})
	body := `{"title":"Plan","owner_id":"not-a-uuid"}`
	req := httptest.NewRequest("POST", "/plans", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID owner_id", rr.Code)
	}
}

// ---- Get ----

func TestPlansGet_Success(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{
		GetFn: func(_ context.Context, id string) (*model.Plan, error) {
			return testPlan(id, "Expansion"), nil
		},
	})
	req := chiRequest("GET", "/plans/"+testUUID, "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestPlansGet_NotFound(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{
		GetFn: func(_ context.Context, _ string) (*model.Plan, error) {
			return nil, errors.New("not found")
		},
	})
	req := chiRequest("GET", "/plans/"+testUUID2, "", map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404", rr.Code)
	}
}

func TestPlansGet_BadUUID(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{})
	req := chiRequest("GET", "/plans/p1", "", map[string]string{"id": "p1"})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID id", rr.Code)
	}
}

// ---- Update ----

func TestPlansUpdate_Success(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{
		UpdateFn: func(_ context.Context, id string, fields map[string]any) (*model.Plan, error) {
			p := testPlan(id, "Updated")
			return p, nil
		},
	})
	body := `{"title":"Updated","status":"active"}`
	req := chiRequest("PATCH", "/plans/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestPlansUpdate_BadUUID(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{})
	req := chiRequest("PATCH", "/plans/p1", `{"title":"X"}`, map[string]string{"id": "p1"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID id", rr.Code)
	}
}

func TestPlansUpdate_InvalidStatus(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{})
	body := `{"status":"bogus"}`
	req := chiRequest("PATCH", "/plans/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for invalid status", rr.Code)
	}
}

func TestPlansUpdate_BadOwnerID(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{})
	body := `{"owner_id":"not-a-uuid"}`
	req := chiRequest("PATCH", "/plans/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID owner_id", rr.Code)
	}
}

func TestPlansUpdate_BadTargetDate(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{})
	body := `{"target_date":"March 5th"}`
	req := chiRequest("PATCH", "/plans/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for bad target_date", rr.Code)
	}
}

func TestPlansUpdate_NoValidFields(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{})
	body := `{"unknown":"x"}`
	req := chiRequest("PATCH", "/plans/"+testUUID, body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 when no valid fields", rr.Code)
	}
}

func TestPlansUpdate_NotFound(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{
		UpdateFn: func(_ context.Context, _ string, _ map[string]any) (*model.Plan, error) {
			return nil, pgx.ErrNoRows
		},
	})
	req := chiRequest("PATCH", "/plans/"+testUUID2, `{"title":"X"}`, map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404 for ErrNoRows", rr.Code)
	}
}

func TestPlansUpdate_RepoError(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{
		UpdateFn: func(_ context.Context, _ string, _ map[string]any) (*model.Plan, error) {
			return nil, errors.New("db error")
		},
	})
	req := chiRequest("PATCH", "/plans/"+testUUID, `{"title":"X"}`, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

// ---- Delete ----

func TestPlansDelete_Success(t *testing.T) {
	deleted := false
	h := NewPlansHandler(&mockPlansRepo{
		GetFn: func(_ context.Context, id string) (*model.Plan, error) {
			return &model.Plan{ID: id, Title: "Roadmap"}, nil
		},
		DeleteFn: func(_ context.Context, _ string) error { deleted = true; return nil },
	})
	req := chiRequest("DELETE", "/plans/"+testUUID+"?confirm=Roadmap", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 204 {
		t.Errorf("status: got %d, want 204; body: %s", rr.Code, rr.Body)
	}
	if !deleted {
		t.Error("expected Delete to be called")
	}
}

func TestPlansDelete_NotifiesOwner(t *testing.T) {
	fn := &fakeNotifier{}
	h := NewPlansHandler(&mockPlansRepo{
		GetFn: func(_ context.Context, id string) (*model.Plan, error) {
			return &model.Plan{ID: id, Title: "Roadmap"}, nil
		},
		OwnerEmailFn: func(_ context.Context, _ string) (string, error) { return "owner@example.com", nil },
		DeleteFn:     func(_ context.Context, _ string) error { return nil },
	})
	h.SetNotifier(fn)
	req := withCtxUser(chiRequest("DELETE", "/plans/"+testUUID+"?confirm=Roadmap", "", map[string]string{"id": testUUID}), "actor-1", "superadmin")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 204 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if !fn.called || fn.entityType != "plan" || len(fn.affected) != 1 || fn.affected[0] != "owner@example.com" {
		t.Errorf("notifier: called=%v type=%q affected=%v", fn.called, fn.entityType, fn.affected)
	}
}

func TestPlansDelete_BadUUID(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{})
	req := chiRequest("DELETE", "/plans/p1", "", map[string]string{"id": "p1"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID id", rr.Code)
	}
}

func TestPlansDelete_NotFound(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{
		GetFn: func(_ context.Context, _ string) (*model.Plan, error) { return nil, pgx.ErrNoRows },
	})
	req := chiRequest("DELETE", "/plans/"+testUUID2+"?confirm=Roadmap", "", map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404 for ErrNoRows", rr.Code)
	}
}

func TestPlansDelete_MissingConfirm(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{
		GetFn: func(_ context.Context, id string) (*model.Plan, error) {
			return &model.Plan{ID: id, Title: "Roadmap"}, nil
		},
	})
	req := chiRequest("DELETE", "/plans/"+testUUID, "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 400 || errCode(t, rr) != "confirmation_required" {
		t.Errorf("status/code: got %d/%s, want 400/confirmation_required", rr.Code, rr.Body)
	}
}

func TestPlansDelete_MismatchedConfirm(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{
		GetFn: func(_ context.Context, id string) (*model.Plan, error) {
			return &model.Plan{ID: id, Title: "Roadmap"}, nil
		},
	})
	req := chiRequest("DELETE", "/plans/"+testUUID+"?confirm=Nope", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 400 || errCode(t, rr) != "confirmation_mismatch" {
		t.Errorf("status/code: got %d/%s, want 400/confirmation_mismatch", rr.Code, rr.Body)
	}
}

// ---- CreateDecision ----

func TestPlansCreateDecision_Success(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{
		CreateDecisionFn: func(_ context.Context, d *model.PlanDecision, _ string) (*model.PlanDecision, error) {
			d.ID = "dec-1"
			return d, nil
		},
	})
	body := `{"summary":"Approved budget increase","rationale":"Growth phase"}`
	req := withCtxUser(
		chiRequest("POST", "/plans/"+testUUID+"/decisions", body, map[string]string{"id": testUUID}),
		"u1", "officer",
	)
	rr := httptest.NewRecorder()
	h.CreateDecision(rr, req)
	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestPlansCreateDecision_MissingSummary(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{})
	body := `{"rationale":"Some rationale"}`
	req := chiRequest("POST", "/plans/"+testUUID+"/decisions", body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.CreateDecision(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestPlansCreateDecision_BadPlanUUID(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{})
	body := `{"summary":"S"}`
	req := chiRequest("POST", "/plans/p1/decisions", body, map[string]string{"id": "p1"})
	rr := httptest.NewRecorder()
	h.CreateDecision(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID plan id", rr.Code)
	}
}

// ---- UpdateDecision ----

func TestPlansUpdateDecision_Success(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{
		UpdateDecisionFn: func(_ context.Context, id string, summary, rationale *string) (*model.PlanDecision, error) {
			return testPlanDecision(id, testUUID, *summary), nil
		},
	})
	body := `{"summary":"Revised decision"}`
	req := chiRequest("PATCH", "/plans/"+testUUID+"/decisions/"+testUUID2, body, map[string]string{"id": testUUID, "did": testUUID2})
	rr := httptest.NewRecorder()
	h.UpdateDecision(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestPlansUpdateDecision_BadUUID(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{})
	body := `{"summary":"X"}`
	req := chiRequest("PATCH", "/plans/"+testUUID+"/decisions/dec-1", body, map[string]string{"id": testUUID, "did": "dec-1"})
	rr := httptest.NewRecorder()
	h.UpdateDecision(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID decision id", rr.Code)
	}
}

func TestPlansUpdateDecision_NotFound(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{
		UpdateDecisionFn: func(_ context.Context, _ string, _, _ *string) (*model.PlanDecision, error) {
			return nil, pgx.ErrNoRows
		},
	})
	body := `{"summary":"X"}`
	req := chiRequest("PATCH", "/plans/"+testUUID+"/decisions/"+testUUID2, body, map[string]string{"id": testUUID, "did": testUUID2})
	rr := httptest.NewRecorder()
	h.UpdateDecision(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404 for ErrNoRows", rr.Code)
	}
}

// ---- DeleteDecision ----

func TestPlansDeleteDecision_Success(t *testing.T) {
	deleted := false
	h := NewPlansHandler(&mockPlansRepo{
		DeleteDecisionFn: func(_ context.Context, _ string) error { deleted = true; return nil },
	})
	req := chiRequest("DELETE", "/plans/"+testUUID+"/decisions/"+testUUID2, "", map[string]string{"id": testUUID, "did": testUUID2})
	rr := httptest.NewRecorder()
	h.DeleteDecision(rr, req)
	if rr.Code != 204 {
		t.Errorf("status: got %d, want 204", rr.Code)
	}
	if !deleted {
		t.Error("expected DeleteDecision to be called")
	}
}

func TestPlansDeleteDecision_BadUUID(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{})
	req := chiRequest("DELETE", "/plans/"+testUUID+"/decisions/dec-1", "", map[string]string{"id": testUUID, "did": "dec-1"})
	rr := httptest.NewRecorder()
	h.DeleteDecision(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID decision id", rr.Code)
	}
}
