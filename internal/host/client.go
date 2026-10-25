package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Settings shared by every tool that talks to the Arkism API.
var (
	Home      = homeDir()
	BaseURL   = strings.TrimRight(Env("ARKISM_URL", "http://127.0.0.1:8091"), "/")
	TokenFile = Env("ARKISM_TOKEN_FILE", "/srv/arkism/config/owner.token")
	// CaptureState is the capture client's state dir (spool, metrics).
	CaptureState = Env("ARKISM_CAPTURE_STATE", filepath.Join(Home, ".local/state/arkism-capture"))
	// ClaudeBin is the Claude Code CLI the capture client runs for extraction.
	ClaudeBin = claudeBin(Env("ARKISM_CAPTURE_CLAUDE", "claude"))
	// AgentsState holds agents.json, provider keys and runner tokens (was arkism_crew.STATE).
	AgentsState = Env("ARKISM_AGENTS_STATE", filepath.Join(Home, ".config/arkism"))
	SkillsRoot  = Env("ARKISM_SKILLS_ROOT", filepath.Join(Home, "skills"))
	McpRoot     = Env("ARKISM_MCP_ROOT", filepath.Join(Home, "mcp"))
	PluginsRoot = Env("ARKISM_PLUGINS_ROOT", filepath.Join(Home, "plugins"))
	DataDir     = Env("ARKISM_DATA_DIR", filepath.Join(Home, ".local/share/arkism"))
)

