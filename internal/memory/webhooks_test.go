package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ListScopedRows makes the fake repository and its transactions scopedRowStores.
func (f *fakeRepo) ListScopedRows(ctx context.Context, q scopedRowQuery) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().ListScopedRows(ctx, q)
}

func (x fakeTx) ListScopedRows(_ context.Context, q scopedRowQuery) ([]Record, error) {
	if q.Limit < 1 || q.Limit > 1000 || q.Kind == "" {
		return nil, ErrInvalid
	}
	out := []Record{}
	for _, r := range x.store[q.Collection] {
		if r.Scope == q.Scope && r.Kind == q.Kind && (q.Status == "" || r.Status == q.Status) &&
			(q.Before == "" || r.ID < q.Before) && (q.Value == "" || docPath(r, q.Path) == q.Value) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out[:min(len(out), q.Limit)], nil
}

func docPath(r Record, path []string) string {
	raw, err := storeMarshal(r)
	if err != nil {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	for _, p := range path {
		m, _ := v.(map[string]any)
		v = m[p]
	}
	s, _ := v.(string)
	return s
}

const testWebhookSecret = "0123456789abcdef-secret"

type hookServer struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	bodies [][]byte
	sigs   []string
}

func newHookServer(t *testing.T, status int) *hookServer {
	h := &hookServer{status: status}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.bodies, h.sigs = append(h.bodies, b), append(h.sigs, r.Header.Get(WebhookSignatureHeader))
		code := h.status
		h.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *hookServer) hits() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.bodies)
}

func webhookFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.svc.WebhookKey = []byte(strings.Repeat("k", 32))
	f.svc.WebhookAllowPrivate = true // httptest listens on loopback
	return f
}

