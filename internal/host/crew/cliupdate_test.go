//go:build linux

package crew

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type runReply struct {
	code string
	out  string
}

type cliFixture struct {
	t       *testing.T
	root    string
	state   string
	replies []runReply // queued cliRun answers; the last one repeats
	onRun   func()
	argv    [][]string
	gets    [][2]string
}

func newCLIFixture(t *testing.T) *cliFixture {
	f := &cliFixture{t: t, root: t.TempDir()}
	f.state = filepath.Join(f.root, "state")
	cliCache = map[string]memoEntry{}
	oldRun, oldGet, oldNow := cliRun, latestGet, cacheNow
	t.Cleanup(func() { cliRun, latestGet, cacheNow = oldRun, oldGet, oldNow; cliCache = map[string]memoEntry{} })
	cliRun = func(argv []string, timeout time.Duration, capture bool) (string, []byte) {
		f.argv = append(f.argv, argv)
		if f.onRun != nil {
			f.onRun()
		}
		if len(f.replies) == 0 {
			t.Fatal("unexpected process", argv)
		}
		r := f.replies[0]
		if len(f.replies) > 1 {
			f.replies = f.replies[1:]
		}
		if !capture {
			return r.code, nil
		}
		return r.code, []byte(r.out)
	}
	latestGet = func(host, target string) (int, []byte, error) { t.Fatal("unexpected network"); return 0, nil, nil }
	t.Setenv("PATH", t.TempDir())
	return f
}

func (f *cliFixture) reply(rs ...runReply) { f.replies, f.argv = rs, nil }

func (f *cliFixture) file(relative, body string) string {
	p := filepath.Join(f.root, relative)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte(body), 0o700)
	return p
}

func (f *cliFixture) installed(name, via string, cask bool) string {
	var target string
	switch via {
	case "self":
		target = ".local/share/claude/versions/1.0.0"
		if name == "codex" {
			target = ".codex/packages/standalone/releases/1.0.0/codex"
		}
	case "npm":
		root := "npm/lib/node_modules/" + cliPackages[name]
		f.file(root+"/package.json", fmt.Sprintf(`{"name": %q}`, cliPackages[name]))
		f.file("npm/bin/npm", "fake")
		target = root + "/bin/cli.js"
	default:
		f.file("brew/bin/brew", "fake")
		kind := "Cellar"
		if cask {
			kind = "Caskroom"
		}
		target = "brew/" + kind + "/" + cliBrew[name] + "/1.0.0/bin/" + name
	}
	realFile := f.file(target, "fake")
	link := filepath.Join(f.root, fmt.Sprint(name, "-", via, cask))
	_ = os.Symlink(realFile, link)
	return link
}

func (f *cliFixture) update(executable string, version, via any) map[string]any {
	return performUpdate("claude", executable, version, via, f.state)
}

func TestCLIKnownInstallationsAndAllowlistedCommands(t *testing.T) {
	f := newCLIFixture(t)
	for _, name := range []string{"claude", "codex"} {
		for _, c := range []struct {
			via  string
			cask bool
		}{{"self", false}, {"npm", false}, {"brew", false}, {"brew", true}} {
			path := f.installed(name, c.via, c.cask)
			f.reply(runReply{"", "1.0.0"}, runReply{"", "secret update log"}, runReply{"", "1.1.0"})
			result := performUpdate(name, path, "1.0.0", c.via, f.state)
			info := result["info"].(map[string]any)
			if result["ok"] != true || info["updated"] != true || info["via"] != c.via {
				t.Fatal(name, c, result)
			}
			want := []string{path, "update"}
			switch {
			case c.via == "npm":
				want = []string{filepath.Join(f.root, "npm/bin/npm"), "install", "-g", "--prefix", filepath.Join(f.root, "npm"), cliPackages[name] + "@latest"}
			case c.via == "brew" && c.cask:
				want = []string{filepath.Join(f.root, "brew/bin/brew"), "upgrade", "--cask", cliBrew[name]}
			case c.via == "brew":
				want = []string{filepath.Join(f.root, "brew/bin/brew"), "upgrade", "--formula", cliBrew[name]}
			}
			if strings.Join(f.argv[1], " ") != strings.Join(want, " ") || strings.Contains(mustCanonical(result), "secret") {
				t.Fatal(f.argv[1], want)
			}
		}
	}
}

