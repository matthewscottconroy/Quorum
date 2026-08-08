package handler

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"quorum/internal/model"
)

// glRepo is satisfied by *repo.GLRepo.
type glRepo interface {
	TrialBalance(ctx context.Context) ([]model.GLBalance, error)
	Reconcile(ctx context.Context) ([]model.GLReconcileRow, error)
	RecentEntries(ctx context.Context, limit int) ([]model.GLEntry, error)
}

// AccountingHandler serves the read-only face of the general ledger
// (Phase A). Officer+: the same bar as the rest of the financial surface.
type AccountingHandler struct {
	repo glRepo
	c    glRepoC
	ap   apAgingSource
}

// apAgingSource is the payables-aging slice of the bills repo.
type apAgingSource interface {
	APAging(ctx context.Context, asOf string) ([]model.ARAgingRow, error)
}

// SetAPAging wires the payables aging source.
func (h *AccountingHandler) SetAPAging(src apAgingSource) { h.ap = src }

// NewAccountingHandler constructs the handler.
func NewAccountingHandler(r glRepo) *AccountingHandler {
	return &AccountingHandler{repo: r}
}

// TrialBalance returns per-account balances, the AR reconciliation check,
// and the most recent postings.
func (h *AccountingHandler) TrialBalance(w http.ResponseWriter, r *http.Request) {
	tb, err := h.repo.TrialBalance(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	rec, err := h.repo.Reconcile(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	entries, err := h.repo.RecentEntries(r.Context(), clampLimit(r.URL.Query().Get("limit"), 25))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if tb == nil {
		tb = []model.GLBalance{}
	}
	if entries == nil {
		entries = []model.GLEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts":   tb,
		"reconciled": len(rec) == 0,
		"mismatches": rec,
		"recent":     entries,
	})
}

// glRepoC is the Phase C surface of *repo.GLRepo.
type glRepoC interface {
	Periods(ctx context.Context) ([]model.AccountingPeriod, error)
	ClosePeriod(ctx context.Context, month, closedBy string) error
	ReopenPeriod(ctx context.Context, month string) error
	ManualEntry(ctx context.Context, entryDate, memo, createdBy string, lines []model.GLLineInput) (string, error)
	Statement(ctx context.Context, from, to string, types []string) ([]model.GLBalance, error)
	ARAging(ctx context.Context, asOf string) ([]model.ARAgingRow, error)
	Accounts(ctx context.Context) ([]model.GLAccount, error)
	CreateAccount(ctx context.Context, code, name, typ string) error
	UpdateAccount(ctx context.Context, id string, name *string, active *bool) error
	LedgerCSVRows(ctx context.Context, from, to string) ([][]string, error)
	PostingRules(ctx context.Context) ([]model.PostingRule, error)
	SetPostingRule(ctx context.Context, key, accountCode, updatedBy string) error
	StatementCash(ctx context.Context, from, to string) ([]model.GLBalance, error)
	PurchasesCSVRows(ctx context.Context, from, to string) ([][]string, error)
}

var ruleKeyRe = regexp.MustCompile(`^(receivable|income\.dues|income\.unlinked|writeoff|cash\.operating|expense\.default|cash\.provider\.[a-z0-9_]{1,32})$`)

// PostingRules lists the automatic-posting mappings (officer+).
func (h *AccountingHandler) PostingRules(w http.ResponseWriter, r *http.Request) {
	rules, err := h.c.PostingRules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

// SetPostingRules updates mappings (admin): {key: account_code, ...}.
// Applies to FUTURE postings only; history is immutable.
func (h *AccountingHandler) SetPostingRules(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := decodeJSON(r, &body); err != nil || len(body) == 0 {
		writeError(w, http.StatusBadRequest, "a {rule_key: account_code} object is required", "bad_request")
		return
	}
	for k, code := range body {
		if !ruleKeyRe.MatchString(k) || !acctCodeRe.MatchString(code) {
			writeError(w, http.StatusBadRequest, "invalid rule key or account code: "+k, "bad_request")
			return
		}
	}
	for k, code := range body {
		if err := h.c.SetPostingRule(r.Context(), k, code, userIDFromCtx(r)); err != nil {
			writeError(w, http.StatusBadRequest, "unknown account code for "+k, "bad_request")
			return
		}
	}
	setAuditDetail(r, map[string]any{"rules": len(body)})
	h.PostingRules(w, r)
}

// SetPhaseC wires the Phase C repo surface (same concrete repo).
func (h *AccountingHandler) SetPhaseC(r glRepoC) { h.c = r }

func validISODate(s string) bool { _, err := time.Parse("2006-01-02", s); return err == nil }

// Statements returns the income statement for [from,to] plus the balance
// sheet as of `to`, per currency, org-agnostic (any range, any calendar).
func (h *AccountingHandler) Statements(w http.ResponseWriter, r *http.Request) {
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if !validISODate(from) || !validISODate(to) {
		writeError(w, http.StatusBadRequest, "from and to (YYYY-MM-DD) required", "bad_request")
		return
	}
	basis := r.URL.Query().Get("basis")
	var income []model.GLBalance
	var err error
	if basis == "cash" {
		income, err = h.c.StatementCash(r.Context(), from, to)
	} else {
		basis = "accrual"
		income, err = h.c.Statement(r.Context(), from, to, []string{"income", "expense"})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	position, err := h.c.Statement(r.Context(), "0001-01-01", to, []string{"asset", "liability", "net_assets"})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	// Net income to date completes the balance sheet equation.
	allIE, err := h.c.Statement(r.Context(), "0001-01-01", to, []string{"income", "expense"})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	netToDate := map[string]int64{}
	for _, b := range allIE {
		netToDate[b.Currency] -= b.Balance // income/expense are credit-normal net
	}
	aging, err := h.c.ARAging(r.Context(), to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	var apAging []model.ARAgingRow
	if h.ap != nil {
		if apAging, err = h.ap.APAging(r.Context(), to); err != nil {
			writeError(w, http.StatusInternalServerError, "query error", "internal_error")
			return
		}
	}
	if apAging == nil {
		apAging = []model.ARAgingRow{}
	}
	if income == nil {
		income = []model.GLBalance{}
	}
	if position == nil {
		position = []model.GLBalance{}
	}
	if aging == nil {
		aging = []model.ARAgingRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": from, "to": to, "basis": basis,
		"income_statement":   income,
		"balance_sheet":      position,
		"net_income_to_date": netToDate,
		"ar_aging":           aging,
		"ap_aging":           apAging,
	})
}

// Periods lists closed months (officer+).
func (h *AccountingHandler) Periods(w http.ResponseWriter, r *http.Request) {
	ps, err := h.c.Periods(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if ps == nil {
		ps = []model.AccountingPeriod{}
	}
	writeJSON(w, http.StatusOK, ps)
}

// ClosePeriod locks a month (admin). Body: {month: "YYYY-MM-01"}.
func (h *AccountingHandler) ClosePeriod(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Month string `json:"month"`
	}
	if err := decodeJSON(r, &body); err != nil || !validISODate(body.Month) {
		writeError(w, http.StatusBadRequest, "month (YYYY-MM-DD) required", "bad_request")
		return
	}
	if err := h.c.ClosePeriod(r.Context(), body.Month, userIDFromCtx(r)); err != nil {
		writeRepoError(w, err, "", "close error")
		return
	}
	setAuditDetail(r, map[string]any{"month": body.Month[:7]})
	w.WriteHeader(http.StatusNoContent)
}

// ReopenPeriod unlocks a month (admin; the audit trail records it).
func (h *AccountingHandler) ReopenPeriod(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Month string `json:"month"`
	}
	if err := decodeJSON(r, &body); err != nil || !validISODate(body.Month) {
		writeError(w, http.StatusBadRequest, "month (YYYY-MM-DD) required", "bad_request")
		return
	}
	if err := h.c.ReopenPeriod(r.Context(), body.Month); err != nil {
		writeRepoError(w, err, "period is not closed", "reopen error")
		return
	}
	setAuditDetail(r, map[string]any{"month": body.Month[:7], "reopened": true})
	w.WriteHeader(http.StatusNoContent)
}

// Accounts lists the chart (officer+).
func (h *AccountingHandler) Accounts(w http.ResponseWriter, r *http.Request) {
	as, err := h.c.Accounts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, as)
}

var validAcctType = map[string]bool{"asset": true, "liability": true, "net_assets": true, "income": true, "expense": true}
var acctCodeRe = regexp.MustCompile(`^[0-9]{4}$`)

// CreateAccount adds a chart account (admin).
func (h *AccountingHandler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code, Name, Type string
	}
	if err := decodeJSON(r, &body); err != nil || !acctCodeRe.MatchString(body.Code) ||
		body.Name == "" || len(body.Name) > 80 || !validAcctType[body.Type] {
		writeError(w, http.StatusBadRequest, "code (4 digits), name, and a valid type are required", "bad_request")
		return
	}
	if err := h.c.CreateAccount(r.Context(), body.Code, body.Name, body.Type); err != nil {
		writeRepoError(w, err, "", "create error")
		return
	}
	setAuditDetail(r, map[string]any{"code": body.Code, "name": body.Name, "type": body.Type})
	w.WriteHeader(http.StatusCreated)
}

