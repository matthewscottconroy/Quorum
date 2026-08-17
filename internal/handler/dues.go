package handler

import (
	"errors"
	"fmt"
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
	// The page's first question is "how much are we owed?" — answer it in
	// the same response, per currency, for the SAME filtered set.
	summary, err := h.repo.SummarizeInvoices(r.Context(), f)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	if invoices == nil {
		invoices = []model.DuesInvoice{}
	}
	if summary == nil {
		summary = []repo.InvoiceSummary{}
	}
	writeJSON(w, 200, map[string]any{
		"data": invoices, "total": total, "limit": f.Limit, "offset": f.Offset,
		"summary": summary,
	})
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
		// ContactID invoices an outside customer (services receivable)
		// instead of a member. Exactly one counterparty kind per request.
		ContactID string `json:"contact_id"`
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
	currency, curOK := validCurrencyCode(currency)
	if !curOK {
		writeError(w, 400, "currency must be a 3-8 letter code", "bad_request")
		return
	}
	dueDate, err := time.Parse("2006-01-02", body.DueDate)
	if err != nil {
		writeError(w, 400, "due_date must be YYYY-MM-DD", "bad_request")
		return
	}

	// Contact (outside customer) invoice: single, no bulk.
	if body.ContactID != "" {
		if body.MemberID != "" || len(body.MemberIDs) > 0 {
			writeError(w, 400, "an invoice has exactly one counterparty: member or contact", "bad_request")
			return
		}
		if !isValidUUID(body.ContactID) {
			writeError(w, 400, "contact_id must be a UUID", "bad_request")
			return
		}
		cid := body.ContactID
		created, err := h.repo.CreateInvoiceBatch(r.Context(), []*model.DuesInvoice{{
			ContactID: &cid, AmountMinor: body.AmountMinor, Currency: currency,
			PeriodLabel: body.PeriodLabel, DueDate: dueDate, Status: "pending", Notes: body.Notes,
		}})
		if err != nil {
			writeRepoError(w, err, "contact not found", "create error")
			return
		}
		writeJSON(w, 201, created[0])
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
	// 'paid' and 'partial' are computed from recorded transactions, never set
	// by hand — a manually-"paid" invoice would leave the receivable open in
	// the GL while vanishing from AR aging, and nothing would ever flag the
	// drift. Record a payment (or waive) instead.
	if *body.Status == "paid" || *body.Status == "partial" {
		writeError(w, 409, "paid and partial are computed from recorded payments: record a payment, or waive the invoice", "conflict")
		return
	}
	// Waiving a paid invoice is a no-op write-off of nothing; refuse it
	// loudly rather than freeze a settled invoice in 'waived'.
	if *body.Status == "waived" {
		cur, err := h.repo.GetInvoice(r.Context(), id)
		if err != nil {
			writeError(w, 404, "invoice not found", "not_found")
			return
		}
		if cur.Status == "paid" {
			writeError(w, 409, "invoice is fully paid: there is nothing to waive", "conflict")
			return
		}
	}
	if err := h.repo.UpdateInvoiceStatus(r.Context(), id, *body.Status, body.Notes); err != nil {
		writeRepoError(w, err, "invoice not found", "update error")
		return
	}
	// Whatever the officer asked for, the ledger has the last word: an
	// un-waived or re-opened invoice with covering payments snaps back to
	// paid, one with partial payments to partial.
	if *body.Status != "waived" {
		if err := h.repo.RecomputeInvoiceStatus(r.Context(), id); err != nil {
			log.Printf("recompute invoice %s after status update: %v", id, err)
		}
	}
	// Invoice identity is frozen in the database; the status transition is the
	// financially meaningful change, so put it in the chain-protected detail.
	setAuditDetail(r, map[string]any{"status_new": *body.Status})
	inv, err := h.repo.GetInvoice(r.Context(), id)
	if err != nil {
		writeError(w, 500, "fetch error", "internal_error")
		return
	}
	writeJSON(w, 200, inv)
}

// BatchUpdateStatus sets the same status on many invoices (officer+): bulk
// waive, or re-open. Body: {ids: [...], status: "..."}.
func (h *DuesHandler) BatchUpdateStatus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs    []string `json:"ids"`
		Status string   `json:"status"`
	}
	if err := decodeJSON(r, &body); err != nil || len(body.IDs) == 0 {
		writeError(w, 400, "ids (non-empty) required", "bad_request")
		return
	}
	if len(body.IDs) > 500 {
		writeError(w, 400, "at most 500 ids per batch", "bad_request")
		return
	}
	if !model.ValidInvoiceStatuses[body.Status] {
		writeError(w, 400, "invalid status", "bad_request")
		return
	}
	if body.Status == "paid" || body.Status == "partial" {
		writeError(w, 409, "paid and partial are computed from recorded payments: record payments, or waive", "conflict")
		return
	}
	for _, id := range body.IDs {
		if !isValidUUID(id) {
			writeError(w, 400, "ids must be UUIDs", "bad_request")
			return
		}
	}
	n, err := h.repo.BatchUpdateStatus(r.Context(), body.IDs, body.Status)
	if err != nil {
		writeError(w, 500, "update error", "internal_error")
		return
	}
	setAuditDetail(r, map[string]any{"count": n, "status_new": body.Status})
	writeJSON(w, 200, map[string]any{"updated": n})
}

