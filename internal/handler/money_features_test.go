package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"quorum/internal/model"
)

// ---- refunds ----

func TestRecordRefund_Success(t *testing.T) {
	var captured *model.Transaction
	h := NewDuesHandler(&mockDuesRepo{
		GetInvoiceFn: func(_ context.Context, id string) (*model.DuesInvoice, error) {
			return testInvoice(id, "m1"), nil // USD invoice
		},
		CreateTransactionFn: func(_ context.Context, tx *model.Transaction) (*model.Transaction, error) {
			captured = tx
			tx.ID = "tx1"
			return tx, nil
		},
		RecomputeInvoiceStatusFn: func(_ context.Context, _ string) error { return nil },
	})
	body := `{"amount_minor":5000,"currency":"USD","provider":"manual","note":"overpaid"}`
	req := chiRequest("POST", "/dues/"+testUUID+"/refund", body, map[string]string{"id": testUUID})
	req = withCtxUser(req, "u1", "officer")
	rr := httptest.NewRecorder()
	h.RecordRefund(rr, req)
	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if captured == nil || captured.AmountMinor != -5000 {
		t.Errorf("expected reversing entry of -5000, got %+v", captured)
	}
}

func TestRecordRefund_CurrencyMismatch(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{
		GetInvoiceFn: func(_ context.Context, id string) (*model.DuesInvoice, error) {
			return testInvoice(id, "m1"), nil // USD invoice
		},
	})
	body := `{"amount_minor":5000,"currency":"EUR","provider":"manual"}`
	req := chiRequest("POST", "/dues/"+testUUID+"/refund", body, map[string]string{"id": testUUID})
	req = withCtxUser(req, "u1", "officer")
	rr := httptest.NewRecorder()
	h.RecordRefund(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for currency mismatch", rr.Code)
	}
}

func TestRecordRefund_MissingProvider(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{})
	body := `{"amount_minor":5000,"currency":"USD"}`
	req := chiRequest("POST", "/dues/"+testUUID+"/refund", body, map[string]string{"id": testUUID})
	req = withCtxUser(req, "u1", "officer")
	rr := httptest.NewRecorder()
	h.RecordRefund(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for missing provider", rr.Code)
	}
}

func TestRecordRefund_NonPositiveAmount(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{})
	body := `{"amount_minor":0,"provider":"manual"}`
	req := chiRequest("POST", "/dues/"+testUUID+"/refund", body, map[string]string{"id": testUUID})
	req = withCtxUser(req, "u1", "officer")
	rr := httptest.NewRecorder()
	h.RecordRefund(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-positive amount", rr.Code)
	}
}

// ---- installments ----

func TestSetInstallments_Success(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{})
	body := `{"installments":[{"amount_minor":5000,"due_date":"2026-01-15"},{"amount_minor":5000,"due_date":"2026-02-15"}]}`
	req := chiRequest("PUT", "/dues/"+testUUID+"/installments", body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.SetInstallments(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestSetInstallments_BadDate(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{})
	body := `{"installments":[{"amount_minor":5000,"due_date":"nope"}]}`
	req := chiRequest("PUT", "/dues/"+testUUID+"/installments", body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.SetInstallments(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for bad date", rr.Code)
	}
}

func TestSetInstallments_NonPositiveAmount(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{})
	body := `{"installments":[{"amount_minor":0,"due_date":"2026-01-15"}]}`
	req := chiRequest("PUT", "/dues/"+testUUID+"/installments", body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.SetInstallments(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-positive amount", rr.Code)
	}
}

func TestSetInstallments_ClearsPlan(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{})
	req := chiRequest("PUT", "/dues/"+testUUID+"/installments", `{"installments":[]}`, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.SetInstallments(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200 clearing the plan", rr.Code)
	}
}

// ---- budget vs actual ----

// mockActuals implements budgetActuals.
type mockActuals struct {
	fn func(ctx context.Context, from, to string, types []string) ([]model.GLBalance, error)
}

func (m *mockActuals) Statement(ctx context.Context, from, to string, types []string) ([]model.GLBalance, error) {
	return m.fn(ctx, from, to, types)
}

func TestBudgetVsActual_Success(t *testing.T) {
	h := budgetHandler(&mockBudgetRepo{
		GetScenarioFn: func(_ context.Context, id string) (*model.BudgetScenario, error) {
			return &model.BudgetScenario{
				ID: id, Name: "FY26", Currency: "USD",
				Totals: model.BudgetTotals{IncomeMinor: 100000, ExpenseMinor: 80000, Currency: "USD"},
			}, nil
		},
	})
	h.SetActuals(&mockActuals{
		fn: func(_ context.Context, _, _ string, _ []string) ([]model.GLBalance, error) {
			// Income accounts carry credit balances (negative); expenses positive.
			return []model.GLBalance{
				{Type: "income", Balance: -90000},
				{Type: "expense", Balance: 85000},
			}, nil
		},
	})
	req := chiRequest("GET", "/budgets/"+testUUID+"/vs-actual?from=2026-01-01&to=2026-12-31", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.VsActual(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["actual_income"].(float64) != 90000 {
		t.Errorf("actual_income: got %v, want 90000", got["actual_income"])
	}
	if got["actual_expense"].(float64) != 85000 {
		t.Errorf("actual_expense: got %v, want 85000", got["actual_expense"])
	}
	if got["income_variance"].(float64) != -10000 {
		t.Errorf("income_variance: got %v, want -10000", got["income_variance"])
	}
	if got["expense_variance"].(float64) != 5000 {
		t.Errorf("expense_variance: got %v, want 5000", got["expense_variance"])
	}
}

func TestBudgetVsActual_BadDate(t *testing.T) {
	h := budgetHandler(&mockBudgetRepo{})
	req := chiRequest("GET", "/budgets/"+testUUID+"/vs-actual?from=nope&to=2026-12-31", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.VsActual(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for bad date", rr.Code)
	}
}
