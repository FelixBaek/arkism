package capture

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/FelixBaek/arkism/internal/host"
)

// 기억 추출 runs on whichever agent CLI this PC is signed in to, Claude Code first (host.Extractors;
// the server accepts a pick for any of them).
var extractors = host.Extractors

// extractorCLI is the CLI that runs model (claude when no other CLI's names match).
func extractorCLI(model string) string {
	if cli := host.ModelCLI(model); cli == "codex" || cli == "gemini" {
		return cli
	}
	return "claude"
}

// extractorState: "ready" (installed and signed in), "login_needed" or "missing".
func extractorState(cli string) string {
	row, _ := host.CLIByName(cli)
	exe := host.Bin(row.BinEnv, row.BinName)
	if cli == "claude" {
		exe = host.ClaudeBin
	}
	if _, err := exec.LookPath(exe); err != nil {
		return "missing"
	}
	if row.KeyEnv != "" && os.Getenv(row.KeyEnv) != "" {
		return "ready"
	}
	if login := host.CLILogins[cli]; login != "" && !exists(filepath.Join(host.Home, login)) {
		return "login_needed"
	}
	return "ready"
}

// extractorDefault is the first signed-in CLI's model; with none, Claude's (the app says what is missing).
func extractorDefault() string {
	for _, e := range extractors {
		if extractorState(e.CLI) == "ready" {
			return e.Model
		}
	}
	return extractors[0].Model
}

// ExtractorStatus is what `agents sync` reports for 기억 추출: the model that runs when nothing is picked,
// its CLI and whether it can run, and each known CLI's state so the app can say which one to sign in to.
func ExtractorStatus() host.Obj {
	clis := host.Obj{}
	for _, e := range extractors {
		clis.Set(e.CLI, extractorState(e.CLI))
	}
	cli := extractorCLI(capModel)
	state, _ := clis.Get(cli)
	if state == nil {
		state = extractorState(cli)
	}
	return host.Obj{{Key: "model", Value: capModel}, {Key: "cli", Value: cli}, {Key: "state", Value: state}, {Key: "clis", Value: clis}}
}
