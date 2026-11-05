package skills

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// The space's MCP tool allowlist (mcp_tools in its settings) and the server's
// (ARKISM_MCP_TOOLS) narrow what this router lists and runs: a tool must pass
// both, and an empty list allows every tool. They are read from the server and
// kept for a minute. When the server cannot be asked and nothing was read
// before, no tool is available (asking again after 10 seconds);
// ARKISM_MCP_TOOLS_ON_ERROR=open keeps every tool instead, the old behaviour.
//
// Extension tools (ext_*) come from the server too, already filtered by both lists.

const (
	spaceToolsTTL   = time.Minute
	spaceToolsRetry = 10 * time.Second
)

type spaceToolLists struct {
	Space, Server []string
}

var spaceTools struct {
	mu          sync.Mutex
	at          time.Time
	read        bool
	space, srv  map[string]bool // nil = every tool
	ext         []json.RawMessage
	extNames    map[string]bool
	unreachable bool
}

// spaceToolsFetch reads the allowlists of the router's space.
var spaceToolsFetch = func() (spaceToolLists, error) {
	res, err := postJSON(spaceConfigPath(), nil, routerTokenFile, func(code int) error { return fmt.Errorf("space settings unavailable (HTTP %d)", code) })
	if err != nil {
		return spaceToolLists{}, err
	}
	config, _ := res["config"].(map[string]any)
	return spaceToolLists{Space: strList(config["mcp_tools"]), Server: strList(res["server_mcp_tools"])}, nil
}

// extToolsFetch reads the extension tools this space may use; a server without extensions has none.
var extToolsFetch = func() ([]json.RawMessage, error) {
	res, err := routerRequest("/v1/extension-tools?"+scopeQueryAll(), nil)
	if err != nil {
		return nil, err
	}
	out := []json.RawMessage{}
	for _, raw := range asList(getOr(res, "tools", []any{})) {
		t := asMap(raw)
		if name := str(t["name"]); strings.HasPrefix(name, "ext_") {
			b, err := json.Marshal(host.Obj{{Key: "name", Value: name}, {Key: "description", Value: str(t["description"])}, {Key: "inputSchema", Value: getOr(t, "inputSchema", map[string]any{"type": "object"})}})
			if err != nil {
				return nil, err
			}
			out = append(out, b)
		}
	}
	return out, nil
}

func strList(v any) []string {
	names := []string{}
	list, _ := v.([]any)
	for _, x := range list {
		if name := str(x); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func nameSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return set
}

// spaceConfigPath is the router space's effective settings route (read_space_config reads it too).
func spaceConfigPath() string {
	scope := host.Scope()
	return "/v1/spaces/" + url.PathEscape(str(scope["workspace_id"])) + "/" + url.PathEscape(str(scope["project_id"])) + "/config/effective"
}

// refreshSpaceTools rereads the lists when they are old; the caller holds the lock.
func refreshSpaceTools() {
	wait := spaceToolsTTL
	if !spaceTools.read {
		wait = spaceToolsRetry
	}
	if time.Since(spaceTools.at) <= wait {
		return
	}
	// A failed read keeps the last answer.
	spaceTools.at = time.Now()
	lists, err := spaceToolsFetch()
	spaceTools.unreachable = err != nil && !spaceTools.read
	if err != nil {
		return
	}
	spaceTools.read, spaceTools.space, spaceTools.srv = true, nameSet(lists.Space), nameSet(lists.Server)
	if ext, err := extToolsFetch(); err == nil {
		spaceTools.ext, spaceTools.extNames = ext, map[string]bool{}
		for _, raw := range ext {
			var t struct{ Name string }
			_ = json.Unmarshal(raw, &t) // marshalled by extToolsFetch
			spaceTools.extNames[t.Name] = true
		}
	}
}

func spaceToolAllowed(name string) bool {
	spaceTools.mu.Lock()
	defer spaceTools.mu.Unlock()
	refreshSpaceTools()
	if spaceTools.unreachable {
		return os.Getenv("ARKISM_MCP_TOOLS_ON_ERROR") == "open"
	}
	return (spaceTools.space == nil || spaceTools.space[name]) && (spaceTools.srv == nil || spaceTools.srv[name])
}

// extensionTools is the cached extension tool list (the server filtered it by both allowlists).
func extensionTools() []json.RawMessage {
	spaceTools.mu.Lock()
	defer spaceTools.mu.Unlock()
	refreshSpaceTools()
	return spaceTools.ext
}

func extensionToolKnown(name string) bool {
	spaceTools.mu.Lock()
	defer spaceTools.mu.Unlock()
	refreshSpaceTools()
	return spaceTools.extNames[name]
}

// callExtensionTool runs an extension tool on the server, which checks the allowlists and permission again.
func callExtensionTool(name string, args map[string]any) (host.Obj, error) {
	res, err := routerRequest("/v1/extension-tools/call", map[string]any{"scope": host.Scope(), "name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	return toolText(host.Obj{{Key: "result", Value: getOr(res, "result", nil)}, {Key: "authority", Value: "data_only_untrusted"}})
}
