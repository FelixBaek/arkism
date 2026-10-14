package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// A local HTTP server stands in for Google and
// the host login lives in a temporary home. Pure validation is compared with goldens recorded
// from the Python module (testdata/google_goldens/google.json).

const (
	gAPIKey  = "synthetic-google-api-key"
	gAccess  = "synthetic-old-access-token"
	gRefresh = "synthetic-refresh-token"
	gRenewed = "synthetic-renewed-access-token"
	gClient  = "synthetic-client-id.apps.example.invalid"
	gSecret  = "synthetic-client-secret"
	gProject = "synthetic-project-123"
	gModel   = "gemini-synthetic-model"
	gUsage   = `{"promptTokenCount":13,"candidatesTokenCount":7,"totalTokenCount":25,"thoughtsTokenCount":5,"cachedContentTokenCount":3}`
	gRequest = `{"contents":[{"role":"user","parts":[{"text":"안녕하세요 🌱"}]}]}`
	gText    = `[{"text":"답변 🌱"}]`
)

// gPage is the test page(): one candidate with parts, an optional finish reason and usage.
func gPage(parts, finish string, usage bool, index int) string {
	candidate := fmt.Sprintf(`{"index":%d,"content":{"role":"model","parts":%s}`, index, parts)
	if finish != "" {
		candidate += `,"finishReason":"` + finish + `"`
	}
	page := `{"candidates":[` + candidate + `}]`
	if usage {
		page += `,"usageMetadata":` + gUsage
	}
	return page + "}"
}

var gFinal = gPage(gText, "STOP", true, 0)

type gClock struct {
	mu         sync.Mutex
	mono, wall float64
}

func (c *gClock) set(mono float64) {
	c.mu.Lock()
	c.mono = mono
	c.mu.Unlock()
}

func (c *gClock) add(d float64) {
	c.mu.Lock()
	c.mono += d
	c.mu.Unlock()
}

type gSent struct {
	uri    string
	header http.Header
	body   string
}

type gFake struct {
	t        *testing.T
	srv      *httptest.Server
	home     string
	folder   string
	login    string
	clock    *gClock
	mu       sync.Mutex
	handlers []http.HandlerFunc
	requests []gSent
	bodies   []*gBody
	fault    func(io.ReadCloser) io.ReadCloser // wraps the next response body
}

// gTrack records every response body so tests can check it was closed.
type gTrack struct {
	f    *gFake
	base http.RoundTripper
}

type gBody struct {
	io.ReadCloser
	closed atomic.Bool
}

func (b *gBody) Close() error {
	b.closed.Store(true)
	return b.ReadCloser.Close()
}

func (g gTrack) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := g.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	g.f.mu.Lock()
	defer g.f.mu.Unlock()
	if g.f.fault != nil {
		resp.Body, g.f.fault = g.f.fault(resp.Body), nil
	}
	body := &gBody{ReadCloser: resp.Body}
	resp.Body = body
	g.f.bodies = append(g.f.bodies, body)
	return resp, nil
}

func (f *gFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, gSent{r.URL.RequestURI(), r.Header.Clone(), string(body)})
	var handler http.HandlerFunc
	if len(f.handlers) > 0 {
		handler, f.handlers = f.handlers[0], f.handlers[1:]
	}
	f.mu.Unlock()
	if handler == nil {
		f.t.Errorf("unexpected HTTP request %s", r.URL)
		w.WriteHeader(500)
		return
	}
	handler(w, r)
}

// replies queues the answers to the next requests and forgets earlier requests.
func (f *gFake) replies(handlers ...http.HandlerFunc) {
	f.mu.Lock()
	f.handlers, f.requests = handlers, nil
	f.mu.Unlock()
}

func (f *gFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// sent is request i (negative counts from the end).
func (f *gFake) sent(i int) gSent {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 {
		i += len(f.requests)
	}
	if i < 0 || i >= len(f.requests) {
		f.t.Fatalf("no request %d of %d", i, len(f.requests))
	}
	return f.requests[i]
}

func (f *gFake) writeLogin(fields ...string) {
	values := map[string]string{"access_token": `"` + gAccess + `"`, "refresh_token": `"` + gRefresh + `"`, "expiry_date": "4600000"}
	order := []string{"access_token", "refresh_token", "expiry_date"}
	for i := 0; i < len(fields); i += 2 {
		if _, ok := values[fields[i]]; !ok {
			order = append(order, fields[i])
		}
		values[fields[i]] = fields[i+1]
	}
	var pairs []string
	for _, key := range order {
		pairs = append(pairs, `"`+key+`":`+values[key])
	}
	if err := os.WriteFile(f.login, []byte("{"+strings.Join(pairs, ",")+"}"), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *gFake) api() { host.APIMode["gemini"] = true }

func (f *gFake) oauth() { delete(host.APIMode, "gemini") }

func (f *gFake) writeKeys(rows string, mode os.FileMode) {
	if err := os.WriteFile(host.KeysFile, []byte(`{"keys":`+rows+`}`), mode); err != nil {
		f.t.Fatal(err)
	}
	os.Chmod(host.KeysFile, mode)
}

func gSetup(t *testing.T) *gFake {
	home := t.TempDir()
	f := &gFake{t: t, home: home, folder: filepath.Join(home, ".gemini"), clock: &gClock{mono: 100, wall: 1000}}
	f.login = filepath.Join(f.folder, "oauth_creds.json")
	if err := os.Mkdir(f.folder, 0o700); err != nil {
		t.Fatal(err)
	}
	f.writeLogin()
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)

	saved := []any{googleAPI, googleAssist, googleOAuth, googleOAuthClientFile, googleMaxInput, googleMaxOutput,
		googleDeadline, googleMono, googleWall, host.KeysFile, host.Home, MaxFrameBytes}
	savedMode, savedClient := host.APIMode, googleClient
	googleClient = &http.Client{Transport: gTrack{f, savedClient.Transport}, CheckRedirect: savedClient.CheckRedirect}
	host.APIMode = map[string]bool{}
	googleAPI, googleAssist, googleOAuth = f.srv.URL+"/v1beta", f.srv.URL+"/v1internal", f.srv.URL+"/token"
	googleOAuthClientFile = filepath.Join(home, "absent-google-oauth.json")
	host.KeysFile = filepath.Join(home, "provider-keys.json")
	googleMono = func() float64 { f.clock.mu.Lock(); defer f.clock.mu.Unlock(); return f.clock.mono }
	googleWall = func() float64 { f.clock.mu.Lock(); defer f.clock.mu.Unlock(); return f.clock.wall }
	gReset()
	f.writeKeys(`[{"provider":"openai","key":"synthetic-other-provider"},{"provider":"google","key":"`+gAPIKey+`","base_url":"https://untrusted.invalid"}]`, 0o600)
	for key, value := range map[string]string{"GEMINI_CLI_HOME": home, "GEMINI_API_KEY": "caller-environment-key",
		"GOOGLE_API_KEY": "other-environment-key", "HTTPS_PROXY": "http://untrusted.invalid", "HTTP_PROXY": "http://untrusted.invalid",
		"ARKISM_GOOGLE_CLIENT_ID": gClient, "ARKISM_GOOGLE_CLIENT_SECRET": gSecret, "GOOGLE_CLOUD_PROJECT": "", "GOOGLE_CLOUD_PROJECT_ID": ""} {
		t.Setenv(key, value)
	}
	t.Cleanup(func() {
		if n := len(googleSlots); n != 0 {
			t.Errorf("request leaked %d capacity slots", n)
		}
		if len(googleAuthLock) != 0 {
			t.Error("request leaked the login lock")
		}
		googleAPI, googleAssist, googleOAuth = saved[0].(string), saved[1].(string), saved[2].(string)
		googleOAuthClientFile, googleMaxInput, googleMaxOutput = saved[3].(string), saved[4].(int), saved[5].(int)
		googleDeadline, googleMono, googleWall = saved[6].(float64), saved[7].(func() float64), saved[8].(func() float64)
		host.KeysFile, host.Home, MaxFrameBytes, host.APIMode = saved[9].(string), saved[10].(string), saved[11].(int), savedMode
		googleClient = savedClient
		f.mu.Lock()
		for _, body := range f.bodies {
			if !body.closed.Load() {
				t.Error("a response body was left open")
			}
		}
		f.mu.Unlock()
		gReset()
	})
	return f
}

func gReset() {
	googleLock.Lock()
	googleSignatures, googleSigOrder = map[googleSigKey]googleSig{}, nil
	googleLock.Unlock()
	googleCache = nil
}

func gJSON(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}
}

