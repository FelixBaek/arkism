package gateway

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// Cases the goldens cannot hold: every split point, 1 MiB limits, deep nesting and callback
// timing.

func chunks(parts ...[]byte) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		for _, p := range parts {
			if !yield(p, nil) {
				return
			}
		}
	}
}

func mustParse(t *testing.T, text string) any {
	t.Helper()
	v, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var c Code
	if !errors.As(err, &c) || string(c) != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}

func collect(run func(emit func([]byte) error) error) (string, error) {
	var b strings.Builder
	err := run(func(frame []byte) error {
		b.Write(frame)
		return nil
	})
	return b.String(), err
}

func TestEveryByteSplitMatchesWholeStream(t *testing.T) {
	raw, err := os.ReadFile("testdata/chat_codec.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	var data []byte
	for _, c := range cases {
		if strings.HasSuffix(c.Test, "test_every_byte_split_crlf_and_frame_split") && c.Fn == "chat_codec.stream" {
			data, _ = base64.StdEncoding.DecodeString(c.Data)
			break
		}
	}
	if len(data) == 0 {
		t.Fatal("no split case in goldens")
	}
	whole, err := collect(func(emit func([]byte) error) error { return CodecStream(chunks(data), "m", true, emit) })
	if err != nil || !strings.HasSuffix(whole, "data: [DONE]\n\n") {
		t.Fatalf("whole stream: %v %q", err, whole)
	}
	for i := range data {
		got, err := collect(func(emit func([]byte) error) error {
			return CodecStream(chunks(data[:i], data[i:]), "m", true, emit)
		})
		if err != nil || got != whole {
			t.Fatalf("split at %d: %v", i, err)
		}
	}
}

func nested(depth int, leaf string) string {
	return strings.Repeat(`{"x":`, depth) + leaf + strings.Repeat("}", depth)
}

func TestExcessiveNestingHasFixedError(t *testing.T) {
	deep := nested(600, `"leaf"`)
	for _, field := range []string{"parameters", "parametersJsonSchema"} {
		body := mustParse(t, `{"contents":[{"parts":[{"text":"x"}]}],"tools":[{"functionDeclarations":[{"name":"a","`+field+`":{"default":`+deep+`}}]}]}`)
		_, err := GeminiToChat(body, "m", false)
		wantCode(t, err, "invalid_request")
	}
	_, err := ResponsesToChat(mustParse(t, `{"model":"m","store":false,"input":`+strings.Repeat("[", 1100)+strings.Repeat("]", 1100)+`}`))
	wantCode(t, err, "invalid_request")
	_, err = MessagesToChat(mustParse(t, `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}],"tools":[{"name":"f","input_schema":`+deep+`}]}`))
	wantCode(t, err, "invalid_request")
	_, err = CodecToMessages(mustParse(t, `{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"f","parameters":`+deep+`}}]}`))
	wantCode(t, err, "invalid_request")
}

func TestToolArgumentByteLimits(t *testing.T) {
	request := func(args string) any {
		return mustParse(t, `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"f","args":`+args+`}}]},`+
			`{"role":"user","parts":[{"functionResponse":{"name":"f","response":{}}}]}],"tools":[{"functionDeclarations":[{"name":"f"}]}]}`)
	}
	_, err := GeminiToChat(request(`{"x":"`+strings.Repeat("한", MaxToolArgumentBytes/3)+`"}`), "m", false)
	wantCode(t, err, "tool_arguments_too_large")
	if _, err := GeminiToChat(request(`{"x":"`+strings.Repeat("x", MaxToolArgumentBytes-len(`{"x":""}`))+`"}`), "m", false); err != nil {
		t.Fatal(err)
	}

	big := strings.Repeat("x", MaxToolArgumentBytes)
	page := mustParse(t, `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"f","args":{"x":"`+big+`"}}}]},"finishReason":"STOP"}],`+
		`"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	_, err = GoogleResponse(pageSeq(page), "m", func(host.Obj, []any) {})
	wantCode(t, err, "tool_arguments_too_large")

	_, err = collect(func(emit func([]byte) error) error {
		return CodecStream(chunks([]byte("data: "+strings.Repeat("x", MaxFrameBytes)+"\n\n")), "m", false, emit)
	})
	wantCode(t, err, "stream_frame_too_large")
}

func nativeChunk(delta string, finish string) string {
	return `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"u","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `}]}` + "\n\n"
}

func TestNativeToolIndexAndArgumentLimits(t *testing.T) {
	call := func(index int, args string) string {
		return `{"tool_calls":[{"index":` + strconv.Itoa(index) + `,"id":"a","type":"function","function":{"name":"f","arguments":"` + args + `"}}]}`
	}
	_, err := collect(func(emit func([]byte) error) error {
		return NativeStream(chunks([]byte(nativeChunk(call(MaxToolCalls, "{}"), "null"))), "m", false, emit)
	})
	wantCode(t, err, "invalid_stream")

	half := strings.Repeat("x", MaxToolArgumentBytes/2+1)
	next := `{"tool_calls":[{"index":0,"function":{"arguments":"` + half + `"}}]}`
	_, err = collect(func(emit func([]byte) error) error {
		return MessagesStream(chunks([]byte(nativeChunk(call(0, ""), "null")), []byte(nativeChunk(next, "null")),
			[]byte(nativeChunk(next, "null"))), "m", emit)
	})
	wantCode(t, err, "tool_arguments_too_large")
}

func pageSeq(pages ...any) iter.Seq2[any, error] {
	return func(yield func(any, error) bool) {
		for _, p := range pages {
			if !yield(p, nil) {
				return
			}
		}
	}
}

func TestGoogleCallbackRunsOnceAfterEOFBeforeToolFrames(t *testing.T) {
	page := mustParse(t, `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"},{"functionCall":{"name":"f","args":{}}}]},"finishReason":"STOP"}],`+
		`"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	var log []string
	pages := func(yield func(any, error) bool) {
		log = append(log, "page")
		if yield(page, nil) {
			log = append(log, "eof")
		}
	}
	err := GoogleStream(pages, "m", func(message host.Obj, parts []any) {
		log = append(log, "callback")
	}, func(frame []byte) error {
		if strings.Contains(string(frame), `"delta":{"tool_calls"`) {
			log = append(log, "tool")
		} else {
			log = append(log, "frame")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(log, ","); got != "page,frame,frame,eof,callback,tool,frame,frame,frame" {
		t.Fatalf("order %s", got)
	}

	called := false
	broken := func(yield func(any, error) bool) {
		if yield(page, nil) {
			yield(nil, errors.New("PRIVATE"))
		}
	}
	_, err = GoogleResponse(broken, "m", func(host.Obj, []any) { called = true })
	wantCode(t, err, "upstream_stream_error")
	if called {
		t.Fatal("callback ran for a failed upstream")
	}
	_, err = GoogleResponse(pageSeq(page), "m", nil)
	wantCode(t, err, "invalid_option")

	MaxToolCalls = 1
	defer func() { MaxToolCalls = 1024 }()
	two := mustParse(t, `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"f","args":{}}},{"functionCall":{"name":"f","args":{}}}]},"finishReason":"STOP"}],`+
		`"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	_, err = GoogleResponse(pageSeq(two), "m", func(host.Obj, []any) {})
	wantCode(t, err, "invalid_response")
}

func TestEmitErrorStopsStream(t *testing.T) {
	stop := errors.New("client gone")
	data := []byte(nativeChunk(`{"role":"assistant","content":"a"}`, "null"))
	err := NativeStream(chunks(data), "m", false, func([]byte) error { return stop })
	if err != stop {
		t.Fatalf("got %v", err)
	}
}
