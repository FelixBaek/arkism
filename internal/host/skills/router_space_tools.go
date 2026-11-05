package skills

import (
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// The space's MCP tool allowlist (mcp_tools in its settings) narrows what this
// router lists and runs. It is read from the server and kept for a minute; an
// empty list means every tool.
// shortcut: when the server cannot be asked and nothing was read before, every tool stays
// available; the server's own permissions still decide what each call may do.

const spaceToolsTTL = time.Minute

var spaceTools struct {
	mu    sync.Mutex
	at    time.Time
	allow map[string]bool
}

// spaceToolsFetch reads the allowlist of the router's space (nil = every tool).
var spaceToolsFetch = func() ([]string, error) {
	res, err := postJSON(spaceConfigPath(), nil, routerTokenFile, func(code int) error { return fmt.Errorf("space settings unavailable (HTTP %d)", code) })
	if err != nil {
		return nil, err
	}
	config, _ := res["config"].(map[string]any)
	list, _ := config["mcp_tools"].([]any)
	names := []string{}
	for _, v := range list {
		if name := str(v); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// spaceConfigPath is the router space's effective settings route (read_space_config reads it too).
func spaceConfigPath() string {
	scope := host.Scope()
	return "/v1/spaces/" + url.PathEscape(str(scope["workspace_id"])) + "/" + url.PathEscape(str(scope["project_id"])) + "/config/effective"
}

func spaceToolAllowed(name string) bool {
	spaceTools.mu.Lock()
	defer spaceTools.mu.Unlock()
	if time.Since(spaceTools.at) > spaceToolsTTL {
		// A failed read keeps the last answer and waits a full minute before asking again.
		spaceTools.at = time.Now()
		if names, err := spaceToolsFetch(); err == nil {
			spaceTools.allow = nil
			if len(names) > 0 {
				spaceTools.allow = map[string]bool{}
				for _, n := range names {
					spaceTools.allow[n] = true
				}
			}
		}
	}
	return spaceTools.allow == nil || spaceTools.allow[name]
}
