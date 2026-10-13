package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const fixInstall = "arkism doctor --fix (install-arkism 재실행)"

// readSmall reads a settings file of at most 4MB (Claude's .claude.json grows).
func readSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, errors.New("too large")
	}
	return data, nil
}

// readJSON is the file's top-level object; a BOM is dropped.
func readJSON(path string) (map[string]any, error) {
	data, err := readSmall(path)
	if err != nil {
		return nil, err
	}
	data = []byte(strings.TrimPrefix(string(data), "\ufeff"))
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// ---- 1. app connection ----

type conn struct {
	Mode      string `json:"mode"`
	URL       string `json:"url"`
	TokenFile string `json:"token_file"`
}

func (e *env) checkConnection() []Check {
	data, err := readSmall(e.connPath)
	if os.IsNotExist(err) {
		return []Check{{ID: "app.connection", Status: OK, Message: "앱 연결 파일 없음: 서버(SSH) 모드로 동작",
			Details: map[string]any{"mode": "server"}}}
	}
	var c conn
	if err == nil {
		err = json.Unmarshal(data, &c)
	}
	if err != nil {
		return []Check{{ID: "app.connection", Status: Fail, Message: "앱 연결 파일(agents.json)을 읽을 수 없음",
			Fix: "Arkism 앱을 다시 시작해 연결 파일을 새로 쓰게 하세요"}}
	}
	if c.Mode != "local" {
		return []Check{{ID: "app.connection", Status: OK, Message: "서버 모드: 에이전트가 SSH로 서버 Arkism에 연결",
			Details: map[string]any{"mode": "server"}}}
	}
	port := strings.TrimPrefix(c.URL, "http://127.0.0.1:")
	if !strings.HasPrefix(c.URL, "http://127.0.0.1:") || port == "" || strings.ContainsAny(port, "/?#@") {
		return []Check{{ID: "app.connection", Status: Fail, Message: "로컬 모드인데 주소가 이 PC(127.0.0.1)가 아님: 연결 파일이 무시됨",
			Fix: "Arkism 앱을 다시 시작하세요", Details: map[string]any{"mode": "local"}}}
	}
	out := []Check{{ID: "app.connection", Status: OK, Message: "로컬 모드: 앱 내장 Arkism (포트 " + port + ")",
		Details: map[string]any{"mode": "local", "port": port}}}
	if c.TokenFile == "" || !isFile(c.TokenFile) {
		out = append(out, Check{ID: "app.token", Status: Fail, Message: "토큰 파일이 없음: 에이전트가 SSH로 우회",
			Fix: "Arkism 앱을 다시 시작해 토큰 파일을 만들게 하세요"})
	} else {
		out = append(out, Check{ID: "app.token", Status: OK, Message: "토큰 파일 있음"})
	}
	resp, err := e.http.Get(c.URL + "/readyz")
	switch {
	case err != nil:
		out = append(out, Check{ID: "app.readyz", Status: Fail, Message: "내장 Arkism이 응답하지 않음(2초)",
			Fix: "Arkism 앱이 켜져 있는지 확인하고 다시 시작하세요"})
	default:
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			out = append(out, Check{ID: "app.readyz", Status: OK, Message: "내장 Arkism 준비됨(readyz)"})
		} else {
			out = append(out, Check{ID: "app.readyz", Status: Fail, Message: fmt.Sprintf("내장 Arkism이 준비되지 않음(HTTP %d)", resp.StatusCode),
				Fix: "잠시 뒤 다시 확인하고, 계속되면 앱을 다시 시작하세요", Details: map[string]any{"http_status": resp.StatusCode}})
		}
	}
	return out
}

// ---- 2. client and agents ----

func (e *env) checkClient() Check {
	path := e.client()
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return Check{ID: "client", Status: Fail, Message: "설치된 클라이언트(~/.config/arkism/" + filepath.Base(path) + ")가 없음", Fix: fixInstall}
	}
	if e.goos != "windows" && fi.Mode().Perm()&0o111 == 0 {
		return Check{ID: "client", Status: Fail, Message: "설치된 클라이언트에 실행 권한이 없음", Fix: fixInstall}
	}
	return Check{ID: "client", Status: OK, Message: "설치된 클라이언트 있음"}
}

