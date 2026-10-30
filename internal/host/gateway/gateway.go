package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"iter"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/desktopcontrol"
	"github.com/FelixBaek/arkism/internal/host/subscription"
	"github.com/FelixBaek/arkism/internal/httpguard"
)

// The model gateway. Claude Code points ANTHROPIC_BASE_URL here; its
// model picker lists the models enabled in Arkism 라우팅 (GET /v1/models), and each request goes
// where its model lives:
//   - bare Claude models on Messages pass through with the caller's own auth; anthropic/<model>
//     and groups use the host API key or the Claude subscription CLI;
//   - zai/<glm>: z.ai's Anthropic endpoint with the host's z.ai key;
//   - custom-<id>/<model>: the provider's Anthropic endpoint when it has one, otherwise its
//     OpenAI Chat API through the Messages -> Chat translation;
//   - openai/<model>: the host Codex login, or the stored key in explicit codex API mode; xai
//     uses its stored key in grok API mode; google/<model> uses the native host adapter;
//   - group/<slug>: Arkism's scoped decision and up to three fallbacks, failing over on a
//     rejection, a 5xx (529 included) or a transport failure, only before any client response
//     starts. When no member answers the caller gets the pick's own error. Each attempt is
//     reported without request or response content.
//   - every provider/model request (a group's members included) keeps to the model's 라우팅 설정
//     concurrency and rpm_limit, and quota, credit or 429 failures rest a model (lanes.go).
// Chat, Responses and Gemini entrances use the same engine through pure codecs; caller headers
// never cross that boundary.

func init() { host.Register("gateway", runGateway) }

// Settings (environment, like the Python module constants).
var (
	Listen = host.Env("ARKISM_GATEWAY_LISTEN", host.DefaultGatewayListen)
	// Anthropic is where bare Claude requests pass through.
	Anthropic = host.Env("ARKISM_GATEWAY_ANTHROPIC", "https://api.anthropic.com")
	ZAIURL    = host.Env("ARKISM_GATEWAY_ZAI", "https://api.z.ai/api/anthropic")
	// ZAIEnv holds KEY=<z.ai key>.
	ZAIEnv = envOr("ARKISM_GATEWAY_ZAI_ENV", filepath.Join(host.AgentsState, "zai.env"))
)

// envOr is os.getenv(name) or def: an empty value falls back.
func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

var (
	hop = set("host", "connection", "keep-alive", "transfer-encoding", "te", "trailer", "upgrade",
		"proxy-authorization", "content-length", host.GatewayTokenHeader)
	// Body fields a non-Anthropic model understands; Claude Code's beta-only fields are dropped.
	portableKeys  = []string{"model", "max_tokens", "messages", "system", "tools", "tool_choice", "stream", "temperature", "top_p", "stop_sequences"}
	retryStatuses = map[int]bool{401: true, 402: true, 403: true, 429: true, 503: true}
	// upstreamWait bounds one provider call, headers and body; a test shortens it.
	upstreamWait = 10 * time.Minute
	// Our own checks refused these before any egress: the candidate was skipped, not called (a
	// login, capacity or provider rejection stays a real attempt signal).
	noCall     = set("unsupported_field", "unsupported_request", "unsupported_or_invalid_request")
	groupID    = regexp.MustCompile(`^group/[a-z0-9-]{1,64}$`)
	modelID    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}/[A-Za-z0-9][A-Za-z0-9._:/ -]{0,79}$`)
	betaPart   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)
	geminiURL  = regexp.MustCompile(`^/v1beta/models/(.+):(generateContent|streamGenerateContent|countTokens)$`)
	autoGroup  = "group/auto"
	bareOpenAI = regexp.MustCompile(`^(gpt-|o[0-9]|codex-)[A-Za-z0-9._-]{0,60}$`)
	zaiKeyRe   = regexp.MustCompile(`(?m)^KEY=['"]?([^'"\s]+)`)
	sizeRe     = regexp.MustCompile(`^[0-9]{1,6}$`)
	offsetRe   = regexp.MustCompile(`^[0-9]{1,9}$`)
	entrances  = set("/v1/messages", "/v1/messages/count_tokens", "/v1/chat/completions", "/v1/responses")
)

// direct never uses an environment proxy, never follows a redirect (a 3xx reaches the caller's
// status mapping) and never asks for compression on its own.
var direct = &http.Client{
	Transport: &http.Transport{Proxy: nil, DisableCompression: true,
		DialContext: (&net.Dialer{Timeout: 600 * time.Second}).DialContext, ResponseHeaderTimeout: 600 * time.Second},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Test seams for the crew and provider helpers tests replace.
var (
	claudeHeaders     = subscription.ClaudeHeaders
	chatConnection    = subscription.ChatConnection
	codexIterMessages = subscription.IterMessages
	codexProfile      = func(model string, exclude []string) map[string]any {
		if host.APIModeOn("codex") {
			return nil
		}
		profile, _ := subscription.Choose("codex", model, host.Bin("ARKISM_CODEX_BIN", "codex"), host.Home, host.AgentsState, exclude)
		return profile
	}
	recordLimit   = subscription.RecordLimit
	target        = resolve
	routingModels = listModels
)

type byteSeq = iter.Seq2[[]byte, error]

func once(b []byte) byteSeq { return func(yield func([]byte, error) bool) { yield(b, nil) } }

var errStop = errors.New("stop")

// pipe turns a push-style translator into a lazy chunk sequence; its error ends the sequence.
func pipe(run func(emit func([]byte) error) error) byteSeq {
	return func(yield func([]byte, error) bool) {
		stopped := false
		err := run(func(b []byte) error {
			if stopped || !yield(b, nil) {
				stopped = true
				return errStop
			}
			return nil
		})
		if err != nil && !stopped && !errors.Is(err, errStop) {
			yield(nil, err)
		}
	}
}

// gather joins a whole sequence.
func gather(data byteSeq) ([]byte, error) {
	var buf bytes.Buffer
	for chunk, err := range data {
		if err != nil {
			return nil, err
		}
		buf.Write(chunk)
	}
	return buf.Bytes(), nil
}

// pyDumps is json.dumps(v) with Python's default separators and ensure_ascii.
func pyDumps(v any) ([]byte, error) {
	s, err := host.Dumps(v, host.DumpOpts{})
	return []byte(s), err
}

func mustPy(v any) []byte { return []byte(host.MustDumps(v, host.DumpOpts{})) }

// pyDumpsUnicode is json.dumps(v, ensure_ascii=False).
func pyDumpsUnicode(v any) []byte { return []byte(host.MustDumps(v, host.DumpOpts{NoASCII: true})) }

func with(o host.Obj, key string, value any) host.Obj {
	out := append(host.Obj(nil), o...)
	out.Set(key, value)
	return out
}

// plain turns a JSON tree into the map form the subscription package takes.
func plain(v any) any {
	switch x := v.(type) {
	case host.Obj:
		out := make(map[string]any, len(x))
		for _, kv := range x {
			out[kv.Key] = plain(kv.Value)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = plain(item)
		}
		return out
	}
	return v
}

// scopeObj is the memory scope in the Python dict's order.
func scopeObj() host.Obj {
	s := host.Scope()
	return host.Obj{{Key: "workspace_id", Value: s["workspace_id"]}, {Key: "project_id", Value: s["project_id"]},
		{Key: "user_id", Value: s["user_id"]}, {Key: "agent_id", Value: s["agent_id"]}}
}

// scopeQuery is urlencode(scope()).
func scopeQuery() string {
	var parts []string
	for _, kv := range scopeObj() {
		parts = append(parts, url.QueryEscape(kv.Key)+"="+url.QueryEscape(fmt.Sprint(kv.Value)))
	}
	return strings.Join(parts, "&")
}

func anthropicError(status int, message string) (int, []byte) {
	return status, mustPy(host.Obj{{Key: "type", Value: "error"}, {Key: "error", Value: host.Obj{
		{Key: "type", Value: "api_error"}, {Key: "message", Value: message}}}})
}

var geminiStatuses = map[int]string{400: "INVALID_ARGUMENT", 401: "UNAUTHENTICATED", 403: "PERMISSION_DENIED",
	404: "NOT_FOUND", 413: "RESOURCE_EXHAUSTED", 429: "RESOURCE_EXHAUSTED", 501: "UNIMPLEMENTED",
	503: "UNAVAILABLE", 504: "DEADLINE_EXCEEDED"}

func geminiError(status int, message string) host.Obj {
	name, ok := geminiStatuses[status]
	if !ok {
		name = "INTERNAL"
	}
	return host.Obj{{Key: "error", Value: host.Obj{{Key: "code", Value: status}, {Key: "message", Value: message},
		{Key: "status", Value: name}}}}
}

func chatError(code string, status int) []byte {
	out, _ := CodecError(code, status)
	return mustPy(out)
}

// splitURL is urlsplit(path) for a request target: (path, query).
func splitURL(target string) (string, string) {
	target, _, _ = strings.Cut(target, "#")
	path, query, _ := strings.Cut(target, "?")
	return path, query
}

// unquote is urllib.parse.unquote(errors="strict"): bad escapes stay as written, and decoded
// bytes must be UTF-8.
func unquote(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			n, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			b.WriteByte(byte(n))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	if !utf8.ValidString(b.String()) {
		return "", errors.New("invalid_request")
	}
	return b.String(), nil
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// geminiPath is gemini_path: the decoded path and single-valued query.
func geminiPath(target string) (string, map[string]string, error) {
	rawPath, rawQuery := splitURL(target)
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", nil, err
	}
	query := map[string]string{}
	for key, list := range values {
		if len(list) != 1 {
			return "", nil, errors.New("invalid_request")
		}
		query[key] = list[0]
	}
	path, err := unquote(rawPath)
	return path, query, err
}

// where is a model's destination: kind "anthropic", "google" (model), "codex" (model), "direct"
// (base, key, name) or "chat" (base, key, name).
type where struct{ kind, model, base, key, name, headers string } // headers: a custom row's "Name: Value" lines

func zaiKey() string {
	raw, err := os.ReadFile(ZAIEnv)
	if err != nil {
		return ""
	}
	if m := zaiKeyRe.FindStringSubmatch(string(raw)); m != nil {
		return m[1]
	}
	return ""
}

func customKey(pid string) (map[string]any, error) {
	keys, err := host.LoadKeys()
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if k["provider"] == "custom" && host.KeyUsable(k) && host.CustomPID(k) == pid {
			return k, nil
		}
	}
	return nil, nil
}