func gSetupReply(extra string) http.HandlerFunc {
	body := `{"currentTier":{"id":"standard-tier"},"cloudaicompanionProject":"` + gProject + `"`
	if extra != "" {
		body = `{"currentTier":{"id":"standard-tier"},` + extra
	}
	return gJSON(body + "}")
}

var gRenewal = gJSON(`{"access_token":"` + gRenewed + `","expires_in":3600}`)

func gSSEBody(pages ...string) string {
	var b strings.Builder
	for _, page := range pages {
		b.WriteString("data: " + page + "\r\n\r\n")
	}
	return b.String()
}

func gSSE(pages ...string) http.HandlerFunc {
	return gStream(gSSEBody(pages...))
}

func gStream(raw string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		io.WriteString(w, raw)
	}
}

// gTrickle writes body one byte at a time, flushing each.
func gTrickle(contentType, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		for i := range len(body) {
			w.Write([]byte{body[i]})
			w.(http.Flusher).Flush()
		}
	}
}

// gWire answers with raw bytes on the hijacked connection, then closes it.
func gWire(raw string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			panic(err)
		}
		rw.WriteString(raw)
		rw.Flush()
		conn.Close()
	}
}

// gShort declares one byte more than it sends.
func gShort(contentType, body string) http.HandlerFunc {
	return gWire(fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n%s", contentType, len(body)+1, body))
}

func gWrapped(page string) string { return `{"response":` + page + `}` }

func gCollect(t *testing.T, body string, stream, count bool) ([]any, error) {
	t.Helper()
	var pages []any
	for page, err := range googleIterContent(mustParse(t, body), gModel, stream, count) {
		if err != nil {
			if page != nil {
				t.Fatal("failure carried a page")
			}
			return pages, err
		}
		pages = append(pages, page)
	}
	return pages, nil
}

func gWantPages(t *testing.T, body string, stream, count bool, want ...string) {
	t.Helper()
	got, err := gCollect(t, body, stream, count)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	bridgeSameJSON(t, got, mustParse(t, "["+strings.Join(want, ",")+"]"))
}

func gWantError(t *testing.T, err error, code string, status int, started bool) {
	t.Helper()
	gerr := wantGoogle(t, err, code, status)
	if gerr.Started != started {
		t.Fatalf("%s started=%v, want %v", code, gerr.Started, started)
	}
	for _, secret := range []string{gAPIKey, gAccess, gRefresh, gRenewed, "private-upstream-body"} {
		if strings.Contains(fmt.Sprintf("%v %#v", err, err), secret) {
			t.Fatalf("error exposes %s", secret)
		}
	}
}

func gFail(t *testing.T, body string, stream, count bool, code string, status int, started bool) []any {
	t.Helper()
	pages, err := gCollect(t, body, stream, count)
	gWantError(t, err, code, status, started)
	return pages
}

func gSentJSON(t *testing.T, s gSent) any { return mustParse(t, s.body) }

func TestGoogleGoldens(t *testing.T) {
	raw, err := os.ReadFile("testdata/google_goldens/google.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Prepare    []struct{ Body, Model, Out, Err string }
		Count      []struct{ In, Out, Err string }
		Completion []struct {
			Pages []string
			Holds []bool
			End   string
		}
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	outcome := func(v any, err error) (string, string) {
		var gerr *GoogleError
		if errors.As(err, &gerr) {
			return "", fmt.Sprintf("%s:%d:%t", gerr.Code, gerr.Status, gerr.Started)
		}
		if err != nil {
			t.Fatalf("non-Google error %v", err)
		}
		return host.MustDumps(v, host.DumpOpts{NoASCII: true}), ""
	}
	for _, c := range golden.Prepare {
		body := mustParse(t, c.Body)
		before := fmt.Sprintf("%#v", body)
		out, err := googlePrepare(body, c.Model)
		if gotOut, gotErr := outcome(out, err); gotOut != c.Out || gotErr != c.Err {
			t.Errorf("prepare %s %q\n got %s %s\nwant %s %s", c.Body, c.Model, gotOut, gotErr, c.Out, c.Err)
		}
		if fmt.Sprintf("%#v", body) != before {
			t.Errorf("prepare mutated %s", c.Body)
		}
	}
	for _, c := range golden.Count {
		in := mustParse(t, c.In).([]any)
		before := host.MustDumps(in, host.DumpOpts{})
		out, err := googleCountContents(in)
		if gotOut, gotErr := outcome(out, err); gotOut != c.Out || gotErr != c.Err {
			t.Errorf("count %s\n got %s %s\nwant %s %s", c.In, gotOut, gotErr, c.Out, c.Err)
		}
		if host.MustDumps(in, host.DumpOpts{}) != before {
			t.Errorf("count mutated %s", c.In)
		}
	}
	for _, c := range golden.Completion {
		complete, holds, end := newGoogleCompletion(), []bool{}, "ok"
		for _, text := range c.Pages {
			hold, err := complete.accept(mustParse(t, text).(host.Obj))
			if err != nil {
				end = err.Error()
				break
			}
			holds = append(holds, hold)
		}
		if end == "ok" {
			if err := complete.finish(); err != nil {
				end = err.Error()
			}
		}
		if end != c.End || fmt.Sprint(holds) != fmt.Sprint(c.Holds) {
			t.Errorf("completion %v\n got %v %s\nwant %v %s", c.Pages, holds, end, c.Holds, c.End)
		}
	}
}

func TestGoogleAPIKeyUsesOnlyStoredKeyAndFixedEndpoint(t *testing.T) {
	f := gSetup(t)
	f.api()
	os.Remove(f.login) // API mode never reads the login
	f.replies(gJSON(gFinal))
	gWantPages(t, gRequest, false, false, gFinal)
	sent := f.sent(0)
	if sent.uri != "/v1beta/models/"+gModel+":generateContent" {
		t.Fatal(sent.uri)
	}
	if sent.header.Get("Content-Type") != "application/json" || sent.header.Get("X-Goog-Api-Key") != gAPIKey ||
		sent.header.Get("Authorization") != "" || sent.header.Get("Accept-Encoding") != "" {
		t.Fatal(sent.header)
	}
	bridgeSameJSON(t, gSentJSON(t, sent), mustParse(t, gRequest))
	if f.count() != 1 {
		t.Fatal(f.count())
	}
}

func TestGoogleMissingOrInvalidAPIKeyNeverFallsBack(t *testing.T) {
	f := gSetup(t)
	f.api()
	for _, rows := range []string{`[]`, `[{"provider":"openai","key":"` + gAPIKey + `"}]`,
		`[{"provider":"google","key":"bad\nkey"}]`, `[{"provider":"google","key":null}]`} {
		f.writeKeys(rows, 0o600)
		gFail(t, gRequest, false, false, "login_unavailable", 503, false)
	}
	if f.count() != 0 {
		t.Fatal("sent a request")
	}
}

func TestGoogleUnreadableKeyStoreDoesNotFallBack(t *testing.T) {
	f := gSetup(t)
	f.api()
	f.writeKeys(`[{"provider":"google","key":"`+gAPIKey+`"}]`, 0o644) // too wide: refused
	gFail(t, gRequest, false, false, "login_unavailable", 503, false)
	os.WriteFile(host.KeysFile, []byte("not json "+gAPIKey), 0o600)
	gFail(t, gRequest, false, false, "login_unavailable", 503, false)
	if f.count() != 0 {
		t.Fatal("sent a request")
	}
}

func TestGoogleOAuthIgnoresAPIKeysAndUnwrapsResponse(t *testing.T) {
	f := gSetup(t)
	f.replies(gSetupReply(""), gJSON(`{"response":`+gFinal+`,"traceId":"trace-synthetic","private":"not-public"}`))
	want := strings.TrimSuffix(gFinal, "}") + `,"responseId":"trace-synthetic"}`
	gWantPages(t, gRequest, false, false, want)
	sent := f.sent(1)
	if sent.uri != "/v1internal:generateContent" || sent.header.Get("Authorization") != "Bearer "+gAccess ||
		sent.header.Get("X-Goog-Api-Key") != "" || sent.header.Get("Content-Type") != "application/json" {
		t.Fatal(sent.uri, sent.header)
	}
	payload := gSentJSON(t, sent).(host.Obj)
	bridgeSameJSON(t, get(payload, "request"), mustParse(t, gRequest))
	var keys []string
	for _, kv := range payload {
		keys = append(keys, kv.Key)
	}
	id, _ := get(payload, "user_prompt_id").(string)
	if strings.Join(keys, ",") != "model,project,user_prompt_id,request" || get(payload, "model") != gModel ||
		get(payload, "project") != gProject || len(id) != 36 || strings.Count(id, "-") != 4 {
		t.Fatal(payload)
	}
	if f.count() != 2 {
		t.Fatal(f.count())
	}
}

func TestGoogleDefaultLoginDirectoryIsHostHome(t *testing.T) {
	f := gSetup(t)
	t.Setenv("GEMINI_CLI_HOME", "")
	host.Home = f.home
	f.replies(gSetupReply(""))
	auth, err := googleCredentials(googleMono())
	if err != nil || auth.token != gAccess {
		t.Fatal(auth, err)
	}
}

func TestGoogleRefreshOnceCachesProjectAndNeverWritesLogin(t *testing.T) {
	f := gSetup(t)
	f.writeLogin("expiry_date", "999000")
	os.WriteFile(filepath.Join(f.folder, "settings.json"), []byte(`{"selectedAuthType":"oauth-personal"}`), 0o600)
	before := gSnapshot(t, f.folder)
	f.replies(gRenewal, gSetupReply(""))
	first, err := googleCredentials(googleMono())
	if err != nil {
		t.Fatal(err)
	}
	second, err := googleCredentials(googleMono())
	if err != nil || second != first || first.token != gRenewed || first.project != gProject {
		t.Fatal(first, second, err)
	}
	if fmt.Sprint(gSorted(first.secrets)) != fmt.Sprint(gSorted([]string{gAccess, gRefresh, gRenewed})) {
		t.Fatal(first.secrets)
	}
	refresh := f.sent(0)
	form, err := url.ParseQuery(refresh.body)
	if err != nil || refresh.uri != "/token" || refresh.header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Fatal(refresh, err)
	}
	if !strings.HasPrefix(refresh.body, "client_id=") || len(form) != 4 || form.Get("client_id") != gClient ||
		form.Get("client_secret") != gSecret || form.Get("refresh_token") != gRefresh || form.Get("grant_type") != "refresh_token" {
		t.Fatal(refresh.body)
	}
	if setup := f.sent(1); setup.uri != "/v1internal:loadCodeAssist" || setup.header.Get("Authorization") != "Bearer "+gRenewed {
		t.Fatal(setup)
	}
	if f.count() != 2 || gSnapshot(t, f.folder) != before {
		t.Fatal("extra request or login folder changed")
	}
}

func gSorted(list []string) []string {
	out := append([]string{}, list...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// gSnapshot is every file's name, bytes, mode and modification time.
func gSnapshot(t *testing.T, dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, entry := range entries {
		info, _ := entry.Info()
		raw, _ := os.ReadFile(filepath.Join(dir, entry.Name()))
		fmt.Fprintf(&b, "%s %v %d %q\n", entry.Name(), info.Mode(), info.ModTime().UnixNano(), raw)
	}
	return b.String()
}

func TestGoogleConcurrentCredentialsRefreshOnce(t *testing.T) {
	f := gSetup(t)
	f.writeLogin("expiry_date", "999000")
	f.replies(gRenewal, gSetupReply(""))
	var wg sync.WaitGroup
	results := make([]*googleAuth, 16)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = googleCredentials(googleMono())
		}()
	}
	wg.Wait()
	for _, auth := range results {
		if auth == nil || auth.token != gRenewed || auth.project != gProject {
			t.Fatal(auth)
		}
	}
	if f.count() != 2 {
		t.Fatal(f.count())
	}
}

func TestGoogleLoginLockWaitUsesRemainingDeadline(t *testing.T) {
	f := gSetup(t)
	googleDeadline = 0.05
	googleAuthLock <- struct{}{}
	before, _ := os.ReadFile(f.login)
	started := time.Now()
	gFail(t, gRequest, false, false, "upstream_timeout", 504, false)
	if waited := time.Since(started); waited > 2*time.Second {
		t.Fatal("waited past the deadline", waited)
	}
	<-googleAuthLock
	after, _ := os.ReadFile(f.login)
	if f.count() != 0 || googleCache != nil || !bytes.Equal(before, after) {
		t.Fatal("login touched")
	}
	googleDeadline = 600
	f.api()
	f.replies(gJSON(gFinal))
	gWantPages(t, gRequest, false, false, gFinal)
}

func TestGoogleLoginLockAcquiredAfterDeadlineIsReleasedWithoutHTTP(t *testing.T) {
	f := gSetup(t)
	googleAuthLock <- struct{}{}
	go func() {
		time.Sleep(20 * time.Millisecond)
		f.clock.set(701)
		<-googleAuthLock
	}()
	gFail(t, gRequest, false, false, "upstream_timeout", 504, false)
	if f.count() != 0 || googleCache != nil {
		t.Fatal("login continued after the deadline")
	}
}

func TestGoogleDirectCredentialsStartTheirOwnDeadline(t *testing.T) {
	f := gSetup(t)
	googleAuthLock <- struct{}{}
	defer func() { <-googleAuthLock }()
	f.clock.set(100 + 600 - 0.05)
	_, err := googleCredentials(100)
	gWantError(t, err, "upstream_timeout", 504, false)
	if f.count() != 0 {
		t.Fatal("sent a request")
	}
}

func TestGoogleCacheIsBoundedAndLoginChangesInvalidateIt(t *testing.T) {
	f := gSetup(t)
	f.replies(gSetupReply(""), gSetupReply(""), gSetupReply(""))
	credentials := func() *googleAuth {
		auth, err := googleCredentials(googleMono())
		if err != nil {
			t.Fatal(err)
		}
		return auth
	}
	credentials()
	f.clock.wall = 1200
	credentials()
	if f.count() != 1 {
		t.Fatal(f.count())
	}
	f.clock.wall = 1301
	credentials()
	if f.count() != 2 {
		t.Fatal(f.count())
	}
	f.writeLogin("access_token", `"`+gRenewed+`"`)
	if credentials().token != gRenewed || f.count() != 3 || f.sent(2).header.Get("Authorization") != "Bearer "+gRenewed {
		t.Fatal("changed login reused the cache")
	}
}

func TestGoogleProjectDiscoveryAndEnvironmentFallback(t *testing.T) {
	f := gSetup(t)
	for _, variable := range []string{"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_PROJECT_ID"} {
		t.Setenv(variable, gProject)
		googleCache = nil
		f.replies(gJSON(`{"currentTier":{"id":"standard-tier"}}`))
		auth, err := googleCredentials(googleMono())
		if err != nil || auth.project != gProject {
			t.Fatal(auth, err)
		}
		bridgeSameJSON(t, gSentJSON(t, f.sent(0)), mustParse(t, `{"metadata":{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"},"cloudaicompanionProject":"`+gProject+`"}`))
		t.Setenv(variable, "")
	}
	googleCache = nil
	f.replies(gSetupReply(`"cloudaicompanionProject":"discovered-project-456"`))
	auth, err := googleCredentials(googleMono())
	if err != nil || auth.project != "discovered-project-456" {
		t.Fatal(auth, err)
	}
	if payload := gSentJSON(t, f.sent(0)).(host.Obj); len(payload) != 1 || payload[0].Key != "metadata" {
		t.Fatal(payload)
	}
}

func TestGoogleProjectAndLoginHomeChangesDoNotShareCache(t *testing.T) {
	f := gSetup(t)
	f.replies(gSetupReply(""), gJSON(`{"currentTier":{"id":"standard-tier"}}`), gSetupReply(""))
	if _, err := googleCredentials(googleMono()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_CLOUD_PROJECT", "other-project-456")
	if auth, err := googleCredentials(googleMono()); err != nil || auth.project != "other-project-456" {
		t.Fatal(auth, err)
	}
	if get(gSentJSON(t, f.sent(1)), "cloudaicompanionProject") != "other-project-456" {
		t.Fatal(f.sent(1).body)
	}
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	other := filepath.Join(f.home, "other-home", ".gemini")
	os.MkdirAll(other, 0o700)
	raw, _ := os.ReadFile(f.login)
	os.WriteFile(filepath.Join(other, "oauth_creds.json"), raw, 0o600)
	t.Setenv("GEMINI_CLI_HOME", filepath.Dir(other))
	if auth, err := googleCredentials(googleMono()); err != nil || auth.project != gProject {
		t.Fatal(auth, err)
	}
	if f.count() != 3 {
		t.Fatal(f.count())
	}
}

func TestGoogleMissingTierOrProjectDoesNotOnboard(t *testing.T) {
	f := gSetup(t)
	for _, info := range []string{`{}`, `{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`, `{"currentTier":{"id":"standard-tier"}}`,
		`{"cloudaicompanionProject":"` + gProject + `"}`, `{"currentTier":{},"cloudaicompanionProject":"../unsafe"}`} {
		f.replies(gJSON(info))
		_, err := googleCredentials(googleMono())
		gWantError(t, err, "google_setup_required", 503, false)
		if f.count() != 1 || f.sent(0).uri != "/v1internal:loadCodeAssist" || googleCache != nil {
			t.Fatal(info)
		}
		if payload := gSentJSON(t, f.sent(0)).(host.Obj); len(payload) != 1 {
			t.Fatal(payload)
		}
	}
}

func TestGoogleCorruptAndUnsupportedLoginsFailBeforeHTTP(t *testing.T) {
	f := gSetup(t)
	for _, raw := range []string{"not-json", "[]", "null", "\xff", `{"access_token":"a","access_token":"b"}`,
		`{"access_token":"a","expiry_date":NaN}`, `{"type":"service_account","private_key":"private-upstream-body"}`,
		`{"api_key":"synthetic-google-api-key"}`} {
		os.WriteFile(f.login, []byte(raw), 0o600)
		gFail(t, gRequest, false, false, "login_unavailable", 503, false)
	}
	if f.count() != 0 {
		t.Fatal("sent a request")
	}
}

func TestGoogleMissingSymlinkOrOversizeLoginIsSafe(t *testing.T) {
	f := gSetup(t)
	os.Remove(f.login)
	gFail(t, gRequest, false, false, "login_unavailable", 503, false)
	other := filepath.Join(f.folder, "other.json")
	os.WriteFile(other, []byte("{}"), 0o600)
	if err := os.Symlink(other, f.login); err == nil {
		gFail(t, gRequest, false, false, "login_unavailable", 503, false)
		os.Remove(f.login)
	}
	os.WriteFile(f.login, bytes.Repeat([]byte(" "), 512*1024+1), 0o600)
	gFail(t, gRequest, false, false, "login_unavailable", 503, false)
	if f.count() != 0 {
		t.Fatal("sent a request")
	}
}

func TestGoogleOAuthClientFromFileOrClearFailure(t *testing.T) {
	f := gSetup(t)
	f.writeLogin("expiry_date", "999000")
	t.Setenv("ARKISM_GOOGLE_CLIENT_ID", "")
	t.Setenv("ARKISM_GOOGLE_CLIENT_SECRET", "")
	gFail(t, gRequest, false, false, "google_oauth_client_missing", 503, false)
	if f.count() != 0 {
		t.Fatal("sent a request")
	}
	os.WriteFile(googleOAuthClientFile, []byte(`{"client_id":"file-client","client_secret":"file-secret"}`), 0o600)
	f.replies(gRenewal, gSetupReply(""))
	if auth, err := googleCredentials(googleMono()); err != nil || auth.token != gRenewed {
		t.Fatal(auth, err)
	}
	form, _ := url.ParseQuery(f.sent(0).body)
	if form.Get("client_id") != "file-client" || form.Get("client_secret") != "file-secret" {
		t.Fatal(form)
	}
}

func TestGoogleBadExpiryOrUnrefreshableLoginFails(t *testing.T) {
	f := gSetup(t)
	for _, fields := range [][]string{{"expiry_date", "true"}, {"expiry_date", `"4600000"`}, {"expiry_date", "-1"},
		{"expiry_date", "1e999"}, {"access_token", `"bad\ntoken"`}, {"expiry_date", "999000", "refresh_token", "null"}} {
		f.writeLogin(fields...)
		gFail(t, gRequest, false, false, "login_unavailable", 503, false)
	}
	if f.count() != 0 {
		t.Fatal("sent a request")
	}
}

func TestGoogleInvalidRefreshResponseStopsLogin(t *testing.T) {
	f := gSetup(t)
	f.writeLogin("expiry_date", "999000")
	for _, fields := range []string{`{}`, `{"access_token":"` + gRenewed + `","expires_in":true}`,
		`{"access_token":"` + gRenewed + `","expires_in":60}`, `{"access_token":"` + gRenewed + `","expires_in":86401}`,
		`{"access_token":"bad\ntoken","expires_in":3600}`, `{"access_token":"` + gRenewed + `","expires_in":NaN}`} {
		f.replies(gJSON(fields))
		gFail(t, gRequest, false, false, "login_unavailable", 503, false)
		if f.count() != 1 || f.sent(0).uri != "/token" {
			t.Fatal(fields)
		}
	}
}

func TestGoogleLoginHTTPAndInvalidJSONErrorsAreNotRetried(t *testing.T) {
	f := gSetup(t)
	failing := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, gRefresh+" private-upstream-body")
	}
	for _, reply := range []http.HandlerFunc{gJSON("private-upstream-body"), gJSON("[]"),
		gJSON(strings.Repeat("x", 1<<20+1)), failing} {
		f.replies(reply)
		gFail(t, gRequest, false, false, "login_unavailable", 503, false)
		if f.count() != 1 {
			t.Fatal(f.count())
		}
	}
	f.writeLogin("expiry_date", "999000")
	before, _ := os.ReadFile(f.login)
	f.replies(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(429) })
	gFail(t, gRequest, false, false, "login_unavailable", 503, false)
	after, _ := os.ReadFile(f.login)
	if f.count() != 1 || !bytes.Equal(before, after) {
		t.Fatal("retried or changed the login")
	}
}

