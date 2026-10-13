package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/subscription"
)

// systemone and the gateway half of the health tests.

const managementSecret = "synthetic-management-private-key"

const systemoneRequest = `{"model":"cloudflare/clef-flash","state":{"text":"요청 내용","nested":[true,null]},` +
	`"questions":{"urgent":{"type":"noul","instructions":{"question":"지금 필요한가요?"}}}}`

const systemoneResult = `{"model":"clef-flash","answers":{"urgent":{"type":"noul","noul":0.8}},"usage":{"input_tokens":42}}`

func systemoneResponse() string {
	return `{"authority":"data_only_untrusted","status":200,"result":` + systemoneResult + `}`
}

type management struct {
	*fixture
	seen   []seenRequest
	status int
	ctype  string
	body   string
	extra  int
	drop   bool // close the connection without an answer
	token  string
}

func newManagement(t *testing.T) *management {
	m := &management{fixture: newFixture(t), status: 200, ctype: "application/json", body: systemoneResponse()}
	m.url = strings.Replace(m.url, "/chat/completions", "/systemone", 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, max(r.ContentLength, 0))
		r.Body.Read(raw)
		var body any
		if len(raw) > 0 {
			body = parsed(t, raw)
		}
		m.mu.Lock()
		m.seen = append(m.seen, seenRequest{r.Method, r.URL.RequestURI(), r.Header.Clone(), raw, body})
		status, ctype, data, extra, drop := m.status, m.ctype, m.body, m.extra, m.drop
		m.mu.Unlock()
		if drop {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)+extra))
		w.Header().Set("X-Private", managementSecret)
		w.Header().Set("Location", m.base+"/v1/messages")
		w.WriteHeader(status)
		w.Write([]byte(data))
	}))
	t.Cleanup(server.Close)
	swap(t, &host.BaseURL, server.URL)
	t.Setenv("ARKISM_WORKSPACE", "workspace")
	t.Setenv("ARKISM_USER", "person")
	t.Setenv("ARKISM_PROJECT", "project")
	m.token = host.RunnerTokenFile
	writeFile(t, m.token, managementSecret)
	return m
}

func (m *management) managementSeen() []seenRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]seenRequest(nil), m.seen...)
}

func (m *management) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen = nil
}

func same(t *testing.T, got any, want string) {
	t.Helper()
	w, _ := Parse(want)
	if host.MustDumps(got, host.DumpOpts{}) != host.MustDumps(w, host.DumpOpts{}) {
		t.Fatalf("got %s\nwant %s", host.MustDumps(got, host.DumpOpts{}), want)
	}
}

func TestSystemoneBodyAndScopePreservedOnlyHostAuthorizationSent(t *testing.T) {
	m := newManagement(t)
	reply := m.post(systemoneRequest, "x-goog-api-key", "caller-secret")
	if reply.status != 200 {
		t.Fatalf("status %d %s", reply.status, reply.body)
	}
	same(t, parsed(t, reply.body), systemoneResult)
	if reply.header.Get("x-private") != "" {
		t.Fatal("management header leaked")
	}
	seen := m.managementSeen()
	if seen[0].path != "/v1/routing/systemone" || seen[0].header.Get("Authorization") != "Bearer "+managementSecret {
		t.Fatalf("sent %v", seen[0])
	}
	if strings.Contains(host.MustDumps(seen[0].header, host.DumpOpts{}), "caller-") {
		t.Fatal("caller credentials forwarded")
	}
	same(t, seen[0].body, `{"scope":{"workspace_id":"workspace","project_id":"project","user_id":"person","agent_id":""},"request":`+systemoneRequest+`}`)
	if m.seenCount() != 0 || m.decisions != 0 || m.reportCount() != 0 {
		t.Fatal("systemone reached a provider, a decision or metering")
	}
}

