package gateway

import (
	"bytes"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// Host-key OpenAI and xAI Chat providers through the real
// resolve, reached from every public API.

const providerKeys = `{"keys":[{"provider":"openai","key":"host-secret","base_url":"%[1]s/v1"},{"provider":"xai","key":"host-secret","base_url":"%[1]s/v1"}]}`

type providerAPI struct {
	*fixture
	root string
}

func newProviderAPI(t *testing.T) *providerAPI {
	p := &providerAPI{fixture: newFixture(t)}
	p.root = p.gw.URL
	swap(t, &target, resolve)
	swap(t, &host.APIMode, map[string]bool{"codex": true, "grok": true})
	writeFile(t, host.KeysFile, fmt.Sprintf(providerKeys, p.base))
	swap(t, &codexProfile, func(string, []string) map[string]any {
		t.Error("subscription must not be read")
		return nil
	})
	p.reply(false, false, true)
	return p
}

func providerChunk(delta, finish any) host.Obj {
	return host.Obj{{Key: "id", Value: "chatcmpl-test"}, {Key: "object", Value: "chat.completion.chunk"}, {Key: "created", Value: 1},
		{Key: "model", Value: "upstream"}, {Key: "choices", Value: []any{host.Obj{{Key: "index", Value: 0}, {Key: "delta", Value: delta},
			{Key: "finish_reason", Value: finish}}}}}
}

func (p *providerAPI) reply(tool, streaming, done bool) {
	msg, _ := Parse(message("안녕하세요"))
	body, err := CodecResponse(msg, "first")
	if err != nil {
		p.t.Fatal(err)
	}
	if tool {
		choice := at(body, "choices", 0).(host.Obj)
		choice.Set("finish_reason", "tool_calls")
		choice.Set("message", host.Obj{{Key: "role", Value: "assistant"}, {Key: "content", Value: nil}, {Key: "tool_calls", Value: []any{
			host.Obj{{Key: "id", Value: "call_echo"}, {Key: "type", Value: "function"}, {Key: "function", Value: host.Obj{
				{Key: "name", Value: "echo"}, {Key: "arguments", Value: `{"word":"한글"}`}}}}}}})
	}
	if !streaming {
		p.replies["first"] = upstreamReply{200, "application/json", mustPy(body)}
		return
	}
	last := providerChunk(host.Obj{}, nil)
	last.Set("choices", []any{})
	last.Set("usage", get(body, "usage"))
	frames := []host.Obj{providerChunk(host.Obj{{Key: "role", Value: "assistant"}, {Key: "content", Value: "안녕"}}, nil),
		providerChunk(host.Obj{{Key: "content", Value: "하세요"}}, nil), providerChunk(host.Obj{}, "stop"), last}
	var b bytes.Buffer
	for _, f := range frames {
		b.WriteString("data: " + string(pyDumpsUnicode(f)) + "\n\n")
	}
	if done {
		b.WriteString("data: [DONE]\n\n")
	}
	p.replies["first"] = upstreamReply{200, "text/event-stream", b.Bytes()}
}

// request is the google-cross helper: one tool-offering request per public API.
func (p *providerAPI) request(protocol, model string, stream bool) host.Obj {
	p.url = p.root + "/v1/" + map[string]string{"chat": "chat/completions", "messages": "messages", "responses": "responses"}[protocol]
	parameters := host.Obj{{Key: "type", Value: "object"}, {Key: "properties", Value: host.Obj{{Key: "word", Value: host.Obj{{Key: "type", Value: "string"}}}}}}
	body := host.Obj{{Key: "model", Value: model}, {Key: "stream", Value: stream}}
	ask := []any{host.Obj{{Key: "role", Value: "user"}, {Key: "content", Value: "도구를 써 주세요"}}}
	switch protocol {
	case "chat":
		body = append(body, host.KV{Key: "messages", Value: ask}, host.KV{Key: "max_completion_tokens", Value: 256},
			host.KV{Key: "tools", Value: []any{host.Obj{{Key: "type", Value: "function"}, {Key: "function", Value: host.Obj{
				{Key: "name", Value: "echo"}, {Key: "parameters", Value: parameters}}}}}})
		if stream {
			body = append(body, host.KV{Key: "stream_options", Value: host.Obj{{Key: "include_usage", Value: true}}})
		}
	case "messages":
		body = append(body, host.KV{Key: "messages", Value: ask}, host.KV{Key: "max_tokens", Value: 256},
			host.KV{Key: "tools", Value: []any{host.Obj{{Key: "name", Value: "echo"}, {Key: "input_schema", Value: parameters}}}})
	default:
		body = append(body, host.KV{Key: "input", Value: "도구를 써 주세요"}, host.KV{Key: "store", Value: false},
			host.KV{Key: "max_output_tokens", Value: 256}, host.KV{Key: "tools", Value: []any{host.Obj{{Key: "type", Value: "function"},
				{Key: "name", Value: "echo"}, {Key: "parameters", Value: parameters}, {Key: "strict", Value: false}}}})
	}
	return body
}

// providerFollow answers the first tool call of first with {"result":"결과"}.
func providerFollow(protocol string, body host.Obj, first any) host.Obj {
	out := clone(body).(host.Obj)
	result := `{"result":"결과"}`
	switch protocol {
	case "chat":
		message := at(first, "choices", 0, "message")
		call := at(message, "tool_calls", 0)
		out.Set("messages", append(get(out, "messages").([]any), message,
			host.Obj{{Key: "role", Value: "tool"}, {Key: "tool_call_id", Value: get(call, "id")}, {Key: "content", Value: result}}))
	case "messages":
		var call any
		for _, b := range get(first, "content").([]any) {
			if get(b, "type") == "tool_use" && call == nil {
				call = b
			}
		}
		out.Set("messages", append(get(out, "messages").([]any), host.Obj{{Key: "role", Value: "assistant"}, {Key: "content", Value: get(first, "content")}},
			host.Obj{{Key: "role", Value: "user"}, {Key: "content", Value: []any{host.Obj{{Key: "type", Value: "tool_result"},
				{Key: "tool_use_id", Value: get(call, "id")}, {Key: "content", Value: result}}}}}))
	default:
		var call any
		for _, b := range get(first, "output").([]any) {
			if get(b, "type") == "function_call" && call == nil {
				call = b
			}
		}
		input := append([]any{host.Obj{{Key: "role", Value: "user"}, {Key: "content", Value: get(body, "input")}}}, get(first, "output").([]any)...)
		out.Set("input", append(input, host.Obj{{Key: "type", Value: "function_call_output"}, {Key: "call_id", Value: get(call, "call_id")},
			{Key: "output", Value: result}}))
	}
	return out
}

func (p *providerAPI) postObj(body host.Obj, headers ...string) response {
	return p.post(string(pyDumpsUnicode(body)), headers...)
}

func (p *providerAPI) last() seenRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[len(p.seen)-1]
}

