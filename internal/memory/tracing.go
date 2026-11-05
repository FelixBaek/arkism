package memory

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FelixBaek/arkism/internal/httpguard"
)

// Request tracing: W3C trace context (the traceparent header) and an optional
// OTLP/HTTP JSON span exporter, standard library only. It is off until
// StartTracing is called (memoryd: ARKISM_OTLP_ENDPOINT); off, every helper
// here is a no-op. A span carries names, ids, routes and status codes only,
// never memory text, queries or tokens. traceparent goes out only to loopback
// and private-network model servers, never to a hosted provider.

const (
	spanKindInternal = 1
	spanKindServer   = 2
	spanKindClient   = 3
	traceQueueSize   = 2048
	traceBatchSize   = 256
	traceFlushEvery  = 2 * time.Second
)

type spanContext struct {
	TraceID [16]byte
	SpanID  [8]byte
	Sampled bool
}

func (sc spanContext) valid() bool { return sc.TraceID != [16]byte{} && sc.SpanID != [8]byte{} }

// traceparent is the W3C header value of sc (version 00).
func (sc spanContext) traceparent() string {
	flags := "-00"
	if sc.Sampled {
		flags = "-01"
	}
	return "00-" + hex.EncodeToString(sc.TraceID[:]) + "-" + hex.EncodeToString(sc.SpanID[:]) + flags
}

// parseTraceparent reads a W3C traceparent header; ok is false for anything malformed.
func parseTraceparent(h string) (spanContext, bool) {
	var sc spanContext
	parts := strings.Split(strings.TrimSpace(h), "-")
	if len(parts) < 4 || len(parts[0]) != 2 || parts[0] == "ff" || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 ||
		parts[0] == "00" && len(parts) != 4 || parts[0] != strings.ToLower(parts[0]) {
		return sc, false
	}
	if _, err := hex.Decode(sc.TraceID[:], []byte(parts[1])); err != nil {
		return sc, false
	}
	if _, err := hex.Decode(sc.SpanID[:], []byte(parts[2])); err != nil {
		return sc, false
	}
	flags, err := hex.DecodeString(parts[3])
	if err != nil {
		return sc, false
	}
	sc.Sampled = flags[0]&1 == 1
	return sc, sc.valid()
}

type span struct {
	exporter *spanExporter
	sc       spanContext
	parent   [8]byte
	name     string
	kind     int
	start    time.Time
	attrs    map[string]string
	errCode  string
	ended    atomic.Bool
}

type spanKey struct{}

var activeTracer atomic.Pointer[spanExporter]

func newSpan(ctx context.Context, name string, kind int, parent spanContext) (context.Context, *span) {
	exp := activeTracer.Load()
	if exp == nil {
		return ctx, nil
	}
	sp := &span{exporter: exp, name: name, kind: kind, start: time.Now(), attrs: map[string]string{}}
	if parent.valid() { // the caller's sampling decision holds for the whole trace
		sp.sc.TraceID, sp.parent, sp.sc.Sampled = parent.TraceID, parent.SpanID, parent.Sampled
	} else {
		_, _ = rand.Read(sp.sc.TraceID[:])
		sp.sc.Sampled = exp.ratio >= 1 || mrand.Float64() < exp.ratio
	}
	_, _ = rand.Read(sp.sc.SpanID[:])
	return context.WithValue(ctx, spanKey{}, sp), sp
}

// startSpan opens a child of the span in ctx (or a new trace). The returned
// span is nil when tracing is off; its methods accept nil.
func startSpan(ctx context.Context, name string) (context.Context, *span) {
	var parent spanContext
	if p, ok := ctx.Value(spanKey{}).(*span); ok {
		parent = p.sc
	}
	return newSpan(ctx, name, spanKindInternal, parent)
}

// startServerSpan opens the span of one HTTP request, continuing the caller's
// trace when it sent a valid traceparent.
func startServerSpan(r *http.Request, route string) (*http.Request, *span) {
	if activeTracer.Load() == nil {
		return r, nil
	}
	parent, _ := parseTraceparent(r.Header.Get("traceparent"))
	ctx, sp := newSpan(r.Context(), route, spanKindServer, parent)
	sp.set("http.request.method", r.Method)
	sp.set("http.route", route)
	return r.WithContext(ctx), sp
}

