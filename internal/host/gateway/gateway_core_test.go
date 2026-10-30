package gateway

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/subscription"
)

// The core handler through the real resolve. Custom keys come from a
// keys file; the Anthropic-compatible provider and Anthropic itself are one recording upstream
// (Python's Upstream), and a Chat provider is the fixture's own upstream.

const coreDeepKey = "sk-deep-secret"

type coreReply struct {
	status int
	data   []byte
	extra  int // declared length beyond the body: a truncated answer
}

type core struct {
	*fixture
	cmu      sync.Mutex
	cseen    []seenRequest
	creplies map[string]coreReply
	decided  [][2]string
	limits   []string
}

func corePick(mid string, fallbacks ...string) map[string]any {
	list := []any{}
	for _, f := range fallbacks {
		list = append(list, f)
	}
	return map[string]any{"group": "group/test", "id": mid, "decision": "fixed", "classifier_ms": int64(7), "fallbacks": list}
}

func newCore(t *testing.T) *core {
	c := &core{fixture: newFixture(t), creplies: map[string]coreReply{}}
	up := httptest.NewServer(http.HandlerFunc(c.upstream))
	t.Cleanup(up.Close)
	swap(t, &Anthropic, up.URL)
	swap(t, &target, resolve)
	// The Anthropic-compatible key points at the recording upstream (Python patched anthropic_url);
	// the Chat key at the fixture's upstream (Python patched _chat_stream).
	writeFile(t, host.KeysFile, `{"keys":[{"id":"key_aaaaaaaaaaaa","provider":"custom","key":"`+coreDeepKey+`","base_url":"`+up.URL+`"},`+
		`{"id":"key_bbbbbbbbbbbb","provider":"custom","key":"sk-qwen","base_url":"`+c.base+`/v1"}]}`)
	info := map[string]string{"key_aaaaaaaaaaaa": "anthropic", "key_bbbbbbbbbbbb": "openai"}
	swap(t, &host.CustomInfo, func(key map[string]any, refresh bool) (map[string]any, error) {
		return map[string]any{"models": []any{}, "api": info[key["id"].(string)]}, nil
	})
	swap(t, &codexProfile, func(string, []string) map[string]any { return nil })
	swap(t, &recordLimit, func(profile map[string]any, state, model string, retryAfter float64) error {
		c.cmu.Lock()
		defer c.cmu.Unlock()
		c.limits = append(c.limits, fmt.Sprint(profile["id"], " ", state == host.AgentsState, " ", model, " ", retryAfter))
		return nil
	})
	c.setDecision(corePick("custom-aaaaaaaaaaaa/deepseek-v4-pro"), nil)
	// The real claude_headers refuses without API mode; this one also counts calls.
	c.auth = func(string) (map[string]string, string, error) {
		return nil, "", errors.New("claude_subscription_requires_cli")
	}
	return c
}

func (c *core) setDecision(decision map[string]any, err error) {
	c.decide = func(task, group string) (map[string]any, error) {
		c.cmu.Lock()
		c.decided = append(c.decided, [2]string{task, group})
		c.cmu.Unlock()
		return decision, err
	}
}

func (c *core) upstream(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	body, _ := Parse(string(raw))
	model, _ := get(body, "model").(string)
	c.cmu.Lock()
	c.cseen = append(c.cseen, seenRequest{r.Method, r.URL.RequestURI(), r.Header.Clone(), raw, body})
	reply, ok := c.creplies[model]
	c.cmu.Unlock()
	if !ok {
		reply = coreReply{200, []byte(`{"ok":true,"echo":"` + coreDeepKey + `"}`), 0}
	}
	w.Header().Set("content-length", strconv.Itoa(len(reply.data)+reply.extra))
	w.Header().Set("x-echo", coreDeepKey)
	w.Header().Set("x-model", model)
	w.WriteHeader(reply.status)
	// Split the key across two reads: masking must not depend on read boundaries.
	cut := bytes.Index(reply.data, []byte("secret"))
	if cut < 0 {
		cut = len(reply.data) / 2
	}
	w.Write(reply.data[:cut])
	w.(http.Flusher).Flush()
	time.Sleep(20 * time.Millisecond)
	w.Write(reply.data[cut:])
}

func (c *core) seenList() []seenRequest {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	return slices.Clone(c.cseen)
}

func (c *core) reportList() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.reports)
}

func (c *core) reset() {
	c.cmu.Lock()
	c.cseen, c.decided, c.limits = nil, nil, nil
	c.cmu.Unlock()
	c.clear()
}

func (c *core) seenModels() []string {
	var out []string
	for _, s := range c.seenList() {
		out = append(out, fmt.Sprint(get(s.body, "model")))
	}
	return out
}

// send posts a Messages body like Python's post(): content-type plus the given headers only.
func (c *core) send(path, body string, headers ...string) response {
	return send(c.t, "POST", c.gw.URL+path, []byte(body), append([]string{"content-type", "application/json"}, headers...)...)
}

func (c *core) messages(body string, headers ...string) response {
	return c.send("/v1/messages?beta=true", body, headers...)
}

const coreTask = `"messages":[{"role":"user","content":"task"}]`

// coreRoundTrip lets a test replace the gateway's direct client transport (Python's _DIRECT.open).
type coreRoundTrip func(*http.Request) (*http.Response, error)

