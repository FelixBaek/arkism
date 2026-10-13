package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// The Google gateway fixture (test_arkism_google_gateway.GoogleGatewayTest): a local provider
// standing in for the Gemini API, Code Assist and the OAuth token endpoint, the gateway on real
// TCP with the real target, and a stored host API key.

// gxResult is the provider's answer: text, or a signed function call.
func gxResult(tool bool) host.Obj {
	parts := `[{"text":"완료"}]`
	if tool {
		parts = `[{"functionCall":{"name":"echo","args":{"word":"한글"}},"thoughtSignature":"c2lnbmVkLXRob3VnaHQ="}]`
	}
	v, _ := Parse(`{"candidates":[{"index":0,"content":{"role":"model","parts":` + parts + `},"finishReason":"STOP"}],` +
		`"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":4,"thoughtsTokenCount":2,"totalTokenCount":17}}`)
	return v.(host.Obj)
}

const gxBody = `{"contents":[{"role":"user","parts":[{"text":"도구를 호출해 주세요"}]}],` +
	`"tools":[{"functionDeclarations":[{"name":"echo","parametersJsonSchema":{"type":"object"}}]}],` +
	`"generationConfig":{"thinkingConfig":{"thinkingBudget":128},"maxOutputTokens":256},` +
	`"safetySettings":[{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_NONE"}]}`

type gxSeen struct {
	path   string
	header http.Header
	body   any // url.Values for the token form
}

type gxFixture struct {
	*fixture
	root     string
	mode     string                    // json, tool or truncated
	result   func(tool bool) host.Obj  // the page the provider answers with
	once     func(http.ResponseWriter) // replaces the next generation answer
	provider []gxSeen
	login    string
}

func gxSetup(t *testing.T) *gxFixture {
	g := &gxFixture{fixture: newFixture(t), mode: "json", result: gxResult}
	g.root = g.gw.URL
	up := httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(up.Close)
	swap(t, &target, resolve)
	swap(t, &googleAPI, up.URL+"/api")
	swap(t, &googleAssist, up.URL+"/assist")
	swap(t, &googleOAuth, up.URL+"/token")
	swap(t, &googleOAuthClientFile, filepath.Join(g.state, "absent-google-oauth.json"))
	gReset()
	gxBridgeReset()
	t.Cleanup(func() { gReset(); gxBridgeReset() })
	host.APIMode["gemini"] = true
	writeFile(t, host.KeysFile, `{"keys":[{"provider":"google","key":"host-api-key"}]}`)
	g.decide = func(task, group string) (map[string]any, error) {
		return map[string]any{"group": "group/test", "id": "google/gemini-test", "fallbacks": []any{},
			"decision": "fixed", "classifier_ms": int64(0)}, nil
	}
	home := t.TempDir()
	for key, value := range map[string]string{"GEMINI_CLI_HOME": home, "GOOGLE_CLOUD_PROJECT": "", "GOOGLE_CLOUD_PROJECT_ID": "",
		"ARKISM_GOOGLE_CLIENT_ID": "synthetic-client-id", "ARKISM_GOOGLE_CLIENT_SECRET": "synthetic-client-secret"} {
		t.Setenv(key, value)
	}
	g.login = filepath.Join(home, ".gemini", "oauth_creds.json")
	g.endpoint("google/gemini-test", "generateContent")
	return g
}

func gxBridgeReset() {
	bridgeLock.Lock()
	bridgeTurns, bridgeOrder = map[[32]byte]*bridgeTurn{}, nil
	bridgeLock.Unlock()
}

// route sets the decision's model and fallbacks.
func (g *gxFixture) route(id string, fallbacks ...any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.decide = func(task, group string) (map[string]any, error) {
		return map[string]any{"group": "group/test", "id": id, "fallbacks": append([]any{}, fallbacks...),
			"decision": "fixed", "classifier_ms": int64(0)}, nil
	}
}

// oauth turns API mode off and writes an expired host login that must be refreshed.
func (g *gxFixture) oauth() []byte {
	delete(host.APIMode, "gemini")
	os.MkdirAll(filepath.Dir(g.login), 0o700)
	writeFile(g.t, g.login, `{"access_token": "old-host-oauth", "refresh_token": "host-refresh", "expiry_date": 1000}`)
	raw, _ := os.ReadFile(g.login)
	return raw
}