func TestSystemoneModelsUseHostScopeAndNeverExposeServerMetadata(t *testing.T) {
	m := newManagement(t)
	m.body = `{"authority":"data_only_untrusted","items":[{"id":"cloudflare/clef-flash","name":"Clef"},{"id":"typesafe/jev-1.13.0","name":"Jev"}]}`
	reply := fetch(t, m.url+"/models")
	if reply.status != 200 {
		t.Fatalf("status %d", reply.status)
	}
	same(t, parsed(t, reply.body), `{"object":"list","data":[{"id":"cloudflare/clef-flash","object":"model"},{"id":"typesafe/jev-1.13.0","object":"model"}]}`)
	seen := m.managementSeen()
	if !strings.Contains(seen[0].path, "project_id=project") || seen[0].body != nil {
		t.Fatalf("listing request %v", seen[0])
	}
}

func TestSystemoneInvalidInputAndUnsupportedResourceNeverCallAnyServer(t *testing.T) {
	m := newManagement(t)
	for _, raw := range []string{`[]`, `{}`, `{`, `{"model":"x/y","model":"x/z","state":"hi","questions":{"q":{}}}`,
		strings.TrimSuffix(systemoneRequest, "}") + `,"stream":true}`,
		strings.Replace(systemoneRequest, "cloudflare/clef-flash", "group/test", 1),
		`{"model":"cloudflare/clef-flash","state":{},"questions":{}}`} {
		if got := m.post(raw).status; got != 400 {
			t.Fatalf("%s: %d", raw, got)
		}
	}
	original := m.url
	for _, suffix := range []string{"?key=caller-secret", "/unknown", "/models"} {
		m.url = original + suffix
		if got := m.post(systemoneRequest).status; got != 501 {
			t.Fatalf("%s: %d", suffix, got)
		}
	}
	m.url = original
	if got := m.post(systemoneRequest, "content-length", strconv.Itoa(subscription.MaxInput+1)).status; got != 413 {
		t.Fatalf("oversized: %d", got)
	}
	if len(m.managementSeen()) != 0 || m.seenCount() != 0 {
		t.Fatal("a server was called")
	}
}

func TestSystemoneMissingHostCredentialsNeverUsesCallersKey(t *testing.T) {
	m := newManagement(t)
	os.Remove(m.token)
	if got := m.post(systemoneRequest).status; got != 503 {
		t.Fatalf("status %d", got)
	}
	if len(m.managementSeen()) != 0 {
		t.Fatal("called without credentials")
	}
	writeFile(t, host.TokenFile, "owner-fallback")
	if got := m.post(systemoneRequest).status; got != 200 {
		t.Fatalf("status %d", got)
	}
	seen := m.managementSeen()
	if seen[len(seen)-1].header.Get("Authorization") != "Bearer owner-fallback" {
		t.Fatal("owner token not used")
	}
}

func TestSystemoneProviderRefusalsReturnFixedErrorsWithoutRetry(t *testing.T) {
	m := newManagement(t)
	for _, status := range []int{401, 403, 422, 429, 500, 529} {
		m.reset()
		m.body = `{"authority":"data_only_untrusted","status":` + strconv.Itoa(status) + `,"result":{"error":{"message":"PRIVATE_BODY"}}}`
		reply := m.post(systemoneRequest)
		if reply.status != status || errorMessage(t, reply.body) != "upstream_http_"+strconv.Itoa(status) ||
			strings.Contains(string(reply.body), "PRIVATE") || len(m.managementSeen()) != 1 {
			t.Fatalf("%d: %d %s", status, reply.status, reply.body)
		}
	}
}

func TestSystemoneManagementDenialsOldServerAndRedirectNeverRetry(t *testing.T) {
	m := newManagement(t)
	for _, status := range []int{400, 401, 403, 404, 409, 429, 500, 501, 503, 307} {
		m.reset()
		m.status, m.body = status, "PRIVATE_BODY"
		want := status
		if status == 404 || status == 501 {
			want = 501
		} else if status == 307 {
			want = 502
		}
		reply := m.post(systemoneRequest)
		if reply.status != want || strings.Contains(string(reply.body), "PRIVATE") || len(m.managementSeen()) != 1 || m.seenCount() != 0 {
			t.Fatalf("%d: %d %s", status, reply.status, reply.body)
		}
	}
}