func (f *fixture) hook(url string, includeContent bool, events ...string) WebhookView {
	f.t.Helper()
	v, err := f.svc.CreateWebhook(testCtx, f.human, WebhookInput{Scope: scopeA, URL: url, Events: events, Secret: testWebhookSecret, IncludeContent: includeContent})
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

// workUntilTried runs the worker until the hook's first delivery was attempted:
// the approval's embedding job has a random id and may be claimed first.
func (f *fixture) workUntilTried(id string) WebhookDeliveryView {
	f.t.Helper()
	for range 4 {
		if d := f.deliveries(id)[0]; d.Attempts > 0 {
			return d
		}
		_ = f.svc.WorkOnce(testCtx, f.work)
	}
	return f.deliveries(id)[0]
}

func (f *fixture) deliveries(id string) []WebhookDeliveryView {
	f.t.Helper()
	page, err := f.svc.ListWebhookDeliveries(testCtx, f.human, scopeA, id, "", 0)
	if err != nil {
		f.t.Fatal(err)
	}
	return page.Items
}

func TestWebhookURLGuard(t *testing.T) {
	for _, c := range []struct {
		url          string
		allowPrivate bool
		ok           bool
	}{
		{"https://hooks.example.com/x", false, true},
		{"http://hooks.example.com/x", false, false},
		{"http://127.0.0.1:9/x", false, false},
		{"http://127.0.0.1:9/x", true, true},
		{"http://localhost:9/x", false, false},
		{"http://localhost:9/x", true, true},
		{"https://10.0.0.1/x", false, false},
		{"https://192.168.1.1/x", false, false},
		{"https://100.64.0.1/x", false, false},
		{"https://169.254.169.254/latest", false, false},
		{"https://169.254.169.254/latest", true, false},
		{"https://100.100.100.200/x", true, false},
		{"https://[fd00:ec2::254]/x", true, false},
		{"https://[::1]/x", false, false},
		{"https://user:pw@hooks.example.com/x", false, false},
		{"https://hooks.example.com/x#frag", false, false},
		{"ftp://hooks.example.com/x", false, false},
	} {
		if err := checkWebhookURL(c.url, c.allowPrivate); (err == nil) != c.ok {
			t.Errorf("%s allowPrivate=%v: err=%v want ok=%v", c.url, c.allowPrivate, err, c.ok)
		}
	}
}

func TestWebhookSignatureVerification(t *testing.T) {
	secret, body, now := []byte("s3cret-s3cret-16"), []byte(`{"id":"1"}`), time.Now()
	sig := SignWebhook(secret, now, body)
	if !VerifyWebhook(secret, sig, body, now, 5*time.Minute) {
		t.Fatal("valid signature refused")
	}
	for name, ok := range map[string]bool{
		"tampered body": VerifyWebhook(secret, sig, []byte(`{"id":"2"}`), now, 5*time.Minute),
		"wrong secret":  VerifyWebhook([]byte("other-secret-123"), sig, body, now, 5*time.Minute),
		"expired":       VerifyWebhook(secret, sig, body, now.Add(10*time.Minute), 5*time.Minute),
		"garbage":       VerifyWebhook(secret, "v1=abc", body, now, 5*time.Minute),
	} {
		if ok {
			t.Error(name, "accepted")
		}
	}
}

func TestWebhookCRUDIsHumanOnlyAndHidesSecret(t *testing.T) {
	f := webhookFixture(t)
	in := WebhookInput{Scope: scopeA, URL: "https://hooks.example.com/x", Events: []string{EventMemoryApproved}, Secret: testWebhookSecret}
	if _, err := f.svc.CreateWebhook(testCtx, f.agent, in); !errors.Is(err, ErrDenied) {
		t.Fatal("agent created webhook", err)
	}
	bad := in
	bad.Events = []string{"memory.unknown"}
	if _, err := f.svc.CreateWebhook(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown event accepted", err)
	}
	v := f.hook(in.URL, false, EventMemoryApproved)
	raw, _ := json.Marshal(v)
	if !v.SecretSet || strings.Contains(string(raw), testWebhookSecret) {
		t.Fatal("secret exposed or missing", string(raw))
	}
	stored := f.repo.rows(colWebhooks)[0]
	if strings.Contains(stored.Webhook.SealedKey, testWebhookSecret) {
		t.Fatal("secret stored in clear")
	}
	off := false
	u, err := f.svc.UpdateWebhook(testCtx, f.human, v.ID, WebhookUpdate{Scope: scopeA, ExpectedRevision: v.Revision, Active: &off})
	if err != nil || u.Active {
		t.Fatal("update", err, u)
	}
	if _, err := f.svc.ListWebhooks(testCtx, f.agent, scopeA); !errors.Is(err, ErrDenied) {
		t.Fatal("agent listed webhooks", err)
	}
	if err := f.svc.DeleteWebhook(testCtx, f.human, scopeA, v.ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := f.svc.ListWebhooks(testCtx, f.human, scopeA); len(l.Items) != 0 {
		t.Fatal("webhook survived delete", l)
	}
}

func approveCandidate(t *testing.T, f *fixture, text string) Record {
	t.Helper()
	c := f.create(f.agent, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: text, Generator: &Generator{Model: "test-model", PromptVersion: "p1", PipelineVersion: "v1"}, Sources: refs(f.source())}))
	m, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWebhookApprovalDeliversSignedPayloadWithoutContent(t *testing.T) {
	f := webhookFixture(t)
	srv := newHookServer(t, http.StatusOK)
	v := f.hook(srv.URL, false, EventMemoryApproved)
	m := approveCandidate(t, f, "the deploy key lives in the vault")
	for range 5 {
		_ = f.svc.WorkOnce(testCtx, f.work)
	}
	if srv.hits() != 1 {
		t.Fatal("deliveries", srv.hits())
	}
	body := srv.bodies[0]
	if !VerifyWebhook([]byte(testWebhookSecret), srv.sigs[0], body, time.Now(), time.Minute) {
		t.Fatal("signature does not verify")
	}
	var p webhookPayload
	if err := json.Unmarshal(body, &p); err != nil || p.Event != EventMemoryApproved || p.Data == nil || p.Data.ID != m.ID || p.Data.Collection != colMemories {
		t.Fatal("payload", err, string(body))
	}
	if p.Content != nil || strings.Contains(string(body), "vault") {
		t.Fatal("content left without include_content", string(body))
	}
	d := f.deliveries(v.ID)
	if len(d) != 1 || d[0].Status != JobStatusDone || d[0].Delivery.HTTPStatus != 200 || d[0].Attempts != 1 || d[0].Delivery.AttemptAt == nil {
		t.Fatalf("delivery log %+v", d)
	}
}

func TestWebhookIncludeContentKeepsPrivateText(t *testing.T) {
	f := webhookFixture(t)
	srv := newHookServer(t, http.StatusOK)
	f.hook(srv.URL, true, EventMemoryApproved)
	plain := approveCandidate(t, f, "use pnpm for installs")
	hidden := approveCandidate(t, f, "notes <private>card 4242</private> end")
	for range 6 {
		_ = f.svc.WorkOnce(testCtx, f.work)
	}
	got := map[string]*string{}
	for _, b := range srv.bodies {
		var p webhookPayload
		_ = json.Unmarshal(b, &p)
		got[p.Data.ID] = p.Content
		if strings.Contains(string(b), "4242") {
			t.Fatal("private span left the server", string(b))
		}
	}
	if c := got[plain.ID]; c == nil || *c != "use pnpm for installs" {
		t.Fatal("include_content dropped plain text", got)
	}
	if _, sent := got[hidden.ID]; !sent || got[hidden.ID] != nil {
		t.Fatal("private-marked record", got)
	}
}

func TestWebhookRetryStateAndGiveUp(t *testing.T) {
	f := webhookFixture(t)
	srv := newHookServer(t, http.StatusInternalServerError)
	v := f.hook(srv.URL, false, EventMemoryApproved)
	approveCandidate(t, f, "retry me")
	d := f.workUntilTried(v.ID)
	if d.Status != JobStatusRetry || d.Attempts != 1 || d.Delivery.HTTPStatus != 500 || d.Delivery.Error != "http_500" {
		t.Fatalf("after first failure %+v", d)
	}
	for range maxJobAttempts + 2 {
		_ = f.svc.WorkOnce(testCtx, f.work)
	}
	d = f.deliveries(v.ID)[0]
	if d.Status != JobStatusFailed || d.Attempts != maxJobAttempts || srv.hits() != maxJobAttempts {
		t.Fatalf("after give-up %+v hits=%d", d, srv.hits())
	}
}

func TestWebhookBackoffGrowsPerAttempt(t *testing.T) {
	if !(retryDelay(1) < retryDelay(3) && retryDelay(3) < retryDelay(5)) {
		t.Fatal("backoff is not increasing", retryDelay(1), retryDelay(3), retryDelay(5))
	}
}

// A name allowed at registration that resolves to a private address at
// delivery time (DNS rebinding) is refused by the dialer, not sent.
func TestWebhookDeliveryRefusesRebindingToPrivateAddress(t *testing.T) {
	f := webhookFixture(t)
	srv := newHookServer(t, http.StatusOK)
	v := f.hook(strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), false, EventMemoryApproved)
	f.svc.WebhookAllowPrivate = false
	approveCandidate(t, f, "rebinding")
	d := f.workUntilTried(v.ID)
	if srv.hits() != 0 {
		t.Fatal("request reached a private address")
	}
	if d.Delivery.Error != "blocked_address" || d.Status != JobStatusRetry {
		t.Fatalf("delivery %+v", d)
	}
}