func (f coreRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func coreSwapTransport(t *testing.T, rt func(next http.RoundTripper, r *http.Request) (*http.Response, error)) {
	next := direct.Transport
	swap(t, &direct, &http.Client{Transport: coreRoundTrip(func(r *http.Request) (*http.Response, error) { return rt(next, r) }),
		CheckRedirect: direct.CheckRedirect})
}

func coreChatReply(t *testing.T, model string) upstreamReply {
	out, err := CodecResponse(parsed(t, []byte(message("안녕하세요"))), model)
	if err != nil {
		t.Fatal(err)
	}
	return upstreamReply{200, "application/json", mustPy(out)}
}

func coreNum(v any) string { return fmt.Sprint(v) }

func TestCoreClaudePassesThroughWithTheCallersLogin(t *testing.T) {
	c := newCore(t)
	reply := c.messages(`{"model":"claude-opus-5-5","messages":[],"context_management":{}}`,
		"authorization", "Bearer sk-ant-oat-me", "anthropic-beta", "oauth-2025-04-20")
	seen := c.seenList()
	if reply.status != 200 || seen[0].path != "/v1/messages?beta=true" {
		t.Fatalf("%d %v", reply.status, seen)
	}
	if seen[0].header.Get("authorization") != "Bearer sk-ant-oat-me" || seen[0].header.Get("anthropic-beta") != "oauth-2025-04-20" {
		t.Fatal(seen[0].header)
	}
	if !has(seen[0].body.(host.Obj), "context_management") {
		t.Fatal("body changed")
	}
}

func TestCoreCustomAnthropicProviderGetsItsKeyPortableBodyAndNoLeak(t *testing.T) {
	c := newCore(t)
	reply := c.messages(`{"model":"custom-aaaaaaaaaaaa/deepseek-v4-pro","messages":[],"tools":[`+
		`{"name":"Artifact","input_schema":{"properties":{"p":{"type":"string","pattern":"^[^\\0]*$"}}}},`+
		`{"type":"web_search_20250305","name":"web_search"}],"context_management":{},"max_tokens":5}`,
		"authorization", "Bearer sk-ant-oat-me")
	seen := c.seenList()[0]
	if reply.status != 200 || seen.header.Get("x-api-key") != coreDeepKey || seen.path != "/anthropic/v1/messages" {
		t.Fatalf("%d %s %v", reply.status, reply.body, seen)
	}
	if get(seen.body, "model") != "deepseek-v4-pro" || has(seen.body.(host.Obj), "context_management") {
		t.Fatalf("%s", seen.raw)
	}
	tools := get(seen.body, "tools").([]any)
	if len(tools) != 1 || get(tools[0], "name") != "Artifact" { // server tools exist only at Anthropic
		t.Fatalf("%s", seen.raw)
	}
	if has(at(tools[0], "input_schema", "properties", "p").(host.Obj), "pattern") {
		t.Fatalf("%s", seen.raw)
	}
	// An upstream that echoes the key never hands it back.
	if at(parsed(t, reply.body), "echo") != "***" || reply.header.Get("x-echo") != "***" {
		t.Fatalf("%s %v", reply.body, reply.header)
	}
}

func TestCoreUnknownOrSubscriptionModelsAreRefusedAndListedModelsAreServable(t *testing.T) {
	c := newCore(t)
	writeFile(t, host.KeysFile, `{"keys":[{"id":"key_aaaaaaaaaaaa","provider":"custom","key":"sk-deep-secret","base_url":"https://api.deepseek.com"},`+
		`{"id":"key_bbbbbbbbbbbb","provider":"custom","key":"sk-qwen","base_url":"https://qwen.example/v1"}]}`)
	reply := c.messages(`{"model":"openai/gpt-6.1-sol","messages":[]}`)
	if reply.status != 503 || at(parsed(t, reply.body), "type") != "error" || len(c.seenList()) != 0 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	w, err := resolve("custom-bbbbbbbbbbbb/qwen3.8-flash")
	if err != nil || *w != (where{kind: "chat", base: "https://qwen.example/v1", key: "sk-qwen", name: "qwen3.8-flash"}) {
		t.Fatal(w, err)
	}
	if w, err := resolve("anthropic/opus 5.5"); err != nil || w.kind != "anthropic" {
		t.Fatal(w, err)
	}
	if w, err := resolve("xai/grok-4.7-build-fast"); err != nil || w != nil {
		t.Fatal(w, err)
	}
}

// coreCodexRun fakes codex.iter_messages: run sees the request and folder and yields the result.
func coreCodexRun(t *testing.T, calls *atomic.Int32, run func(request map[string]any, folder string) byteSeq) {
	swap(t, &codexIterMessages, func(request map[string]any, folder, executable string, timeout float64) iter.Seq2[[]byte, error] {
		calls.Add(1)
		return run(request, folder)
	})
}

func coreCodexFail(code string, status int) *subscription.CodexError {
	return &subscription.CodexError{Code: code, Status: status, RetryAfter: -1}
}

func coreCodexChoose(ids ...string) func(string, []string) map[string]any {
	return func(model string, exclude []string) map[string]any {
		for _, id := range ids {
			if !slices.Contains(exclude, id) {
				return map[string]any{"id": id, "config_dir": "/synthetic/" + id}
			}
		}
		return nil
	}
}

// coreCodexPrimaryRefused refuses the primary account and answers on any other.
func coreCodexPrimaryRefused(request map[string]any, folder string) byteSeq {
	return func(yield func([]byte, error) bool) {
		if strings.HasSuffix(folder, "primary") {
			yield(nil, coreCodexFail("upstream_rate_limit", 429))
			return
		}
		yield([]byte(`{"type":"message","content":[]}`), nil)
	}
}

// coreCodexStartedThenRefused writes a stream start, then fails; closed records the generator ending.
func coreCodexStartedThenRefused(closed *atomic.Bool) func(map[string]any, string) byteSeq {
	return func(map[string]any, string) byteSeq {
		return func(yield func([]byte, error) bool) {
			defer closed.Store(true)
			if !yield([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"), nil) {
				return
			}
			yield(nil, coreCodexFail("upstream_rate_limit", 429))
		}
	}
}

func TestCoreCodexSubscriptionPreservesRequestAndUsesSelectedProfile(t *testing.T) {
	c := newCore(t)
	swap(t, &codexProfile, func(string, []string) map[string]any {
		return map[string]any{"id": "codex", "config_dir": "/synthetic/login"}
	})
	var calls atomic.Int32
	var request map[string]any
	var folder string
	coreCodexRun(t, &calls, func(r map[string]any, f string) byteSeq {
		request, folder = r, f
		return once([]byte(`{"type":"message","content":[]}`))
	})
	reply := c.messages(`{"model":"openai/gpt-test","messages":[{"role":"user","content":"synthetic"}],"tools":[{"type":"web_search","name":"search"}]}`)
	if reply.status != 200 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	sorted := host.DumpOpts{SortKeys: true}
	want, _ := Parse(`{"model":"gpt-test","messages":[{"role":"user","content":"synthetic"}],"tools":[{"type":"web_search","name":"search"}]}`)
	if host.MustDumps(request, sorted) != host.MustDumps(want, sorted) {
		t.Fatal(host.MustDumps(request, sorted))
	}
	if folder != "/synthetic/login" || len(c.seenList()) != 0 {
		t.Fatal(folder, c.seenList())
	}
}

func TestCoreCodexRefusedAccountHandsOffOnlyBeforeOutput(t *testing.T) {
	c := newCore(t)
	swap(t, &codexProfile, coreCodexChoose("primary", "backup"))
	var calls atomic.Int32
	coreCodexRun(t, &calls, coreCodexPrimaryRefused)
	reply := c.messages(`{"model":"openai/gpt-test","messages":[]}`)
	if reply.status != 200 || calls.Load() != 2 {
		t.Fatalf("%d %s %d", reply.status, reply.body, calls.Load())
	}
	if !slices.Equal(c.limits, []string{"primary true gpt-test -1"}) {
		t.Fatal(c.limits)
	}
}

func TestCoreCodexStreamErrorNeverReplaysAfterOutputAndCloses(t *testing.T) {
	c := newCore(t)
	swap(t, &codexProfile, func(string, []string) map[string]any {
		return map[string]any{"id": "codex", "config_dir": "/synthetic/login"}
	})
	var calls atomic.Int32
	var closed atomic.Bool
	coreCodexRun(t, &calls, coreCodexStartedThenRefused(&closed))
	reply := c.send("/v1/messages", `{"model":"openai/gpt-test","messages":[],"stream":true}`)
	output := string(reply.body)
	if !strings.Contains(output, "event: error") || !strings.Contains(output, "upstream_rate_limit") {
		t.Fatal(output)
	}
	if calls.Load() != 1 || !closed.Load() || len(c.limits) != 1 {
		t.Fatal(calls.Load(), closed.Load(), c.limits)
	}
}

// The gateway half of the engine contract. The /v1/routing/decide payload and runner token belong
// to decide_route, which this package reaches only through host.DecideRoute.
func TestCoreGroupUsesScopedEngineContractAndKeepsContext(t *testing.T) {
	c := newCore(t)
	request := `{"model":"group/test","system":"synthetic-system","max_tokens":10,"messages":[{"role":"user","content":` +
		`[{"type":"text","text":"synthetic-task"},{"type":"image","source":{"data":"omit-image"}}]}]}`
	reply := c.messages(request, "authorization", "Bearer synthetic-caller", "x-api-key", "caller-key")
	if reply.status != 200 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	if !slices.Equal(c.decided, [][2]string{{"synthetic-task", "group/test"}}) {
		t.Fatal(c.decided)
	}
	seen := c.seenList()[0]
	want := parsed(t, []byte(request))
	same(t, get(seen.body, "messages"), host.MustDumps(get(want, "messages"), host.DumpOpts{}))
	if get(seen.body, "system") != "synthetic-system" || seen.header.Get("authorization") != "Bearer "+coreDeepKey {
		t.Fatalf("%s %v", seen.raw, seen.header)
	}
	reports := c.reportList()
	call := reports[len(reports)-1]
	if coreNum(call["classifier_ms"]) != "7" || call["ok"] != true {
		t.Fatal(call)
	}
	var keys []string
	for k := range call {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	// origin carries the call kind (and, when Claude Code names its session, the turn trace): ids only.
	if !slices.Equal(keys, []string{"classifier_ms", "decision", "error", "latency_ms", "model", "ok", "origin", "replaced"}) ||
		host.MustDumps(call["origin"], host.DumpOpts{}) != `{"kind": "gateway"}` {
		t.Fatal(keys, call["origin"])
	}
	dumped := host.MustDumps(call, host.DumpOpts{})
	for _, secret := range []string{"synthetic-task", "synthetic-system", "synthetic-caller", coreDeepKey, "caller-key"} {
		if strings.Contains(dumped, secret) {
			t.Fatal(secret, dumped)
		}
	}
}

func TestCoreModelDiscoveryIncludesOnlyActiveSupportedGroups(t *testing.T) {
	c := newCore(t)
	var mu sync.Mutex
	var calls []string
	authority := "data_only_untrusted"
	listing := `[{"id":"anthropic/opus 5.5","enabled":true},{"id":"custom-aaaaaaaaaaaa/deepseek-v4-pro","enabled":true},` +
		`{"id":"xai/grok-test","enabled":true},{"id":"custom-bbbbbbbbbbbb/qwen3.8-flash","enabled":false},null,{"id":[],"enabled":true}]`
	groups := `[{"id":"group/claude","name":"Claude group","enabled":true,"members":["anthropic/opus 5.5"]},` +
		`{"id":"group/custom","enabled":true,"members":["xai/grok-test","custom-aaaaaaaaaaaa/deepseek-v4-pro"]},` +
		`{"id":"group/off","enabled":false,"members":["anthropic/opus 5.5"]},` +
		`{"id":"group/unsupported","enabled":true,"members":["xai/grok-test"]},` +
		`{"id":"group/disabled-member","enabled":true,"members":["custom-bbbbbbbbbbbb/qwen3.8-flash"]},` +
		`null,{"id":"group/bad","enabled":true,"members":{}},{"id":"group/bad-name","name":{},"enabled":true,"members":["anthropic/opus 5.5"]}]`
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.URL.RequestURI()+" "+r.Header.Get("authorization"))
		items := listing
		if strings.HasPrefix(r.URL.RequestURI(), "/v1/routing/groups?") {
			items = groups
		}
		a := authority
		mu.Unlock()
		w.Write([]byte(`{"authority":"` + a + `","items":` + items + `}`))
	}))
	t.Cleanup(engine.Close)
	swap(t, &host.BaseURL, engine.URL)
	writeFile(t, host.TokenFile, "synthetic-engine-token\n")
	t.Setenv("ARKISM_WORKSPACE", "org")
	t.Setenv("ARKISM_PROJECT", "project")
	reply := fetch(t, c.gw.URL+"/v1/models")
	data := at(parsed(t, reply.body), "data").([]any)
	var ids []string
	for _, m := range data {
		ids = append(ids, get(m, "id").(string))
	}
	if !slices.Equal(ids, []string{"anthropic/opus 5.5", "custom-aaaaaaaaaaaa/deepseek-v4-pro", "group/claude", "group/custom", "group/auto"}) {
		t.Fatal(ids)
	}
	if get(data[2], "display_name") != "Claude group" {
		t.Fatal(data[2])
	}
	listModels()
	if len(calls) != 2 { // same one-minute cache
		t.Fatal(calls)
	}
	mu.Lock()
	authority = "instructions"
	mu.Unlock()
	models.Lock()
	models.at, models.items = time.Time{}, nil
	models.Unlock()
	if got := listModels(); len(got) != 0 {
		t.Fatal(got)
	}
	for _, call := range calls {
		uri, token, _ := strings.Cut(call, " ")
		if _, query, _ := strings.Cut(uri, "?"); query != scopeQuery() || token != "Bearer synthetic-engine-token" {
			t.Fatal(call)
		}
	}
	if !strings.Contains(scopeQuery(), "workspace_id=org") || !strings.Contains(scopeQuery(), "project_id=project") {
		t.Fatal(scopeQuery())
	}
}

