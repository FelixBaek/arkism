package gateway

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// Goldens are recorded from the Python translators (inputs captured while their unit tests run,
// then replayed with fixed uuid4 and time). Each case logs the consume/emit order:
// ["in", n] n upstream chunks or pages pulled, ["out", text], ["cb", message, parts], ["err", code].

type goldenCase struct {
	Test     string
	Fn       string
	Limits   map[string]int
	Args     []string
	Data     string
	Sizes    []int
	Pages    []string
	Raise    bool
	Callback bool
	Events   []json.RawMessage
}

type call struct {
	t      *testing.T
	args   []any
	c      goldenCase
	events [][]any
}

var errPrivate = errors.New("PRIVATE")

func (c *call) in() {
	if n := len(c.events); n > 0 && c.events[n-1][0] == "in" {
		c.events[n-1][1] = c.events[n-1][1].(int) + 1
		return
	}
	c.events = append(c.events, []any{"in", 1})
}

func (c *call) out(text string) { c.events = append(c.events, []any{"out", text}) }

// result logs a non-streaming result as json.dumps(result, ensure_ascii=False).
func (c *call) result(v any, err error) {
	if err != nil {
		c.fail(err)
		return
	}
	c.out(host.MustDumps(v, host.DumpOpts{NoASCII: true}))
}

func (c *call) fail(err error) {
	var code Code
	if !errors.As(err, &code) {
		c.t.Fatalf("non-code error: %v", err)
	}
	c.events = append(c.events, []any{"err", string(code)})
}

func (c *call) chunks() iter.Seq2[[]byte, error] {
	data, err := base64.StdEncoding.DecodeString(c.c.Data)
	if err != nil {
		c.t.Fatal(err)
	}
	return func(yield func([]byte, error) bool) {
		for _, size := range c.c.Sizes {
			c.in()
			chunk := data[:size:size]
			data = data[size:]
			if !yield(chunk, nil) {
				return
			}
		}
		if c.c.Raise {
			yield(nil, errPrivate)
		}
	}
}

func (c *call) pages() iter.Seq2[any, error] {
	return func(yield func(any, error) bool) {
		for _, text := range c.c.Pages {
			c.in()
			page, err := Parse(text)
			if err != nil {
				c.t.Fatal(err)
			}
			if !yield(page, nil) {
				return
			}
		}
		if c.c.Raise {
			yield(nil, errPrivate)
		}
	}
}

func (c *call) callback() func(host.Obj, []any) {
	if !c.c.Callback {
		return nil
	}
	return func(message host.Obj, parts []any) {
		c.events = append(c.events, []any{"cb", host.MustDumps(message, host.DumpOpts{NoASCII: true}),
			host.MustDumps(parts, host.DumpOpts{NoASCII: true})})
	}
}

func (c *call) emit(frame []byte) error {
	c.out(string(frame))
	return nil
}

// errSkip marks an argument Go's static types cannot express (a non-string model and so on).
var errSkip = errors.New("type not expressible in Go")

func (c *call) str(i int) string {
	s, ok := c.args[i].(string)
	if !ok {
		panic(errSkip)
	}
	return s
}

func (c *call) obj(i int) host.Obj {
	o, ok := c.args[i].(host.Obj)
	if !ok {
		panic(errSkip)
	}
	return o
}

func (c *call) boolean(i int) bool {
	b, ok := c.args[i].(bool)
	if !ok {
		panic(errSkip)
	}
	return b
}

func (c *call) integer(i int) int {
	n, ok := host.Int(c.args[i])
	if !ok {
		panic(errSkip)
	}
	return int(n)
}

var goldenFuncs = map[string]func(c *call){
	"chat_codec.to_messages": func(c *call) { c.result(CodecToMessages(c.args[0])) },
	"chat_codec.response":    func(c *call) { c.result(CodecResponse(c.args[0], c.str(1))) },
	"chat_codec.error":       func(c *call) { c.result(CodecError(c.str(0), c.integer(1))) },
	"chat_codec.stream": func(c *call) {
		if err := CodecStream(c.chunks(), c.str(0), c.boolean(1), c.emit); err != nil {
			c.fail(err)
		}
	},
	"chat_native.response": func(c *call) { c.result(NativeResponse(c.args[0], c.str(1))) },
	"chat_native.stream": func(c *call) {
		if err := NativeStream(c.chunks(), c.str(0), c.boolean(1), c.emit); err != nil {
			c.fail(err)
		}
	},
	"messages_request.to_chat":   func(c *call) { c.result(MessagesToChat(c.args[0])) },
	"messages_response.response": func(c *call) { c.result(MessagesResponse(c.args[0], c.str(1))) },
	"messages_response.stream": func(c *call) {
		if err := MessagesStream(c.chunks(), c.str(0), c.emit); err != nil {
			c.fail(err)
		}
	},
	"responses_request.to_chat":   func(c *call) { c.result(ResponsesToChat(c.args[0])) },
	"responses_response.response": func(c *call) { c.result(ResponsesResponse(c.args[0], c.obj(1))) },
	"responses_response.stream": func(c *call) {
		if err := ResponsesStream(c.chunks(), c.obj(0), c.emit); err != nil {
			c.fail(err)
		}
	},
	"gemini_request.to_chat":   func(c *call) { c.result(GeminiToChat(c.args[0], c.str(1), c.boolean(2))) },
	"gemini_response.response": func(c *call) { c.result(GeminiResponse(c.args[0], c.str(1))) },
	"gemini_response.stream": func(c *call) {
		if err := GeminiStream(c.chunks(), c.str(0), c.emit); err != nil {
			c.fail(err)
		}
	},
	"google_response.response": func(c *call) { c.result(GoogleResponse(c.pages(), c.str(0), c.callback())) },
	"google_response.stream": func(c *call) {
		if err := GoogleStream(c.pages(), c.str(0), c.callback(), c.emit); err != nil {
			c.fail(err)
		}
	},
	"chat_codec.sse_frames": func(c *call) {
		err := SSEFrames(c.chunks(), func(event, data string) error {
			var name any = event
			if event == "" {
				name = nil // Python yields None for a frame without an event line
			}
			c.out(host.MustDumps([]any{name, data}, host.DumpOpts{NoASCII: true}))
			return nil
		})
		if err != nil {
			c.fail(err)
		}
	},
}

