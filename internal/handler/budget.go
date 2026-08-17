package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"quorum/internal/model"
)

// budgetActuals is the GL slice the budget-vs-actual view needs. Both bases
// are exposed so a cash-books org compares against numbers matching its own
// statements.
type budgetActuals interface {
	Statement(ctx context.Context, from, to string, types []string) ([]model.GLBalance, error)
	StatementCash(ctx context.Context, from, to string) ([]model.GLBalance, error)
}

// Bounds on budget line inputs. The product quantity × unit stays well within
// int64 (max ~9.2e18), so the Go rollup and the Postgres SUM agree instead of
// one silently wrapping while the other errors.
const (
	maxBudgetQuantity  = 1_000_000         // e.g. members or months
	maxBudgetUnitMinor = 1_000_000_000_000 // 10 billion major units
	maxCompareIDs      = 50
)

// BudgetHandler handles budget scenario planning endpoints.
type BudgetHandler struct {
	repo    budgetRepo
	actuals budgetActuals
}

// NewBudgetHandler constructs a BudgetHandler.
func NewBudgetHandler(r budgetRepo) *BudgetHandler {
	return &BudgetHandler{repo: r}
}

// SetActuals wires the GL source for the budget-vs-actual comparison.
func (h *BudgetHandler) SetActuals(a budgetActuals) { h.actuals = a }