func TestCoreGroupDecisionFailureAndMismatchNeverEscapeToAnthropic(t *testing.T) {
	c := newCore(t)
	bad := func(key string, value any) map[string]any {
		d := corePick("custom-aaaaaaaaaaaa/deepseek-v4-pro")
		d[key] = value
		return d
	}
	cases := []struct {
		decision map[string]any
		err      error
	}{
		{nil, &host.CrewError{Msg: "synthetic-failure"}}, {map[string]any{}, nil}, {bad("group", "group/other"), nil},
		{bad("id", "group/nested"), nil}, {bad("fallbacks", map[string]any{"bad": "shape"}), nil}, {bad("decision", "unknown"), nil},
	}
	for i, tc := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			c.reset()
			c.setDecision(tc.decision, tc.err)
			reply := c.messages(`{"model":"group/test",` + coreTask + `}`)
			message, _ := errorMessage(t, reply.body).(string)
			if (reply.status != 502 && reply.status != 503) || !strings.HasPrefix(message, "group_decision_") {
				t.Fatalf("%d %s", reply.status, reply.body)
			}
			if len(c.seenList()) != 0 || c.reportCount() != 0 {
				t.Fatal("a provider was called or metered")
			}
		})
	}
}

func TestCoreUnsupportedGroupCandidateIsSkippedBeforeTransmission(t *testing.T) {
	c := newCore(t)
	c.setDecision(corePick("xai/grok-test", "custom-aaaaaaaaaaaa/deepseek-v4-pro"), nil)
	reply := c.messages(`{"model":"group/test",` + coreTask + `}`)
	if reply.status != 200 || len(c.seenList()) != 1 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	calls := c.reportList()
	if len(calls) != 2 || calls[0]["error"] != "unsupported_target" || calls[1]["error"] != "" {
		t.Fatal(calls)
	}
	if calls[1]["replaced"] != "xai/grok-test" || calls[1]["decision"] != "failover" {
		t.Fatal(calls[1])
	}
}

