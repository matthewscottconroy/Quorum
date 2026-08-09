package handler

import (
	"context"
	"net/http"
	"strings"

	"quorum/internal/model"
)

// budgetActuals is the GL slice the budget-vs-actual view needs.
type budgetActuals interface {
	Statement(ctx context.Context, from, to string, types []string) ([]model.GLBalance, error)
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

// VsActual compares a scenario's planned totals to GL actuals over [from,to]
// (officer+). Budget lines are not account-linked, so the comparison is at the
// income/expense-total level — planned vs actual, with the variance.
func (h *BudgetHandler) VsActual(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if !validISODate(from) || !validISODate(to) {
		writeError(w, http.StatusBadRequest, "from and to (YYYY-MM-DD) required", "bad_request")
		return
	}
	sc, err := h.repo.GetScenario(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "scenario not found", "not_found")
		return
	}
	var actualIncome, actualExpense int64
	if h.actuals != nil {
		rows, err := h.actuals.Statement(r.Context(), from, to, []string{"income", "expense"})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "query error", "internal_error")
			return
		}
		for _, b := range rows {
			// Income accounts carry credit balances (negative in debit-minus-credit);
			// flip so both report as positive magnitudes.
			switch b.Type {
			case "income":
				actualIncome += -b.Balance
			case "expense":
				actualExpense += b.Balance
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scenario":         sc.Name,
		"currency":         sc.Totals.Currency,
		"budget_income":    sc.Totals.IncomeMinor,
		"budget_expense":   sc.Totals.ExpenseMinor,
		"actual_income":    actualIncome,
		"actual_expense":   actualExpense,
		"income_variance":  actualIncome - sc.Totals.IncomeMinor,
		"expense_variance": actualExpense - sc.Totals.ExpenseMinor,
		"from":             from, "to": to,
	})
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
	s, err := h.repo.CreateScenario(r.Context(), &model.BudgetScenario{
		Name: body.Name, Description: body.Description, PeriodLabel: body.PeriodLabel,
		Status: body.Status, Currency: body.Currency,
	}, userIDFromCtx(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create error", "internal_error")
		return
	}
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
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	if body.Status != nil && !model.ValidBudgetStatuses[*body.Status] {
		writeError(w, http.StatusBadRequest, "invalid status", "bad_request")
		return
	}
	s, err := h.repo.UpdateScenario(r.Context(), id, body.Name, body.Description, body.PeriodLabel, body.Status, body.Currency)
	if err != nil {
		writeRepoError(w, err, "scenario not found", "update error")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// Delete removes a scenario (officer+).
func (h *BudgetHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.repo.DeleteScenario(r.Context(), id); err != nil {
		writeRepoError(w, err, "scenario not found", "delete error")
		return
	}
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
	added, err := h.repo.SeedDuesIncome(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "scenario not found", "seed failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines_added": added})
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
	l, err := h.repo.AddLine(r.Context(), &model.BudgetLine{
		ScenarioID: scenarioID, Kind: body.Kind, Category: body.Category, Label: body.Label,
		Quantity: qty, UnitAmountMinor: body.UnitAmountMinor, Note: body.Note, SortOrder: body.SortOrder,
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
	l, err := h.repo.UpdateLine(r.Context(), id, body.Kind, body.Category, body.Label, body.Quantity, body.UnitAmountMinor, body.Note, body.SortOrder)
	if err != nil {
		writeRepoError(w, err, "line not found", "update error")
		return
	}
	writeJSON(w, http.StatusOK, l)
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