func TestSystemoneBadEnvelopesTruncationAndCredentialsNeverSucceed(t *testing.T) {
	m := newManagement(t)
	with := func(key, value string) string {
		var o map[string]json.RawMessage
		json.Unmarshal([]byte(systemoneResponse()), &o)
		o[key] = json.RawMessage(value)
		out, _ := json.Marshal(o)
		return string(out)
	}
	bad := []string{`{`, `{}{}`, `{"authority":"data_only_untrusted","authority":"other"}`,
		with("authority", `"trusted"`), with("status", `true`), with("status", `201`),
		with("result", `{"model":"m","answers":{},"usage":{}}`),
		with("result", strings.Replace(systemoneResult, `"clef-flash"`, `"`+managementSecret+`"`, 1)),
		with("extra", `{"nested":["`+managementSecret+`"]}`)}
	for _, body := range bad {
		m.body = body
		reply := m.post(systemoneRequest)
		if reply.status != 502 || strings.Contains(string(reply.body), managementSecret) {
			t.Fatalf("%s: %d %s", body, reply.status, reply.body)
		}
	}
	m.body, m.extra = systemoneResponse(), 1
	if got := m.post(systemoneRequest).status; got != 502 {
		t.Fatalf("truncated: %d", got)
	}
	m.extra, m.ctype = 0, "text/plain"
	if got := m.post(systemoneRequest).status; got != 502 {
		t.Fatalf("text/plain: %d", got)
	}
}

func TestSystemoneTransportErrorNeverRetriesOrExposesException(t *testing.T) {
	m := newManagement(t)
	m.drop = true
	reply := m.post(systemoneRequest)
	if reply.status != 502 || len(m.managementSeen()) != 1 || errorMessage(t, reply.body) != "upstream_invalid_response" {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
}

func TestSystemoneResponseCapAndMalformedModelList(t *testing.T) {
	m := newManagement(t)
	m.body = strings.TrimSuffix(systemoneResponse(), "}") + `,"padding":"` + strings.Repeat("x", 2048) + `"}`
	swap(t, &subscription.MaxOutput, 32)
	if got := m.post(systemoneRequest).status; got != 502 {
		t.Fatalf("over the cap: %d", got)
	}
	for _, rows := range []string{`null`, `[null]`, `[{"id":"../secret"}]`, `[{"id":"group/test"}]`} {
		m.body = `{"authority":"data_only_untrusted","items":` + rows + `}`
		if got := fetch(t, m.url+"/models").status; got != 502 {
			t.Fatalf("%s: %d", rows, got)
		}
	}
}

func TestHealthIdentityNeverReadsModelsOrCallsProvider(t *testing.T) {
	f := newFixture(t)
	swap(t, &routingModels, func() []host.Obj { t.Fatal("models read"); return nil })
	reply := fetch(t, f.gw.URL+"/healthz")
	if reply.status != 200 {
		t.Fatalf("status %d", reply.status)
	}
	same(t, parsed(t, reply.body), `{"service":"arkism-gateway","pid":`+strconv.Itoa(os.Getpid())+`}`)
	if f.seenCount() != 0 {
		t.Fatal("provider called")
	}
}

func TestHealthQueryAndPostCannotBeForwarded(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		method, path string
		status       int
	}{{"GET", "/healthz?secret=private", 400}, {"POST", "/healthz", 405}} {
		var body []byte
		if c.method == "POST" {
			body = []byte("{}")
		}
		reply := send(t, c.method, f.gw.URL+c.path, body)
		if reply.status != c.status || strings.Contains(string(reply.body), "private") {
			t.Fatalf("%s %s: %d %s", c.method, c.path, reply.status, reply.body)
		}
	}
	if f.seenCount() != 0 {
		t.Fatal("provider called")
	}
}