func (g *gxFixture) endpoint(model, method string) {
	g.url = g.root + "/v1beta/models/" + url.PathEscape(model) + ":" + method + "?key=caller-query"
	if method == "streamGenerateContent" {
		g.url += "&alt=sse"
	}
}

// next replaces the next generation answer.
func (g *gxFixture) next(answer func(http.ResponseWriter)) {
	g.mu.Lock()
	g.once = answer
	g.mu.Unlock()
}

func (g *gxFixture) setMode(mode string) {
	g.mu.Lock()
	g.mode = mode
	g.mu.Unlock()
}

func (g *gxFixture) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	path := r.URL.RequestURI()
	var body any
	if r.URL.Path == "/token" {
		body, _ = url.ParseQuery(string(raw))
	} else {
		body, _ = Parse(string(raw))
	}
	g.mu.Lock()
	g.provider = append(g.provider, gxSeen{path, r.Header.Clone(), body})
	mode, result, once := g.mode, g.result, g.once
	g.once = nil
	g.mu.Unlock()
	status, ctype := 200, "application/json"
	var page any
	switch {
	case r.URL.Path == "/token":
		page = M{"access_token": "fresh-host-oauth", "expires_in": 3600}
	case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
		page = M{"currentTier": M{"id": "free-tier"}, "cloudaicompanionProject": "synthetic-project"}
	case strings.HasSuffix(r.URL.Path, ":countTokens"):
		page = M{"totalTokens": 137}
	default:
		if once != nil {
			once(w)
			return
		}
		model, _ := get(body, "model").(string)
		if model == "" {
			model, _, _ = strings.Cut(r.URL.Path[strings.LastIndex(r.URL.Path, "/models/")+len("/models/"):], ":")
		}
		if model == "gemini-reject" {
			status, page = 429, M{"error": M{"message": "fresh-host-oauth private-diagnostic"}}
		} else {
			p := result(mode == "tool" || mode == "truncated")
			if mode == "truncated" {
				p = parsed(g.t, []byte(strings.Replace(dumpJSON(p), `,"finishReason":"STOP"`, "", 1))).(host.Obj)
			}
			page = p
			if strings.HasPrefix(r.URL.Path, "/assist") {
				page = host.Obj{{Key: "response", Value: p}, {Key: "traceId", Value: "synthetic-trace"}}
			}
		}
	}
	data := dumpJSON(page)
	if strings.Contains(r.URL.Path, "streamGenerateContent") {
		ctype = "text/event-stream"
		var text any = M{"candidates": []any{M{"index": 0, "content": M{"role": "model", "parts": []any{M{"text": "先"}}}}}}
		if strings.HasPrefix(r.URL.Path, "/assist") {
			text = M{"response": text}
		}
		data = "data: " + dumpJSON(text) + "\n\ndata: " + data + "\n\n"
	}
	w.Header().Set("content-type", ctype)
	w.Header().Set("content-length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	io.WriteString(w, data)
}

func (g *gxFixture) providerCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.provider)
}

// providerAt is provider request i (negative counts from the end).
func (g *gxFixture) providerAt(i int) gxSeen {
	g.mu.Lock()
	defer g.mu.Unlock()
	if i < 0 {
		i += len(g.provider)
	}
	if i < 0 || i >= len(g.provider) {
		g.t.Fatalf("no provider request %d of %d", i, len(g.provider))
	}
	return g.provider[i]
}

func (g *gxFixture) providerPaths() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, s := range g.provider {
		out = append(out, s.path)
	}
	return out
}

func (g *gxFixture) clearAll() {
	g.clear()
	g.mu.Lock()
	g.provider = nil
	g.mu.Unlock()
}

func (g *gxFixture) lastReportOK() any {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.reports) == 0 {
		return nil
	}
	return g.reports[len(g.reports)-1]["ok"]
}

func (g *gxFixture) reportOKs() []any {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []any{}
	for _, r := range g.reports {
		out = append(out, r["ok"])
	}
	return out
}

func (g *gxFixture) decisionCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.decisions
}

// gxObj parses a JSON object for a test body.
func gxObj(t *testing.T, text string) host.Obj {
	t.Helper()
	return parsed(t, []byte(text)).(host.Obj)
}

// gxClone deep-copies a decoded JSON value.
func gxClone(t *testing.T, v any) any {
	t.Helper()
	return parsed(t, []byte(dumpJSON(v)))
}
