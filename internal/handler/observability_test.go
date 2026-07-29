package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"quorum/internal/metrics"
)

func TestRequestID_GeneratesAndEchoes(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = r.Context().Value(ctxRequestID).(string)
		w.WriteHeader(200)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/x", nil))
	if seen == "" {
		t.Fatal("request id not set in context")
	}
	if rr.Header().Get("X-Request-Id") != seen {
		t.Errorf("response header %q != context id %q", rr.Header().Get("X-Request-Id"), seen)
	}
}

func TestRequestID_HonorsInboundHeader(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, _ = r.Context().Value(ctxRequestID).(string)
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("X-Request-Id", "trace-abc")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen != "trace-abc" {
		t.Errorf("inbound request id not honored: got %q", seen)
	}
}

func TestRequestID_RejectsOverlongInbound(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, _ = r.Context().Value(ctxRequestID).(string)
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("X-Request-Id", strings.Repeat("z", 100))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if len(seen) > 64 || seen == strings.Repeat("z", 100) {
		t.Errorf("overlong inbound id should have been replaced, got len %d", len(seen))
	}
}

func TestRecoverer_CountsAndReturns500(t *testing.T) {
	reg := metrics.New()
	h := Recoverer(reg)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/x", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	var b strings.Builder
	reg.Render(&b)
	if !strings.Contains(b.String(), "quorum_http_panics_total 1") {
		t.Errorf("panic not counted:\n%s", b.String())
	}
}

func TestMetrics_LabelsByRoutePattern(t *testing.T) {
	reg := metrics.New()
	router := chi.NewRouter()
	router.Use(Metrics(reg))
	router.Get("/api/v1/members/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/members/abc123", nil))

	var b strings.Builder
	reg.Render(&b)
	// The metric must use the pattern, not the concrete id, to bound cardinality.
	if !strings.Contains(b.String(), `route="/api/v1/members/{id}"`) {
		t.Errorf("expected route pattern label:\n%s", b.String())
	}
	if strings.Contains(b.String(), "abc123") {
		t.Errorf("raw id leaked into metrics labels:\n%s", b.String())
	}
}

func TestMetricsEndpoint_Gating(t *testing.T) {
	reg := metrics.New()

	// Disabled when no token configured.
	off := MetricsEndpoint(reg, "")
	rr := httptest.NewRecorder()
	off(rr, httptest.NewRequest("GET", "/metrics", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("no-token metrics should 404, got %d", rr.Code)
	}

	on := MetricsEndpoint(reg, "sekret")
	// Missing token -> 401.
	rr = httptest.NewRecorder()
	on(rr, httptest.NewRequest("GET", "/metrics", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("missing token should 401, got %d", rr.Code)
	}
	// Correct bearer token -> 200 with exposition.
	rr = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer sekret")
	on(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid token should 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "quorum_http_requests_in_flight") {
		t.Errorf("exposition body missing:\n%s", rr.Body.String())
	}
	// Query-param token also works.
	rr = httptest.NewRecorder()
	on(rr, httptest.NewRequest("GET", "/metrics?token=sekret", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("query-param token should 200, got %d", rr.Code)
	}
}
