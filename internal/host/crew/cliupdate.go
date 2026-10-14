package crew

// Linux CLI inspection and explicitly requested updates.
//
// inspectCLI returns optional version/latest/via and update (a newer known release). performUpdate
// returns ok (command succeeded), error (fixed code, empty on success), and info (fresh inspection
// plus updated: a strictly newer version was observed). A successful no-op has ok=true,
// info.update=false and info.updated=false. No command output, error text, executable paths or
// credentials are returned.
//
// Only absolute executable paths are accepted. Inspection caches live in memory for one hour, keyed
// by binary identity; updates always bypass the version cache. One persistent flock file in the state
// directory (default <tmp>/arkism-cli-update-<uid>) serializes both CLIs across processes; never
// unlink it while in use.

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	cliPackages = map[string]string{"claude": "@anthropic-ai/claude-code", "codex": "@openai/codex"}
	cliBrew     = map[string]string{"claude": "claude-code", "codex": "codex"}
)

const (
	versionTimeout = 5 * time.Second
	updateTimeout  = 600 * time.Second
	httpTimeout    = 8 * time.Second
	outputLimit    = 64 * 1024
	jsonLimit      = 1024 * 1024
	cacheTTL       = 3600 * time.Second
	versionPattern = `[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?`
)

var (
	versionRe     = regexp.MustCompile(`^` + versionPattern + `$`)
	versionLineRe = regexp.MustCompile(`^(?:(?:claude|codex|codex-cli|Claude Code)\s+)?v?(` + versionPattern + `)(?: \(Claude Code\))?$`)
	ansiRe        = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
)

// Test seams: the bounded process runner, the registry fetch and the cache clock.
var (
	cliRun    = runBounded
	latestGet = httpsGet
	cacheNow  = time.Now
)

type memoEntry struct {
	until time.Time
	value any
}

var (
	cliCache   = map[string]memoEntry{}
	cliCacheMu sync.Mutex
)

func validVersion(v any) bool {
	s, ok := v.(string)
	return ok && len(s) <= 60 && versionRe.MatchString(s)
}

// newer is semver precedence: a > b. Build metadata is ignored.
func newer(a, b string) bool {
	parts := func(v string) ([]int, string) {
		v, _, _ = strings.Cut(v, "+")
		core, pre, _ := strings.Cut(v, "-")
		var nums []int
		for _, p := range strings.Split(core, ".") {
			n, _ := strconv.Atoi(p)
			nums = append(nums, n)
		}
		return nums, pre
	}
	ac, ap := parts(a)
	bc, bp := parts(b)
	for i := range ac {
		if ac[i] != bc[i] {
			return ac[i] > bc[i]
		}
	}
	if ap == "" || bp == "" {
		return bp != "" && ap == ""
	}
	as, bs := strings.Split(ap, "."), strings.Split(bp, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, xNum := prereleaseNum(as[i])
		y, yNum := prereleaseNum(bs[i])
		switch {
		case xNum && yNum && x != y:
			return x > y
		case xNum != yNum:
			return !xNum // identifiers outrank numbers
		case !xNum && as[i] != bs[i]:
			return as[i] > bs[i]
		}
	}
	return len(as) > len(bs)
}

func prereleaseNum(s string) (int, bool) {
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && s != ""
}

func memo(key string, ask func() any, fresh bool) any {
	cliCacheMu.Lock()
	defer cliCacheMu.Unlock()
	if e, ok := cliCache[key]; ok && !fresh && cacheNow().Before(e.until) {
		return e.value
	}
	value := ask()
	if len(cliCache) >= 256 {
		cliCache = map[string]memoEntry{}
	}
	cliCache[key] = memoEntry{cacheNow().Add(cacheTTL), value}
	return value
}

