package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func llmTraces(t *testing.T, f *fixture, scope Scope, operation string) []LLMRequestView {
	t.Helper()
	page, err := f.svc.ListLLMRequests(testCtx, f.human, scope, LLMRequestFilter{Operation: operation})
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

// Calls under a job share the job's trace id and name its span as parent, with tracing off.
func TestLLMTraceJobHierarchyWithTracingOff(t *testing.T) {
	f := newFixture(t)
	f.svc.Wiki = traceLLM{}
	ctx, sp := startJobSpan(testCtx, "job.wiki", "job-42")
	if sp != nil {
		t.Fatal("tracing should be off")
	}
	for range 2 {
		if _, _, err := f.svc.modelCall(ctx, scopeA, "wiki.page", "sys", "u", 10); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := f.svc.modelCall(testCtx, scopeA, "wiki.page", "sys", "u", 10); err != nil {
		t.Fatal(err)
	}
	items := llmTraces(t, f, scopeA, "wiki.page")
	if len(items) != 3 {
		t.Fatal("traces", items)
	}
	job := ctx.Value(llmJobKey{}).(llmJob)
	inJob, spans := 0, map[string]bool{}
	for _, it := range items {
		r := it.Request
		if len(r.TraceID) != 32 || len(r.SpanID) != 16 || spans[r.SpanID] {
			t.Fatalf("ids %+v", r)
		}
		spans[r.SpanID] = true
		if r.JobID == "job-42" {
			inJob++
			if r.TraceID != job.traceID || r.ParentSpanID != job.spanID {
				t.Fatalf("job call not under the job span %+v", r)
			}
		} else if r.ParentSpanID != "" || r.TraceID == job.traceID {
			t.Fatalf("call outside the job joined it %+v", r)
		}
		if r.Provider != wikiProvider || r.Generator == nil || r.Generator.Model != "trace-model" {
			t.Fatalf("provider/model %+v", r)
		}
	}
	if inJob != 2 {
		t.Fatal("job calls", inJob)
	}
	// The ledger row is still written for each call.
	rows, _ := f.repo.ListRecords(testCtx, scopeA, colProviderUsage, 10)
	if len(rows) != 3 {
		t.Fatal("ledger rows", len(rows))
	}
}

// With tracing on, the trace row carries the exported span's own ids.
func TestLLMTraceUsesSpanIDsWithTracingOn(t *testing.T) {
	stop, err := StartTracing("http://127.0.0.1:1/v1/traces", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	f := newFixture(t)
	f.svc.Wiki = traceLLM{}
	ctx, sp := startJobSpan(testCtx, "job.file_extract", "job-7")
	if _, _, err := f.svc.modelCall(ctx, scopeA, "file_extract", "sys", "u", 10); err != nil {
		t.Fatal(err)
	}
	sp.end(nil)
	r := llmTraces(t, f, scopeA, "file_extract")[0].Request
	traceID, spanID, _ := sp.ids()
	if r.TraceID != traceID || r.ParentSpanID != spanID || r.JobID != "job-7" {
		t.Fatalf("span ids not used %+v", r)
	}
}

func TestLLMTraceNamesReadAndCreatedMemories(t *testing.T) {
	f := newFixture(t)
	f.svc.Wiki = traceLLM{}
	ctx, note := withLLMNote(testCtx, "src-1", []string{"m1", "m2"})
	if _, _, err := f.svc.modelCall(ctx, scopeA, "file_extract", "sys", "u", 10); err != nil {
		t.Fatal(err)
	}
	f.svc.attachLLMCreated(ctx, scopeA, note, []string{"cand-1"})
	r := llmTraces(t, f, scopeA, "file_extract")[0].Request
	if r.DocumentID != "src-1" || strings.Join(r.SourceMemoryIDs, ",") != "m1,m2" || strings.Join(r.CreatedMemoryIDs, ",") != "cand-1" {
		t.Fatalf("memory ids %+v", r)
	}
	many := make([]string, 100)
	for i := range many {
		many[i] = "x"
	}
	_, n := withLLMNote(testCtx, "", many)
	if len(n.sources) != llmTraceMaxIDs {
		t.Fatal("source ids unbounded", len(n.sources))
	}
}

func TestLLMTraceOperationIncludeExclude(t *testing.T) {
	s := &Service{}
	if !s.traceOperation("anything") {
		t.Fatal("default records all")
	}
	s.LLMTraceInclude = []string{"embedding_*", "wiki.page"}
	s.LLMTraceExclude = []string{"embedding_review"}
	for op, want := range map[string]bool{"embedding_query": true, "embedding_review": false, "wiki.page": true, "wiki.pages": false, "rerank": false} {
		if s.traceOperation(op) != want {
			t.Error(op, want)
		}
	}
}

// The routing picker and skill analysis go through the shared ledger call and are traced.
type tracePicker struct{}

func (tracePicker) PickModel(context.Context, string, []ModelOption) (string, ProviderUsage, error) {
	return "fast", ProviderUsage{InputTokens: 3, OutputTokens: 1, TotalTokens: 4}, nil
}

func TestModelPickerCallIsTraced(t *testing.T) {
	f := newFixture(t)
	choice, ok := f.svc.pickModel(testCtx, tracePicker{}, scopeA, "task", []ModelOption{{ID: "fast", Name: "Fast"}})
	if !ok || choice != "fast" {
		t.Fatal("pick", choice, ok)
	}
	items := llmTraces(t, f, scopeA, "model_routing")
	if len(items) != 1 || items[0].Request.TotalTokens != 4 || items[0].Request.Provider != "cloudflare" {
		t.Fatal("routing trace", items)
	}
}

// A document's model call traces are deleted when the source is erased.
func TestSourceErasureDeletesItsLLMTraces(t *testing.T) {
	f := newFixture(t)
	f.svc.Wiki = traceLLM{}
	src := f.source()
	for _, doc := range []string{src.ID, "other-doc"} {
		ctx, _ := withLLMNote(testCtx, doc, nil)
		if _, _, err := f.svc.modelCall(ctx, scopeA, "file_extract", "sys", "u", 10); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); err != nil {
		t.Fatal(err)
	}
	token := erasureWorker(f)
	for range 10 {
		if err := f.svc.WorkOnce(testCtx, token); errors.Is(err, ErrNotFound) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	items := llmTraces(t, f, scopeA, "file_extract")
	if len(items) != 1 || items[0].Request.DocumentID != "other-doc" {
		t.Fatal("traces after erasure", items)
	}
}

// ------------------------------------------------------------------ audit

func auditRows(f *fixture, scope Scope) map[string]Record {
	rows, _ := f.repo.ListRecords(testCtx, scope, "audit_events", 500)
	out := map[string]Record{}
	for _, r := range rows {
		out[r.Kind] = r
	}
	return out
}

func writeAudit(t *testing.T, f *fixture, ctx context.Context, actions ...string) {
	t.Helper()
	err := f.repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		for _, a := range actions {
			if err := audit(ctx, tx, scopeA, "human-1", a, "x"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSpaceAuditSettingsNeverDropMandatoryEvents(t *testing.T) {
	f := newFixture(t)
	off := false
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{Audit: &SpaceAudit{Enabled: &off}}, false); err != nil {
		t.Fatal(err)
	}
	writeAudit(t, f, testCtx, "read:memories", "search:memories", "delete:memories", "read:sources", "read:artifacts", "settings:space", "brand_new_verb:x")
	got := auditRows(f, scopeA)
	for _, kept := range []string{"delete:memories", "read:sources", "read:artifacts", "settings:space", "brand_new_verb:x"} {
		if _, ok := got[kept]; !ok {
			t.Error("mandatory event dropped", kept)
		}
	}
	for _, dropped := range []string{"read:memories", "search:memories"} {
		if _, ok := got[dropped]; ok {
			t.Error("optional event kept with audit off", dropped)
		}
	}
	// An allowlist keeps only the optional actions it names.
	view, _ := f.svc.GetSpaceConfig(testCtx, f.human, scopeA)
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: view.Revision, Audit: &SpaceAudit{Actions: []string{"read:mem*"}}}, false); err != nil {
		t.Fatal(err)
	}
	writeAudit(t, f, testCtx, "read:memories", "read:skills")
	got = auditRows(f, scopeA)
	if _, ok := got["read:memories"]; !ok {
		t.Error("allowlisted action dropped")
	}
	if _, ok := got["read:skills"]; ok {
		t.Error("unlisted optional action kept")
	}
	// {} hands the space back to recording everything.
	view, _ = f.svc.GetSpaceConfig(testCtx, f.human, scopeA)
	if v, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: view.Revision, Audit: &SpaceAudit{}}, false); err != nil || v.Config.Audit != nil {
		t.Fatal("clear", v.Config.Audit, err)
	}
	many := make([]string, spaceAuditActionsMax+1)
	for i := range many {
		many[i] = "read:a" + strings.Repeat("b", i)
	}
	view, _ = f.svc.GetSpaceConfig(testCtx, f.human, scopeA)
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: view.Revision, Audit: &SpaceAudit{Actions: many}}, false); !errors.Is(err, ErrInvalid) {
		t.Fatal("unbounded allowlist accepted", err)
	}
}

