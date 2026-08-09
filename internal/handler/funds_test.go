package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quorum/internal/auth"
	"quorum/internal/model"
	"quorum/internal/repo"
)

const (
	prID   = "22222222-2222-2222-2222-222222222222"
	fundID = "33333333-3333-3333-3333-333333333333"
)

// ---- mocks for the funds/purchases money handler ----

type mockFundsRepo struct {
	getFund      func(ctx context.Context, id string) (*model.Fund, error)
	transfer     func(ctx context.Context, fundID, direction string, amount int64, currency, memo string) error
	updatePolicy func(ctx context.Context, id string, purpose *string, approvals *int, signers *[]string) (int, error)
}

func (m *mockFundsRepo) CreateFund(ctx context.Context, name string, purpose *string, approvals int, signers []string, by string) (*model.Fund, error) {
	return &model.Fund{ID: "f1", Name: name}, nil
}
func (m *mockFundsRepo) ListFunds(ctx context.Context) ([]model.Fund, error) { return nil, nil }
func (m *mockFundsRepo) GetFund(ctx context.Context, id string) (*model.Fund, error) {
	if m.getFund != nil {
		return m.getFund(ctx, id)
	}
	return &model.Fund{ID: id, Name: "Legal Defense"}, nil
}
func (m *mockFundsRepo) UpdatePolicy(ctx context.Context, id string, purpose *string, approvals *int, signers *[]string) (int, error) {
	if m.updatePolicy != nil {
		return m.updatePolicy(ctx, id, purpose, approvals, signers)
	}
	return 0, nil
}
func (m *mockFundsRepo) Transfer(ctx context.Context, fundID, direction string, amount int64, currency, memo string) error {
	if m.transfer != nil {
		return m.transfer(ctx, fundID, direction, amount, currency, memo)
	}
	return nil
}

type mockPurchasesRepo struct {
	get      func(ctx context.Context, id string) (*model.PurchaseRequest, error)
	isSigner func(ctx context.Context, fundID, userID string) (bool, error)
	approve  func(ctx context.Context, requestID, approverID, ip string) (*model.PurchaseRequest, error)
	complete func(ctx context.Context, requestID string) (*model.PurchaseRequest, error)
	terminal func(ctx context.Context, requestID, status string) error
}

func (m *mockPurchasesRepo) Create(ctx context.Context, p *model.PurchaseRequest, requester string) (*model.PurchaseRequest, error) {
	p.ID = "pr1"
	return p, nil
}
func (m *mockPurchasesRepo) Get(ctx context.Context, id string) (*model.PurchaseRequest, error) {
	if m.get != nil {
		return m.get(ctx, id)
	}
	return &model.PurchaseRequest{ID: id, FundID: "f1", Status: "pending"}, nil
}
func (m *mockPurchasesRepo) List(ctx context.Context, fundID, status string, limit int) ([]model.PurchaseRequest, error) {
	return nil, nil
}
func (m *mockPurchasesRepo) IsSigner(ctx context.Context, fundID, userID string) (bool, error) {
	if m.isSigner != nil {
		return m.isSigner(ctx, fundID, userID)
	}
	return false, nil
}
func (m *mockPurchasesRepo) Approve(ctx context.Context, requestID, approverID, ip string) (*model.PurchaseRequest, error) {
	if m.approve != nil {
		return m.approve(ctx, requestID, approverID, ip)
	}
	return &model.PurchaseRequest{ID: requestID, Status: "approved"}, nil
}
func (m *mockPurchasesRepo) SetTerminal(ctx context.Context, requestID, newStatus string) error {
	if m.terminal != nil {
		return m.terminal(ctx, requestID, newStatus)
	}
	return nil
}
func (m *mockPurchasesRepo) Complete(ctx context.Context, requestID string) (*model.PurchaseRequest, error) {
	if m.complete != nil {
		return m.complete(ctx, requestID)
	}
	return &model.PurchaseRequest{ID: requestID, Status: "completed"}, nil
}

type mockPasswordChecker struct{ hash string }

func (m *mockPasswordChecker) GetPasswordHash(ctx context.Context, id string) (string, error) {
	return m.hash, nil
}

func fundsHandler(f fundsRepo, p purchasesRepo, u passwordChecker) *FundsHandler {
	return NewFundsHandler(f, p, u, &mockResourcesRepo{}, func(*http.Request) string { return "10.0.0.1" })
}

// The requester's own approval is refused even with the correct password.
func TestFundsApprove_RefusesSelfApproval(t *testing.T) {
	requester := "u-requester"
	hash, _ := auth.HashPassword("correct-horse")
	h := fundsHandler(&mockFundsRepo{}, &mockPurchasesRepo{
		get: func(_ context.Context, id string) (*model.PurchaseRequest, error) {
			return &model.PurchaseRequest{ID: id, FundID: "f1", Status: "pending", RequesterID: &requester}, nil
		},
	}, &mockPasswordChecker{hash: hash})

	req := reqWithParam("POST", "/purchases/pr1/approve", `{"password":"correct-horse"}`, map[string]string{"id": prID})
	req = withCtxUser(req, requester, "officer")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("self-approval: got %d, want 403 (%s)", rr.Code, rr.Body)
	}
}

