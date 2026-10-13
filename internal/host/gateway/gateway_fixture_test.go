package gateway

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// The shared gateway fixture (test_arkism_gateway_chat.ChatGatewayTest): a recording upstream
// that answers by model, the gateway on real TCP, and fakes for the crew helpers.

// swap replaces *p for the test, like mock.patch.
func swap[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

type seenRequest struct {
	method, path string
	header       http.Header
	raw          []byte
	body         any
}

type upstreamReply struct {
	status int
	ctype  string
	data   []byte
}

type fixture struct {
	t           *testing.T
	mu          sync.Mutex
	seen        []seenRequest
	replies     map[string]upstreamReply
	fallback    *upstreamReply // the answer for any other model; nil means message("안녕하세요")
	lengthExtra int
	up, gw      *httptest.Server
	base, url   string
	reports     []map[string]any
	decisions   int
	decide      func(task, group string) (map[string]any, error)
	authCalls   []string
	auth        func(model string) (map[string]string, string, error)
	state       string // a temporary host state folder
}

func message(text string) string {
	return string(mustPy(host.Obj{{Key: "id", Value: "msg_test"}, {Key: "type", Value: "message"}, {Key: "role", Value: "assistant"},
		{Key: "model", Value: "upstream"}, {Key: "content", Value: []any{host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: text}}}},
		{Key: "stop_reason", Value: "end_turn"}, {Key: "usage", Value: host.Obj{{Key: "input_tokens", Value: 4}, {Key: "output_tokens", Value: 2}}}}))
}

// sse renders Messages events as named SSE frames.
func sse(events ...string) []byte {
	var b bytes.Buffer
	for _, e := range events {
		v, _ := Parse(e)
		b.WriteString("event: " + get(v, "type").(string) + "\ndata: " + string(pyDumpsUnicode(v)) + "\n\n")
	}
	return b.Bytes()
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, replies: map[string]upstreamReply{}, state: t.TempDir()}
	f.up = httptest.NewServer(http.HandlerFunc(f.upstream))
	t.Cleanup(f.up.Close)
	f.base = f.up.URL
	f.decide = func(task, group string) (map[string]any, error) {
		return map[string]any{"group": "group/test", "id": "custom-test/first", "fallbacks": []any{"custom-test/backup"},
			"decision": "fixed", "classifier_ms": int64(2)}, nil
	}
	f.auth = func(string) (map[string]string, string, error) {
		return map[string]string{"content-type": "application/json", "x-api-key": "host-secret"}, "host-secret", nil
	}
	isolate(t, f.state)
	swap(t, &Anthropic, f.base)
	swap(t, &host.ReportCall, func(call map[string]any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reports = append(f.reports, call)
	})
	swap(t, &host.DecideRoute, func(task, group string) (map[string]any, error) {
		f.mu.Lock()
		f.decisions++
		decide := f.decide
		f.mu.Unlock()
		return decide(task, group)
	})
	swap(t, &target, func(model string) (*where, error) {
		if strings.HasPrefix(model, "anthropic/") {
			return &where{kind: "anthropic"}, nil
		}
		_, name, found := strings.Cut(model, "/")
		if !found {
			name = model
		}
		return &where{kind: "direct", base: f.base, key: "host-secret", name: name}, nil
	})
	swap(t, &claudeHeaders, func(model string) (map[string]string, string, error) {
		f.mu.Lock()
		f.authCalls = append(f.authCalls, model)
		auth := f.auth
		f.mu.Unlock()
		return auth(model)
	})
	f.gw = httptest.NewServer(Handler{})
	t.Cleanup(f.gw.Close)
	f.url = f.gw.URL + "/v1/chat/completions"
	return f
}

// isolate points every host file at an empty temporary folder and clears the model cache.
func isolate(t *testing.T, dir string) {
	swap(t, &host.TokenFile, filepath.Join(dir, "owner.token"))
	swap(t, &host.RunnerTokenFile, filepath.Join(dir, "runner.token"))
	swap(t, &host.KeysFile, filepath.Join(dir, "keys.json"))
	swap(t, &host.APIMode, map[string]bool{})
	swap(t, &ZAIEnv, filepath.Join(dir, "zai.env"))
	resetLimits(t)
	models.Lock()
	models.at, models.items = time.Time{}, nil
	models.Unlock()
	t.Cleanup(func() {
		models.Lock()
		models.at, models.items = time.Time{}, nil
		models.Unlock()
	})
}

