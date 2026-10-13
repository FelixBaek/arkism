package gateway

import (
	"bytes"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/subscription"
)

// The Chat entrance, plus the Chat/Gemini half of the Codex reasoning-usage test.

// sortedJSON compares like Python's dict equality: key order does not matter.
func sortedJSON(v any) string { return host.MustDumps(v, host.DumpOpts{SortKeys: true}) }

func sameJSON(t *testing.T, got any, want string) {
	t.Helper()
	w, err := Parse(want)
	if err != nil {
		t.Fatalf("bad want %s", want)
	}
	if sortedJSON(got) != sortedJSON(w) {
		t.Fatalf("got %s\nwant %s", sortedJSON(got), sortedJSON(w))
	}
}

func objOf(t *testing.T, text string) host.Obj {
	t.Helper()
	return parsed(t, []byte(text)).(host.Obj)
}

// fixtureChunk, fixtureWire and fixtureTools are test_arkism_chat_native's chunk, wire and tools.
func fixtureChunk(delta string, finish any, extra ...host.KV) host.Obj {
	if delta == "" {
		delta = "{}"
	}
	d, _ := Parse(delta)
	out := host.Obj{{Key: "id", Value: "chatcmpl-test"}, {Key: "object", Value: "chat.completion.chunk"}, {Key: "created", Value: int64(1)},
		{Key: "model", Value: "upstream"}, {Key: "choices", Value: []any{host.Obj{{Key: "index", Value: int64(0)}, {Key: "delta", Value: d},
			{Key: "finish_reason", Value: finish}}}}}
	for _, item := range extra {
		out.Set(item.Key, item.Value)
	}
	return out
}

func fixtureUsageChunk() host.Obj {
	usage, _ := Parse(`{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7}`)
	return fixtureChunk("", nil, host.KV{Key: "choices", Value: []any{}}, host.KV{Key: "usage", Value: usage})
}

func fixtureWire(events []host.Obj, done bool) []byte {
	var b bytes.Buffer
	for _, e := range events {
		b.WriteString("data: " + string(pyDumpsUnicode(e)) + "\n\n")
	}
	if done {
		b.WriteString("data: [DONE]\n\n")
	}
	return b.Bytes()
}

func fixtureTools() []host.Obj {
	return []host.Obj{
		fixtureChunk(`{"role": "assistant", "content": "", "reasoning_content": "private"}`, nil),
		fixtureChunk(`{"tool_calls": [{"index": 0, "id": "call_0", "type": "function", "function": {"name": "first", "arguments": ""}},
			{"index": 1, "id": "call_1", "type": "function", "function": {"name": "second", "arguments": ""}}]}`, nil),
		fixtureChunk(`{"tool_calls": [{"index": 1, "function": {"arguments": "{\"y\":2}"}}, {"index": 0, "function": {"arguments": "{\"x\":1}"}}]}`, nil),
		fixtureChunk("", "tool_calls"),
	}
}

// chatReply is chat_codec.response(message(text), model) as an upstream Chat body.
func chatReply(t *testing.T, text, model string) []byte {
	out, err := CodecResponse(objOf(t, message(text)), model)
	if err != nil {
		t.Fatal(err)
	}
	return mustPy(out)
}

// routes records every target lookup, like the patched gw.target mock.
type routes struct {
	mu  sync.Mutex
	ids []string
}

func (r *routes) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

func (f *fixture) route(fn func(model string) *where) *routes {
	r := &routes{}
	swap(f.t, &target, func(model string) (*where, error) {
		r.mu.Lock()
		r.ids = append(r.ids, model)
		r.mu.Unlock()
		return fn(model), nil
	})
	return r
}

// defaultWhere is the fixture's own target: anthropic/ goes to Anthropic, the rest direct.
func (f *fixture) defaultWhere(model string) *where {
	if strings.HasPrefix(model, "anthropic/") {
		return &where{kind: "anthropic"}
	}
	_, name, found := strings.Cut(model, "/")
	if !found {
		name = model
	}
	return &where{kind: "direct", base: f.base, key: "host-secret", name: name}
}

func (f *fixture) chatWhere(name string) *where {
	return &where{kind: "chat", base: f.base + "/v1", key: "host-secret", name: name}
}

func (f *fixture) seenList() []seenRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seenRequest(nil), f.seen...)
}

func (f *fixture) reportList() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.reports...)
}

