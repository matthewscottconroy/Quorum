package handler

import (
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"quorum/internal/model"
)

// FXHandler manages the org reporting currency and the exchange-rate table used
// to convert analytics and budget figures into that currency.
type FXHandler struct {
	repo fxRepo
}

// NewFXHandler constructs an FXHandler.
func NewFXHandler(r fxRepo) *FXHandler {
	return &FXHandler{repo: r}
}

// GetSettings returns the org reporting currency (officer+).
func (h *FXHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	cur, err := h.repo.ReportingCurrency(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"reporting_currency": cur})
}

// UpdateSettings sets the org reporting currency (admin+).
func (h *FXHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ReportingCurrency string `json:"reporting_currency"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	code, ok := validCurrencyCode(body.ReportingCurrency)
	if !ok {
		writeError(w, http.StatusBadRequest, "reporting_currency must be a 3-8 letter code", "bad_request")
		return
	}
	if err := h.repo.SetReportingCurrency(r.Context(), code); err != nil {
		writeError(w, http.StatusInternalServerError, "update error", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"reporting_currency": code})
}

// ListRates returns all exchange rates (officer+).
func (h *FXHandler) ListRates(w http.ResponseWriter, r *http.Request) {
	rates, err := h.repo.ListRates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query error", "internal_error")
		return
	}
	if rates == nil {
		rates = []model.FXRate{}
	}
	writeJSON(w, http.StatusOK, rates)
}

// CreateRate adds an exchange rate (admin+).
func (h *FXHandler) CreateRate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FromCurrency string `json:"from_currency"`
		ToCurrency   string `json:"to_currency"`
		Rate         string `json:"rate"`
		EffectiveAt  string `json:"effective_at"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	from, okF := validCurrencyCode(body.FromCurrency)
	to, okT := validCurrencyCode(body.ToCurrency)
	if !okF || !okT {
		writeError(w, http.StatusBadRequest, "from_currency and to_currency must be 3-8 letter codes", "bad_request")
		return
	}
	if from == to {
		writeError(w, http.StatusBadRequest, "from_currency and to_currency must differ", "bad_request")
		return
	}
	if !validRate(body.Rate) {
		writeError(w, http.StatusBadRequest, "rate must be a positive decimal number", "bad_request")
		return
	}
	if _, err := time.Parse("2006-01-02", strings.TrimSpace(body.EffectiveAt)); err != nil {
		writeError(w, http.StatusBadRequest, "effective_at must be a YYYY-MM-DD date", "bad_request")
		return
	}
	rate, err := h.repo.CreateRate(r.Context(), from, to, strings.TrimSpace(body.Rate),
		strings.TrimSpace(body.EffectiveAt), userIDFromCtx(r))
	if err != nil {
		// writeRepoError maps the (from, to, effective_at) unique violation to
		// a 409; anything else is a genuine server fault, so the fallback must
		// not claim a conflict that never happened.
		writeRepoError(w, err, "", "create error")
		return
	}
	writeJSON(w, http.StatusCreated, rate)
}

// DeleteRate removes an exchange rate (admin+).
func (h *FXHandler) DeleteRate(w http.ResponseWriter, r *http.Request) {
	id, ok := requireUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.repo.DeleteRate(r.Context(), id); err != nil {
		writeRepoError(w, err, "rate not found", "delete error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validCurrencyCode normalizes and validates a currency code: 3-8 ASCII letters.
// The app allows non-ISO free-text codes elsewhere, so this is permissive but
// still rejects empty/garbage input. Returns the upper-cased code and ok.
func validCurrencyCode(code string) (string, bool) {
	c := strings.ToUpper(strings.TrimSpace(code))
	if len(c) < 3 || len(c) > 8 {
		return "", false
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return "", false
		}
	}
	return c, true
}

// validRate reports whether s is a positive PLAIN decimal ("1.0785", "142").
// big.Rat alone would also accept fractions ("1/3") and exponents that
// ::numeric then rejects with a confusing 500, so shape-check first.
func validRate(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) == 0 || len(s) > 40 || !plainDecimalRe.MatchString(s) {
		return false
	}
	rat, ok := new(big.Rat).SetString(s)
	return ok && rat.Sign() > 0
}

var plainDecimalRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