func (f *fixture) upstream(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	body, _ := Parse(string(raw))
	f.mu.Lock()
	f.seen = append(f.seen, seenRequest{r.Method, r.URL.RequestURI(), r.Header.Clone(), raw, body})
	model, _ := get(body, "model").(string)
	reply, ok := f.replies[model]
	extra := f.lengthExtra
	if !ok && f.fallback != nil {
		reply, ok = *f.fallback, true
	}
	f.mu.Unlock()
	if !ok {
		reply = upstreamReply{200, "application/json", []byte(message("안녕하세요"))}
	}
	w.Header().Set("content-type", reply.ctype)
	w.Header().Set("content-length", strconv.Itoa(len(reply.data)+extra))
	w.Header().Set("x-private", "host-secret")
	w.WriteHeader(reply.status)
	for offset := 0; offset < len(reply.data); offset += 7 {
		w.Write(reply.data[offset:min(offset+7, len(reply.data))])
		w.(http.Flusher).Flush()
	}
}

func (f *fixture) seenCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.seen)
}

func (f *fixture) reportCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reports)
}

func (f *fixture) clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen, f.reports, f.decisions, f.authCalls = nil, nil, 0, nil
	resetLimitState() // a case's 429 must not rest a model for the next case
}

type response struct {
	status int
	header http.Header
	body   []byte
	err    error // a body that ended early (the connection was dropped)
}

// post sends a Chat-style request with caller credentials that must never reach a provider.
func (f *fixture) post(body string, headers ...string) response {
	if body == "" {
		body = `{"model":"custom-test/first","messages":[{"role":"user","content":"안녕"}]}`
	}
	all := append([]string{"content-type", "application/json", "authorization", "Bearer caller-secret",
		"x-api-key", "caller-key", "x-extra", "caller-private"}, headers...)
	return send(f.t, "POST", f.url, []byte(body), all...)
}

// send writes one raw HTTP/1.1 request, so odd targets and declared lengths reach the server as
// written; headers are name/value pairs, and a later pair replaces an earlier one.
func send(t *testing.T, method, rawURL string, body []byte, headers ...string) response {
	t.Helper()
	rest := strings.TrimPrefix(rawURL, "http://")
	address, target, _ := strings.Cut(rest, "/")
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	values := map[string]string{}
	var order []string
	for i := 0; i+1 < len(headers); i += 2 {
		key := strings.ToLower(headers[i])
		if _, ok := values[key]; !ok {
			order = append(order, key)
		}
		values[key] = headers[i+1]
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s /%s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n", method, target, address)
	if _, ok := values["content-length"]; !ok && (body != nil || method == "POST") {
		order, values["content-length"] = append(order, "content-length"), strconv.Itoa(len(body))
	}
	for _, key := range order {
		fmt.Fprintf(&b, "%s: %s\r\n", key, values[key])
	}
	b.WriteString("\r\n")
	b.Write(body)
	if _, err := conn.Write(b.Bytes()); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return response{err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return response{resp.StatusCode, resp.Header, data, err}
}

func fetch(t *testing.T, rawURL string, headers ...string) response {
	return send(t, "GET", rawURL, nil, headers...)
}

// parsed decodes a JSON body, failing the test when it is not JSON.
func parsed(t *testing.T, raw []byte) any {
	t.Helper()
	v, err := Parse(string(raw))
	if err != nil {
		t.Fatalf("not JSON: %q", raw)
	}
	return v
}

// at walks a decoded JSON value by keys and indexes.
func at(v any, path ...any) any {
	for _, step := range path {
		switch s := step.(type) {
		case string:
			v = get(v, s)
		case int:
			list, ok := v.([]any)
			if !ok || s >= len(list) {
				return nil
			}
			v = list[s]
		}
	}
	return v
}

func errorMessage(t *testing.T, raw []byte) any {
	return at(parsed(t, raw), "error", "message")
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}