// gSlow writes body after moving the clock by step, simulating a read that takes that long.
func gSlow(f *gFake, body string, step float64) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		for i := range len(body) {
			if i == len(body)/2 {
				f.clock.add(step)
			}
			w.Write([]byte{body[i]})
			w.(http.Flusher).Flush()
		}
	}
}

func TestGoogleLoginReadsExpireAndNeverGenerate(t *testing.T) {
	f := gSetup(t)
	f.writeLogin("expiry_date", "999000")
	before, _ := os.ReadFile(f.login)
	googleDeadline = 1
	for _, stage := range []string{"refresh", "project"} {
		googleCache = nil
		f.clock.set(100)
		if stage == "refresh" {
			f.replies(gSlow(f, `{"access_token":"`+gRenewed+`","expires_in":3600}`, 2))
		} else {
			f.replies(gRenewal, gSlow(f, `{"currentTier":{"id":"standard-tier"},"cloudaicompanionProject":"`+gProject+`"}`, 2))
		}
		gFail(t, gRequest, false, false, "upstream_timeout", 504, false)
		want := map[string]int{"refresh": 1, "project": 2}[stage]
		after, _ := os.ReadFile(f.login)
		if f.count() != want || googleCache != nil || !bytes.Equal(before, after) {
			t.Fatal(stage)
		}
	}
}

