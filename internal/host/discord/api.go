package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// API calls memoryd. The real one uses the host's service token; tests point it at a fake memoryd.
type API interface {
	Get(path string) (map[string]any, error)
	Post(path string, payload any) (map[string]any, error)
}

// TokenFile is the discord host's service token (perm handoff:discord); without it the owner token is used, like
// the crew runner.
var TokenFile = host.Env("ARKISM_DISCORD_TOKEN_FILE", filepath.Join(host.AgentsState, "discord.token"))

type httpAPI struct {
	base      string
	tokenFile string
	client    *http.Client
}

func newHTTPAPI() *httpAPI {
	tf := TokenFile
	if _, err := os.Stat(tf); err != nil {
		tf = host.TokenFile
	}
	return &httpAPI{base: host.BaseURL, tokenFile: tf, client: &http.Client{Transport: &http.Transport{Proxy: nil}}}
}

func (a *httpAPI) Get(path string) (map[string]any, error) { return a.call(http.MethodGet, path, nil) }

func (a *httpAPI) Post(path string, payload any) (map[string]any, error) {
	return a.call(http.MethodPost, path, payload)
}

func (a *httpAPI) call(method, path string, payload any) (map[string]any, error) {
	raw, err := os.ReadFile(a.tokenFile)
	if err != nil {
		return nil, err
	}
	var body *strings.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = strings.NewReader(string(b))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var req *http.Request
	if body != nil {
		req, err = http.NewRequestWithContext(ctx, method, a.base+path, body)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, a.base+path, nil)
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(raw)))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return host.DoJSON(a.client, req)
}

// scopeQuery is the scope as query parameters, plus extra key/value pairs.
func scopeQuery(extra ...string) string {
	q := url.Values{}
	for k, v := range host.Scope() {
		if s, _ := v.(string); s != "" {
			q.Set(k, s)
		}
	}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return q.Encode()
}

// JSON helpers over decoded maps (numbers arrive as json.Number).

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

func num(v any) float64 {
	switch t := v.(type) {
	case json.Number:
		f, _ := t.Float64()
		return f
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	}
	return 0
}

func strs(v any) []string {
	out := []string{}
	for _, x := range list(v) {
		if s := str(x); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// body is a record's typed payload: rec[field] when present (the memory_records shape), else rec itself.
func body(rec map[string]any, fields ...string) map[string]any {
	for _, f := range fields {
		if m, ok := rec[f].(map[string]any); ok {
			return m
		}
	}
	return rec
}