// A wrong password is rejected before any state check.
func TestFundsApprove_WrongPassword(t *testing.T) {
	hash, _ := auth.HashPassword("correct-horse")
	h := fundsHandler(&mockFundsRepo{}, &mockPurchasesRepo{}, &mockPasswordChecker{hash: hash})

	req := reqWithParam("POST", "/purchases/pr1/approve", `{"password":"wrong"}`, map[string]string{"id": prID})
	req = withCtxUser(req, "u-officer", "officer")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("wrong password: got %d, want 403 (%s)", rr.Code, rr.Body)
	}
}

// A member who is neither an officer nor a named signer cannot approve.
func TestFundsApprove_NonSignerMemberRefused(t *testing.T) {
	hash, _ := auth.HashPassword("pw")
	h := fundsHandler(&mockFundsRepo{}, &mockPurchasesRepo{
		isSigner: func(_ context.Context, _, _ string) (bool, error) { return false, nil },
	}, &mockPasswordChecker{hash: hash})

	req := reqWithParam("POST", "/purchases/pr1/approve", `{"password":"pw"}`, map[string]string{"id": prID})
	req = withCtxUser(req, "u-member", "member")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-signer member: got %d, want 403 (%s)", rr.Code, rr.Body)
	}
}

// A named signer (below officer) with the right password CAN approve.
func TestFundsApprove_NamedSignerSucceeds(t *testing.T) {
	hash, _ := auth.HashPassword("pw")
	h := fundsHandler(&mockFundsRepo{}, &mockPurchasesRepo{
		isSigner: func(_ context.Context, _, _ string) (bool, error) { return true, nil },
		approve: func(_ context.Context, id, _, _ string) (*model.PurchaseRequest, error) {
			return &model.PurchaseRequest{ID: id, Status: "approved"}, nil
		},
	}, &mockPasswordChecker{hash: hash})

	req := reqWithParam("POST", "/purchases/pr1/approve", `{"password":"pw"}`, map[string]string{"id": prID})
	req = withCtxUser(req, "u-signer", "member")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("named signer: got %d, want 200 (%s)", rr.Code, rr.Body)
	}
}

// A missing password is a 400 before any lookup.
func TestFundsApprove_MissingPassword(t *testing.T) {
	h := fundsHandler(&mockFundsRepo{}, &mockPurchasesRepo{}, &mockPasswordChecker{})
	req := reqWithParam("POST", "/purchases/pr1/approve", `{}`, map[string]string{"id": prID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing password: got %d, want 400 (%s)", rr.Code, rr.Body)
	}
}

// A double-approval (repo returns ErrAlreadyApproved) surfaces as 409.
func TestFundsApprove_AlreadyApproved409(t *testing.T) {
	hash, _ := auth.HashPassword("pw")
	h := fundsHandler(&mockFundsRepo{}, &mockPurchasesRepo{
		approve: func(_ context.Context, _, _, _ string) (*model.PurchaseRequest, error) {
			return nil, repo.ErrAlreadyApproved
		},
	}, &mockPasswordChecker{hash: hash})

	req := reqWithParam("POST", "/purchases/pr1/approve", `{"password":"pw"}`, map[string]string{"id": prID})
	req = withCtxUser(req, "u-officer", "officer")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("already-approved: got %d, want 409 (%s)", rr.Code, rr.Body)
	}
}

// Completing an under-funded purchase surfaces the repo's insufficient-funds
// as a 409, not a 500.
func TestFundsComplete_InsufficientFunds409(t *testing.T) {
	h := fundsHandler(&mockFundsRepo{}, &mockPurchasesRepo{
		complete: func(_ context.Context, _ string) (*model.PurchaseRequest, error) {
			return nil, repo.ErrInsufficientFunds
		},
	}, &mockPasswordChecker{})

	req := reqWithParam("POST", "/purchases/pr1/complete", "", map[string]string{"id": prID})
	req = withCtxUser(req, "u-officer", "officer")
	rr := httptest.NewRecorder()
	h.Complete(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("overdraft complete: got %d, want 409 (%s)", rr.Code, rr.Body)
	}
}

// Completing a non-approved purchase (ErrNotApprovable) is a 409.
func TestFundsComplete_NotApprovable409(t *testing.T) {
	h := fundsHandler(&mockFundsRepo{}, &mockPurchasesRepo{
		complete: func(_ context.Context, _ string) (*model.PurchaseRequest, error) {
			return nil, repo.ErrNotApprovable
		},
	}, &mockPasswordChecker{})

	req := reqWithParam("POST", "/purchases/pr1/complete", "", map[string]string{"id": prID})
	req = withCtxUser(req, "u-officer", "officer")
	rr := httptest.NewRecorder()
	h.Complete(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("not-approvable complete: got %d, want 409 (%s)", rr.Code, rr.Body)
	}
}

