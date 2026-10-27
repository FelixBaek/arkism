package gateway

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/subscription"
)

// The Messages entrance on Chat providers.

const messagesRequest = `{"model": "custom-test/first", "max_tokens": 32, "messages": [{"role": "user", "content": "안녕"}]}`

func newMessagesFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.url = strings.Replace(f.url, "/chat/completions", "/messages", 1)
	f.route(func(model string) *where {
		if strings.HasSuffix(model, "/backup") {
			return &where{kind: "direct", base: f.base, key: "host-secret", name: "backup"}
		}
		_, name, found := strings.Cut(model, "/")
		if !found {
			name = model
		}
		return f.chatWhere(name)
	})
	f.replies["first"] = upstreamReply{200, "application/json", chatReply(t, "안녕하세요", "first")}
	return f
}

// messagesWith is messagesRequest with the given fields set.
func messagesWith(t *testing.T, fields ...any) string {
	body := objOf(t, messagesRequest)
	for i := 0; i+1 < len(fields); i += 2 {
		value := fields[i+1]
		if s, ok := value.(string); ok && fields[i] != "model" { // JSON text, except the model name
			value = parsed(t, []byte(s))
		}
		body.Set(fields[i].(string), value)
	}
	return string(mustPy(body))
}

// messagesFrames decodes the data of each SSE frame.
func messagesFrames(t *testing.T, raw []byte) []any {
	var events []any
	SSEFrames(once(raw), func(_, data string) error {
		events = append(events, parsed(t, []byte(data)))
		return nil
	})
	return events
}

func TestMessagesChatJSONRequestResponseAndAuthBoundaries(t *testing.T) {
	f := newMessagesFixture(t)
	reply := f.post(messagesRequest)
	mustStatus(t, reply, 200)
	out := parsed(t, reply.body)
	sameJSON(t, at(out, "type"), `"message"`)
	sameJSON(t, at(out, "content", 0, "text"), `"안녕하세요"`)
	sameJSON(t, at(out, "usage", "input_tokens"), `4`)
	seen := f.seenList()[0]
	if seen.path != "/v1/chat/completions" {
		t.Fatal(seen.path)
	}
	sameJSON(t, at(seen.body, "model"), `"first"`)
	sameJSON(t, at(seen.body, "max_tokens"), `32`)
	if strings.Contains(sortedJSON(seen.header), "caller-") || reply.header.Get("x-private") != "" || f.decisions != 0 {
		t.Fatal("credentials crossed the boundary or a route was decided")
	}
}

func TestMessagesChatParallelToolStreamHasDistinctIDsArgumentsAndOption(t *testing.T) {
	f := newMessagesFixture(t)
	f.replies["first"] = upstreamReply{200, "text/event-stream", fixtureWire(append(fixtureTools(), fixtureUsageChunk()), true)}
	reply := f.post(messagesWith(t, "stream", true,
		"tools", `[{"name": "first", "input_schema": {"type": "object"}}, {"name": "second", "input_schema": {"type": "object"}}]`,
		"tool_choice", `{"type": "auto", "disable_parallel_tool_use": true}`))
	mustStatus(t, reply, 200)
	sent := f.seenList()[0].body
	sameJSON(t, at(sent, "parallel_tool_calls"), `false`)
	sameJSON(t, at(sent, "stream_options", "include_usage"), `true`)
	events := messagesFrames(t, reply.body)
	starts, args := map[string]any{}, map[string]any{}
	var usage any
	for _, e := range events {
		index := sortedJSON(at(e, "index"))
		switch {
		case at(e, "type") == "content_block_start" && at(e, "content_block", "type") == "tool_use":
			starts[index] = at(e, "content_block", "id")
		case at(e, "type") == "content_block_delta" && at(e, "delta", "type") == "input_json_delta":
			args[index] = parsed(t, []byte(at(e, "delta", "partial_json").(string)))
		case at(e, "type") == "message_delta" && usage == nil:
			usage = at(e, "usage", "output_tokens")
		}
	}
	byID := host.Obj{}
	for index, id := range starts {
		byID.Set(id.(string), args[index])
	}
	sameJSON(t, byID, `{"call_0": {"x": 1}, "call_1": {"y": 2}}`)
	sameJSON(t, at(events[len(events)-1], "type"), `"message_stop"`)
	sameJSON(t, usage, `4`)
}