func TestCLIUnknownAndWrongMarkersNeverUpdate(t *testing.T) {
	f := newCLIFixture(t)
	paths := []string{f.file("bin/claude", "x"), f.file(".local/bin/claude", "x"), f.file("Cellar/wrong/1.0.0/claude", "x"),
		f.file(".codex/packages/other/codex", "x"), f.file("local/node_modules/@anthropic-ai/claude-code/cli.js", "x"),
		f.file(".bun/lib/node_modules/@anthropic-ai/claude-code/cli.js", "x"), f.file("bad/lib/node_modules/@anthropic-ai/claude-code/cli.js", "x")}
	f.file("bad/lib/node_modules/@anthropic-ai/claude-code/package.json", `{"name":"wrong"}`)
	for _, p := range paths {
		f.reply(runReply{"", "1.0.0"})
		if _, ok := inspectCLI("claude", p, true)["via"]; ok {
			t.Error(p)
		}
		if r := f.update(p, "1.0.0", nil); r["error"] != "unknown_installation" {
			t.Error(p, r)
		}
		for _, argv := range f.argv {
			if argv[len(argv)-1] != "--version" {
				t.Error(argv)
			}
		}
	}
}

func TestCLINpmMarkerRequiresValidBoundedJSON(t *testing.T) {
	f := newCLIFixture(t)
	path := f.installed("claude", "npm", false)
	resolved, _ := filepath.EvalSymlinks(path)
	marker := filepath.Join(filepath.Dir(filepath.Dir(resolved)), "package.json")
	for _, body := range []string{"[]", "null", "{", strings.Repeat(" ", outputLimit+1)} {
		_ = os.WriteFile(marker, []byte(body), 0o600)
		if _, ok := installationOf("claude", resolved); ok {
			t.Error(body[:min(len(body), 10)])
		}
	}
}

func TestCLICacheTTLAndBinaryChange(t *testing.T) {
	f := newCLIFixture(t)
	path := f.installed("claude", "self", false)
	now := time.Unix(10, 0)
	cacheNow = func() time.Time { return now }
	f.reply(runReply{"", "1.0.0"})
	first := inspectCLI("claude", path, false)
	first["version"] = "9.0.0"
	if inspectCLI("claude", path, false)["version"] != "1.0.0" || len(f.argv) != 1 {
		t.Fatal(f.argv)
	}
	now = time.Unix(3611, 0)
	inspectCLI("claude", path, false)
	if len(f.argv) != 2 {
		t.Fatal(len(f.argv))
	}
	resolved, _ := filepath.EvalSymlinks(path)
	_ = os.WriteFile(resolved, []byte("replacement"), 0o700)
	inspectCLI("claude", path, false)
	if len(f.argv) != 3 {
		t.Fatal(len(f.argv))
	}
}

func TestCLIConflictBypassesCache(t *testing.T) {
	f := newCLIFixture(t)
	path := f.installed("claude", "self", false)
	f.reply(runReply{"", "1.0.0"})
	inspectCLI("claude", path, false)
	f.reply(runReply{"", "2.0.0"})
	if r := f.update(path, "1.0.0", "self"); r["error"] != "conflict" || len(f.argv) != 1 {
		t.Fatal(r)
	}
	f.reply(runReply{"", "1.0.0"})
	if r := f.update(path, "1.0.0", "npm"); r["error"] != "conflict" || len(f.argv) != 1 {
		t.Fatal(r)
	}
}

func TestCLIBinaryReplacedDuringProbeIsConflict(t *testing.T) {
	f := newCLIFixture(t)
	path := f.installed("claude", "self", false)
	resolved, _ := filepath.EvalSymlinks(path)
	f.onRun = func() { _ = os.WriteFile(resolved, []byte("changed binary identity"), 0o700) }
	f.reply(runReply{"", "1.0.0"})
	if r := f.update(path, "1.0.0", "self"); r["error"] != "conflict" || len(f.argv) != 1 {
		t.Fatal(r)
	}
}

func TestCLIMissingPackageManagerNeverRunsUpdate(t *testing.T) {
	f := newCLIFixture(t)
	path := f.installed("claude", "npm", false)
	_ = os.Remove(filepath.Join(f.root, "npm/bin/npm"))
	f.reply(runReply{"", "1.0.0"})
	if r := f.update(path, "1.0.0", "npm"); r["error"] != "manager_unavailable" || len(f.argv) != 1 {
		t.Fatal(r)
	}
}