func (f *fixture) lastReport() map[string]any {
	reports := f.reportList()
	if len(reports) == 0 {
		f.t.Fatal("no report")
	}
	return reports[len(reports)-1]
}

func (f *fixture) authList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.authCalls...)
}

func (f *fixture) setDecision(id string, fallbacks ...any) {
	f.decide = func(task, group string) (map[string]any, error) {
		return map[string]any{"group": "group/test", "id": id, "fallbacks": append([]any{}, fallbacks...),
			"decision": "fixed", "classifier_ms": int64(2)}, nil
	}
}

func authError(code string) func(string) (map[string]string, string, error) {
	return func(string) (map[string]string, string, error) { return nil, "", errors.New(code) }
}

// noExecution makes Codex and Google execution fail the test and counts any call.
func noExecution(t *testing.T) *int {
	var mu sync.Mutex
	calls := 0
	hit := func() {
		mu.Lock()
		calls++
		mu.Unlock()
		t.Errorf("execution started")
	}
	swap(t, &codexProfile, func(string, []string) map[string]any { hit(); return nil })
	swap(t, &codexIterMessages, func(map[string]any, string, string, float64) iter.Seq2[[]byte, error] {
		hit()
		return func(func([]byte, error) bool) {}
	})
	swap(t, &googleIterContent, func(any, string, bool, bool) iter.Seq2[any, error] {
		hit()
		return func(func(any, error) bool) {}
	})
	return &calls
}

func chatData(raw []byte) []any {
	var events []any
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("data: ")) && string(line) != "data: [DONE]" {
			v, _ := Parse(string(line[6:]))
			events = append(events, v)
		}
	}
	return events
}

func mustStatus(t *testing.T, reply response, status int) {
	t.Helper()
	if reply.status != status {
		t.Fatalf("status %d, want %d: %s (%v)", reply.status, status, reply.body, reply.err)
	}
}

func TestChatTextUsesSameEngineAndDoesNotForwardCallerCredentials(t *testing.T) {
	f := newFixture(t)
	reply := f.post("")
	mustStatus(t, reply, 200)
	out := parsed(t, reply.body)
	sameJSON(t, at(out, "choices", 0, "message", "content"), `"안녕하세요"`)
	sameJSON(t, at(out, "model"), `"custom-test/first"`)
	seen := f.seenList()
	if seen[0].path != "/v1/messages" {
		t.Fatal(seen[0].path)
	}
	sameJSON(t, at(seen[0].body, "max_tokens"), `4096`)
	sameJSON(t, at(seen[0].body, "model"), `"first"`)
	if strings.Contains(sortedJSON(seen[0].header), "caller-") {
		t.Fatal("caller credentials forwarded")
	}
	if reply.header.Get("x-private") != "" || f.decisions != 0 {
		t.Fatal("provider header leaked or a route was decided")
	}
}

func TestChatModelListIsCompatibleWithBothClients(t *testing.T) {
	f := newFixture(t)
	swap(t, &routingModels, func() []host.Obj {
		return []host.Obj{{{Key: "id", Value: "group/test"}, {Key: "label", Value: "테스트"}}}
	})
	reply := fetch(t, f.gw.URL+"/v1/models")
	mustStatus(t, reply, 200)
	out := parsed(t, reply.body)
	sameJSON(t, at(out, "object"), `"list"`)
	sameJSON(t, at(out, "data", 0, "object"), `"model"`)
	sameJSON(t, at(out, "data", 0, "display_name"), `"테스트"`)
	if !isInt(at(out, "data", 0, "created")) {
		t.Fatalf("created is not an int: %v", at(out, "data", 0, "created"))
	}
}

func TestChatClaudeUsesHostLoginAndMissingLoginNeverSends(t *testing.T) {
	f := newFixture(t)
	request := `{"model": "anthropic/claude-test", "messages": [{"role": "user", "content": "hi"}]}`
	mustStatus(t, f.post(request), 200)
	if got := f.authList(); len(got) != 1 || got[0] != "claude-test" {
		t.Fatalf("auth calls %v", got)
	}
	for _, s := range f.seenList() {
		if strings.Contains(sortedJSON(s.header)+string(s.raw), "caller-") {
			t.Fatal("caller credentials forwarded")
		}
	}
	f.clear()
	f.auth = authError("auth_expired")
	reply := f.post(request)
	mustStatus(t, reply, 503)
	sameJSON(t, errorMessage(t, reply.body), `"login_unavailable"`)
	if f.seenCount() != 0 {
		t.Fatal("sent without a login")
	}
}

