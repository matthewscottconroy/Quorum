package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"quorum/internal/auth"
	"quorum/internal/model"
	"quorum/internal/repo"
)

// fundsRepo is satisfied by *repo.FundsRepo.
type fundsRepo interface {
	CreateFund(ctx context.Context, name string, purpose *string, approvalsRequired int, signerIDs []string, createdBy string) (*model.Fund, error)
	ListFunds(ctx context.Context) ([]model.Fund, error)
	GetFund(ctx context.Context, id string) (*model.Fund, error)
	UpdatePolicy(ctx context.Context, id string, purpose *string, approvalsRequired *int, signerIDs *[]string) (int, error)
	Transfer(ctx context.Context, fundID, direction string, amount int64, currency, memo string) error
}

// purchasesRepo is satisfied by *repo.PurchasesRepo.
type purchasesRepo interface {
	Create(ctx context.Context, p *model.PurchaseRequest, requester string) (*model.PurchaseRequest, error)
	Get(ctx context.Context, id string) (*model.PurchaseRequest, error)
	List(ctx context.Context, fundID, status string, limit int) ([]model.PurchaseRequest, error)
	IsSigner(ctx context.Context, fundID, userID string) (bool, error)
	Approve(ctx context.Context, requestID, approverID, ip string) (*model.PurchaseRequest, error)
	SetTerminal(ctx context.Context, requestID, newStatus string) error
	Complete(ctx context.Context, requestID string) (*model.PurchaseRequest, error)
}

// passwordChecker is the slice of the auth repo the signing re-auth needs.
type passwordChecker interface {
	GetPasswordHash(ctx context.Context, id string) (string, error)
}

// FundsHandler serves purpose-restricted funds and the multi-signature
// purchase workflow (Phase B). Reads are member-visible (org transparency);
// filing purchases and completing them are officer acts; fund management
// and treasury transfers are admin acts. Approving requires the approver's
// password again — each signature is a deliberate, recorded act.
type FundsHandler struct {
	funds     fundsRepo
	purchases purchasesRepo
	users     passwordChecker
	resources resourcesRepo
	clientIP  func(*http.Request) string
}

// NewFundsHandler constructs the handler.
func NewFundsHandler(f fundsRepo, p purchasesRepo, u passwordChecker, res resourcesRepo, ip func(*http.Request) string) *FundsHandler {
	return &FundsHandler{funds: f, purchases: p, users: u, resources: res, clientIP: ip}
}

func (h *FundsHandler) ip(r *http.Request) string {
	if h.clientIP != nil {
		return h.clientIP(r)
	}
	return r.RemoteAddr
}