// UpdateAccount renames/deactivates (admin; code+type freeze after postings).
func (h *AccountingHandler) UpdateAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Name   *string `json:"name"`
		Active *bool   `json:"active"`
	}
	if err := decodeJSON(r, &body); err != nil || (body.Name == nil && body.Active == nil) {
		writeError(w, http.StatusBadRequest, "name and/or active required", "bad_request")
		return
	}
	if err := h.c.UpdateAccount(r.Context(), id, body.Name, body.Active); err != nil {
		writeRepoError(w, err, "account not found", "update error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ManualEntry posts an adjusting entry (admin). The DB enforces balance,
// append-only, and closed periods; this validates shape.
func (h *AccountingHandler) ManualEntry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EntryDate string              `json:"entry_date"`
		Memo      string              `json:"memo"`
		Lines     []model.GLLineInput `json:"lines"`
	}
	if err := decodeJSON(r, &body); err != nil || !validISODate(body.EntryDate) ||
		strings.TrimSpace(body.Memo) == "" || len(body.Lines) < 2 || len(body.Lines) > 40 {
		writeError(w, http.StatusBadRequest, "entry_date, memo, and 2-40 lines required", "bad_request")
		return
	}
	for _, ln := range body.Lines {
		if !acctCodeRe.MatchString(ln.AccountCode) || len(ln.Currency) != 3 ||
			ln.Debit < 0 || ln.Credit < 0 || (ln.Debit == 0) == (ln.Credit == 0) {
			writeError(w, http.StatusBadRequest, "each line needs account_code, currency, and exactly one of debit/credit > 0", "bad_request")
			return
		}
	}
	eid, err := h.c.ManualEntry(r.Context(), body.EntryDate, strings.TrimSpace(body.Memo), userIDFromCtx(r), body.Lines)
	if err != nil {
		writeRepoError(w, err, "unknown account code", "entry error")
		return
	}
	setAuditDetail(r, map[string]any{"entry": eid, "memo": body.Memo, "lines": len(body.Lines)})
	writeJSON(w, http.StatusCreated, map[string]string{"id": eid})
}
