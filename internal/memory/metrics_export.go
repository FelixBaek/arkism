package memory

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Process metrics in two opt-in exports built from one snapshot
// (metricFamilies): Prometheus text exposition on GET /metrics/prometheus
// (Service.Prometheus, memoryd ARKISM_PROMETHEUS=1) and an OTLP/HTTP JSON
// metrics push (StartMetricsExport, memoryd ARKISM_OTLP_METRICS_ENDPOINT).
// Labels are routes, status codes, operation, provider and model names only:
// fixed, configuration-bounded sets, never user input. The JSON GET /metrics
// stays the scoped operator view of the same counters plus store queues.

type metricPoint struct {
	labels [][2]string
	value  float64
	// cumulative counts per family bound (a histogram), plus count and sum.
	buckets   []uint64
	count     uint64
	sum       float64
	histogram bool
}

type metricFamily struct {
	name, help, kind string // kind: counter | gauge | histogram
	bounds           []float64
	points           []metricPoint
}

// ------------------------------------------------------------------ model calls

var (
	llmTokenBounds = []float64{16, 64, 256, 1024, 4096, 16384, 65536, 262144}
	llmStats       = &llmMetrics{series: map[[3]string]*llmSeries{}}
)

const llmMetricsMaxSeries = 512

type llmSeries struct {
	calls, errors             uint64
	duration                  histogramCounts
	inputTokens, outputTokens histogramCounts
	cachedTokens              histogramCounts
	reasoningTokens           histogramCounts
}

type histogramCounts struct {
	buckets []uint64
	count   uint64
	sum     float64
}

func (h *histogramCounts) observe(bounds []float64, v float64) {
	if h.buckets == nil {
		h.buckets = make([]uint64, len(bounds))
	}
	h.count++
	h.sum += v
	for i, b := range bounds {
		if v <= b {
			h.buckets[i]++
		}
	}
}

// metricLabel keeps a configured name as a label value; anything unusual,
// secret-looking or long becomes "other", so a label never carries user text.
func metricLabel(v string) string {
	if !validRunLabel(v, 128) || containsSecret(v) {
		return "other"
	}
	return v
}

type llmMetrics struct {
	mu     sync.Mutex
	series map[[3]string]*llmSeries
	// overflow counts calls past llmMetricsMaxSeries label sets.
	overflow uint64
}

// observeLLMCall counts one model call in this process (meteredCall).
func observeLLMCall(operation, provider, model string, ok bool, elapsed time.Duration, usage ProviderUsage) {
	llmStats.mu.Lock()
	defer llmStats.mu.Unlock()
	key := [3]string{metricLabel(operation), metricLabel(provider), metricLabel(model)}
	s := llmStats.series[key]
	if s == nil {
		if len(llmStats.series) >= llmMetricsMaxSeries {
			llmStats.overflow++
			return
		}
		s = &llmSeries{}
		llmStats.series[key] = s
	}
	s.calls++
	if !ok {
		s.errors++
	}
	s.duration.observe(latencyBounds[:], elapsed.Seconds())
	s.inputTokens.observe(llmTokenBounds, float64(usage.InputTokens))
	s.outputTokens.observe(llmTokenBounds, float64(usage.OutputTokens))
	s.cachedTokens.observe(llmTokenBounds, float64(usage.CachedInputTokens))
	s.reasoningTokens.observe(llmTokenBounds, float64(usage.ReasoningTokens))
}

