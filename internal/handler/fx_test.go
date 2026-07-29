package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quorum/internal/model"
)

// ---- mockFXRepo ----

type mockFXRepo struct {
	ReportingCurrencyFn    func(ctx context.Context) (string, error)
	SetReportingCurrencyFn func(ctx context.Context, code string) error
	ListRatesFn            func(ctx context.Context) ([]model.FXRate, error)
	CreateRateFn           func(ctx context.Context, from, to, rate, effectiveAt, createdBy string) (*model.FXRate, error)
	DeleteRateFn           func(ctx context.Context, id string) error
}

func (m *mockFXRepo) ReportingCurrency(ctx context.Context) (string, error) {
	if m.ReportingCurrencyFn != nil {
		return m.ReportingCurrencyFn(ctx)
	}
	return "USD", nil
}
func (m *mockFXRepo) SetReportingCurrency(ctx context.Context, code string) error {
	if m.SetReportingCurrencyFn != nil {
		return m.SetReportingCurrencyFn(ctx, code)
	}
	return nil
}
func (m *mockFXRepo) ListRates(ctx context.Context) ([]model.FXRate, error) {
	if m.ListRatesFn != nil {
		return m.ListRatesFn(ctx)
	}
	return nil, nil
}
func (m *mockFXRepo) CreateRate(ctx context.Context, from, to, rate, effectiveAt, createdBy string) (*model.FXRate, error) {
	if m.CreateRateFn != nil {
		return m.CreateRateFn(ctx, from, to, rate, effectiveAt, createdBy)
	}
	return &model.FXRate{ID: "r1", FromCurrency: from, ToCurrency: to, Rate: rate, EffectiveAt: effectiveAt}, nil
}
func (m *mockFXRepo) DeleteRate(ctx context.Context, id string) error {
	if m.DeleteRateFn != nil {
		return m.DeleteRateFn(ctx, id)
	}
	return nil
}

func TestFXGetSettings(t *testing.T) {
	h := NewFXHandler(&mockFXRepo{ReportingCurrencyFn: func(_ context.Context) (string, error) { return "EUR", nil }})
	rr := httptest.NewRecorder()
	h.GetSettings(rr, httptest.NewRequest("GET", "/fx/settings", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp["reporting_currency"] != "EUR" {
		t.Errorf("reporting_currency = %q, want EUR", resp["reporting_currency"])
	}
}

func TestFXUpdateSettings_Normalizes(t *testing.T) {
	var saved string
	h := NewFXHandler(&mockFXRepo{SetReportingCurrencyFn: func(_ context.Context, code string) error { saved = code; return nil }})
	req := httptest.NewRequest("PUT", "/fx/settings", strings.NewReader(`{"reporting_currency":" eur "}`))
	rr := httptest.NewRecorder()
	h.UpdateSettings(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if saved != "EUR" {
		t.Errorf("saved currency = %q, want normalized EUR", saved)
	}
}

func TestFXUpdateSettings_RejectsBadCode(t *testing.T) {
	h := NewFXHandler(&mockFXRepo{})
	for _, bad := range []string{`{"reporting_currency":"US"}`, `{"reporting_currency":"US1"}`, `{"reporting_currency":""}`} {
		rr := httptest.NewRecorder()
		h.UpdateSettings(rr, httptest.NewRequest("PUT", "/fx/settings", strings.NewReader(bad)))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body %s: expected 400, got %d", bad, rr.Code)
		}
	}
}

func TestFXCreateRate_Valid(t *testing.T) {
	var gotFrom, gotTo, gotRate string
	h := NewFXHandler(&mockFXRepo{CreateRateFn: func(_ context.Context, from, to, rate, _, _ string) (*model.FXRate, error) {
		gotFrom, gotTo, gotRate = from, to, rate
		return &model.FXRate{ID: "r1", FromCurrency: from, ToCurrency: to, Rate: rate}, nil
	}})
	req := withCtxUser(httptest.NewRequest("POST", "/fx/rates",
		strings.NewReader(`{"from_currency":"eur","to_currency":"usd","rate":"1.0850","effective_at":"2026-01-01"}`)), "u", "admin")
	rr := httptest.NewRecorder()
	h.CreateRate(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if gotFrom != "EUR" || gotTo != "USD" || gotRate != "1.0850" {
		t.Errorf("create args = (%q,%q,%q), want (EUR,USD,1.0850)", gotFrom, gotTo, gotRate)
	}
}

func TestFXCreateRate_Rejects(t *testing.T) {
	h := NewFXHandler(&mockFXRepo{})
	cases := map[string]string{
		"same currency": `{"from_currency":"USD","to_currency":"USD","rate":"1","effective_at":"2026-01-01"}`,
		"negative rate": `{"from_currency":"EUR","to_currency":"USD","rate":"-1","effective_at":"2026-01-01"}`,
		"zero rate":     `{"from_currency":"EUR","to_currency":"USD","rate":"0","effective_at":"2026-01-01"}`,
		"non-numeric":   `{"from_currency":"EUR","to_currency":"USD","rate":"abc","effective_at":"2026-01-01"}`,
		"bad date":      `{"from_currency":"EUR","to_currency":"USD","rate":"1","effective_at":"01/01/2026"}`,
		"bad from":      `{"from_currency":"E","to_currency":"USD","rate":"1","effective_at":"2026-01-01"}`,
	}
	for name, body := range cases {
		req := withCtxUser(httptest.NewRequest("POST", "/fx/rates", strings.NewReader(body)), "u", "admin")
		rr := httptest.NewRecorder()
		h.CreateRate(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d: %s", name, rr.Code, rr.Body)
		}
	}
}