func TestCoreGroupSwitchesOnConfirmedRejectionsBeforeClientHeaders(t *testing.T) {
	c := newCore(t)
	c.setDecision(corePick("custom-aaaaaaaaaaaa/deepseek-v4-pro", "custom-aaaaaaaaaaaa/backup"), nil)
	for _, rejected := range []int{401, 403, 429, 503} {
		t.Run(strconv.Itoa(rejected), func(t *testing.T) {
			if !retryStatuses[rejected] {
				t.Fatal("not a retry status")
			}
			c.reset()
			c.creplies["deepseek-v4-pro"] = coreReply{rejected, []byte(`{"error":"synthetic-rejection"}`), 0}
			reply := c.messages(`{"model":"group/test",` + coreTask + `}`)
			if reply.status != 200 || at(parsed(t, reply.body), "echo") != "***" || reply.header.Get("x-model") != "backup" {
				t.Fatalf("%d %s %v", reply.status, reply.body, reply.header)
			}
			if !slices.Equal(c.seenModels(), []string{"deepseek-v4-pro", "backup"}) {
				t.Fatal(c.seenModels())
			}
			calls := c.reportList()
			if len(calls) != 2 || calls[0]["ok"] != false || calls[1]["ok"] != true {
				t.Fatal(calls)
			}
		})
	}
	if len(retryStatuses) != 5 || !retryStatuses[402] {
		t.Fatal(retryStatuses)
	}
}

func TestCoreGroupAttemptsAtMostThreeFallbacksAndReturnsExplicitError(t *testing.T) {
	c := newCore(t)
	var mids []string
	for _, name := range []string{"one", "two", "three", "four", "five"} {
		mids = append(mids, "custom-aaaaaaaaaaaa/"+name)
		c.creplies[name] = coreReply{429, []byte(`{"error":"refused"}`), 0}
	}
	c.setDecision(corePick(mids[0], mids[1:]...), nil)
	reply := c.messages(`{"model":"group/test",` + coreTask + `}`)
	if reply.status != 429 || errorMessage(t, reply.body) != "upstream_http_429" {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	if !slices.Equal(c.seenModels(), []string{"one", "two", "three", "four"}) || c.reportCount() != 4 {
		t.Fatal(c.seenModels(), c.reportCount())
	}
}

// A 500 or a transport failure before any response moves on to the next member; when every
// member fails the same way the caller gets the pick's own error.
func TestCoreGroup500AndTransportErrorsMoveToNextMember(t *testing.T) {
	for _, failure := range []error{nil, errors.New("synthetic-timeout"), errors.New("synthetic-network")} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			c := newCore(t)
			c.setDecision(corePick("custom-aaaaaaaaaaaa/deepseek-v4-pro", "custom-aaaaaaaaaaaa/backup"), nil)
			c.creplies["deepseek-v4-pro"] = coreReply{500, []byte(`{"error":"synthetic-500"}`), 0}
			var opened atomic.Int32
			coreSwapTransport(t, func(next http.RoundTripper, r *http.Request) (*http.Response, error) {
				opened.Add(1)
				if failure != nil {
					return nil, failure
				}
				return next.RoundTrip(r)
			})
			reply := c.messages(`{"model":"group/test",` + coreTask + `}`)
			want, backupOK := 200, true
			if failure != nil {
				want, backupOK = 502, false
			}
			reports := c.reportList()
			if reply.status != want || opened.Load() != 2 || len(reports) != 2 || reports[0]["ok"] != false ||
				reports[1]["ok"] != backupOK || reports[1]["decision"] != "failover" {
				t.Fatalf("%d %s %d %v", reply.status, reply.body, opened.Load(), reports)
			}
		})
	}
}

