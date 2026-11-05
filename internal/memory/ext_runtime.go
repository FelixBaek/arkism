package memory

// Process extensions add checks, routes and MCP tools without recompiling Arkism.
// ARKISM_EXTENSIONS_FILE names a JSON file listing programs; each is either a
// local executable Arkism starts and talks to over stdin/stdout (one JSON-RPC
// 2.0 message per line) or an HTTP endpoint it POSTs one JSON-RPC message to:
//
//	{"extensions":[
//	  {"name":"house","command":["/opt/house/bin/ext"],"env":{"MODE":"strict"},"timeout":"2s","fail":"closed"},
//	  {"name":"corp","url":"https://ext.example.com/rpc","token_file":"/etc/arkism/corp.token"}]}
//
// Methods Arkism calls:
//
//	describe  {} -> {"operations":["capture","recall",...],"tenant":true,"routes":[...],"tools":[...]}
//	operation {"phase":"before"|"after","operation","actor","scope","input","ok","error","result"?} -> {"allow":bool,"reason"?}
//	tenant    {"actor","scope","action"} -> {"allow":bool}
//	route     {"actor","scope","method","path","query","body","data"?} -> {"status":200,"body":{...},"data"?:[...]}
//	tool      {"actor","scope","tool","body","data"?} -> {"result":...,"data"?:[...]}
//
// A route or tool that declares "data": true gets the extension's stored
// entries for the space ("data": {key: value}) and may answer changes
// ([{"op":"put","key","value"},{"op":"delete","key"}]); Arkism applies them with
// the caller's read:ext_data and write:ext_data permission (ext_data.go).
//
// Every call has a timeout. When an extension fails or times out, "fail":"closed"
// (the default) refuses the operation, tenant check, route or tool; "open" lets
// operations and tenant checks pass. HTTP endpoints go through the outbound
// address guard (https, or loopback http with allow_private). Hooks only refuse
// or observe: nothing an extension answers can approve a memory.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/FelixBaek/arkism/internal/httpguard"
	"github.com/FelixBaek/arkism/internal/procgroup"
)

const (
	extDefaultTimeout = 2 * time.Second
	extMaxTimeout     = 30 * time.Second
	extMaxMessage     = 1 << 20
	extTenantTTL      = 30 * time.Second
	extTenantCacheMax = 4096
)

var (
	extNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)
	extToolPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	extPathPattern   = regexp.MustCompile(`^/[A-Za-z0-9._~/-]{0,200}$`)
	extActionPattern = regexp.MustCompile(`^[a-z][a-z_]{0,31}:[a-z][a-z0-9_]{0,63}$`)
	extEnvPattern    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	// extSecretEnv names look like credentials; one is passed only when secret_env lists it.
	extSecretEnv = regexp.MustCompile(`KEY|TOKEN|SECRET|PASS|AUTH|DATABASE|CREDENTIAL`)
	errExtFailed = errors.New("extension failed")
)

// ProcessExtensionConfig is one entry of ARKISM_EXTENSIONS_FILE.
type ProcessExtensionConfig struct {
	Name    string            `json:"name"`
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// SecretEnv names env entries whose names look like credentials and are still meant to be passed.
	SecretEnv    []string `json:"secret_env,omitempty"`
	URL          string   `json:"url,omitempty"`
	TokenFile    string   `json:"token_file,omitempty"`
	AllowPrivate bool     `json:"allow_private,omitempty"`
	Timeout      string   `json:"timeout,omitempty"`
	Fail         string   `json:"fail,omitempty"`
	// SendResults adds the operation's result to the "after" call; off, the extension hears only ok/error.
	SendResults bool `json:"send_results,omitempty"`
}

type extToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Action      string          `json:"action"`
	Data        bool            `json:"data,omitempty"`
}

type extDescription struct {
	Operations []string         `json:"operations"`
	Tenant     bool             `json:"tenant"`
	Routes     []ExtensionRoute `json:"routes"`
	Tools      []extToolSpec    `json:"tools"`
}

type extTransport interface {
	call(ctx context.Context, method string, params any) (json.RawMessage, error)
	close()
}

type processExtension struct {
	cfg      ProcessExtensionConfig
	timeout  time.Duration
	failOpen bool
	t        extTransport
	desc     extDescription
}

