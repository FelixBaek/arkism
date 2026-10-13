package crew

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// helperModes lets the test binary stand in for child programs (adapters, CLIs). Each takes the
// remaining argv and returns an exit code.
var helperModes = map[string]func(args []string) int{}

var selfCommands = map[string]bool{"skill-execution": true, "crew": true}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		if mode, ok := helperModes[os.Args[1]]; ok {
			os.Exit(mode(os.Args[2:]))
		}
		// The binary re-launching itself (background workers) reaches the real command.
		if selfCommands[os.Args[1]] {
			os.Exit(host.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
		}
	}
	os.Exit(isolated(m))
}

// isolated runs the tests against an empty home and state: never the host's keys, logins or Arkism.
func isolated(m *testing.M) int {
	root, err := os.MkdirTemp("", "crew-test-")
	if err != nil {
		return 1
	}
	defer os.RemoveAll(root)
	empty := filepath.Join(root, "empty-path")
	_ = os.Mkdir(empty, 0o700)
	host.Home = root
	host.AgentsState = filepath.Join(root, "state")
	host.KeysFile = filepath.Join(root, "provider-keys.json")
	host.RunnerTokenFile = filepath.Join(root, "crew-runner.token")
	host.TokenFile = filepath.Join(root, "owner.token")
	host.CaptureState = filepath.Join(root, "capture")
	host.DataDir = filepath.Join(root, "data")
	host.APIMode = map[string]bool{}
	for _, k := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR", "ARKISM_CREW_ROUTES", "ARKISM_CLAUDE_BIN", "ARKISM_CODEX_BIN",
		"ARKISM_GEMINI_BIN", "ARKISM_GLM_BIN", "ARKISM_GROK_BIN", "ARKISM_AGY_BIN", "ARKISM_CREW_OUTPUTS", "ARKISM_CODEX_MAX_EFFORT"} {
		_ = os.Unsetenv(k)
	}
	_ = os.Setenv("PATH", empty)
	runnerCall = func(string, any, time.Duration) (map[string]any, error) { return nil, errors.New("no Arkism in tests") }
	return m.Run()
}

// testBinary is this test executable, used as a pinned adapter or a fake CLI.
func testBinary(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, _ = filepath.Abs(p)
	return p
}

// readStdinJSON is json.load(sys.stdin) for helper modes.
func readStdinJSON() map[string]any {
	v, err := host.Decode(os.Stdin)
	if err != nil {
		os.Exit(3)
	}
	m, _ := v.(map[string]any)
	return m
}

func init() {
	// The comparison adapter: echo input when a skill is given (or the case is not "quality").
	helperModes["adapter-echo"] = func([]string) int {
		x := readStdinJSON()
		if !keysAre(x, "input", "skill") || os.Getenv("ARKISM_API_TOKEN") != "" {
			return 1
		}
		out := x["input"]
		if x["skill"] == "" && x["input"] == "quality" {
			out = "wrong"
		}
		fmt.Println(mustCanonical(map[string]any{"output": out, "cost": json.Number("0.25")}))
		return 0
	}
	// Decides only from the materialized companion file, never from the skill text.
	helperModes["adapter-companion"] = func([]string) int {
		x := readStdinJSON()
		dir, _ := x["skill_dir"].(string)
		script, err1 := os.ReadFile(filepath.Join(dir, "scripts/answer.txt"))
		body, err2 := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		ok := err1 == nil && err2 == nil && string(script) == "right" && string(body) == x["skill"]
		out := x["input"]
		if !ok && x["input"] == "quality" {
			out = "wrong"
		}
		fmt.Println(mustCanonical(map[string]any{"output": out, "cost": json.Number("0.25")}))
		return 0
	}
	// The echo adapter that also counts its runs in $COUNT_FILE.
	helperModes["adapter-count"] = func([]string) int {
		x := readStdinJSON()
		time.Sleep(40 * time.Millisecond)
		f, err := os.OpenFile(os.Getenv("COUNT_FILE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return 1
		}
		fmt.Fprintln(f, "called")
		f.Close()
		out := x["input"]
		if x["skill"] == "" && x["input"] == "quality" {
			out = "wrong"
		}
		fmt.Println(mustCanonical(map[string]any{"output": out, "cost": json.Number("0.1")}))
		return 0
	}
	helperModes["adapter-print"] = func(args []string) int { fmt.Println(args[0]); return 0 }
	helperModes["adapter-sleep"] = func([]string) int { time.Sleep(10 * time.Second); return 0 }
	helperModes["adapter-big"] = func([]string) int {
		for i := 0; i < 1000; i++ {
			fmt.Print("xxxxxxxxxx")
		}
		return 0
	}
	helperModes["adapter-fail"] = func([]string) int { fmt.Fprintln(os.Stderr, "private failure"); return 1 }
	// Leaves a descendant that writes a marker late; the runner must stop it.
	helperModes["adapter-spawn"] = func(args []string) int {
		self, _ := os.Executable()
		_ = exec.Command(self, "late-write", args[0]).Start()
		fmt.Println(`{"output":1,"cost":0}`)
		return 0
	}
	helperModes["late-write"] = func(args []string) int {
		time.Sleep(time.Second) // outlasts a slow -race adapter exit
		_ = os.WriteFile(args[0], []byte("late"), 0o600)
		return 0
	}
}