// coreBrokenBody hands out first, then fails like a read timeout.
type coreBrokenBody struct{ first []byte }

func (b *coreBrokenBody) Read(p []byte) (int, error) {
	if len(b.first) > 0 {
		n := copy(p, b.first)
		b.first = b.first[n:]
		return n, nil
	}
	return 0, errors.New("synthetic mid-response failure")
}

func (b *coreBrokenBody) Close() error { return nil }

func TestCoreGroupDirectStreamOrSuccessHeadersBlockReplayOnReadFailure(t *testing.T) {
	for _, first := range []string{"", "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"} {
		t.Run(strconv.Itoa(len(first)), func(t *testing.T) {
			c := newCore(t)
			c.setDecision(corePick("custom-aaaaaaaaaaaa/deepseek-v4-pro", "custom-aaaaaaaaaaaa/backup"), nil)
			var opened atomic.Int32
			coreSwapTransport(t, func(_ http.RoundTripper, r *http.Request) (*http.Response, error) {
				opened.Add(1)
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
					Body: &coreBrokenBody{[]byte(first)}, Request: r}, nil
			})
			reply := c.send("/v1/messages", `{"model":"group/test",`+coreTask+`,"stream":true}`)
			// Accepted wire difference: the started response is dropped, never ended cleanly.
			if reply.err == nil || (reply.status != 0 && reply.status != 200) {
				t.Fatalf("%d %v %s", reply.status, reply.err, reply.body)
			}
			reports := c.reportList()
			if opened.Load() != 1 || len(reports) == 0 || reports[len(reports)-1]["error"] != "upstream_interrupted" {
				t.Fatal(opened.Load(), reports)
			}
		})
	}
}

func TestCoreGroupDetectsTruncatedHTTPBodyWithoutReplay(t *testing.T) {
	c := newCore(t)
	c.setDecision(corePick("custom-aaaaaaaaaaaa/deepseek-v4-pro", "custom-aaaaaaaaaaaa/backup"), nil)
	c.creplies["deepseek-v4-pro"] = coreReply{200, []byte(`{"partial":`), 1000 - len(`{"partial":`)}
	reply := c.send("/v1/messages", `{"model":"group/test",`+coreTask+`}`)
	reports := c.reportList()
	if reply.status != 200 || len(c.seenList()) != 1 || reports[len(reports)-1]["error"] != "upstream_truncated" {
		t.Fatalf("%d %s %v", reply.status, reply.body, reports)
	}
}

// Python also covered an exception wrapping an HTTPError (__cause__) and one carrying a
// status_code attribute; Go's chat stream reports a rejection as its own *statusError and a
// transport failure as a *url.Error, and a group moves on from either.
func TestCoreGroupChatRetriesOnRejectionAndTransportFailure(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(strconv.FormatBool(confirmed), func(t *testing.T) {
			c := newCore(t)
			c.setDecision(corePick("custom-bbbbbbbbbbbb/qwen3.8-flash", "custom-aaaaaaaaaaaa/backup"), nil)
			c.replies["qwen3.8-flash"] = upstreamReply{429, "application/json", []byte(`{"error":"refused"}`)}
			if !confirmed {
				coreSwapTransport(t, func(next http.RoundTripper, r *http.Request) (*http.Response, error) {
					if strings.HasSuffix(r.URL.Path, "/chat/completions") {
						return nil, errors.New("synthetic timeout")
					}
					return next.RoundTrip(r)
				})
			}
			reply := c.messages(`{"model":"group/test","max_tokens":5,` + coreTask + `}`)
			if reply.status != 200 || len(c.seenList()) != 1 || c.reportCount() != 2 {
				t.Fatalf("%d %s seen %d reports %d", reply.status, reply.body, len(c.seenList()), c.reportCount())
			}
			if confirmed && c.seenCount() != 1 {
				t.Fatal("chat target called", c.seenCount())
			}
		})
	}
}

func coreChunk(delta string, finish any) string {
	return `{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"upstream","choices":[{"index":0,"delta":` +
		delta + `,"finish_reason":` + string(mustPy(finish)) + `}]}`
}

func TestCoreGroupChatStreamFailureIsReportedAndNeverReplayed(t *testing.T) {
	c := newCore(t)
	c.setDecision(corePick("custom-bbbbbbbbbbbb/qwen3.8-flash", "custom-aaaaaaaaaaaa/backup"), nil)
	// The stream starts, then the connection ends short of its declared length. (The second frame
	// lets the first one past the key-masking tail.)
	c.replies["qwen3.8-flash"] = upstreamReply{200, "text/event-stream", []byte("data: " + coreChunk(`{"role":"assistant","content":"안녕"}`, nil) +
		"\n\ndata: " + coreChunk(`{"content":"하세요"}`, nil) + "\n\n")}
	c.lengthExtra = 1000
	reply := c.send("/v1/messages", `{"model":"group/test","max_tokens":5,`+coreTask+`,"stream":true}`)
	if reply.status != 200 || !bytes.Contains(reply.body, []byte("event: error")) {
		t.Fatalf("%d %v %s", reply.status, reply.err, reply.body)
	}
	reports := c.reportList()
	if c.seenCount() != 1 || len(c.seenList()) != 0 || reports[len(reports)-1]["ok"] != false {
		t.Fatal(c.seenCount(), c.seenList(), reports)
	}
}

func TestCoreHostClaudeAPIPreservesExtensionsAndIsolatesCredentials(t *testing.T) {
	for _, model := range []string{"anthropic/opus 5.5", "group/test"} {
		t.Run(model, func(t *testing.T) {
			c := newCore(t)
			c.setDecision(corePick("anthropic/opus 5.5"), nil)
			c.auth = func(string) (map[string]string, string, error) {
				return map[string]string{"x-api-key": "host-key", "anthropic-version": "2023-06-01"}, "host-key", nil
			}
			reply := c.messages(`{"model":"`+model+`",`+coreTask+`,"context_management":{},"thinking":{"type":"adaptive"}}`,
				"authorization", "Bearer synthetic-caller", "x-api-key", "caller-key", "x-private-header", "private",
				"anthropic-beta", "synthetic-beta,other-2026-01-01")
			seen := c.seenList()
			last := seen[len(seen)-1]
			if reply.status != 200 || last.path != "/v1/messages" || get(last.body, "model") != "claude-opus-5-5" {
				t.Fatalf("%d %s %v", reply.status, reply.body, last)
			}
			same(t, get(last.body, "thinking"), `{"type":"adaptive"}`)
			if !has(last.body.(host.Obj), "context_management") || last.header.Get("x-api-key") != "host-key" {
				t.Fatalf("%s %v", last.raw, last.header)
			}
			if last.header.Get("authorization") != "" || last.header.Get("x-private-header") != "" ||
				last.header.Get("anthropic-beta") != "synthetic-beta,other-2026-01-01" {
				t.Fatal(last.header)
			}
		})
	}
}