// norm lower-cases a path and writes it with forward slashes, undoing JSON/TOML \\ escapes.
func norm(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, `\\`, `\`), `\`, "/"))
}

// ours reports whether s names the installed client (or the Windows Codex wrapper).
func (e *env) ours(s string) bool {
	s = norm(s)
	for _, m := range []string{e.client(), filepath.Join(e.folder(), "hook-codex.cmd")} {
		if strings.Contains(s, norm(m)) {
			return true
		}
	}
	return false
}

// mentions reports whether any string inside v names the client.
func (e *env) mentions(v any) bool {
	switch x := v.(type) {
	case string:
		return e.ours(x)
	case []any:
		for _, it := range x {
			if e.mentions(it) {
				return true
			}
		}
	case map[string]any:
		for _, it := range x {
			if e.mentions(it) {
				return true
			}
		}
	}
	return false
}

func child(obj map[string]any, keys ...string) any {
	var v any = obj
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// hookEvents are the events missing a client handler under obj[keys...].
func (e *env) hookEvents(path string, events []string, keys ...string) ([]string, error) {
	obj, err := readJSON(path)
	if err != nil {
		return events, err
	}
	var missing []string
	for _, ev := range events {
		if !e.mentions(child(obj, append(keys, ev)...)) {
			missing = append(missing, ev)
		}
	}
	return missing, nil
}

// jsonServer reports whether obj[keys...] is a server entry that runs the client.
func (e *env) jsonServer(path string, keys ...string) bool {
	obj, err := readJSON(path)
	if err != nil {
		return false
	}
	entry, ok := child(obj, keys...).(map[string]any)
	return ok && e.mentions(entry["command"])
}

// tomlServer reports whether [mcp_servers.skill-router] exists and its command is the client.
func (e *env) tomlServer(path string) bool {
	data, err := readSmall(path)
	if err != nil {
		return false
	}
	in := false
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			in = t == "[mcp_servers.skill-router]" || t == `[mcp_servers."skill-router"]`
			continue
		}
		if in && strings.HasPrefix(t, "command") && e.ours(t) {
			return true
		}
	}
	return false
}

var hookEvents4 = []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"}

func (e *env) checkAgents() []Check {
	h := e.home
	type agent struct {
		name, dir string
		probe     func() []string // what is missing
	}
	hooksProblem := func(file, label string, events []string, keys ...string) []string {
		missing, err := e.hookEvents(file, events, keys...)
		if err != nil && !os.IsNotExist(err) {
			return []string{label + " 읽기 실패"}
		}
		if len(missing) > 0 {
			return []string{label + " 훅 없음(" + strings.Join(missing, ",") + ")"}
		}
		return nil
	}
	agents := []agent{
		{"claude", filepath.Join(h, ".claude"), func() (p []string) {
			p = hooksProblem(filepath.Join(h, ".claude", "settings.json"), "settings.json", hookEvents4, "hooks")
			if !e.jsonServer(filepath.Join(h, ".claude.json"), "mcpServers", "skill-router") {
				p = append(p, ".claude.json MCP(skill-router) 없음")
			}
			return p
		}},
		{"codex", filepath.Join(h, ".codex"), func() (p []string) {
			p = hooksProblem(filepath.Join(h, ".codex", "hooks.json"), "hooks.json", hookEvents4, "hooks")
			if !e.tomlServer(filepath.Join(h, ".codex", "config.toml")) {
				p = append(p, "config.toml [mcp_servers.skill-router] 없음")
			}
			return p
		}},
		{"zcode", filepath.Join(h, ".zcode"), func() (p []string) {
			file := filepath.Join(h, ".zcode", "cli", "config.json")
			p = hooksProblem(file, "config.json", []string{"SessionStart", "UserPromptSubmit", "Stop"}, "hooks", "events")
			if !e.jsonServer(file, "mcp", "servers", "arkism") {
				p = append(p, "config.json MCP(arkism) 없음")
			}
			return p
		}},
		{"gemini", filepath.Join(h, ".gemini"), func() (p []string) {
			if !e.jsonServer(filepath.Join(h, ".gemini", "settings.json"), "mcpServers", "skill-router") {
				p = append(p, "settings.json MCP(skill-router) 없음")
			}
			return p
		}},
		{"grok", filepath.Join(h, ".grok"), func() (p []string) {
			if !e.tomlServer(filepath.Join(h, ".grok", "config.toml")) {
				p = append(p, "config.toml [mcp_servers.skill-router] 없음")
			}
			return p
		}},
	}
	var out []Check
	for _, a := range agents {
		if !isDir(a.dir) {
			continue
		}
		id := "agent." + a.name
		if problems := a.probe(); len(problems) > 0 {
			out = append(out, Check{ID: id, Status: Fail, Message: a.name + " 연결 빠짐: " + strings.Join(problems, "; "),
				Fix: fixInstall, Details: map[string]any{"missing": problems}})
		} else {
			out = append(out, Check{ID: id, Status: OK, Message: a.name + " 훅·MCP 연결됨"})
		}
	}
	if len(out) == 0 {
		out = append(out, Check{ID: "agents", Status: Warn, Message: "설치된 에이전트 폴더(~/.claude, ~/.codex 등)가 없음",
			Fix: "Claude Code나 Codex를 설치한 뒤 arkism doctor --fix"})
	}
	return out
}

// ---- 3. model CLIs ----

func (e *env) checkCLIs() []Check {
	var out []Check
	for _, name := range []string{"claude", "codex"} {
		id := "cli." + name
		path, err := e.lookPath(name)
		if err != nil {
			for _, p := range []string{filepath.Join(e.home, ".local", "bin", name), filepath.Join(e.home, "."+name, "bin", name)} {
				if isFile(p) {
					path, err = p, nil
					break
				}
			}
		}
		if err != nil {
			out = append(out, Check{ID: id, Status: Warn, Message: name + " CLI가 PATH에 없음",
				Fix: name + " CLI를 설치하고 PATH에 넣으세요"})
			continue
		}
		c := Check{ID: id, Status: OK, Message: name + " CLI 있음"}
		ext := strings.ToLower(filepath.Ext(path))
		if e.goos == "windows" && (ext == ".cmd" || ext == ".bat" || ext == ".ps1") {
			c.Details = map[string]any{"npm_shim": true}
			c.Message = name + " CLI 있음(npm " + ext + " 셈)"
			if name == "claude" {
				native := false
				for _, exe := range []string{
					filepath.Join(filepath.Dir(path), "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe"),
					filepath.Join(e.home, ".local", "bin", "claude.exe"),
				} {
					native = native || isFile(exe)
				}
				c.Details["native_exe"] = native
				if native {
					c.Message += " → claude.exe 사용"
				} else {
					c.Message += ", claude.exe를 찾지 못해 셈으로 실행"
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// ---- 4. capture queues ----

type queue struct{ origin, dir string }

func (e *env) queues() []queue {
	found, _ := filepath.Glob(filepath.Join(e.folder(), "local-capture-*"))
	sort.Strings(found)
	var out []queue
	for _, d := range found {
		if isDir(d) {
			out = append(out, queue{strings.TrimPrefix(filepath.Base(d), "local-capture-"), d})
		}
	}
	return out
}

func jobFiles(dir string) []string {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	return paths
}

var codeRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,80}$`)