func TestChatGroupRejectionCanSwitchButServerErrorCannot(t *testing.T) {
	f := newFixture(t)
	request := `{"model": "group/test", "messages": [{"role": "user", "content": "hi"}]}`
	for _, c := range []struct{ failure, expected int }{{429, 200}, {500, 500}} {
		t.Run(strconv.Itoa(c.failure), func(t *testing.T) {
			f.clear()
			f.replies["first"] = upstreamReply{c.failure, "application/json", []byte(`{"error":"caller-secret host-secret"}`)}
			reply := f.post(request)
			mustStatus(t, reply, c.expected)
			if bytes.Contains(reply.body, []byte("secret")) {
				t.Fatalf("secret leaked: %s", reply.body)
			}
			want := 1
			if c.failure == 429 {
				want = 2
			}
			if f.seenCount() != want || f.reportCount() != want {
				t.Fatalf("seen %d reports %d, want %d", f.seenCount(), f.reportCount(), want)
			}
			if strings.Contains(sortedJSON(f.reportList()[0]), "hi") {
				t.Fatal("task text reported")
			}
		})
	}
}

func TestChatBadInputAndUnsupportedFieldsNeverExecute(t *testing.T) {
	f := newFixture(t)
	routed := f.route(f.defaultWhere)
	for _, raw := range []string{`[]`, `{`, `{"model":"group/test","messages":[],"n":2}`,
		`{"model":"group/test","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{},"strict":true}}}`} {
		reply := f.post(raw)
		mustStatus(t, reply, 400)
		if at(parsed(t, reply.body), "error") == nil {
			t.Fatalf("no error: %s", reply.body)
		}
	}
	if f.seenCount() != 0 || f.decisions != 0 || len(routed.list()) != 0 {
		t.Fatal("bad input executed")
	}
}

func TestChatBodyLimitAppliesBeforeReading(t *testing.T) {
	f := newFixture(t)
	reply := f.post("", "content-length", strconv.Itoa(subscription.MaxInput+1))
	mustStatus(t, reply, 413)
	if at(parsed(t, reply.body), "error") == nil || f.seenCount() != 0 {
		t.Fatalf("got %s", reply.body)
	}
}

func TestChatSchemaIsPreservedAndToolsRoundTrip(t *testing.T) {
	f := newFixture(t)
	reply := objOf(t, message("안녕하세요"))
	reply.Set("content", parsed(t, []byte(`[{"type": "tool_use", "id": "call_one", "name": "read", "input": {"path": "한글"}}]`)))
	reply.Set("stop_reason", "tool_use")
	f.replies["first"] = upstreamReply{200, "application/json", mustPy(reply)}
	schema := `{"type": "object", "properties": {"path": {"type": "string", "pattern": "^[a-z]+$"}}}`
	got := f.post(`{"model": "custom-test/first", "messages": [{"role": "user", "content": "read"}],
		"tools": [{"type": "function", "function": {"name": "read", "parameters": ` + schema + `}}]}`)
	mustStatus(t, got, 200)
	sameJSON(t, at(f.seenList()[0].body, "tools", 0, "input_schema"), schema)
	choice := at(parsed(t, got.body), "choices", 0)
	sameJSON(t, at(choice, "finish_reason"), `"tool_calls"`)
	sameJSON(t, parsed(t, []byte(at(choice, "message", "tool_calls", 0, "function", "arguments").(string))), `{"path": "한글"}`)
}

func TestChatClaudeSubscriptionIsNotReplayedAsAPIAuth(t *testing.T) {
	f := newFixture(t)
	f.auth = authError("claude_subscription_requires_cli")
	swap(t, &claudeProfile, func(string) (map[string]any, string, error) { return nil, "", claudeErr("login_unavailable", 503) })
	reply := f.post(`{"model": "anthropic/claude-test", "messages": [{"role": "user", "content": "hi"}]}`)
	mustStatus(t, reply, 503)
	sameJSON(t, errorMessage(t, reply.body), `"login_unavailable"`)
	if f.seenCount() != 0 {
		t.Fatal("replayed as API auth")
	}
}

