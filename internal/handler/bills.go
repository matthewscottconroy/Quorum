package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// billsRepo is satisfied by *repo.BillsRepo.
type billsRepo interface {
	Create(ctx context.Context, b *model.Bill, createdBy string) (*model.Bill, error)
	Get(ctx context.Context, id string) (*model.Bill, error)
	List(ctx context.Context, status string, limit int) ([]model.Bill, error)
	Pay(ctx context.Context, id, fundID, provider string) (*model.Bill, error)
	Void(ctx context.Context, id string) (*model.Bill, error)
	APAging(ctx context.Context, asOf string) ([]model.ARAgingRow, error)
}

// BillsHandler serves accounts payable (officer+; void is admin).
type BillsHandler struct {
	repo billsRepo
}

// NewBillsHandler constructs the handler.
func NewBillsHandler(r billsRepo) *BillsHandler {
	return &BillsHandler{repo: r}
}

// List returns bills (?status=open|paid|void).
func (h *BillsHandler) List(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != "open" && status != "paid" && status != "void" {
		writeError(w, 400, "status must be open, paid, or void", "bad_request")
		return
	}
	bills, err := h.repo.List(r.Context(), status, clampLimit(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	if bills == nil {
		bills = []model.Bill{}
	}
	writeJSON(w, 200, bills)
}

// Create records a vendor bill and accrues it on the books.
func (h *BillsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ContactID        string  `json:"contact_id"`
		AmountMinor      int64   `json:"amount_minor"`
		Currency         string  `json:"currency"`
		Memo             *string `json:"memo"`
		ExpenseAccountID string  `json:"expense_account_id"`
		BillDate         string  `json:"bill_date"`
		DueDate          string  `json:"due_date"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	body.Currency = strings.ToUpper(strings.TrimSpace(body.Currency))
	if !isValidUUID(body.ContactID) || !isValidUUID(body.ExpenseAccountID) ||
		body.AmountMinor <= 0 || len(body.Currency) != 3 {
		writeError(w, 400, "contact_id, expense_account_id, positive amount_minor, and 3-letter currency required", "bad_request")
		return
	}
	for _, d := range []string{body.BillDate, body.DueDate} {
		if d != "" && !validISODate(d) {
			writeError(w, 400, "dates must be YYYY-MM-DD", "bad_request")
			return
		}
	}
	created, err := h.repo.Create(r.Context(), &model.Bill{
		ContactID: body.ContactID, Amount: body.AmountMinor, Currency: body.Currency,
		Memo: body.Memo, ExpenseAccountID: body.ExpenseAccountID,
		BillDateStr: body.BillDate, DueDateStr: body.DueDate,
	}, userIDFromCtx(r))
	if err != nil {
		writeRepoError(w, err, "contact or account not found", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"vendor": created.ContactName, "amount_minor": created.Amount, "currency": created.Currency})
	writeJSON(w, 201, created)
}

// Pay settles an open bill from a fund or the provider-routed cash account.
func (h *BillsHandler) Pay(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		FundID   string `json:"fund_id"`
		Provider string `json:"provider"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	if body.FundID != "" && !isValidUUID(body.FundID) {
		writeError(w, 400, "fund_id must be a UUID", "bad_request")
		return
	}
	paid, err := h.repo.Pay(r.Context(), id, body.FundID, strings.ToLower(strings.TrimSpace(body.Provider)))
	if errors.Is(err, repo.ErrInsufficientFunds) || errors.Is(err, repo.ErrNotApprovable) {
		writeError(w, 409, err.Error(), "conflict")
		return
	}
	if err != nil {
		writeRepoError(w, err, "bill not found", "pay error")
		return
	}
	setAuditDetail(r, map[string]any{"vendor": paid.ContactName, "amount_minor": paid.Amount, "currency": paid.Currency, "fund": body.FundID != ""})
	writeJSON(w, 200, paid)
}

// Void reverses an open bill (admin).
func (h *BillsHandler) Void(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	voided, err := h.repo.Void(r.Context(), id)
	if errors.Is(err, repo.ErrNotApprovable) {
		writeError(w, 409, err.Error(), "conflict")
		return
	}
	if err != nil {
		writeRepoError(w, err, "bill not found", "void error")
		return
	}
	setAuditDetail(r, map[string]any{"vendor": voided.ContactName, "amount_minor": voided.Amount})
	writeJSON(w, 200, voided)
}