// VsActual compares a scenario's plan to GL actuals (officer+).
//
// Correctness rules learned the hard way:
//   - the GL reports per-currency rows; only rows in the scenario's currency
//     are counted, and any other currencies present are NAMED in the response
//     instead of being silently summed at par;
//   - ?basis=cash uses the cash-basis statement so cash-books orgs compare
//     like with like (default: accrual);
//   - from/to default to the scenario's own dates when it has them;
//   - when the scenario has dates, totals are also prorated by elapsed time
//     so a mid-year check isn't guaranteed good news in February.
//
// Lines linked to GL accounts additionally produce per-account rows —
// planned vs actual for that account — with unlinked lines aggregated into
// an "(unlinked)" bucket per kind.
func (h *BudgetHandler) VsActual(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	sc, err := h.repo.GetScenario(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "scenario not found", "not_found")
		return
	}
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if from == "" && sc.StartsOn != nil {
		from = sc.StartsOn.Format("2006-01-02")
	}
	if to == "" && sc.EndsOn != nil {
		to = sc.EndsOn.Format("2006-01-02")
	}
	if !validISODate(from) || !validISODate(to) {
		writeError(w, http.StatusBadRequest, "from and to (YYYY-MM-DD) required (or give the scenario dates)", "bad_request")
		return
	}
	basis := r.URL.Query().Get("basis")
	if basis == "" {
		basis = "accrual"
	}
	if basis != "accrual" && basis != "cash" {
		writeError(w, http.StatusBadRequest, "basis must be accrual or cash", "bad_request")
		return
	}

	var rows []model.GLBalance
	if h.actuals != nil {
		if basis == "cash" {
			rows, err = h.actuals.StatementCash(r.Context(), from, to)
		} else {
			rows, err = h.actuals.Statement(r.Context(), from, to, []string{"income", "expense"})
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "query error", "internal_error")
			return
		}
	}

	// Only the scenario's currency counts; name anything else that appeared.
	var actualIncome, actualExpense int64
	actualByCode := map[string]int64{}
	otherCurrencies := map[string]bool{}
	for _, b := range rows {
		if b.Type != "income" && b.Type != "expense" {
			continue
		}
		if b.Currency != sc.Currency {
			otherCurrencies[b.Currency] = true
			continue
		}
		// Income accounts carry credit balances (negative in debit-minus-credit);
		// flip so both report as positive magnitudes.
		amount := b.Balance
		if b.Type == "income" {
			amount = -amount
		}
		actualByCode[b.Code] += amount
		if b.Type == "income" {
			actualIncome += amount
		} else {
			actualExpense += amount
		}
	}
	excluded := make([]string, 0, len(otherCurrencies))
	for c := range otherCurrencies {
		excluded = append(excluded, c)
	}
	for i := 1; i < len(excluded); i++ { // tiny, keep deterministic
		for j := i; j > 0 && excluded[j] < excluded[j-1]; j-- {
			excluded[j], excluded[j-1] = excluded[j-1], excluded[j]
		}
	}

	// Per-account budget rows from account-linked lines; the rest bucket
	// into "(unlinked)" per kind so the totals always reconcile.
	type catRow struct {
		Kind        string `json:"kind"`
		AccountCode string `json:"account_code,omitempty"`
		Label       string `json:"label"`
		Budget      int64  `json:"budget_minor"`
		Actual      int64  `json:"actual_minor"`
		Variance    int64  `json:"variance_minor"`
	}
	byAccount := map[string]*catRow{}
	var order []string
	unlinked := map[string]int64{}
	for _, l := range sc.Lines {
		if l.AccountID == nil || l.AccountCode == nil {
			unlinked[l.Kind] += l.AmountMinor
			continue
		}
		key := *l.AccountCode
		row, seen := byAccount[key]
		if !seen {
			name := key
			if l.AccountName != nil {
				name = *l.AccountName
			}
			row = &catRow{Kind: l.Kind, AccountCode: key, Label: name}
			byAccount[key] = row
			order = append(order, key)
		}
		row.Budget += l.AmountMinor
	}
	categories := make([]catRow, 0, len(order)+2)
	for _, code := range order {
		row := byAccount[code]
		row.Actual = actualByCode[code]
		if row.Kind == "income" {
			row.Variance = row.Actual - row.Budget
		} else {
			row.Variance = row.Budget - row.Actual // under budget = positive
		}
		categories = append(categories, *row)
	}
	for _, kind := range []string{"income", "expense"} {
		if amt := unlinked[kind]; amt != 0 {
			categories = append(categories, catRow{Kind: kind, Label: "(unlinked)", Budget: amt})
		}
	}

	resp := map[string]any{
		"scenario":       sc.Name,
		"currency":       sc.Currency,
		"basis":          basis,
		"budget_income":  sc.Totals.IncomeMinor,
		"budget_expense": sc.Totals.ExpenseMinor,
		"actual_income":  actualIncome,
		"actual_expense": actualExpense,
		// Variance is favorable-positive at EVERY level, matching the
		// per-category rows: income above budget is +, expense under
		// budget is + — a treasurer never sees the same overspend with
		// two different signs in one modal.
		"income_variance":  actualIncome - sc.Totals.IncomeMinor,
		"expense_variance": sc.Totals.ExpenseMinor - actualExpense,
		"categories":       categories,
		"from":             from, "to": to,
	}
	if len(excluded) > 0 {
		resp["excluded_currencies"] = excluded
	}
	// Time-elapsed proration when the scenario carries real dates.
	if sc.StartsOn != nil && sc.EndsOn != nil && sc.EndsOn.After(*sc.StartsOn) {
		total := sc.EndsOn.Sub(*sc.StartsOn)
		elapsed := time.Since(*sc.StartsOn)
		pct := float64(elapsed) / float64(total)
		if pct < 0 {
			pct = 0
		}
		if pct > 1 {
			pct = 1
		}
		resp["period_elapsed_pct"] = int(pct * 100)
		resp["prorated_budget_income"] = int64(float64(sc.Totals.IncomeMinor) * pct)
		resp["prorated_budget_expense"] = int64(float64(sc.Totals.ExpenseMinor) * pct)
	}
	writeJSON(w, http.StatusOK, resp)
}