func TestChatGroupSkipsUnavailableLocalCredentialsBeforeGeneration(t *testing.T) {
	f := newFixture(t)
	f.auth = authError("claude_subscription_requires_cli")
	f.setDecision("anthropic/claude-test", "custom-test/backup")
	swap(t, &claudeProfile, func(string) (map[string]any, string, error) { return nil, "", claudeErr("login_unavailable", 503) })
	reply := f.post(`{"model": "group/test", "messages": [{"role": "user", "content": "hi"}]}`)
	mustStatus(t, reply, 200)
	sameJSON(t, at(parsed(t, reply.body), "model"), `"group/test"`)
	var models []string
	for _, s := range f.seenList() {
		models = append(models, at(s.body, "model").(string))
	}
	var oks []any
	for _, r := range f.reportList() {
		oks = append(oks, r["ok"])
	}
	if fmt.Sprint(models) != "[backup]" || fmt.Sprint(oks) != "[false true]" {
		t.Fatalf("models %v reports %v", models, oks)
	}
}

func TestChatCodexAndChatProviderShareResponseConversion(t *testing.T) {
	f := newFixture(t)
	f.route(func(string) *where { return &where{kind: "codex", model: "gpt-test"} })
	swap(t, &codexProfile, func(string, []string) map[string]any {
		return map[string]any{"id": "synthetic", "config_dir": "/synthetic"}
	})
	var ran []map[string]any
	swap(t, &codexIterMessages, func(body map[string]any, _, _ string, _ float64) iter.Seq2[[]byte, error] {
		ran = append(ran, body)
		return func(yield func([]byte, error) bool) { yield([]byte(message("안녕하세요")), nil) }
	})
	reply := f.post(`{"model": "openai/gpt-test", "messages": [{"role": "user", "content": "hi"}]}`)
	mustStatus(t, reply, 200)
	sameJSON(t, at(parsed(t, reply.body), "choices", 0, "message", "content"), `"안녕하세요"`)
	if len(ran) != 1 || ran[0]["model"] != "gpt-test" {
		t.Fatalf("codex ran %v", ran)
	}
	f.route(func(string) *where {
		return &where{kind: "chat", base: f.base + "/v1", key: "stored-key", name: "other"}
	})
	f.replies["other"] = upstreamReply{200, "application/json", chatReply(t, "안녕하세요", "other")}
	mustStatus(t, f.post(""), 200)
	seen := f.seenList()[0]
	if seen.path != "/v1/chat/completions" || strings.Contains(sortedJSON(seen.header), "caller-") {
		t.Fatalf("sent %s %v", seen.path, seen.header)
	}
	// Passed through natively, not translated: the caller's Chat body with only the model replaced.
	sameJSON(t, seen.body, `{"model": "other", "messages": [{"role": "user", "content": "안녕"}]}`)
}

// native routes every model to a Chat provider that answers "안녕하세요".
func (f *fixture) native(t *testing.T) {
	f.route(func(string) *where {
		return &where{kind: "chat", base: f.base + "/v1", key: "stored-key", name: "first"}
	})
	f.replies["first"] = upstreamReply{200, "application/json", chatReply(t, "안녕하세요", "first")}
}

func TestChatEntranceForwardsResponseFormatToChatTargetsOnly(t *testing.T) {
	f := newFixture(t)
	f.native(t)
	schema := `{"type": "object", "properties": {"값": {"type": "string"}}, "required": ["값"]}`
	for _, format := range []string{`{"type": "json_object"}`,
		`{"type": "json_schema", "json_schema": {"name": "answer", "schema": ` + schema + `, "strict": false}}`,
		`{"type": "json_schema", "json_schema": {"name": "answer", "schema": ` + schema + `}}`} {
		f.clear()
		reply := f.post(`{"model": "custom-test/first", "messages": [{"role": "user", "content": "hi"}], "response_format": ` + format + `}`)
		mustStatus(t, reply, 200)
		sameJSON(t, at(parsed(t, reply.body), "choices", 0, "message", "content"), `"안녕하세요"`)
		seen := f.seenList()[0]
		if seen.path != "/v1/chat/completions" {
			t.Fatal(seen.path)
		}
		sameJSON(t, at(seen.body, "model"), `"first"`)
		sameJSON(t, at(seen.body, "response_format"), format)
	}
}