func llmFamilies() []metricFamily {
	llmStats.mu.Lock()
	defer llmStats.mu.Unlock()
	calls := metricFamily{name: "arkism_llm_calls_total", help: "Model calls made by this process.", kind: "counter"}
	dur := metricFamily{name: "arkism_llm_call_duration_seconds", help: "Model call latency.", kind: "histogram", bounds: latencyBounds[:]}
	in := metricFamily{name: "arkism_llm_input_tokens", help: "Input tokens per model call.", kind: "histogram", bounds: llmTokenBounds}
	out := metricFamily{name: "arkism_llm_output_tokens", help: "Output tokens per model call.", kind: "histogram", bounds: llmTokenBounds}
	cached := metricFamily{name: "arkism_llm_cached_input_tokens", help: "Input tokens per model call the provider served from its prompt cache.", kind: "histogram", bounds: llmTokenBounds}
	reasoning := metricFamily{name: "arkism_llm_reasoning_tokens", help: "Output tokens per model call the model spent thinking, when the provider says.", kind: "histogram", bounds: llmTokenBounds}
	for key, s := range llmStats.series {
		labels := [][2]string{{"operation", key[0]}, {"provider", key[1]}, {"model", key[2]}}
		calls.points = append(calls.points,
			metricPoint{labels: append(slicesClone(labels), [2]string{"status", "ok"}), value: float64(s.calls - s.errors)},
			metricPoint{labels: append(slicesClone(labels), [2]string{"status", "error"}), value: float64(s.errors)})
		dur.points = append(dur.points, histogramPoint(labels, s.duration))
		in.points = append(in.points, histogramPoint(labels, s.inputTokens))
		out.points = append(out.points, histogramPoint(labels, s.outputTokens))
		cached.points = append(cached.points, histogramPoint(labels, s.cachedTokens))
		reasoning.points = append(reasoning.points, histogramPoint(labels, s.reasoningTokens))
	}
	dropped := metricFamily{name: "arkism_llm_calls_unlabeled_total", help: "Model calls not counted by label set (series cap reached).", kind: "counter",
		points: []metricPoint{{value: float64(llmStats.overflow)}}}
	return []metricFamily{calls, dur, in, out, cached, reasoning, dropped}
}

func slicesClone(l [][2]string) [][2]string { return append([][2]string(nil), l...) }

func histogramPoint(labels [][2]string, h histogramCounts) metricPoint {
	return metricPoint{labels: labels, buckets: h.buckets, count: h.count, sum: h.sum, histogram: true}
}

// ------------------------------------------------------------------ snapshot

// PoolStats is a database connection pool's state; Postgres implements poolStatser.
type PoolStats struct {
	Acquired, Idle, Total, Max      int64
	Acquires, EmptyAcquires, Cancel int64
	AcquireSeconds                  float64
}

type poolStatser interface{ PoolStats() (PoolStats, bool) }

var processStart = time.Now()

// lastRequestStats is the request counters of the last handler built, for the OTLP metrics push.
var lastRequestStats atomic.Pointer[requestMetrics]

// metricFamilies is the one snapshot both exports encode.
func (s *Service) metricFamilies(stats *requestMetrics) []metricFamily {
	var out []metricFamily
	if stats != nil {
		out = append(out, requestFamilies(stats.snapshot())...)
		out = append(out, gauge("arkism_http_requests_in_flight", "HTTP requests being served.", float64(stats.inFlight.Load())))
	}
	out = append(out, llmFamilies()...)
	out = append(out, stageFamilies()...)
	out = append(out, s.queueFamilies()...)
	if s.Reranker != nil {
		m := s.Reranker.Metrics()
		out = append(out, counter("arkism_rerank_calls_total", "Rerank calls.", float64(m.Calls)),
			counter("arkism_rerank_fallbacks_total", "Rerank calls that served the fused order.", float64(m.Fallbacks)),
			counter("arkism_rerank_timeouts_total", "Rerank calls that timed out.", float64(m.Timeouts)))
	}
	if t := tracingMetrics(); t != nil {
		out = append(out, metricFamily{name: "arkism_trace_spans_total", help: "Finished spans by export outcome.", kind: "counter", points: []metricPoint{
			{labels: [][2]string{{"outcome", "exported"}}, value: float64(t.Exported)},
			{labels: [][2]string{{"outcome", "dropped"}}, value: float64(t.Dropped)},
			{labels: [][2]string{{"outcome", "failed"}}, value: float64(t.Failed)}}})
	}
	if p, ok := s.Repo.(poolStatser); ok {
		if st, ok := p.PoolStats(); ok {
			out = append(out, poolFamilies(st)...)
		}
	}
	return append(out, runtimeFamilies()...)
}