// lastError is the newest failure's error code: a short code only, never free text.
func lastError(paths []string) (code string, at time.Time) {
	var newest string
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && (newest == "" || fi.ModTime().After(at)) {
			newest, at = p, fi.ModTime()
		}
	}
	if newest == "" {
		return "", at
	}
	obj, err := readJSON(newest)
	if err != nil {
		return "unreadable", at
	}
	s, _ := obj["last_error"].(string)
	if !codeRe.MatchString(s) {
		return "unknown", at
	}
	return s, at
}

func (e *env) checkQueues() []Check {
	qs := e.queues()
	if len(qs) == 0 {
		return []Check{{ID: "queue", Status: OK, Message: "수집 대기열 없음"}}
	}
	var out []Check
	for _, q := range qs {
		spool, failed := jobFiles(filepath.Join(q.dir, "spool")), jobFiles(filepath.Join(q.dir, "failed"))
		c := Check{ID: "queue." + q.origin, Status: OK,
			Message: fmt.Sprintf("%s 대기 %d건, 실패 %d건", q.origin, len(spool), len(failed)),
			Details: map[string]any{"spool": len(spool), "failed": len(failed)}}
		if len(failed) > 0 {
			code, at := lastError(failed)
			c.Status = Warn
			c.Message += ", 최근 오류 " + code
			c.Fix = "arkism doctor --fix 로 실패 건을 다시 대기열에 넣습니다"
			c.Details["last_error"] = code
			c.Details["last_failed_at"] = at.UTC().Format(time.RFC3339)
		} else if len(spool) >= 100 {
			c.Status = Warn
			c.Message += ": 대기 건이 쌓여 있음"
			c.Fix = "에이전트를 한 번 실행해 전송을 다시 시작하세요"
		}
		out = append(out, c)
	}
	return out
}