func TestGoogleRefreshAndProjectShareOneDeadline(t *testing.T) {
	f := gSetup(t)
	f.writeLogin("expiry_date", "999000")
	googleDeadline = 1
	f.replies(gSlow(f, `{"access_token":"`+gRenewed+`","expires_in":3600}`, 0.8),
		gSlow(f, `{"currentTier":{"id":"standard-tier"},"cloudaicompanionProject":"`+gProject+`"}`, 0.4))
	gFail(t, gRequest, false, false, "upstream_timeout", 504, false)
	if f.count() != 2 || f.sent(1).uri != "/v1internal:loadCodeAssist" || googleCache != nil {
		t.Fatal(f.count())
	}
}

func TestGoogleLoginShortBodyIsFixedAndNotRetried(t *testing.T) {
	f := gSetup(t)
	for _, stage := range []string{"refresh", "project"} {
		googleCache = nil
		value := `{"currentTier":{"id":"free-tier"},"cloudaicompanionProject":"` + gProject + `"}`
		uri := "/v1internal:loadCodeAssist"
		if stage == "refresh" {
			f.writeLogin("expiry_date", "999000")
			value, uri = `{"access_token":"`+gRenewed+`","expires_in":3600}`, "/token"
		} else {
			f.writeLogin()
		}
		f.replies(gShort("application/json", value))
		gFail(t, gRequest, false, false, "login_unavailable", 503, false)
		if f.count() != 1 || f.sent(0).uri != uri || googleCache != nil {
			t.Fatal(stage)
		}
	}
}