// GetInstallments returns an invoice's payment-plan schedule (officer+).
func (h *DuesHandler) GetInstallments(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	plan, err := h.repo.ListInstallments(r.Context(), id)
	if err != nil {
		writeError(w, 500, "query error", "internal_error")
		return
	}
	writeJSON(w, 200, plan)
}

// SetInstallments replaces an invoice's payment plan (officer+).
// Body: {installments: [{amount_minor, due_date}]}. An empty list clears it.
func (h *DuesHandler) SetInstallments(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Installments []struct {
			AmountMinor int64  `json:"amount_minor"`
			DueDate     string `json:"due_date"`
		} `json:"installments"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "invalid body", "bad_request")
		return
	}
	if len(body.Installments) > 60 {
		writeError(w, 400, "at most 60 installments", "bad_request")
		return
	}
	plan := make([]model.InvoiceInstallment, 0, len(body.Installments))
	for _, in := range body.Installments {
		d, err := time.Parse("2006-01-02", in.DueDate)
		if err != nil || in.AmountMinor <= 0 {
			writeError(w, 400, "each installment needs a positive amount_minor and a YYYY-MM-DD due_date", "bad_request")
			return
		}
		plan = append(plan, model.InvoiceInstallment{AmountMinor: in.AmountMinor, DueDate: d})
	}
	if err := h.repo.SetInstallments(r.Context(), id, plan); err != nil {
		writeRepoError(w, err, "invoice not found", "update error")
		return
	}
	setAuditDetail(r, map[string]any{"invoice": id, "installments": len(plan)})
	out, _ := h.repo.ListInstallments(r.Context(), id)
	// The plan is tracking-only, but a plan that doesn't sum to the invoice
	// silently misleads the member — report the delta so the UI can warn.
	var planTotal int64
	for _, p := range plan {
		planTotal += p.AmountMinor
	}
	var deltaMinor *int64
	if len(plan) > 0 {
		if inv, err := h.repo.GetInvoice(r.Context(), id); err == nil {
			if d := planTotal - inv.AmountMinor; d != 0 {
				deltaMinor = &d
			}
		}
	}
	writeJSON(w, 200, map[string]any{"installments": out, "delta_minor": deltaMinor})
}

// RecordRefund records a refund against an invoice (officer+): a negative
// transaction that the GL trigger posts as a reversing entry (DR income /
// CR cash) and that lowers the invoice's paid total. Body amount_minor is the
// positive refund amount; it is stored negative.
func (h *DuesHandler) RecordRefund(w http.ResponseWriter, r *http.Request) {
	invoiceID, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	userID := userIDFromCtx(r)
	var body struct {
		AmountMinor int64   `json:"amount_minor"`
		Currency    string  `json:"currency"`
		Provider    string  `json:"provider"`
		Note        *string `json:"note"`
	}
	if err := decodeJSON(r, &body); err != nil || body.AmountMinor <= 0 || body.Provider == "" {
		writeError(w, 400, "positive amount_minor and provider required", "bad_request")
		return
	}
	inv, err := h.repo.GetInvoice(r.Context(), invoiceID)
	if err != nil {
		writeError(w, 404, "invoice not found", "not_found")
		return
	}
	currency := body.Currency
	if currency == "" {
		currency = inv.Currency
	}
	if !strings.EqualFold(currency, inv.Currency) {
		writeError(w, 400, "refund currency must match the invoice currency", "bad_request")
		return
	}
	status := "refunded"
	var memberPtr *string
	if inv.MemberID != "" {
		memberPtr = &inv.MemberID
	}
	// The waived check and the refund cap both run inside the repo under the
	// invoice row lock — the unlocked read-then-write this replaced let two
	// concurrent full refunds each pass the cap and mint money out of A/R.
	tx, limit, err := h.repo.CreateGuardedTransaction(r.Context(), &model.Transaction{
		InvoiceID:      &invoiceID,
		MemberID:       memberPtr,
		AmountMinor:    -body.AmountMinor, // negative → reversing GL entry
		Currency:       inv.Currency,      // canonical casing: recompute matches exactly
		Provider:       body.Provider,
		ProviderStatus: &status,
		RecordedBy:     &userID,
		OccurredAt:     time.Now(),
		Notes:          body.Note,
	}, false)
	if errors.Is(err, repo.ErrExceedsPaid) {
		writeError(w, 409, fmt.Sprintf("refund exceeds the amount actually paid: at most %d minor units are refundable", limit), "conflict")
		return
	}
	if errors.Is(err, repo.ErrInvoiceNotPayable) {
		writeError(w, 409, "invoice is waived: un-waive it before recording a refund", "conflict")
		return
	}
	if err != nil {
		writeRepoError(w, err, "", "refund error")
		return
	}
	setAuditDetail(r, map[string]any{"invoice": invoiceID, "refund_minor": body.AmountMinor, "currency": currency})
	writeJSON(w, 201, tx)
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
		// AllowOverpayment acknowledges a payment beyond the invoice balance
		// (a genuine overpay/donation) instead of a typo'd extra zero.
		AllowOverpayment bool `json:"allow_overpayment"`
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

	// Contact (outside-customer) invoices have no member; GetInvoice coalesces
	// their member_id to "". Pass nil so the transaction's member_id is NULL
	// rather than failing the uuid cast.
	var memberPtr *string
	if inv.MemberID != "" {
		memberPtr = &inv.MemberID
	}

	// The waived check and the fat-fingered-extra-zero guard both run inside
	// the repo under the invoice row lock, and the recompute commits with the
	// insert — no window for a webhook to slip a payment between our read
	// and our write.
	tx, limit, err := h.repo.CreateGuardedTransaction(r.Context(), &model.Transaction{
		InvoiceID:           &invoiceID,
		MemberID:            memberPtr,
		AmountMinor:         body.AmountMinor,
		Currency:            inv.Currency, // canonical casing: recompute matches exactly
		Provider:            body.Provider,
		ProviderReferenceID: body.ProviderReference,
		ProviderStatus:      providerStr,
		PaymentMethodType:   body.PaymentMethodType,
		RecordedBy:          &userID,
		OccurredAt:          time.Now(),
		Notes:               body.Notes,
	}, body.AllowOverpayment)
	if errors.Is(err, repo.ErrExceedsRemaining) {
		writeError(w, 409,
			fmt.Sprintf("payment exceeds the remaining balance (%d minor units): resend with allow_overpayment if intentional", limit),
			"conflict")
		return
	}
	if errors.Is(err, repo.ErrInvoiceNotPayable) {
		writeError(w, 409, "invoice is waived: un-waive it before recording a payment", "conflict")
		return
	}
	if err != nil {
		writeRepoError(w, err, "", "create error")
		return
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
	writePage(w, txs, total, f.Limit, f.Offset)
}