func (p *providerAPI) sentModels() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, s := range p.seen {
		out = append(out, fmt.Sprint(get(s.body, "model")))
	}
	return out
}

func TestProviderAPIBothProvidersAllFourAPIsUseHostKeyAndMeasuredUsage(t *testing.T) {
	p := newProviderAPI(t)
	for _, provider := range []string{"openai", "xai"} {
		for _, protocol := range []string{"chat", "messages", "responses", "gemini"} {
			for _, streaming := range []bool{false, true} {
				t.Run(fmt.Sprint(provider, "/", protocol, "/", streaming), func(t *testing.T) {
					p.reply(false, streaming, true)
					model := provider + "/first"
					var body host.Obj
					if protocol == "gemini" {
						method := ":generateContent"
						if streaming {
							method = ":streamGenerateContent?alt=sse"
						}
						p.url = p.root + "/v1beta/models/" + url.PathEscape(model) + method
						body = host.Obj{{Key: "contents", Value: []any{host.Obj{{Key: "role", Value: "user"},
							{Key: "parts", Value: []any{host.Obj{{Key: "text", Value: "question"}}}}}}},
							{Key: "generationConfig", Value: host.Obj{{Key: "maxOutputTokens", Value: 256}}}}
					} else {
						body = p.request(protocol, model, streaming)
					}
					reply := p.postObj(body, "x-goog-api-key", "caller-google-secret")
					if reply.status != 200 || reply.err != nil {
						t.Fatalf("%d %v %s", reply.status, reply.err, reply.body)
					}
					text := reply.body
					if !streaming {
						text = pyDumpsUnicode(parsed(t, reply.body))
					}
					if !bytes.Contains(text, []byte("안녕")) || bytes.Contains(reply.body, []byte("host-secret")) {
						t.Fatalf("%s", reply.body)
					}
					sent := p.last()
					if sent.path != "/v1/chat/completions" || sent.header.Get("authorization") != "Bearer host-secret" {
						t.Fatal(sent.path, sent.header)
					}
					if strings.Contains(host.MustDumps(sent.header, host.DumpOpts{}), "caller-") || reply.header.Get("x-private") != "" {
						t.Fatal("caller or provider headers crossed")
					}
					if get(sent.body, "model") != "first" || fmt.Sprint(get(sent.body, "max_completion_tokens")) != "256" ||
						has(sent.body.(host.Obj), "max_tokens") {
						t.Fatalf("%s", sent.raw)
					}
					if streaming {
						if !strings.Contains(reply.header.Get("content-type"), "text/event-stream") {
							t.Fatal(reply.header)
						}
						return
					}
					result := parsed(t, reply.body)
					var tokens any
					switch protocol {
					case "chat":
						tokens = at(result, "usage", "prompt_tokens")
					case "gemini":
						tokens = at(result, "usageMetadata", "promptTokenCount")
					default:
						tokens = at(result, "usage", "input_tokens")
					}
					if fmt.Sprint(tokens) != "4" {
						t.Fatalf("%s", reply.body)
					}
				})
			}
		}
	}
}

