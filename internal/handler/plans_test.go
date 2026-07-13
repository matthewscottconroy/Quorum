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
	json.NewDecoder(rr.Body).Decode(&got)
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

// ---- Get ----

func TestPlansGet_Success(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{
		GetFn: func(_ context.Context, id string) (*model.Plan, error) {
			return testPlan(id, "Expansion"), nil
		},
	})
	req := chiRequest("GET", "/plans/p1", "", map[string]string{"id": "p1"})
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
	req := chiRequest("GET", "/plans/missing", "", map[string]string{"id": "missing"})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404", rr.Code)
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
	req := chiRequest("PATCH", "/plans/p1", body, map[string]string{"id": "p1"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

// ---- Delete ----

func TestPlansDelete_Success(t *testing.T) {
	deleted := false
	h := NewPlansHandler(&mockPlansRepo{
		DeleteFn: func(_ context.Context, _ string) error { deleted = true; return nil },
	})
	req := chiRequest("DELETE", "/plans/p1", "", map[string]string{"id": "p1"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != 204 {
		t.Errorf("status: got %d, want 204", rr.Code)
	}
	if !deleted {
		t.Error("expected Delete to be called")
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
		chiRequest("POST", "/plans/p1/decisions", body, map[string]string{"id": "p1"}),
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
	req := chiRequest("POST", "/plans/p1/decisions", body, map[string]string{"id": "p1"})
	rr := httptest.NewRecorder()
	h.CreateDecision(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

// ---- UpdateDecision ----

func TestPlansUpdateDecision_Success(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{
		UpdateDecisionFn: func(_ context.Context, id string, summary, rationale *string) (*model.PlanDecision, error) {
			return testPlanDecision(id, "p1", *summary), nil
		},
	})
	newSummary := "Revised decision"
	body := `{"summary":"Revised decision"}`
	req := chiRequest("PATCH", "/plans/p1/decisions/dec-1", body, map[string]string{"id": "p1", "did": "dec-1"})
	rr := httptest.NewRecorder()
	h.UpdateDecision(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	_ = newSummary
}

// ---- DeleteDecision ----

func TestPlansDeleteDecision_Success(t *testing.T) {
	deleted := false
	h := NewPlansHandler(&mockPlansRepo{
		DeleteDecisionFn: func(_ context.Context, _ string) error { deleted = true; return nil },
	})
	req := chiRequest("DELETE", "/plans/p1/decisions/dec-1", "", map[string]string{"id": "p1", "did": "dec-1"})
	rr := httptest.NewRecorder()
	h.DeleteDecision(rr, req)
	if rr.Code != 204 {
		t.Errorf("status: got %d, want 204", rr.Code)
	}
	if !deleted {
		t.Error("expected DeleteDecision to be called")
	}
}
