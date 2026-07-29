package metrics

import (
	"strings"
	"testing"
	"time"
)

func render(r *Registry) string {
	var b strings.Builder
	r.Render(&b)
	return b.String()
}

func TestRegistry_RequestCountersAndHistogram(t *testing.T) {
	r := New()
	r.ObserveRequest("GET", "/api/v1/members/{id}", 200, 30*time.Millisecond)
	r.ObserveRequest("GET", "/api/v1/members/{id}", 200, 40*time.Millisecond)
	r.ObserveRequest("POST", "/api/v1/members", 201, 120*time.Millisecond)
	out := render(r)

	if !strings.Contains(out, `quorum_http_requests_total{method="GET",route="/api/v1/members/{id}",status="200"} 2`) {
		t.Errorf("missing GET counter:\n%s", out)
	}
	if !strings.Contains(out, `quorum_http_requests_total{method="POST",route="/api/v1/members",status="201"} 1`) {
		t.Errorf("missing POST counter:\n%s", out)
	}
	// Histogram: 2 GET observations, both <= 0.05s bucket.
	if !strings.Contains(out, `quorum_http_request_duration_seconds_bucket{method="GET",route="/api/v1/members/{id}",le="0.05"} 2`) {
		t.Errorf("histogram bucket wrong:\n%s", out)
	}
	if !strings.Contains(out, `quorum_http_request_duration_seconds_count{method="GET",route="/api/v1/members/{id}"} 2`) {
		t.Errorf("histogram count wrong:\n%s", out)
	}
	// The 120ms POST falls above 0.1 but at/below 0.25.
	if !strings.Contains(out, `quorum_http_request_duration_seconds_bucket{method="POST",route="/api/v1/members",le="0.1"} 0`) {
		t.Errorf("POST should not be in the 0.1 bucket:\n%s", out)
	}
	if !strings.Contains(out, `quorum_http_request_duration_seconds_bucket{method="POST",route="/api/v1/members",le="0.25"} 1`) {
		t.Errorf("POST should be in the 0.25 bucket:\n%s", out)
	}
}

func TestRegistry_InFlightAndPanics(t *testing.T) {
	r := New()
	r.IncInFlight()
	r.IncInFlight()
	r.DecInFlight()
	r.IncPanic()
	out := render(r)
	if !strings.Contains(out, "quorum_http_requests_in_flight 1") {
		t.Errorf("in-flight gauge wrong:\n%s", out)
	}
	if !strings.Contains(out, "quorum_http_panics_total 1") {
		t.Errorf("panic counter wrong:\n%s", out)
	}
}

func TestRegistry_Gauges(t *testing.T) {
	r := New()
	r.RegisterGauge("quorum_db_pool_total_conns", "Total connections.", func() float64 { return 7 })
	out := render(r)
	if !strings.Contains(out, "# TYPE quorum_db_pool_total_conns gauge") {
		t.Errorf("gauge TYPE missing:\n%s", out)
	}
	if !strings.Contains(out, "quorum_db_pool_total_conns 7") {
		t.Errorf("gauge value missing:\n%s", out)
	}
}

func TestRegistry_ExpositionShape(t *testing.T) {
	r := New()
	r.ObserveRequest("GET", "/healthz", 200, time.Millisecond)
	out := render(r)
	// Every metric family must carry a HELP and TYPE line (Prometheus requires it).
	for _, want := range []string{
		"# HELP quorum_http_requests_total",
		"# TYPE quorum_http_requests_total counter",
		"# TYPE quorum_http_request_duration_seconds histogram",
		"# TYPE quorum_http_requests_in_flight gauge",
		`le="+Inf"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition missing %q:\n%s", want, out)
		}
	}
}
