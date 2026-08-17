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
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
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
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
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
	body := `{"status":"overdue"}`
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
	body := `{"status":"overdue"}`
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

func TestDuesCreateTransaction_CurrencyMismatch(t *testing.T) {
	// A payment in a different currency than the invoice must be rejected.
	inv := testInvoice("11111111-1111-1111-1111-111111111111", "m1")
	inv.Currency = "USD"
	h := NewDuesHandler(&mockDuesRepo{
		GetInvoiceFn: func(_ context.Context, _ string) (*model.DuesInvoice, error) { return inv, nil },
	})
	body := `{"amount_minor":10000,"provider":"manual","currency":"EUR"}`
	req := withCtxUser(
		chiRequest("POST", "/dues/i1/transactions", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111"}),
		"user-1", "officer",
	)
	rr := httptest.NewRecorder()
	h.CreateTransaction(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for currency mismatch", rr.Code)
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
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	h.ListTransactions(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d", rr.Code)
	}
	var got model.Page[model.Transaction]
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Data) != 1 {
		t.Errorf("expected 1 transaction, got %d", len(got.Data))
	}
}

func TestDuesListTransactions_BadUUIDFilters(t *testing.T) {
	for _, param := range []string{"invoice_id", "member_id"} {
		h := NewDuesHandler(&mockDuesRepo{})
		req := httptest.NewRequest("GET", "/dues/transactions?"+param+"=garbage", nil)
		req = withCtxUser(req, "u", "officer")
		rr := httptest.NewRecorder()
		h.ListTransactions(rr, req)
		if rr.Code != 400 {
			t.Errorf("%s=garbage: got %d, want 400", param, rr.Code)
		}
	}
}

// ---- Valid statuses ----

func TestDuesUpdate_AllValidStatuses(t *testing.T) {
	// pending/overdue/waived remain manual transitions; paid and partial are
	// ledger-derived and refused (recorded payments decide them).
	for status, want := range map[string]int{
		"pending": 200, "overdue": 200, "waived": 200, "paid": 409, "partial": 409,
	} {
		h := NewDuesHandler(&mockDuesRepo{
			UpdateInvoiceStatusFn:    func(_ context.Context, _, _ string, _ *string) error { return nil },
			RecomputeInvoiceStatusFn: func(_ context.Context, _ string) error { return nil },
			GetInvoiceFn: func(_ context.Context, id string) (*model.DuesInvoice, error) {
				return testInvoice(id, "m1"), nil
			},
		})
		body := `{"status":"` + status + `"}`
		req := chiRequest("PATCH", "/dues/i1", body, map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
		rr := httptest.NewRecorder()
		h.Update(rr, req)
		if rr.Code != want {
			t.Errorf("status %q: got HTTP %d, want %d", status, rr.Code, want)
		}
	}
}

// Ensure http.Handler interface — compile-time check.
var _ http.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

// 'paid' and 'partial' are ledger-derived: setting them by hand is refused.
func TestDuesUpdate_ComputedStatusesRefused(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{})
	for _, status := range []string{"paid", "partial"} {
		req := chiRequest("PATCH", "/dues/x", `{"status":"`+status+`"}`,
			map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
		rr := httptest.NewRecorder()
		h.Update(rr, withCtxUser(req, "u", "officer"))
		if rr.Code != http.StatusConflict {
			t.Fatalf("manual %s: got %d, want 409 (%s)", status, rr.Code, rr.Body)
		}
	}
}

// Waiving a fully paid invoice is refused: there is nothing left to waive.
func TestDuesUpdate_WaivePaidRefused(t *testing.T) {
	h := NewDuesHandler(&mockDuesRepo{
		GetInvoiceFn: func(_ context.Context, id string) (*model.DuesInvoice, error) {
			return &model.DuesInvoice{ID: id, Status: "paid", AmountMinor: 100, Currency: "USD"}, nil
		},
	})
	req := chiRequest("PATCH", "/dues/x", `{"status":"waived"}`,
		map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.Update(rr, withCtxUser(req, "u", "officer"))
	if rr.Code != http.StatusConflict {
		t.Fatalf("waive paid: got %d, want 409 (%s)", rr.Code, rr.Body)
	}
}

// A manual payment beyond the remaining balance needs the explicit flag.
func TestDuesTransaction_OverpaymentGuard(t *testing.T) {
	recorded := false
	mock := &mockDuesRepo{
		GetInvoiceFn: func(_ context.Context, id string) (*model.DuesInvoice, error) {
			return &model.DuesInvoice{ID: id, MemberID: "22222222-2222-2222-2222-222222222222",
				Status: "partial", AmountMinor: 1000, Currency: "USD"}, nil
		},
		// The guard lives in the repo now (under the invoice lock): 400 of
		// 1000 remains, so 500 without the flag is refused.
		CreateGuardedTransactionFn: func(_ context.Context, tr *model.Transaction, allowOverpay bool) (*model.Transaction, int64, error) {
			if tr.AmountMinor > 400 && !allowOverpay {
				return nil, 400, repo.ErrExceedsRemaining
			}
			recorded = true
			return tr, 0, nil
		},
	}
	h := NewDuesHandler(mock)
	// 500 > the 400 remaining → 409, nothing recorded.
	req := chiRequest("POST", "/dues/x/transactions", `{"amount_minor":500,"provider":"cash"}`,
		map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr := httptest.NewRecorder()
	h.CreateTransaction(rr, withCtxUser(req, "u", "officer"))
	if rr.Code != http.StatusConflict || recorded {
		t.Fatalf("overpay: got %d recorded=%v, want 409 and no record (%s)", rr.Code, recorded, rr.Body)
	}
	// Same payment with the acknowledgement → recorded.
	req2 := chiRequest("POST", "/dues/x/transactions", `{"amount_minor":500,"provider":"cash","allow_overpayment":true}`,
		map[string]string{"id": "11111111-1111-1111-1111-111111111111"})
	rr2 := httptest.NewRecorder()
	h.CreateTransaction(rr2, withCtxUser(req2, "u", "officer"))
	if rr2.Code != http.StatusCreated || !recorded {
		t.Fatalf("acknowledged overpay: got %d recorded=%v, want 201 (%s)", rr2.Code, recorded, rr2.Body)
	}
}

// A plain member hitting /dues/transactions sees exactly their own history:
// the handler pins member_id to the caller regardless of what was asked.
func TestListTransactions_MemberPinnedToSelf(t *testing.T) {
	var gotMember string
	h := NewDuesHandler(&mockDuesRepo{
		ListTransactionsFn: func(_ context.Context, f repo.TransactionFilter) ([]model.Transaction, int, error) {
			gotMember = f.MemberID
			return []model.Transaction{}, 0, nil
		},
	})
	// The member asks for SOMEONE ELSE's history; the pin overrides it.
	req := httptest.NewRequest("GET", "/dues/transactions?member_id=99999999-9999-4999-8999-999999999999", nil)
	req = withCtxUserMember(req, "u", "member", "11111111-1111-4111-8111-111111111111")
	rr := httptest.NewRecorder()
	h.ListTransactions(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if gotMember != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("filter member = %q, want the CALLER's id", gotMember)
	}
	// No member link at all: refused.
	req2 := httptest.NewRequest("GET", "/dues/transactions", nil)
	req2 = withCtxUser(req2, "u", "member")
	rr2 := httptest.NewRecorder()
	h.ListTransactions(rr2, req2)
	if rr2.Code != 403 {
		t.Fatalf("unlinked member: got %d, want 403", rr2.Code)
	}
}