// runBounded runs argv in its own session with combined output capped at outputLimit, returning a
// fixed error code ("" on success) and the output when capture is set. Descendants that outlive the
// command or keep its pipe are killed with the group.
func runBounded(argv []string, timeout time.Duration, capture bool) (string, []byte) {
	r, w, err := os.Pipe()
	if err != nil {
		return "spawn_failed", nil
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = w, w
	newSession(cmd)
	if err := cmd.Start(); err != nil {
		r.Close()
		w.Close()
		return "spawn_failed", nil
	}
	w.Close()
	chunks := make(chan []byte)
	go func() {
		defer close(chunks)
		for {
			buf := make([]byte, 8192)
			n, err := r.Read(buf)
			if n > 0 {
				chunks <- buf[:n]
			}
			if err != nil {
				return
			}
		}
	}()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	exited := make(chan error, 1)
	var output []byte
	total, code := 0, ""
read:
	for {
		select {
		case <-deadline.C:
			code = "timeout"
			break read
		case chunk, ok := <-chunks:
			if !ok {
				break read
			}
			total += len(chunk)
			if total > outputLimit {
				code = "output_limit"
				break read
			}
			if capture {
				output = append(output, chunk...)
			}
		}
	}
	go func() { exited <- cmd.Wait() }()
	if code == "" {
		select {
		case err := <-exited:
			exited <- err
			if err != nil {
				code = "command_failed"
			}
		case <-deadline.C:
			code = "timeout"
		}
	}
	killGroup(cmd.Process.Pid, sigKill)
	<-exited
	r.Close()
	for range chunks { // drain so the reader goroutine can exit
	}
	if code != "" {
		return code, nil
	}
	return "", output
}

type fileID struct {
	real string
	key  string
}

// identity is the resolved regular executable and what changes when it is replaced.
func identity(executable string) (fileID, bool) {
	if !filepath.IsAbs(executable) {
		return fileID{}, false
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return fileID{}, false
	}
	resolved, _ = filepath.Abs(resolved)
	st, err := os.Stat(resolved)
	if err != nil || !st.Mode().IsRegular() || !canExecute(resolved) {
		return fileID{}, false
	}
	return fileID{resolved, fmt.Sprint(resolved, " ", st.Size(), " ", st.ModTime().UnixNano(), " ", statIdentity(st))}, true
}

type installation struct {
	via, manager string
	cask         bool
}

func pathParts(p string) []string {
	parts := []string{"/"}
	for _, s := range strings.Split(strings.Trim(p, "/"), "/") {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return parts
}

func joinParts(parts []string) string { return filepath.Join(parts...) }

func installationOf(cli, resolved string) (installation, bool) {
	parts := pathParts(resolved)
	pkg := strings.Split(cliPackages[cli], "/")
	// Alternative managers must not be silently converted to npm installations.
	for _, p := range parts {
		switch p {
		case ".bun", "bun", ".pnpm", "pnpm", ".yarn", "yarn", ".volta", "_npx":
			return installation{}, false
		}
	}
	for i := len(parts) - 1; i >= 1; i-- { // parents, nearest first
		root := parts[:i]
		if len(root) < 3 || root[len(root)-3] != "node_modules" || root[len(root)-2] != pkg[0] || root[len(root)-1] != pkg[1] {
			continue
		}
		modules := root[:len(root)-2]
		modulesParent := modules[:len(modules)-1]
		lib := len(modulesParent) > 1 && modulesParent[len(modulesParent)-1] == "lib"
		if !lib {
			npmish := false
			for _, p := range modules {
				npmish = npmish || p == ".npm" || p == ".npm-global" || p == ".npm-packages"
			}
			if !npmish {
				return installation{}, false
			}
		}
		f, err := os.Open(filepath.Join(joinParts(root), "package.json"))
		if err != nil {
			return installation{}, false
		}
		raw, err := io.ReadAll(io.LimitReader(f, outputLimit+1))
		f.Close()
		if err != nil || len(raw) > outputLimit {
			return installation{}, false
		}
		var marker map[string]any
		if json.Unmarshal(raw, &marker) != nil || marker == nil || marker["name"] != cliPackages[cli] {
			return installation{}, false
		}
		prefix := modulesParent
		if lib && len(modulesParent) > 1 {
			prefix = modulesParent[:len(modulesParent)-1]
		}
		return installation{"npm", filepath.Join(joinParts(prefix), "bin", "npm"), false}, true
	}
	for i := len(parts) - 1; i >= 2; i-- {
		root := parts[:i]
		if parent := root[len(root)-2]; root[len(root)-1] == cliBrew[cli] && (parent == "Cellar" || parent == "Caskroom") {
			return installation{"brew", filepath.Join(joinParts(root[:len(root)-2]), "bin", "brew"), parent == "Caskroom"}, true
		}
	}
	marker := []string{".codex", "packages", "standalone"}
	if cli == "claude" {
		marker = []string{".local", "share", "claude", "versions"}
	}
	for i := 0; i < len(parts)-len(marker); i++ {
		if strings.Join(parts[i:i+len(marker)], "/") == strings.Join(marker, "/") {
			return installation{"self", "", false}, true
		}
	}
	return installation{}, false
}

func cliVersion(executable string) string {
	code, raw := cliRun([]string{executable, "--version"}, versionTimeout, true)
	if code != "" {
		return ""
	}
	text := strings.TrimSpace(ansiRe.ReplaceAllString(strings.ToValidUTF8(string(raw), "�"), ""))
	// Only a version or the two known CLI labels; arbitrary log lines are not data.
	m := versionLineRe.FindStringSubmatch(text)
	if m == nil || !validVersion(m[1]) {
		return ""
	}
	return m[1]
}

var registryClient = &http.Client{
	Transport:     &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
	Timeout:       httpTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// httpsGet fetches https://host+target; the body is read only for a 200.
func httpsGet(host, target string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, "https://"+host+target, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := registryClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return resp.StatusCode, nil, nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, jsonLimit+1))
	return resp.StatusCode, raw, err
}

func latestRelease(cli string, inst installation) string {
	host, target := "registry.npmjs.org", "/"+cliPackages[cli]+"/latest"
	if inst.via == "brew" {
		kind := "formula"
		if inst.cask {
			kind = "cask"
		}
		host, target = "formulae.brew.sh", "/api/"+kind+"/"+cliBrew[cli]+".json"
	}
	status, raw, err := latestGet(host, target)
	if err != nil || status != 200 || len(raw) > jsonLimit {
		return "" // never follow redirects, even to the same host
	}
	var page map[string]any
	if json.Unmarshal(raw, &page) != nil || page == nil {
		return ""
	}
	version := page["version"]
	if inst.via == "brew" && !inst.cask {
		versions, ok := page["versions"].(map[string]any)
		if _, present := page["versions"]; present && !ok {
			return ""
		}
		version = versions["stable"]
	}
	if s, ok := version.(string); ok && inst.via == "brew" && inst.cask {
		version, _, _ = strings.Cut(s, ",")
	}
	if !validVersion(version) {
		return ""
	}
	return version.(string)
}

func inspect(cli, executable string, checkLatest, fresh bool) map[string]any {
	info := map[string]any{"update": false}
	if _, ok := cliPackages[cli]; !ok {
		return info
	}
	id, ok := identity(executable)
	if !ok {
		return info
	}
	version, _ := memo("version\x00"+cli+"\x00"+executable+"\x00"+id.key, func() any { return cliVersion(executable) }, fresh).(string)
	if version != "" {
		info["version"] = version
	}
	if inst, ok := installationOf(cli, id.real); ok {
		info["via"] = inst.via
		if checkLatest {
			latest, _ := memo(fmt.Sprint("latest\x00", cli, "\x00", inst.via, "\x00", inst.cask), func() any { return latestRelease(cli, inst) }, false).(string)
			if latest != "" {
				info["latest"] = latest
				info["update"] = version != "" && newer(latest, version)
			}
		}
	}
	return info
}

// inspectCLI reads a supported CLI; the latest lookup is opt-in and failures omit unknown fields.
func inspectCLI(cli, executable string, checkLatest bool) map[string]any {
	return inspect(cli, executable, checkLatest, false)
}

func updateCLI(cli, executable string, expectedVersion, expectedVia any) map[string]any {
	initial, initialOK := identity(executable)
	before := inspect(cli, executable, false, true)
	result := func(code string, info map[string]any) map[string]any {
		out := map[string]any{"updated": false}
		for k, v := range info {
			out[k] = v
		}
		return map[string]any{"ok": code == "", "error": code, "info": out}
	}
	if !same(before["version"], expectedVersion) || !same(before["via"], expectedVia) {
		return result("conflict", before)
	}
	if before["version"] == nil {
		return result("version_unavailable", before)
	}
	id, ok := identity(executable)
	if ok != initialOK || id != initial {
		return result("conflict", before)
	}
	inst, known := installationOf(cli, id.real)
	if !ok || !known {
		return result("unknown_installation", before)
	}
	if !same(inst.via, expectedVia) {
		return result("conflict", before)
	}
	var command []string
	if inst.via == "self" {
		command = []string{executable, "update"}
	} else {
		prefix := filepath.Dir(filepath.Dir(inst.manager))
		manager := inst.manager
		if _, ok := identity(manager); !ok {
			manager = ""
			if inst.via == "npm" {
				manager, _ = exec.LookPath("npm")
			}
		}
		if manager == "" {
			return result("manager_unavailable", before)
		}
		if inst.via == "npm" {
			command = []string{manager, "install", "-g", "--prefix", prefix, cliPackages[cli] + "@latest"}
		} else {
			kind := "--formula"
			if inst.cask {
				kind = "--cask"
			}
			command = []string{manager, "upgrade", kind, cliBrew[cli]}
		}
	}
	code, _ := cliRun(command, updateTimeout, false)
	after := inspect(cli, executable, false, true)
	if code == "" && after["version"] == nil {
		code = "verification_failed"
	}
	response := result(code, after)
	v, _ := after["version"].(string)
	response["info"].(map[string]any)["updated"] = v != "" && newer(v, before["version"].(string))
	return response
}

// performUpdate serializes, checks the caller's snapshot, updates, then re-reads even on failure.
// Safe errors: unsupported_cli, invalid_request, busy, state_unavailable, conflict,
// version_unavailable, unknown_installation, manager_unavailable, spawn_failed, timeout,
// output_limit, command_failed, process_failed, verification_failed. A failed command can still
// have info.updated=true.
func performUpdate(cli, executable string, expectedVersion, expectedVia any, stateDir string) map[string]any {
	rejected := func(code string) map[string]any {
		return map[string]any{"ok": false, "error": code, "info": map[string]any{"update": false, "updated": false}}
	}
	if _, ok := cliPackages[cli]; !ok {
		return rejected("unsupported_cli")
	}
	switch expectedVia {
	case nil, "npm", "brew", "self":
	default:
		return rejected("invalid_request")
	}
	if !validVersion(expectedVersion) {
		return rejected("invalid_request")
	}
	folder := stateDir
	if folder == "" {
		folder = filepath.Join(os.TempDir(), fmt.Sprintf("arkism-cli-update-%d", uid()))
	}
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return rejected("state_unavailable")
	}
	st, err := os.Lstat(folder)
	if err != nil || !st.IsDir() || !ownedByMe(st) || st.Mode().Perm()&0o077 != 0 {
		return rejected("state_unavailable")
	}
	fd, err := syscall.Open(filepath.Join(folder, "update.lock"), syscall.O_CREAT|syscall.O_RDWR|oNoFollow|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return rejected("state_unavailable")
	}
	lock := os.NewFile(uintptr(fd), "update.lock")
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByMe(info) || linkCount(info) != 1 || info.Mode().Perm()&0o077 != 0 {
		return rejected("state_unavailable")
	}
	held, err := flock(lock, false)
	if err != nil {
		return rejected("state_unavailable")
	}
	if !held {
		return rejected("busy")
	}
	return updateCLI(cli, executable, expectedVersion, expectedVia)
}