// ProcessExtensions runs the configured extension programs; Apply puts it into Service.Extensions.
type ProcessExtensions struct {
	list   []*processExtension
	mu     sync.Mutex
	tenant map[string]extTenantAnswer
}

type extTenantAnswer struct {
	allow bool
	at    time.Time
}

// LoadProcessExtensions reads the config file and asks every extension to describe itself.
// A bad file or an extension that does not answer stops startup.
func LoadProcessExtensions(ctx context.Context, path string) (*ProcessExtensions, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("extensions file: %w", err)
	}
	var file struct {
		Extensions []ProcessExtensionConfig `json:"extensions"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&file); err != nil || len(file.Extensions) > 32 {
		return nil, fmt.Errorf("extensions file: not a valid list of at most 32 extensions: %w", ErrInvalid)
	}
	out := &ProcessExtensions{tenant: map[string]extTenantAnswer{}}
	for _, cfg := range file.Extensions {
		if slices.ContainsFunc(out.list, func(e *processExtension) bool { return e.cfg.Name == cfg.Name }) {
			return nil, fmt.Errorf("extension %q is listed twice: %w", cfg.Name, ErrInvalid)
		}
		e, err := newProcessExtension(cfg)
		if err == nil {
			if err = e.describe(ctx); err != nil {
				e.t.close() // started for describe
			}
		}
		if err != nil {
			out.Close()
			return nil, fmt.Errorf("extension %q: %w", cfg.Name, err)
		}
		out.list = append(out.list, e)
	}
	return out, nil
}

func newProcessExtension(cfg ProcessExtensionConfig) (*processExtension, error) {
	e := &processExtension{cfg: cfg, timeout: extDefaultTimeout, failOpen: cfg.Fail == "open"}
	if !extNamePattern.MatchString(cfg.Name) || cfg.Fail != "" && cfg.Fail != "open" && cfg.Fail != "closed" || (len(cfg.Command) > 0) == (cfg.URL != "") {
		return nil, fmt.Errorf("needs a name (a-z0-9, at most 16), exactly one of command or url, and fail open|closed: %w", ErrInvalid)
	}
	if cfg.Timeout != "" {
		t, err := time.ParseDuration(cfg.Timeout)
		if err != nil || t <= 0 || t > extMaxTimeout {
			return nil, fmt.Errorf("timeout must be a duration up to 30s: %w", ErrInvalid)
		}
		e.timeout = t
	}
	if cfg.URL != "" {
		t, err := newHTTPTransport(cfg)
		e.t = t
		return e, err
	}
	t, err := newStdioTransport(cfg)
	e.t = t
	return e, err
}

func (e *processExtension) describe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, max(e.timeout, 10*time.Second))
	defer cancel()
	raw, err := e.t.call(ctx, "describe", map[string]any{})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &e.desc); err != nil {
		return fmt.Errorf("describe answer: %w", ErrInvalid)
	}
	return validExtDescription(&e.desc)
}

func validExtDescription(d *extDescription) error {
	for _, op := range d.Operations {
		if _, ok := operationActions[op]; !ok {
			return fmt.Errorf("unknown operation %q: %w", op, ErrInvalid)
		}
	}
	if len(d.Routes) > 64 || len(d.Tools) > 64 {
		return fmt.Errorf("at most 64 routes and 64 tools: %w", ErrInvalid)
	}
	for _, r := range d.Routes {
		if r.Method != http.MethodGet && r.Method != http.MethodPost || !extPathPattern.MatchString(r.Path) || strings.Contains(r.Path, "..") || !validExtAction(r.Action, r.Roles) {
			return fmt.Errorf("route %s %s needs GET or POST, a plain path, an action verb:collection and known roles: %w", r.Method, r.Path, ErrInvalid)
		}
	}
	for i, t := range d.Tools {
		if t.Action == "" {
			d.Tools[i].Action = "search:memories"
		}
		if len(t.InputSchema) == 0 {
			d.Tools[i].InputSchema = json.RawMessage(`{"type":"object"}`)
		}
		var schema map[string]any
		if !extToolPattern.MatchString(t.Name) || len(t.Description) > 2000 || json.Unmarshal(d.Tools[i].InputSchema, &schema) != nil || !validExtAction(d.Tools[i].Action, nil) {
			return fmt.Errorf("tool %q needs a name a-z0-9_, a description up to 2000 bytes, an object input schema and an action: %w", t.Name, ErrInvalid)
		}
	}
	return nil
}

func validExtAction(action string, roles []string) bool {
	for _, r := range roles {
		if !decisionRoles[r] {
			return false
		}
	}
	return extActionPattern.MatchString(action)
}

// Apply sets the hooks the extensions use; hooks nobody asked for stay nil.
func (p *ProcessExtensions) Apply(x *Extensions) {
	for _, e := range p.list {
		if len(e.desc.Operations) > 0 {
			x.OperationHook = p
		}
		if e.desc.Tenant {
			x.TenantHook = p
		}
		if len(e.desc.Routes) > 0 || len(e.desc.Tools) > 0 {
			x.Endpoints = p
		}
	}
}

// Close stops every extension program.
func (p *ProcessExtensions) Close() {
	for _, e := range p.list {
		e.t.close()
	}
}

func (p *ProcessExtensions) find(name string) *processExtension {
	for _, e := range p.list {
		if e.cfg.Name == name {
			return e
		}
	}
	return nil
}

// extActor is all an extension learns about the caller.
func extActor(d Decision) map[string]string {
	return map[string]string{"principal_id": d.PrincipalID, "role": d.Role}
}

func (e *processExtension) ask(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	raw, err := e.t.call(ctx, method, params)
	if err != nil {
		slog.Warn("extension call failed", "extension", e.cfg.Name, "method", method)
	}
	return raw, err
}

// allowed asks one extension a yes/no question; a failure follows its fail setting and says so.
func (e *processExtension) allowed(ctx context.Context, method string, params any) (allow, failed bool) {
	raw, err := e.ask(ctx, method, params)
	var answer struct {
		Allow *bool `json:"allow"`
	}
	if err != nil || json.Unmarshal(raw, &answer) != nil || answer.Allow == nil {
		return e.failOpen, true
	}
	return *answer.Allow, false
}

func (p *ProcessExtensions) BeforeOperation(ctx context.Context, actor Decision, op string, input any) error {
	for _, e := range p.list {
		if !slices.Contains(e.desc.Operations, op) {
			continue
		}
		if allow, _ := e.allowed(ctx, "operation", map[string]any{"phase": "before", "operation": op, "actor": extActor(actor), "scope": actor.Scope, "input": input}); !allow {
			return fmt.Errorf("%s refused %s: %w", e.cfg.Name, op, ErrExtensionRefused)
		}
	}
	return nil
}

func (p *ProcessExtensions) AfterOperation(ctx context.Context, actor Decision, op string, input, result any, err error) {
	for _, e := range p.list {
		if !slices.Contains(e.desc.Operations, op) {
			continue
		}
		params := map[string]any{"phase": "after", "operation": op, "actor": extActor(actor), "scope": actor.Scope, "input": input, "ok": err == nil}
		if err != nil {
			params["error"] = captureErrorCode(err)
		} else if e.cfg.SendResults {
			params["result"] = result
		}
		_, _ = e.ask(ctx, "operation", params) // an observer: its answer and failures change nothing
	}
}

// CheckTenant asks the tenant extensions once per principal, space and action every 30 seconds.
// The key holds everything the extension is told, so one answer never stands in for another
// caller; an answer that failed (fail open or closed) is not kept.
func (p *ProcessExtensions) CheckTenant(ctx context.Context, actor Decision, action string) error {
	key := strings.Join([]string{actor.PrincipalID, actor.Role, structuredText(actor.Scope), action}, "\x00")
	p.mu.Lock()
	cached, ok := p.tenant[key]
	p.mu.Unlock()
	if !ok || time.Since(cached.at) > extTenantTTL {
		cached = extTenantAnswer{allow: true, at: time.Now()}
		keep := true
		for _, e := range p.list {
			if !e.desc.Tenant {
				continue
			}
			allow, failed := e.allowed(ctx, "tenant", map[string]any{"actor": extActor(actor), "scope": actor.Scope, "action": action})
			keep = keep && !failed
			if !allow {
				cached.allow = false
				break
			}
		}
		if keep {
			p.mu.Lock()
			if len(p.tenant) >= extTenantCacheMax {
				clear(p.tenant)
			}
			p.tenant[key] = cached
			p.mu.Unlock()
		}
	}
	if !cached.allow {
		return ErrExtensionRefused
	}
	return nil
}

// Route finds a declared route by exact path.
// shortcut: no path parameters; an extension reads ids from the query or body until one needs them.
func (p *ProcessExtensions) Route(extension, method, path string) (ExtensionRoute, bool) {
	if e := p.find(extension); e != nil {
		for _, r := range e.desc.Routes {
			if r.Method == method && r.Path == path {
				return r, true
			}
		}
	}
	return ExtensionRoute{}, false
}

func (p *ProcessExtensions) Tools() []ExtensionTool {
	out := []ExtensionTool{}
	for _, e := range p.list {
		for _, t := range e.desc.Tools {
			out = append(out, ExtensionTool{Name: "ext_" + e.cfg.Name + "_" + t.Name, Description: t.Description, InputSchema: t.InputSchema, Action: t.Action, Data: t.Data})
		}
	}
	return out
}

// Serve forwards an authorized route or tool call; any failure is a 503 for the caller.
func (p *ProcessExtensions) Serve(ctx context.Context, call ExtensionCall) (ExtensionAnswer, error) {
	e := p.find(call.Extension)
	if e == nil {
		return ExtensionAnswer{}, ErrNotFound
	}
	params := map[string]any{"actor": extActor(call.Actor), "scope": call.Scope, "body": call.Body}
	if call.Data != nil {
		params["data"] = call.Data
	}
	method := "route"
	if call.Tool != "" {
		method, params["tool"] = "tool", call.Tool
	} else {
		params["method"], params["path"], params["query"] = call.Method, call.Path, call.Query
	}
	raw, err := e.ask(ctx, method, params)
	if err != nil {
		return ExtensionAnswer{}, errExtFailed
	}
	var answer struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
		Result json.RawMessage `json:"result"`
		Data   []ExtDataOp     `json:"data"`
	}
	if json.Unmarshal(raw, &answer) != nil {
		return ExtensionAnswer{}, errExtFailed
	}
	if call.Tool != "" {
		return ExtensionAnswer{Status: http.StatusOK, Body: answer.Result, Data: answer.Data}, nil
	}
	if answer.Status < 200 || answer.Status > 499 {
		return ExtensionAnswer{}, errExtFailed
	}
	return ExtensionAnswer{Status: answer.Status, Body: answer.Body, Data: answer.Data}, nil
}

type rpcResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func rpcResult(data []byte, id int64) (json.RawMessage, error) {
	var res rpcResponse
	if json.Unmarshal(data, &res) != nil || res.ID != id || res.Error != nil || len(res.Result) == 0 {
		return nil, errExtFailed
	}
	return res.Result, nil
}

func rpcRequest(id int64, method string, params any) ([]byte, error) {
	return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

// stdioTransport keeps one program running and sends one call at a time. A call
// that fails or times out stops the program; the next call starts it again.
type stdioTransport struct {
	argv, env []string
	mu        sync.Mutex
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	stdin     io.WriteCloser
	lines     chan []byte
	next      int64
}

func newStdioTransport(cfg ProcessExtensionConfig) (*stdioTransport, error) {
	if !filepath.IsAbs(cfg.Command[0]) || cfg.TokenFile != "" || cfg.AllowPrivate {
		return nil, fmt.Errorf("command needs an absolute program path (token_file and allow_private are for url): %w", ErrInvalid)
	}
	env, err := extEnv(cfg)
	if err != nil {
		return nil, err
	}
	return &stdioTransport{argv: cfg.Command, env: env}, nil
}

// extEnv is the program's whole environment: PATH and its own env, never the
// server's. A name that looks like a credential is refused unless secret_env lists it.
func extEnv(cfg ProcessExtensionConfig) ([]string, error) {
	env := []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range cfg.Env {
		if !extEnvPattern.MatchString(k) || k == "PATH" || extSecretEnv.MatchString(k) && !slices.Contains(cfg.SecretEnv, k) {
			return nil, fmt.Errorf("env name %q (a credential-like name needs secret_env): %w", k, ErrInvalid)
		}
		env = append(env, k+"="+v)
	}
	for _, k := range cfg.SecretEnv {
		if _, ok := cfg.Env[k]; !ok {
			return nil, fmt.Errorf("secret_env %q is not in env: %w", k, ErrInvalid)
		}
	}
	return env, nil
}

func (t *stdioTransport) start() error {
	// Its own process group: stopping the program also stops anything it started.
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, t.argv[0], t.argv[1:]...)
	procgroup.Set(cmd)
	cmd.Env, cmd.Stderr, cmd.WaitDelay = t.env, os.Stderr, 2*time.Second
	stdin, err := cmd.StdinPipe()
	if err == nil {
		var stdout io.ReadCloser
		if stdout, err = cmd.StdoutPipe(); err == nil {
			err = cmd.Start()
		}
		if err == nil {
			t.read(stdout)
		}
	}
	if err != nil {
		cancel()
		return err
	}
	t.cmd, t.stdin, t.cancel = cmd, stdin, cancel
	return nil
}

// read passes the program's answer lines on; a line over extMaxMessage ends the stream.
func (t *stdioTransport) read(stdout io.Reader) {
	lines := make(chan []byte, 1)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), extMaxMessage)
		for sc.Scan() {
			lines <- bytes.Clone(sc.Bytes())
		}
	}()
	t.lines = lines
}

// stop ends the program and its process group; done closes once it has exited.
func (t *stdioTransport) stop() (done chan struct{}) {
	done = make(chan struct{})
	if t.cmd == nil {
		close(done)
		return done
	}
	_ = t.stdin.Close()
	t.cancel() // SIGTERM to the group, SIGKILL a second later (procgroup)
	go func(lines chan []byte) {
		for range lines { // let the reader finish
		}
	}(t.lines)
	go func(c *exec.Cmd) {
		_ = c.Wait()
		close(done)
	}(t.cmd)
	t.cmd = nil
	return done
}

// close stops the program and waits a little for it, so a daemon shutdown leaves no program behind.
func (t *stdioTransport) close() {
	t.mu.Lock()
	done := t.stop()
	t.mu.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

func (t *stdioTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cmd == nil {
		if err := t.start(); err != nil {
			return nil, err
		}
	}
	t.next++
	msg, err := rpcRequest(t.next, method, params)
	if err == nil {
		_, err = t.stdin.Write(append(msg, '\n'))
	}
	if err != nil {
		t.stop()
		return nil, errExtFailed
	}
	select {
	case line, ok := <-t.lines:
		res, err := rpcResult(line, t.next)
		if !ok || err != nil {
			t.stop() // the stream is out of step or closed
			return nil, errExtFailed
		}
		return res, nil
	case <-ctx.Done():
		t.stop()
		return nil, errors.Join(errExtFailed, ctx.Err())
	}
}

type httpTransport struct {
	url, token string
	client     *http.Client
	mu         sync.Mutex
	next       int64
}

func newHTTPTransport(cfg ProcessExtensionConfig) (*httpTransport, error) {
	if len(cfg.Env) > 0 || checkWebhookURL(cfg.URL, cfg.AllowPrivate) != nil {
		return nil, fmt.Errorf("url must be https (or loopback http with allow_private) without credentials; env is for command: %w", ErrInvalid)
	}
	t := &httpTransport{url: cfg.URL, client: httpguard.OutboundClient(cfg.AllowPrivate, extMaxTimeout, nil)}
	if cfg.TokenFile != "" {
		raw, err := os.ReadFile(cfg.TokenFile)
		t.token = strings.TrimSpace(string(raw))
		if err != nil || t.token == "" {
			return nil, fmt.Errorf("token_file is unreadable or empty: %w", ErrInvalid)
		}
	}
	return t, nil
}

func (t *httpTransport) close() {}

func (t *httpTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	t.mu.Lock()
	t.next++
	id := t.next
	t.mu.Unlock()
	msg, err := rpcRequest(id, method, params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(msg))
	if err != nil {
		return nil, err
	}
	req.Header.Set(headerContentType, "application/json")
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	res, err := t.client.Do(req)
	if err != nil {
		return nil, errors.Join(errExtFailed, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, extMaxMessage+1))
	if err != nil || res.StatusCode != http.StatusOK || len(data) > extMaxMessage {
		return nil, errExtFailed
	}
	return rpcResult(data, id)
}