// ListFunds returns all funds with balances and policies (member+).
func (h *FundsHandler) ListFunds(w http.ResponseWriter, r *http.Request) {
	funds, err := h.funds.ListFunds(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if funds == nil {
		funds = []model.Fund{}
	}
	writeJSON(w, http.StatusOK, funds)
}

// CreateFund (admin): name, purpose, approvals_required, signer user ids.
func (h *FundsHandler) CreateFund(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name              string   `json:"name"`
		Purpose           *string  `json:"purpose"`
		ApprovalsRequired int      `json:"approvals_required"`
		SignerIDs         []string `json:"signer_ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 80 {
		writeError(w, http.StatusBadRequest, "name is required (at most 80 characters)", "bad_request")
		return
	}
	if body.ApprovalsRequired < 1 || body.ApprovalsRequired > 10 {
		writeError(w, http.StatusBadRequest, "approvals_required must be 1-10", "bad_request")
		return
	}
	for _, id := range body.SignerIDs {
		if !isValidUUID(id) {
			writeError(w, http.StatusBadRequest, "signer_ids must be UUIDs", "bad_request")
			return
		}
	}
	created, err := h.funds.CreateFund(r.Context(), body.Name, body.Purpose, body.ApprovalsRequired, body.SignerIDs, userIDFromCtx(r))
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"name": created.Name, "approvals_required": created.ApprovalsRequired, "signers": len(created.Signers)})
	writeJSON(w, http.StatusCreated, created)
}

// UpdateFund (admin): policy changes void in-flight approvals.
func (h *FundsHandler) UpdateFund(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Purpose           *string   `json:"purpose"`
		ApprovalsRequired *int      `json:"approvals_required"`
		SignerIDs         *[]string `json:"signer_ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if body.ApprovalsRequired != nil && (*body.ApprovalsRequired < 1 || *body.ApprovalsRequired > 10) {
		writeError(w, http.StatusBadRequest, "approvals_required must be 1-10", "bad_request")
		return
	}
	if body.SignerIDs != nil {
		for _, sid := range *body.SignerIDs {
			if !isValidUUID(sid) {
				writeError(w, http.StatusBadRequest, "signer_ids must be UUIDs", "bad_request")
				return
			}
		}
	}
	voided, err := h.funds.UpdatePolicy(r.Context(), id, body.Purpose, body.ApprovalsRequired, body.SignerIDs)
	if err != nil {
		writeRepoError(w, err, "fund not found", "update error")
		return
	}
	updated, err := h.funds.GetFund(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	setAuditDetail(r, map[string]any{"name": updated.Name, "approvals_voided": voided})
	writeJSON(w, http.StatusOK, map[string]any{"fund": updated, "approvals_voided": voided})
}

// Transfer (admin): move money between operating cash and the fund.
func (h *FundsHandler) Transfer(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Direction   string `json:"direction"`
		AmountMinor int64  `json:"amount_minor"`
		Currency    string `json:"currency"`
		Memo        string `json:"memo"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if body.Direction != "in" && body.Direction != "out" {
		writeError(w, http.StatusBadRequest, "direction must be in or out", "bad_request")
		return
	}
	if body.AmountMinor <= 0 || len(body.Currency) != 3 {
		writeError(w, http.StatusBadRequest, "amount_minor (positive) and 3-letter currency required", "bad_request")
		return
	}
	memo := strings.TrimSpace(body.Memo)
	if memo == "" {
		memo = "Fund transfer " + body.Direction
	}
	fund, err := h.funds.GetFund(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "fund not found", "not_found")
		return
	}
	if err := h.funds.Transfer(r.Context(), id, body.Direction, body.AmountMinor, strings.ToUpper(body.Currency), memo); err != nil {
		if errors.Is(err, repo.ErrInsufficientFunds) {
			writeError(w, http.StatusConflict, err.Error(), "conflict")
			return
		}
		writeRepoError(w, err, "fund not found", "transfer error")
		return
	}
	setAuditDetail(r, map[string]any{"fund": fund.Name, "direction": body.Direction, "amount_minor": body.AmountMinor, "currency": strings.ToUpper(body.Currency)})
	updated, _ := h.funds.GetFund(r.Context(), id)
	writeJSON(w, http.StatusOK, updated)
}

// ListPurchases (member+): ?fund_id=&status=
func (h *FundsHandler) ListPurchases(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fundID := q.Get("fund_id")
	if fundID != "" && !isValidUUID(fundID) {
		writeError(w, http.StatusBadRequest, "fund_id must be a UUID", "bad_request")
		return
	}
	status := q.Get("status")
	list, err := h.purchases.List(r.Context(), fundID, status, clampLimit(q.Get("limit"), 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if list == nil {
		list = []model.PurchaseRequest{}
	}
	writeJSON(w, http.StatusOK, list)
}

// CreatePurchase (officer+): files a pending request.
func (h *FundsHandler) CreatePurchase(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FundID           string  `json:"fund_id"`
		AmountMinor      int64   `json:"amount_minor"`
		Currency         string  `json:"currency"`
		Payee            string  `json:"payee"`
		Memo             *string `json:"memo"`
		ResourceID       *string `json:"resource_id"`
		ExpenseAccountID *string `json:"expense_account_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	body.Payee = strings.TrimSpace(body.Payee)
	if !isValidUUID(body.FundID) || body.AmountMinor <= 0 || len(body.Currency) != 3 ||
		body.Payee == "" || len(body.Payee) > 120 {
		writeError(w, http.StatusBadRequest,
			"fund_id, positive amount_minor, 3-letter currency, and payee are required", "bad_request")
		return
	}
	if body.ResourceID != nil {
		if !isValidUUID(*body.ResourceID) {
			writeError(w, http.StatusBadRequest, "resource_id must be a UUID", "bad_request")
			return
		}
		if _, err := h.resources.GetVisible(r.Context(), *body.ResourceID,
			roleAtLeast(roleFromCtx(r), "officer"), memberIDFromCtx(r), roleRank[roleFromCtx(r)]); err != nil {
			writeError(w, http.StatusNotFound, "supporting document not found", "not_found")
			return
		}
	}
	if _, err := h.funds.GetFund(r.Context(), body.FundID); err != nil {
		writeError(w, http.StatusNotFound, "fund not found", "not_found")
		return
	}
	if body.ExpenseAccountID != nil && !isValidUUID(*body.ExpenseAccountID) {
		writeError(w, http.StatusBadRequest, "expense_account_id must be a UUID", "bad_request")
		return
	}
	created, err := h.purchases.Create(r.Context(), &model.PurchaseRequest{
		FundID: body.FundID, Amount: body.AmountMinor, Currency: strings.ToUpper(body.Currency),
		Payee: body.Payee, Memo: body.Memo, ResourceID: body.ResourceID, ExpenseAccountID: body.ExpenseAccountID,
	}, userIDFromCtx(r))
	if err != nil {
		writeRepoError(w, err, "fund not found", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"fund": created.FundName, "payee": created.Payee, "amount_minor": created.Amount, "currency": created.Currency})
	writeJSON(w, http.StatusCreated, created)
}

// GetPurchase (member+).
func (h *FundsHandler) GetPurchase(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	p, err := h.purchases.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "purchase request not found", "not_found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// Approve (member+ if eligible): the signature. Requires the approver's
// password (a deliberate act), refuses self-approval, and only officers or
// the fund's named signers may sign.
func (h *FundsHandler) Approve(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Password == "" {
		writeError(w, http.StatusBadRequest, "your password is required to sign an approval", "bad_request")
		return
	}
	uid := userIDFromCtx(r)
	hash, err := h.users.GetPasswordHash(r.Context(), uid)
	if err != nil || !auth.CheckPassword(hash, body.Password) {
		writeError(w, http.StatusForbidden, "password confirmation failed", "forbidden")
		return
	}
	p, err := h.purchases.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "purchase request not found", "not_found")
		return
	}
	if p.RequesterID != nil && *p.RequesterID == uid {
		writeError(w, http.StatusForbidden, "the requester cannot approve their own purchase", "forbidden")
		return
	}
	if !roleAtLeast(roleFromCtx(r), "officer") {
		isSigner, err := h.purchases.IsSigner(r.Context(), p.FundID, uid)
		if err != nil || !isSigner {
			writeError(w, http.StatusForbidden, "only officers or the fund's named signers may approve", "forbidden")
			return
		}
	}
	updated, err := h.purchases.Approve(r.Context(), id, uid, h.ip(r))
	if errors.Is(err, repo.ErrNotApprovable) || errors.Is(err, repo.ErrAlreadyApproved) {
		writeError(w, http.StatusConflict, err.Error(), "conflict")
		return
	}
	if err != nil {
		writeRepoError(w, err, "purchase request not found", "approve error")
		return
	}
	setAuditDetail(r, map[string]any{
		"fund": updated.FundName, "payee": updated.Payee, "amount_minor": updated.Amount,
		"approvals": len(updated.Approvals), "now_approved": updated.Status == "approved", "ip": h.ip(r),
	})
	writeJSON(w, http.StatusOK, updated)
}

