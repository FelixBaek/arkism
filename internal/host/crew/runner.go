package crew

// The runner's own calls to the Arkism API.

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

var scopeOrder = []string{"workspace_id", "project_id", "user_id", "agent_id"}

// scope is the capture scope in its Python key order.
func scope() host.Obj { return ordered(host.Scope(), scopeOrder...) }

// scopeQuery is the non-empty scope fields, then extra pairs, URL-encoded in that order.
func scopeQuery(extra ...string) string {
	parts := []string{}
	for _, kv := range scope() {
		if truthy(kv.Value) {
			parts = append(parts, quotePlus(kv.Key)+"="+quotePlus(pyStr(kv.Value)))
		}
	}
	for i := 0; i+1 < len(extra); i += 2 {
		parts = append(parts, quotePlus(extra[i])+"="+quotePlus(extra[i+1]))
	}
	return strings.Join(parts, "&")
}

var directClient = &http.Client{Transport: &http.Transport{Proxy: nil}}

// runnerCall is runnerAPI (TestMain makes it fail: no Arkism in tests).
var runnerCall = runnerAPI

// runnerAPI calls Arkism with the runner token (the owner token when there is none), never through a proxy.
func runnerAPI(path string, payload any, timeout time.Duration) (map[string]any, error) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	tokenFile := host.RunnerTokenFile
	if !exists(tokenFile) {
		tokenFile = host.TokenFile
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, err
	}
	method, body := http.MethodGet, ""
	if payload != nil {
		if body, err = host.Dumps(payload, host.DumpOpts{}); err != nil {
			return nil, err
		}
		method = http.MethodPost
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, host.BaseURL+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", bearer+strings.TrimSpace(string(raw)))
	if payload != nil {
		req.Header.Set("Content-Type", jsonType)
	}
	return host.DoJSON(directClient, req)
}

// requester is runnerCall in the shape the work context reader takes.
func requester(path string, payload map[string]any) (map[string]any, error) {
	if payload == nil {
		return runnerCall(path, nil, 15*time.Second)
	}
	return runnerCall(path, payload, 15*time.Second)
}