// List returns all budget scenarios with rolled-up totals (officer+).
func (h *BudgetHandler) List(w http.ResponseWriter, r *http.Request) {
	scenarios, err := h.repo.ListScenarios(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if scenarios == nil {
		scenarios = []model.BudgetScenario{}
	}
	writeJSON(w, http.StatusOK, scenarios)
}

// Compare returns the totals for several scenarios side by side (?ids=a,b,c).
func (h *BudgetHandler) Compare(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("ids"))
	if raw == "" {
		writeError(w, http.StatusBadRequest, "ids query parameter is required", "bad_request")
		return
	}
	ids := strings.Split(raw, ",")
	if len(ids) > maxCompareIDs {
		writeError(w, http.StatusBadRequest, "too many scenarios to compare at once", "bad_request")
		return
	}
	for _, id := range ids {
		if !isValidUUID(strings.TrimSpace(id)) {
			writeError(w, http.StatusBadRequest, "each id must be a UUID", "bad_request")
			return
		}
	}
	scenarios, err := h.repo.CompareScenarios(r.Context(), ids)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if scenarios == nil {
		scenarios = []model.BudgetScenario{}
	}
	writeJSON(w, http.StatusOK, scenarios)
}

// Get returns a single scenario with its lines and totals (officer+).
func (h *BudgetHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	s, err := h.repo.GetScenario(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "scenario not found", "query error")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// Create adds a new budget scenario (officer+).
func (h *BudgetHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
		PeriodLabel *string `json:"period_label"`
		Status      string  `json:"status"`
		Currency    string  `json:"currency"`
		StartsOn    string  `json:"starts_on"`
		EndsOn      string  `json:"ends_on"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required", "bad_request")
		return
	}
	if body.Status == "" {
		body.Status = "draft"
	}
	if !model.ValidBudgetStatuses[body.Status] {
		writeError(w, http.StatusBadRequest, "status must be draft, active, or archived", "bad_request")
		return
	}
	if body.Currency == "" {
		body.Currency = "USD"
	}
	cur, curOK := validCurrencyCode(body.Currency)
	if !curOK {
		writeError(w, http.StatusBadRequest, "currency must be a 3-8 letter code", "bad_request")
		return
	}
	body.Currency = cur
	sc := &model.BudgetScenario{
		Name: body.Name, Description: body.Description, PeriodLabel: body.PeriodLabel,
		Status: body.Status, Currency: body.Currency,
	}
	for _, d := range []struct {
		raw  string
		into **time.Time
	}{{body.StartsOn, &sc.StartsOn}, {body.EndsOn, &sc.EndsOn}} {
		if d.raw == "" {
			continue
		}
		t, err := time.Parse("2006-01-02", d.raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "dates must be YYYY-MM-DD", "bad_request")
			return
		}
		*d.into = &t
	}
	if sc.StartsOn != nil && sc.EndsOn != nil && sc.EndsOn.Before(*sc.StartsOn) {
		writeError(w, http.StatusBadRequest, "ends_on must not precede starts_on", "bad_request")
		return
	}
	s, err := h.repo.CreateScenario(r.Context(), sc, userIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create error", "internal_error")
		return
	}
	setAuditDetail(r, map[string]any{"scenario": s.Name})
	writeJSON(w, http.StatusCreated, s)
}

// Update edits scenario metadata (officer+).
func (h *BudgetHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		PeriodLabel *string `json:"period_label"`
		Status      *string `json:"status"`
		Currency    *string `json:"currency"`
		StartsOn    *string `json:"starts_on"`
		EndsOn      *string `json:"ends_on"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if body.Status != nil && !model.ValidBudgetStatuses[*body.Status] {
		writeError(w, http.StatusBadRequest, "invalid status", "bad_request")
		return
	}
	for _, d := range []*string{body.StartsOn, body.EndsOn} {
		if d != nil && *d != "" && !validISODate(*d) {
			writeError(w, http.StatusBadRequest, "dates must be YYYY-MM-DD", "bad_request")
			return
		}
	}
	status, currency, hasLines, err := h.repo.ScenarioGuard(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "scenario not found", "query error")
		return
	}
	// Currency freezes once lines exist: changing it would silently
	// re-denominate every line (a $10,000 budget "becoming" €10,000).
	if body.Currency != nil && hasLines && !strings.EqualFold(*body.Currency, currency) {
		writeError(w, http.StatusConflict,
			"currency is locked once the scenario has lines — clone into a new scenario instead", "conflict")
		return
	}
	// An archived scenario is a record; only its status may move (back to
	// draft/active) so un-archiving stays possible.
	if status == "archived" && (body.Name != nil || body.Description != nil ||
		body.PeriodLabel != nil || body.Currency != nil || body.StartsOn != nil || body.EndsOn != nil) {
		writeError(w, http.StatusConflict, "archived scenarios are read-only (change status first)", "conflict")
		return
	}
	s, err := h.repo.UpdateScenario(r.Context(), id, body.Name, body.Description, body.PeriodLabel, body.Status, body.Currency, body.StartsOn, body.EndsOn)
	if err != nil {
		writeRepoError(w, err, "scenario not found", "update error")
		return
	}
	setAuditDetail(r, map[string]any{"scenario": s.Name})
	writeJSON(w, http.StatusOK, s)
}

