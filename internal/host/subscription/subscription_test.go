package subscription

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

const secret = "synthetic-private-token"

func mustDumps(v any) string { return host.MustDumps(v, host.DumpOpts{NoASCII: true}) }

func loadGolden(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	v, err := host.Loads(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v.(map[string]any)
}

func TestParseUsageMatchesPython(t *testing.T) {
	seoul, err := time.LoadLocation("Asia/Seoul") // the goldens ran with TZ=Asia/Seoul
	if err != nil {
		t.Fatal(err)
	}
	defer func(old *time.Location) { time.Local = old }(time.Local)
	time.Local = seoul
	g := loadGolden(t, "parse_usage.json")
	for _, raw := range g["claude"].([]any) {
		c := raw.(map[string]any)
		at, _ := time.Parse(time.RFC3339, c["now"].(string))
		if got, want := mustDumps(ParseClaudeUsage(c["text"].(string), at)), mustDumps(c["out"]); got != want {
			t.Errorf("claude %q at %s\n got %s\nwant %s", c["text"], c["now"], got, want)
		}
	}
	at, _ := time.Parse(time.RFC3339, g["now"].(string))
	for _, raw := range g["codex"].([]any) {
		c := raw.(map[string]any)
		if got, want := mustDumps(ParseCodexUsage(c["obj"], at)), mustDumps(c["out"]); got != want {
			t.Errorf("codex %s\n got %s\nwant %s", mustDumps(c["obj"]), got, want)
		}
	}
}

func TestParseInputBoundaries(t *testing.T) {
	at := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC)
	for _, text := range []string{strings.Repeat("x", MaxBytes+1), strings.Repeat("가", MaxBytes)} {
		if got := ParseClaudeUsage(text, at)["usage_status"]; got != "unavailable" {
			t.Errorf("oversize text: %v", got)
		}
	}
	for _, used := range []float64{math.NaN(), math.Inf(1)} {
		obj := map[string]any{"rate_limit": map[string]any{"primary_window": map[string]any{"used_percent": used, "limit_window_seconds": 18000}}}
		if got := ParseCodexUsage(obj, at)["usage_status"]; got != "unavailable" {
			t.Errorf("used %v: %v", used, got)
		}
	}
}