// requeue moves failed/*.json back into spool/ with the attempt count reset.
func requeue(dir string) (moved, skipped int, err error) {
	failedDir, spoolDir := filepath.Join(dir, "failed"), filepath.Join(dir, "spool")
	paths := jobFiles(failedDir)
	if len(paths) == 0 {
		return 0, 0, nil
	}
	if err := os.MkdirAll(spoolDir, 0o700); err != nil {
		return 0, 0, err
	}
	for _, p := range paths {
		dst := filepath.Join(spoolDir, filepath.Base(p))
		if _, statErr := os.Lstat(dst); statErr == nil {
			skipped++
			continue
		}
		data, rerr := readSmall(p)
		if rerr != nil {
			err = rerr
			continue
		}
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.UseNumber()
		var job map[string]any
		if derr := dec.Decode(&job); derr != nil || job == nil {
			err = errors.New("unreadable job")
			continue
		}
		job["attempts"] = 0
		delete(job, "last_error")
		delete(job, "archive_retry_after")
		text, merr := json.Marshal(job)
		if merr != nil {
			err = merr
			continue
		}
		tmp := filepath.Join(spoolDir, "."+filepath.Base(p)+".tmp") // drain's *.json glob skips it
		if werr := os.WriteFile(tmp, text, 0o600); werr != nil {
			err = werr
			continue
		}
		if rerr := os.Rename(tmp, dst); rerr != nil {
			os.Remove(tmp)
			err = rerr
			continue
		}
		if rerr := os.Remove(p); rerr != nil {
			err = rerr
			continue
		}
		moved++
	}
	return moved, skipped, err
}

// ---- 5. capture settings ----

func (e *env) checkSettings() Check {
	path := filepath.Join(e.folder(), "capture.json")
	data, err := readSmall(path)
	if os.IsNotExist(err) {
		return Check{ID: "capture.settings", Status: OK, Message: "수집 켜짐, 제외 폴더 0개",
			Details: map[string]any{"paused": false, "excluded": 0}}
	}
	var s struct {
		Paused  bool     `json:"paused"`
		Exclude []string `json:"exclude"`
	}
	if err == nil {
		err = json.Unmarshal(data, &s)
	}
	if err != nil {
		return Check{ID: "capture.settings", Status: Warn, Message: "수집 설정(capture.json)을 읽을 수 없어 기본값으로 동작",
			Fix: "앱의 설정 › 고급 › 수집에서 다시 저장하세요"}
	}
	n := 0
	for _, f := range s.Exclude {
		if strings.TrimSpace(f) != "" {
			n++
		}
	}
	c := Check{ID: "capture.settings", Status: OK, Message: fmt.Sprintf("수집 켜짐, 제외 폴더 %d개", n),
		Details: map[string]any{"paused": s.Paused, "excluded": n}}
	if s.Paused {
		c.Status, c.Message = Warn, fmt.Sprintf("수집 일시정지됨, 제외 폴더 %d개", n)
		c.Fix = "앱의 설정 › 고급 › 수집에서 다시 켜세요"
	}
	return c
}

// ---- 6. disk ----

func (e *env) checkDisk() Check {
	dir := filepath.Dir(filepath.Dir(e.connPath)) // the app's data folder
	for !isDir(dir) {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	free, err := e.freeBytes(dir)
	if err != nil {
		return Check{ID: "disk", Status: Warn, Message: "앱 데이터 볼륨의 여유 공간을 알 수 없음"}
	}
	gb := float64(free) / (1 << 30)
	c := Check{ID: "disk", Status: OK, Message: fmt.Sprintf("앱 데이터 볼륨 여유 %.1fGB", gb),
		Details: map[string]any{"free_bytes": free}}
	if free < diskWarnBytes {
		c.Status, c.Message = Warn, fmt.Sprintf("앱 데이터 볼륨 여유 %.1fGB: 2GB 미만", gb)
		c.Fix = "디스크 공간을 확보하세요"
	}
	return c
}