func TestAuditMandatoryClassification(t *testing.T) {
	for action, want := range map[string]bool{"read:memories": false, "search": false, "preview:delete:memories": false, "read:sources": true,
		"read:session_archive_raw": true, "read:session_archive_messages": true, "read:session_archives": true, "read:source_derived:memories": true,
		"erase:source_memories": true, "export:memories": true, "decide:x": true, "": true,
		// writes, state changes and blocked content are never optional
		"wiki:edit": true, "wiki:source_removed": true, "inspection:blocked:v1": true, "ingest:replay": true, "propose:memories": true,
		"set_review_due:memories": true, "start:skill_trials": true, "claim:flow_effect": true, "use:merge_proposals": true,
		"generate:skills:reuse": true, "extract:entities": true, "judge:user_digests:keep": true, "knowledge:binding": true} {
		if auditMandatory(action) != want {
			t.Error(action, want)
		}
	}
}

func TestAuditBodyIsOptInRedactedAndCapped(t *testing.T) {
	f := newFixture(t)
	secret := "ghp_" + strings.Repeat("a1B2c3D4e5", 4)
	send := func(contentType, body string) context.Context {
		req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		req, _ = captureAuditBody(req.WithContext(withAuditRequest(testCtx, req, "POST /v1/x", time.Now())))
		if got, _ := io.ReadAll(req.Body); string(got) != body {
			t.Fatal("the handler must read the body unchanged")
		}
		return req.Context()
	}
	writeAudit(t, f, send("application/json; charset=utf-8", `{"text":"hi <private>pin 1234</private> `+secret+`"}`), "write:memories")
	body := auditRows(f, scopeA)["write:memories"].Audit.RequestBody
	if !strings.Contains(body, `"text":"hi`) || strings.Contains(body, "pin 1234") || strings.Contains(body, secret) {
		t.Fatalf("stored body %q", body)
	}
	writeAudit(t, f, send("application/json", `{"text":"`+strings.Repeat("가", 3000)+`"}`), "erase:memories")
	if b := auditRows(f, scopeA)["erase:memories"].Audit.RequestBody; len(b) > auditBodyBytes || !utf8.ValidString(b) || b == "" {
		t.Fatalf("capped body %d bytes", len(b))
	}
	writeAudit(t, f, send("text/plain", "plain words"), "delete:memories")
	if b := auditRows(f, scopeA)["delete:memories"].Audit.RequestBody; b != "" {
		t.Fatal("a non-JSON body was kept", b)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{}`))
	if req, c := captureAuditBody(req); req.Body.(io.ReadCloser) == nil || c.request.text() != "" {
		t.Fatal("a body without a JSON content type was captured")
	}
	if v := viewAuditEvent(auditRows(f, scopeA)["write:memories"]); v.RequestBody != body {
		t.Fatal("the detail view drops the body", v.RequestBody)
	}
}

func TestAuditResponseBodyIsAFollowUpEvent(t *testing.T) {
	f := newFixture(t)
	secret := "ghp_" + strings.Repeat("a1B2c3D4e5", 4)
	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{"q":1}`))
	req.Header.Set("Content-Type", "application/json")
	req, c := captureAuditBody(req.WithContext(withAuditRequest(context.WithValue(testCtx, auditRequestKey{}, "req-1"), req, "POST /v1/x", time.Now())))
	_, _ = io.ReadAll(req.Body)
	writeAudit(t, f, req.Context(), "write:memories", "search:memories")
	w := &statusWriter{ResponseWriter: httptest.NewRecorder(), tee: &c.response}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{"text":"ok <private>pin 1234</private> ` + secret + `","pad":"` + strings.Repeat("x", 5000) + `"}`))
	f.svc.auditResponse(req.Context(), c, http.StatusCreated)
	rows := auditRows(f, scopeA)
	first, resp := rows["write:memories"], rows["response:write:memories"]
	a := resp.Audit
	if a == nil || a.ResponseStatus != http.StatusCreated || a.RequestBody != "" || a.RequestID != "req-1" || first.Audit.RequestID != "req-1" || resp.Target == nil || resp.Target.ID != first.ID {
		t.Fatalf("response event %+v %+v", resp, a)
	}
	if !strings.Contains(a.ResponseBody, `"text":"ok`) || strings.Contains(a.ResponseBody, "pin 1234") || strings.Contains(a.ResponseBody, secret) || len(a.ResponseBody) > auditBodyBytes {
		t.Fatalf("response body %q", a.ResponseBody)
	}
	if _, twice := rows["response:search:memories"]; twice || !auditMandatory(resp.Kind) {
		t.Fatal("one response event per scope, always kept")
	}
	if v := viewAuditEvent(resp); v.ResponseBody != a.ResponseBody || v.ResponseStatus != http.StatusCreated {
		t.Fatalf("view %+v", v)
	}

	// Through the HTTP handler: the response event follows the request's own events.
	f.svc.AuditBodies = true
	put := httptest.NewRequest(http.MethodPut, "/v1/spaces/"+scopeA.WorkspaceID+"/"+scopeA.ProjectID+"/config", strings.NewReader(`{"notes":"audited"}`)).WithContext(testCtx)
	put.Header.Set("Authorization", "Bearer "+f.human)
	put.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(rec, put)
	found := false
	for kind, r := range auditRows(f, scopeA) {
		if strings.HasPrefix(kind, "response:") && kind != "response:write:memories" {
			found = r.Audit.ResponseStatus == rec.Code && strings.Contains(r.Audit.ResponseBody, "audited") && r.Audit.Route != ""
		}
	}
	if !found {
		t.Fatalf("no response event for the HTTP request (%d)", rec.Code)
	}

	// A non-JSON response keeps no body; a request that wrote no event writes no response event.
	req2, c2 := captureAuditBody(httptest.NewRequest(http.MethodGet, "/v1/y", nil).WithContext(testCtx))
	w2 := &statusWriter{ResponseWriter: httptest.NewRecorder(), tee: &c2.response}
	w2.Header().Set("Content-Type", "text/plain")
	_, _ = w2.Write([]byte("plain"))
	if c2.response.text() != "" {
		t.Fatal("a non-JSON response was kept")
	}
	before := len(auditRows(f, scopeA))
	f.svc.auditResponse(req2.Context(), c2, http.StatusOK)
	if len(auditRows(f, scopeA)) != before {
		t.Fatal("a request without audit events wrote a response event")
	}
}