// Reject (officer+) and Cancel (requester or admin) end a request.
func (h *FundsHandler) Reject(w http.ResponseWriter, r *http.Request) {
	h.terminal(w, r, "rejected", func(p *model.PurchaseRequest) bool {
		return roleAtLeast(roleFromCtx(r), "officer")
	})
}

// Cancel withdraws a request (its requester, or an admin).
func (h *FundsHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	h.terminal(w, r, "cancelled", func(p *model.PurchaseRequest) bool {
		return (p.RequesterID != nil && *p.RequesterID == userIDFromCtx(r)) || roleAtLeast(roleFromCtx(r), "admin")
	})
}

func (h *FundsHandler) terminal(w http.ResponseWriter, r *http.Request, status string, allowed func(*model.PurchaseRequest) bool) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	p, err := h.purchases.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "purchase request not found", "not_found")
		return
	}
	if !allowed(p) {
		writeError(w, http.StatusForbidden, "not allowed for this request", "forbidden")
		return
	}
	if err := h.purchases.SetTerminal(r.Context(), id, status); err != nil {
		writeError(w, http.StatusConflict, "request is no longer open", "conflict")
		return
	}
	setAuditDetail(r, map[string]any{"fund": p.FundName, "payee": p.Payee, "status": status})
	w.WriteHeader(http.StatusNoContent)
}

// Complete (officer+): executes an approved purchase — refuses overdraft
// and posts the books in the same transaction.
func (h *FundsHandler) Complete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	updated, err := h.purchases.Complete(r.Context(), id)
	if errors.Is(err, repo.ErrInsufficientFunds) || errors.Is(err, repo.ErrNotApprovable) {
		writeError(w, http.StatusConflict, err.Error(), "conflict")
		return
	}
	if err != nil {
		writeRepoError(w, err, "purchase request not found", "complete error")
		return
	}
	setAuditDetail(r, map[string]any{
		"fund": updated.FundName, "payee": updated.Payee, "amount_minor": updated.Amount,
		"currency": updated.Currency, "journal_entry": updated.JournalEntryID,
	})
	writeJSON(w, http.StatusOK, updated)
}
