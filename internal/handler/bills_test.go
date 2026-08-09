package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quorum/internal/model"
	"quorum/internal/repo"
)

type mockBillsRepo struct {
	pay  func(ctx context.Context, id, fundID, provider string) (*model.Bill, error)
	void func(ctx context.Context, id string) (*model.Bill, error)
}

func (m *mockBillsRepo) Create(ctx context.Context, b *model.Bill, by string) (*model.Bill, error) {
	b.ID = billID
	b.Status = "open"
	return b, nil
}
func (m *mockBillsRepo) Get(ctx context.Context, id string) (*model.Bill, error) {
	return &model.Bill{ID: id, Status: "open"}, nil
}
func (m *mockBillsRepo) List(ctx context.Context, status string, limit int) ([]model.Bill, error) {
	return []model.Bill{}, nil
}
func (m *mockBillsRepo) Pay(ctx context.Context, id, fundID, provider string) (*model.Bill, error) {
	if m.pay != nil {
		return m.pay(ctx, id, fundID, provider)
	}
	return &model.Bill{ID: id, Status: "paid"}, nil
}
func (m *mockBillsRepo) Void(ctx context.Context, id string) (*model.Bill, error) {
	if m.void != nil {
		return m.void(ctx, id)
	}
	return &model.Bill{ID: id, Status: "void"}, nil
}
func (m *mockBillsRepo) APAging(ctx context.Context, asOf string) ([]model.ARAgingRow, error) {
	return nil, nil
}

const billID = "55555555-5555-5555-5555-555555555555"

// List rejects an out-of-range status filter.
func TestBillsList_BadStatus(t *testing.T) {
	h := NewBillsHandler(&mockBillsRepo{})
	req := httptest.NewRequest("GET", "/bills?status=frozen", nil)
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad status: got %d, want 400 (%s)", rr.Code, rr.Body)
	}
}

// Create validates contact_id, expense_account_id, amount, and currency.
func TestBillsCreate_Validation(t *testing.T) {
	h := NewBillsHandler(&mockBillsRepo{})
	good := "11111111-1111-1111-1111-111111111111"
	bad := []string{
		`{"contact_id":"nope","expense_account_id":"` + good + `","amount_minor":100,"currency":"USD"}`,
		`{"contact_id":"` + good + `","expense_account_id":"nope","amount_minor":100,"currency":"USD"}`,
		`{"contact_id":"` + good + `","expense_account_id":"` + good + `","amount_minor":0,"currency":"USD"}`,
		`{"contact_id":"` + good + `","expense_account_id":"` + good + `","amount_minor":100,"currency":"US"}`,
		`{"contact_id":"` + good + `","expense_account_id":"` + good + `","amount_minor":100,"currency":"USD","bill_date":"2026-13-01"}`,
	}
	for _, body := range bad {
		req := httptest.NewRequest("POST", "/bills", strings.NewReader(body))
		req = withCtxUser(req, "u", "officer")
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("create %q: got %d, want 400 (%s)", body, rr.Code, rr.Body)
		}
	}
}

// Paying from a fund with too little money is a 409, not a 500.
func TestBillsPay_InsufficientFunds409(t *testing.T) {
	h := NewBillsHandler(&mockBillsRepo{
		pay: func(_ context.Context, _, _, _ string) (*model.Bill, error) {
			return nil, repo.ErrInsufficientFunds
		},
	})
	fund := "11111111-1111-1111-1111-111111111111"
	req := reqWithParam("POST", "/bills/"+billID+"/pay", `{"fund_id":"`+fund+`"}`, map[string]string{"id": billID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	h.Pay(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("underfunded pay: got %d, want 409 (%s)", rr.Code, rr.Body)
	}
}

// Paying an already-paid/void bill (ErrNotApprovable) is a 409.
func TestBillsPay_NotApprovable409(t *testing.T) {
	h := NewBillsHandler(&mockBillsRepo{
		pay: func(_ context.Context, _, _, _ string) (*model.Bill, error) {
			return nil, repo.ErrNotApprovable
		},
	})
	req := reqWithParam("POST", "/bills/"+billID+"/pay", `{"provider":"wire"}`, map[string]string{"id": billID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	h.Pay(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("double pay: got %d, want 409 (%s)", rr.Code, rr.Body)
	}
}

// Pay rejects a non-UUID fund_id (400).
func TestBillsPay_BadFundID(t *testing.T) {
	h := NewBillsHandler(&mockBillsRepo{})
	req := reqWithParam("POST", "/bills/"+billID+"/pay", `{"fund_id":"not-a-uuid"}`, map[string]string{"id": billID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	h.Pay(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad fund_id: got %d, want 400 (%s)", rr.Code, rr.Body)
	}
}

// Voiding a non-open bill (ErrNotApprovable) is a 409.
func TestBillsVoid_NotApprovable409(t *testing.T) {
	h := NewBillsHandler(&mockBillsRepo{
		void: func(_ context.Context, _ string) (*model.Bill, error) {
			return nil, repo.ErrNotApprovable
		},
	})
	req := reqWithParam("POST", "/bills/"+billID+"/void", "", map[string]string{"id": billID})
	req = withCtxUser(req, "u", "admin")
	rr := httptest.NewRecorder()
	h.Void(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("void non-open: got %d, want 409 (%s)", rr.Code, rr.Body)
	}
}

// A well-formed pay succeeds (200) and passes the provider through.
func TestBillsPay_Success(t *testing.T) {
	var gotProvider string
	h := NewBillsHandler(&mockBillsRepo{
		pay: func(_ context.Context, _, _, provider string) (*model.Bill, error) {
			gotProvider = provider
			return &model.Bill{ID: billID, Status: "paid"}, nil
		},
	})
	req := reqWithParam("POST", "/bills/"+billID+"/pay", `{"provider":"ZELLE"}`, map[string]string{"id": billID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	h.Pay(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("pay: got %d, want 200 (%s)", rr.Code, rr.Body)
	}
	if gotProvider != "zelle" {
		t.Fatalf("provider = %q, want lowercased zelle", gotProvider)
	}
}