func TestAuditEventCarriesRequestMetadata(t *testing.T) {
	f := newFixture(t)
	writeAudit(t, f, testCtx, "delete:memories")
	if a := auditRows(f, scopeA)["delete:memories"].Audit; a == nil || a.Transport != "system" || a.Route != "" {
		t.Fatalf("worker event %+v", a)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/x", nil)
	req.Header.Set(auditTransportHeader, "mcp")
	ctx := withAuditRequest(testCtx, req, "POST /v1/x", time.Now().Add(-20*time.Millisecond))
	ctx = context.WithValue(ctx, httpActorKey{}, &httpActor{principal: "agent-1", role: roleAgent})
	writeAudit(t, f, ctx, "erase:memories")
	ev := auditRows(f, scopeA)["erase:memories"]
	if a := ev.Audit; a.Transport != "mcp" || a.Route != "POST /v1/x" || a.Method != http.MethodPost || a.DurationMS < 20 {
		t.Fatalf("request event %+v", a)
	}
	// GET /v1/audit-events/{id}: the redacted detail, human only.
	h := Handler(f.svc)
	get := func(token, id string) *httptest.ResponseRecorder {
		q := "?workspace_id=" + scopeA.WorkspaceID + "&project_id=" + scopeA.ProjectID + "&user_id=" + scopeA.UserID + "&agent_id=" + scopeA.AgentID
		r := httptest.NewRequest(http.MethodGet, "/v1/audit-events/"+id+q, nil).WithContext(testCtx)
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	rec := get(f.human, ev.ID)
	var v AuditEventView
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &v) != nil || v.Transport != "mcp" || v.Route != "POST /v1/x" || !v.Mandatory || v.DurationMS < 20 {
		t.Fatalf("detail %d %s", rec.Code, rec.Body)
	}
	if rec = get(f.agent, ev.ID); rec.Code != http.StatusForbidden {
		t.Fatal("agent read audit detail", rec.Code)
	}
	if rec = get(f.human, "missing"); rec.Code != http.StatusNotFound {
		t.Fatal("missing event", rec.Code)
	}
}

// A caller cannot forge its transport: only an agent or service principal keeps
// a declared "mcp", any other label or role is "http", and "system" is never
// taken from a request.
func TestAuditTransportIsNotCallerControlled(t *testing.T) {
	for _, c := range []struct{ header, role, want string }{
		{"mcp", roleAgent, "mcp"}, {"mcp", roleService, "mcp"}, {"mcp", roleHuman, "http"}, {"mcp", "", "http"},
		{"MCP", roleAgent, "http"}, {"system", roleService, "http"}, {"", roleAgent, "http"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
		req.Header.Set(auditTransportHeader, c.header)
		ctx := withAuditRequest(testCtx, req, "GET /v1/x", time.Now())
		if got := auditMeta(ctx, c.role, AuditMetadata{}).Transport; got != c.want {
			t.Errorf("header %q role %q: %q, want %q", c.header, c.role, got, c.want)
		}
	}
	// The role the route check bound to ctx counts the same way.
	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	req.Header.Set(auditTransportHeader, "mcp")
	ctx := context.WithValue(withAuditRequest(testCtx, req, "GET /v1/x", time.Now()), httpActorKey{}, &httpActor{role: roleHuman})
	if got := auditMeta(ctx, "", AuditMetadata{}).Transport; got != "http" {
		t.Fatal("human claimed mcp", got)
	}
}

// Only a human changes a space's audit block, and even "off" keeps writes.
func TestAgentCannotSwitchOffSpaceAudit(t *testing.T) {
	f := newFixture(t)
	off := false
	body := strings.NewReader(`{"audit":{"enabled":false}}`)
	r := httptest.NewRequest(http.MethodPut, "/v1/spaces/"+scopeA.WorkspaceID+"/"+scopeA.ProjectID+"/config", body).WithContext(testCtx)
	r.Header.Set("Authorization", "Bearer "+f.agent)
	rec := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatal("agent changed space audit", rec.Code, rec.Body)
	}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.agent, scopeA, SpaceConfigInput{Audit: &SpaceAudit{Enabled: &off}}, false); err == nil {
		t.Fatal("agent changed space audit through the service")
	}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{Audit: &SpaceAudit{Enabled: &off, Actions: []string{"*"}}}, false); err != nil {
		t.Fatal(err)
	}
	writeAudit(t, f, testCtx, "wiki:edit", "propose:memories", "read:session_archive_messages", "read:memories")
	got := auditRows(f, scopeA)
	for _, kept := range []string{"wiki:edit", "propose:memories", "read:session_archive_messages"} {
		if _, ok := got[kept]; !ok {
			t.Error("space admin dropped mandatory event", kept)
		}
	}
	if _, ok := got["read:memories"]; ok {
		t.Error("optional read kept with audit off")
	}
}