func TestGoogleLoginMalformedHTTPIsFixedAndNotRetried(t *testing.T) {
	f := gSetup(t)
	for _, stage := range []string{"refresh", "project"} {
		for _, raw := range []string{"", "HTTP/1.1 abc " + gRefresh + " private-upstream-body\r\n\r\n"} {
			googleCache = nil
			if stage == "refresh" {
				f.writeLogin("expiry_date", "999000")
			} else {
				f.writeLogin()
			}
			f.replies(gWire(raw))
			gFail(t, gRequest, false, false, "login_unavailable", 503, false)
			if f.count() != 1 {
				t.Fatal(stage, f.count())
			}
		}
	}
}

func TestGoogleNativeOptionsAndUserJSONArePreserved(t *testing.T) {
	f := gSetup(t)
	args := `{"top_p":0.9,"function_call":{"any_of":[1,true,"한글"]}}`
	body := `{"contents":[{"role":"user","parts":[{"text":"안녕하세요 🌱"}]},` +
		`{"role":"model","parts":[{"functionCall":{"id":"call-a","name":"lookup","args":` + args + `},"thoughtSignature":"synthetic-thought-signature=="}]},` +
		`{"role":"user","parts":[{"functionResponse":{"id":"call-a","name":"lookup","response":` + args + `}}]}],` +
		`"systemInstruction":{"parts":[{"text":"규칙"}]},"tools":[{"functionDeclarations":[{"name":"lookup","parametersJsonSchema":{"type":"object","properties":{"top_p":{"type":"number"}}}}]}],` +
		`"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["lookup"]}},` +
		`"generationConfig":{"temperature":1.7,"topP":0.91,"topK":23,"maxOutputTokens":123,"candidateCount":2,"responseMimeType":"application/json","responseSchema":{"type":"OBJECT"},"thinkingConfig":{"thinkingBudget":1024,"includeThoughts":true},"stopSequences":["END"]},` +
		`"safetySettings":[{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_NONE"}],"cachedContent":"cachedContents/synthetic","labels":{"purpose":"test"}}`
	result := `{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call-b","name":"lookup","args":` + args +
		`},"thoughtSignature":"synthetic-thought-signature=="}]},"finishReason":"STOP","safetyRatings":[{"category":"HARM_CATEGORY_HATE_SPEECH","probability":"NEGLIGIBLE"}]}],` +
		`"usageMetadata":` + gUsage + `,"modelVersion":"synthetic-version"}`
	for _, oauth := range []bool{false, true} {
		if oauth {
			f.oauth()
			f.replies(gSetupReply(""), gJSON(gWrapped(result)))
		} else {
			f.api()
			f.replies(gJSON(result))
		}
		gWantPages(t, body, false, false, result)
		sent := gSentJSON(t, f.sent(-1))
		if oauth {
			sent = get(sent, "request")
		}
		if host.MustDumps(sent, host.DumpOpts{NoASCII: true}) != host.MustDumps(mustParse(t, body), host.DumpOpts{NoASCII: true}) {
			t.Fatal("native body changed on the wire")
		}
	}
}

func TestGoogleInvalidRequestAndModelFailLocally(t *testing.T) {
	f := gSetup(t)
	for _, body := range []string{"null", "[]", "{}", `{"contents":[]}`, `{"contents":[{"parts":[null]}]}`,
		`{"contents":[{"role":"user","parts":[{"text":"x"}]}],"callerAuthorization":"` + gAPIKey + `"}`} {
		gFail(t, body, false, false, "unsupported_or_invalid_request", 400, false)
	}
	for _, model := range []string{"", "../escape", "models/gemini", "gemini?key=caller", "gemini\n", strings.Repeat("x", 81)} {
		_, err := googlePrepare(mustParse(t, gRequest), model)
		gWantError(t, err, "invalid_request", 400, false)
		for _, err := range googleIterContent(mustParse(t, gRequest), model, false, false) {
			gWantError(t, err, "invalid_request", 400, false)
		}
	}
	if f.count() != 0 {
		t.Fatal("sent a request")
	}
}

func TestGoogleCountTokensNeverGenerates(t *testing.T) {
	f := gSetup(t)
	result := `{"totalTokens":12345,"cachedContentTokenCount":12}`
	for _, oauth := range []bool{false, true} {
		if oauth {
			f.oauth()
			f.replies(gSetupReply(""), gJSON(result))
		} else {
			f.api()
			f.replies(gJSON(result))
		}
		gWantPages(t, gRequest, true, true, result)
		sent := f.sent(-1)
		if oauth {
			if sent.uri != "/v1internal:countTokens" {
				t.Fatal(sent.uri)
			}
			bridgeSameJSON(t, gSentJSON(t, sent), mustParse(t, `{"request":{"model":"models/`+gModel+`","contents":[{"role":"user","parts":[{"text":"안녕하세요 🌱"}]}]}}`))
		} else {
			if sent.uri != "/v1beta/models/"+gModel+":countTokens" {
				t.Fatal(sent.uri)
			}
			bridgeSameJSON(t, gSentJSON(t, sent), mustParse(t, gRequest))
		}
	}
}

// gGate serves an SSE body, then holds the connection open (no EOF) until release is closed.
func gGate(body string, release chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		io.WriteString(w, body)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
}