var goldenLimits = map[string]*int{
	"MAX_FRAME_BYTES":         &MaxFrameBytes,
	"MAX_TOOL_ARGUMENT_BYTES": &MaxToolArgumentBytes,
	"MAX_TOOL_CALLS":          &MaxToolCalls,
	"MAX_OUTPUT":              &MaxOutput,
}

func TestGoldens(t *testing.T) {
	files, _ := filepath.Glob("testdata/*.json")
	if len(files) == 0 {
		t.Fatal("no goldens")
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var cases []goldenCase
		if err := json.Unmarshal(raw, &cases); err != nil {
			t.Fatal(err)
		}
		module := strings.TrimSuffix(filepath.Base(file), ".json")
		t.Run(module, func(t *testing.T) {
			runGoldens(t, cases)
		})
	}
}

func runGoldens(t *testing.T, cases []goldenCase) {
	missing, skipped := map[string]int{}, map[string]int{}
	for i, gc := range cases {
		fn, ok := goldenFuncs[gc.Fn]
		if !ok {
			missing[gc.Fn]++
			continue
		}
		name := fmt.Sprintf("%s/%d", strings.TrimPrefix(gc.Test, gc.Fn[:strings.IndexByte(gc.Fn, '.')]+"."), i)
		ok = t.Run(name, func(t *testing.T) {
			c := &call{t: t, c: gc}
			for _, text := range gc.Args {
				v, err := Parse(text)
				if err != nil {
					t.Fatalf("arg %q: %v", text, err)
				}
				c.args = append(c.args, v)
			}
			before := make([]string, len(c.args))
			for i, arg := range c.args {
				before[i] = fmt.Sprintf("%#v", arg)
			}
			restore := setup(gc)
			defer restore()
			skip := func() (skip bool) {
				defer func() {
					if r := recover(); r != nil {
						if r != errSkip {
							panic(r)
						}
						skip = true
					}
				}()
				fn(c)
				return false
			}()
			if skip {
				skipped[gc.Test]++
				return
			}
			got := make([]string, len(c.events))
			for i, event := range c.events {
				got[i] = host.MustDumps(event, host.DumpOpts{})
			}
			want := make([]string, len(gc.Events))
			for i, event := range gc.Events {
				var v []any
				if err := json.Unmarshal(event, &v); err != nil {
					t.Fatal(err)
				}
				if n, ok := v[1].(float64); ok {
					v[1] = int(n)
				}
				want[i] = host.MustDumps(v, host.DumpOpts{})
			}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("args %v\n got: %s\nwant: %s", gc.Args, strings.Join(got, "\n      "), strings.Join(want, "\n      "))
			}
			for i, arg := range c.args {
				if fmt.Sprintf("%#v", arg) != before[i] {
					t.Fatalf("argument %d was modified", i)
				}
			}
		})
		if !ok && testing.Verbose() {
			continue
		}
	}
	if len(missing) > 0 {
		t.Errorf("functions without a Go port: %v", missing)
	}
	if len(skipped) > 0 {
		t.Logf("skipped (argument types Go cannot pass): %v", skipped)
	}
}

// setup fixes time, ids and the case's limits; only limits of the called function's module
// apply, as each Python module bound its own copy.
func setup(gc goldenCase) func() {
	savedNow, savedHex := now, newHex
	saved := map[*int]int{}
	for p, v := range goldenLimitValues() {
		saved[p] = v
	}
	module := gc.Fn[:strings.IndexByte(gc.Fn, '.')]
	for key, value := range gc.Limits {
		m, name, _ := strings.Cut(key, ".")
		if p, ok := goldenLimits[name]; ok && m == module {
			*p = value
		}
	}
	counter := 0
	now = func() time.Time { return time.Unix(1700000000, 750_000_000) }
	newHex = func() string {
		counter++
		return fmt.Sprintf("%032x", counter)
	}
	return func() {
		now, newHex = savedNow, savedHex
		for p, v := range saved {
			*p = v
		}
	}
}

func goldenLimitValues() map[*int]int {
	out := map[*int]int{}
	for _, p := range goldenLimits {
		out[p] = *p
	}
	return out
}
