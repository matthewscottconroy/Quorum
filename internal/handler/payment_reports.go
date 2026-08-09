package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
)

// paymentReportsRepo is satisfied by *repo.PaymentReportsRepo.
type paymentReportsRepo interface {
	Create(ctx context.Context, invoiceID, memberID, method, reference, note string) (*model.PaymentReport, error)
	ListPending(ctx context.Context, limit int) ([]model.PaymentReport, error)
	PendingCount(ctx context.Context) (int, error)
	Resolve(ctx context.Context, id, status, resolvedBy string) (invoiceID string, err error)
}

// prDuesRepo is the slice of the dues repo the confirm step needs.
type prDuesRepo interface {
	GetInvoice(ctx context.Context, id string) (*model.DuesInvoice, error)
	CreateTransaction(ctx context.Context, t *model.Transaction) (*model.Transaction, error)
	RecomputeInvoiceStatus(ctx context.Context, id string) error
}

// PaymentReportsHandler runs the member-reported-payment flow: a member says
// "I paid via Zelle/check", officers see a queue, and confirming records the
// real transaction against the invoice.
type PaymentReportsHandler struct {
	repo paymentReportsRepo
	dues prDuesRepo
}

// NewPaymentReportsHandler constructs the handler.
func NewPaymentReportsHandler(r paymentReportsRepo, d prDuesRepo) *PaymentReportsHandler {
	return &PaymentReportsHandler{repo: r, dues: d}
}

// Create files a self-reported payment for one of the caller's invoices.
func (h *PaymentReportsHandler) Create(w http.ResponseWriter, r *http.Request) {
	invoiceID, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Method    string `json:"method"`
		Reference string `json:"reference"`
		Note      string `json:"note"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	body.Method = strings.TrimSpace(body.Method)
	if body.Method == "" || len(body.Method) > 40 || len(body.Reference) > 120 || len(body.Note) > 500 {
		writeError(w, http.StatusBadRequest, "method is required (payment method you used)", "bad_request")
		return
	}
	// Members may only report against their own invoice.
	inv, err := h.dues.GetInvoice(r.Context(), invoiceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "invoice not found", "not_found")
		return
	}
	memberID := memberIDFromCtx(r)
	if !roleAtLeast(roleFromCtx(r), "officer") && inv.MemberID != memberID {
		writeError(w, http.StatusForbidden, "you can only report payment on your own invoice", "forbidden")
		return
	}
	rep, err := h.repo.Create(r.Context(), invoiceID, memberID, body.Method, body.Reference, body.Note)
	if err != nil {
		// The partial unique index rejects a second open report per invoice.
		if strings.Contains(err.Error(), "idx_payment_reports_one_open") {
			writeError(w, http.StatusConflict, "a payment report is already pending for this invoice", "conflict")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not file report", "internal_error")
		return
	}
	writeJSON(w, http.StatusCreated, rep)
}

// ListPending returns the officer confirmation queue.
func (h *PaymentReportsHandler) ListPending(w http.ResponseWriter, r *http.Request) {
	list, err := h.repo.ListPending(r.Context(), clampLimit(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if list == nil {
		list = []model.PaymentReport{}
	}
	writeJSON(w, http.StatusOK, list)
}

// Confirm (officer) records the real payment and closes the report.
func (h *PaymentReportsHandler) Confirm(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	invoiceID, err := h.repo.Resolve(r.Context(), id, "confirmed", userIDFromCtx(r))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "report is no longer pending", "conflict")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "confirm error", "internal_error")
		return
	}
	inv, err := h.dues.GetInvoice(r.Context(), invoiceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invoice lookup error", "internal_error")
		return
	}
	if inv.Status == "waived" {
		// Confirmed but not posted (posting a waived invoice corrupts A/R).
		writeJSON(w, http.StatusOK, map[string]any{"confirmed": true, "posted": false, "reason": "invoice is waived"})
		return
	}
	uid := userIDFromCtx(r)
	var memberPtr *string
	if inv.MemberID != "" {
		memberPtr = &inv.MemberID
	}
	status := "succeeded"
	if _, err := h.dues.CreateTransaction(r.Context(), &model.Transaction{
		InvoiceID: &invoiceID, MemberID: memberPtr, AmountMinor: inv.AmountMinor, Currency: inv.Currency,
		Provider: "reported", ProviderStatus: &status, RecordedBy: &uid, OccurredAt: time.Now(),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "could not record payment", "internal_error")
		return
	}
	if err := h.dues.RecomputeInvoiceStatus(r.Context(), invoiceID); err != nil {
		// Non-fatal: the transaction is recorded.
		_ = err
	}
	setAuditDetail(r, map[string]any{"invoice": invoiceID, "amount_minor": inv.AmountMinor, "currency": inv.Currency})
	writeJSON(w, http.StatusOK, map[string]any{"confirmed": true, "posted": true})
}

// Dismiss (officer) rejects the report without recording a payment.
func (h *PaymentReportsHandler) Dismiss(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if _, err := h.repo.Resolve(r.Context(), id, "dismissed", userIDFromCtx(r)); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "report is no longer pending", "conflict")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "dismiss error", "internal_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
