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

	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

func testInvoice(id, memberID string) *model.DuesInvoice {
	return &model.DuesInvoice{
		ID:          id,
		MemberID:    memberID,
		AmountMinor: 10000,
		Currency:    "USD",
		PeriodLabel: "2024-Q1",
		DueDate:     time.Now().Add(30 * 24 * time.Hour),
		Status:      "pending",
	}
}

// ---- List ----

func TestDuesList_ReturnsInvoices(t *testing.T) {
	invoices := []model.DuesInvoice{*testInvoice("11111111-1111-1111-1111-111111111111", "m1"), *testInvoice("i2", "m2")}
	h := NewDuesHandler(&mockDuesRepo{
		ListInvoicesFn: func(_ context.Context, _ repo.InvoiceFilter) ([]model.DuesInvoice, int, error) {
			return invoices, len(invoices), nil
		},
	})
	req := httptest.NewRequest("GET", "/dues", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: got %d", rr.Code)
	}
	var got model.Page[model.DuesInvoice]
	json.NewDecoder(rr.Body).Decode(&got)
	if len(got.Data) != 2 {
		t.Errorf("expected 2 invoices, got %d", len(got.Data))
	}
}

func TestDuesList_FilterPassedThrough(t *testing.T) {
	var captured repo.InvoiceFilter
	h := NewDuesHandler(&mockDuesRepo{
		ListInvoicesFn: func(_ context.Context, f repo.InvoiceFilter) ([]model.DuesInvoice, int, error) {
			captured = f
			return nil, 0, nil
		},
	})
	req := httptest.NewRequest("GET", "/dues?member_id="+testUUID+"&status=overdue&period=2024-Q1", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if captured.MemberID != testUUID {
		t.Errorf("MemberID: got %q", captured.MemberID)
	}
	if captured.Status != "overdue" {
		t.Errorf("Status: got %q", captured.Status)
	}
	if captured.PeriodLabel != "2024-Q1" {
		t.Errorf("PeriodLabel: got %q", captured.PeriodLabel)
	}
}

func TestDuesList_BadMemberID(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{})
	req := httptest.NewRequest("GET", "/dues?member_id=not-a-uuid", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID member_id", rr.Code)
	}
}

// ---- Create ----

func TestDuesCreate_Single(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{
		CreateInvoiceBatchFn: func(_ context.Context, invs []*model.DuesInvoice) ([]model.DuesInvoice, error) {
			invs[0].ID = "new-id"
			return []model.DuesInvoice{*invs[0]}, nil
		},
	})
	body := `{"member_id":"` + testUUID + `","amount_minor":15000,"period_label":"2024-Q2","due_date":"2024-06-30"}`
	req := httptest.NewRequest("POST", "/dues", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	var got model.DuesInvoice
	json.NewDecoder(rr.Body).Decode(&got)
	if got.ID != "new-id" {
		t.Errorf("id: got %q", got.ID)
	}
}