func TestChatTargetsThatCannotExpressResponseFormatRefuseBeforeAnyEgress(t *testing.T) {
	f := newFixture(t)
	request := `{"model": "custom-test/first", "messages": [{"role": "user", "content": "hi"}], "response_format": {"type": "json_object"}}`
	for _, w := range []*where{{kind: "direct", base: f.base, key: "host-secret", name: "first"}, {kind: "anthropic"},
		{kind: "codex", model: "gpt-test"}, {kind: "google", model: "gemini-test"}} {
		t.Run(w.kind, func(t *testing.T) {
			f.clear()
			f.route(func(string) *where { return w })
			calls := noExecution(t)
			reply := f.post(request)
			mustStatus(t, reply, 400)
			sameJSON(t, errorMessage(t, reply.body), `"unsupported_field"`)
			if f.seenCount() != 0 || len(f.authList()) != 0 || f.reportCount() != 0 || *calls != 0 {
				t.Fatal("egress before refusal")
			}
		})
	}
}

func TestChatGroupSkipsOnlyTheCandidateThatCannotExpressResponseFormat(t *testing.T) {
	f := newFixture(t)
	f.setDecision("anthropic/claude-test", "custom-test/backup")
	f.route(func(model string) *where {
		if strings.HasPrefix(model, "anthropic/") {
			return &where{kind: "anthropic"}
		}
		return f.chatWhere(strings.SplitN(model, "/", 2)[1])
	})
	f.replies["backup"] = upstreamReply{200, "application/json", chatReply(t, "안녕하세요", "backup")}
	reply := f.post(`{"model": "group/test", "messages": [{"role": "user", "content": "hi"}], "response_format": {"type": "json_object"}}`)
	mustStatus(t, reply, 200)
	var sent []any
	for _, s := range f.seenList() {
		sent = append(sent, []any{s.path, at(s.body, "model"), at(s.body, "response_format")})
	}
	sameJSON(t, sent, `[["/v1/chat/completions", "backup", {"type": "json_object"}]]`)
	var reports []any
	for _, r := range f.reportList() {
		reports = append(reports, []any{r["model"], r["ok"], r["error"]})
	}
	// A skipped candidate is not a call that happened.
	sameJSON(t, reports, `[["custom-test/backup", true, ""]]`)
	if len(f.authList()) != 0 {
		t.Fatal("skipped candidate asked for a login")
	}
}

func TestChatGroupWhereEveryCandidateIsNonChatReportsUnsupportedField(t *testing.T) {
	f := newFixture(t)
	f.setDecision("anthropic/claude-test", "custom-test/backup")
	f.route(f.defaultWhere)
	reply := f.post(`{"model": "group/test", "messages": [{"role": "user", "content": "hi"}], "response_format": {"type": "json_object"}}`)
	mustStatus(t, reply, 400)
	sameJSON(t, errorMessage(t, reply.body), `"unsupported_field"`)
	if f.seenCount() != 0 || len(f.authList()) != 0 || f.reportCount() != 0 {
		t.Fatal("a skipped candidate made a call")
	}
}

func TestChatBadResponseFormatIsRefusedAtTheEntrance(t *testing.T) {
	f := newFixture(t)
	f.native(t) // a chat target would otherwise forward whatever it was given
	for _, format := range []string{`{"type": "text"}`, `{"type": "json"}`, `{"type": "JSON_OBJECT"}`,
		`{"type": "json_object", "name": "answer"}`, `{"type": "json_schema"}`,
		`{"type": "json_schema", "json_schema": []}`,
		`{"type": "json_schema", "json_schema": {"name": "answer"}}`,
		`{"type": "json_schema", "json_schema": {"name": "bad name", "schema": {}}}`,
		`{"type": "json_schema", "json_schema": {"name": "answer", "schema": {}, "strict": true}}`,
		`{"type": "json_schema", "json_schema": {"name": "answer", "schema": {}, "description": "d"}}`,
		`"json_object"`, `[]`} {
		reply := f.post(`{"model": "custom-test/first", "messages": [{"role": "user", "content": "hi"}], "response_format": ` + format + `}`)
		if reply.status != 400 || at(parsed(t, reply.body), "error") == nil {
			t.Fatalf("%s: %d %s", format, reply.status, reply.body)
		}
	}
	if f.seenCount() != 0 || f.decisions != 0 {
		t.Fatal("bad format executed")
	}
}

