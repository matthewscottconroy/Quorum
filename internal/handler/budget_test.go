package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quorum/internal/model"
)

const testBudgetID = "88888888-8888-8888-8888-888888888888"

func budgetHandler(r budgetRepo) *BudgetHandler { return NewBudgetHandler(r) }

func TestBudgetGet_ComputesNet(t *testing.T) {
	// income 250000 (2500.00), expense 100000 → net 150000 surplus.
	repo := &mockBudgetRepo{
		GetScenarioFn: func(_ context.Context, _ string) (*model.BudgetScenario, error) {
			return &model.BudgetScenario{
				ID: testBudgetID, Name: "2027 Base", Currency: "USD",
				Lines: []model.BudgetLine{
					{Kind: "income", Label: "Dues", Quantity: 50, UnitAmountMinor: 5000, AmountMinor: 250000},
					{Kind: "expense", Label: "Venue", Quantity: 1, UnitAmountMinor: 100000, AmountMinor: 100000},
				},
				Totals: model.BudgetTotals{IncomeMinor: 250000, ExpenseMinor: 100000, NetMinor: 150000, Currency: "USD"},
			}, nil
		},
	}
	req := reqWithParam("GET", "/budgets/"+testBudgetID, "", map[string]string{"id": testBudgetID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	budgetHandler(repo).Get(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var s model.BudgetScenario
	json.NewDecoder(rr.Body).Decode(&s)
	if s.Totals.NetMinor != 150000 {
		t.Errorf("expected net 150000, got %d", s.Totals.NetMinor)
	}
}

func TestBudgetCreate_RequiresName(t *testing.T) {
	req := withCtxUser(httptest.NewRequest("POST", "/budgets", strings.NewReader(`{"name":""}`)), "u", "officer")
	rr := httptest.NewRecorder()
	budgetHandler(&mockBudgetRepo{}).Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestBudgetClone_DefaultsName(t *testing.T) {
	var clonedName string
	repo := &mockBudgetRepo{
		CloneScenarioFn: func(_ context.Context, _, newName, _ string) (*model.BudgetScenario, error) {
			clonedName = newName
			return &model.BudgetScenario{ID: "new", Name: newName}, nil
		},
	}
	req := reqWithParam("POST", "/budgets/"+testBudgetID+"/clone", `{}`, map[string]string{"id": testBudgetID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	budgetHandler(repo).Clone(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if clonedName != "Copy" {
		t.Errorf("expected default clone name 'Copy', got %q", clonedName)
	}
}

func TestBudgetAddLine_Validation(t *testing.T) {
	h := budgetHandler(&mockBudgetRepo{})
	for _, b := range []string{
		`{"kind":"revenue","label":"x"}`,              // bad kind
		`{"kind":"income","label":""}`,                // no label
		`{"kind":"income","label":"x","quantity":-1}`, // negative qty
	} {
		req := reqWithParam("POST", "/budgets/"+testBudgetID+"/lines", b, map[string]string{"id": testBudgetID})
		req = withCtxUser(req, "u", "officer")
		rr := httptest.NewRecorder()
		h.AddLine(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body %s: expected 400, got %d", b, rr.Code)
		}
	}
}

func TestBudgetSeedDues_ReturnsCount(t *testing.T) {
	repo := &mockBudgetRepo{
		SeedDuesIncomeFn: func(_ context.Context, _ string) (int, error) { return 2, nil },
	}
	req := reqWithParam("POST", "/budgets/"+testBudgetID+"/seed-dues", "", map[string]string{"id": testBudgetID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	budgetHandler(repo).SeedDues(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["lines_added"].(float64) != 2 {
		t.Errorf("expected lines_added=2, got %v", resp["lines_added"])
	}
}

func TestBudgetCompare_RequiresIDs(t *testing.T) {
	req := withCtxUser(httptest.NewRequest("GET", "/budgets/compare", nil), "u", "officer")
	rr := httptest.NewRecorder()
	budgetHandler(&mockBudgetRepo{}).Compare(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without ids, got %d", rr.Code)
	}
}
