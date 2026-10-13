package gateway

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func guarded(t *testing.T, optional bool) (*fixture, string, *bytes.Buffer) {
	f := newFixture(t)
	warn := &bytes.Buffer{}
	gw := httptest.NewServer(Guard{Next: Handler{}, Token: testToken, Optional: optional, Warn: warn})
	t.Cleanup(gw.Close)
	return f, gw.URL, warn
}

const bareMessages = `{"model":"claude-test","max_tokens":5,"messages":[{"role":"user","content":"안녕"}]}`

func TestGuardRefusesMissingAndWrongTokensBeforeAnyEgress(t *testing.T) {
	f, url, _ := guarded(t, false)
	for _, c := range []struct {
		name    string
		headers []string
		code    string
	}{
		{"missing", nil, "gateway_token_missing"},
		{"wrong", []string{host.GatewayTokenHeader, strings.Repeat("f", 64)}, "gateway_token_invalid"},
		{"prefix", []string{host.GatewayTokenHeader, testToken[:32]}, "gateway_token_invalid"},
		{"empty", []string{host.GatewayTokenHeader, ""}, "gateway_token_invalid"},
	} {
		headers := append([]string{"content-type", "application/json", "authorization", "Bearer caller"}, c.headers...)
		for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/models"} {
			method, body := "POST", []byte(bareMessages)
			if path == "/v1/models" {
				method, body = "GET", nil
			}
			reply := send(t, method, url+path, body, headers...)
			if reply.status != 401 || reply.header.Get("content-type") != "application/json" {
				t.Fatalf("%s %s: %d %s", c.name, path, reply.status, reply.body)
			}
			got := parsed(t, reply.body)
			if get(get(got, "error"), "type") != "authentication_error" ||
				!strings.HasPrefix(get(get(got, "error"), "message").(string), c.code) {
				t.Fatalf("%s %s: %s", c.name, path, reply.body)
			}
			if strings.Contains(string(reply.body), testToken) {
				t.Fatal("token echoed")
			}
		}
	}
	if f.seenCount() != 0 {
		t.Fatal("provider called without a valid token")
	}
}

func TestGuardPassesTheRightTokenAndNeverForwardsIt(t *testing.T) {
	f, url, _ := guarded(t, false)
	// A bare Claude model passes through with the caller's own headers: the token must still stay here.
	swap(t, &target, func(string) (*where, error) { return &where{kind: "anthropic"}, nil })
	reply := send(t, "POST", url+"/v1/messages", []byte(bareMessages), "content-type", "application/json",
		"authorization", "Bearer caller", host.GatewayTokenHeader, testToken)
	if reply.status != 200 || f.seenCount() != 1 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	seen := f.seen[0]
	if seen.header.Get(host.GatewayTokenHeader) != "" || seen.header.Get("authorization") != "Bearer caller" {
		t.Fatal(seen.header)
	}
	for _, values := range seen.header {
		for _, v := range values {
			if strings.Contains(v, testToken) {
				t.Fatal("token forwarded", seen.header)
			}
		}
	}
	if strings.Contains(string(seen.raw), testToken) {
		t.Fatal("token in body")
	}
}

func TestGuardKeepsHealthOpenForTheControlProbe(t *testing.T) {
	_, url, _ := guarded(t, false)
	if reply := fetch(t, url+"/healthz"); reply.status != 200 || get(parsed(t, reply.body), "service") != "arkism-gateway" {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	if reply := fetch(t, url+"/healthzx"); reply.status != 401 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
}

func TestGuardGraceFlagLetsTokenlessCallsThroughWithAWarningMetric(t *testing.T) {
	t.Setenv("ARKISM_GATEWAY_TOKEN_OPTIONAL", "1")
	f, url, warn := guarded(t, true)
	before := tokenless.Load()
	reply := send(t, "POST", url+"/v1/messages", []byte(bareMessages), "content-type", "application/json")
	if reply.status != 200 || f.seenCount() != 1 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	if tokenless.Load() != before+1 {
		t.Fatal("tokenless call not counted")
	}
	health := parsed(t, fetch(t, url+"/healthz").body)
	if n, _ := host.Int(get(health, "tokenless_calls")); n != before+1 {
		t.Fatal(health)
	}
	if !strings.Contains(warn.String(), "ARKISM_GATEWAY_TOKEN_OPTIONAL=1") || strings.Contains(warn.String(), testToken) {
		t.Fatal(warn.String())
	}
	// A wrong token is still refused: the grace covers clients that send none.
	reply = send(t, "POST", url+"/v1/messages", []byte(bareMessages), "content-type", "application/json", host.GatewayTokenHeader, "nope")
	if reply.status != 401 || f.seenCount() != 1 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
}

func TestGatewayTokenComesFromEnvOrIsMadeOnce(t *testing.T) {
	dir := t.TempDir()
	swap(t, &host.GatewayTokenFile, dir+"/gateway.token")
	t.Setenv("ARKISM_GATEWAY_TOKEN", "")
	first, err := gatewayToken()
	if err != nil || !host.ValidGatewayToken(first) {
		t.Fatal(first, err)
	}
	if again, err := gatewayToken(); err != nil || again != first {
		t.Fatal(again, err)
	}
	t.Setenv("ARKISM_GATEWAY_TOKEN", testToken)
	if v, err := gatewayToken(); err != nil || v != testToken {
		t.Fatal(v, err)
	}
	t.Setenv("ARKISM_GATEWAY_TOKEN", "short")
	if _, err := gatewayToken(); err == nil {
		t.Fatal("malformed env token accepted")
	}
}