// Delete removes a scenario (admin, ?confirm=<name>). A budget is a record:
// deleting one deserves the same friction as deleting a plan.
func (h *BudgetHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	sc, err := h.repo.GetScenario(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "scenario not found", "query error")
		return
	}
	if !confirmMatches(w, r, sc.Name) {
		return
	}
	if err := h.repo.DeleteScenario(r.Context(), id); err != nil {
		writeRepoError(w, err, "scenario not found", "delete error")
		return
	}
	setAuditDetail(r, map[string]any{"scenario": sc.Name})
	w.WriteHeader(http.StatusNoContent)
}

// Clone duplicates a scenario under a new name to explore a variation (officer+).
func (h *BudgetHandler) Clone(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = decodeJSON(r, &body)
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "Copy"
	}
	s, err := h.repo.CloneScenario(r.Context(), id, name, userIDFromCtx(r))
	if err != nil {
		writeRepoError(w, err, "scenario not found", "clone error")
		return
	}
	writeJSON(w, http.StatusCreated, s)
}

// SeedDues projects dues income into the scenario from active members and their
// tier schedules, one line per tier (officer+).
func (h *BudgetHandler) SeedDues(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	added, skips, err := h.repo.SeedDuesIncome(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "scenario not found", "seed failed")
		return
	}
	if skips == nil {
		skips = []model.BudgetSeedSkip{}
	}
	setAuditDetail(r, map[string]any{"scenario": id, "lines": added, "skipped": len(skips)})
	writeJSON(w, http.StatusOK, map[string]any{"lines_added": added, "skipped": skips})
}