func counter(name, help string, v float64) metricFamily {
	return metricFamily{name: name, help: help, kind: "counter", points: []metricPoint{{value: v}}}
}

func gauge(name, help string, v float64) metricFamily {
	return metricFamily{name: name, help: help, kind: "gauge", points: []metricPoint{{value: v}}}
}

func requestFamilies(routes map[string]RequestMetric) []metricFamily {
	total := metricFamily{name: "arkism_http_requests_total", help: "HTTP requests by route and status.", kind: "counter"}
	dur := metricFamily{name: "arkism_http_request_duration_seconds", help: "HTTP request latency by route.", kind: "histogram", bounds: latencyBounds[:]}
	for route, row := range routes {
		for status, n := range row.StatusCodes {
			total.points = append(total.points, metricPoint{labels: [][2]string{{"route", route}, {"status", strconv.Itoa(status)}}, value: float64(n)})
		}
		dur.points = append(dur.points, metricPoint{labels: [][2]string{{"route", route}}, buckets: row.LatencyLeCounts[:], count: row.Count, sum: row.DurationSecondsSum, histogram: true})
	}
	return []metricFamily{total, dur}
}

func poolFamilies(st PoolStats) []metricFamily {
	return []metricFamily{
		gauge("arkism_db_pool_acquired_connections", "Connections in use.", float64(st.Acquired)),
		gauge("arkism_db_pool_idle_connections", "Idle connections.", float64(st.Idle)),
		gauge("arkism_db_pool_total_connections", "Open connections.", float64(st.Total)),
		gauge("arkism_db_pool_max_connections", "Pool size limit.", float64(st.Max)),
		counter("arkism_db_pool_acquires_total", "Successful connection acquires.", float64(st.Acquires)),
		counter("arkism_db_pool_empty_acquires_total", "Acquires that waited for a free connection.", float64(st.EmptyAcquires)),
		counter("arkism_db_pool_canceled_acquires_total", "Acquires canceled by their context.", float64(st.Cancel)),
		counter("arkism_db_pool_acquire_seconds_total", "Time spent acquiring connections.", st.AcquireSeconds),
	}
}

func runtimeFamilies() []metricFamily {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	out := []metricFamily{
		gauge("go_goroutines", "Goroutines that currently exist.", float64(runtime.NumGoroutine())),
		gauge("go_memstats_heap_alloc_bytes", "Heap bytes allocated and in use.", float64(ms.HeapAlloc)),
		gauge("go_memstats_heap_sys_bytes", "Heap bytes obtained from the system.", float64(ms.HeapSys)),
		gauge("go_memstats_sys_bytes", "Bytes obtained from the system.", float64(ms.Sys)),
		counter("go_gc_cycles_total", "Completed GC cycles.", float64(ms.NumGC)),
		counter("go_gc_pause_seconds_total", "Total GC stop-the-world pause.", float64(ms.PauseTotalNs)/1e9),
		gauge("process_start_time_seconds", "Process start time (Unix seconds).", float64(processStart.UnixNano())/1e9),
	}
	if cpu, ok := processCPUSeconds(); ok {
		out = append(out, counter("process_cpu_seconds_total", "User and system CPU time.", cpu))
	}
	if rss, ok := processRSSBytes(); ok {
		out = append(out, gauge("process_resident_memory_bytes", "Resident memory.", rss))
	}
	if entries, err := os.ReadDir("/proc/self/fd"); err == nil {
		out = append(out, gauge("process_open_fds", "Open file descriptors.", float64(len(entries))))
	}
	return out
}