func TestProviderAPIToolResultIdsAndStringsSurviveEachPublicAPI(t *testing.T) {
	p := newProviderAPI(t)
	for _, provider := range []string{"openai", "xai"} {
		for _, protocol := range []string{"chat", "messages", "responses"} {
			t.Run(provider+"/"+protocol, func(t *testing.T) {
				p.reply(true, false, true)
				request := p.request(protocol, provider+"/first", false)
				reply := p.postObj(request)
				if reply.status != 200 {
					t.Fatalf("%d %s", reply.status, reply.body)
				}
				next := providerFollow(protocol, request, parsed(t, reply.body))
				p.reply(false, false, true)
				if reply = p.postObj(next); reply.status != 200 {
					t.Fatalf("%d %s", reply.status, reply.body)
				}
				var result any
				for _, m := range get(p.last().body, "messages").([]any) {
					if get(m, "role") == "tool" && result == nil {
						result = m
					}
				}
				if get(result, "tool_call_id") != "call_echo" || get(result, "content") != `{"result":"결과"}` {
					t.Fatalf("%s", p.last().raw)
				}
			})
		}
	}
}

func TestProviderAPIGroupFailoverOnlyOnConfirmedRejection(t *testing.T) {
	p := newProviderAPI(t)
	p.decide = func(task, group string) (map[string]any, error) {
		return map[string]any{"group": "group/test", "id": "openai/first", "fallbacks": []any{"xai/backup"},
			"decision": "fixed", "classifier_ms": int64(2)}, nil
	}
	backup, _ := Parse(message("안녕하세요"))
	answer, _ := CodecResponse(backup, "backup")
	p.replies["backup"] = upstreamReply{200, "application/json", mustPy(answer)}
	for _, protocol := range []string{"chat", "messages", "responses"} {
		for _, status := range []int{429, 500} {
			t.Run(fmt.Sprint(protocol, status), func(t *testing.T) {
				p.clear()
				p.replies["first"] = upstreamReply{status, "application/json", []byte(`{"error":"host-secret private"}`)}
				reply := p.postObj(p.request(protocol, "group/test", false))
				want, models := 500, []string{"first"}
				if status == 429 {
					want, models = 200, []string{"first", "backup"}
				}
				if reply.status != want || !slices.Equal(p.sentModels(), models) || bytes.Contains(reply.body, []byte("host-secret")) {
					t.Fatalf("%d %s %v", reply.status, reply.body, p.sentModels())
				}
				if p.reportCount() != len(models) {
					t.Fatal(p.reportCount())
				}
			})
		}
	}
}

func TestProviderAPIStartedTruncationNeverReplaysOrSucceeds(t *testing.T) {
	p := newProviderAPI(t)
	p.decide = func(task, group string) (map[string]any, error) {
		return map[string]any{"group": "group/test", "id": "openai/first", "fallbacks": []any{"xai/backup"},
			"decision": "fixed", "classifier_ms": int64(2)}, nil
	}
	for _, protocol := range []string{"chat", "messages", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			p.reply(false, true, false)
			p.clear()
			reply := p.postObj(p.request(protocol, "group/test", true))
			if reply.status != 200 || !bytes.Contains(reply.body, []byte("error")) {
				t.Fatalf("%d %v %s", reply.status, reply.err, reply.body)
			}
			for _, finish := range []string{"[DONE]", "message_stop", "response.completed"} {
				if bytes.Contains(reply.body, []byte(finish)) {
					t.Fatalf("%s", reply.body)
				}
			}
			if p.seenCount() != 1 {
				t.Fatal(p.seenCount())
			}
		})
	}
}

func TestProviderAPIBadHostConfigAndCountNeverGenerateOrUseSubscription(t *testing.T) {
	p := newProviderAPI(t)
	for _, provider := range []string{"openai", "xai"} {
		body := p.request("messages", provider+"/first", false)
		p.url += "/count_tokens"
		if reply := p.postObj(body); reply.status != 501 {
			t.Fatalf("%s count %d %s", provider, reply.status, reply.body)
		}
		writeFile(t, host.KeysFile, `{"keys":[]}`)
		reply := p.postObj(p.request("chat", provider+"/first", false))
		if reply.status != 500 || !bytes.Contains(reply.body, []byte("upstream_error")) {
			t.Fatalf("%s %d %s", provider, reply.status, reply.body)
		}
		writeFile(t, host.KeysFile, fmt.Sprintf(providerKeys, p.base))
	}
	if p.seenCount() != 0 {
		t.Fatal(p.seenCount())
	}
}