// injectTraceparent marks an outgoing model request as a child of the span in
// ctx. Only a loopback or private-network endpoint (a self-hosted server that
// may join the trace) gets it; a hosted provider learns no trace ids.
func injectTraceparent(ctx context.Context, endpoint string, h http.Header) {
	sp, ok := ctx.Value(spanKey{}).(*span)
	if !ok || sp == nil {
		return
	}
	if u, err := url.Parse(endpoint); err == nil && isPrivateNetworkHost(u.Hostname()) {
		h.Set("traceparent", sp.sc.traceparent())
	}
}

// isPrivateNetworkHost: localhost, or a literal address the outbound guard counts as
// private (httpguard.PrivateIP). Other host names are not resolved, so they count as remote.
func isPrivateNetworkHost(host string) bool {
	return isLoopbackHost(host) || httpguard.PrivateIP(net.ParseIP(host))
}

func (sp *span) set(key, value string) {
	if sp != nil && value != "" {
		sp.attrs[key] = value
	}
}

// end closes the span once; err sets an error status with its fixed code, never its text.
func (sp *span) end(err error) {
	if sp == nil || !sp.ended.CompareAndSwap(false, true) {
		return
	}
	if err != nil {
		sp.errCode = captureErrorCode(err)
	}
	sp.exporter.enqueue(sp, time.Now())
}

// endHTTP closes a server span with the response status; 5xx is an error.
func (sp *span) endHTTP(status int) {
	if sp == nil {
		return
	}
	sp.set("http.response.status_code", strconv.Itoa(status))
	if status >= 500 {
		sp.errCode = "http_" + strconv.Itoa(status)
	}
	if sp.ended.CompareAndSwap(false, true) {
		sp.exporter.enqueue(sp, time.Now())
	}
}

type finishedSpan struct {
	span *span
	end  time.Time
}

type spanExporter struct {
	endpoint string
	service  string
	ratio    float64
	client   *http.Client
	queue    chan finishedSpan
	quit     chan struct{}
	// exported, dropped (queue full or stopped) and failed (collector error) spans.
	exported, dropped, failed atomic.Uint64
}

// enqueue never blocks and never panics: the queue is never closed.
func (e *spanExporter) enqueue(sp *span, end time.Time) {
	if !sp.sc.Sampled {
		return
	}
	select {
	case <-e.quit:
		e.dropped.Add(1)
	case e.queue <- finishedSpan{sp, end}:
	default:
		e.dropped.Add(1) // a slow collector never slows a request
	}
}

// TracingMetrics counts this process's exported spans; nil when tracing is off.
type TracingMetrics struct {
	Exported    uint64  `json:"exported_spans"`
	Dropped     uint64  `json:"dropped_spans"`
	Failed      uint64  `json:"failed_spans"`
	SampleRatio float64 `json:"sample_ratio"`
}

func tracingMetrics() *TracingMetrics {
	e := activeTracer.Load()
	if e == nil {
		return nil
	}
	return &TracingMetrics{Exported: e.exported.Load(), Dropped: e.dropped.Load(), Failed: e.failed.Load(), SampleRatio: e.ratio}
}

// StartTracing turns tracing on and exports spans to endpoint (an OTLP/HTTP
// traces URL such as https://collector:4318/v1/traces; plain http only on
// loopback). sampleRatio in (0,1] keeps that share of new traces (0 or out of
// range keeps all); a caller's traceparent decides for its own trace. stop
// flushes what is queued (at most 5 seconds) and turns tracing off.
func StartTracing(endpoint, service string, sampleRatio float64) (stop func(), err error) {
	if err := validateEndpoint(endpoint); err != nil {
		return nil, fmt.Errorf("otlp endpoint: %w", err)
	}
	if strings.TrimSpace(service) == "" {
		service = "arkism-memoryd"
	}
	if !(sampleRatio > 0 && sampleRatio <= 1) {
		sampleRatio = 1
	}
	e := &spanExporter{endpoint: endpoint, service: service, ratio: sampleRatio, client: providerClient(nil, 5*time.Second),
		queue: make(chan finishedSpan, traceQueueSize), quit: make(chan struct{})}
	done := make(chan struct{})
	go e.run(done)
	activeTracer.Store(e)
	var once sync.Once
	return func() {
		once.Do(func() {
			activeTracer.CompareAndSwap(e, nil)
			close(e.quit)
			<-done
		})
	}, nil
}

