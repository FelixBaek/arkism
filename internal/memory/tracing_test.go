//go:build linux

package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseTraceparent(t *testing.T) {
	good := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	sc, ok := parseTraceparent(good)
	if !ok || sc.traceparent() != good {
		t.Fatal("valid header", sc.traceparent())
	}
	for _, bad := range []string{"", "00-00000000000000000000000000000000-00f067aa0ba902b7-01", "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01x",
		"00-4bf92f3577b34da6a3ce929d0e0e473z-00f067aa0ba902b7-01", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra"} {
		if _, ok := parseTraceparent(bad); ok {
			t.Fatal("accepted", bad)
		}
	}
}

type otlpCollector struct {
	mu    sync.Mutex
	spans []otlpSpan
}

func (c *otlpCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []otlpSpan `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if r.URL.Path != "/v1/traces" || json.NewDecoder(r.Body).Decode(&body) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rs := range body.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			c.spans = append(c.spans, ss.Spans...)
		}
	}
}

func TestTracingOffIsNoop(t *testing.T) {
	ctx, sp := startSpan(testCtx, "x")
	sp.set("k", "v")
	sp.end(errors.New("boom"))
	h := http.Header{}
	injectTraceparent(ctx, "http://127.0.0.1/embed", h)
	if sp != nil || h.Get("traceparent") != "" {
		t.Fatal("tracing ran while off")
	}
}

func TestTracingExportsRequestAndModelSpans(t *testing.T) {
	col := &otlpCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()
	if _, err := StartTracing("http://example.com/v1/traces", "", 1); !errors.Is(err, ErrInvalid) {
		t.Fatal("plain http to a remote collector accepted", err)
	}
	stop, err := StartTracing(srv.URL+"/v1/traces", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	var sent string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer provider.Close()
	f := newFixture(t)
	f.svc.Reranker, _ = NewTEIReranker(TEIRerankConfig{URL: provider.URL})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil).WithContext(testCtx)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	req, server := startServerSpan(req, "GET /x")
	if _, callErr, _ := f.svc.meteredRerank(req.Context(), f.svc.Reranker, scopeA, "q", []string{"a", "b"}); callErr == nil {
		t.Fatal("provider failure lost")
	}
	server.endHTTP(http.StatusOK)
	Handler(f.svc).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	stop()
	byName := map[string]otlpSpan{}
	for _, s := range col.spans {
		byName[s.Name] = s
	}
	root, rerank := byName["GET /x"], byName["model.rerank"]
	if root.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || root.ParentSpanID != "00f067aa0ba902b7" || root.Kind != spanKindServer {
		t.Fatalf("server span %+v", root)
	}
	if rerank.TraceID != root.TraceID || rerank.ParentSpanID != root.SpanID || rerank.Status.Code != 2 {
		t.Fatalf("model span %+v", rerank)
	}
	if sent != "00-"+root.TraceID+"-"+rerank.SpanID+"-01" {
		t.Fatal("provider call did not carry the span", sent)
	}
	if _, ok := byName["GET /healthz"]; !ok {
		t.Fatal("middleware span missing", byName)
	}
}

// rawCollector keeps every exported body byte for byte.
type rawCollector struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (c *rawCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.bodies = append(c.bodies, b)
	c.mu.Unlock()
}

// The exported spans carry ids, names, routes, timings and fixed codes only:
// no query, memory text, model prompt, secret or private span, whatever the
// request contained.
func TestTracingExportsNoContent(t *testing.T) {
	col := &rawCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()
	stop, err := StartTracing(srv.URL+"/v1/traces", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	f := newFixture(t)
	f.svc.Wiki = traceLLM{}
	const query, text, secret, private = "QRYzz91", "MEMzz92 기억", "sk-live-ZZ93abcdefghijklmnopqrstuv", "PRIVzz94"
	f.activeMemory(text+" "+query, query)
	h := Handler(f.svc)
	body := `{"scope":{"workspace_id":"` + scopeA.WorkspaceID + `","project_id":"` + scopeA.ProjectID + `","user_id":"` + scopeA.UserID + `","agent_id":"` + scopeA.AgentID + `"},"query":"` + query + ` ` + secret + ` <private>` + private + `</private>"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(body)).WithContext(testCtx)
	req.Header.Set("Authorization", "Bearer "+f.human)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	ctx, sp := startJobSpan(testCtx, "job", "job-1")
	_, _, _ = f.svc.modelCall(ctx, scopeA, "wiki.page", "sys "+secret, "user "+text+" <private>"+private+"</private>", 10)
	_, _, _ = f.svc.meteredEmbed(ctx, scopeA, "embedding_query", []string{text, query}, "query")
	sp.end(errors.New(text))
	stop()
	all := bytes.Join(col.bodies, nil)
	if len(all) == 0 || !bytes.Contains(all, []byte("POST /v1/search")) || !bytes.Contains(all, []byte("model.complete")) || !bytes.Contains(all, []byte("model.embed")) ||
		!bytes.Contains(all, []byte(`"key":"gen_ai.usage.input_tokens","value":{"intValue":"10"}`)) || !bytes.Contains(all, []byte(`"key":"gen_ai.operation.name","value":{"stringValue":"embeddings"}`)) {
		t.Fatalf("spans missing (search status %d): %s", rec.Code, all)
	}
	for _, leak := range []string{query, "MEMzz92", secret, private, "user ", "sys "} {
		if bytes.Contains(all, []byte(leak)) {
			t.Fatalf("span export leaked %q: %s", leak, all)
		}
	}
	assertSpanAttributesAllowed(t, col.bodies)
}