// processRSSBytes reads VmRSS from /proc (Linux); elsewhere it is absent.
func processRSSBytes() (float64, bool) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			f := strings.Fields(rest)
			if len(f) == 2 && f[1] == "kB" {
				if kb, err := strconv.ParseFloat(f[0], 64); err == nil {
					return kb * 1024, true
				}
			}
		}
	}
	return 0, false
}

// ------------------------------------------------------------------ Prometheus

// writePrometheus encodes families in the Prometheus text format (0.0.4).
func writePrometheus(w io.Writer, families []metricFamily) {
	for _, f := range families {
		if len(f.points) == 0 {
			continue
		}
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.kind)
		sort.SliceStable(f.points, func(i, j int) bool { return promLabels(f.points[i].labels) < promLabels(f.points[j].labels) })
		for _, p := range f.points {
			if !p.histogram {
				fmt.Fprintf(w, "%s%s %s\n", f.name, promLabels(p.labels), promFloat(p.value))
				continue
			}
			for i, b := range f.bounds {
				var n uint64
				if i < len(p.buckets) {
					n = p.buckets[i]
				}
				fmt.Fprintf(w, "%s_bucket%s %d\n", f.name, promLabels(append(slicesClone(p.labels), [2]string{"le", promFloat(b)})), n)
			}
			fmt.Fprintf(w, "%s_bucket%s %d\n", f.name, promLabels(append(slicesClone(p.labels), [2]string{"le", "+Inf"})), p.count)
			fmt.Fprintf(w, "%s_sum%s %s\n%s_count%s %d\n", f.name, promLabels(p.labels), promFloat(p.sum), f.name, promLabels(p.labels), p.count)
		}
	}
}

