package memory

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
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
	injectTraceparent(ctx, h)
	if sp != nil || h.Get("traceparent") != "" {
		t.Fatal("tracing ran while off")
	}
}

func TestTracingExportsRequestAndModelSpans(t *testing.T) {
	col := &otlpCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()
	if _, err := StartTracing("http://example.com/v1/traces", ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("plain http to a remote collector accepted", err)
	}
	stop, err := StartTracing(srv.URL+"/v1/traces", "")
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
	if _, callErr, _ := f.svc.meteredRerank(req.Context(), scopeA, "q", []string{"a", "b"}); callErr == nil {
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