// resolve is target: where a model's request goes, nil when nothing can serve it.
func resolve(model string) (*where, error) {
	provider, name, found := strings.Cut(model, "/")
	// A bare OpenAI model name comes from Codex pointed here: its own login serves it.
	if !found && bareOpenAI.MatchString(model) {
		provider, name, found = "openai", model, true
	}
	if !found || name == "" || provider == "anthropic" {
		return &where{kind: "anthropic"}, nil
	}
	if provider == "google" {
		return &where{kind: "google", model: strings.ToLower(strings.Join(strings.Fields(name), "-"))}, nil
	}
	if provider == "openai" || provider == "xai" {
		base, key, err := chatConnection(provider)
		if err != nil {
			return nil, err
		}
		if base != "" {
			return &where{kind: "chat", base: base, key: key, name: name}, nil
		}
	}
	if provider == "openai" && !host.APIModeOn("codex") {
		return &where{kind: "codex", model: name}, nil
	}
	if provider == "zai" {
		if key := zaiKey(); key != "" {
			return &where{kind: "direct", base: ZAIURL, key: key, name: name}, nil
		}
	}
	if strings.HasPrefix(provider, "custom-") {
		key, err := customKey(provider)
		if err != nil || key == nil {
			return nil, err
		}
		info, err := host.CustomInfo(key, false)
		if err != nil {
			return nil, err
		}
		secret, _ := key["key"].(string)
		base, _ := key["base_url"].(string)
		headers, _ := key["headers"].(string)
		if info["api"] == "anthropic" {
			return &where{kind: "direct", base: host.AnthropicURL(base), key: secret, name: name, headers: headers}, nil
		}
		return &where{kind: "chat", base: strings.TrimRight(base, "/"), key: secret, name: name, headers: headers}, nil
	}
	return nil, nil
}

var models struct {
	sync.Mutex
	at    time.Time
	items []host.Obj
}

// fetchPage reads one 라우팅 listing; nil when it is unreachable or invalid.
func fetchPage(path, token string) []any {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host.BaseURL+path+"?"+scopeQuery(), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := direct.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil
	}
	page, err := Parse(string(raw))
	items, ok := get(page, "items").([]any)
	if err != nil || get(page, "authority") != "data_only_untrusted" || !ok {
		return nil
	}
	return items
}

// listModels is routing_models: the enabled 라우팅 models this gateway can serve, kept a minute.
func listModels() []host.Obj {
	models.Lock()
	defer models.Unlock()
	if time.Since(models.at) < time.Minute {
		return models.items
	}
	raw, err := os.ReadFile(host.TokenFile)
	if err != nil {
		return models.items
	}
	token := strings.TrimSpace(string(raw))
	items := fetchPage("/v1/routing/models", token)
	if items == nil {
		return models.items
	}
	supported := map[string]bool{}
	for _, m := range items {
		id, ok := get(m, "id").(string)
		if get(m, "enabled") == true && ok && modelID.MatchString(id) {
			if w, err := target(id); err == nil && w != nil {
				supported[id] = true
			}
		}
	}
	var keep []host.Obj
	for _, m := range items {
		if id, ok := get(m, "id").(string); ok && supported[id] {
			keep = append(keep, m.(host.Obj))
		}
	}
	for _, g := range fetchPage("/v1/routing/groups", token) {
		if entry := groupEntry(g, supported); entry != nil {
			keep = append(keep, entry)
		}
	}
	// The built-in group of every model in 모델 설정 (memory.AutoGroupID), unless a saved group took its id.
	if len(supported) > 0 && !slices.ContainsFunc(keep, func(m host.Obj) bool { return get(m, "id") == autoGroup }) {
		keep = append(keep, host.Obj{{Key: "id", Value: autoGroup}, {Key: "label", Value: "자동 · 모든 모델"}})
	}
	models.at, models.items = time.Now(), keep
	return keep
}

// groupEntry is the listing entry of an enabled, valid 라우팅 group with a supported member, else nil.
func groupEntry(g any, supported map[string]bool) host.Obj {
	o, ok := g.(host.Obj)
	id, idOK := get(g, "id").(string)
	name, present := o.Get("name")
	_, nameOK := name.(string)
	members, listOK := get(g, "members").([]any)
	if !ok || get(g, "enabled") != true || !idOK || !groupID.MatchString(id) || (present && !nameOK) || !listOK {
		return nil
	}
	for _, mid := range members {
		if s, isStr := mid.(string); isStr && supported[s] {
			label := any(id)
			if truthy(name) {
				label = name
			}
			return host.Obj{{Key: "id", Value: id}, {Key: "label", Value: label}}
		}
	}
	return nil
}

func label(m host.Obj) any {
	if v := get(m, "label"); truthy(v) {
		return v
	}
	return get(m, "id")
}

func noPatterns(schema any) any {
	switch x := schema.(type) {
	case host.Obj:
		out := host.Obj{}
		for _, kv := range x {
			if _, isStr := kv.Value.(string); kv.Key == "pattern" && isStr {
				continue
			}
			out = append(out, host.KV{Key: kv.Key, Value: noPatterns(kv.Value)})
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = noPatterns(v)
		}
		return out
	}
	return schema
}

// portable keeps the body fields every Anthropic-compatible provider understands.
func portable(body host.Obj, name string) (host.Obj, error) {
	out := host.Obj{}
	for _, k := range portableKeys {
		if v, ok := body.Get(k); ok {
			out = append(out, host.KV{Key: k, Value: v})
		}
	}
	out.Set("model", name)
	raw, ok := out.Get("tools")
	if !ok {
		return out, nil
	}
	tools, isList := raw.([]any)
	if !isList {
		return nil, errors.New("invalid_request")
	}
	// Server tools (web_search …) exist only at Anthropic; custom tools carry an input_schema.
	var kept []any
	for _, t := range tools {
		tool, isObj := t.(host.Obj)
		if !isObj {
			return nil, errors.New("invalid_request")
		}
		if !has(tool, "input_schema") {
			kind, kindOK := get(tool, "type").(string)
			name, nameOK := get(tool, "name").(string)
			if !kindOK || kind == "" || kind == "custom" || !nameOK || name == "" {
				return nil, errors.New("invalid_request")
			}
		}
	}
	for _, t := range tools {
		tool := t.(host.Obj)
		if schema, ok := tool.Get("input_schema"); ok {
			kept = append(kept, with(tool, "input_schema", noPatterns(schema)))
		}
	}
	if len(kept) == 0 {
		out.Del("tools")
		out.Del("tool_choice")
	} else {
		out.Set("tools", kept)
	}
	return out, nil
}

// statusError is an upstream HTTP rejection (urllib's HTTPError).
type statusError struct{ status int }

func (e *statusError) Error() string { return "HTTP Error " + strconv.Itoa(e.status) }

// responseChunks is response_chunks: the body with secret masked across read boundaries; with a
// limit (>= 0) an oversized or short body fails. *short reports a body shorter than declared.
func responseChunks(resp *http.Response, secret string, limit int, short *bool) byteSeq {
	return func(yield func([]byte, error) bool) {
		key := []byte(secret)
		var tail []byte
		total := 0
		buf := make([]byte, 65536)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				total += n
				if limit >= 0 && total > limit {
					yield(nil, Code("upstream_too_large"))
					return
				}
				chunk := append([]byte(nil), buf[:n]...)
				if len(key) == 0 {
					if !yield(chunk, nil) {
						return
					}
				} else {
					// Keep the boundary tail so a key split across reads is still masked.
					joined := bytes.ReplaceAll(append(tail, chunk...), key, []byte("***"))
					cut := max(len(joined)-len(key)+1, 0)
					if !yield(joined[:cut], nil) {
						return
					}
					tail = append([]byte(nil), joined[cut:]...)
				}
			}
			if err == io.EOF {
				break
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				if short != nil {
					*short = true
				}
				if limit >= 0 {
					yield(nil, Code("upstream_truncated"))
					return
				}
				break
			}
			if err != nil {
				yield(nil, err)
				return
			}
		}
		if len(tail) > 0 {
			yield(tail, nil)
		}
	}
}

func mediaType(h http.Header) string {
	t, _, err := mime.ParseMediaType(h.Get("content-type"))
	if err != nil {
		return "text/plain"
	}
	return t
}

// chatStream is _chat_stream: a Messages body sent to a provider's Chat API, answered in
// Messages form. A rejection is a *statusError before the first chunk.
func chatStream(parent context.Context, base, key string, body host.Obj, extra ...[2]string) byteSeq {
	return func(yield func([]byte, error) bool) {
		raw, err := pyDumps(body)
		if err != nil {
			yield(nil, err)
			return
		}
		ctx, cancel := context.WithTimeout(parent, upstreamWait)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(raw))
		if err != nil {
			yield(nil, err)
			return
		}
		session := make([]byte, 16)
		rand.Read(session)
		req.Header.Set("content-type", "application/json")
		for _, h := range extra {
			req.Header.Set(h[0], h[1])
		}
		if key != "" { // a custom endpoint saved without a key gets no Authorization
			req.Header.Set("authorization", "Bearer "+key)
		}
		req.Header.Set("x-opencode-session", hex.EncodeToString(session))
		resp, err := direct.Do(req)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			yield(nil, &statusError{resp.StatusCode})
			return
		}
		contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("content-type"), ";")[0]))
		data := responseChunks(resp, key, subscription.MaxOutput, nil)
		model := get(body, "model").(string)
		if truthy(get(body, "stream")) {
			if contentType != "text/event-stream" {
				yield(nil, Code("upstream_protocol"))
				return
			}
			for chunk, err := range pipe(func(emit func([]byte) error) error { return MessagesStream(data, model, emit) }) {
				if !yield(chunk, err) || err != nil {
					return
				}
			}
			return
		}
		if contentType != "application/json" {
			yield(nil, Code("upstream_protocol"))
			return
		}
		all, err := gather(data)
		if err != nil {
			yield(nil, err)
			return
		}
		parsed, err := Parse(string(all))
		if err != nil {
			yield(nil, err)
			return
		}
		result, err := MessagesResponse(parsed, model)
		if err != nil {
			yield(nil, err)
			return
		}
		out, err := pyDumps(result)
		yield(out, err)
	}
}

// upstreamRejected and targetUnavailable steer a group to its next candidate.
type upstreamRejected struct {
	status int
	code   string
}

func (e *upstreamRejected) Error() string { return e.code }

type targetUnavailable struct {
	status int
	code   string
}

func (e *targetUnavailable) Error() string { return e.code }

func rejected(status int) error {
	return &upstreamRejected{status, "upstream_http_" + strconv.Itoa(status)}
}

type geminiRoute struct {
	model  string
	stream bool
}