func (e *spanExporter) run(done chan struct{}) {
	defer close(done)
	tick := time.NewTicker(traceFlushEvery)
	defer tick.Stop()
	batch := make([]finishedSpan, 0, traceBatchSize)
	for {
		select {
		case fs := <-e.queue:
			if batch = append(batch, fs); len(batch) >= traceBatchSize {
				e.flush(context.Background(), batch)
				batch = batch[:0]
			}
		case <-tick.C:
			e.flush(context.Background(), batch)
			batch = batch[:0]
		case <-e.quit:
			e.drain(batch)
			return
		}
	}
}

// drain sends what is queued at stop within one shared 5-second budget.
func (e *spanExporter) drain(batch []finishedSpan) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		select {
		case fs := <-e.queue:
			if batch = append(batch, fs); len(batch) >= traceBatchSize {
				e.flush(ctx, batch)
				batch = batch[:0]
			}
		default:
			e.flush(ctx, batch)
			return
		}
	}
}

func (e *spanExporter) flush(parent context.Context, batch []finishedSpan) {
	if len(batch) == 0 {
		return
	}
	n := uint64(len(batch))
	body, err := json.Marshal(otlpPayload(e.service, batch))
	if err != nil {
		e.failed.Add(n)
		return
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		e.failed.Add(n)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		e.failed.Add(n)
		slog.Warn("trace export failed", "spans", n, "error", captureErrorCode(err))
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) // lets the connection be reused
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		e.failed.Add(n)
		slog.Warn("trace export refused", "spans", n, "status", resp.StatusCode)
		return
	}
	e.exported.Add(n)
}

type otlpValue struct {
	StringValue string `json:"stringValue"`
}

type otlpAttr struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

type otlpStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

type otlpSpan struct {
	TraceID      string     `json:"traceId"`
	SpanID       string     `json:"spanId"`
	ParentSpanID string     `json:"parentSpanId,omitempty"`
	Name         string     `json:"name"`
	Kind         int        `json:"kind"`
	Start        string     `json:"startTimeUnixNano"`
	End          string     `json:"endTimeUnixNano"`
	Attributes   []otlpAttr `json:"attributes,omitempty"`
	Status       otlpStatus `json:"status"`
}

// otlpPayload is the OTLP/HTTP JSON body (ExportTraceServiceRequest) of batch.
func otlpPayload(service string, batch []finishedSpan) map[string]any {
	spans := make([]otlpSpan, 0, len(batch))
	for _, fs := range batch {
		sp := fs.span
		o := otlpSpan{TraceID: hex.EncodeToString(sp.sc.TraceID[:]), SpanID: hex.EncodeToString(sp.sc.SpanID[:]), Name: sp.name, Kind: sp.kind,
			Start: strconv.FormatInt(sp.start.UnixNano(), 10), End: strconv.FormatInt(fs.end.UnixNano(), 10), Status: otlpStatus{Code: 1}}
		if sp.parent != [8]byte{} {
			o.ParentSpanID = hex.EncodeToString(sp.parent[:])
		}
		for k, v := range sp.attrs {
			o.Attributes = append(o.Attributes, otlpAttr{k, otlpValue{v}})
		}
		if sp.errCode != "" {
			o.Status = otlpStatus{Code: 2, Message: sp.errCode}
		}
		spans = append(spans, o)
	}
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": []otlpAttr{{"service.name", otlpValue{service}}}},
		"scopeSpans": []any{map[string]any{"scope": map[string]string{"name": "arkism"}, "spans": spans}},
	}}}
}