func TestMessagesChatToolResultsRemainBoundToIDs(t *testing.T) {
	f := newMessagesFixture(t)
	mustStatus(t, f.post(messagesWith(t, "messages", `[{"role": "user", "content": "안녕"}, {"role": "assistant", "content": [
		{"type": "tool_use", "id": "call_a", "name": "first", "input": {"x": 1}},
		{"type": "tool_use", "id": "call_b", "name": "second", "input": {"y": 2}}]},
		{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call_b", "content": "둘"}, {"type": "tool_result", "tool_use_id": "call_a", "content": "하나"}]}]`)), 200)
	var ids, arguments []any
	for _, m := range at(f.seenList()[0].body, "messages").([]any) {
		switch at(m, "role") {
		case "tool":
			ids = append(ids, at(m, "tool_call_id"))
		case "assistant":
			for _, c := range at(m, "tool_calls").([]any) {
				arguments = append(arguments, parsed(t, []byte(at(c, "function", "arguments").(string))))
			}
		}
	}
	sameJSON(t, ids, `["call_b", "call_a"]`)
	sameJSON(t, arguments, `[{"x": 1}, {"y": 2}]`)
}

func TestMessagesChatReturnedProviderToolIDRoundTrips(t *testing.T) {
	f := newMessagesFixture(t)
	reply := parsed(t, chatReply(t, "안녕하세요", "first")).(host.Obj)
	choice := at(reply, "choices", 0).(host.Obj)
	choice.Set("finish_reason", "tool_calls")
	choice.Set("message", parsed(t, []byte(`{"role": "assistant", "content": null, "tool_calls": [{"id": "call.a/한글",
		"type": "function", "function": {"name": "lookup", "arguments": "{}"}}]}`)))
	reply.Set("choices", []any{choice})
	f.replies["first"] = upstreamReply{200, "application/json", mustPy(reply)}
	got := f.post(messagesRequest)
	mustStatus(t, got, 200)
	content := at(parsed(t, got.body), "content")
	messages := []any{at(objOf(t, messagesRequest), "messages", 0),
		host.Obj{{Key: "role", Value: "assistant"}, {Key: "content", Value: content}},
		host.Obj{{Key: "role", Value: "user"}, {Key: "content", Value: []any{host.Obj{{Key: "type", Value: "tool_result"},
			{Key: "tool_use_id", Value: at(content, 0, "id")}, {Key: "content", Value: "done"}}}}}}
	mustStatus(t, f.post(string(mustPy(with(objOf(t, messagesRequest), "messages", messages)))), 200)
	seen := f.seenList()
	sent := at(seen[len(seen)-1].body, "messages").([]any)
	sameJSON(t, at(sent[len(sent)-1], "tool_call_id"), `"call.a/한글"`)
}

func TestMessagesChatGroupReplaysRejectionAndServerError(t *testing.T) {
	f := newMessagesFixture(t)
	request := messagesWith(t, "model", "group/test")
	for _, failure := range []int{429, 500} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			f.clear()
			f.replies["first"] = upstreamReply{failure, "application/json", []byte(`{"error":"host-secret private-provider-details"}`)}
			reply := f.post(request)
			mustStatus(t, reply, 200)
			if f.seenCount() != 2 || f.reportCount() != 2 {
				t.Fatalf("seen %d reports %d", f.seenCount(), f.reportCount())
			}
			if bytes.Contains(reply.body, []byte("private-provider-details")) || bytes.Contains(reply.body, []byte("host-secret")) {
				t.Fatalf("leaked: %s", reply.body)
			}
		})
	}
}

func TestMessagesChatStreamTruncationMissingUsageAndHTTPLengthNeverReplay(t *testing.T) {
	f := newMessagesFixture(t)
	for _, missing := range []string{"done", "usage", "http_length"} {
		t.Run(missing, func(t *testing.T) {
			f.clear()
			events := fixtureTools()
			if missing != "usage" {
				events = append(events, fixtureUsageChunk())
			}
			f.lengthExtra = 0
			if missing == "http_length" {
				f.lengthExtra = 10
			}
			t.Cleanup(func() { f.lengthExtra = 0 })
			f.replies["first"] = upstreamReply{200, "text/event-stream", fixtureWire(events, missing != "done")}
			reply := f.post(messagesWith(t, "model", "group/test", "stream", true))
			if reply.status != 200 && reply.status != 502 {
				t.Fatalf("status %d", reply.status)
			}
			if bytes.Contains(reply.body, []byte("event: message_stop")) || bytes.Contains(reply.body, []byte(`"type": "tool_use"`)) ||
				bytes.Contains(reply.body, []byte(`"type":"tool_use"`)) {
				t.Fatalf("looks complete: %s", reply.body)
			}
			if !bytes.Contains(reply.body, []byte("error")) {
				t.Fatalf("no error in body (connection error %v): %s", reply.err, reply.body)
			}
			if f.seenCount() != 1 || f.lastReport()["ok"] != false {
				t.Fatal("replayed or reported as ok")
			}
		})
	}
}