// ------------------------------------------------------------------ metrics

func TestPrometheusEndpointIsOptInAndGuarded(t *testing.T) {
	f := newFixture(t)
	f.svc.Wiki = traceLLM{}
	if _, _, err := f.svc.modelCall(testCtx, scopeA, "wiki.page", "sys", "u", 10); err != nil {
		t.Fatal(err)
	}
	h := Handler(f.svc)
	scrape := func(remote, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/metrics/prometheus", nil).WithContext(testCtx)
		r.RemoteAddr = remote
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	if rec := scrape("127.0.0.1:5000", ""); rec.Code != http.StatusNotFound {
		t.Fatal("off but served", rec.Code)
	}
	f.svc.Prometheus = PrometheusConfig{Enabled: true}
	if rec := scrape("203.0.113.9:5000", ""); rec.Code != http.StatusForbidden {
		t.Fatal("remote peer without token", rec.Code)
	}
	rec := scrape("127.0.0.1:5000", "")
	body := rec.Body.String()
	for _, want := range []string{"# TYPE arkism_http_requests_total counter", `arkism_llm_calls_total{operation="wiki.page",provider="` + wikiProvider + `",model="trace-model",status="ok"} `,
		`arkism_llm_input_tokens_bucket{operation="wiki.page",provider="` + wikiProvider + `",model="trace-model",le="16"} `, "arkism_llm_output_tokens_count", "go_goroutines", "process_start_time_seconds"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in\n%s", want, body)
		}
	}
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get(headerContentType), "text/plain; version=0.0.4") {
		t.Fatal("scrape", rec.Code, rec.Header())
	}
	f.svc.Prometheus.Token = "scrape-token-123"
	if rec := scrape("127.0.0.1:5000", "wrong"); rec.Code != http.StatusForbidden {
		t.Fatal("wrong token", rec.Code)
	}
	if rec := scrape("203.0.113.9:5000", "scrape-token-123"); rec.Code != 200 {
		t.Fatal("token scrape", rec.Code)
	}
}