// A fund transfer that would overdraw surfaces as 409.
func TestFundsTransfer_InsufficientFunds409(t *testing.T) {
	h := fundsHandler(&mockFundsRepo{
		transfer: func(_ context.Context, _, _ string, _ int64, _, _ string) error {
			return repo.ErrInsufficientFunds
		},
	}, &mockPurchasesRepo{}, &mockPasswordChecker{})

	req := reqWithParam("POST", "/funds/f1/transfers", `{"direction":"in","amount_minor":5000,"currency":"USD"}`, map[string]string{"id": fundID})
	req = withCtxUser(req, "u-admin", "admin")
	rr := httptest.NewRecorder()
	h.Transfer(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("overdraft transfer: got %d, want 409 (%s)", rr.Code, rr.Body)
	}
}

// Transfer rejects a bad direction and a non-3-letter currency (400).
func TestFundsTransfer_Validation(t *testing.T) {
	h := fundsHandler(&mockFundsRepo{}, &mockPurchasesRepo{}, &mockPasswordChecker{})
	cases := []string{
		`{"direction":"sideways","amount_minor":100,"currency":"USD"}`,
		`{"direction":"in","amount_minor":0,"currency":"USD"}`,
		`{"direction":"in","amount_minor":100,"currency":"US"}`,
	}
	for _, body := range cases {
		req := reqWithParam("POST", "/funds/f1/transfers", body, map[string]string{"id": fundID})
		req = withCtxUser(req, "u-admin", "admin")
		rr := httptest.NewRecorder()
		h.Transfer(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("transfer %q: got %d, want 400 (%s)", body, rr.Code, rr.Body)
		}
	}
}

// Cancel is allowed for the requester but refused for an unrelated member.
func TestFundsCancel_RequesterVsStranger(t *testing.T) {
	requester := "u-owner"
	newHandler := func() *FundsHandler {
		return fundsHandler(&mockFundsRepo{}, &mockPurchasesRepo{
			get: func(_ context.Context, id string) (*model.PurchaseRequest, error) {
				return &model.PurchaseRequest{ID: id, Status: "pending", RequesterID: &requester}, nil
			},
		}, &mockPasswordChecker{})
	}

	// The requester can cancel.
	req := reqWithParam("POST", "/purchases/pr1/cancel", "", map[string]string{"id": prID})
	req = withCtxUser(req, requester, "member")
	rr := httptest.NewRecorder()
	newHandler().Cancel(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("requester cancel: got %d, want 204 (%s)", rr.Code, rr.Body)
	}

	// An unrelated member cannot.
	req2 := reqWithParam("POST", "/purchases/pr1/cancel", "", map[string]string{"id": prID})
	req2 = withCtxUser(req2, "u-stranger", "member")
	rr2 := httptest.NewRecorder()
	newHandler().Cancel(rr2, req2)
	if rr2.Code != http.StatusForbidden {
		t.Fatalf("stranger cancel: got %d, want 403 (%s)", rr2.Code, rr2.Body)
	}
}

// CreatePurchase validates its body (positive amount, 3-letter currency, payee).
func TestFundsCreatePurchase_Validation(t *testing.T) {
	h := fundsHandler(&mockFundsRepo{}, &mockPurchasesRepo{}, &mockPasswordChecker{})
	bad := []string{
		`{"fund_id":"not-a-uuid","amount_minor":100,"currency":"USD","payee":"ACME"}`,
		`{"fund_id":"11111111-1111-1111-1111-111111111111","amount_minor":0,"currency":"USD","payee":"ACME"}`,
		`{"fund_id":"11111111-1111-1111-1111-111111111111","amount_minor":100,"currency":"US","payee":"ACME"}`,
		`{"fund_id":"11111111-1111-1111-1111-111111111111","amount_minor":100,"currency":"USD","payee":""}`,
	}
	for _, body := range bad {
		req := httptest.NewRequest("POST", "/purchases", strings.NewReader(body))
		req = withCtxUser(req, "u-officer", "officer")
		rr := httptest.NewRecorder()
		h.CreatePurchase(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("create %q: got %d, want 400 (%s)", body, rr.Code, rr.Body)
		}
	}
}

// A valid transfer echoes the updated fund and audit-details the movement.
func TestFundsTransfer_Success(t *testing.T) {
	var gotDir string
	h := fundsHandler(&mockFundsRepo{
		transfer: func(_ context.Context, _, direction string, _ int64, _, _ string) error {
			gotDir = direction
			return nil
		},
	}, &mockPurchasesRepo{}, &mockPasswordChecker{})

	req := reqWithParam("POST", "/funds/f1/transfers", `{"direction":"out","amount_minor":2500,"currency":"usd"}`, map[string]string{"id": fundID})
	req = withCtxUser(req, "u-admin", "admin")
	rr := httptest.NewRecorder()
	h.Transfer(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("transfer: got %d, want 200 (%s)", rr.Code, rr.Body)
	}
	if gotDir != "out" {
		t.Fatalf("direction passed to repo = %q, want out", gotDir)
	}
	var fund map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &fund); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
}