// exchange is one request's state (the Python handler's attributes).
type exchange struct {
	w      http.ResponseWriter
	r      *http.Request
	target string // self.path

	started    bool
	group      map[string]any // _group_call; nil is None
	attemptErr string
	attemptAt  time.Time
	publicErr  string
	status     int           // the status last sent to the caller
	retryAfter time.Duration // the provider's Retry-After on its last refusal, 0 when none

	chat         bool // _chat_request is not None
	chatReq      host.Obj
	responses    bool
	responsesReq host.Obj
	geminiAPI    bool
	geminiReq    *geminiRoute
	systemoneAPI bool

	nativeChat, nativeGemini bool
	geminiRaw                host.Obj // nil is None
	geminiUnconvertible      bool
	geminiCount              bool
	googleResume             string
	structured               bool
	requested                string
	origin                   host.Obj // a Messages call's kind, session and turn (trace.go); nil elsewhere
}

// Handler serves the gateway.
type Handler struct{}

func (Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	x := &exchange{w: w, r: r, target: r.RequestURI}
	var err error
	switch r.Method {
	case http.MethodGet:
		err = x.doGET()
	case http.MethodPost:
		err = x.doPOST()
	default:
		w.WriteHeader(http.StatusNotImplemented)
		return
	}
	if err != nil {
		// An escaping failure drops the connection, so a partial body never looks complete.
		panic(http.ErrAbortHandler)
	}
}

func (x *exchange) path() string { p, _ := splitURL(x.target); return p }

func (x *exchange) sendResponse(status int) {
	x.started, x.status = true, status
	if x.group != nil && (status < 200 || status > 299) && x.attemptErr == "" {
		x.attemptErr = "upstream_http_" + strconv.Itoa(status)
	}
}