func TestPrometheusLabelEscapingAndPool(t *testing.T) {
	var b bytes.Buffer
	writePrometheus(&b, []metricFamily{gauge("x", "h", 1), {name: "y", help: "h", kind: "gauge", points: []metricPoint{{labels: [][2]string{{"route", "a\"b\\c\nd"}}, value: 2}}}})
	if !strings.Contains(b.String(), `y{route="a\"b\\c\nd"} 2`) || !strings.Contains(b.String(), "x 1\n") {
		t.Fatal(b.String())
	}
	fams := poolFamilies(PoolStats{Acquired: 2, Max: 10})
	writePrometheus(&b, fams)
	if !strings.Contains(b.String(), "arkism_db_pool_max_connections 10") {
		t.Fatal(b.String())
	}
}

type metricsCollector struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (c *metricsCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.bodies = append(c.bodies, b)
	c.mu.Unlock()
}

func TestOTLPMetricsExport(t *testing.T) {
	col := &metricsCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()
	f := newFixture(t)
	f.svc.Wiki = traceLLM{}
	if _, _, err := f.svc.modelCall(testCtx, scopeA, "wiki.page", "sys", "u", 10); err != nil {
		t.Fatal(err)
	}
	stop, err := StartMetricsExport(f.svc, srv.URL+"/v1/metrics", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	stop() // the final export
	if len(col.bodies) != 1 {
		t.Fatal("exports", len(col.bodies))
	}
	var payload struct {
		ResourceMetrics []struct {
			ScopeMetrics []struct {
				Metrics []struct {
					Name      string `json:"name"`
					Histogram *struct {
						DataPoints []otlpHistogramPoint `json:"dataPoints"`
					} `json:"histogram"`
					Sum *struct {
						IsMonotonic bool `json:"isMonotonic"`
					} `json:"sum"`
				} `json:"metrics"`
			} `json:"scopeMetrics"`
		} `json:"resourceMetrics"`
	}
	if err := json.Unmarshal(col.bodies[0], &payload); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, m := range payload.ResourceMetrics[0].ScopeMetrics[0].Metrics {
		seen[m.Name] = true
		if m.Name == "arkism_llm_input_tokens" {
			p := m.Histogram.DataPoints[0]
			if len(p.BucketCounts) != len(p.ExplicitBounds)+1 || p.Count == "0" {
				t.Fatalf("histogram point %+v", p)
			}
		}
		if m.Name == "arkism_llm_calls_total" && (m.Sum == nil || !m.Sum.IsMonotonic) {
			t.Fatal("counter not a monotonic sum")
		}
	}
	for _, want := range []string{"arkism_llm_input_tokens", "arkism_llm_calls_total", "go_goroutines"} {
		if !seen[want] {
			t.Fatal("missing", want)
		}
	}
	if _, err := StartMetricsExport(f.svc, "http://collector.example/v1/metrics", "", 0); err == nil {
		t.Fatal("plain http to a remote collector accepted")
	}
	if got := otlpBuckets([]uint64{1, 3, 3}, 3, 5); strings.Join(got, ",") != "1,2,0,2" {
		t.Fatal("bucket conversion", got)
	}
}

// Metric labels never carry odd or secret-looking names, and the metrics push
// only goes to https or a loopback collector.
func TestMetricLabelsAndExportEndpointGuarded(t *testing.T) {
	for v, want := range map[string]string{"wiki.page": "wiki.page", "gpt-x": "gpt-x", "": "other", "a b\n": "other",
		"sk-ant-api03-" + strings.Repeat("A", 40): "other", strings.Repeat("m", 200): "other"} {
		if got := metricLabel(v); got != want {
			t.Errorf("%q: %q", v, got)
		}
	}
	for _, endpoint := range []string{"http://169.254.169.254/v1/metrics", "http://10.0.0.5:4318/v1/metrics", "file:///etc/passwd", ""} {
		if stop, err := StartMetricsExport(&Service{}, endpoint, "", time.Hour); err == nil {
			stop()
			t.Error("accepted", endpoint)
		}
	}
}

// A ledger that refuses the reservation still lets the picker choose, as before
// the shared ledger call; the pick is then unrecorded.
func TestModelPickerWorksWithoutLedger(t *testing.T) {
	s := &Service{}
	choice, ok := s.pickModel(testCtx, tracePicker{}, scopeA, "task", []ModelOption{{ID: "fast", Name: "Fast"}})
	if !ok || choice != "fast" {
		t.Fatal("pick without ledger", choice, ok)
	}
}