// gBlocked checks next stays blocked while the server holds EOF, then returns its page after release.
func gBlocked(t *testing.T, next func() (any, error, bool), release chan struct{}) any {
	t.Helper()
	done := make(chan any, 1)
	go func() {
		page, err, ok := next()
		if err != nil || !ok {
			t.Errorf("next: %v %v", err, ok)
		}
		done <- page
	}()
	select {
	case <-done:
		t.Fatal("page escaped before the real HTTP EOF")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	return <-done
}

func gPull(t *testing.T, body string, stream bool) (func() (any, error, bool), func()) {
	return iter.Pull2(googleIterContent(mustParse(t, body), gModel, stream, false))
}

func gThoughtParts() string {
	return `[{"text":"생각 🌱","thought":true,"thoughtSignature":"text-signature=="},{"text":"","thought":true},` +
		`{"thought":true,"thoughtSignature":"empty-signature=="},{"text":"보통 텍스트","thought":false},{"text":"그대로"},` +
		`{"functionCall":{"name":"lookup","args":{"thought":true,"text":"사용자 JSON"}},"text":"도구 텍스트","thought":true,"thoughtSignature":"call-signature=="},` +
		`{"functionResponse":{"name":"lookup","response":{"thought":true,"value":"결과"}},"thought":true,"thoughtSignature":"response-signature=="},` +
		`{"inlineData":{"mimeType":"image/png","data":"c3ludGhldGlj"},"thought":true,"thoughtSignature":"inline-signature=="},` +
		`{"fileData":{"mimeType":"image/png","fileUri":"gs://synthetic/image.png"},"thought":true,"thoughtSignature":"file-signature=="}]`
}

func TestGoogleOAuthCountThoughtPartsPreserveInput(t *testing.T) {
	f := gSetup(t)
	user := `{"role":"user","parts":[{"text":"안녕하세요 🌱"}]}`
	body := `{"contents":[{"role":"model","parts":` + gThoughtParts() + `},` + user + `]}`
	converted := `[{"role":"model","parts":[{"text":"생각 🌱\n[Thought: true]","thoughtSignature":"text-signature=="},{"text":"[Thought: true]"},` +
		`{"text":"[Thought: true]","thoughtSignature":"empty-signature=="},{"text":"보통 텍스트","thought":false},{"text":"그대로"},` +
		`{"functionCall":{"name":"lookup","args":{"thought":true,"text":"사용자 JSON"}},"text":"도구 텍스트","thoughtSignature":"call-signature=="},` +
		`{"functionResponse":{"name":"lookup","response":{"thought":true,"value":"결과"}},"thoughtSignature":"response-signature=="},` +
		`{"inlineData":{"mimeType":"image/png","data":"c3ludGhldGlj"},"thoughtSignature":"inline-signature=="},` +
		`{"fileData":{"mimeType":"image/png","fileUri":"gs://synthetic/image.png"},"thoughtSignature":"file-signature=="}]},` + user + `]`
	dump := func(v any) string { return host.MustDumps(v, host.DumpOpts{NoASCII: true}) }
	for _, c := range []struct{ oauth, count bool }{{true, true}, {false, true}, {true, false}} {
		googleCache = nil
		result, wire := `{"totalTokens":137}`, `{"totalTokens":137}`
		if !c.count {
			result, wire = gFinal, gWrapped(gFinal)
		}
		if c.oauth {
			f.oauth()
			f.replies(gSetupReply(""), gJSON(wire))
		} else {
			f.api()
			f.replies(gJSON(result))
		}
		input := mustParse(t, body)
		before := dump(input)
		var got []any
		for page, err := range googleIterContent(input, gModel, false, c.count) {
			if err != nil {
				t.Fatal(c, err)
			}
			got = append(got, page)
		}
		bridgeSameJSON(t, got, mustParse(t, "["+result+"]"))
		sent := f.sent(-1)
		if want := map[bool]string{true: ":countTokens", false: ":generateContent"}[c.count]; !strings.HasSuffix(sent.uri, want) {
			t.Fatal(sent.uri)
		}
		payload := gSentJSON(t, sent)
		switch {
		case c.oauth && c.count:
			bridgeSameJSON(t, payload, mustParse(t, `{"request":{"model":"models/`+gModel+`","contents":`+converted+`}}`))
		case c.oauth:
			bridgeSameJSON(t, get(payload, "request"), mustParse(t, body))
		default:
			bridgeSameJSON(t, payload, mustParse(t, body))
		}
		if dump(input) != before {
			t.Fatal("input mutated")
		}
		if f.count() != (map[bool]int{true: 2, false: 1}[c.oauth]) {
			t.Fatal(f.count())
		}
	}
}

func TestGoogleZeroTokensIsExactAndInvalidCountsAreRejected(t *testing.T) {
	f := gSetup(t)
	f.api()
	f.replies(gJSON(`{"totalTokens":0}`))
	gWantPages(t, gRequest, false, true, `{"totalTokens":0}`)
	for _, value := range []string{`{}`, `{"totalTokens":true}`, `{"totalTokens":-1}`, `{"totalTokens":1.5}`, `{"totalTokens":"3"}`, `[]`} {
		f.replies(gJSON(value))
		gFail(t, gRequest, false, true, "upstream_invalid_response", 502, true)
		if f.count() != 1 {
			t.Fatal(value)
		}
	}
}

func TestGoogleUnsupportedCountOptionsFailBeforeLogin(t *testing.T) {
	f := gSetup(t)
	os.Remove(f.login)
	body := `{"contents":[{"role":"user","parts":[{"text":"x"}]}],"generationConfig":{"temperature":0.8}}`
	gFail(t, body, false, true, "unsupported_field", 400, false)
	if f.count() != 0 {
		t.Fatal("sent a request")
	}
}

func TestGoogleSSETextIsLiveButToolsAndFinishWaitForEOF(t *testing.T) {
	f := gSetup(t)
	text := gPage(gText, "", false, 0)
	tool := gPage(`[{"functionCall":{"id":"call-1","name":"lookup","args":{"query":"한글"}},"thoughtSignature":"preserved-signature=="}]`, "", false, 0)
	final := gPage(`[]`, "STOP", true, 0)
	for _, oauth := range []bool{false, true} {
		release := make(chan struct{})
		if oauth {
			f.oauth()
			f.replies(gSetupReply(""), gGate(gSSEBody(gWrapped(text), gWrapped(tool), gWrapped(final)), release))
		} else {
			f.api()
			f.replies(gGate(gSSEBody(text, tool, final), release))
		}
		next, stop := gPull(t, gRequest, true)
		page, err, _ := next()
		if err != nil {
			t.Fatal(err)
		}
		bridgeSameJSON(t, page, mustParse(t, text))
		bridgeSameJSON(t, gBlocked(t, next, release), mustParse(t, tool))
		page, err, _ = next()
		if err != nil {
			t.Fatal(err)
		}
		bridgeSameJSON(t, page, mustParse(t, final))
		if _, err, ok := next(); ok || err != nil {
			t.Fatal("extra page", err)
		}
		stop()
		if !strings.HasSuffix(f.sent(-1).uri, ":streamGenerateContent?alt=sse") {
			t.Fatal(f.sent(-1).uri)
		}
	}
}

func TestGoogleSSETerminalOnlyResponseWaitsForEOF(t *testing.T) {
	f := gSetup(t)
	f.api()
	release := make(chan struct{})
	f.replies(gGate(gSSEBody(gFinal), release))
	next, stop := gPull(t, gRequest, true)
	defer stop()
	bridgeSameJSON(t, gBlocked(t, next, release), mustParse(t, gFinal))
}

func TestGoogleTruncatedSSENeverReleasesToolsOrSuccess(t *testing.T) {
	f := gSetup(t)
	f.api()
	text := gPage(gText, "", false, 0)
	tool := gPage(`[{"functionCall":{"name":"lookup","args":{"x":1}},"thoughtSignature":"signature=="}]`, "", false, 0)
	final := gPage(`[]`, "STOP", true, 0)
	for _, raw := range []string{gSSEBody(text, tool), gSSEBody(text, tool, gPage(`[]`, "STOP", false, 0)),
		gSSEBody(text, tool, final) + "data: {", gSSEBody(text, tool, final) + "data: {}",
		gSSEBody(text, tool, final) + "event: error\ndata: private-upstream-body\n\n"} {
		f.replies(gStream(raw))
		visible, err := gCollect(t, gRequest, true, false)
		gWantError(t, err, "upstream_invalid_response", 502, true)
		bridgeSameJSON(t, visible, mustParse(t, "["+text+"]"))
		if f.count() != 1 {
			t.Fatal(f.count())
		}
	}
}

// gReadError makes the body fail with a raw error once its bytes are read.
func gReadError(body io.ReadCloser) io.ReadCloser {
	return struct {
		io.Reader
		io.Closer
	}{io.MultiReader(body, gErrReader{}), body}
}

type gErrReader struct{}

func (gErrReader) Read([]byte) (int, error) {
	return 0, errors.New(gAccess + " private-upstream-body")
}

func TestGoogleTruncationOrReadErrorAfterFinishDoesNotPublish(t *testing.T) {
	f := gSetup(t)
	f.api()
	for _, stream := range []bool{false, true} {
		value, contentType := gFinal, "application/json"
		if stream {
			value, contentType = gSSEBody(gFinal), "text/event-stream; charset=utf-8"
		}
		// A body shorter than its Content-Length: the stream decoder reports an invalid stream.
		f.replies(gShort(contentType, value))
		visible, err := gCollect(t, gRequest, stream, false)
		gWantError(t, err, map[bool]string{false: "upstream_truncated", true: "upstream_invalid_response"}[stream], 502, true)
		if len(visible) != 0 {
			t.Fatal(visible)
		}
		f.fault = gReadError
		if stream {
			f.replies(gStream(value))
		} else {
			f.replies(gJSON(value))
		}
		visible, err = gCollect(t, gRequest, stream, false)
		gWantError(t, err, "upstream_invalid_response", 502, true)
		if len(visible) != 0 {
			t.Fatal(visible)
		}
	}
}

func TestGoogleAllCandidatesMustFinishBeforeToolsArePublic(t *testing.T) {
	f := gSetup(t)
	f.api()
	first := gPage(`[{"functionCall":{"name":"lookup","args":{}}}]`, "STOP", true, 0)
	second := gPage(gText, "", false, 1)
	f.replies(gSSE(first, second))
	visible, err := gCollect(t, gRequest, true, false)
	gWantError(t, err, "upstream_invalid_response", 502, true)
	if len(visible) != 0 {
		t.Fatal(visible)
	}
	terminal := gPage(`[]`, "STOP", true, 1)
	f.replies(gSSE(first, second, terminal))
	gWantPages(t, gRequest, true, false, first, second, terminal)
}

func TestGoogleJSONMissingFinishUsageOrStructureIsNotPublic(t *testing.T) {
	f := gSetup(t)
	f.api()
	for _, value := range []string{gPage(gText, "", true, 0), gPage(gText, "STOP", false, 0), `{}`, `[]`,
		`{"error":{"message":"` + gAPIKey + `"}}`, `{"candidates":{}}`, `{"candidates":[null]}`,
		gPage(gText, "FINISH_REASON_UNSPECIFIED", true, 0), gPage(`[{"functionCall":{"name":"lookup","args":[]}}]`, "STOP", true, 0),
		strings.Replace(gFinal, `"index":0`, `"index":true`, 1)} {
		f.replies(gJSON(value))
		gFail(t, gRequest, false, false, "upstream_invalid_response", 502, true)
		if f.count() != 1 {
			t.Fatal(value)
		}
	}
}

func TestGoogleMeasuredUsageIsPreservedAndBadNumbersRejected(t *testing.T) {
	f := gSetup(t)
	f.api()
	f.replies(gJSON(gFinal))
	pages, err := gCollect(t, gRequest, false, false)
	if err != nil {
		t.Fatal(err)
	}
	bridgeSameJSON(t, get(pages[0], "usageMetadata"), mustParse(t, gUsage))
	for _, key := range []string{"promptTokenCount", "candidatesTokenCount", "totalTokenCount", "thoughtsTokenCount", "cachedContentTokenCount"} {
		for _, value := range []string{"-1", "true", "1.5", `"2"`} {
			usage := mustParse(t, gUsage).(host.Obj)
			usage.Set(key, mustParse(t, value))
			page := strings.Replace(gFinal, gUsage, host.MustDumps(usage, host.DumpOpts{Compact: true}), 1)
			f.replies(gJSON(page))
			gFail(t, gRequest, false, false, "upstream_invalid_response", 502, true)
		}
	}
}

func TestGoogleMaxTokensAndSafetyAreDistinctFinishReasons(t *testing.T) {
	f := gSetup(t)
	f.api()
	for _, reason := range []string{"STOP", "MAX_TOKENS", "SAFETY"} {
		result := gPage(`[]`, reason, true, 0)
		f.replies(gJSON(result))
		gWantPages(t, gRequest, false, false, result)
	}
}

func TestGooglePromptBlockIsValidWithoutCandidatesOrUsage(t *testing.T) {
	f := gSetup(t)
	f.api()
	result := `{"promptFeedback":{"blockReason":"SAFETY"}}`
	f.replies(gJSON(result))
	gWantPages(t, gRequest, false, false, result)
}

func TestGoogleSSEPromptBlockIsTerminalAndWaitsForEOF(t *testing.T) {
	f := gSetup(t)
	f.api()
	result := `{"promptFeedback":{"blockReason":"SAFETY"}}`
	release := make(chan struct{})
	f.replies(gGate(gSSEBody(result)+": keepalive keepalive keepalive keepalive\n\n", release))
	next, stop := gPull(t, gRequest, true)
	defer stop()
	bridgeSameJSON(t, gBlocked(t, next, release), mustParse(t, result))
}

func TestGoogleTruncatedPromptBlockOrFinalUsageIsNotPublished(t *testing.T) {
	f := gSetup(t)
	f.api()
	text := gPage(gText, "", false, 0)
	for _, c := range []struct{ pages, want []string }{
		{[]string{`{"promptFeedback":{"blockReason":"SAFETY"}}`}, nil},
		{[]string{text, `{"usageMetadata":` + gUsage + `}`}, []string{text}}} {
		f.replies(gStream(gSSEBody(c.pages...) + "data: {"))
		visible, err := gCollect(t, gRequest, true, false)
		gWantError(t, err, "upstream_invalid_response", 502, true)
		bridgeSameJSON(t, append([]any{}, visible...), mustParse(t, "["+strings.Join(c.want, ",")+"]"))
	}
}

func TestGoogleHTTPErrorIsOneAttemptAndBodyIsHidden(t *testing.T) {
	f := gSetup(t)
	f.api()
	for _, status := range []int{401, 403, 429, 500, 503} {
		f.replies(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			io.WriteString(w, gAPIKey+" private-upstream-body")
		})
		gFail(t, gRequest, false, false, fmt.Sprintf("upstream_http_%d", status), status, false)
		if f.count() != 1 {
			t.Fatal(f.count())
		}
	}
}