func TestCoreHostClaudeInvalidStructureOrBetaNeverExecutes(t *testing.T) {
	c := newCore(t)
	for _, model := range []string{"anthropic/opus 5.5", "group/test"} {
		for _, messages := range []string{"null", "[]", "[null]", "[{}]", `[{"role":"system","content":"x"}]`, `[{"role":"user","content":42}]`} {
			if reply := c.messages(`{"model":"` + model + `","messages":` + messages + `}`); reply.status != 400 {
				t.Fatalf("%s %s %d", model, messages, reply.status)
			}
		}
		for _, beta := range []string{"bad;value", "a,,b", strings.Repeat("x", 101), strings.Repeat(strings.Repeat("x", 99)+",", 10) + strings.Repeat("x", 99)} {
			if reply := c.messages(`{"model":"`+model+`",`+coreTask+`}`, "anthropic-beta", beta); reply.status != 400 {
				t.Fatalf("%s %q %d", model, beta, reply.status)
			}
		}
	}
	for _, model := range []string{"anthropic/", "anthropic/" + strings.Repeat("x", 81), "anthropic/model\n"} {
		if reply := c.messages(`{"model":` + string(mustPy(model)) + `,` + coreTask + `}`); reply.status != 400 {
			t.Fatalf("%q %d", model, reply.status)
		}
	}
	if len(c.authCalls) != 0 || len(c.decided) != 0 || len(c.seenList()) != 0 {
		t.Fatal(c.authCalls, c.decided, c.seenList())
	}
}

// coreClaudeLaunches counts host Claude CLI selections (a launch always selects a login first).
func coreClaudeLaunches(t *testing.T) *atomic.Int32 {
	var n atomic.Int32
	swap(t, &claudeProfile, func(string) (map[string]any, string, error) {
		n.Add(1)
		return nil, "", claudeErr("login_unavailable", 503)
	})
	swap(t, &claudeRuns, &claudeBridge{})
	return &n
}