func promLabels(labels [][2]string) string {
	if len(labels) == 0 {
		return ""
	}
	parts := make([]string, len(labels))
	for i, l := range labels {
		parts[i] = l[0] + `="` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(l[1]) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func promFloat(v float64) string {
	if math.IsInf(v, 1) {
		return "+Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// PrometheusConfig turns on GET /metrics/prometheus. With a Token the scraper
// sends it as a bearer token; without one only loopback peers are answered.
type PrometheusConfig struct {
	Enabled bool
	Token   string
}

// prometheusAllowed: the configured bearer token (constant time), or, with no
// token, a loopback peer. Behind a local reverse proxy every peer is loopback,
// so a proxied deployment sets a token.
func (c PrometheusConfig) allowed(r *http.Request) bool {
	if c.Token != "" {
		got, want := sha256.Sum256([]byte(bearer(r))), sha256.Sum256([]byte(c.Token)) // equal lengths: no length leak
		return subtle.ConstantTimeCompare(got[:], want[:]) == 1
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && isLoopbackHost(host)
}

func servePrometheus(s *Service, stats *requestMetrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.Prometheus.Enabled {
			respond(w, nil, ErrNotFound)
			return
		}
		if !s.Prometheus.allowed(r) {
			respond(w, nil, ErrDenied)
			return
		}
		w.Header().Set(headerContentType, "text/plain; version=0.0.4; charset=utf-8")
		writePrometheus(w, s.metricFamilies(stats))
	}
}

// ------------------------------------------------------------------ OTLP metrics

type otlpNumberPoint struct {
	Attributes []otlpAttr `json:"attributes,omitempty"`
	Start      string     `json:"startTimeUnixNano"`
	Time       string     `json:"timeUnixNano"`
	AsDouble   float64    `json:"asDouble"`
}

type otlpHistogramPoint struct {
	Attributes     []otlpAttr `json:"attributes,omitempty"`
	Start          string     `json:"startTimeUnixNano"`
	Time           string     `json:"timeUnixNano"`
	Count          string     `json:"count"`
	Sum            float64    `json:"sum"`
	BucketCounts   []string   `json:"bucketCounts"`
	ExplicitBounds []float64  `json:"explicitBounds"`
}

// otlpMetricsPayload is the OTLP/HTTP JSON body (ExportMetricsServiceRequest):
// cumulative sums, gauges and explicit-bucket histograms.
func otlpMetricsPayload(service string, families []metricFamily, now time.Time) map[string]any {
	start, ts := strconv.FormatInt(processStart.UnixNano(), 10), strconv.FormatInt(now.UnixNano(), 10)
	metrics := make([]map[string]any, 0, len(families))
	for _, f := range families {
		if len(f.points) == 0 {
			continue
		}
		m := map[string]any{"name": f.name, "description": f.help}
		switch f.kind {
		case "histogram":
			points := make([]otlpHistogramPoint, 0, len(f.points))
			for _, p := range f.points {
				points = append(points, otlpHistogramPoint{Attributes: otlpAttrs(p.labels), Start: start, Time: ts, Count: strconv.FormatUint(p.count, 10),
					Sum: p.sum, BucketCounts: otlpBuckets(p.buckets, len(f.bounds), p.count), ExplicitBounds: f.bounds})
			}
			m["histogram"] = map[string]any{"dataPoints": points, "aggregationTemporality": 2}
		case "counter":
			m["sum"] = map[string]any{"dataPoints": numberPoints(f.points, start, ts), "aggregationTemporality": 2, "isMonotonic": true}
		default:
			m["gauge"] = map[string]any{"dataPoints": numberPoints(f.points, start, ts)}
		}
		metrics = append(metrics, m)
	}
	return map[string]any{"resourceMetrics": []any{map[string]any{
		"resource":     map[string]any{"attributes": []otlpAttr{{"service.name", otlpValue{StringValue: service}}}},
		"scopeMetrics": []any{map[string]any{"scope": map[string]string{"name": "arkism"}, "metrics": metrics}},
	}}}
}

func otlpAttrs(labels [][2]string) []otlpAttr {
	out := make([]otlpAttr, 0, len(labels))
	for _, l := range labels {
		out = append(out, otlpAttr{l[0], otlpValue{StringValue: l[1]}})
	}
	return out
}

func numberPoints(points []metricPoint, start, ts string) []otlpNumberPoint {
	out := make([]otlpNumberPoint, 0, len(points))
	for _, p := range points {
		out = append(out, otlpNumberPoint{Attributes: otlpAttrs(p.labels), Start: start, Time: ts, AsDouble: p.value})
	}
	return out
}

// otlpBuckets turns cumulative le-counts into OTLP's per-bucket counts (one more
// than the bounds: the last is above every bound).
func otlpBuckets(cumulative []uint64, bounds int, count uint64) []string {
	out := make([]string, bounds+1)
	var prev uint64
	for i := range bounds {
		var c uint64
		if i < len(cumulative) {
			c = cumulative[i]
		}
		out[i] = strconv.FormatUint(c-prev, 10)
		prev = c
	}
	out[bounds] = strconv.FormatUint(count-prev, 10)
	return out
}

// StartMetricsExport pushes the process metrics to endpoint (an OTLP/HTTP
// metrics URL such as https://collector:4318/v1/metrics; plain http only on
// loopback) every interval (at least 5 seconds; 0 means 30). The request
// counters are those of the last handler built for s. stop sends a final export.
func StartMetricsExport(s *Service, endpoint, service string, every time.Duration) (stop func(), err error) {
	if err := validateEndpoint(endpoint); err != nil {
		return nil, fmt.Errorf("otlp metrics endpoint: %w", err)
	}
	if strings.TrimSpace(service) == "" {
		service = "arkism-memoryd"
	}
	if every == 0 {
		every = 30 * time.Second
	}
	every = max(every, 5*time.Second)
	client := providerClient(nil, 5*time.Second)
	push := func(ctx context.Context) {
		payload := otlpMetricsPayload(service, s.metricFamilies(lastRequestStats.Load()), time.Now())
		if err := otlpPost(ctx, client, endpoint, payload); err != nil {
			slog.Warn("metrics export failed", "error", err.Error())
		}
	}
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				push(context.Background())
			case <-quit:
				push(context.Background())
				return
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(quit); <-done }) }, nil
}
