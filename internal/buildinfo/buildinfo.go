// Package buildinfo is what a server build says about itself on /healthz, so the
// desktop app can tell an older server from a current one before calling it.
package buildinfo

import "runtime/debug"

// Version is the release number, set at build time:
// go build -ldflags "-X github.com/FelixBaek/arkism/internal/buildinfo.Version=0.1.7".
// A build without it (go run, tests) reports "dev".
var Version = "dev"

// APILevel rises only when the app starts to need an API an older server lacks.
// Servers from before this field report none, which the app reads as 0.
const APILevel = 1

// Features names API additions the app turns off on a server that does not list them.
func Features() []string {
	return []string{
		"role_model_picks", // POST /v1/providers/role_model
		"forget_both",      // maintenance proposal decision forget_both
	}
}

// Health is the /healthz body. Commit and Built come from the Go toolchain's VCS stamp
// (empty for builds outside a git checkout); the app shows them as "10월 26일 판 · abc1234".
type Health struct {
	Status   string   `json:"status"`
	Version  string   `json:"version"`
	Commit   string   `json:"commit"`
	Built    string   `json:"built"`
	APILevel int      `json:"api_level"`
	Features []string `json:"features"`
}

func Current() Health {
	h := Health{Status: "ok", Version: Version, APILevel: APILevel, Features: Features()}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				h.Commit = s.Value
			case "vcs.time":
				h.Built = s.Value
			}
		}
	}
	return h
}