// Deliveries are queued in the approval's own transaction: a rolled-back
// approval leaves none, a committed one always has its delivery.
func TestWebhookDeliveryIsQueuedInTheChangeTransaction(t *testing.T) {
	f := webhookFixture(t)
	v := f.hook("https://hooks.example.com/x", false, EventMemoryApproved)
	c := f.create(f.agent, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "outbox", Generator: &Generator{Model: "test-model", PromptVersion: "p1", PipelineVersion: "v1"}, Sources: refs(f.source())}))
	f.repo.failAudit = true
	if _, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"}); err == nil {
		t.Fatal("approval committed despite failed audit")
	}
	if d := f.deliveries(v.ID); len(d) != 0 {
		t.Fatalf("rolled-back approval queued deliveries %+v", d)
	}
	f.repo.failAudit = false
	if _, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"}); err != nil {
		t.Fatal(err)
	}
	if d := f.deliveries(v.ID); len(d) != 1 || d[0].Status != JobStatusPending {
		t.Fatalf("committed approval deliveries %+v", d)
	}
}

func TestWebhookEventsForCandidateApprovalAndDelete(t *testing.T) {
	f := webhookFixture(t)
	v := f.hook("https://hooks.example.com/x", false, EventCandidateCreated, EventMemoryApproved, EventMemoryUpdated, EventMemoryDeleted)
	m := approveCandidate(t, f, "event chain")
	// Metadata edits of a live memory (tags, reach) are updates too.
	m, err := f.svc.SetMemoryTags(testCtx, f.human, m.ID, MemoryTagsInput{Scope: scopeA, Add: []string{"chain"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(testCtx, f.human, colMemories, m.ID, ChangeInput{Scope: scopeA, ExpectedRevision: m.Revision}); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, d := range f.deliveries(v.ID) {
		if d.Delivery.Subject == nil || d.Delivery.Subject.ID != m.ID {
			t.Fatalf("subject %+v", d.Delivery.Subject)
		}
		got = append(got, d.Delivery.Event)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "candidate.created,memory.approved,memory.deleted,memory.updated" {
		t.Fatal("events", got)
	}
}

func TestWebhookClientErrorIsTerminalExceptTimeoutAndRateLimit(t *testing.T) {
	for status, terminal := range map[int]bool{400: true, 404: true, 410: true, 408: false, 429: false, 500: false, 503: false} {
		f := webhookFixture(t)
		srv := newHookServer(t, status)
		v := f.hook(srv.URL, false, EventMemoryApproved)
		approveCandidate(t, f, "status")
		d := f.workUntilTried(v.ID)
		want := JobStatusRetry
		if terminal {
			want = JobStatusFailed
		}
		if d.Status != want || srv.hits() != 1 {
			t.Errorf("status %d: delivery %s after %d hits, want %s", status, d.Status, srv.hits(), want)
		}
	}
}

// ListScopedRows compares doc #>> path as text; the delivery and trace filters
// must name plain string fields of the stored document (not {"$date"} objects).
func TestScopedRowFilterPathsAreStoredStrings(t *testing.T) {
	job := webhookDeliveryJob(scopeA, "hook-1", EventMemoryApproved, nil, time.Now())
	trace := Record{ID: "t", Scope: scopeA, Kind: llmRequestKind, LLMRequest: &LLMRequest{Operation: "wiki.page"}, CreatedAt: time.Now()}
	for _, c := range []struct {
		r    Record
		path []string
		want string
	}{{job, []string{"webhook_delivery", "webhook_id"}, "hook-1"}, {trace, []string{"llm_request", "operation"}, "wiki.page"}} {
		_, doc, err := encodeRecord(c.r)
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err = json.Unmarshal(doc, &v); err != nil {
			t.Fatal(err)
		}
		for _, p := range c.path {
			v = v.(map[string]any)[p]
		}
		if got, ok := v.(string); !ok || got != c.want {
			t.Fatalf("doc %v = %#v, want string %q", c.path, v, c.want)
		}
	}
	if !strings.Contains(scopedRowsWhere, "doc #>> $10::text[] = $9") || strings.Contains(scopedRowsWhere, "timestamptz") {
		t.Fatal("ListScopedRows filter changed; re-check it against the stored document form", scopedRowsWhere)
	}
}

// Session archive chunks sealed before sealAEAD/openAEAD were extracted (fixed
// nonce, same AES-256-GCM layout) still open.
func TestSessionArchiveOpensChunkSealedByEarlierCode(t *testing.T) {
	s := &Service{SessionArchiveKey: []byte(strings.Repeat("a", 32))}
	const sealed = "MDEyMzQ1Njc4OWFi71CwJj6xIkGwiVvM/dvpUNk3gVhbHOirAQ+CHm+j"
	plain, err := s.openSessionArchiveChunk(scopeA, "parent-1", "sha-1", sealed)
	if err != nil || string(plain) != "archived chunk" {
		t.Fatal("old chunk unreadable", err, string(plain))
	}
	if _, err = s.openSessionArchiveChunk(scopeA, "parent-2", "sha-1", sealed); !errors.Is(err, errSessionArchiveCiphertext) {
		t.Fatal("chunk opened under another header", err)
	}
}

func TestWebhookTestDeliveryAndPaging(t *testing.T) {
	f := webhookFixture(t)
	srv := newHookServer(t, http.StatusOK)
	v := f.hook(srv.URL, false, EventMemoryDeleted)
	for range 3 {
		if _, err := f.svc.TestWebhook(testCtx, f.human, scopeA, v.ID); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if _, err := f.svc.TestWebhook(testCtx, f.agent, scopeA, v.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("agent sent test", err)
	}
	page, err := f.svc.ListWebhookDeliveries(testCtx, f.human, scopeA, v.ID, "", 2)
	if err != nil || len(page.Items) != 2 || page.NextBefore == "" {
		t.Fatal("first page", err, page)
	}
	rest, _ := f.svc.ListWebhookDeliveries(testCtx, f.human, scopeA, v.ID, page.NextBefore, 2)
	if len(rest.Items) != 1 || rest.NextBefore != "" {
		t.Fatal("second page", rest)
	}
	if _, err := f.svc.ListWebhookDeliveries(testCtx, f.human, scopeA, v.ID, "", 101); !errors.Is(err, ErrInvalid) {
		t.Fatal("unbounded page accepted", err)
	}
}

type traceLLM struct{ err error }

func (traceLLM) Generator() Generator { return Generator{Model: "trace-model"} }
func (l traceLLM) Complete(context.Context, string, string, int) (string, ProviderUsage, error) {
	if l.err != nil {
		return "", ProviderUsage{}, l.err
	}
	return "answer password=synthetic-test-secret", ProviderUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}, nil
}

func TestLLMTraceRecordsSuccessAndError(t *testing.T) {
	f := newFixture(t)
	f.svc.Wiki = traceLLM{}
	f.svc.LLMTraceExclude = []string{"untraced*"}
	if _, _, err := f.svc.modelCall(testCtx, scopeA, "wiki.page", "sys", "user <private>pin 1234</private>", 100); err != nil {
		t.Fatal(err)
	}
	f.svc.Wiki = traceLLM{err: ErrUnavailable}
	if _, _, err := f.svc.modelCall(testCtx, scopeA, "wiki.page", "sys", "u", 100); !errors.Is(err, ErrUnavailable) {
		t.Fatal("ledger call changed the error", err)
	}
	f.svc.Wiki = traceLLM{}
	if _, _, err := f.svc.modelCall(testCtx, scopeA, "untraced.op", "untraced", "u", 1); err != nil {
		t.Fatal(err)
	}
	page, err := f.svc.ListLLMRequests(testCtx, f.human, scopeA, LLMRequestFilter{})
	if err != nil || len(page.Items) != 2 {
		t.Fatal("traces", err, page)
	}
	byStatus := map[string]LLMRequest{}
	for _, it := range page.Items {
		byStatus[it.Request.Status] = it.Request
	}
	s, e := byStatus["ok"], byStatus["error"]
	if s.Operation != "wiki.page" || s.TotalTokens != 15 || s.PromptSHA256 == "" || s.ResponseChars == 0 || s.Prompt != "" || s.Response != "" {
		t.Fatalf("success trace %+v", s)
	}
	if e.Error == "" || e.ResponseSHA256 != "" {
		t.Fatalf("error trace %+v", e)
	}
	if _, err := f.svc.ListLLMRequests(testCtx, f.agent, scopeA, LLMRequestFilter{}); !errors.Is(err, ErrDenied) {
		t.Fatal("agent read traces", err)
	}
	only, _ := f.svc.ListLLMRequests(testCtx, f.human, scopeA, LLMRequestFilter{Status: "error"})
	if len(only.Items) != 1 {
		t.Fatal("status filter", only)
	}
	stats, err := f.svc.LLMRequestStatsFor(testCtx, f.human, scopeA, 1)
	if err != nil || len(stats.Items) != 1 || stats.Items[0].Calls != 2 || stats.Items[0].Errors != 1 || stats.Items[0].TotalTokens != 15 {
		t.Fatal("stats", err, stats)
	}
}

func TestLLMTraceStoresRedactedContentOnlyWithFlag(t *testing.T) {
	f := newFixture(t)
	f.svc.Wiki, f.svc.LLMTraceContent = traceLLM{}, true
	if _, _, err := f.svc.modelCall(testCtx, scopeA, "wiki.page", "sys", "user <private>pin 1234</private>", 100); err != nil {
		t.Fatal(err)
	}
	page, _ := f.svc.ListLLMRequests(testCtx, f.human, scopeA, LLMRequestFilter{})
	r := page.Items[0].Request
	if !strings.Contains(r.Prompt, "user") || strings.Contains(r.Prompt, "1234") || r.Response == "" || strings.Contains(r.Response, "synthetic-test-secret") {
		t.Fatalf("stored content not redacted %+v", r)
	}
}

func TestWebhookRequestShapeIsSealedAndSent(t *testing.T) {
	f := webhookFixture(t)
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Clone(context.Background()) }))
	t.Cleanup(srv.Close)
	in := WebhookInput{Scope: scopeA, URL: srv.URL + "/in?keep=1", Events: []string{EventMemoryDeleted}, Secret: testWebhookSecret,
		Method: http.MethodPut, TimeoutSeconds: 5, Headers: map[string]string{"Authorization": "Bearer hook-token-value"}, QueryParams: map[string]string{"api_key": "q-token-value"}}
	for _, bad := range []func(*WebhookInput){
		func(i *WebhookInput) { i.Method = http.MethodGet },
		func(i *WebhookInput) { i.TimeoutSeconds = 31 },
		func(i *WebhookInput) { i.Headers = map[string]string{"Host": "evil"} },
		func(i *WebhookInput) { i.Headers = map[string]string{"X-Arkism-Signature": "forged"} },
		func(i *WebhookInput) { i.Headers = map[string]string{"X-Ok": "a\r\nInjected: 1"} },
	} {
		b := in
		bad(&b)
		if _, err := f.svc.CreateWebhook(testCtx, f.human, b); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %+v: %v", b, err)
		}
	}
	v, err := f.svc.CreateWebhook(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	stored, _ := json.Marshal(f.repo.rows(colWebhooks)[0])
	for _, secret := range []string{"hook-token-value", "q-token-value"} {
		if strings.Contains(string(raw), secret) || strings.Contains(string(stored), secret) {
			t.Fatal("header or query value readable", string(raw))
		}
	}
	if v.Method != http.MethodPut || v.TimeoutSeconds != 5 || len(v.HeaderNames) != 1 || v.QueryParamNames[0] != "api_key" {
		t.Fatalf("view %+v", v)
	}
	if _, err = f.svc.TestWebhook(testCtx, f.human, scopeA, v.ID); err != nil {
		t.Fatal(err)
	}
	_ = f.svc.WorkOnce(testCtx, f.work)
	if got == nil || got.Method != http.MethodPut || got.Header.Get("Authorization") != "Bearer hook-token-value" ||
		got.URL.Query().Get("api_key") != "q-token-value" || got.URL.Query().Get("keep") != "1" || got.Header.Get(WebhookSignatureHeader) == "" {
		t.Fatalf("request %+v", got)
	}
	// Changing only the query keeps the headers; an empty map clears.
	u, err := f.svc.UpdateWebhook(testCtx, f.human, v.ID, WebhookUpdate{Scope: scopeA, ExpectedRevision: v.Revision, QueryParams: map[string]string{}})
	if err != nil || len(u.QueryParamNames) != 0 || len(u.HeaderNames) != 1 {
		t.Fatal("update", err, u)
	}
	h, err := f.svc.openWebhookHTTP(f.repo.rows(colWebhooks)[0].Webhook, scopeA, v.ID)
	if err != nil || h.Headers["Authorization"] != "Bearer hook-token-value" || len(h.Query) != 0 {
		t.Fatal("sealed shape after update", err, h)
	}
	// A new path on the same origin keeps the sealed headers; a new origin drops them.
	same := srv.URL + "/other"
	if u, err = f.svc.UpdateWebhook(testCtx, f.human, v.ID, WebhookUpdate{Scope: scopeA, ExpectedRevision: u.Revision, URL: &same}); err != nil || len(u.HeaderNames) != 1 {
		t.Fatal("same origin", err, u)
	}
	moved := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/in"
	if u, err = f.svc.UpdateWebhook(testCtx, f.human, v.ID, WebhookUpdate{Scope: scopeA, ExpectedRevision: u.Revision, URL: &moved}); err != nil || len(u.HeaderNames) != 0 || len(u.QueryParamNames) != 0 {
		t.Fatal("new origin kept sealed headers", err, u)
	}
}

func queuedEvents(f *fixture) []string {
	out := []string{}
	for _, j := range f.repo.rows(colJobs) {
		if j.WebhookDelivery != nil {
			out = append(out, j.WebhookDelivery.Event)
		}
	}
	return out
}

func TestWebhookDefenseAndConsolidationEvents(t *testing.T) {
	f, _ := wikiFixture(t)
	f.svc.WebhookKey = []byte(strings.Repeat("k", 32))
	f.hook("https://hooks.example.com/x", false, EventDefenseTriggered, EventConsolidationCompleted)
	putSettings(t, f, scopeA, SpaceConfigInput{DefenseRules: &[]SpaceDefenseRule{{Pattern: `falcon`, Action: "redact"}, {Pattern: `\bINC-\d+\b`, Action: "block"}}})
	text := "project falcon ships"
	if err := f.svc.defendSpace(testCtx, "", defenseGuard{}, scopeA, &text); err != nil || text == "project falcon ships" {
		t.Fatal("redact", err, text)
	}
	plain := "nothing to see"
	_ = f.svc.defendSpace(testCtx, "", defenseGuard{}, scopeA, &plain)
	blocked := "see INC-42"
	if err := f.svc.defendSpace(testCtx, "", defenseGuard{}, scopeA, &blocked); !errors.Is(err, ErrDefenseBlocked) {
		t.Fatal("block", err)
	}
	if ev := queuedEvents(f); len(ev) != 2 || ev[0] != EventDefenseTriggered || ev[1] != EventDefenseTriggered {
		t.Fatal("defense events", ev)
	}
	if _, err := f.consolidate(f.human, true); err != nil {
		t.Fatal(err)
	}
	if ev := queuedEvents(f); len(ev) != 2 {
		t.Fatal("dry run queued an event", ev)
	}
	if _, err := f.consolidate(f.human, false); err != nil {
		t.Fatal(err)
	}
	if ev := queuedEvents(f); len(ev) != 3 || !slices.Contains(ev, EventConsolidationCompleted) { // queued rows have no order
		t.Fatal("consolidation event", ev)
	}
}

// A chat app's webhook address carries its key in the path: the hook is accepted,
// reads and the stored row show the address masked, and delivery uses the real one.
func TestWebhookURLWithKeyIsSealedAndSent(t *testing.T) {
	f := webhookFixture(t)
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.URL.RequestURI() }))
	t.Cleanup(srv.Close)
	key := "Zq8XvN3kLw7RtY2mPc5HbJ9dGf4SaE6uQ1oIy0TnB7xC3vM9pL2kJ5hG8fD4sA6w"
	path := "/api/webhooks/123456789012345678/" + key
	v, err := f.svc.CreateWebhook(testCtx, f.human, WebhookInput{Scope: scopeA, URL: srv.URL + path, Events: []string{EventMemoryDeleted}, Secret: testWebhookSecret})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	stored, _ := json.Marshal(f.repo.rows(colWebhooks)[0])
	if strings.Contains(string(raw), key) || strings.Contains(string(stored), key) || v.URL != srv.URL+"/api/webhooks/123456789012345678/[REDACTED]" {
		t.Fatal("key readable", v.URL, string(stored))
	}
	if _, err = f.svc.TestWebhook(testCtx, f.human, scopeA, v.ID); err != nil {
		t.Fatal(err)
	}
	_ = f.svc.WorkOnce(testCtx, f.work)
	if got != path {
		t.Fatalf("delivered to %q", got)
	}
	// A plain address replaces the sealed one; delivery follows it.
	plain := srv.URL + "/plain"
	if v, err = f.svc.UpdateWebhook(testCtx, f.human, v.ID, WebhookUpdate{Scope: scopeA, ExpectedRevision: v.Revision, URL: &plain}); err != nil || v.URL != plain {
		t.Fatal("update", err, v.URL)
	}
	if h, err := f.svc.openWebhookHTTP(f.repo.rows(colWebhooks)[0].Webhook, scopeA, v.ID); err != nil || h.URL != "" {
		t.Fatal("sealed url kept", err, h.URL)
	}
}