func TestGoogleTransportErrorIsSafeAndNeverRetries(t *testing.T) {
	f := gSetup(t)
	f.api()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	googleAPI = closed.URL + "/v1beta"
	gFail(t, gRequest, false, false, "upstream_unreachable", 502, true)
}

func TestGoogleGenerationShortBodyHoldsTerminalPartsAndNeverRetries(t *testing.T) {
	f := gSetup(t)
	terminal := gPage(`[{"functionCall":{"name":"lookup","args":{}},"thoughtSignature":"held-signature=="}]`, "STOP", true, 0)
	for _, oauth := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			googleCache = nil
			value := terminal
			if oauth {
				value = gWrapped(terminal)
			}
			contentType := "application/json"
			if stream {
				value, contentType = gSSEBody(value), "text/event-stream; charset=utf-8"
			}
			if oauth {
				f.oauth()
				f.replies(gSetupReply(""), gShort(contentType, value))
			} else {
				f.api()
				f.replies(gShort(contentType, value))
			}
			visible, err := gCollect(t, gRequest, stream, false)
			gWantError(t, err, map[bool]string{false: "upstream_truncated", true: "upstream_invalid_response"}[stream], 502, true)
			if len(visible) != 0 || f.count() != map[bool]int{true: 2, false: 1}[oauth] {
				t.Fatal(visible, f.count())
			}
			if want := map[bool]string{true: ":streamGenerateContent", false: ":generateContent"}[stream]; !strings.Contains(f.sent(-1).uri, want) {
				t.Fatal(f.sent(-1).uri)
			}
		}
	}
}

func TestGoogleGenerationMalformedHTTPIsFixedAndNotRetried(t *testing.T) {
	f := gSetup(t)
	for _, oauth := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, raw := range []string{"", "HTTP/1.1 abc " + gAccess + " private-upstream-body\r\n\r\n"} {
				googleCache = nil
				if oauth {
					f.oauth()
					f.replies(gSetupReply(""), gWire(raw))
				} else {
					f.api()
					f.replies(gWire(raw))
				}
				gFail(t, gRequest, stream, false, "upstream_unreachable", 502, true)
				if f.count() != map[bool]int{true: 2, false: 1}[oauth] {
					t.Fatal(oauth, stream, f.count())
				}
			}
		}
	}
}

