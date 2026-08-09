package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quorum/internal/model"
)

// mockGLRepoC implements the Phase C surface; every method returns a benign
// default, with hooks for the few the tests need to steer.
type mockGLRepoC struct {
	manualEntry func(ctx context.Context, date, memo, by string, lines []model.GLLineInput) (string, error)
	closePeriod func(ctx context.Context, month, by string) error
	createAcct  func(ctx context.Context, code, name, typ string) error
	setRule     func(ctx context.Context, key, code, by string) error
}

func (m *mockGLRepoC) Periods(context.Context) ([]model.AccountingPeriod, error) { return nil, nil }
func (m *mockGLRepoC) ClosePeriod(ctx context.Context, month, by string) error {
	if m.closePeriod != nil {
		return m.closePeriod(ctx, month, by)
	}
	return nil
}
func (m *mockGLRepoC) ReopenPeriod(context.Context, string) error { return nil }
func (m *mockGLRepoC) ManualEntry(ctx context.Context, date, memo, by string, lines []model.GLLineInput) (string, error) {
	if m.manualEntry != nil {
		return m.manualEntry(ctx, date, memo, by, lines)
	}
	return "entry-1", nil
}
func (m *mockGLRepoC) Statement(context.Context, string, string, []string) ([]model.GLBalance, error) {
	return nil, nil
}
func (m *mockGLRepoC) ARAging(context.Context, string) ([]model.ARAgingRow, error) { return nil, nil }
func (m *mockGLRepoC) Accounts(context.Context) ([]model.GLAccount, error)         { return nil, nil }
func (m *mockGLRepoC) CreateAccount(ctx context.Context, code, name, typ string) error {
	if m.createAcct != nil {
		return m.createAcct(ctx, code, name, typ)
	}
	return nil
}
func (m *mockGLRepoC) UpdateAccount(context.Context, string, *string, *bool) error { return nil }
func (m *mockGLRepoC) LedgerCSVRows(context.Context, string, string) ([][]string, error) {
	return nil, nil
}
func (m *mockGLRepoC) PostingRules(context.Context) ([]model.PostingRule, error) { return nil, nil }
func (m *mockGLRepoC) SetPostingRule(ctx context.Context, key, code, by string) error {
	if m.setRule != nil {
		return m.setRule(ctx, key, code, by)
	}
	return nil
}
func (m *mockGLRepoC) StatementCash(context.Context, string, string) ([]model.GLBalance, error) {
	return nil, nil
}
func (m *mockGLRepoC) PurchasesCSVRows(context.Context, string, string) ([][]string, error) {
	return nil, nil
}

type mockGLRepo struct{}

func (m *mockGLRepo) TrialBalance(context.Context) ([]model.GLBalance, error)     { return nil, nil }
func (m *mockGLRepo) Reconcile(context.Context) ([]model.GLReconcileRow, error)   { return nil, nil }
func (m *mockGLRepo) RecentEntries(context.Context, int) ([]model.GLEntry, error) { return nil, nil }

func accountingHandler(c glRepoC) *AccountingHandler {
	h := NewAccountingHandler(&mockGLRepo{})
	h.SetPhaseC(c)
	return h
}

func postManual(t *testing.T, h *AccountingHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/accounting/entries", strings.NewReader(body))
	req = withCtxUser(req, "u-admin", "admin")
	rr := httptest.NewRecorder()
	h.ManualEntry(rr, req)
	return rr
}

// A balanced two-line entry posts.
func TestManualEntry_Balanced(t *testing.T) {
	h := accountingHandler(&mockGLRepoC{})
	body := `{"entry_date":"2026-03-01","memo":"reclass","lines":[
		{"account_code":"1000","currency":"USD","debit":5000,"credit":0},
		{"account_code":"4000","currency":"USD","debit":0,"credit":5000}]}`
	rr := postManual(t, h, body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("balanced entry: got %d, want 201 (%s)", rr.Code, rr.Body)
	}
}

// An unbalanced entry is refused at the handler with 400 (before hitting the DB).
func TestManualEntry_Unbalanced(t *testing.T) {
	called := false
	h := accountingHandler(&mockGLRepoC{
		manualEntry: func(context.Context, string, string, string, []model.GLLineInput) (string, error) {
			called = true
			return "x", nil
		},
	})
	body := `{"entry_date":"2026-03-01","memo":"skewed","lines":[
		{"account_code":"1000","currency":"USD","debit":5000,"credit":0},
		{"account_code":"4000","currency":"USD","debit":0,"credit":4000}]}`
	rr := postManual(t, h, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unbalanced entry: got %d, want 400 (%s)", rr.Code, rr.Body)
	}
	if called {
		t.Fatal("unbalanced entry reached the repo; it should be rejected first")
	}
}