func TestDuesCreate_Bulk(t *testing.T) {
	var created []string
	h := NewDuesHandler(&mockDuesRepo{
		CreateInvoiceBatchFn: func(_ context.Context, invs []*model.DuesInvoice) ([]model.DuesInvoice, error) {
			var result []model.DuesInvoice
			for _, inv := range invs {
				created = append(created, inv.MemberID)
				inv.ID = "id-" + inv.MemberID
				result = append(result, *inv)
			}
			return result, nil
		},
	})
	body := `{"member_ids":["` + testUUID + `","` + testUUID2 + `","33333333-3333-3333-3333-333333333333"],"amount_minor":10000,"period_label":"2024-Q3","due_date":"2024-09-30"}`
	req := httptest.NewRequest("POST", "/dues", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if len(created) != 3 {
		t.Errorf("expected 3 invoices created, got %d", len(created))
	}
}

func TestDuesCreate_MissingAmount(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{})
	body := `{"member_id":"` + testUUID + `","period_label":"2024-Q1","due_date":"2024-03-31"}`
	req := httptest.NewRequest("POST", "/dues", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestDuesCreate_InvalidDueDate(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{})
	body := `{"member_id":"` + testUUID + `","amount_minor":10000,"period_label":"Q1","due_date":"not-a-date"}`
	req := httptest.NewRequest("POST", "/dues", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestDuesCreate_NoMemberID(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{})
	body := `{"amount_minor":10000,"period_label":"Q1","due_date":"2024-03-31"}`
	req := httptest.NewRequest("POST", "/dues", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestDuesCreate_BadMemberUUID(t *testing.T) {
	// Every member id (single or bulk) must be a UUID.
	h := NewDuesHandler(&mockDuesRepo{})
	body := `{"member_ids":["` + testUUID + `","garbage"],"amount_minor":10000,"period_label":"Q1","due_date":"2024-03-31"}`
	req := httptest.NewRequest("POST", "/dues", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID member id", rr.Code)
	}

	h = NewDuesHandler(&mockDuesRepo{})
	body = `{"member_id":"m1","amount_minor":10000,"period_label":"Q1","due_date":"2024-03-31"}`
	req = httptest.NewRequest("POST", "/dues", strings.NewReader(body))
	rr = httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for non-UUID single member_id", rr.Code)
	}
}

func TestDuesCreate_DefaultCurrency(t *testing.T) {
	var capturedCurrency string
	h := NewDuesHandler(&mockDuesRepo{
		CreateInvoiceBatchFn: func(_ context.Context, invs []*model.DuesInvoice) ([]model.DuesInvoice, error) {
			capturedCurrency = invs[0].Currency
			return []model.DuesInvoice{*invs[0]}, nil
		},
	})
	body := `{"member_id":"` + testUUID + `","amount_minor":5000,"period_label":"Q1","due_date":"2024-03-31"}`
	req := httptest.NewRequest("POST", "/dues", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if capturedCurrency != "USD" {
		t.Errorf("default currency: got %q, want USD", capturedCurrency)
	}
}

// ---- Get ----

func TestDuesGet_Found(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{
		GetInvoiceFn: func(_ context.Context, id string) (*model.DuesInvoice, error) {
			return testInvoice(id, "m1"), nil
		},
	})
	req := chiRequest("GET", "/dues/i1", "", map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d", rr.Code)
	}
}

func TestDuesGet_NotFound(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{
		GetInvoiceFn: func(_ context.Context, _ string) (*model.DuesInvoice, error) {
			return nil, errors.New("not found")
		},
	})
	req := chiRequest("GET", "/dues/ghost", "", map[string]string{"id": "00000000-0000-0000-0000-000000000000"})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404", rr.Code)
	}
}

// ---- Update ----

func TestDuesUpdate_Success(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{
		UpdateInvoiceStatusFn: func(_ context.Context, _, _ string, _ *string) error { return nil },
		GetInvoiceFn:          func(_ context.Context, id string) (*model.DuesInvoice, error) { return testInvoice(id, "m1"), nil },
	})
	body := `{"status":"waived"}`
	req := chiRequest("PATCH", "/dues/i1", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200; body: %s", rr.Code, rr.Body)
	}
}

func TestDuesUpdate_InvalidStatus(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{})
	body := `{"status":"invalid-status"}`
	req := chiRequest("PATCH", "/dues/i1", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestDuesUpdate_MissingStatus(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{})
	body := `{"notes":"some note"}`
	req := chiRequest("PATCH", "/dues/i1", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestDuesUpdate_NotFound(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{
		UpdateInvoiceStatusFn: func(_ context.Context, _, _ string, _ *string) error { return pgx.ErrNoRows },
	})
	body := `{"status":"paid"}`
	req := chiRequest("PATCH", "/dues/i1", body, map[string]string{"id": testUUID2})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404 for ErrNoRows", rr.Code)
	}
}

func TestDuesUpdate_RepoError(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{
		UpdateInvoiceStatusFn: func(_ context.Context, _, _ string, _ *string) error { return errors.New("db error") },
	})
	body := `{"status":"paid"}`
	req := chiRequest("PATCH", "/dues/i1", body, map[string]string{"id": testUUID})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500", rr.Code)
	}
}

// ---- CreateTransaction ----

