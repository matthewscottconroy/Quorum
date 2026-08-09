package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

func strp(s string) *string { return &s }

func scenarioUSD(id string) *model.BudgetScenario {
	return &model.BudgetScenario{
		ID: id, Name: "FY26", Currency: "USD", Status: "active",
		Totals: model.BudgetTotals{IncomeMinor: 100000, ExpenseMinor: 80000, Currency: "USD"},
	}
}

// ---- vs-actual: currency discipline, basis, proration, categories ----

func TestBudgetVsActual_ExcludesForeignCurrenciesAndNamesThem(t *testing.T) {
	h := budgetHandler(&mockBudgetRepo{
		GetScenarioFn: func(_ context.Context, id string) (*model.BudgetScenario, error) {
			return scenarioUSD(id), nil
		},
	})
	h.SetActuals(&mockActuals{
		fn: func(_ context.Context, _, _ string, _ []string) ([]model.GLBalance, error) {
			return []model.GLBalance{
				{Type: "income", Balance: -90000, Currency: "USD", Code: "4000"},
				{Type: "income", Balance: -55500, Currency: "EUR", Code: "4000"}, // must NOT sum
				{Type: "expense", Balance: 20000, Currency: "JPY", Code: "5000"}, // must NOT sum
			}, nil
		},
	})
	req := chiRequest("GET", "/budgets/"+testUUID+"/vs-actual?from=2026-01-01&to=2026-12-31", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.VsActual(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["actual_income"].(float64) != 90000 {
		t.Errorf("actual_income: got %v, want 90000 (USD only)", got["actual_income"])
	}
	if got["actual_expense"].(float64) != 0 {
		t.Errorf("actual_expense: got %v, want 0 (JPY excluded)", got["actual_expense"])
	}
	excluded, _ := got["excluded_currencies"].([]any)
	if len(excluded) != 2 || excluded[0] != "EUR" || excluded[1] != "JPY" {
		t.Errorf("excluded_currencies: got %v", got["excluded_currencies"])
	}
}

func TestBudgetVsActual_CashBasisUsesCashStatement(t *testing.T) {
	var sawCash bool
	h := budgetHandler(&mockBudgetRepo{
		GetScenarioFn: func(_ context.Context, id string) (*model.BudgetScenario, error) {
			return scenarioUSD(id), nil
		},
	})
	h.SetActuals(&mockActuals{
		fn: func(_ context.Context, _, _ string, types []string) ([]model.GLBalance, error) {
			// The cash shim passes types == nil.
			if types == nil {
				sawCash = true
			}
			return nil, nil
		},
	})
	req := chiRequest("GET", "/budgets/"+testUUID+"/vs-actual?from=2026-01-01&to=2026-12-31&basis=cash", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.VsActual(rr, req)
	if rr.Code != 200 || !sawCash {
		t.Fatalf("status %d, sawCash=%v", rr.Code, sawCash)
	}

	req = chiRequest("GET", "/budgets/"+testUUID+"/vs-actual?from=2026-01-01&to=2026-12-31&basis=voodoo", "", map[string]string{"id": testUUID})
	rr = httptest.NewRecorder()
	h.VsActual(rr, req)
	if rr.Code != 400 {
		t.Errorf("bad basis: got %d, want 400", rr.Code)
	}
}

func TestBudgetVsActual_ProratesAndDefaultsDatesFromScenario(t *testing.T) {
	start := time.Now().AddDate(-1, 0, 0) // fully elapsed period
	end := time.Now().AddDate(0, 0, -1)
	h := budgetHandler(&mockBudgetRepo{
		GetScenarioFn: func(_ context.Context, id string) (*model.BudgetScenario, error) {
			sc := scenarioUSD(id)
			sc.StartsOn = &start
			sc.EndsOn = &end
			return sc, nil
		},
	})
	h.SetActuals(&mockActuals{
		fn: func(_ context.Context, from, to string, _ []string) ([]model.GLBalance, error) {
			if from != start.Format("2006-01-02") || to != end.Format("2006-01-02") {
				t.Errorf("dates should default from scenario: got %s..%s", from, to)
			}
			return nil, nil
		},
	})
	// No from/to in the query: scenario dates take over.
	req := chiRequest("GET", "/budgets/"+testUUID+"/vs-actual", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.VsActual(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["period_elapsed_pct"].(float64) != 100 {
		t.Errorf("period_elapsed_pct: got %v, want 100", got["period_elapsed_pct"])
	}
	if got["prorated_budget_income"].(float64) != 100000 {
		t.Errorf("prorated income at 100%%: got %v", got["prorated_budget_income"])
	}
}

func TestBudgetVsActual_PerAccountCategories(t *testing.T) {
	h := budgetHandler(&mockBudgetRepo{
		GetScenarioFn: func(_ context.Context, id string) (*model.BudgetScenario, error) {
			sc := scenarioUSD(id)
			sc.Lines = []model.BudgetLine{
				{Kind: "expense", Label: "Venue", AmountMinor: 40000, AccountID: strp(testUUID), AccountCode: strp("5100"), AccountName: strp("Events")},
				{Kind: "expense", Label: "Misc", AmountMinor: 40000}, // unlinked
			}
			return sc, nil
		},
	})
	h.SetActuals(&mockActuals{
		fn: func(_ context.Context, _, _ string, _ []string) ([]model.GLBalance, error) {
			return []model.GLBalance{
				{Type: "expense", Balance: 25000, Currency: "USD", Code: "5100", Name: "Events"},
			}, nil
		},
	})
	req := chiRequest("GET", "/budgets/"+testUUID+"/vs-actual?from=2026-01-01&to=2026-12-31", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.VsActual(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var got struct {
		Categories []struct {
			Kind        string `json:"kind"`
			AccountCode string `json:"account_code"`
			Label       string `json:"label"`
			Budget      int64  `json:"budget_minor"`
			Actual      int64  `json:"actual_minor"`
			Variance    int64  `json:"variance_minor"`
		} `json:"categories"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Categories) != 2 {
		t.Fatalf("categories: got %d, want 2 (linked + unlinked)", len(got.Categories))
	}
	events := got.Categories[0]
	if events.AccountCode != "5100" || events.Budget != 40000 || events.Actual != 25000 || events.Variance != 15000 {
		t.Errorf("events row wrong: %+v", events)
	}
	if got.Categories[1].Label != "(unlinked)" || got.Categories[1].Budget != 40000 {
		t.Errorf("unlinked bucket wrong: %+v", got.Categories[1])
	}
}

// ---- guards ----

func TestBudgetUpdate_CurrencyFrozenOnceLinesExist(t *testing.T) {
	h := budgetHandler(&mockBudgetRepo{
		ScenarioGuardFn: func(_ context.Context, _ string) (string, string, bool, error) {
			return "draft", "USD", true, nil // has lines
		},
	})
	req := reqWithParam("PATCH", "/budgets/"+testBudgetID, `{"currency":"EUR"}`, map[string]string{"id": testBudgetID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 409 {
		t.Errorf("currency change with lines: got %d, want 409", rr.Code)
	}
}

func TestBudgetUpdate_ArchivedIsReadOnlyExceptStatus(t *testing.T) {
	repo := &mockBudgetRepo{
		ScenarioGuardFn: func(_ context.Context, _ string) (string, string, bool, error) {
			return "archived", "USD", true, nil
		},
		UpdateScenarioFn: func(_ context.Context, id string, _, _, _, _, _, _, _ *string) (*model.BudgetScenario, error) {
			return scenarioUSD(id), nil
		},
	}
	req := reqWithParam("PATCH", "/budgets/"+testBudgetID, `{"name":"sneaky edit"}`, map[string]string{"id": testBudgetID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	budgetHandler(repo).Update(rr, req)
	if rr.Code != 409 {
		t.Errorf("archived rename: got %d, want 409", rr.Code)
	}
	// Status-only change (un-archiving) stays allowed.
	req = reqWithParam("PATCH", "/budgets/"+testBudgetID, `{"status":"draft"}`, map[string]string{"id": testBudgetID})
	req = withCtxUser(req, "u", "officer")
	rr = httptest.NewRecorder()
	budgetHandler(repo).Update(rr, req)
	if rr.Code != 200 {
		t.Errorf("un-archive: got %d, want 200", rr.Code)
	}
}

func TestBudgetAddLine_ArchivedRefused(t *testing.T) {
	h := budgetHandler(&mockBudgetRepo{
		ScenarioGuardFn: func(_ context.Context, _ string) (string, string, bool, error) {
			return "archived", "USD", true, nil
		},
	})
	req := reqWithParam("POST", "/budgets/"+testBudgetID+"/lines", `{"kind":"expense","label":"x","unit_amount_minor":100}`, map[string]string{"id": testBudgetID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	h.AddLine(rr, req)
	if rr.Code != 409 {
		t.Errorf("add line to archived: got %d, want 409", rr.Code)
	}
}

func TestBudgetAddLine_AccountKindMustMatch(t *testing.T) {
	h := budgetHandler(&mockBudgetRepo{
		AccountKindFn: func(_ context.Context, _ string) (string, error) {
			return "income", nil // account is income...
		},
	})
	// ...but the line is an expense.
	body := `{"kind":"expense","label":"x","unit_amount_minor":100,"account_id":"` + testUUID + `"}`
	req := reqWithParam("POST", "/budgets/"+testBudgetID+"/lines", body, map[string]string{"id": testBudgetID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	h.AddLine(rr, req)
	if rr.Code != 400 {
		t.Errorf("kind mismatch: got %d, want 400", rr.Code)
	}
}

func TestBudgetDelete_RequiresMatchingConfirm(t *testing.T) {
	repo := &mockBudgetRepo{
		GetScenarioFn: func(_ context.Context, id string) (*model.BudgetScenario, error) {
			return scenarioUSD(id), nil
		},
		DeleteScenarioFn: func(_ context.Context, _ string) error { return nil },
	}
	req := reqWithParam("DELETE", "/budgets/"+testBudgetID, "", map[string]string{"id": testBudgetID})
	req = withCtxUser(req, "u", "admin")
	rr := httptest.NewRecorder()
	budgetHandler(repo).Delete(rr, req)
	if rr.Code != 400 {
		t.Errorf("no confirm: got %d, want 400", rr.Code)
	}
	req = reqWithParam("DELETE", "/budgets/"+testBudgetID+"?confirm=FY26", "", map[string]string{"id": testBudgetID})
	req = withCtxUser(req, "u", "admin")
	rr = httptest.NewRecorder()
	budgetHandler(repo).Delete(rr, req)
	if rr.Code != 204 {
		t.Errorf("matching confirm: got %d, want 204", rr.Code)
	}
}

// ---- remaining (spend-time hint) ----

func TestBudgetRemaining_MathAndCurrencyScope(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	h := budgetHandler(&mockBudgetRepo{
		AccountBudgetFn: func(_ context.Context, accountID string) (*model.BudgetScenario, int64, error) {
			sc := scenarioUSD("s1")
			sc.StartsOn = &start
			sc.EndsOn = &end
			sc.Lines = []model.BudgetLine{
				{Kind: "expense", AccountID: &accountID, AccountCode: strp("5100"), AmountMinor: 50000},
			}
			return sc, 50000, nil
		},
	})
	h.SetActuals(&mockActuals{
		fn: func(_ context.Context, from, to string, _ []string) ([]model.GLBalance, error) {
			if from != "2026-01-01" || to != "2026-12-31" {
				t.Errorf("period should come from the scenario: %s..%s", from, to)
			}
			return []model.GLBalance{
				{Type: "expense", Balance: 12000, Currency: "USD", Code: "5100"},
				{Type: "expense", Balance: 99999, Currency: "EUR", Code: "5100"}, // excluded
			}, nil
		},
	})
	req := withCtxUser(httptest.NewRequest("GET", "/budgets/remaining?account_id="+testUUID, nil), "u", "officer")
	rr := httptest.NewRecorder()
	h.Remaining(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["remaining_minor"].(float64) != 38000 {
		t.Errorf("remaining: got %v, want 38000 (USD actuals only)", got["remaining_minor"])
	}
}

func TestBudgetRemaining_NoActiveBudgetIs404(t *testing.T) {
	h := budgetHandler(&mockBudgetRepo{})
	req := withCtxUser(httptest.NewRequest("GET", "/budgets/remaining?account_id="+testUUID, nil), "u", "officer")
	rr := httptest.NewRecorder()
	h.Remaining(rr, req)
	if rr.Code != 404 {
		t.Errorf("got %d, want 404", rr.Code)
	}
}

// ---- seed transparency ----

func TestBudgetSeedDues_ReportsSkips(t *testing.T) {
	h := budgetHandler(&mockBudgetRepo{
		SeedDuesIncomeFn: func(_ context.Context, _ string) (int, []model.BudgetSeedSkip, error) {
			return 1, []model.BudgetSeedSkip{{Tier: "honorary", Members: 4, Reason: "no_active_schedule"}}, nil
		},
	})
	req := reqWithParam("POST", "/budgets/"+testBudgetID+"/seed-dues", "", map[string]string{"id": testBudgetID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	h.SeedDues(rr, req)
	var got struct {
		LinesAdded int                    `json:"lines_added"`
		Skipped    []model.BudgetSeedSkip `json:"skipped"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.LinesAdded != 1 || len(got.Skipped) != 1 || got.Skipped[0].Tier != "honorary" {
		t.Errorf("seed response: %+v", got)
	}
}

// ---- plans: cost estimate + linked work ----

func TestPlansCreate_RejectsBadCostCurrency(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{})
	req := withCtxUser(httptest.NewRequest("POST", "/plans",
		strings.NewReader(`{"title":"Roof fund","estimated_cost_minor":500000,"cost_currency":"DOLLARS"}`)), "u", "officer")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("bad currency: got %d, want 400", rr.Code)
	}
}

func TestPlansGet_AttachesLinkedWork(t *testing.T) {
	h := NewPlansHandler(&mockPlansRepo{
		GetFn: func(_ context.Context, id string) (*model.Plan, error) {
			return &model.Plan{ID: id, Title: "Roof"}, nil
		},
	})
	h.SetWork(&mockActionItemsRepo{
		ListFn: func(_ context.Context, f repo.ActionItemFilter) ([]model.ActionItem, int, error) {
			if f.PlanID == "" {
				t.Error("work lookup must filter by plan id")
			}
			return []model.ActionItem{{ID: "a1", Title: "Get quotes", Status: "done"}}, 1, nil
		},
	})
	req := chiRequest("GET", "/plans/"+testUUID, "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var got model.Plan
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.ActionItems) != 1 || got.ActionItems[0].Title != "Get quotes" {
		t.Errorf("linked work missing: %+v", got.ActionItems)
	}
}