func TestChatAbsentResponseFormatIsUnchanged(t *testing.T) {
	f := newFixture(t)
	f.native(t)
	reply := f.post(`{"model": "custom-test/first", "messages": [{"role": "user", "content": "hi"}]}`)
	mustStatus(t, reply, 200)
	sameJSON(t, at(parsed(t, reply.body), "choices", 0, "message", "content"), `"안녕하세요"`)
	seen := f.seenList()
	if len(seen) != 1 || has(seen[0].body.(host.Obj), "response_format") {
		t.Fatalf("sent %v", seen)
	}
}

func TestChatProviderRedirectDoesNotForwardCredentials(t *testing.T) {
	f := newFixture(t)
	var mu sync.Mutex
	var paths []string
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("location", "/unexpected")
		w.WriteHeader(307)
	}))
	t.Cleanup(redirect.Close)
	f.route(func(string) *where {
		return &where{kind: "direct", base: redirect.URL, key: "stored-key", name: "first"}
	})
	reply := f.post("")
	mustStatus(t, reply, 502)
	mu.Lock()
	defer mu.Unlock()
	if at(parsed(t, reply.body), "error") == nil || fmt.Sprint(paths) != "[/v1/messages]" {
		t.Fatalf("redirect followed: %v %s", paths, reply.body)
	}
}

func TestChatCodexCannotSilentlyDropAnExplicitOutputLimit(t *testing.T) {
	f := newFixture(t)
	f.route(func(string) *where { return &where{kind: "codex", model: "gpt-test"} })
	calls := noExecution(t)
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		reply := f.post(`{"model": "openai/gpt-test", "messages": [{"role": "user", "content": "hi"}], "` + key + `": 12}`)
		mustStatus(t, reply, 400)
		sameJSON(t, errorMessage(t, reply.body), `"unsupported_field"`)
	}
	if *calls != 0 {
		t.Fatal("codex ran")
	}
}

func TestChatCodexUnsupportedSamplingCanUseGroupFallbackBeforeExecution(t *testing.T) {
	f := newFixture(t)
	f.setDecision("openai/gpt-test", "custom-test/backup")
	f.route(func(model string) *where {
		if strings.HasPrefix(model, "openai/") {
			return &where{kind: "codex", model: "gpt-test"}
		}
		return &where{kind: "direct", base: f.base, key: "host-secret", name: "backup"}
	})
	calls := noExecution(t)
	reply := f.post(`{"model": "group/test", "messages": [{"role": "user", "content": "hi"}], "temperature": 0.5}`)
	mustStatus(t, reply, 200)
	seen := f.seenList()
	if *calls != 0 || len(seen) != 1 || at(seen[0].body, "model") != "backup" {
		t.Fatalf("calls %d seen %d", *calls, len(seen))
	}
	sameJSON(t, at(seen[0].body, "temperature"), `0.5`)
}

func TestChatNativeChatPreservesParallelToolsAndDisableSetting(t *testing.T) {
	f := newFixture(t)
	f.route(func(string) *where {
		return &where{kind: "chat", base: f.base + "/v1", key: "stored-key", name: "first"}
	})
	f.replies["first"] = upstreamReply{200, "text/event-stream", fixtureWire(fixtureTools(), true)}
	tools := `[{"type": "function", "function": {"name": "first"}}, {"type": "function", "function": {"name": "second"}}]`
	request := `{"model": "custom-test/first", "messages": [{"role": "user", "content": "hi"}], "stream": true,
		"tools": ` + tools + `, "parallel_tool_calls": false}`
	reply := f.post(request)
	mustStatus(t, reply, 200)
	sent := f.seenList()[0].body
	sameJSON(t, at(sent, "parallel_tool_calls"), `false`)
	sameJSON(t, at(sent, "tools"), tools)
	// Passed through natively, not translated.
	sameJSON(t, sent, string(mustPy(with(objOf(t, request), "model", "first"))))
	var calls []string
	for _, e := range chatData(reply.body) {
		for _, c := range at(e, "choices").([]any) {
			list, _ := at(c, "delta", "tool_calls").([]any)
			for _, call := range list {
				calls = append(calls, sortedJSON([]any{at(call, "index"), at(call, "function", "arguments")}))
			}
		}
	}
	if len(calls) < 2 || fmt.Sprint(calls[len(calls)-2:]) != `[[1, "{\"y\":2}"] [0, "{\"x\":1}"]]` {
		t.Fatalf("tool calls %v", calls)
	}
	if !bytes.HasSuffix(reply.body, []byte("data: [DONE]\n\n")) {
		t.Fatal("no [DONE]")
	}
}