func TestMessagesChatInvalidToolSequenceFailsBeforeNetwork(t *testing.T) {
	f := newMessagesFixture(t)
	reply := f.post(messagesWith(t, "messages", `[{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "orphan", "content": "x"}]}]`))
	mustStatus(t, reply, 400)
	if at(parsed(t, reply.body), "error") == nil || f.seenCount() != 0 {
		t.Fatalf("got %s", reply.body)
	}
}

func TestMessagesChatMalformedToolsFailBeforeCompatibilityFilterOrNetwork(t *testing.T) {
	f := newMessagesFixture(t)
	for _, value := range []string{`null`, `[null]`, `"input_schema"`, `{"name": "tool"}`, `[{}]`, `[{"name": "tool"}]`} {
		reply := f.post(messagesWith(t, "tools", value))
		if reply.status != 400 || errorMessage(t, reply.body) != "invalid_request" || f.seenCount() != 0 {
			t.Fatalf("%s: %d %s", value, reply.status, reply.body)
		}
	}
}

func TestMessagesChatResponseFormatIsRefusedAtTheMessagesEntrance(t *testing.T) {
	f := newMessagesFixture(t)
	entrance := f.url
	request := messagesWith(t, "response_format", `{"type": "json_object"}`)
	for _, w := range []*where{{kind: "chat", base: f.base + "/v1", key: "host-secret", name: "first"},
		{kind: "direct", base: f.base, key: "host-secret", name: "first"}, {kind: "anthropic"}} {
		for _, suffix := range []string{"", "/count_tokens"} {
			t.Run(w.kind+suffix, func(t *testing.T) {
				f.clear()
				f.route(func(string) *where { return w })
				f.url = entrance + suffix
				reply := f.post(request)
				mustStatus(t, reply, 400)
				sameJSON(t, errorMessage(t, reply.body), `"unsupported_field"`)
				if f.seenCount() != 0 || len(f.authList()) != 0 {
					t.Fatal("egress before refusal")
				}
			})
		}
	}
}

func TestMessagesChatAbsentResponseFormatKeepsTheMessagesPathUnchanged(t *testing.T) {
	f := newMessagesFixture(t)
	mustStatus(t, f.post(messagesRequest), 200)
	if f.seenList()[0].path != "/v1/chat/completions" {
		t.Fatal(f.seenList()[0].path)
	}
	f.clear()
	f.route(func(string) *where { return &where{kind: "anthropic"} })
	request := messagesWith(t, "model", "claude-test")
	reply := f.post(request)
	mustStatus(t, reply, 200)
	sameJSON(t, at(parsed(t, reply.body), "type"), `"message"`)
	seen := f.seenList()[0]
	if seen.path != "/v1/messages" {
		t.Fatal(seen.path)
	}
	sameJSON(t, seen.body, request) // still verbatim
}

func TestMessagesChatTextArrivesBeforeUpstreamFinishes(t *testing.T) {
	f := newMessagesFixture(t)
	release := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	t.Cleanup(free)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body.Close()
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		// The redactor retains a key-length tail between reads.
		w.Write(append(fixtureWire([]host.Obj{fixtureChunk(`{"content": "early text"}`, nil)}, false), ": padding for redaction\n\n"...))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		usage, _ := Parse(`{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3}`)
		w.Write(fixtureWire([]host.Obj{fixtureChunk("", "stop", host.KV{Key: "usage", Value: usage})}, true))
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(free) // runs before upstream.Close, which waits for the handler
	f.route(func(string) *where {
		return &where{kind: "chat", base: upstream.URL + "/v1", key: "host-secret", name: "first"}
	})
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post(f.url, "application/json", strings.NewReader(messagesWith(t, "stream", true)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	early := false
	for range 12 {
		line, err := reader.ReadString('\n')
		if strings.Contains(line, "early text") {
			early = true
			break
		}
		if err != nil {
			break
		}
	}
	if !early {
		t.Fatal("text was buffered until upstream completion")
	}
	select {
	case <-release:
		t.Fatal("released early")
	default:
	}
	free()
	var rest bytes.Buffer
	rest.ReadFrom(reader)
	if !strings.Contains(rest.String(), "event: message_stop") {
		t.Fatalf("no message_stop: %s", rest.String())
	}
}

func TestMessagesChatOutputLimitAndRedactionAreAppliedToLegacyChat(t *testing.T) {
	f := newMessagesFixture(t)
	f.replies["first"] = upstreamReply{200, "application/json", chatReply(t, "host-secret", "first")}
	reply := f.post(messagesRequest)
	mustStatus(t, reply, 200)
	if bytes.Contains(reply.body, []byte("host-secret")) {
		t.Fatal("secret not redacted")
	}
	swap(t, &subscription.MaxOutput, 20)
	reply = f.post(messagesRequest)
	mustStatus(t, reply, 502)
	if at(parsed(t, reply.body), "error") == nil {
		t.Fatalf("got %s", reply.body)
	}
}