// assertSpanAttributesAllowed fails on any exported span attribute outside the fixed allow-list.
func assertSpanAttributesAllowed(t *testing.T, bodies [][]byte) {
	t.Helper()
	allowed := map[string]bool{"service.name": true, "http.request.method": true, "http.route": true, "http.response.status_code": true, "arkism.operation": true, "arkism.collection": true,
		"gen_ai.operation.name": true, "gen_ai.provider.name": true, "gen_ai.request.model": true, "gen_ai.response.model": true,
		"gen_ai.usage.input_tokens": true, "gen_ai.usage.output_tokens": true}
	var payload struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []otlpSpan `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	for _, b := range bodies {
		if err := json.Unmarshal(b, &payload); err != nil {
			t.Fatal(err)
		}
		for _, rs := range payload.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, s := range ss.Spans {
					assertSpanAttributeKeys(t, allowed, s)
				}
			}
		}
	}
}

func assertSpanAttributeKeys(t *testing.T, allowed map[string]bool, s otlpSpan) {
	t.Helper()
	for _, a := range s.Attributes {
		if !allowed[a.Key] {
			t.Fatalf("unexpected span attribute %q=%q", a.Key, a.Value.StringValue)
		}
	}
}

func TestTraceparentStaysOffHostedProviders(t *testing.T) {
	stop, err := StartTracing("http://127.0.0.1:1/v1/traces", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	ctx, sp := startSpan(testCtx, "x")
	defer sp.end(nil)
	for endpoint, want := range map[string]bool{"https://api.example.com/v1/embeddings": false, "https://8.8.8.8/embed": false, "not a url\x00": false,
		"http://127.0.0.1:8080/embed": true, "http://localhost/rerank": true, "https://10.2.3.4/embed": true, "https://[fd00::1]/embed": true, "https://100.64.1.2/embed": true} {
		h := http.Header{}
		injectTraceparent(ctx, endpoint, h)
		if (h.Get("traceparent") != "") != want {
			t.Fatal("traceparent to", endpoint, h)
		}
	}
}

// A caller that did not sample its trace gets no spans exported for it.
func TestTracingHonoursCallerSampling(t *testing.T) {
	col := &otlpCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()
	stop, err := StartTracing(srv.URL+"/v1/traces", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00")
	req, server := startServerSpan(req, "GET /x")
	_, child := startSpan(req.Context(), "child")
	h := http.Header{}
	injectTraceparent(req.Context(), "http://127.0.0.1/embed", h)
	child.end(nil)
	server.endHTTP(200)
	stop()
	if len(col.spans) != 0 || !strings.HasSuffix(h.Get("traceparent"), "-00") {
		t.Fatal("unsampled trace exported", col.spans, h)
	}
}

// A collector that never answers costs at most the queue and the stop budget:
// spans past the queue are dropped, stop returns, the exporter goroutine ends,
// and a span ending after stop neither blocks nor panics.
func TestTracingExporterIsBounded(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	before := runtime.NumGoroutine()
	stop, err := StartTracing(srv.URL+"/v1/traces", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < traceQueueSize+traceBatchSize*2; i++ {
		_, sp := startSpan(testCtx, "s")
		sp.end(nil)
	}
	m := tracingMetrics()
	if m == nil || m.Dropped == 0 {
		t.Fatal("full queue did not drop", m)
	}
	_, late := startSpan(testCtx, "late")
	began := time.Now()
	stop()
	if took := time.Since(began); took > 12*time.Second {
		t.Fatal("stop waited on the collector", took)
	}
	late.end(nil) // after stop: must not panic or block
	if tracingMetrics() != nil {
		t.Fatal("tracing still on after stop")
	}
	srv.CloseClientConnections()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+2 {
		t.Fatal("exporter goroutines leaked", before, n)
	}
}