func streamEvents() []string {
	v, _ := Parse(message("안녕하세요"))
	start := v.(host.Obj)
	start.Set("content", []any{})
	start.Set("stop_reason", nil)
	return []string{`{"type": "message_start", "message": ` + string(mustPy(start)) + `}`,
		`{"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}`,
		`{"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "안녕😀"}}`,
		`{"type": "content_block_stop", "index": 0}`,
		`{"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"output_tokens": 2}}`,
		`{"type": "message_stop"}`}
}

func TestChatStreamAndUsageOverTCP(t *testing.T) {
	f := newFixture(t)
	f.replies["first"] = upstreamReply{200, "text/event-stream", sse(streamEvents()...)}
	reply := f.post(`{"model": "group/test", "messages": [{"role": "user", "content": "hi"}], "stream": true, "stream_options": {"include_usage": true}}`)
	mustStatus(t, reply, 200)
	if reply.header.Get("content-type") != "text/event-stream" {
		t.Fatal(reply.header.Get("content-type"))
	}
	usage, text := false, ""
	for _, e := range chatData(reply.body) {
		if n, ok := host.Int(at(e, "usage", "total_tokens")); ok && n == 6 {
			usage = true
		}
		for _, c := range at(e, "choices").([]any) {
			s, _ := at(c, "delta", "content").(string)
			text += s
		}
	}
	if !usage || !strings.Contains(text, "안녕😀") || !bytes.HasSuffix(reply.body, []byte("data: [DONE]\n\n")) {
		t.Fatalf("got %s", reply.body)
	}
	if f.lastReport()["ok"] != true {
		t.Fatal("not reported as ok")
	}
}

// failedStream checks an answer that must never look complete: whether the failure came as
// an HTTP error or after the stream started, it carries an error and no [DONE].
func failedStream(t *testing.T, f *fixture, reply response, statuses ...int) {
	t.Helper()
	ok := false
	for _, s := range statuses {
		ok = ok || reply.status == s
	}
	if !ok {
		t.Fatalf("status %d: %s", reply.status, reply.body)
	}
	if bytes.Contains(reply.body, []byte("[DONE]")) || bytes.Contains(reply.body, []byte("secret")) {
		t.Fatalf("looks complete or leaked: %s", reply.body)
	}
	if !bytes.Contains(reply.body, []byte(`"error"`)) {
		t.Fatalf("no error in body (connection error %v): %s", reply.err, reply.body)
	}
	if f.seenCount() != 1 {
		t.Fatalf("retried: %d upstream calls", f.seenCount())
	}
	if f.lastReport()["ok"] != false {
		t.Fatal("reported as ok")
	}
}

func TestChatTruncatedAndErrorStreamsNeverRetryOrFinishSuccessfully(t *testing.T) {
	f := newFixture(t)
	for name, ending := range map[string][]string{"truncated": nil,
		"error": {`{"type": "error", "error": {"type": "overloaded_error", "message": "host-secret"}}`}} {
		t.Run(name, func(t *testing.T) {
			f.clear()
			f.replies["first"] = upstreamReply{200, "text/event-stream", sse(append(streamEvents()[:3], ending...)...)}
			reply := f.post(`{"model": "group/test", "messages": [{"role": "user", "content": "hi"}], "stream": true}`)
			if reply.status != 200 || bytes.Contains(reply.body, []byte("[DONE]")) || bytes.Contains(reply.body, []byte("secret")) {
				t.Fatalf("got %d %s", reply.status, reply.body)
			}
			if f.seenCount() != 1 || f.lastReport()["ok"] != false {
				t.Fatal("retried or reported as ok")
			}
		})
	}
}

func TestChatInvalidSuccessResponseIsNotReplayed(t *testing.T) {
	f := newFixture(t)
	f.replies["first"] = upstreamReply{200, "application/json", []byte(`{"broken":true}`)}
	reply := f.post(`{"model": "group/test", "messages": [{"role": "user", "content": "hi"}]}`)
	mustStatus(t, reply, 502)
	if at(parsed(t, reply.body), "error") == nil || f.seenCount() != 1 || f.lastReport()["ok"] != false {
		t.Fatalf("got %s", reply.body)
	}
}

