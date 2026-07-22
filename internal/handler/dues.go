package handler

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// DuesHandler handles dues invoice and transaction endpoints.
type DuesHandler struct {
	repo duesRepo
}

// NewDuesHandler constructs a DuesHandler.
func NewDuesHandler(r duesRepo) *DuesHandler {
	return &DuesHandler{repo: r}
}

// List handles GET requests for a paginated, filterable list of invoices.
func (h *DuesHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if v := q.Get("member_id"); v != "" && !isValidUUID(v) {
		writeError(w, 400, "member_id must be a UUID", "bad_request")
		return
	}
	f := repo.InvoiceFilter{
		MemberID:    q.Get("member_id"),
		Status:      q.Get("status"),
		PeriodLabel: q.Get("period"),
		Limit:       100,
	}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= maxPageSize {
		f.Limit = v
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		f.Offset = v
	}
	invoices, total, err := h.repo.ListInvoices(r.Context(), f)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	if invoices == nil {
		invoices = []model.DuesInvoice{}
	}
	writeJSON(w, 200, model.Page[model.DuesInvoice]{Data: invoices, Total: total, Limit: f.Limit, Offset: f.Offset})
}

// Create handles creating a invoice.
func (h *DuesHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		// Single invoice
		MemberID string `json:"member_id"`
		// AmountMinor is the amount in the currency's minor units (e.g. cents).
		AmountMinor int64   `json:"amount_minor"`
		Currency    string  `json:"currency"`
		PeriodLabel string  `json:"period_label"`
		DueDate     string  `json:"due_date"`
		Notes       *string `json:"notes"`
		// Bulk: list of member_ids + shared amount/period/due_date
		MemberIDs []string `json:"member_ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	if body.PeriodLabel == "" || body.DueDate == "" || body.AmountMinor <= 0 {
		writeError(w, 400, "amount_minor (>0), period_label, and due_date are required", "bad_request")
		return
	}
	currency := body.Currency
	if currency == "" {
		currency = "USD"
	}
	dueDate, err := time.Parse("2006-01-02", body.DueDate)
	if err != nil {
		writeError(w, 400, "due_date must be YYYY-MM-DD", "bad_request")
		return
	}

	// Collect member IDs for bulk or single.
	memberIDs := body.MemberIDs
	if body.MemberID != "" {
		memberIDs = append(memberIDs, body.MemberID)
	}
	if len(memberIDs) == 0 {
		writeError(w, 400, "member_id or member_ids required", "bad_request")
		return
	}
	if len(memberIDs) > 100 {
		writeError(w, 400, "member_ids must not exceed 100 entries", "bad_request")
		return
	}
	for _, mid := range memberIDs {
		if !isValidUUID(mid) {
			writeError(w, 400, "member ids must be UUIDs", "bad_request")
			return
		}
	}

	invs := make([]*model.DuesInvoice, len(memberIDs))
	for i, mid := range memberIDs {
		invs[i] = &model.DuesInvoice{
			MemberID:    mid,
			AmountMinor: body.AmountMinor,
			Currency:    currency,
			PeriodLabel: body.PeriodLabel,
			DueDate:     dueDate,
			Status:      "pending",
			Notes:       body.Notes,
		}
	}

	created, err := h.repo.CreateInvoiceBatch(r.Context(), invs)
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}

	if len(created) == 1 {
		writeJSON(w, 201, created[0])
	} else {
		writeJSON(w, 201, created)
	}
}

// Get handles fetching a single invoice by id.
func (h *DuesHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	inv, err := h.repo.GetInvoice(r.Context(), id)
	if err != nil {
		writeError(w, 404, "invoice not found", "not_found")
		return
	}
	writeJSON(w, 200, inv)
}

// Update handles updating a invoice.
func (h *DuesHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Status *string `json:"status"`
		Notes  *string `json:"notes"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Status == nil {
		writeError(w, 400, "status is required", "bad_request")
		return
	}
	if !model.ValidInvoiceStatuses[*body.Status] {
		writeError(w, 400, "invalid status", "bad_request")
		return
	}
	if err := h.repo.UpdateInvoiceStatus(r.Context(), id, *body.Status, body.Notes); err != nil {
		writeRepoError(w, err, "invoice not found", "update error")
		return
	}
	inv, err := h.repo.GetInvoice(r.Context(), id)
	if err != nil {
		writeError(w, 500, "fetch error", "internal_error")
		return
	}
	writeJSON(w, 200, inv)
}

// CreateTransaction records a manual payment against an invoice.
func (h *DuesHandler) CreateTransaction(w http.ResponseWriter, r *http.Request) {
	invoiceID, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	userID := userIDFromCtx(r)

	var body struct {
		AmountMinor       int64   `json:"amount_minor"`
		Currency          string  `json:"currency"`
		Provider          string  `json:"provider"`
		ProviderReference *string `json:"provider_reference_id"`
		PaymentMethodType *string `json:"payment_method_type"`
		Notes             *string `json:"notes"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	if body.AmountMinor <= 0 || body.Provider == "" {
		writeError(w, 400, "amount_minor (>0) and provider required", "bad_request")
		return
	}

	// Get invoice to obtain member_id.
	inv, err := h.repo.GetInvoice(r.Context(), invoiceID)
	if err != nil {
		writeError(w, 404, "invoice not found", "not_found")
		return
	}

	currency := body.Currency
	if currency == "" {
		currency = inv.Currency
	}
	// A payment in a different currency can't be summed against the invoice
	// (the recompute only counts matching-currency transactions), so reject it
	// rather than silently record a payment that never affects the balance.
	if !strings.EqualFold(currency, inv.Currency) {
		writeError(w, 400, "transaction currency must match the invoice currency", "bad_request")
		return
	}
	status := "succeeded"
	providerStr := &status

	tx, err := h.repo.CreateTransaction(r.Context(), &model.Transaction{
		InvoiceID:           &invoiceID,
		MemberID:            &inv.MemberID,
		AmountMinor:         body.AmountMinor,
		Currency:            currency,
		Provider:            body.Provider,
		ProviderReferenceID: body.ProviderReference,
		ProviderStatus:      providerStr,
		PaymentMethodType:   body.PaymentMethodType,
		RecordedBy:          &userID,
		OccurredAt:          time.Now(),
		Notes:               body.Notes,
	})
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}

	if err := h.repo.RecomputeInvoiceStatus(r.Context(), invoiceID); err != nil {
		log.Printf("recompute invoice %s: %v", invoiceID, err)
	}

	writeJSON(w, 201, tx)
}

// ListTransactions handles listing payment transactions.
func (h *DuesHandler) ListTransactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for _, p := range []string{"invoice_id", "member_id"} {
		if v := q.Get(p); v != "" && !isValidUUID(v) {
			writeError(w, 400, p+" must be a UUID", "bad_request")
			return
		}
	}
	f := repo.TransactionFilter{
		InvoiceID: q.Get("invoice_id"),
		MemberID:  q.Get("member_id"),
		Limit:     50,
	}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= maxPageSize {
		f.Limit = v
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		f.Offset = v
	}
	txs, total, err := h.repo.ListTransactions(r.Context(), f)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	if txs == nil {
		txs = []model.Transaction{}
	}
	writeJSON(w, 200, model.Page[model.Transaction]{Data: txs, Total: total, Limit: f.Limit, Offset: f.Offset})
}