func TestCoreHostClaudeCountTokensUsesAPIAndNeverLaunchesCLI(t *testing.T) {
	c := newCore(t)
	launches := coreClaudeLaunches(t)
	c.creplies["claude-opus-5-5"] = coreReply{200, []byte(`{"input_tokens":137}`), 0}
	c.auth = func(string) (map[string]string, string, error) {
		return map[string]string{"x-api-key": "host-key"}, "host-key", nil
	}
	body := `{"model":"anthropic/opus 5.5",` + coreTask + `}`
	reply := c.send("/v1/messages/count_tokens", body, "authorization", "Bearer caller")
	if reply.status != 200 || string(reply.body) != `{"input_tokens": 137}` && string(reply.body) != `{"input_tokens":137}` {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	last := c.seenList()[0]
	if last.path != "/v1/messages/count_tokens" || get(last.body, "model") != "claude-opus-5-5" ||
		last.header.Get("x-api-key") != "host-key" || last.header.Get("authorization") != "" {
		t.Fatal(last)
	}
	c.reset()
	c.auth = func(string) (map[string]string, string, error) {
		return nil, "", errors.New("claude_subscription_requires_cli")
	}
	reply = c.send("/v1/messages/count_tokens", body)
	if reply.status != 501 || bytes.Contains(reply.body, []byte("input_tokens")) || launches.Load() != 0 || len(c.seenList()) != 0 {
		t.Fatalf("%d %s %d", reply.status, reply.body, launches.Load())
	}
}

func TestCoreNativeCLIBetaFailsBeforeLaunch(t *testing.T) {
	c := newCore(t)
	launches := coreClaudeLaunches(t)
	reply := c.messages(`{"model":"anthropic/opus 5.5",`+coreTask+`}`, "anthropic-beta", "synthetic-beta")
	if reply.status != 400 || launches.Load() != 0 || len(c.seenList()) != 0 {
		t.Fatalf("%d %s %d", reply.status, reply.body, launches.Load())
	}
}

// Python captured the owner bridge.continuation_model received; Go has no seam there, so the
// owner is computed for the same requests and the requests are checked to reach nothing.
func TestCoreNativeCLIOwnerIncludesPublicModelAndCaller(t *testing.T) {
	c := newCore(t)
	launches := coreClaudeLaunches(t)
	c.auth = func(string) (map[string]string, string, error) { return nil, "", errors.New("unavailable") }
	c.decide = func(task, group string) (map[string]any, error) {
		d := corePick("anthropic/opus 5.5")
		d["group"] = group
		return d, nil
	}
	cases := [][2]string{{"anthropic/opus 5.5", "a"}, {"anthropic/opus 5.5", "a"}, {"anthropic/opus 5.5", "b"}, {"group/a", "a"}, {"group/b", "a"}}
	var owners []string
	for _, tc := range cases {
		reply := c.messages(`{"model":"`+tc[0]+`",`+coreTask+`}`, "x-api-key", tc[1])
		if reply.status != 503 {
			t.Fatalf("%v %d %s", tc, reply.status, reply.body)
		}
		r := httptest.NewRequest("POST", "/v1/messages?beta=true", nil)
		r.Header.Set("x-api-key", tc[1])
		owners = append(owners, (&exchange{r: r, target: r.RequestURI, requested: tc[0]}).owner())
	}
	unique := slices.Compact(slices.Sorted(slices.Values(owners)))
	if owners[0] != owners[1] || len(unique) != 4 {
		t.Fatal(owners)
	}
	if len(c.seenList()) != 0 || launches.Load() != 0 {
		t.Fatal(c.seenList(), launches.Load())
	}
}

func TestCoreNativeGroupContinuationAcceptsAliasAndNeverChangesProvider(t *testing.T) {
	c := newCore(t)
	h := claudeNewHarness(t)
	swap(t, &claudeRuns, h.bridge)
	body := claudeWith(claudeRequest(t), "model", "group/test")
	// A first turn through the group starts a native run that waits for tool results.
	c.setDecision(corePick("anthropic/opus 5.5"), nil)
	reply := c.messages(string(pyDumpsUnicode(body)))
	if reply.status != 200 || len(h.runs()) != 1 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	c.reset()
	c.setDecision(corePick("custom-aaaaaaaaaaaa/other", "anthropic/opus 5.5"), nil)
	reply = c.messages(string(pyDumpsUnicode(claudeFollow(body, parsed(t, reply.body).(host.Obj)))))
	if reply.status != 200 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	if runs := h.runs(); len(runs) != 1 || runs[0].model != "claude-opus-5-5" || h.profileCalls() != 1 {
		t.Fatal("the continuation did not reuse the run", h.profileCalls())
	}
	reports := c.reportList()
	if len(c.authCalls) != 0 || len(c.seenList()) != 0 || len(reports) != 1 || reports[0]["model"] != "anthropic/opus 5.5" {
		t.Fatal(c.authCalls, c.seenList(), reports)
	}
}

func TestCoreGroupCountTokensDoesNotDecideOrExecute(t *testing.T) {
	c := newCore(t)
	var targets, runs atomic.Int32
	swap(t, &target, func(string) (*where, error) { targets.Add(1); return nil, nil })
	coreCodexRun(t, &runs, func(map[string]any, string) byteSeq { return once(nil) })
	reply := c.send("/v1/messages/count_tokens", `{"model":"group/test","messages":[]}`)
	tokens, _ := host.Int(at(parsed(t, reply.body), "input_tokens"))
	if reply.status != 200 || tokens <= 0 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	if len(c.decided) != 0 || targets.Load() != 0 || runs.Load() != 0 || c.reportCount() != 0 || len(c.seenList()) != 0 {
		t.Fatal("count_tokens decided or executed")
	}
}

func TestCoreGroupCodexWithNoEligibleAccountUsesNextModel(t *testing.T) {
	c := newCore(t)
	c.setDecision(corePick("openai/gpt-test", "custom-aaaaaaaaaaaa/backup"), nil)
	var calls atomic.Int32
	coreCodexRun(t, &calls, func(map[string]any, string) byteSeq { return once(nil) })
	reply := c.messages(`{"model":"group/test",` + coreTask + `}`)
	if reply.status != 200 || calls.Load() != 0 || len(c.seenList()) != 1 {
		t.Fatalf("%d %s %d", reply.status, reply.body, calls.Load())
	}
}

func TestCoreGroupCodexAccountRejectionsAreEachReported(t *testing.T) {
	c := newCore(t)
	c.setDecision(corePick("openai/gpt-test", "custom-aaaaaaaaaaaa/backup"), nil)
	swap(t, &codexProfile, coreCodexChoose("primary", "backup"))
	var calls atomic.Int32
	coreCodexRun(t, &calls, coreCodexPrimaryRefused)
	reply := c.messages(`{"model":"group/test",` + coreTask + `}`)
	if reply.status != 200 || calls.Load() != 2 || len(c.seenList()) != 0 {
		t.Fatalf("%d %s %d", reply.status, reply.body, calls.Load())
	}
	reports := c.reportList()
	if len(reports) != 2 {
		t.Fatal(reports)
	}
	for i, want := range []struct {
		ok bool
		ms string
	}{{false, "7"}, {true, "0"}} {
		if reports[i]["model"] != "openai/gpt-test" || reports[i]["ok"] != want.ok || coreNum(reports[i]["classifier_ms"]) != want.ms {
			t.Fatal(reports)
		}
	}
	if reports[1]["decision"] != "failover" {
		t.Fatal(reports[1])
	}
}

func TestCoreGroupCodexLastRefusalSwitchesModelWithoutDuplicateReport(t *testing.T) {
	c := newCore(t)
	c.setDecision(corePick("openai/gpt-test", "custom-aaaaaaaaaaaa/backup"), nil)
	swap(t, &codexProfile, coreCodexChoose("primary"))
	var calls atomic.Int32
	coreCodexRun(t, &calls, func(map[string]any, string) byteSeq {
		return func(yield func([]byte, error) bool) { yield(nil, coreCodexFail("upstream_forbidden", 403)) }
	})
	reply := c.messages(`{"model":"group/test",` + coreTask + `}`)
	reports := c.reportList()
	if reply.status != 200 || calls.Load() != 1 || len(reports) != 2 ||
		reports[0]["model"] != "openai/gpt-test" || reports[1]["model"] != "custom-aaaaaaaaaaaa/backup" {
		t.Fatalf("%d %s %d %v", reply.status, reply.body, calls.Load(), reports)
	}
}

func TestCoreGroupCodexLocalOrAmbiguousFailuresNeverRetry(t *testing.T) {
	for _, code := range []string{"auth_missing", "auth_expired", "cli_unavailable", "deadline_exceeded", "upstream_error"} {
		t.Run(code, func(t *testing.T) {
			c := newCore(t)
			c.setDecision(corePick("openai/gpt-test", "custom-aaaaaaaaaaaa/backup"), nil)
			swap(t, &codexProfile, coreCodexChoose("profile"))
			var calls atomic.Int32
			coreCodexRun(t, &calls, func(map[string]any, string) byteSeq {
				return func(yield func([]byte, error) bool) { yield(nil, coreCodexFail(code, 502)) }
			})
			reply := c.messages(`{"model":"group/test",` + coreTask + `}`)
			reports := c.reportList()
			if reply.status < 400 || calls.Load() != 1 || len(c.seenList()) != 0 || len(reports) != 1 || reports[0]["ok"] != false {
				t.Fatalf("%d %s %d %v", reply.status, reply.body, calls.Load(), reports)
			}
		})
	}
}

func TestCoreGroupCodexMidStreamRefusalDoesNotSwitchAccountsOrModels(t *testing.T) {
	c := newCore(t)
	c.setDecision(corePick("openai/gpt-test", "custom-aaaaaaaaaaaa/backup"), nil)
	swap(t, &codexProfile, coreCodexChoose("profile", "other"))
	var calls atomic.Int32
	var closed atomic.Bool
	coreCodexRun(t, &calls, coreCodexStartedThenRefused(&closed))
	reply := c.send("/v1/messages", `{"model":"group/test",`+coreTask+`,"stream":true}`)
	reports := c.reportList()
	if reply.status != 200 || !bytes.Contains(reply.body, []byte("event: error")) {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	if calls.Load() != 1 || len(c.seenList()) != 0 || !closed.Load() || reports[len(reports)-1]["error"] != "upstream_rate_limit" {
		t.Fatal(calls.Load(), closed.Load(), reports)
	}
}

func TestCoreSingleChatReusesProviderKeyAndBodyTranslation(t *testing.T) {
	c := newCore(t)
	c.replies["qwen3.8-flash"] = coreChatReply(t, "qwen3.8-flash")
	reply := c.messages(`{"model":"custom-bbbbbbbbbbbb/qwen3.8-flash","max_tokens":5,`+coreTask+`,"context_management":{}}`,
		"authorization", "Bearer synthetic-caller", "x-api-key", "synthetic-caller-key")
	if reply.status != 200 || c.seenCount() != 1 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	sent := c.seen[0]
	if sent.path != "/v1/chat/completions" || sent.header.Get("authorization") != "Bearer sk-qwen" {
		t.Fatal(sent.path, sent.header)
	}
	if get(sent.body, "model") != "qwen3.8-flash" || coreNum(get(sent.body, "max_tokens")) != "5" ||
		at(sent.body, "messages", 0, "role") != "user" || has(sent.body.(host.Obj), "context_management") {
		t.Fatalf("%s", sent.raw)
	}
	if len(c.decided) != 0 || c.reportCount() != 0 {
		t.Fatal("a single model was routed or metered")
	}
}

func TestCoreGroupTaskUsesUserTextOnlyAndIsBounded(t *testing.T) {
	c := newCore(t)
	request := `{"model":"group/test","system":"omit-system","messages":[{"role":"user","content":"` + strings.Repeat("x", 20001) + `"},` +
		`{"role":"assistant","content":"omit-assistant"},{"role":"user","content":[{"type":"tool_result","content":"omit-tool-output"}]}]}`
	if reply := c.messages(request); reply.status != 200 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	if !slices.Equal(c.decided, [][2]string{{strings.Repeat("x", 20000), "group/test"}}) {
		t.Fatal(len(c.decided))
	}
	c.reset()
	reply := c.messages(`{"model":"group/test","messages":[{"role":"user","content":[]}]}`)
	if reply.status != 400 || errorMessage(t, reply.body) != "group_task_missing" || len(c.decided) != 0 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
}

func TestCoreGroupTaskLimitCountsUTF8BytesWithoutTruncatingExecution(t *testing.T) {
	c := newCore(t)
	task := strings.Repeat("한😀", 5000)
	request := `{"model":"group/test","messages":[{"role":"user","content":"` + task + `"}]}`
	if reply := c.messages(request); reply.status != 200 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	routed := c.decided[0][0]
	if len(routed) > 20000 || len(routed) <= 19996 || !strings.HasPrefix(task, routed) {
		t.Fatal(len(routed))
	}
	same(t, get(c.seenList()[0].body, "messages"), host.MustDumps(get(parsed(t, []byte(request)), "messages"), host.DumpOpts{}))
}

func TestCoreGroupBodyBoundariesAreRejectedBeforeDeciding(t *testing.T) {
	c := newCore(t)
	cases := []struct {
		data    string
		headers []string
		status  int
	}{
		{`{"model":"group/test"`, nil, 400}, {`[]`, nil, 400}, {`{"model":1}`, nil, 400}, {`{"model":"group/INVALID"}`, nil, 400},
		{`{}`, []string{"content-length", "-1"}, 400}, {`{}`, []string{"content-length", "invalid"}, 400},
		{`{}`, []string{"content-length", strconv.Itoa(subscription.MaxInput + 1)}, 413},
		{`{}`, []string{"transfer-encoding", "chunked"}, 400},
	}
	for _, tc := range cases {
		t.Run(tc.data+strings.Join(tc.headers, "="), func(t *testing.T) {
			reply := send(t, "POST", c.gw.URL+"/v1/messages", []byte(tc.data), tc.headers...)
			if reply.status != tc.status {
				t.Fatalf("%d %s", reply.status, reply.body)
			}
			// net/http answers a malformed Content-Length itself with a plain 400, before the handler runs.
			if len(tc.headers) == 2 && tc.headers[0] == "content-length" && tc.status == 400 {
				return
			}
			if v, err := Parse(string(reply.body)); err != nil || get(v, "type") != "error" {
				t.Fatalf("not an error body: %q", reply.body)
			}
		})
	}
	if len(c.decided) != 0 || len(c.seenList()) != 0 {
		t.Fatal("decided or sent")
	}
}

// A custom endpoint saved without a key gets no Authorization or x-api-key, on both the Messages and the Chat
// path, and its stored extra headers ride along; the caller's own credentials never do.
func TestCoreKeylessCustomEndpointSendsOnlyItsHeaders(t *testing.T) {
	c := newCore(t)
	keys, err := host.LoadKeys()
	if err != nil || len(keys) != 2 {
		t.Fatal(keys, err)
	}
	rows := []string{}
	for _, k := range keys {
		rows = append(rows, `{"id":"`+k["id"].(string)+`","provider":"custom","key":"","base_url":"`+k["base_url"].(string)+`",`+
			`"header_names":["X-Gateway-Token"],"headers":"X-Gateway-Token: gw-secret-1234"}`)
	}
	writeFile(t, host.KeysFile, `{"keys":[`+strings.Join(rows, ",")+`]}`)
	c.messages(`{"model":"custom-aaaaaaaaaaaa/deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"max_tokens":5}`,
		"authorization", "Bearer sk-ant-oat-me")
	c.messages(`{"model":"custom-bbbbbbbbbbbb/qwen","messages":[{"role":"user","content":"hi"}],"max_tokens":5}`,
		"authorization", "Bearer sk-ant-oat-me")
	seen := append(c.seenList(), c.fixture.seenList()...)
	if len(seen) != 2 {
		t.Fatalf("%d requests", len(seen))
	}
	for _, s := range seen {
		if s.header.Get("Authorization") != "" || s.header.Get("X-Api-Key") != "" || s.header.Get("X-Gateway-Token") != "gw-secret-1234" {
			t.Fatalf("%s %v", s.path, s.header)
		}
	}
}