// claudeBin turns a Windows npm shim (claude.cmd / claude.ps1) into the claude.exe behind it.
// Going through cmd.exe re-parses the arguments and breaks the JSON schema and the empty
// --tools value the extractor passes, so the CLI answers without structured output.
func claudeBin(name string) string {
	if runtime.GOOS != "windows" {
		return name
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return name
	}
	if ext := strings.ToLower(filepath.Ext(path)); ext != ".cmd" && ext != ".bat" && ext != ".ps1" {
		return path
	}
	for _, exe := range []string{
		filepath.Join(filepath.Dir(path), "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe"),
		filepath.Join(Home, ".local", "bin", "claude.exe"),
	} {
		if info, err := os.Stat(exe); err == nil && !info.IsDir() {
			return exe
		}
	}
	return path
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// Env is os.getenv(name, def): an empty value counts as set, like Python.
func Env(name, def string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return def
}

// Scope is the memory scope every request carries.
func Scope() map[string]any {
	return map[string]any{
		"workspace_id": Env("ARKISM_WORKSPACE", "default"),
		"project_id":   Env("ARKISM_PROJECT", "default"),
		"user_id":      Env("ARKISM_USER", "management:owner"),
		"agent_id":     Env("ARKISM_AGENT", ""),
	}
}

// NowISO is UTC with milliseconds and a Z, like 2026-10-11T03:04:05.678Z.
func NowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// Metric appends one event line to the capture metrics log.
func Metric(fields map[string]any) error {
	if err := os.MkdirAll(CaptureState, 0o777); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(CaptureState, "metrics.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	defer f.Close()
	row := Obj{{"ts", NowISO()}}
	for _, k := range sortedKeys(fields) {
		row.Set(k, fields[k])
	}
	_, err = f.WriteString(MustDumps(row, DumpOpts{NoASCII: true}) + "\n")
	return err
}

// AtomicWrite replaces path so a kill mid-write never leaves a torn file (mode 0600).
func AtomicWrite(path, text string) error {
	// Each writer owns an exclusively created file. A predictable temporary
	// name could follow a foreign link or collide with another state writer.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".arkism-state-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, writeErr := tmp.WriteString(text)
	if writeErr == nil {
		writeErr = tmp.Sync()
	}
	closeErr := tmp.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := replaceAtomicWrite(name, path); err != nil {
		return err
	}
	return nil
}

// HTTPError is a non-2xx answer (urllib's HTTPError).
type HTTPError struct {
	Code int
	Body []byte
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP Error %d", e.Code) }

// HTTPCode returns the status of an *HTTPError in err, or 0.
func HTTPCode(err error) int {
	var h *HTTPError
	if errors.As(err, &h) {
		return h.Code
	}
	return 0
}

// direct never uses a proxy from the environment (urllib ProxyHandler({})).
var direct = &http.Client{Transport: &http.Transport{Proxy: nil}}

func tokenFrom(file string) (string, error) {
	raw, err := os.ReadFile(file)
	return strings.TrimSpace(string(raw)), err
}

// Get calls the Arkism API with the owner token and decodes the JSON answer.
func Get(path string, timeout time.Duration) (map[string]any, error) {
	return call(TokenFile, http.MethodGet, path, nil, timeout)
}

// GetWith is Get under the credential in tokenFile instead of TokenFile.
func GetWith(tokenFile, path string, timeout time.Duration) (map[string]any, error) {
	return call(tokenFile, http.MethodGet, path, nil, timeout)
}

// PostWith is Post under the credential in tokenFile instead of TokenFile.
func PostWith(tokenFile, path string, payload any, timeout time.Duration) (map[string]any, error) {
	return call(tokenFile, http.MethodPost, path, payload, timeout)
}

// Post sends payload as JSON with the owner token and decodes the JSON answer.
func Post(path string, payload any, timeout time.Duration) (map[string]any, error) {
	return call(TokenFile, http.MethodPost, path, payload, timeout)
}

func call(tokenFile, method, path string, payload any, timeout time.Duration) (map[string]any, error) {
	tok, err := tokenFrom(tokenFile)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if payload != nil {
		raw, err := Dumps(payload, DumpOpts{})
		if err != nil {
			return nil, err
		}
		body = strings.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return DoJSON(direct, req)
}

// DoJSON runs req and decodes a JSON object answer; a non-2xx status is an *HTTPError.
func DoJSON(client *http.Client, req *http.Request) (map[string]any, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &HTTPError{Code: resp.StatusCode, Body: raw}
	}
	v, err := Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("json: answer is not an object")
	}
	return obj, nil
}

// Search recalls memories for query; on an embedder outage it keeps lexical recall
// within the same time budget instead of returning nothing.
func Search(query string, limit int, timeout time.Duration) ([]any, error) {
	return SearchTo(query, limit, timeout, "internal")
}

// SearchTo is Search for a destination: "external:<name>" makes the server pass every hit through its transfer policy.
func SearchTo(query string, limit int, timeout time.Duration, destination string) ([]any, error) {
	deadline := time.Now().Add(timeout)
	if r := []rune(query); len(r) > 1000 { // the server rejects queries over 1000 runes
		query = string(r[:1000])
	}
	body := map[string]any{"scope": Scope(), "query": query, "modes": []any{"exact", "fts", "vector"}, "purpose": "answer",
		"destination": destination, "limit": limit, "max_chars": 6000}
	res, err := Post("/v1/search", body, timeout)
	if err != nil {
		if HTTPCode(err) != 503 || time.Until(deadline) <= 0 {
			return nil, err
		}
		body["modes"] = []any{"exact", "fts"}
		res, err = Post("/v1/search", body, time.Until(deadline))
		if err != nil {
			return nil, err
		}
		return items(res), nil
	}
	if degraded, _ := res["degraded"].([]any); contains(degraded, "memory_vector_unavailable") {
		_ = Metric(map[string]any{"event": "recall_degraded", "reason": "memory_vector_unavailable"})
		if remaining := time.Until(deadline); remaining > 0 {
			retry, err := Post("/v1/search", body, remaining)
			if err == nil {
				res = retry
			} else if code := HTTPCode(err); code != 0 && code != 503 {
				return nil, err
			} // a 503 or network error keeps the first, lexical answer
		}
	}
	return items(res), nil
}

func items(res map[string]any) []any {
	list, _ := res["items"].([]any)
	return list
}

func contains(list []any, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// AgentOff reports whether the owner took agent id off in 설정 › 에이전트 (agents.json "removed").
func AgentOff(id string) bool {
	raw, err := os.ReadFile(filepath.Join(AgentsState, "agents.json"))
	if err != nil {
		return false
	}
	var reg struct {
		Removed []string `json:"removed"`
	}
	if json.Unmarshal(raw, &reg) != nil {
		return false
	}
	for _, r := range reg.Removed {
		if r == id {
			return true
		}
	}
	return false
}
