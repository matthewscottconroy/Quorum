// Package metrics implements a tiny, dependency-free Prometheus text-format
// exposition for the HTTP server and database pool. It intentionally avoids the
// prometheus/client_golang dependency — the app's ethos is a minimal dependency
// footprint — at the cost of covering only the handful of metrics we actually
// scrape: request counts, request latency, in-flight requests, recovered
// panics, and DB pool saturation.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// latencyBuckets are the cumulative histogram upper bounds (seconds) for request
// duration — a standard spread from 5ms to 10s.
var latencyBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

type histogram struct {
	counts []uint64 // per-bucket cumulative counts (aligned to latencyBuckets)
	sum    float64
	count  uint64
}

func newHistogram() *histogram { return &histogram{counts: make([]uint64, len(latencyBuckets))} }

func (h *histogram) observe(v float64) {
	for i, b := range latencyBuckets {
		if v <= b {
			h.counts[i]++
		}
	}
	h.sum += v
	h.count++
}

// GaugeFunc is a named callback sampled at scrape time (e.g. DB pool stats).
type GaugeFunc struct {
	Name string
	Help string
	Fn   func() float64
}

// Registry accumulates metrics and renders them in Prometheus text format. It is
// safe for concurrent use.
type Registry struct {
	mu        sync.Mutex
	requests  map[string]uint64     // key -> count (http_requests_total)
	reqLabels map[string][3]string  // key -> {method, route, status}
	durs      map[string]*histogram // key -> latency histogram
	durLabels map[string][2]string  // key -> {method, route}
	gauges    []GaugeFunc           // sampled at render time
	counters  []*counter            // application counters (see RegisterCounter)
	inFlight  int64                 // atomic
	panics    uint64                // atomic
}

// counter is a named monotonic counter incremented from application code
// (e.g. dropped notifications). Concurrency-safe via atomic.
type counter struct {
	name string
	help string
	val  uint64
}

// Inc adds one to the counter.
func (c *counter) Inc() { atomic.AddUint64(&c.val, 1) }

// New constructs an empty Registry.
func New() *Registry {
	return &Registry{
		requests:  map[string]uint64{},
		reqLabels: map[string][3]string{},
		durs:      map[string]*histogram{},
		durLabels: map[string][2]string{},
	}
}

// RegisterGauge adds a gauge sampled each time metrics are rendered.
func (r *Registry) RegisterGauge(name, help string, fn func() float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges = append(r.gauges, GaugeFunc{Name: name, Help: help, Fn: fn})
}

// RegisterCounter creates a named counter and returns an increment function.
// The returned closure is safe to call concurrently.
func (r *Registry) RegisterCounter(name, help string) func() {
	c := &counter{name: name, help: help}
	r.mu.Lock()
	r.counters = append(r.counters, c)
	r.mu.Unlock()
	return c.Inc
}

// ObserveRequest records one completed HTTP request.
func (r *Registry) ObserveRequest(method, route string, status int, d time.Duration) {
	statusStr := fmt.Sprintf("%d", status)
	ckey := method + "\x00" + route + "\x00" + statusStr
	dkey := method + "\x00" + route
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.reqLabels[ckey]; !ok {
		r.reqLabels[ckey] = [3]string{method, route, statusStr}
	}
	r.requests[ckey]++
	h := r.durs[dkey]
	if h == nil {
		h = newHistogram()
		r.durs[dkey] = h
		r.durLabels[dkey] = [2]string{method, route}
	}
	h.observe(d.Seconds())
}

// IncInFlight / DecInFlight track concurrently-served requests.
func (r *Registry) IncInFlight() { atomic.AddInt64(&r.inFlight, 1) }
func (r *Registry) DecInFlight() { atomic.AddInt64(&r.inFlight, -1) }

// IncPanic counts a recovered panic.
func (r *Registry) IncPanic() { atomic.AddUint64(&r.panics, 1) }

// Render writes all metrics in Prometheus text exposition format.
func (r *Registry) Render(w io.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()

	fmt.Fprintf(w, "# HELP quorum_http_requests_total Total HTTP requests handled.\n")
	fmt.Fprintf(w, "# TYPE quorum_http_requests_total counter\n")
	for _, key := range sortedKeys(r.requests) {
		l := r.reqLabels[key]
		fmt.Fprintf(w, "quorum_http_requests_total{method=%q,route=%q,status=%q} %d\n",
			l[0], escapeLabel(l[1]), l[2], r.requests[key])
	}

	fmt.Fprintf(w, "# HELP quorum_http_request_duration_seconds HTTP request latency.\n")
	fmt.Fprintf(w, "# TYPE quorum_http_request_duration_seconds histogram\n")
	dkeys := make([]string, 0, len(r.durs))
	for k := range r.durs {
		dkeys = append(dkeys, k)
	}
	sort.Strings(dkeys)
	for _, key := range dkeys {
		h := r.durs[key]
		l := r.durLabels[key]
		method, route := l[0], escapeLabel(l[1])
		for i, b := range latencyBuckets {
			fmt.Fprintf(w, "quorum_http_request_duration_seconds_bucket{method=%q,route=%q,le=%q} %d\n",
				method, route, formatBucket(b), h.counts[i])
		}
		fmt.Fprintf(w, "quorum_http_request_duration_seconds_bucket{method=%q,route=%q,le=\"+Inf\"} %d\n", method, route, h.count)
		fmt.Fprintf(w, "quorum_http_request_duration_seconds_sum{method=%q,route=%q} %g\n", method, route, h.sum)
		fmt.Fprintf(w, "quorum_http_request_duration_seconds_count{method=%q,route=%q} %d\n", method, route, h.count)
	}

	fmt.Fprintf(w, "# HELP quorum_http_requests_in_flight Requests currently being served.\n")
	fmt.Fprintf(w, "# TYPE quorum_http_requests_in_flight gauge\n")
	fmt.Fprintf(w, "quorum_http_requests_in_flight %d\n", atomic.LoadInt64(&r.inFlight))

	fmt.Fprintf(w, "# HELP quorum_http_panics_total Recovered panics in HTTP handlers.\n")
	fmt.Fprintf(w, "# TYPE quorum_http_panics_total counter\n")
	fmt.Fprintf(w, "quorum_http_panics_total %d\n", atomic.LoadUint64(&r.panics))

	for _, c := range r.counters {
		fmt.Fprintf(w, "# HELP %s %s\n", c.name, c.help)
		fmt.Fprintf(w, "# TYPE %s counter\n", c.name)
		fmt.Fprintf(w, "%s %d\n", c.name, atomic.LoadUint64(&c.val))
	}

	for _, g := range r.gauges {
		fmt.Fprintf(w, "# HELP %s %s\n", g.Name, g.Help)
		fmt.Fprintf(w, "# TYPE %s gauge\n", g.Name)
		fmt.Fprintf(w, "%s %g\n", g.Name, g.Fn())
	}
}

func sortedKeys(m map[string]uint64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// formatBucket renders a bucket bound without a trailing ".0" mismatch; Prometheus
// treats le as a string label, so a stable representation is all that matters.
func formatBucket(b float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", b), "0"), ".")
}

// escapeLabel escapes the characters Prometheus reserves in a label value.
func escapeLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}