func TestChatDeclaredTruncationAndOutputLimitNeverLookSuccessful(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct{ limit, extra int }{{subscription.MaxOutput, 10}, {20, 0}} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprint(c.limit, c.extra, streaming), func(t *testing.T) {
				swap(t, &subscription.MaxOutput, c.limit)
				f.clear()
				f.lengthExtra = c.extra
				t.Cleanup(func() { f.lengthExtra = 0 })
				f.replies["first"] = upstreamReply{200, "application/json", []byte(message("안녕하세요"))}
				status := 502
				if streaming {
					f.replies["first"] = upstreamReply{200, "text/event-stream", sse(streamEvents()...)}
					status = 200
				}
				reply := f.post(`{"model": "group/test", "messages": [{"role": "user", "content": "hi"}], "stream": ` +
					strconv.FormatBool(streaming) + `, "stream_options": null}`)
				failedStream(t, f, reply, status)
			})
		}
	}
}

// From test_arkism_codex.test_reasoning_usage_preserved_through_messages_chat_gemini_json_and_sse:
// the Codex bridge's Messages output (recorded from Python) through Chat and Gemini, JSON and SSE.
func TestReasoningUsagePreservedThroughMessagesChatGeminiJSONAndSSE(t *testing.T) {
	for _, reasoning := range []int{-1, 0, 4, 9} {
		for _, stop := range []string{"end_turn", "max_tokens"} {
			for _, streaming := range []bool{false, true} {
				t.Run(fmt.Sprint(reasoning, stop, streaming), func(t *testing.T) {
					usage := `{"input_tokens":27,"output_tokens":9}`
					wantChat := `{"prompt_tokens": 27, "completion_tokens": 9, "total_tokens": 36}`
					wantGemini := `{"promptTokenCount": 27, "candidatesTokenCount": 9, "totalTokenCount": 36}`
					if reasoning >= 0 {
						usage = fmt.Sprintf(`{"input_tokens":27,"output_tokens":9,"output_tokens_details":{"reasoning_tokens":%d}}`, reasoning)
						wantChat = fmt.Sprintf(`{"prompt_tokens": 27, "completion_tokens": 9, "total_tokens": 36, "completion_tokens_details": {"reasoning_tokens": %d}}`, reasoning)
						wantGemini = fmt.Sprintf(`{"promptTokenCount": 27, "candidatesTokenCount": %d, "totalTokenCount": 36, "thoughtsTokenCount": %d}`, 9-reasoning, reasoning)
					}
					var chatUsage, geminiUsage any
					if streaming {
						messages := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"synthetic-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"안녕하세요 🌱"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"` + stop + `","stop_sequence":null},"usage":` + usage + `}

event: message_stop
data: {"type":"message_stop"}

`
						var chat [][]byte
						if err := CodecStream(once([]byte(messages)), "m", true, func(b []byte) error {
							chat = append(chat, append([]byte(nil), b...))
							return nil
						}); err != nil {
							t.Fatal(err)
						}
						chatUsage = at(parsed(t, bytes.TrimPrefix(chat[len(chat)-2], []byte("data: "))), "usage")
						var gemini [][]byte
						frames := func(yield func([]byte, error) bool) {
							for _, c := range chat {
								if !yield(c, nil) {
									return
								}
							}
						}
						if err := GeminiStream(frames, "m", func(b []byte) error {
							gemini = append(gemini, append([]byte(nil), b...))
							return nil
						}); err != nil {
							t.Fatal(err)
						}
						geminiUsage = at(parsed(t, bytes.TrimPrefix(gemini[len(gemini)-1], []byte("data: "))), "usageMetadata")
					} else {
						messages := objOf(t, `{"id":"msg_1","type":"message","role":"assistant","model":"synthetic-model","content":[{"type":"text","text":"안녕하세요 🌱"}],"stop_reason":"`+
							stop+`","stop_sequence":null,"usage":`+usage+`}`)
						chat, err := CodecResponse(messages, "m")
						if err != nil {
							t.Fatal(err)
						}
						chatUsage = at(chat, "usage")
						result, err := GeminiResponse(chat, "m")
						if err != nil {
							t.Fatal(err)
						}
						geminiUsage = at(result, "usageMetadata")
					}
					sameJSON(t, chatUsage, wantChat)
					sameJSON(t, geminiUsage, wantGemini)
				})
			}
		}
	}
}