func TestGoogleClientRejectsRedirectsAndProxies(t *testing.T) {
	if transport := googleClient.Transport.(*http.Transport); transport.Proxy != nil || !transport.DisableCompression {
		t.Fatal("client uses an environment proxy or transparent compression")
	}
	f := gSetup(t)
	f.api()
	stolen := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("redirect was followed") }))
	defer stolen.Close()
	for _, status := range []int{301, 302, 303, 307, 308} {
		f.replies(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", stolen.URL+"/steal")
			w.WriteHeader(status)
		})
		gFail(t, gRequest, false, false, "upstream_http_502", 502, false)
		if f.count() != 1 {
			t.Fatal(f.count())
		}
	}
}

func TestGoogleInputLimitCountsUTF8BytesBeforeCredentials(t *testing.T) {
	f := gSetup(t)
	os.Remove(f.login)
	body := `{"contents":[{"parts":[{"text":"한글"}]}]}`
	googleMaxInput = len(body)
	if _, err := googlePrepare(mustParse(t, body), gModel); err != nil {
		t.Fatal(err)
	}
	googleMaxInput = len(body) - 1
	gFail(t, body, false, false, "input_too_large", 413, false)
	if f.count() != 0 {
		t.Fatal("sent a request")
	}
}

func TestGoogleDefaultResourceLimits(t *testing.T) {
	if googleMaxInput != 8<<20 || googleMaxOutput != 16<<20 || MaxFrameBytes != 1<<20 || googleDeadline != 600 {
		t.Fatal(googleMaxInput, googleMaxOutput, MaxFrameBytes, googleDeadline)
	}
}

func TestGoogleOutputAndFrameLimitsDoNotReleaseTerminalFrames(t *testing.T) {
	f := gSetup(t)
	f.api()
	googleMaxOutput = len(gFinal)
	f.replies(gJSON(gFinal))
	gWantPages(t, gRequest, false, false, gFinal)
	googleMaxOutput = len(gFinal) - 1
	f.replies(gJSON(gFinal))
	gFail(t, gRequest, false, false, "upstream_too_large", 502, true)
	f.replies(gSSE(gFinal))
	gFail(t, gRequest, true, false, "upstream_invalid_response", 502, true)
	googleMaxOutput, MaxFrameBytes = 16<<20, 64
	f.replies(gSSE(gFinal))
	gFail(t, gRequest, true, false, "upstream_invalid_response", 502, true)
}

func TestGoogleStreamRequiresSSEContentType(t *testing.T) {
	f := gSetup(t)
	f.api()
	f.replies(gJSON(gSSEBody(gFinal)))
	gFail(t, gRequest, true, false, "upstream_invalid_response", 502, true)
}

func TestGoogleDeadlineBoundsResponseReadAndOpen(t *testing.T) {
	f := gSetup(t)
	f.api()
	f.replies(gSlow(f, gFinal, 601))
	gFail(t, gRequest, false, false, "upstream_timeout", 504, true)
	// No response head within the remaining deadline.
	googleDeadline = 0.1
	f.replies(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	gFail(t, gRequest, false, false, "upstream_timeout", 504, true)
}

func TestGoogleEightInflightStreamsRefuseNinthThenCloseFreesSlots(t *testing.T) {
	f := gSetup(t)
	f.api()
	initial, final := gPage(gText, "", false, 0), gPage(`[]`, "STOP", true, 0)
	var handlers []http.HandlerFunc
	for range 9 {
		handlers = append(handlers, gSSE(initial, final))
	}
	f.replies(handlers...)
	var stops []func()
	defer func() {
		for _, stop := range stops {
			stop()
		}
	}()
	for range 8 {
		next, stop := gPull(t, gRequest, true)
		stops = append(stops, stop)
		page, err, _ := next()
		if err != nil {
			t.Fatal(err)
		}
		bridgeSameJSON(t, page, mustParse(t, initial))
	}
	gFail(t, gRequest, true, false, "google_capacity", 503, false)
	if f.count() != 8 {
		t.Fatal(f.count())
	}
	stops[len(stops)-1]()
	stops = stops[:len(stops)-1]
	gWantPages(t, gRequest, true, false, initial, final)
	if f.count() != 9 {
		t.Fatal(f.count())
	}
}

func TestGoogleSecretMaskingAcrossSmallFragments(t *testing.T) {
	f := gSetup(t)
	for _, oauth := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			googleCache = nil
			f.writeLogin("expiry_date", "999000")
			secrets := []string{gAPIKey}
			if oauth {
				secrets = []string{gAccess, gRefresh, gRenewed}
			}
			result := gPage(`[{"text":"한글 `+strings.Join(secrets, " | ")+` 끝"}]`, "STOP", true, 0)
			wire, contentType := result, "application/json"
			if oauth {
				wire = gWrapped(result)
			}
			if stream {
				wire, contentType = gSSEBody(wire), "text/event-stream; charset=utf-8"
			}
			if oauth {
				f.oauth()
				f.replies(gRenewal, gSetupReply(""), gTrickle(contentType, wire))
			} else {
				f.api()
				f.replies(gTrickle(contentType, wire))
			}
			pages, err := gCollect(t, gRequest, stream, false)
			if err != nil {
				t.Fatal(oauth, stream, err)
			}
			masked := strings.TrimSuffix(strings.Repeat("*** | ", len(secrets)), " | ")
			parts := get(get(get(pages[0], "candidates").([]any)[0], "content"), "parts")
			bridgeSameJSON(t, parts, mustParse(t, `[{"text":"한글 `+masked+` 끝"}]`))
		}
	}
}

func TestGoogleSignatureContinuationIsBoundToOwnerModelAndExpiry(t *testing.T) {
	f := gSetup(t)
	content := `{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}},"thoughtSignature":"synthetic-native-signature=="}]}`
	result := mustParse(t, `{"candidates":[{"index":0,"content":`+content+`,"finishReason":"STOP"}],"usageMetadata":`+gUsage+`}`).(host.Obj)
	body := mustParse(t, `{"contents":[`+content+`]}`).(host.Obj)
	if err := googleRemember(result, "owner-a", "google/"+gModel); err != nil {
		t.Fatal(err)
	}
	if model, err := googleContinuationModel(body, "owner-a"); err != nil || model != "google/"+gModel {
		t.Fatal(model, err)
	}
	_, err := googleContinuationModel(body, "owner-b")
	wantGoogle(t, err, "conversation_mismatch", 409)
	if model, err := googleContinuationModel(mustParse(t, gRequest).(host.Obj), "owner-a"); model != "" || err != nil {
		t.Fatal(model, err)
	}
	f.clock.set(1001)
	_, err = googleContinuationModel(body, "owner-a")
	wantGoogle(t, err, "conversation_mismatch", 409)
}

func TestGoogleUnknownConflictingOrInvalidSignaturesFail(t *testing.T) {
	gSetup(t)
	body := func(parts string) host.Obj {
		return mustParse(t, `{"contents":[{"role":"model","parts":`+parts+`}]}`).(host.Obj)
	}
	_, err := googleContinuationModel(body(`[{"text":"x","thoughtSignature":"unknown=="}]`), "owner-a")
	wantGoogle(t, err, "conversation_mismatch", 409)
	for _, signature := range []string{`""`, "true", "7", "{}"} {
		_, err := googleContinuationModel(body(`[{"text":"x","thoughtSignature":`+signature+`}]`), "owner-a")
		wantGoogle(t, err, "invalid_signature", 400)
	}
	for _, c := range [][2]string{{"sig-one==", "google/first"}, {"sig-two==", "google/second"}} {
		page := mustParse(t, gPage(`[{"text":"x","thoughtSignature":"`+c[0]+`"}]`, "STOP", true, 0)).(host.Obj)
		if err := googleRemember(page, "owner-a", c[1]); err != nil {
			t.Fatal(err)
		}
	}
	_, err = googleContinuationModel(body(`[{"text":"x","thoughtSignature":"sig-one=="},{"text":"x","thoughtSignature":"sig-two=="}]`), "owner-a")
	wantGoogle(t, err, "conversation_mismatch", 409)
}