// send is _send: the entrance codecs, then the status, headers and body.
func (x *exchange) send(status int, data byteSeq, ctype string, headers [][2]string) error {
	if x.chat && !x.nativeGemini {
		status, data, ctype = x.chatResponse(status, data, ctype)
		// Provider headers can describe the original body or contain private data.
		headers = nil
	}
	if x.responses {
		status, data, ctype = x.responsesResponse(status, data, ctype)
	}
	if x.geminiAPI {
		status, data, ctype = x.geminiResponse(status, data, ctype)
	}
	x.sendResponse(status)
	h := x.w.Header()
	h.Set("content-type", ctype)
	for _, kv := range headers {
		h.Add(kv[0], kv[1])
	}
	_ = http.NewResponseController(x.w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	x.w.WriteHeader(status)
	flusher, _ := x.w.(http.Flusher)
	for chunk, err := range data {
		if err != nil {
			return err
		}
		_ = http.NewResponseController(x.w).SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := x.w.Write(chunk); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	return nil
}

func (x *exchange) sendBytes(status int, data []byte) error {
	return x.send(status, once(data), "application/json", nil)
}

func (x *exchange) inputError(status int, message string) error {
	x.publicErr = message
	return x.sendBytes(anthropicError(status, message))
}

func bounded(source byteSeq) byteSeq {
	return func(yield func([]byte, error) bool) {
		total := 0
		for chunk, err := range source {
			if err == nil {
				total += len(chunk)
				if total > subscription.MaxOutput {
					err = Code("upstream_too_large")
				}
			}
			if !yield(chunk, err) || err != nil {
				return
			}
		}
	}
}

func (x *exchange) chatResponse(status int, data byteSeq, ctype string) (int, byteSeq, string) {
	if status < 200 || status > 299 {
		if status < 400 || status > 599 {
			status = 502
		}
		code := x.publicErr
		if code == "" {
			code = "upstream_http_" + strconv.Itoa(status)
		}
		return status, once(chatError(code, status)), "application/json"
	}
	request, model := x.chatReq, get(x.chatReq, "model").(string)
	stream, response := CodecStream, CodecResponse
	if x.nativeChat {
		stream, response = NativeStream, NativeResponse
	}
	source := bounded(data)
	if truthy(get(request, "stream")) {
		if !strings.HasPrefix(strings.ToLower(ctype), "text/event-stream") {
			x.attemptErr = "upstream_invalid_response"
			return 502, once(chatError(x.attemptErr, 502)), "application/json"
		}
		usage := truthy(get(get(request, "stream_options"), "include_usage"))
		converted := pipe(func(emit func([]byte) error) error { return stream(source, model, usage, emit) })
		return status, func(yield func([]byte, error) bool) {
			for chunk, err := range converted {
				if err != nil {
					x.attemptErr = "upstream_interrupted"
					yield([]byte("data: "+string(chatError(x.attemptErr, 502))+"\n\n"), nil)
					return
				}
				if !yield(chunk, nil) {
					return
				}
			}
		}, "text/event-stream"
	}
	raw, err := gather(source)
	var parsed any
	var out host.Obj
	if err == nil {
		parsed, err = Parse(string(raw))
	}
	if err == nil {
		out, err = response(parsed, model)
	}
	if err != nil {
		x.attemptErr = "upstream_invalid_response"
		return 502, once(chatError(x.attemptErr, 502)), "application/json"
	}
	return status, once(mustPy(out)), "application/json"
}

func (x *exchange) responsesResponse(status int, data byteSeq, ctype string) (int, byteSeq, string) {
	if status < 200 || status > 299 {
		return status, data, ctype
	}
	request := x.responsesReq
	if truthy(get(request, "stream")) {
		converted := pipe(func(emit func([]byte) error) error { return ResponsesStream(data, request, emit) })
		return status, func(yield func([]byte, error) bool) {
			sequence := 0
			for frame, err := range converted {
				if err != nil {
					x.attemptErr = "upstream_interrupted"
					event := host.Obj{{Key: "type", Value: "error"}, {Key: "code", Value: "upstream_interrupted"},
						{Key: "message", Value: "upstream_interrupted"}, {Key: "param", Value: nil}, {Key: "sequence_number", Value: sequence}}
					yield([]byte("event: error\ndata: "+string(mustPy(event))+"\n\n"), nil)
					return
				}
				if !yield(frame, nil) {
					return
				}
				sequence++
			}
		}, "text/event-stream"
	}
	raw, err := gather(data)
	var parsed any
	var out host.Obj
	if err == nil {
		parsed, err = Parse(string(raw))
	}
	if err == nil {
		out, err = ResponsesResponse(parsed, request)
	}
	if err != nil {
		x.attemptErr = "upstream_invalid_response"
		return 502, once(chatError(x.attemptErr, 502)), "application/json"
	}
	return status, once(mustPy(out)), "application/json"
}

func (x *exchange) geminiResponse(status int, data byteSeq, ctype string) (int, byteSeq, string) {
	if status < 200 || status > 299 {
		if status < 400 || status > 599 {
			status = 502
		}
		code := x.publicErr
		if code == "" {
			code = x.attemptErr
		}
		if code == "" {
			code = "upstream_http_" + strconv.Itoa(status)
		}
		return status, once(mustPy(geminiError(status, code))), "application/json"
	}
	request := x.geminiReq
	if request == nil || x.nativeGemini {
		return status, data, ctype
	}
	if request.stream {
		converted := pipe(func(emit func([]byte) error) error { return GeminiStream(data, request.model, emit) })
		return status, func(yield func([]byte, error) bool) {
			for chunk, err := range converted {
				if err != nil {
					x.attemptErr = "upstream_interrupted"
					yield([]byte("data: "+string(mustPy(geminiError(502, x.attemptErr)))+"\n\n"), nil)
					return
				}
				if !yield(chunk, nil) {
					return
				}
			}
		}, "text/event-stream"
	}
	raw, err := gather(data)
	var parsed any
	var out host.Obj
	if err == nil {
		parsed, err = Parse(string(raw))
	}
	if err == nil {
		out, err = GeminiResponse(parsed, request.model)
	}
	if err != nil {
		x.attemptErr = "upstream_invalid_response"
		return 502, once(mustPy(geminiError(502, x.attemptErr))), "application/json"
	}
	return status, once(mustPy(out)), "application/json"
}

func (x *exchange) geminiGet() error {
	path, query, err := geminiPath(x.target)
	size, offset := 0, 0
	if err == nil {
		size, offset, err = geminiPage(query)
	}
	if err != nil {
		return x.inputError(400, "invalid_request")
	}
	if path != "/v1beta/models" && !strings.HasPrefix(path, "/v1beta/models/") {
		return x.inputError(501, "unsupported_resource")
	}
	rows := geminiRows()
	if path != "/v1beta/models" {
		name := strings.TrimPrefix(path, "/v1beta/")
		for _, row := range rows {
			if get(row, "name") == name {
				return x.sendBytes(200, mustPy(row))
			}
		}
		return x.inputError(404, "model_not_found")
	}
	page := []any{}
	if offset < len(rows) {
		page = rows[offset:min(offset+size, len(rows))]
	}
	result := host.Obj{{Key: "models", Value: page}}
	if offset+size < len(rows) {
		result.Set("nextPageToken", strconv.Itoa(offset+size))
	}
	return x.sendBytes(200, mustPy(result))
}

// geminiPage reads the page size and offset of a Gemini model listing query.
func geminiPage(query map[string]string) (int, int, error) {
	var err error
	size, offset := 0, 0
	for key := range query {
		if !set("key", "pageSize", "page_size", "pageToken", "page_token")[key] {
			err = errors.New("invalid_request")
		}
	}
	_, a := query["pageSize"]
	_, b := query["page_size"]
	_, c := query["pageToken"]
	_, d := query["page_token"]
	if a && b || c && d {
		err = errors.New("invalid_request")
	}
	sizeText, tokenText := "1000", "0"
	for _, k := range []string{"page_size", "pageSize"} {
		if v, ok := query[k]; ok {
			sizeText = v
		}
	}
	for _, k := range []string{"page_token", "pageToken"} {
		if v, ok := query[k]; ok {
			tokenText = v
		}
	}
	if !sizeRe.MatchString(sizeText) || !offsetRe.MatchString(tokenText) {
		err = errors.New("invalid_request")
	} else {
		size, _ = strconv.Atoi(sizeText)
		offset, _ = strconv.Atoi(tokenText)
		if size < 1 || size > 1000 {
			err = errors.New("invalid_request")
		}
	}
	return size, offset, err
}

// geminiRows is the Gemini model listing of the 라우팅 models.
func geminiRows() []any {
	var rows []any
	for _, m := range routingModels() {
		id := get(m, "id").(string)
		methods := []any{"generateContent"}
		if strings.HasPrefix(id, "google/") {
			methods = []any{"generateContent", "countTokens"}
		}
		rows = append(rows, host.Obj{{Key: "name", Value: "models/" + id}, {Key: "baseModelId", Value: id},
			{Key: "displayName", Value: label(m)}, {Key: "supportedGenerationMethods", Value: methods}})
	}
	return rows
}

// mentions reports whether any string or key in v contains secret (codex._redact(v) != v).
func mentions(v any, secret string) bool {
	switch t := v.(type) {
	case string:
		return strings.Contains(t, secret)
	case []any:
		for _, item := range t {
			if mentions(item, secret) {
				return true
			}
		}
	case host.Obj:
		for _, kv := range t {
			if strings.Contains(kv.Key, secret) || mentions(kv.Value, secret) {
				return true
			}
		}
	}
	return false
}

// systemone relays the 라우팅 server's structured-answer route.
func (x *exchange) systemone() error {
	x.chat, x.responses, x.geminiReq, x.geminiAPI, x.systemoneAPI = false, false, nil, false, true
	path, query := splitURL(x.target)
	listing := x.r.Method == http.MethodGet && path == "/v1/systemone/models"
	if query != "" || !(listing || x.r.Method == http.MethodPost && path == "/v1/systemone") {
		return x.inputError(501, "unsupported_method")
	}
	var body host.Obj
	var payload host.Obj
	if !listing {
		var err error
		if body, err = x.systemoneBody(); err != nil {
			status := 400
			if err.Error() == "input_too_large" {
				status = 413
			}
			return x.inputError(status, "invalid_request")
		}
		payload = host.Obj{{Key: "scope", Value: scopeObj()}, {Key: "request", Value: body}}
	}
	tokenFile := host.RunnerTokenFile
	if _, err := os.Stat(tokenFile); err != nil {
		tokenFile = host.TokenFile
	}
	raw, err := os.ReadFile(tokenFile)
	secret := strings.TrimSpace(string(raw))
	if _, ok := subscription.Token(secret); err != nil || !ok {
		return x.inputError(503, "login_unavailable")
	}
	target := host.BaseURL + "/v1/routing/systemone"
	var data io.Reader
	method := http.MethodGet
	if listing {
		target += "?" + scopeQuery()
	} else {
		text, err := host.Dumps(payload, host.DumpOpts{NoASCII: true})
		if err != nil {
			return x.inputError(400, "invalid_request")
		}
		data, method = strings.NewReader(text), http.MethodPost
	}
	return x.systemoneRelay(method, target, data, secret, listing, body)
}

// systemoneBody reads and checks a structured-answer request body.
func (x *exchange) systemoneBody() (host.Obj, error) {
	raw, err := x.body()
	var parsed any
	var body host.Obj
	if err == nil {
		parsed, err = Loads(string(raw), "invalid_request")
	}
	if err == nil {
		body, err = Keys(parsed, []string{"model", "state", "questions"}, "invalid_request")
	}
	if err == nil {
		model, ok := get(body, "model").(string)
		questions, isObj := get(body, "questions").(host.Obj)
		if !ok || !modelID.MatchString(model) || strings.HasPrefix(model, "group/") || !has(body, "state") || !isObj || len(questions) == 0 {
			err = errors.New("invalid_request")
		}
	}
	return body, err
}

// systemoneRelay calls the 라우팅 structured-answer route and sends its checked result.
func (x *exchange) systemoneRelay(method, target string, data io.Reader, secret string, listing bool, body host.Obj) error {
	invalid := func() error { return x.inputError(502, "upstream_invalid_response") }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, data)
	if err != nil {
		return invalid()
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := direct.Do(req)
	if err != nil {
		return invalid()
	}
	defer resp.Body.Close()
	status := resp.StatusCode
	if status != 200 {
		code, public := "upstream_http_"+strconv.Itoa(status), status
		if status == 404 || status == 501 {
			code, public = "systemone_unavailable", 501
		} else if status < 400 || status > 599 {
			public = 502
		}
		return x.inputError(public, code)
	}
	if mediaType(resp.Header) != "application/json" {
		return invalid()
	}
	all, err := gather(responseChunks(resp, "", subscription.MaxOutput+1024, nil))
	if err != nil {
		return invalid()
	}
	page, err := Loads(string(all), "invalid_response")
	if err != nil {
		return invalid()
	}
	o, ok := page.(host.Obj)
	if !ok || get(o, "authority") != "data_only_untrusted" || mentions(o, secret) {
		return invalid()
	}
	var result host.Obj
	if listing {
		if result, ok = systemoneListing(o); !ok {
			return invalid()
		}
	} else {
		var code int64
		if result, code, ok = systemoneAnswer(o, body); !ok {
			return invalid()
		}
		if code != 200 {
			return x.inputError(int(code), "upstream_http_"+strconv.FormatInt(code, 10))
		}
	}
	text, err := host.Dumps(result, host.DumpOpts{NoASCII: true})
	if err != nil {
		return invalid()
	}
	return x.sendBytes(200, []byte(text))
}

// systemoneAnswer checks a structured-answer page against its request; false when it is invalid,
// and the upstream status when that is not 200.
func systemoneAnswer(o, body host.Obj) (host.Obj, int64, bool) {
	code, intOK := host.Int(get(o, "status"))
	result, ok := get(o, "result").(host.Obj)
	if !intOK || !isInt(get(o, "status")) || (code != 200 && (code < 400 || code > 599)) || !ok {
		return nil, 0, false
	}
	if code != 200 {
		return nil, code, true
	}
	_, modelOK := get(result, "model").(string)
	answers, answersOK := get(result, "answers").(host.Obj)
	_, usageOK := get(result, "usage").(host.Obj)
	if !modelOK || !answersOK || !usageOK || !sameKeys(answers, get(body, "questions").(host.Obj)) {
		return nil, 0, false
	}
	return result, code, true
}

// systemoneListing is the OpenAI model list of a structured-answer listing page; false when a row is invalid.
func systemoneListing(o host.Obj) (host.Obj, bool) {
	rows, ok := get(o, "items").([]any)
	if !ok {
		return nil, false
	}
	out := []any{}
	for _, row := range rows {
		id, isStr := get(row, "id").(string)
		if _, isObj := row.(host.Obj); !isObj || !isStr || !modelID.MatchString(id) || strings.HasPrefix(id, "group/") {
			return nil, false
		}
		out = append(out, host.Obj{{Key: "id", Value: id}, {Key: "object", Value: "model"}})
	}
	return host.Obj{{Key: "object", Value: "list"}, {Key: "data", Value: out}}, true
}

func sameKeys(a, b host.Obj) bool {
	keys := map[string]bool{}
	for _, kv := range a {
		keys[kv.Key] = true
	}
	other := map[string]bool{}
	for _, kv := range b {
		other[kv.Key] = true
		if !keys[kv.Key] {
			return false
		}
	}
	return len(keys) == len(other)
}

// body is _body: the declared request body, bounded before it is read.
func (x *exchange) body() ([]byte, error) {
	length := x.r.ContentLength
	if length < 0 && len(x.r.TransferEncoding) == 0 {
		length = 0
	}
	if length < 0 || len(x.r.TransferEncoding) > 0 {
		return nil, errors.New("invalid_request")
	}
	if length > int64(subscription.MaxInput) {
		return nil, errors.New("input_too_large")
	}
	raw := make([]byte, length)
	if _, err := io.ReadFull(x.r.Body, raw); err != nil {
		return nil, errors.New("invalid_request")
	}
	return raw, nil
}

func (x *exchange) doGET() error {
	path, query := splitURL(x.target)
	if path == "/healthz" {
		if query != "" {
			return x.sendBytes(400, []byte(`{"error":"invalid_request"}`))
		}
		health := host.Obj{{Key: "service", Value: "arkism-gateway"}, {Key: "pid", Value: os.Getpid()}}
		if tokenOptional() { // the grace release's warning metric
			health = append(health, host.KV{Key: "tokenless_calls", Value: tokenless.Load()})
		}
		return x.sendBytes(200, mustPy(health))
	}
	if path == "/v1/systemone" || strings.HasPrefix(path, "/v1/systemone/") {
		return x.systemone()
	}
	x.geminiAPI = path == "/v1beta" || strings.HasPrefix(path, "/v1beta/")
	if x.geminiAPI {
		return x.geminiGet()
	}
	if path == "/v1/responses" || strings.HasPrefix(path, "/v1/responses/") {
		x.chat, x.chatReq, x.responses, x.responsesReq = true, host.Obj{}, true, host.Obj{}
		return x.inputError(501, "unsupported_field")
	}
	if path == "/v1/models" {
		data := []any{}
		for _, m := range routingModels() {
			id := get(m, "id").(string)
			owner, _, _ := strings.Cut(id, "/")
			data = append(data, host.Obj{{Key: "type", Value: "model"}, {Key: "object", Value: "model"}, {Key: "id", Value: id},
				{Key: "display_name", Value: label(m)}, {Key: "created_at", Value: "2026-01-01T00:00:00Z"},
				{Key: "created", Value: 1767225600}, {Key: "owned_by", Value: owner}})
		}
		return x.sendBytes(200, mustPy(host.Obj{{Key: "object", Value: "list"}, {Key: "data", Value: data},
			{Key: "has_more", Value: false}, {Key: "first_id", Value: nil}, {Key: "last_id", Value: nil}}))
	}
	return x.pass(nil)
}

func validMessages(messages any) bool {
	list, ok := messages.([]any)
	if !ok || len(list) == 0 {
		return false
	}
	for _, m := range list {
		o, isObj := m.(host.Obj)
		role := get(o, "role")
		switch get(o, "content").(type) {
		case string, []any:
		default:
			return false
		}
		if !isObj || (role != "user" && role != "assistant") {
			return false
		}
	}
	return true
}

func (x *exchange) doPOST() error {
	path, _ := splitURL(x.target)
	if path == "/healthz" {
		return x.sendBytes(405, []byte(`{"error":"unsupported_method"}`))
	}
	if path == "/v1/systemone" || strings.HasPrefix(path, "/v1/systemone/") {
		return x.systemone()
	}
	x.geminiAPI = path == "/v1beta" || strings.HasPrefix(path, "/v1beta/")
	x.responses = path == "/v1/responses" || strings.HasPrefix(path, "/v1/responses/")
	x.chat = path == "/v1/chat/completions" || x.responses || x.geminiAPI
	x.chatReq, x.responsesReq = host.Obj{}, host.Obj{}
	if x.geminiAPI {
		if done, err := x.geminiEntrance(); done {
			return err
		}
	}
	if x.responses && path != "/v1/responses" {
		return x.inputError(501, "unsupported_field")
	}
	raw, err := x.body()
	if err != nil {
		status := 400
		if err.Error() == "input_too_large" {
			status = 413
		}
		return x.inputError(status, err.Error())
	}
	if !x.geminiAPI && !entrances[path] {
		return x.pass(raw)
	}
	body, done, err := x.entranceBody(raw)
	if done {
		return err
	}
	return x.routePOST(path, body, raw)
}

// routePOST serves a parsed entrance request by its model.
func (x *exchange) routePOST(path string, body host.Obj, raw []byte) error {
	modelValue := getd(body, "model", "")
	model, isStr := modelValue.(string)
	if !isStr {
		return x.sendBytes(anthropicError(400, "invalid_request"))
	}
	x.requested = model
	counting := strings.HasSuffix(path, "count_tokens")
	if path == "/v1/messages" && !x.chat {
		x.origin = traceOrigin(body, x.r.Header, time.Now())
	}
	if (strings.HasPrefix(model, "group/") || strings.HasPrefix(model, "anthropic/")) && !x.validClaudeRequest(body, model, counting) {
		return x.inputError(400, "invalid_request")
	}
	if strings.HasPrefix(model, "group/") {
		return x.groupPOST(body, model, raw, counting)
	}
	return x.modelPOST(body, model, raw, counting)
}

// geminiEntrance reads the Gemini route and query of the request; done means a response was sent.
func (x *exchange) geminiEntrance() (bool, error) {
	decoded, query, err := geminiPath(x.target)
	if err != nil {
		return true, x.inputError(400, "invalid_request")
	}
	route := geminiURL.FindStringSubmatch(decoded)
	if route == nil {
		return true, x.inputError(501, "unsupported_method")
	}
	model, method := route[1], route[2]
	alt, ok := query["alt"]
	if !ok {
		alt = "json"
	}
	want := "json"
	if method == "streamGenerateContent" {
		want = "sse"
	}
	extra := false
	for key := range query {
		extra = extra || (key != "key" && key != "alt")
	}
	if !modelID.MatchString(model) || (strings.HasPrefix(model, "group/") && !groupID.MatchString(model)) || extra || alt != want {
		return true, x.inputError(400, "invalid_request")
	}
	x.geminiCount = method == "countTokens"
	if x.geminiCount && !strings.HasPrefix(model, "google/") {
		return true, x.inputError(501, "unsupported_method")
	}
	x.geminiReq = &geminiRoute{model, method == "streamGenerateContent"}
	return false, nil
}

// entranceBody parses an entrance body and converts it to Messages; done means a response was sent.
func (x *exchange) entranceBody(raw []byte) (host.Obj, bool, error) {
	text := string(raw)
	if len(raw) == 0 {
		text = "{}"
	}
	var parsed any
	var err error
	if x.responses || x.geminiAPI {
		parsed, err = Loads(text, "invalid_request")
	} else {
		parsed, err = Parse(text)
	}
	if err != nil {
		return nil, true, x.inputError(400, "invalid_request")
	}
	body, ok := parsed.(host.Obj)
	if !ok {
		return nil, true, x.inputError(400, "invalid_request")
	}
	if !x.chat && has(body, "response_format") {
		// The Messages entrance has no Chat-only field: refuse it before any egress instead of
		// forwarding it verbatim to Anthropic or silently dropping it for a chat target.
		return nil, true, x.inputError(400, "unsupported_field")
	}
	if x.geminiReq != nil {
		if done, err := x.geminiBody(&body); done {
			return nil, true, err
		}
	}
	if x.responses {
		converted, err := ResponsesToChat(body)
		if err != nil {
			return nil, true, x.inputError(400, "unsupported_or_invalid_request")
		}
		x.responsesReq, body = body, converted
	}
	if x.chat {
		// A Chat-only response_format is validated by the codec and kept out of the converted
		// body: only a chat target may forward it, decided before any egress.
		converted, err := CodecToMessages(body)
		if err != nil {
			return nil, true, x.inputError(400, "unsupported_or_invalid_request")
		}
		x.chatReq, x.structured, body = body, has(body, "response_format"), converted
	}
	return body, false, nil
}

// validClaudeRequest checks a group or anthropic/ request's model, messages and anthropic-beta header.
func (x *exchange) validClaudeRequest(body host.Obj, model string, counting bool) bool {
	if !modelID.MatchString(model) {
		return false
	}
	if !counting && !validMessages(get(body, "messages")) {
		return false
	}
	beta := ""
	if !x.chat {
		beta = x.r.Header.Get("anthropic-beta")
	}
	bad := len(beta) > 1024
	if beta != "" {
		for _, part := range strings.Split(beta, ",") {
			bad = bad || !betaPart.MatchString(strings.TrimSpace(part))
		}
	}
	return !bad
}

// groupPOST serves an entrance request for a group/ model.
func (x *exchange) groupPOST(body host.Obj, model string, raw []byte, counting bool) error {
	if !groupID.MatchString(model) {
		return x.sendBytes(anthropicError(400, "invalid_group"))
	}
	if counting {
		probe := host.Obj{{Key: "max_tokens", Value: 1}}
		for _, kv := range body {
			probe.Set(kv.Key, kv.Value)
		}
		if ok, err := x.googleContinuation(probe); !ok {
			return err
		}
		if x.googleResume != "" {
			return x.inputError(501, "unsupported_method")
		}
		return x.sendBytes(200, mustPy(host.Obj{{Key: "input_tokens", Value: len(raw) / 4}}))
	}
	return x.runGroup(body, model)
}

// modelPOST serves an entrance request for a single model.
func (x *exchange) modelPOST(body host.Obj, model string, raw []byte, counting bool) error {
	w, err := target(model)
	if err != nil {
		var crewErr *host.CrewError
		if errors.As(err, &crewErr) {
			return x.sendBytes(anthropicError(500, crewErr.Msg))
		}
		return err
	}
	if w == nil {
		return x.sendBytes(anthropicError(400, "이 모델을 실행할 연결이 없어요. 모델 설정에서 로그인과 한도를 확인해 주세요."))
	}
	if x.structured && w.kind != "chat" { // response_format is Chat-only, refused before egress
		return x.inputError(400, "unsupported_field")
	}
	if counting && (w.kind == "google" || w.kind == "chat" && (strings.HasPrefix(model, "openai/") || strings.HasPrefix(model, "xai/"))) {
		return x.inputError(501, "unsupported_method")
	}
	if x.geminiRaw == nil {
		if ok, err := x.googleContinuation(body); !ok {
			return err
		}
		if x.googleResume != "" && w.kind != "google" {
			return x.inputError(409, "conversation_mismatch")
		}
	}
	if w.kind == "anthropic" && !x.chat && !strings.HasPrefix(model, "anthropic/") {
		return x.pass(raw)
	}
	if counting && w.kind != "anthropic" { // rough: a custom provider's context bar
		return x.sendBytes(200, mustPy(host.Obj{{Key: "input_tokens", Value: len(raw) / 4}}))
	}
	return x.runModel(body, w, model, counting)
}

// runModel executes a single-model request, admitted unless it only counts tokens.
func (x *exchange) runModel(body host.Obj, w *where, model string, counting bool) error {
	if counting {
		return x.execute(body, w)
	}
	release, after, err := admit(x.r.Context(), model, true)
	if err != nil {
		return x.refuseLimit(err, after)
	}
	defer release()
	err = x.execute(body, w)
	// A lone model is never reordered, but what it met still rests (or clears) it for groups.
	modelRests.settle(model, x.status != 0, x.status, x.attemptErr, x.retryAfter, clock())
	return err
}

// geminiBody converts a Gemini entrance body to Chat in place; done means a response was sent.
func (x *exchange) geminiBody(body *host.Obj) (bool, error) {
	original := *body
	model := x.geminiReq.model
	if strings.HasPrefix(model, "google/") || strings.HasPrefix(model, "group/") {
		if stated, ok := original.Get("model"); ok {
			s, isStr := stated.(string)
			if !isStr || strings.TrimPrefix(s, "models/") != model {
				return true, x.inputError(400, "invalid_request")
			}
			original = append(host.Obj(nil), original...)
			original.Del("model")
		}
		prepared, err := googlePrepare(original, "gemini")
		if err != nil {
			var ge *GoogleError
			if errors.As(err, &ge) {
				return true, x.inputError(ge.Status, ge.Code)
			}
			return true, x.inputError(400, "unsupported_or_invalid_request")
		}
		x.geminiRaw = prepared
	}
	converted, err := GeminiToChat(original, model, x.geminiReq.stream)
	if err == nil {
		*body = converted
		return false, nil
	}
	if x.geminiRaw == nil {
		return true, x.inputError(400, "unsupported_or_invalid_request")
	}
	x.geminiUnconvertible, x.chat = true, false
	var texts []string
	for _, content := range get(x.geminiRaw, "contents").([]any) {
		if getd(content.(host.Obj), "role", "user") != "user" {
			continue
		}
		for _, part := range get(content, "parts").([]any) {
			if s, ok := get(part, "text").(string); ok {
				texts = append(texts, s)
			}
		}
	}
	*body = host.Obj{{Key: "model", Value: model}, {Key: "messages", Value: []any{
		host.Obj{{Key: "role", Value: "user"}, {Key: "content", Value: strings.Join(texts, "\n")}}}}}
	return false, nil
}

// owner identifies the caller for conversation state: its credentials and the public model.
func (x *exchange) owner() string {
	geminiKey := []any{}
	googKey := ""
	if x.geminiAPI {
		_, query := splitURL(x.target)
		values, _ := url.ParseQuery(query)
		for _, v := range values["key"] {
			if v != "" {
				geminiKey = append(geminiKey, v)
			}
		}
		googKey = x.r.Header.Get("x-goog-api-key")
	}
	identity := []any{x.r.Header.Get("authorization"), x.r.Header.Get("x-api-key"), geminiKey, googKey, x.requested}
	sum := sha256.Sum256(mustPy(identity))
	return hex.EncodeToString(sum[:])
}

// googleContinuation records a Google-owned history's model; false means a response was sent.
func (x *exchange) googleContinuation(body host.Obj) (bool, error) {
	chat := x.chatReq
	var err error
	if !x.chat {
		chat, err = MessagesToChat(body)
	}
	if err == nil {
		x.googleResume, err = bridgeContinuationModel(chat, x.owner())
	}
	var ge *GoogleError
	if errors.As(err, &ge) {
		return false, x.inputError(ge.Status, ge.Code)
	}
	if err != nil && bridgeOwnedCalls(getd(body, "messages", []any{})) {
		return false, x.inputError(409, "conversation_mismatch")
	}
	return true, nil
}

func (x *exchange) refuse(code string) error {
	if x.group != nil {
		return &targetUnavailable{400, code}
	}
	return x.inputError(400, code)
}

func (x *exchange) execute(body host.Obj, w *where) error {
	x.nativeChat, x.nativeGemini = false, false
	if x.structured && w.kind != "chat" { // group candidates that cannot express it are skipped
		return x.refuse("unsupported_field")
	}
	if w.kind == "google" {
		if x.geminiRaw != nil {
			return x.google(w.model)
		}
		return x.googleChat(body, w.model)
	}
	if x.geminiUnconvertible {
		return x.refuse("unsupported_field")
	}
	switch w.kind {
	case "anthropic":
		return x.executeClaude(body)
	case "codex":
		return x.executeCodex(body, w)
	}
	hostChatAPI := w.kind == "chat" && (strings.HasPrefix(get(body, "model").(string), "openai/") || strings.HasPrefix(get(body, "model").(string), "xai/"))
	if w.kind == "chat" && x.chat {
		x.nativeChat = true
		data, err := pyDumps(with(x.chatReq, "model", w.name))
		if err != nil {
			return x.inputError(400, "invalid_request")
		}
		return x.forward(w.base+"/chat/completions", data, w.auth(map[string]string{"content-type": "application/json"}, "authorization", "Bearer "), w.key)
	}
	var err error
	if x.chat {
		body = with(body, "model", w.name)
	} else if body, err = portable(body, w.name); err != nil {
		return x.inputError(400, "invalid_request")
	}
	if w.kind == "direct" {
		data, err := pyDumps(body)
		if err != nil {
			return x.inputError(400, "invalid_request")
		}
		return x.forward(w.base+"/v1/messages", data, w.auth(map[string]string{"content-type": "application/json",
			"anthropic-version": "2023-06-01"}, "x-api-key", "", "authorization", "Bearer "), w.key)
	}
	return x.chatAPI(w.base, w.key, body, hostChatAPI, host.HeaderPairs(map[string]any{"headers": w.headers})...)
}

// auth adds the target's extra headers, then its key under each (name, prefix) pair; no key, no auth header.
func (w *where) auth(headers map[string]string, pairs ...string) map[string]string {
	for _, h := range host.HeaderPairs(map[string]any{"headers": w.headers}) {
		headers[h[0]] = h[1]
	}
	for i := 0; w.key != "" && i+1 < len(pairs); i += 2 {
		headers[pairs[i]] = pairs[i+1] + w.key
	}
	return headers
}

// executeClaude runs a request on an anthropic target: the Claude CLI runner or the Messages API.
func (x *exchange) executeClaude(body host.Obj) error {
	after := get(body, "model").(string)
	if _, rest, found := strings.Cut(after, "/"); found {
		after = rest
	}
	name := host.ClaudeModel(after)
	counting := x.path() == "/v1/messages/count_tokens"
	if !counting {
		if resumed := claudeRuns.continuationModel(body, x.owner()); resumed != "" && resumed == name {
			return x.claude(with(body, "model", name))
		}
		if claudeOwnedCalls(get(body, "messages")) {
			return x.inputError(409, "conversation_mismatch")
		}
	}
	headers, secret, err := claudeHeaders(name)
	if err != nil {
		if err.Error() == "claude_subscription_requires_cli" {
			if counting {
				return x.inputError(501, "unsupported_method")
			}
			return x.claude(with(body, "model", name))
		}
		x.attemptErr = "login_unavailable"
		if x.group != nil {
			return &targetUnavailable{503, x.attemptErr}
		}
		return x.inputError(503, x.attemptErr)
	}
	if beta := x.r.Header.Get("anthropic-beta"); !x.chat && beta != "" {
		headers = with2(headers, "anthropic-beta", beta)
	}
	endpoint := "/v1/messages"
	if counting {
		endpoint = "/v1/messages/count_tokens"
	}
	data, err := pyDumps(with(body, "model", name))
	if err != nil {
		return x.inputError(400, "invalid_request")
	}
	return x.forward(Anthropic+endpoint, data, headers, secret)
}

// executeCodex runs a request on a Codex subscription target.
func (x *exchange) executeCodex(body host.Obj, w *where) error {
	if x.chat && (get(x.chatReq, "max_tokens") != nil || get(x.chatReq, "max_completion_tokens") != nil) {
		// The Codex backend cannot enforce a requested output-token limit.
		return x.refuse("unsupported_field")
	}
	request := with(body, "model", w.model)
	if x.chat {
		if _, err := subscription.TranslateRequest(plain(request), w.model); err != nil {
			var ce *subscription.CodexError
			errors.As(err, &ce)
			if ce != nil && ce.Code == "unsupported_request" {
				if x.group != nil {
					return &targetUnavailable{400, "unsupported_field"}
				}
				return x.inputError(ce.Status, "unsupported_field")
			}
			status := 400
			if ce != nil {
				status = ce.Status
			}
			return x.inputError(status, "invalid_request")
		}
	}
	return x.codex(request)
}

func with2(h map[string]string, k, v string) map[string]string {
	out := make(map[string]string, len(h)+1)
	for key, value := range h {
		out[key] = value
	}
	out[k] = v
	return out
}

func (x *exchange) reportAttempt() {
	call := map[string]any{}
	for k, v := range x.group {
		call[k] = v
	}
	call["ok"], call["error"] = x.attemptErr == "", x.attemptErr
	call["latency_ms"] = min(int64(86400000), time.Since(x.attemptAt).Milliseconds())
	if x.origin != nil {
		call["origin"] = x.origin
	}
	host.ReportCall(call)
	x.attemptAt = time.Now()
	x.group = map[string]any{"model": x.group["model"], "decision": "failover", "replaced": call["model"], "classifier_ms": 0}
}

// groupTask is the last user turn's text, the routing decision's only input.
func groupTask(messages []any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if get(message, "role") != "user" {
			continue
		}
		task := ""
		switch content := get(message, "content").(type) {
		case string:
			task = strings.TrimSpace(content)
		case []any:
			var texts []string
			for _, c := range content {
				if s, ok := get(c, "text").(string); ok && get(c, "type") == "text" {
					texts = append(texts, s)
				}
			}
			task = strings.TrimSpace(strings.Join(texts, "\n"))
		}
		if task != "" {
			return task
		}
	}
	return ""
}

func (x *exchange) runGroup(body host.Obj, group string) error {
	messages, ok := get(body, "messages").([]any)
	for _, m := range messages {
		if _, isObj := m.(host.Obj); !isObj {
			ok = false
		}
	}
	if !ok {
		return x.sendBytes(anthropicError(400, "invalid_request"))
	}
	if x.geminiRaw == nil {
		if ok, err := x.googleContinuation(body); !ok {
			return err
		}
	}
	task := groupTask(messages)
	if task == "" {
		return x.sendBytes(anthropicError(400, "group_task_missing"))
	}
	googleResumed, done, err := x.groupGoogleResume()
	if done {
		return err
	}
	resumed := claudeRuns.continuationModel(body, x.owner())
	if resumed == "" && claudeOwnedCalls(messages) {
		return x.inputError(409, "conversation_mismatch")
	}
	if len(task) > 20000 {
		task = strings.ToValidUTF8(task[:20000], "")
	}
	decision, err := host.DecideRoute(task, group)
	if err != nil {
		return x.sendBytes(anthropicError(503, "group_decision_failed"))
	}
	if decision == nil || decision["group"] != group {
		return x.sendBytes(anthropicError(502, "group_decision_mismatch"))
	}
	candidates, choice, ms, valid := groupCandidates(decision)
	if !valid {
		return x.sendBytes(anthropicError(502, "group_decision_invalid"))
	}
	if candidates, ok = pinGroup(candidates, resumed, googleResumed); !ok {
		return x.inputError(409, "conversation_mismatch")
	}
	rule, _ := decision["rule"].(string)
	return x.tryGroup(body, candidates, choice, rule, ms)
}

// groupGoogleResume is the model a native Gemini history continues; done means a response was sent.
func (x *exchange) groupGoogleResume() (string, bool, error) {
	googleResumed := x.googleResume
	if x.geminiRaw != nil {
		var err error
		if googleResumed, err = googleContinuationModel(x.geminiRaw, x.owner()); err != nil {
			var ge *GoogleError
			if errors.As(err, &ge) {
				return "", true, x.inputError(ge.Status, ge.Code)
			}
			return "", true, err
		}
	}
	return googleResumed, false, nil
}

// groupCandidates reads a routing decision: its members in order, its kind and classifier time.
func groupCandidates(decision map[string]any) ([]string, string, int64, bool) {
	fallbacks, present := decision["fallbacks"]
	if !present || fallbacks == nil {
		fallbacks = []any{}
	}
	list, isList := fallbacks.([]any)
	ids := []any{decision["id"]}
	if isList {
		ids = append(ids, list[:min(3, len(list))]...)
	}
	var ms int64
	msOK := true
	if raw := decision["classifier_ms"]; truthy(raw) {
		ms, msOK = host.Int(raw)
		msOK = msOK && isInt(raw)
	}
	valid := isList && msOK && ms >= 0 && ms <= 600000
	choice, _ := decision["decision"].(string)
	valid = valid && (choice == "clef" || choice == "default" || choice == "fixed")
	var candidates []string
	for _, id := range ids {
		s, isStr := id.(string)
		if !isStr || !modelID.MatchString(s) || strings.HasPrefix(s, "group/") {
			valid = false
			break
		}
		candidates = append(candidates, s)
	}
	return candidates, choice, ms, valid
}

// pinGroup narrows the candidates to the member a Claude or Google history continues; false when none is.
func pinGroup(candidates []string, resumed, googleResumed string) ([]string, bool) {
	if resumed != "" {
		pinned := ""
		for _, id := range candidates {
			if strings.HasPrefix(id, "anthropic/") && host.ClaudeModel(strings.SplitN(id, "/", 2)[1]) == resumed {
				pinned = id
				break
			}
		}
		if pinned == "" {
			return nil, false
		}
		candidates = []string{pinned}
	}
	if googleResumed != "" {
		found := false
		for _, id := range candidates {
			found = found || id == googleResumed
		}
		if !found {
			return nil, false
		}
		candidates = []string{googleResumed}
	}
	return candidates, true
}

// groupFailure is the last failure a group request met, sent when no member answers.
type groupFailure struct {
	status        int
	message, code string
}

// tryGroup runs the candidates in turn until one answers.
func (x *exchange) tryGroup(body host.Obj, candidates []string, choice, rule string, ms int64) error {
	last := groupFailure{503, "group_target_unavailable", ""}
	var first *groupFailure // the first member actually refused: its error is the caller's when none answers
	var limitErr error
	var limitAfter time.Duration
	unique := []string{}
	for _, mid := range candidates {
		if !slices.Contains(unique, mid) {
			unique = append(unique, mid)
		}
	}
	// Resting members go last; the pick keeps its decision only while it still goes first.
	candidates = modelRests.order(unique, clock())
	previous := ""
	if candidates[0] != unique[0] {
		previous = unique[0]
	}
	i := 0
	for n, mid := range candidates {
		// A member with no room now gives way to the next; only the last one waits its turn.
		release, after, err := admit(x.r.Context(), mid, n == len(candidates)-1)
		if err != nil {
			if !errors.Is(err, errLaneFull) && !errors.Is(err, errLaneWait) && !errors.Is(err, errRPM) {
				return err // the caller left while it waited
			}
			if limitErr == nil || after < limitAfter {
				limitAfter = after
			}
			limitErr = err
			last = groupFailure{http.StatusTooManyRequests, err.Error(), err.Error()}
			continue
		}
		limitErr = nil
		kind := choice
		classifier := ms
		if i > 0 || mid != unique[0] {
			kind, classifier = "failover", 0
		}
		i++
		x.group = map[string]any{"model": mid, "decision": kind, "replaced": previous, "classifier_ms": classifier}
		if kind != "failover" && rule != "" {
			x.group["rule"] = rule
		}
		x.attemptAt, x.attemptErr, x.status, x.retryAfter = time.Now(), "", 0, 0
		before := last
		done, skipped, err := x.groupAttempt(body, mid, release, &last)
		if done {
			return err
		}
		if !skipped {
			x.reportAttempt()
			if first == nil && last != before {
				refused := last
				first = &refused
			}
		}
		previous = mid
	}
	x.group = nil
	if limitErr != nil { // the last word was a limit: every later member was full too
		x.w.Header().Set("Retry-After", retrySeconds(limitAfter))
	} else if first != nil {
		last = *first
	}
	x.publicErr = last.code
	return x.sendBytes(anthropicError(last.status, last.message))
}

// groupAttempt runs one admitted member; done means the group request ended with err,
// skipped that the member made no call.
func (x *exchange) groupAttempt(body host.Obj, mid string, release func(), last *groupFailure) (bool, bool, error) {
	w, err := target(mid)
	if err != nil {
		w = nil
	}
	if w == nil {
		x.attemptErr = "unsupported_target"
		release()
		return false, false, nil
	}
	model := mid
	if w.kind == "anthropic" {
		model = host.ClaudeModel(strings.SplitN(mid, "/", 2)[1])
	}
	modelRests.claim(mid, clock())
	err = x.execute(with(body, "model", model), w)
	release()
	var rejectedErr *upstreamRejected
	var unavailable *targetUnavailable
	status := x.status
	if errors.As(err, &rejectedErr) {
		status = rejectedErr.status
	}
	modelRests.settle(mid, status != 0 || !errors.As(err, &unavailable), status, x.attemptErr, x.retryAfter, clock())
	switch {
	case err == nil:
		x.reportAttempt()
		return true, false, nil
	case errors.As(err, &rejectedErr):
		x.attemptErr, last.status, last.message, last.code = rejectedErr.code, rejectedErr.status, rejectedErr.code, ""
	case errors.As(err, &unavailable):
		x.attemptErr, last.status, last.message = unavailable.code, unavailable.status, unavailable.code
		// A candidate our own checks refused made no call: it is not reported as one, while
		// its code reaches the client exactly like the single-model path.
		skipped := noCall[unavailable.code]
		last.code = ""
		if skipped {
			last.code = unavailable.code
		}
		return false, skipped, nil
	default:
		x.attemptErr = "upstream_unreachable"
		if x.started {
			x.attemptErr = "upstream_interrupted"
		}
		started := x.started
		var sendErr error
		if !started {
			sendErr = x.sendBytes(anthropicError(502, x.attemptErr))
		}
		x.reportAttempt()
		if started {
			return true, false, err // the partial response is dropped, never left looking complete
		}
		return true, false, sendErr
	}
	return false, false, nil
}

func (x *exchange) reject(status int) error {
	if x.group != nil && !x.started && (retryStatuses[status] || status >= 500 && status <= 599) {
		return rejected(status)
	}
	return nil
}

// googleChat runs a Chat-shaped request on native Gemini through the bridge.
func (x *exchange) googleChat(body host.Obj, model string) error {
	var prepared *googlePrepared
	var actual string
	err := func() error {
		if !x.chat && x.r.Header.Get("anthropic-beta") != "" {
			return &GoogleError{Code: "unsupported_field", Status: 400}
		}
		chat := x.chatReq
		if !x.chat {
			var err error
			if chat, err = MessagesToChat(body); err != nil {
				return err
			}
		}
		chat = with(chat, "model", x.requested)
		var err error
		if prepared, err = newGooglePrepared(chat, x.owner()); err != nil {
			return err
		}
		actual = "google/" + model
		if x.group != nil {
			actual = x.group["model"].(string)
		}
		if prepared.pinned != "" && prepared.pinned != actual {
			return &GoogleError{Code: "conversation_mismatch", Status: 409}
		}
		return nil
	}()
	var ge *GoogleError
	if errors.As(err, &ge) {
		if x.group != nil && ge.Code == "unsupported_field" {
			return &targetUnavailable{ge.Status, ge.Code}
		}
		return x.inputError(ge.Status, ge.Code)
	}
	if err != nil {
		if x.group != nil && strings.HasPrefix(err.Error(), "unsupported") {
			return &targetUnavailable{400, "unsupported_field"}
		}
		return x.inputError(400, "unsupported_or_invalid_request")
	}
	chat := x.chatReq
	if !x.chat {
		chat, _ = MessagesToChat(body)
	}
	streaming := truthy(get(chat, "stream"))
	public := x.requested
	x.nativeChat = x.chat
	next, stop := iter.Pull2(googleIterContent(prepared.native, model, streaming, false))
	defer stop()
	// A remember failure surfaces unchanged; a later page failure becomes a stream error.
	remember := func(message host.Obj, parts []any) {
		if err := prepared.remember(message, parts, actual); err != nil {
			panic(emitError{err})
		}
	}
	firstPage, err, ok := next()
	if !ok {
		err = Code("upstream_invalid_response")
	}
	var first []byte
	var output func() ([]byte, error, bool)
	if err == nil {
		pages := func(yield func(any, error) bool) {
			if !yield(firstPage, nil) {
				return
			}
			for {
				page, err, ok := next()
				if !ok || !yield(page, err) || err != nil {
					return
				}
			}
		}
		var out byteSeq
		if streaming {
			out = pipe(func(emit func([]byte) error) error { return GoogleStream(pages, public, remember, emit) })
			if !x.chat {
				chatOut := out
				out = pipe(func(emit func([]byte) error) error { return MessagesStream(chatOut, public, emit) })
			}
		} else {
			out = func(yield func([]byte, error) bool) {
				result, err := GoogleResponse(pages, public, remember)
				if err == nil && !x.chat {
					result, err = MessagesResponse(result, public)
				}
				if err != nil {
					yield(nil, err)
					return
				}
				yield(pyDumpsUnicode(result), nil)
			}
		}
		var stopOut func()
		output, stopOut = iter.Pull2(out)
		defer stopOut()
		first, err, ok = output()
		if !ok {
			err = Code("upstream_invalid_response")
		}
	}
	if err != nil {
		return x.googlePageError(err)
	}
	rest := func(yield func([]byte, error) bool) {
		if !yield(first, nil) {
			return
		}
		for {
			chunk, err, ok := output()
			if !ok {
				return
			}
			if err != nil {
				x.attemptErr = "upstream_interrupted"
				if !x.chat {
					yield([]byte(`event: error`+"\n"+`data: {"type":"error","error":{"type":"api_error","message":"upstream_interrupted"}}`+"\n\n"), nil)
				} else {
					yield([]byte(`data: {"error":{"message":"upstream_interrupted","type":"api_error","code":"upstream_interrupted"}}`+"\n\n"), nil)
				}
				return
			}
			if !yield(chunk, nil) {
				return
			}
		}
	}
	ctype := "application/json"
	if streaming {
		ctype = "text/event-stream"
	}
	return x.send(200, rest, ctype, nil)
}

// googlePageError answers a native Gemini failure met before the first output.
func (x *exchange) googlePageError(err error) error {
	var ge *GoogleError
	if errors.As(err, &ge) {
		x.attemptErr = ge.Code
		if x.group != nil && !ge.Started && (retryStatuses[ge.Status] || ge.Code == "login_unavailable" ||
			ge.Code == "google_setup_required" || ge.Code == "google_capacity") {
			return &targetUnavailable{ge.Status, ge.Code}
		}
		return x.inputError(ge.Status, ge.Code)
	}
	x.attemptErr = "upstream_invalid_response"
	return x.inputError(502, x.attemptErr)
}

// google relays native Gemini pages unchanged.
func (x *exchange) google(model string) error {
	x.nativeGemini = true
	streaming := x.geminiReq.stream
	next, stop := iter.Pull2(googleIterContent(x.geminiRaw, model, streaming, x.geminiCount))
	defer stop()
	first, err, ok := next()
	if !ok {
		err = &GoogleError{Code: "upstream_truncated", Status: 502}
	}
	if err != nil {
		var ge *GoogleError
		if !errors.As(err, &ge) {
			return err
		}
		x.attemptErr = ge.Code
		if x.group != nil && !ge.Started && (retryStatuses[ge.Status] || ge.Code == "login_unavailable" ||
			ge.Code == "google_setup_required" || ge.Code == "google_capacity" || ge.Code == "unsupported_field") {
			return &targetUnavailable{ge.Status, ge.Code}
		}
		return x.inputError(ge.Status, ge.Code)
	}
	encode := func(page any) ([]byte, error) {
		if x.group != nil {
			if err := googleRemember(page.(host.Obj), x.owner(), x.group["model"].(string)); err != nil {
				return nil, err
			}
		}
		raw, err := host.Dumps(page, host.DumpOpts{NoASCII: true})
		if err != nil {
			return nil, err
		}
		if streaming {
			return []byte("data: " + raw + "\n\n"), nil
		}
		return []byte(raw), nil
	}
	data := func(yield func([]byte, error) bool) {
		if !yield(encode(first)) {
			return
		}
		for {
			page, err, ok := next()
			if !ok {
				return
			}
			var raw []byte
			if err == nil {
				raw, err = encode(page)
			}
			if err != nil {
				var ge *GoogleError
				if !errors.As(err, &ge) {
					yield(nil, err)
					return
				}
				x.attemptErr = ge.Code
				if streaming {
					yield([]byte("data: "+string(mustPy(geminiError(ge.Status, ge.Code)))+"\n\n"), nil)
				}
				return
			}
			if !yield(raw, nil) {
				return
			}
		}
	}
	ctype := "application/json"
	if streaming {
		ctype = "text/event-stream"
	}
	return x.send(200, data, ctype, nil)
}

// claude runs the request on the host's Claude CLI with the caller's tools.
func (x *exchange) claude(body host.Obj) error {
	if !x.chat && x.r.Header.Get("anthropic-beta") != "" {
		return x.refuse("unsupported_field")
	}
	ctx := x.r.Context()
	cancelled := func() bool { return ctx.Err() != nil }
	next, stop := iter.Pull2(claudeRuns.iterMessages(body, x.owner(), cancelled))
	defer stop()
	first, err, ok := next()
	if !ok {
		return x.inputError(502, "upstream_truncated")
	}
	if err != nil {
		var ce *ClaudeError
		if !errors.As(err, &ce) {
			return err
		}
		x.attemptErr = ce.Code
		if x.group != nil && !ce.Started {
			return &targetUnavailable{ce.Status, ce.Code}
		}
		return x.inputError(ce.Status, ce.Code)
	}
	streaming := truthy(get(body, "stream"))
	data := func(yield func([]byte, error) bool) {
		if !yield(first, nil) {
			return
		}
		for {
			chunk, err, ok := next()
			if !ok {
				return
			}
			if err != nil {
				var ce *ClaudeError
				if !errors.As(err, &ce) {
					yield(nil, err)
					return
				}
				x.attemptErr = ce.Code
				if streaming {
					yield([]byte("event: error\ndata: "+string(mustPy(streamError(ce.Code)))+"\n\n"), nil)
				}
				return
			}
			if !yield(chunk, nil) {
				return
			}
		}
	}
	ctype := "application/json"
	if streaming {
		ctype = "text/event-stream"
	}
	if err := x.send(200, data, ctype, nil); err != nil {
		claudeRuns.abortRequest(body, x.owner())
		return err
	}
	return nil
}

func streamError(code string) host.Obj {
	return host.Obj{{Key: "type", Value: "error"}, {Key: "error", Value: host.Obj{{Key: "type", Value: "api_error"}, {Key: "message", Value: code}}}}
}

// codex runs the request on the host Codex login, moving to the next signed-in account only on
// a rejection that produced no output.
func (x *exchange) codex(body host.Obj) error {
	model := get(body, "model").(string)
	streaming := truthy(get(body, "stream"))
	request := plain(body).(map[string]any)
	executable := host.Bin("ARKISM_CODEX_BIN", "codex")
	var tried []string
	var last *subscription.CodexError
	profile := codexProfile(model, tried)
	for profile != nil {
		id, _ := profile["id"].(string)
		configDir, _ := profile["config_dir"].(string)
		tried = append(tried, id)
		next, stop := iter.Pull2(codexIterMessages(request, configDir, executable, 120))
		first, err, ok := next()
		if !ok {
			stop()
			x.attemptErr = "upstream_truncated"
			return x.sendBytes(anthropicError(502, "upstream_truncated"))
		}
		if err != nil {
			stop()
			var e *subscription.CodexError
			if !errors.As(err, &e) {
				return err
			}
			last = e
			if e.Code == "upstream_rate_limit" {
				_ = recordLimit(profile, host.AgentsState, model, e.RetryAfter) // best effort, like crew
				x.retryAfter = time.Duration(max(e.RetryAfter, 0) * float64(time.Second))
			}
			// A rejected request produced no output. Try the next already signed-in account
			// without replaying an accepted response.
			retry := set("auth_missing", "auth_expired", "upstream_auth", "upstream_rate_limit")
			if x.group != nil {
				retry = set("upstream_auth", "upstream_forbidden", "upstream_rate_limit")
			}
			if retry[e.Code] {
				nextProfile := codexProfile(model, tried)
				if x.group != nil {
					if nextProfile == nil {
						if err := x.reject(e.Status); err != nil {
							return err
						}
					}
					x.attemptErr = e.Code
					x.reportAttempt()
					x.attemptErr = ""
				}
				profile = nextProfile
				continue
			}
			x.attemptErr = e.Code
			return x.sendBytes(anthropicError(e.Status, e.Code))
		}
		current := profile
		data := func(yield func([]byte, error) bool) {
			if !yield(first, nil) {
				return
			}
			for {
				chunk, err, ok := next()
				if !ok {
					return
				}
				if err != nil {
					var e *subscription.CodexError
					if !errors.As(err, &e) {
						yield(nil, err)
						return
					}
					if e.Code == "upstream_rate_limit" {
						_ = recordLimit(current, host.AgentsState, model, e.RetryAfter)
					}
					x.attemptErr = e.Code
					if streaming {
						yield([]byte("event: error\ndata: "+string(mustPy(streamError(e.Code)))+"\n\n"), nil)
					}
					return
				}
				if !yield(chunk, nil) {
					return
				}
			}
		}
		ctype := "application/json"
		if streaming {
			ctype = "text/event-stream"
		}
		err = x.send(200, data, ctype, nil)
		stop()
		return err
	}
	status, code := 503, "login_unavailable"
	if last != nil {
		status, code = last.Status, last.Code
	}
	if err := x.reject(status); err != nil {
		return err
	}
	x.attemptErr = code
	return x.sendBytes(anthropicError(status, code))
}

// pass relays a request to Anthropic with the caller's own headers.
func (x *exchange) pass(raw []byte) error {
	var order [][2]string
	for k, values := range x.r.Header {
		if !hop[strings.ToLower(k)] { // a repeated header keeps its last value, like the Python dict
			order = append(order, [2]string{k, values[len(values)-1]})
		}
	}
	return x.forwardHeaders(Anthropic+x.target, raw, order, "")
}

func (x *exchange) forward(target string, data []byte, headers map[string]string, secret string) error {
	var list [][2]string
	for k, v := range headers {
		list = append(list, [2]string{k, v})
	}
	return x.forwardHeaders(target, data, list, secret)
}

func (x *exchange) forwardHeaders(target string, data []byte, headers [][2]string, secret string) error {
	var body io.Reader
	if len(data) > 0 {
		body = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(x.r.Context(), upstreamWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, x.r.Method, target, body)
	if err == nil {
		for _, kv := range headers {
			req.Header.Add(kv[0], kv[1])
		}
		var resp *http.Response
		if resp, err = direct.Do(req); err == nil {
			defer resp.Body.Close()
			return x.relay(resp, secret)
		}
	}
	if err := x.unreachable(); err != nil {
		return err
	}
	return x.sendBytes(anthropicError(502, "upstream unreachable: URLError"))
}

// unreachable notes a transport failure (timeout, refused, reset): a group that has sent nothing
// yet moves on to its next member; nil otherwise.
func (x *exchange) unreachable() error {
	x.attemptErr = "upstream_unreachable"
	if x.group != nil && !x.started {
		return &upstreamRejected{502, x.attemptErr}
	}
	return nil
}

func (x *exchange) relay(resp *http.Response, secret string) error {
	x.retryAfter = retryAfterHeader(resp.Header.Get("Retry-After"), clock())
	if err := x.reject(resp.StatusCode); err != nil {
		return err
	}
	var headers [][2]string
	for k, values := range resp.Header {
		if lower := strings.ToLower(k); hop[lower] || lower == "content-type" {
			continue
		}
		for _, v := range values {
			if secret != "" {
				v = strings.ReplaceAll(v, secret, "***")
			}
			headers = append(headers, [2]string{k, v})
		}
	}
	limit := -1
	if x.chat {
		limit = subscription.MaxOutput
	}
	short := false
	ctype := resp.Header.Get("content-type")
	if _, present := resp.Header["Content-Type"]; !present {
		ctype = "application/json"
	}
	err := x.send(resp.StatusCode, responseChunks(resp, secret, limit, &short), ctype, headers)
	if x.group != nil && short {
		x.attemptErr = "upstream_truncated"
	}
	return err
}

// chatAPI sends a Messages body to a provider's Chat API (Messages entrance).
func (x *exchange) chatAPI(base, key string, body host.Obj, hostChatAPI bool, extra ...[2]string) error {
	streaming := truthy(get(body, "stream"))
	request, err := MessagesToChat(body)
	if err != nil {
		code := "invalid_request"
		if err.Error() == "unsupported_request" {
			code = "unsupported_request"
			if x.group != nil {
				return &targetUnavailable{400, code}
			}
		}
		return x.inputError(400, code)
	}
	if hostChatAPI {
		limit, _ := request.Get("max_tokens")
		request.Del("max_tokens")
		request.Set("max_completion_tokens", limit)
	}
	next, stop := iter.Pull2(chatStream(x.r.Context(), base, key, request, extra...))
	defer stop()
	first, err, ok := next()
	if !ok {
		return x.sendBytes(anthropicError(502, "upstream_truncated"))
	}
	if err != nil {
		var se *statusError
		var ue *url.Error
		status := 0
		if errors.As(err, &se) {
			status = se.status
		} else if errors.As(err, &ue) {
			if err := x.unreachable(); err != nil {
				return err
			}
		}
		if err := x.reject(status); err != nil {
			return err
		}
		if status < 400 || status > 599 {
			status = 502
		}
		return x.sendBytes(anthropicError(status, "upstream_unavailable"))
	}
	data := func(yield func([]byte, error) bool) {
		if !yield(first, nil) {
			return
		}
		for {
			chunk, err, ok := next()
			if !ok {
				return
			}
			if err != nil {
				x.attemptErr = "upstream_interrupted"
				if streaming {
					yield([]byte(`event: error`+"\n"+`data: {"type":"error","error":{"type":"api_error","message":"upstream_interrupted"}}`+"\n\n"), nil)
				}
				return
			}
			if !yield(chunk, nil) {
				return
			}
		}
	}
	ctype := "application/json"
	if streaming {
		ctype = "text/event-stream"
	}
	return x.send(200, data, ctype, nil)
}

// runGateway serves until SIGINT or SIGTERM, then ends every live Claude run.
func runGateway(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	host, _, splitErr := net.SplitHostPort(Listen)
	if splitErr != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		fmt.Fprintln(stderr, "arkism gateway: listen address must be a loopback IP")
		return 1
	}
	control, err := desktopcontrol.FromEnvironment("gateway", stdin, stdout)
	if err != nil {
		fmt.Fprintln(stderr, "arkism gateway: desktop control configuration invalid")
		return 1
	}
	listener, err := net.Listen("tcp", Listen)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	token, err := desktopGatewayToken(control != nil)
	if err != nil {
		listener.Close()
		fmt.Fprintln(stderr, "arkism gateway: local token:", err)
		return 1
	}
	server := &http.Server{Handler: httpguard.Protect(Guard{Next: Handler{}, Token: token, Optional: tokenOptional(), Warn: stderr, Attempts: httpguard.NewLimiter(20, time.Second)}),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	// A global write timeout would truncate valid SSE streams. Upstream calls
	// have a request-scoped deadline instead.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	err = serveGatewayLifecycle(server, listener, control, claudeRuns, signals)
	if control == nil || !control.Requested() {
		claudeRuns.close()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return 0
	}
	fmt.Fprintln(stderr, err)
	return 1
}

// Serve returns when listeners close; the process must additionally wait for
// Shutdown to finish before stopping provider children or returning from main.
func serveGatewayLifecycle(server *http.Server, listener net.Listener, control *desktopcontrol.Controller, bridge *claudeBridge, signals <-chan os.Signal) error {
	drained, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(drained)
		select {
		case <-signals:
		case <-stopped:
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if server.Shutdown(ctx) != nil {
			_ = server.Close()
		}
	}()
	err := serveDesktopGateway(server, listener, control, bridge)
	close(stopped)
	<-drained
	return err
}