// AddLine appends an income or expense line to a scenario (officer+).
func (h *BudgetHandler) AddLine(w http.ResponseWriter, r *http.Request) {
	scenarioID, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Kind            string  `json:"kind"`
		Category        *string `json:"category"`
		Label           string  `json:"label"`
		Quantity        *int64  `json:"quantity"`
		UnitAmountMinor int64   `json:"unit_amount_minor"`
		Note            *string `json:"note"`
		SortOrder       int     `json:"sort_order"`
		AccountID       *string `json:"account_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if !model.ValidBudgetKinds[body.Kind] {
		writeError(w, http.StatusBadRequest, "kind must be income or expense", "bad_request")
		return
	}
	if strings.TrimSpace(body.Label) == "" {
		writeError(w, http.StatusBadRequest, "label is required", "bad_request")
		return
	}
	qty := int64(1)
	if body.Quantity != nil {
		qty = *body.Quantity
	}
	if msg := validateLineAmounts(qty, body.UnitAmountMinor); msg != "" {
		writeError(w, http.StatusBadRequest, msg, "bad_request")
		return
	}
	if status, _, _, err := h.repo.ScenarioGuard(r.Context(), scenarioID); err == nil && status == "archived" {
		writeError(w, http.StatusConflict, "archived scenarios are read-only", "conflict")
		return
	}
	if msg := h.validateLineAccount(r.Context(), body.AccountID, body.Kind); msg != "" {
		writeError(w, http.StatusBadRequest, msg, "bad_request")
		return
	}
	l, err := h.repo.AddLine(r.Context(), &model.BudgetLine{
		ScenarioID: scenarioID, Kind: body.Kind, Category: body.Category, Label: body.Label,
		Quantity: qty, UnitAmountMinor: body.UnitAmountMinor, Note: body.Note, SortOrder: body.SortOrder,
		AccountID: normalizeUUIDPtr(body.AccountID),
	})
	if err != nil {
		if isFKViolation(err) {
			writeError(w, http.StatusNotFound, "scenario not found", "not_found")
			return
		}
		writeError(w, http.StatusInternalServerError, "create error", "internal_error")
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

// ReorderLines applies a whole section's order in one call:
// PUT /budgets/{id}/lines/order  {"ids": ["...", ...]} (officer+).
func (h *BudgetHandler) ReorderLines(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &body); err != nil || len(body.IDs) == 0 || len(body.IDs) > 500 {
		writeError(w, http.StatusBadRequest, "ids (1-500) required", "bad_request")
		return
	}
	for _, lid := range body.IDs {
		if !isValidUUID(lid) {
			writeError(w, http.StatusBadRequest, "ids must be UUIDs", "bad_request")
			return
		}
	}
	if status, _, _, err := h.repo.ScenarioGuard(r.Context(), id); err == nil && status == "archived" {
		writeError(w, http.StatusConflict, "archived scenarios are read-only", "conflict")
		return
	}
	if err := h.repo.ReorderLines(r.Context(), id, body.IDs); err != nil {
		writeError(w, http.StatusInternalServerError, "reorder error", "internal_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UpdateLine edits a budget line (officer+).
func (h *BudgetHandler) UpdateLine(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Kind            *string `json:"kind"`
		Category        *string `json:"category"`
		Label           *string `json:"label"`
		Quantity        *int64  `json:"quantity"`
		UnitAmountMinor *int64  `json:"unit_amount_minor"`
		Note            *string `json:"note"`
		SortOrder       *int    `json:"sort_order"`
		// AccountID: absent = keep, "" = unlink, uuid = link.
		AccountID *string `json:"account_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if body.Kind != nil && !model.ValidBudgetKinds[*body.Kind] {
		writeError(w, http.StatusBadRequest, "kind must be income or expense", "bad_request")
		return
	}
	if body.Quantity != nil || body.UnitAmountMinor != nil {
		q, u := int64(1), int64(0)
		if body.Quantity != nil {
			q = *body.Quantity
		}
		if body.UnitAmountMinor != nil {
			u = *body.UnitAmountMinor
		}
		if msg := validateLineAmounts(q, u); msg != "" {
			writeError(w, http.StatusBadRequest, msg, "bad_request")
			return
		}
	}
	if sid, err := h.repo.LineScenario(r.Context(), id); err == nil {
		if status, _, _, gerr := h.repo.ScenarioGuard(r.Context(), sid); gerr == nil && status == "archived" {
			writeError(w, http.StatusConflict, "archived scenarios are read-only", "conflict")
			return
		}
	}
	clearAccount := body.AccountID != nil && *body.AccountID == ""
	if body.AccountID != nil && !clearAccount {
		kind := ""
		if body.Kind != nil {
			kind = *body.Kind
		}
		if msg := h.validateLineAccount(r.Context(), body.AccountID, kind); msg != "" {
			writeError(w, http.StatusBadRequest, msg, "bad_request")
			return
		}
	}
	l, err := h.repo.UpdateLine(r.Context(), id, body.Kind, body.Category, body.Label, body.Quantity, body.UnitAmountMinor, body.Note, body.SortOrder, normalizeUUIDPtr(body.AccountID), clearAccount)
	if err != nil {
		writeRepoError(w, err, "line not found", "update error")
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// validateLineAccount checks an optional GL-account link: valid UUID, real
// account, and — when the line's kind is known — an account of that kind
// (an income line cannot budget against an expense account).
func (h *BudgetHandler) validateLineAccount(ctx context.Context, accountID *string, kind string) string {
	if accountID == nil || *accountID == "" {
		return ""
	}
	if !isValidUUID(*accountID) {
		return "account_id must be a UUID"
	}
	accountKind, err := h.repo.AccountKind(ctx, *accountID)
	if err != nil {
		return "account not found"
	}
	if accountKind != "income" && accountKind != "expense" {
		return "budget lines may only link to income or expense accounts"
	}
	if kind != "" && accountKind != kind {
		return "the account's type must match the line's kind"
	}
	return ""
}

// normalizeUUIDPtr maps the empty-string sentinel to nil for inserts.
func normalizeUUIDPtr(p *string) *string {
	if p == nil || *p == "" {
		return nil
	}
	return p
}

// Remaining reports the active budget for one GL account and how much of it
// is left, given actuals over the scenario's period — the read-only hint
// shown at spend-approval time (officer+). 404 when nothing budgets the
// account; spending screens simply omit the hint.
func (h *BudgetHandler) Remaining(w http.ResponseWriter, r *http.Request) {
	accountID := r.URL.Query().Get("account_id")
	if !isValidUUID(accountID) {
		writeError(w, http.StatusBadRequest, "account_id (UUID) required", "bad_request")
		return
	}
	sc, budget, err := h.repo.AccountBudget(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusNotFound, "no active budget covers this account", "not_found")
		return
	}
	// Actuals over the scenario period (calendar year fallback), scenario
	// currency only — same discipline as VsActual.
	now := time.Now()
	from := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	to := time.Date(now.Year(), 12, 31, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	if sc.StartsOn != nil {
		from = sc.StartsOn.Format("2006-01-02")
	}
	if sc.EndsOn != nil {
		to = sc.EndsOn.Format("2006-01-02")
	}
	var actual int64
	if h.actuals != nil {
		rows, err := h.actuals.Statement(r.Context(), from, to, []string{"income", "expense"})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "query error", "internal_error")
			return
		}
		var code string
		for _, l := range sc.Lines {
			if l.AccountID != nil && *l.AccountID == accountID && l.AccountCode != nil {
				code = *l.AccountCode
				break
			}
		}
		for _, b := range rows {
			if b.Code != code || b.Currency != sc.Currency {
				continue
			}
			if b.Type == "income" {
				actual += -b.Balance
			} else {
				actual += b.Balance
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scenario":        sc.Name,
		"currency":        sc.Currency,
		"budget_minor":    budget,
		"actual_minor":    actual,
		"remaining_minor": budget - actual,
		"from":            from, "to": to,
	})
}

// validateLineAmounts bounds quantity and unit amount so quantity × unit cannot
// overflow int64. Returns "" when valid, else a user-facing error message.
func validateLineAmounts(quantity, unitAmountMinor int64) string {
	if quantity < 0 || quantity > maxBudgetQuantity {
		return "quantity must be between 0 and 1,000,000"
	}
	if unitAmountMinor < -maxBudgetUnitMinor || unitAmountMinor > maxBudgetUnitMinor {
		return "unit amount is out of range"
	}
	return ""
}

// DeleteLine removes a budget line (officer+).
func (h *BudgetHandler) DeleteLine(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.repo.DeleteLine(r.Context(), id); err != nil {
		writeRepoError(w, err, "line not found", "delete error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