func TestCLISuccessNoopFailureAndMissingPostVersion(t *testing.T) {
	f := newCLIFixture(t)
	path := f.installed("claude", "self", false)
	for _, c := range []struct {
		code, after string
		ok          bool
		want        string
		updated     bool
	}{{"", "1.0.0", true, "", false}, {"command_failed", "1.1.0", false, "command_failed", true}, {"timeout", "1.0.0", false, "timeout", false},
		{"", "secret-invalid-output", false, "verification_failed", false}} {
		f.reply(runReply{"", "1.0.0"}, runReply{c.code, "secret"}, runReply{"", c.after})
		r := f.update(path, "1.0.0", "self")
		info := r["info"].(map[string]any)
		if r["ok"] != c.ok || r["error"] != c.want || info["updated"] != c.updated || info["update"] != false || len(f.argv) != 3 ||
			strings.Contains(mustCanonical(r), "secret") {
			t.Error(c, r)
		}
	}
}

func TestCLISingleFlockForBothCLIs(t *testing.T) {
	f := newCLIFixture(t)
	path := f.installed("claude", "self", false)
	_ = os.Mkdir(f.state, 0o700)
	lock, _ := os.OpenFile(filepath.Join(f.state, "update.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if f.update(path, "1.0.0", "self")["error"] != "busy" || performUpdate("codex", path, "1.0.0", "self", f.state)["error"] != "busy" {
		t.Fatal("lock ignored")
	}
	lock.Close()
	f.reply(runReply{"", "1.0.0"})
	first, second := f.update(path, "1.0.0", "self"), f.update(path, "1.0.0", "self")
	if first["ok"] != true || second["ok"] != true {
		t.Fatal("lock not released")
	}
}

func TestCLIStateSymlinkAndOpenPermissionsRejected(t *testing.T) {
	f := newCLIFixture(t)
	path := f.installed("claude", "self", false)
	_ = os.Mkdir(f.state, 0o700)
	_ = os.Symlink(f.file("victim", "x"), filepath.Join(f.state, "update.lock"))
	if r := f.update(path, "1.0.0", "self"); r["error"] != "state_unavailable" {
		t.Fatal(r)
	}
	_ = os.Chmod(f.state, 0o755)
	if r := f.update(path, "1.0.0", "self"); r["error"] != "state_unavailable" {
		t.Fatal(r)
	}
}

func TestCLIInvalidOrUnsupportedInputsDoNotExecute(t *testing.T) {
	f := newCLIFixture(t)
	for _, name := range []string{"gemini", "claude;echo bad", ""} {
		if r := inspectCLI(name, "/fake/bin", false); mustCanonical(r) != `{"update":false}` {
			t.Error(r)
		}
		if r := performUpdate(name, "/fake/bin", "1.0.0", "npm", f.state); r["error"] != "unsupported_cli" {
			t.Error(r)
		}
	}
	for _, v := range []any{nil, "1.0.0;echo bad", "secret", strings.Repeat("1", 129)} {
		if r := f.update("/fake/bin", v, "self"); r["error"] != "invalid_request" {
			t.Error(v, r)
		}
	}
	if r := inspectCLI("claude", "claude", false); mustCanonical(r) != `{"update":false}` {
		t.Error(r)
	}
	if r := f.update("/fake/bin", "1.0.0", []any{}); r["error"] != "invalid_request" {
		t.Error(r)
	}
}

func (f *cliFixture) http(body string, status int) {
	latestGet = func(host, target string) (int, []byte, error) {
		f.gets = append(f.gets, [2]string{host, target})
		if status != 200 {
			return status, nil, nil
		}
		return status, []byte(body), nil
	}
}

func TestCLIFixedLatestEndpoints(t *testing.T) {
	f := newCLIFixture(t)
	for _, name := range []string{"claude", "codex"} {
		for _, c := range []installation{{"npm", "", false}, {"self", "", false}, {"brew", "", false}, {"brew", "", true}} {
			body := `{"version": "2.0.0"}`
			if c.via == "brew" && !c.cask {
				body = `{"versions": {"stable": "2.0.0"}}`
			} else if c.cask {
				body = `{"version": "2.0.0,build"}`
			}
			f.gets = nil
			f.http(body, 200)
			if got := latestRelease(name, c); got != "2.0.0" {
				t.Error(name, c, got)
			}
			host, target := "registry.npmjs.org", "/"+cliPackages[name]+"/latest"
			if c.via == "brew" {
				kind := "formula"
				if c.cask {
					kind = "cask"
				}
				host, target = "formulae.brew.sh", "/api/"+kind+"/"+cliBrew[name]+".json"
			}
			if len(f.gets) != 1 || f.gets[0] != [2]string{host, target} {
				t.Error(f.gets)
			}
		}
	}
}

func TestCLILatestRejectsRedirectOversizeMalformedAndSecret(t *testing.T) {
	f := newCLIFixture(t)
	for _, c := range []struct {
		body   string
		status int
	}{{"{}", 302}, {"{}", 500}, {strings.Repeat(" ", jsonLimit+1), 200}, {"[]", 200}, {"{", 200}, {`{"version":"secret"}`, 200}} {
		f.gets = nil
		f.http(c.body, c.status)
		if got := latestRelease("claude", installation{"npm", "", false}); got != "" || len(f.gets) != 1 {
			t.Error(c.status, got)
		}
	}
}

func TestCLILatestNetworkFailureIsUnknownAndCached(t *testing.T) {
	f := newCLIFixture(t)
	path := f.installed("claude", "self", false)
	latestGet = func(host, target string) (int, []byte, error) {
		f.gets = append(f.gets, [2]string{host, target})
		return 0, nil, os.ErrDeadlineExceeded
	}
	f.reply(runReply{"", "1.0.0"})
	for i := 0; i < 2; i++ {
		info := inspectCLI("claude", path, true)
		if _, ok := info["latest"]; ok || info["update"] != false {
			t.Fatal(info)
		}
	}
	if len(f.gets) != 1 {
		t.Fatal(f.gets)
	}
}

func TestCLILatestOptInCachedAndSemver(t *testing.T) {
	f := newCLIFixture(t)
	path := f.installed("claude", "self", false)
	f.http(`{"version":"1.1.0"}`, 200)
	f.reply(runReply{"", "1.0.0"})
	inspectCLI("claude", path, false)
	if len(f.gets) != 0 {
		t.Fatal("latest without opt-in")
	}
	first, second := inspectCLI("claude", path, true), inspectCLI("claude", path, true)
	if first["update"] != true || second["update"] != true || len(f.gets) != 1 {
		t.Fatal(f.gets)
	}
	for _, c := range [][2]string{{"1.10.0", "1.9.9"}, {"1.0.0", "1.0.0-rc.2"}, {"1.0.0-rc.10", "1.0.0-rc.9"}, {"1.0.0-a", "1.0.0-1"}} {
		if !newer(c[0], c[1]) || newer(c[1], c[0]) {
			t.Error(c)
		}
	}
	if newer("1.0.0+build2", "1.0.0+build1") {
		t.Error("build metadata counted")
	}
}

func TestCLIVersionParsingOnlyKnownFormats(t *testing.T) {
	f := newCLIFixture(t)
	for raw, want := range map[string]string{"2.1.3 (Claude Code)\n": "2.1.3", "codex-cli 0.123.0": "0.123.0",
		"\x1b[32m1.2.3\x1b[0m": "1.2.3", "secret 1.2.3 token": ""} {
		f.reply(runReply{"", raw})
		if got := cliVersion("/fake/cli"); got != want {
			t.Error(raw, got)
		}
	}
}

func init() {
	helperModes["print-stdout"] = func(args []string) int { fmt.Print(args[0]); return 0 }
	helperModes["print-many"] = func([]string) int { fmt.Print(strings.Repeat("a", outputLimit+1)); return 0 }
	helperModes["exit-42"] = func([]string) int { fmt.Print("secret"); return 42 }
	helperModes["hold-pipe"] = func([]string) int { time.Sleep(10 * time.Second); return 0 }
}

func TestCLIRunBoundedOutputTimeoutAndFailures(t *testing.T) {
	bin := testBinary(t)
	for _, c := range []struct {
		argv    []string
		capture bool
		code    string
		out     string
	}{{[]string{bin, "print-stdout", "1.0.0"}, true, "", "1.0.0"}, {[]string{bin, "print-stdout", "secret"}, false, "", ""},
		{[]string{bin, "print-many"}, true, "output_limit", ""}, {[]string{bin, "hold-pipe"}, true, "timeout", ""},
		{[]string{bin, "exit-42"}, true, "command_failed", ""}, {[]string{"/nonexistent/cli", "update"}, true, "spawn_failed", ""}} {
		start := time.Now()
		code, out := runBounded(c.argv, 2*time.Second, c.capture)
		if code != c.code || string(out) != c.out || time.Since(start) > 4*time.Second {
			t.Error(c.argv, code, string(out))
		}
	}
}