func TestStrictJSON(t *testing.T) {
	for _, bad := range []string{"[1]x", "{", "\xff", `{"a":NaN}`, `{"a":1,"a":2}`, `{"a":` + strings.Repeat("9", 129) + `}`, strings.Repeat("[", 1500), "", "1 2"} {
		if _, err := StrictJSON([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, good := range []string{`{"a":[1,true,null,"x"]}`, " 1 ", `-` + strings.Repeat("9", 127), "1e400", "\xef\xbb\xbf{}"} {
		if _, err := StrictJSON([]byte(good)); err != nil {
			t.Errorf("refused %q: %v", good, err)
		}
	}
}

type folder struct {
	t                   *testing.T
	root, claude, codex string
}

func newFolder(t *testing.T) *folder {
	root := t.TempDir()
	f := &folder{t, root, filepath.Join(root, ".claude"), filepath.Join(root, ".codex")}
	os.Mkdir(f.claude, 0o700)
	os.Mkdir(f.codex, 0o700)
	return f
}

func (f *folder) write(path, text string) {
	f.t.Helper()
	os.Remove(path)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// claudeLogin writes .credentials.json; fields are raw JSON values that replace the defaults.
func (f *folder) claudeLogin(fields map[string]string) {
	values := map[string]string{"accessToken": strconv.Quote(secret), "subscriptionType": `"max"`, "expiresAt": "4102444800000"}
	for k, v := range fields {
		values[k] = v
	}
	var parts []string
	for k, v := range values {
		if v != "" {
			parts = append(parts, strconv.Quote(k)+":"+v)
		}
	}
	f.write(filepath.Join(f.claude, ".credentials.json"), `{"claudeAiOauth":{`+strings.Join(parts, ",")+`}}`)
}

func jwt(claims string) string {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return enc(`{"alg": "HS256"}`) + "." + enc(claims) + ".c3ludGhldGlj"
}

const defaultClaims = `{"exp": 4102444800, "https://api.openai.com/auth": {"chatgpt_plan_type": "plus", "chatgpt_account_id": "account-synthetic"}}`

// codexLogin writes auth.json; tokens holds raw JSON token fields over the defaults.
func (f *folder) codexLogin(tokens map[string]string) {
	values := map[string]string{"id_token": strconv.Quote(jwt(defaultClaims)), "access_token": strconv.Quote(secret)}
	for k, v := range tokens {
		values[k] = v
	}
	var parts []string
	for k, v := range values {
		parts = append(parts, strconv.Quote(k)+":"+v)
	}
	f.write(filepath.Join(f.codex, "auth.json"), `{"tokens":{`+strings.Join(parts, ",")+`}}`)
}

func (f *folder) script(body string) string {
	path := filepath.Join(f.root, "synthetic-claude")
	f.write(path, "#!/bin/sh\n"+body)
	os.Chmod(path, 0o700)
	return path
}

func assertNoSecret(t *testing.T, v map[string]any) {
	t.Helper()
	text := mustDumps(v)
	if strings.Contains(text, secret) || strings.Contains(text, "access_token") || strings.Contains(text, "accessToken") {
		t.Errorf("secret in %s", text)
	}
}

func eq(t *testing.T, got, want any) {
	t.Helper()
	if g, w := mustDumps(got), mustDumps(want); g != w {
		t.Errorf("\n got %s\nwant %s", g, w)
	}
}

func TestIdentityClaude(t *testing.T) {
	f := newFolder(t)
	f.claudeLogin(nil)
	f.write(filepath.Join(f.root, ".claude.json"), `{"oauthAccount":{"subscriptionType":"pro"}}`)
	got := Identity("claude", f.claude)
	eq(t, got, map[string]any{"status": "connected", "plan": "max"})
	assertNoSecret(t, got)
	os.Remove(filepath.Join(f.claude, ".credentials.json"))
	eq(t, Identity("claude", f.claude), map[string]any{"status": "missing"})

	// A plan hint never claims an empty token is connected.
	f.claudeLogin(map[string]string{"accessToken": `""`})
	eq(t, Identity("claude", f.claude), map[string]any{"status": "missing", "plan": "max"})

	for _, expiry := range []string{"0", "1"} {
		f.claudeLogin(map[string]string{"expiresAt": expiry})
		eq(t, Identity("claude", f.claude)["status"], "expired")
	}
	for _, expiry := range []string{"true", `"4102444800000"`, "null", "-1", "1" + strings.Repeat("0", 100), "NaN"} {
		f.claudeLogin(map[string]string{"expiresAt": expiry})
		eq(t, Identity("claude", f.claude), map[string]any{"status": "missing"})
	}

	f.claudeLogin(map[string]string{"subscriptionType": "null"})
	eq(t, Identity("claude", f.claude), map[string]any{"status": "connected"})
	// The token never comes back, even in identity fields.
	f.claudeLogin(map[string]string{"subscriptionType": strconv.Quote(secret), "accountId": strconv.Quote(secret)})
	eq(t, Identity("claude", f.claude), map[string]any{"status": "connected"})
}

func TestIdentityCodex(t *testing.T) {
	f := newFolder(t)
	f.codexLogin(map[string]string{"id_token": strconv.Quote(jwt(`{"exp": 4102444800}`))})
	eq(t, Identity("codex", f.codex), map[string]any{"status": "connected"})
	f.codexLogin(map[string]string{"id_token": strconv.Quote(jwt(`{"exp": 4102444800, "https://api.openai.com/auth": {"chatgpt_plan_type": "` + secret + `", "chatgpt_account_id": "` + secret + `"}}`))})
	eq(t, Identity("codex", f.codex), map[string]any{"status": "connected"})

	f.codexLogin(nil)
	got := Identity("codex", f.codex)
	eq(t, got, map[string]any{"status": "connected", "plan": "plus", "account_id": "account-synthetic"})
	assertNoSecret(t, got)
	access := jwt(`{"exp": 4102444800, "https://api.openai.com/auth": {"chatgpt_plan_type": "pro", "chatgpt_account_id": "access-account"}}`)
	f.codexLogin(map[string]string{"id_token": "null", "access_token": strconv.Quote(access)})
	eq(t, Identity("codex", f.codex), map[string]any{"status": "connected", "plan": "pro", "account_id": "access-account"})

	// Expiry follows the access token, not an older identity token.
	for _, c := range []struct{ id, access, want string }{
		{`{"exp": 1}`, `{"exp": 4102444800}`, "connected"},
		{`{"exp": 4102444800}`, `{"exp": 1}`, "expired"},
		{defaultClaims, `{"exp": 0}`, "expired"},
		{defaultClaims, `{"exp": "not-a-time"}`, "missing"},
	} {
		f.codexLogin(map[string]string{"id_token": strconv.Quote(jwt(c.id)), "access_token": strconv.Quote(jwt(c.access))})
		eq(t, Identity("codex", f.codex)["status"], c.want)
	}

	path := filepath.Join(f.codex, "auth.json")
	f.codexLogin(map[string]string{"account_id": `"selected-account"`})
	eq(t, Identity("codex", f.codex)["account_id"], "selected-account")
	raw, _ := os.ReadFile(path)
	f.write(path, `{"auth_mode":"apikey",`+string(raw[1:]))
	eq(t, Identity("codex", f.codex)["status"], "missing")
}

func TestIdentityRefusesMalformedInput(t *testing.T) {
	f := newFolder(t)
	path := filepath.Join(f.codex, "auth.json")
	for _, raw := range []string{"[1]", "{", "\xff", `{"tokens":NaN}`, `{"tokens":{},"tokens":{}}`, `{"large":` + strings.Repeat("9", 5000) + `}`,
		strings.Repeat("[", 1500), strings.Repeat("x", MaxBytes+1)} {
		f.write(path, raw)
		eq(t, Identity("codex", f.codex), map[string]any{"status": "missing"})
	}
	for _, token := range []string{"broken", "e30.!.c3ludGhldGlj", "e30.W10.c3ludGhldGlj", "a.e30.a", jwt(`{"exp": Infinity}`), strings.Repeat("x", 65537)} {
		f.codexLogin(map[string]string{"id_token": strconv.Quote(token)})
		eq(t, Identity("codex", f.codex), map[string]any{"status": "missing"})
	}
	f.codexLogin(map[string]string{"access_token": strconv.Quote("synthetic\r\nInjected: value")})
	eq(t, Identity("codex", f.codex), map[string]any{"status": "missing"})

	// Links and special files are refused.
	f.codexLogin(nil)
	creds := filepath.Join(f.claude, ".credentials.json")
	os.Symlink(path, creds)
	eq(t, Identity("claude", f.claude), map[string]any{"status": "missing"})
	os.Remove(creds)
	if err := testMkfifo(creds); err != nil {
		t.Fatal(err)
	}
	eq(t, Identity("claude", f.claude), map[string]any{"status": "missing"})
	os.Remove(creds)

	eq(t, Identity("claude", f.claude), map[string]any{"status": "missing"})
	eq(t, Identity("other", f.root), map[string]any{"status": "missing"})
	f.write(path, `{"OPENAI_API_KEY":"synthetic-api-key"}`)
	eq(t, Identity("codex", f.codex), map[string]any{"status": "missing"})
}

// usageServer answers the usage path; hits counts every request.
func usageServer(t *testing.T, handler http.HandlerFunc) *atomic.Int32 {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	old := CodexBase
	CodexBase = srv.URL
	t.Cleanup(func() { CodexBase = old })
	return &hits
}

func codexBody() string {
	return `{"rate_limit": {"primary_window": {"used_percent": 12.5, "limit_window_seconds": 18000, "reset_at": 1790839800}}}`
}

func TestFetchMissingAndExpiredNeverLaunchOrConnect(t *testing.T) {
	f := newFolder(t)
	hits := usageServer(t, func(http.ResponseWriter, *http.Request) {})
	marker := filepath.Join(f.root, "launched")
	script := f.script("touch " + marker + "\n")
	eq(t, FetchUsage("claude", f.claude, script, 8)["usage_status"], "unavailable")
	f.claudeLogin(map[string]string{"expiresAt": "1"})
	eq(t, FetchUsage("claude", f.claude, script, 8)["usage_status"], "expired")
	f.codexLogin(map[string]string{"access_token": strconv.Quote(jwt(`{"exp": 1}`))})
	eq(t, FetchUsage("codex", f.codex, "unused", 8)["usage_status"], "expired")
	if _, err := os.Stat(marker); err == nil || hits.Load() != 0 {
		t.Error("launched or connected")
	}
}

func TestFetchClaudeArgvEnvAndOutput(t *testing.T) {
	f := newFolder(t)
	f.claudeLogin(nil)
	names := []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "AUTH_TOKEN", "ANTHROPIC_BASE_URL", "BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN"}
	checks := `[ "$#" = 2 ] && [ "$1" = -p ] && [ "$2" = /usage ] || exit 3
[ "$CLAUDE_CONFIG_DIR" = "$(pwd -P)" ] || exit 4
`
	for _, name := range names {
		t.Setenv(name, secret)
		checks += `[ -z "${` + name + `+x}" ] || exit 5` + "\n"
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/previous")
	got := FetchUsage("claude", f.claude, f.script(checks+"echo 'Current session: 25% used'\n"), 8)
	eq(t, got["quotas"], []any{map[string]any{"window": "5h", "left_pct": 75}})
	eq(t, os.Getenv("CLAUDE_CONFIG_DIR"), "/previous")
	eq(t, FetchUsage("claude", f.claude, f.script("echo 'Unrecognized synthetic output'\n"), 8)["usage_status"], "unavailable")

	// A failing CLI or stderr never leaks, and a missing binary is unavailable.
	for _, c := range []struct{ body, status string }{
		{"echo 'Current session: 0% used'\necho " + secret + " >&2\nexit 1\n", "unavailable"},
		{"echo '401 " + secret + "' >&2\nexit 1\n", "expired"},
	} {
		got := FetchUsage("claude", f.claude, f.script(c.body), 8)
		eq(t, []any{got["usage_status"], got["quotas"]}, []any{c.status, []any{}})
		assertNoSecret(t, got)
	}
	eq(t, FetchUsage("claude", f.claude, filepath.Join(f.root, "absent"), 8)["usage_status"], "unavailable")
}

func TestFetchClaudeOutputBoundedAndGroupKilled(t *testing.T) {
	f := newFolder(t)
	f.claudeLogin(nil)
	for _, redirect := range []string{"", " >&2"} {
		start := time.Now()
		got := FetchUsage("claude", f.claude, f.script("yes xxxxxxxxxxxxxxxx"+redirect+"\n"), 2)
		if time.Since(start) > 3*time.Second || got["usage_status"] != "unavailable" {
			t.Errorf("unbounded output: %v after %v", got["usage_status"], time.Since(start))
		}
	}

	pidFile := filepath.Join(f.root, "synthetic-child.pid")
	script := f.script("(trap '' TERM; while :; do sleep 1; done) &\necho $! > " + pidFile + "\nwhile :; do sleep 1; done\n")
	start := time.Now()
	got := FetchUsage("claude", f.claude, script, 0.3)
	if got["usage_status"] != "unavailable" || time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("timeout: %v after %v", got["usage_status"], time.Since(start))
	}
	raw, _ := os.ReadFile(pidFile)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	defer testKill(pid)
	for range 50 {
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return
		}
		if _, rest, _ := strings.Cut(string(stat), ") "); strings.HasPrefix(rest, "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("synthetic descendant survived the process group timeout")
}

func TestFetchCodexPathHeadersAndRedirects(t *testing.T) {
	f := newFolder(t)
	f.codexLogin(nil)
	var seen http.Header
	var path, method string
	usageServer(t, func(w http.ResponseWriter, r *http.Request) {
		seen, path, method = r.Header, r.URL.Path, r.Method
		io.WriteString(w, codexBody())
	})
	got := FetchUsage("codex", f.codex, "unused", 3)
	eq(t, []any{method, path}, []any{"GET", "/backend-api/wham/usage"})
	eq(t, []any{seen.Get("Authorization"), seen.Get("Accept"), seen.Get("ChatGPT-Account-Id"), seen.Get("Accept-Encoding"), seen.Get("User-Agent")},
		[]any{"Bearer " + secret, "application/json", "account-synthetic", "identity", ""})
	eq(t, got["usage_status"], "ok")
	assertNoSecret(t, got)

	// An unknown account omits the header.
	f.codexLogin(map[string]string{"id_token": strconv.Quote(jwt(`{"exp": 4102444800}`))})
	eq(t, FetchUsage("codex", f.codex, "unused", 3)["usage_status"], "ok")
	if _, ok := seen["Chatgpt-Account-Id"]; ok {
		t.Error("account header sent without an account")
	}

	f.codexLogin(nil)
	for _, status := range []int{301, 302, 303, 307, 308, 401, 403, 429, 500} {
		var followed atomic.Bool
		hits := usageServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/backend-api/wham/usage" {
				followed.Store(true)
			}
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(status)
			io.WriteString(w, `{"error":"`+secret+`"}`)
		})
		got := FetchUsage("codex", f.codex, "unused", 3)
		want := "unavailable"
		if status == 401 {
			want = "expired"
		}
		eq(t, []any{got["usage_status"], got["quotas"]}, []any{want, []any{}})
		if followed.Load() || hits.Load() != 1 {
			t.Errorf("status %d: followed a redirect", status)
		}
		assertNoSecret(t, got)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type closeBody struct {
	io.Reader
	closed *bool
}

func (b closeBody) Close() error { *b.closed = true; return nil }

func TestFetchCodexBodyBoundsAndErrors(t *testing.T) {
	f := newFolder(t)
	f.codexLogin(nil)
	old := Client
	t.Cleanup(func() { Client = old })
	var closed bool
	answer := func(body string, length int64) {
		closed = false
		Client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: closeBody{strings.NewReader(body), &closed}, ContentLength: length, Request: r}, nil
		})}
	}
	for _, body := range []string{"{", "\xff", `{"rate_limit":NaN}`, strings.Repeat("[", 1500), strings.Repeat("x", MaxBytes+1)} {
		answer(body, -1)
		eq(t, FetchUsage("codex", f.codex, "unused", 8)["usage_status"], "unavailable")
		if !closed {
			t.Error("body left open")
		}
	}
	for _, length := range []int64{MaxBytes + 1, 512} {
		answer(codexBody(), length)
		eq(t, FetchUsage("codex", f.codex, "unused", 8)["usage_status"], "unavailable")
	}
	answer(codexBody(), int64(len(codexBody())))
	eq(t, FetchUsage("codex", f.codex, "unused", 8)["usage_status"], "ok")

	for _, err := range []error{errors.New(secret), &timeoutError{}} {
		Client = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { return nil, err })}
		got := FetchUsage("codex", f.codex, "unused", 8)
		eq(t, got["usage_status"], "unavailable")
		assertNoSecret(t, got)
	}

	// The whole read shares one deadline.
	Client = old
	release := make(chan struct{})
	defer close(release)
	usageServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	start := time.Now()
	eq(t, FetchUsage("codex", f.codex, "unused", 0.5)["usage_status"], "unavailable")
	if time.Since(start) > 2*time.Second {
		t.Error("read deadline not applied")
	}
}

type timeoutError struct{}

func (*timeoutError) Error() string   { return "timeout " + secret }
func (*timeoutError) Timeout() bool   { return true }
func (*timeoutError) Temporary() bool { return true }

func TestFetchInvalidTimeoutDoesNoIO(t *testing.T) {
	f := newFolder(t)
	f.codexLogin(nil)
	hits := usageServer(t, func(http.ResponseWriter, *http.Request) {})
	for _, timeout := range []float64{0, -1, 61, math.NaN(), math.Inf(1)} {
		eq(t, FetchUsage("codex", f.codex, "unused", timeout)["usage_status"], "unavailable")
	}
	if hits.Load() != 0 {
		t.Error("connected with an invalid timeout")
	}
}

func TestJSONHelpersRoundTrip(t *testing.T) {
	// The jwt helper above must produce what Python's would.
	v, err := StrictJSON([]byte(`{"a": 1}`))
	if err != nil || json.Number("1") != v.(map[string]any)["a"] {
		t.Fatal(v, err)
	}
}
