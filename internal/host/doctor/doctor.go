// Package doctor is `arkism doctor`: a read-only health check of this computer's Arkism
// client (app connection, agent hooks and MCP entries, model CLIs, capture queues, capture
// settings, disk), with `--fix` for the two safe repairs: re-running the agent installer and
// moving failed capture jobs back into the spool. It never prints tokens or message text.
package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

const usage = `usage: arkism doctor [--json] [--fix]
  --json  print the report as JSON
  --fix   re-run the agent installer and requeue failed capture jobs, then check again
exit code: 0 when nothing failed, 1 when a check failed, 2 on bad arguments`

// Status values of a check.
const (
	OK   = "ok"
	Warn = "warn"
	Fail = "fail"
)

// diskWarnBytes is the free space below which the app data volume is reported.
const diskWarnBytes = 2 << 30

// Check is one diagnosis.
type Check struct {
	ID      string         `json:"id"`
	Status  string         `json:"status"`
	Message string         `json:"message"`
	Fix     string         `json:"fix,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// Action is one repair --fix made.
type Action struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// Report is the whole result.
type Report struct {
	OK      bool           `json:"ok"`
	Summary map[string]int `json:"summary"`
	Checks  []Check        `json:"checks"`
	Fixes   []Action       `json:"fixes,omitempty"`
}

// env is everything the checks read from the computer; tests build their own.
type env struct {
	home       string
	connPath   string // the app's agents.json
	goos       string
	lookPath   func(string) (string, error)
	http       *http.Client
	freeBytes  func(dir string) (uint64, error)
	runInstall func() error
}

func defaultEnv() *env {
	return &env{
		home:       host.Home,
		connPath:   connPath(host.Home),
		goos:       runtime.GOOS,
		lookPath:   exec.LookPath,
		http:       &http.Client{Timeout: 2 * time.Second},
		freeBytes:  freeBytes,
		runInstall: runInstaller,
	}
}

// connPath is the app's agents.json, in Tauri's local data folder (same rule as the capture client).
func connPath(home string) string {
	var local string
	switch runtime.GOOS {
	case "windows":
		local = host.Env("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	case "darwin":
		local = filepath.Join(home, "Library", "Application Support")
	default:
		local = host.Env("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	}
	return host.Env("ARKISM_AGENT_CONNECTION", filepath.Join(local, "dev.arkism.desktop", "runtime", "agents.json"))
}

// runInstaller runs `arkism install-arkism` as a child; its output (backup paths) is not shown.
func runInstaller() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "install-arkism")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}

func (e *env) folder() string { return filepath.Join(e.home, ".config", "arkism") }

func (e *env) client() string {
	if e.goos == "windows" {
		return filepath.Join(e.folder(), "arkism.exe")
	}
	return filepath.Join(e.folder(), "arkism")
}

// run is every check in report order.
func (e *env) run() []Check {
	var out []Check
	out = append(out, e.checkConnection()...)
	out = append(out, e.checkClient())
	out = append(out, e.checkAgents()...)
	out = append(out, e.checkCLIs()...)
	out = append(out, e.checkQueues()...)
	out = append(out, e.checkSettings())
	out = append(out, e.checkDisk())
	return out
}

func report(checks []Check, fixes []Action) Report {
	r := Report{OK: true, Summary: map[string]int{OK: 0, Warn: 0, Fail: 0}, Checks: checks, Fixes: fixes}
	for _, c := range checks {
		r.Summary[c.Status]++
		if c.Status == Fail {
			r.OK = false
		}
	}
	return r
}

// fix applies the safe repairs the checks call for.
func (e *env) fix(checks []Check) []Action {
	var actions []Action
	needInstall := false
	for _, c := range checks {
		if c.Status != OK && (c.ID == "client" || strings.HasPrefix(c.ID, "agent.")) {
			needInstall = true
		}
	}
	if needInstall {
		if err := e.runInstall(); err != nil {
			actions = append(actions, Action{"fix.install", Fail, "에이전트 설치 재실행 실패: " + errCode(err)})
		} else {
			actions = append(actions, Action{"fix.install", OK, "에이전트 설치(install-arkism)를 다시 실행했습니다"})
		}
	}
	for _, q := range e.queues() {
		moved, skipped, err := requeue(q.dir)
		if moved == 0 && skipped == 0 && err == nil {
			continue
		}
		a := Action{"fix.requeue." + q.origin, OK, fmt.Sprintf("실패 %d건을 다시 대기열로 옮겼습니다", moved)}
		if skipped > 0 {
			a.Message += fmt.Sprintf(" (같은 이름이 대기열에 있어 %d건 건너뜀)", skipped)
		}
		if err != nil {
			a.Status, a.Message = Fail, a.Message+"; 오류: "+errCode(err)
		}
		actions = append(actions, a)
	}
	if len(actions) == 0 {
		actions = append(actions, Action{"fix.none", OK, "고칠 항목이 없습니다"})
	}
	return actions
}

// errCode is an error without paths or output: an exit code or a short kind.
func errCode(err error) string {
	if ee, ok := err.(*exec.ExitError); ok {
		return fmt.Sprintf("exit %d", ee.ExitCode())
	}
	if os.IsPermission(err) {
		return "permission_denied"
	}
	if os.IsNotExist(err) {
		return "not_found"
	}
	return "io_error"
}

func doctorMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	return runMain(defaultEnv(), args, stdout, stderr)
}

func runMain(e *env, args []string, stdout, stderr io.Writer) int {
	asJSON, doFix := false, false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "--fix":
			doFix = true
		case "-h", "--help", "help":
			fmt.Fprintln(stdout, usage)
			return 0
		default:
			fmt.Fprintln(stderr, usage)
			return 2
		}
	}
	checks := e.run()
	var fixes []Action
	if doFix {
		fixes = e.fix(checks)
		checks = e.run()
	}
	r := report(checks, fixes)
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
	} else {
		writeText(stdout, r)
	}
	if !r.OK {
		return 1
	}
	return 0
}

var labels = map[string]string{OK: "정상", Warn: "주의", Fail: "실패"}

func writeText(w io.Writer, r Report) {
	fmt.Fprintln(w, "Arkism 진단")
	for _, c := range r.Checks {
		fmt.Fprintf(w, "[%s] %s: %s\n", labels[c.Status], c.ID, c.Message)
		if c.Status != OK && c.Fix != "" {
			fmt.Fprintf(w, "       해결: %s\n", c.Fix)
		}
	}
	if len(r.Fixes) > 0 {
		fmt.Fprintln(w, "수리")
		for _, a := range r.Fixes {
			fmt.Fprintf(w, "[%s] %s: %s\n", labels[a.Status], a.ID, a.Message)
		}
	}
	fmt.Fprintf(w, "정상 %d · 주의 %d · 실패 %d\n", r.Summary[OK], r.Summary[Warn], r.Summary[Fail])
}

func init() { host.Register("doctor", doctorMain) }
