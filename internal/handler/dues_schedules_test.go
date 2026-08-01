package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"quorum/internal/model"
)

const testScheduleID = "77777777-7777-7777-7777-777777777777"

func duesHandler(r duesRepo) *DuesHandler { return NewDuesHandler(r) }

func TestCreateSchedule_Valid(t *testing.T) {
	var created *model.DuesSchedule
	repo := &mockDuesRepo{
		CreateScheduleFn: func(_ context.Context, s *model.DuesSchedule) (*model.DuesSchedule, error) {
			created = s
			s.ID = testScheduleID
			return s, nil
		},
	}
	body := `{"tier":"standard","amount_minor":5000,"cadence":"annual"}`
	req := withCtxUser(httptest.NewRequest("POST", "/dues/schedules", strings.NewReader(body)), "u", "officer")
	rr := httptest.NewRecorder()
	duesHandler(repo).CreateSchedule(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if created.DueDays != 30 || created.Currency != "USD" || !created.Active {
		t.Errorf("defaults not applied: %+v", created)
	}
}

func TestCreateSchedule_Validation(t *testing.T) {
	h := duesHandler(&mockDuesRepo{})
	for _, b := range []string{
		`{"tier":"","amount_minor":5000,"cadence":"annual"}`,         // no tier
		`{"tier":"standard","amount_minor":0,"cadence":"annual"}`,    // non-positive amount
		`{"tier":"standard","amount_minor":5000,"cadence":"weekly"}`, // bad cadence
	} {
		rr := httptest.NewRecorder()
		h.CreateSchedule(rr, withCtxUser(httptest.NewRequest("POST", "/dues/schedules", strings.NewReader(b)), "u", "officer"))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body %s: expected 400, got %d", b, rr.Code)
		}
	}
}

func TestGenerateSchedule_ReturnsCount(t *testing.T) {
	var usedLabel string
	repo := &mockDuesRepo{
		GetScheduleFn: func(_ context.Context, _ string) (*model.DuesSchedule, error) {
			return &model.DuesSchedule{ID: testScheduleID, Tier: "standard", AmountMinor: 5000, Currency: "USD", Cadence: "annual", DueDays: 30}, nil
		},
		GenerateInvoicesForScheduleFn: func(_ context.Context, _ model.DuesSchedule, label string, _ time.Time) (int, error) {
			usedLabel = label
			return 12, nil
		},
	}
	req := reqWithParam("POST", "/dues/schedules/"+testScheduleID+"/generate", "", map[string]string{"id": testScheduleID})
	req = withCtxUser(req, "u", "officer")
	rr := httptest.NewRecorder()
	duesHandler(repo).GenerateSchedule(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["created"].(float64) != 12 {
		t.Errorf("expected created=12, got %v", resp["created"])
	}
	// Annual label is the current year — confirm the handler computed a period.
	if usedLabel == "" {
		t.Error("expected a period label to be computed")
	}
}
