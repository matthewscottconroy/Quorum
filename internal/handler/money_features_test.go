package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// ---- refunds ----

func TestRecordRefund_Success(t *testing.T) {
	var captured *model.Transaction
	h := NewDuesHandler(&mockDuesRepo{
		GetInvoiceFn: func(_ context.Context, id string) (*model.DuesInvoice, error) {
			return testInvoice(id, "m1"), nil // USD invoice
		},
		CreateGuardedTransactionFn: func(_ context.Context, tx *model.Transaction, _ bool) (*model.Transaction, int64, error) {
			captured = tx
			tx.ID = "tx1"
			return tx, 0, nil
		},
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
	h := NewDuesHandler(&mockDuesRepo{
		GetInvoiceFn: func(_ context.Context, id string) (*model.DuesInvoice, error) {
			return testInvoice(id, "m1"), nil // 10000 minor: the plan sums exactly
		},
	})
	body := `{"installments":[{"amount_minor":5000,"due_date":"2026-01-15"},{"amount_minor":5000,"due_date":"2026-02-15"}]}`
	req := chiRequest("PUT", "/dues/"+testUUID+"/installments", body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.SetInstallments(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), `"delta_minor":null`) {
		t.Fatalf("an exact plan must report a null delta: %s", rr.Body)
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

func (m *mockActuals) StatementCash(ctx context.Context, from, to string) ([]model.GLBalance, error) {
	return m.fn(ctx, from, to, nil)
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
				{Type: "income", Balance: -90000, Currency: "USD", Code: "4000"},
				{Type: "expense", Balance: 85000, Currency: "USD", Code: "5000"},
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
	// Favorable-positive at every level: spending 85000 of an 80000 budget
	// is 5000 UNFAVORABLE, i.e. -5000 — the same convention as the
	// per-category rows, so one overspend never shows two signs.
	if got["expense_variance"].(float64) != -5000 {
		t.Errorf("expense_variance: got %v, want -5000", got["expense_variance"])
	}
}

func TestBudgetVsActual_BadDate(t *testing.T) {
	h := budgetHandler(&mockBudgetRepo{
		GetScenarioFn: func(_ context.Context, id string) (*model.BudgetScenario, error) {
			return &model.BudgetScenario{ID: id, Name: "FY26", Currency: "USD"}, nil
		},
	})
	req := chiRequest("GET", "/budgets/"+testUUID+"/vs-actual?from=nope&to=2026-12-31", "", map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.VsActual(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for bad date", rr.Code)
	}
}

// A refund can never exceed what was actually collected, and a waived
// invoice refuses refunds outright (its receivable is already zero).
func TestRecordRefund_Bounds(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{
		GetInvoiceFn: func(_ context.Context, id string) (*model.DuesInvoice, error) {
			return testInvoice(id, "m1"), nil
		},
		// Only 2000 was collected: the locked repo guard refuses 5000.
		CreateGuardedTransactionFn: func(_ context.Context, _ *model.Transaction, _ bool) (*model.Transaction, int64, error) {
			return nil, 2000, repo.ErrExceedsPaid
		},
	})
	body := `{"amount_minor":5000,"currency":"USD","provider":"manual"}`
	req := chiRequest("POST", "/dues/"+testUUID+"/refund", body, map[string]string{"id": testUUID})
	req = withCtxUser(req, "u1", "officer")
	rr := httptest.NewRecorder()
	h.RecordRefund(rr, req)
	if rr.Code != 409 {
		t.Fatalf("over-refund: got %d, want 409 (%s)", rr.Code, rr.Body)
	}

	waived := testInvoice(testUUID, "m1")
	waived.Status = "waived"
	h2 := NewDuesHandler(&mockDuesRepo{
		GetInvoiceFn: func(_ context.Context, _ string) (*model.DuesInvoice, error) { return waived, nil },
		CreateGuardedTransactionFn: func(_ context.Context, _ *model.Transaction, _ bool) (*model.Transaction, int64, error) {
			return nil, 0, repo.ErrInvoiceNotPayable
		},
	})
	req2 := chiRequest("POST", "/dues/"+testUUID+"/refund", `{"amount_minor":100,"currency":"USD","provider":"manual"}`, map[string]string{"id": testUUID})
	req2 = withCtxUser(req2, "u1", "officer")
	rr2 := httptest.NewRecorder()
	h2.RecordRefund(rr2, req2)
	if rr2.Code != 409 {
		t.Fatalf("refund on waived: got %d, want 409 (%s)", rr2.Code, rr2.Body)
	}
}