// Balance is enforced per currency: two currencies each balanced passes,
// but a single-currency imbalance masked by another currency is caught.
func TestManualEntry_PerCurrencyBalance(t *testing.T) {
	h := accountingHandler(&mockGLRepoC{})
	// USD balances, EUR does not.
	body := `{"entry_date":"2026-03-01","memo":"multi","lines":[
		{"account_code":"1000","currency":"USD","debit":100,"credit":0},
		{"account_code":"4000","currency":"USD","debit":0,"credit":100},
		{"account_code":"1000","currency":"EUR","debit":50,"credit":0},
		{"account_code":"4000","currency":"EUR","debit":0,"credit":40}]}`
	rr := postManual(t, h, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("per-currency imbalance: got %d, want 400 (%s)", rr.Code, rr.Body)
	}
}

// A line with both debit and credit (or neither) is rejected.
func TestManualEntry_LineShape(t *testing.T) {
	h := accountingHandler(&mockGLRepoC{})
	bad := []string{
		`{"entry_date":"2026-03-01","memo":"m","lines":[
			{"account_code":"1000","currency":"USD","debit":50,"credit":50},
			{"account_code":"4000","currency":"USD","debit":0,"credit":50}]}`,
		`{"entry_date":"2026-03-01","memo":"m","lines":[
			{"account_code":"1000","currency":"USD","debit":0,"credit":0},
			{"account_code":"4000","currency":"USD","debit":0,"credit":0}]}`,
		// only one line
		`{"entry_date":"2026-03-01","memo":"m","lines":[
			{"account_code":"1000","currency":"USD","debit":50,"credit":0}]}`,
		// bad date
		`{"entry_date":"nope","memo":"m","lines":[
			{"account_code":"1000","currency":"USD","debit":50,"credit":0},
			{"account_code":"4000","currency":"USD","debit":0,"credit":50}]}`,
	}
	for _, body := range bad {
		rr := postManual(t, h, body)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("bad line shape %q: got %d, want 400 (%s)", body, rr.Code, rr.Body)
		}
	}
}

// Posting into a closed period surfaces the repo error, not a 500 masking it.
func TestManualEntry_ClosedPeriodError(t *testing.T) {
	h := accountingHandler(&mockGLRepoC{
		manualEntry: func(context.Context, string, string, string, []model.GLLineInput) (string, error) {
			return "", errors.New("period 2026-03 is closed")
		},
	})
	body := `{"entry_date":"2026-03-01","memo":"late","lines":[
		{"account_code":"1000","currency":"USD","debit":100,"credit":0},
		{"account_code":"4000","currency":"USD","debit":0,"credit":100}]}`
	rr := postManual(t, h, body)
	if rr.Code == http.StatusCreated {
		t.Fatalf("closed-period entry unexpectedly succeeded (%s)", rr.Body)
	}
}

// ClosePeriod validates the month format.
func TestClosePeriod_Validation(t *testing.T) {
	h := accountingHandler(&mockGLRepoC{})
	req := httptest.NewRequest("POST", "/accounting/periods/close", strings.NewReader(`{"month":"2026-03"}`))
	req = withCtxUser(req, "u-admin", "admin")
	rr := httptest.NewRecorder()
	h.ClosePeriod(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("short month: got %d, want 400 (%s)", rr.Code, rr.Body)
	}
}

// CreateAccount validates the 4-digit code, name, and type.
func TestCreateAccount_Validation(t *testing.T) {
	h := accountingHandler(&mockGLRepoC{})
	bad := []string{
		`{"Code":"12","Name":"Cash","Type":"asset"}`,
		`{"Code":"1000","Name":"","Type":"asset"}`,
		`{"Code":"1000","Name":"Cash","Type":"wallet"}`,
	}
	for _, body := range bad {
		req := httptest.NewRequest("POST", "/accounting/accounts", strings.NewReader(body))
		req = withCtxUser(req, "u-admin", "admin")
		rr := httptest.NewRecorder()
		h.CreateAccount(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("create account %q: got %d, want 400 (%s)", body, rr.Code, rr.Body)
		}
	}
}

// SetPostingRules rejects an unknown rule key and a non-4-digit account code,
// and accepts the counterparty-billing keys (payable, income.services).
func TestSetPostingRules_Validation(t *testing.T) {
	h := accountingHandler(&mockGLRepoC{})
	bad := []string{
		`{"not.a.rule":"1000"}`,
		`{"receivable":"12"}`,
		`{}`,
	}
	for _, body := range bad {
		req := httptest.NewRequest("PUT", "/accounting/posting-rules", strings.NewReader(body))
		req = withCtxUser(req, "u-admin", "admin")
		rr := httptest.NewRecorder()
		h.SetPostingRules(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("posting rules %q: got %d, want 400 (%s)", body, rr.Code, rr.Body)
		}
	}

	// A valid update (including the newer payable/income.services keys) passes.
	req := httptest.NewRequest("PUT", "/accounting/posting-rules",
		strings.NewReader(`{"payable":"2000","income.services":"4100"}`))
	req = withCtxUser(req, "u-admin", "admin")
	rr := httptest.NewRecorder()
	h.SetPostingRules(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid posting rules: got %d, want 200 (%s)", rr.Code, rr.Body)
	}
}