func TestDuesCreateTransaction_Success(t *testing.T) {
	inv := testInvoice("11111111-1111-1111-1111-111111111111", "m1")
	h := NewDuesHandler(&mockDuesRepo{
		GetInvoiceFn: func(_ context.Context, _ string) (*model.DuesInvoice, error) { return inv, nil },
		CreateTransactionFn: func(_ context.Context, t *model.Transaction) (*model.Transaction, error) {
			t.ID = "tx1"
			return t, nil
		},
		RecomputeInvoiceStatusFn: func(_ context.Context, _ string) error { return nil },
	})
	body := `{"amount_minor":10000,"provider":"stripe"}`
	req := withCtxUser(
		chiRequest("POST", "/dues/i1/transactions", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111"}),
		"user-1", "officer",
	)
	rr := httptest.NewRecorder()
	h.CreateTransaction(rr, req)

	if rr.Code != 201 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestDuesCreateTransaction_MissingAmount(t *testing.T) {
	inv := testInvoice("11111111-1111-1111-1111-111111111111", "m1")
	h := NewDuesHandler(&mockDuesRepo{
		GetInvoiceFn: func(_ context.Context, _ string) (*model.DuesInvoice, error) { return inv, nil },
	})
	body := `{"provider":"stripe"}`
	req := chiRequest("POST", "/dues/i1/transactions", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.CreateTransaction(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestDuesCreateTransaction_InvoiceNotFound(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{
		GetInvoiceFn: func(_ context.Context, _ string) (*model.DuesInvoice, error) {
			return nil, errors.New("not found")
		},
	})
	body := `{"amount_minor":5000,"provider":"manual"}`
	req := chiRequest("POST", "/dues/ghost/transactions", body, map[string]string{"id": "00000000-0000-0000-0000-000000000000"})
	rr := httptest.NewRecorder()
	h.CreateTransaction(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d, want 404", rr.Code)
	}
}

// ---- ListTransactions ----

func TestDuesListTransactions(t *testing.T) {
	txs := []model.Transaction{{ID: "tx1", Provider: "stripe"}}
	h := NewDuesHandler(&mockDuesRepo{
		ListTransactionsFn: func(_ context.Context, _ repo.TransactionFilter) ([]model.Transaction, int, error) {
			return txs, 1, nil
		},
	})
	req := httptest.NewRequest("GET", "/dues/transactions", nil)
	rr := httptest.NewRecorder()
	h.ListTransactions(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d", rr.Code)
	}
	var got model.Page[model.Transaction]
	json.NewDecoder(rr.Body).Decode(&got)
	if len(got.Data) != 1 {
		t.Errorf("expected 1 transaction, got %d", len(got.Data))
	}
}

func TestDuesListTransactions_BadUUIDFilters(t *testing.T) {
	for _, param := range []string{"invoice_id", "member_id"} {
		h := NewDuesHandler(&mockDuesRepo{})
		req := httptest.NewRequest("GET", "/dues/transactions?"+param+"=garbage", nil)
		rr := httptest.NewRecorder()
		h.ListTransactions(rr, req)
		if rr.Code != 400 {
			t.Errorf("%s=garbage: got %d, want 400", param, rr.Code)
		}
	}
}

// ---- Valid statuses ----

func TestDuesUpdate_AllValidStatuses(t *testing.T) {
	for _, status := range []string{"pending", "paid", "partial", "overdue", "waived"} {
		h := NewDuesHandler(&mockDuesRepo{
			UpdateInvoiceStatusFn: func(_ context.Context, _, _ string, _ *string) error { return nil },
			GetInvoiceFn: func(_ context.Context, id string) (*model.DuesInvoice, error) {
				return testInvoice(id, "m1"), nil
			},
		})
		body := `{"status":"` + status + `"}`
		req := chiRequest("PATCH", "/dues/i1", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
		rr := httptest.NewRecorder()
		h.Update(rr, req)
		if rr.Code != 200 {
			t.Errorf("status %q: got HTTP %d, want 200", status, rr.Code)
		}
	}
}

// Ensure http.Handler interface — compile-time check.
var _ http.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
