package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
	"time"
)

func stubSpaceTools(t *testing.T, names []string, err error) {
	t.Helper()
	stubToolLists(t, spaceToolLists{Space: names}, err, nil)
}

func stubToolLists(t *testing.T, lists spaceToolLists, err error, ext []json.RawMessage) {
	t.Helper()
	old, oldExt := spaceToolsFetch, extToolsFetch
	spaceToolsFetch = func() (spaceToolLists, error) { return lists, err }
	extToolsFetch = func() ([]json.RawMessage, error) { return ext, nil }
	resetSpaceTools()
	t.Cleanup(func() { spaceToolsFetch, extToolsFetch = old, oldExt; resetSpaceTools() })
}

func resetSpaceTools() {
	spaceTools.at, spaceTools.read, spaceTools.space, spaceTools.srv, spaceTools.ext, spaceTools.extNames, spaceTools.unreachable = time.Time{}, false, nil, nil, nil, nil, false
}

func TestSpaceToolAllowlistFiltersListAndCalls(t *testing.T) {
	stubSpaceTools(t, []string{"recall", "router_status"}, nil)
	tools, err := routerTools()
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, raw := range tools {
		var tool struct{ Name string }
		_ = json.Unmarshal(raw, &tool)
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "recall,router_status" && strings.Join(names, ",") != "router_status,recall" {
		t.Fatalf("listed = %v", names)
	}
	if _, err = callTool("remember", map[string]any{"text": "x"}); err == nil || !strings.Contains(err.Error(), "tool_not_enabled") {
		t.Fatalf("remember err = %v", err)
	}
}

func TestSpaceToolAllowlistEmptyKeepsAllUnreadableClosesUnlessOpen(t *testing.T) {
	all := len(routerToolNames())
	stubSpaceTools(t, nil, nil)
	if tools, _ := routerTools(); len(tools) < all-1 {
		t.Fatalf("empty allowlist hid tools: %d of %d", len(tools), all)
	}
	stubSpaceTools(t, nil, errors.New("down"))
	if spaceToolAllowed("remember") {
		t.Fatal("unreadable settings with nothing read before must allow no tool")
	}
	// The refusal names the unreachable server, not the space's settings.
	if _, err := callTool("remember", map[string]any{"text": "x"}); err == nil || !strings.Contains(err.Error(), "tool_not_enabled") || !strings.Contains(err.Error(), "could not be read") {
		t.Fatalf("remember err = %v", err)
	}
	t.Setenv("ARKISM_MCP_TOOLS_ON_ERROR", "open")
	if !spaceToolAllowed("remember") {
		t.Fatal("ARKISM_MCP_TOOLS_ON_ERROR=open keeps every tool")
	}
}

func TestSpaceToolsKeepLastReadOnFailureAndRetrySooner(t *testing.T) {
	stubSpaceTools(t, []string{"recall"}, nil)
	if !spaceToolAllowed("recall") || spaceToolAllowed("remember") {
		t.Fatal("first read")
	}
	spaceToolsFetch = func() (spaceToolLists, error) { return spaceToolLists{}, errors.New("down") }
	spaceTools.at = time.Time{}
	if !spaceToolAllowed("recall") || spaceToolAllowed("remember") {
		t.Fatal("a failed read after a good one keeps the last answer")
	}
	resetSpaceTools()
	calls := 0
	spaceToolsFetch = func() (spaceToolLists, error) { calls++; return spaceToolLists{}, errors.New("down") }
	spaceToolAllowed("recall")
	spaceTools.at = time.Now().Add(-spaceToolsRetry - time.Second)
	spaceToolAllowed("recall")
	if calls != 2 {
		t.Fatalf("never read: asks again after %v, calls=%d", spaceToolsRetry, calls)
	}
}

func TestServerToolAllowlistIntersectsSpace(t *testing.T) {
	stubToolLists(t, spaceToolLists{Space: []string{"recall", "remember"}, Server: []string{"recall", "router_status"}}, nil, nil)
	if !spaceToolAllowed("recall") || spaceToolAllowed("remember") || spaceToolAllowed("router_status") {
		t.Fatal("a tool must pass both the space and the server list")
	}
	stubToolLists(t, spaceToolLists{Server: []string{"router_status"}}, nil, nil)
	if !spaceToolAllowed("router_status") || spaceToolAllowed("recall") {
		t.Fatal("an empty space list leaves the server list in charge")
	}
}

func TestExtensionToolsListedAndCalledThroughServer(t *testing.T) {
	swapRouter(t)
	ext := json.RawMessage(`{"name":"ext_house_lookup","description":"Look up","inputSchema":{"type":"object"}}`)
	stubToolLists(t, spaceToolLists{}, nil, []json.RawMessage{ext})
	tools, err := routerTools()
	if err != nil || string(tools[len(tools)-1]) != string(ext) {
		t.Fatalf("extension tool not listed: %v", err)
	}
	calls := fakeRequest(t, `{"result":{"answer":42},"authority":"data_only_untrusted"}`)
	out, err := callTool("ext_house_lookup", map[string]any{"q": "x"})
	if err != nil || len(*calls) != 1 || (*calls)[0].path != "/v1/extension-tools/call" {
		t.Fatalf("call = %v %v", err, *calls)
	}
	payload := (*calls)[0].payload.(map[string]any)
	if payload["name"] != "ext_house_lookup" || payload["arguments"].(map[string]any)["q"] != "x" {
		t.Fatal(payload)
	}
	if !strings.Contains(fmt.Sprint(structured(t, out)), "data_only_untrusted") {
		t.Fatal(out)
	}
	if _, err := callTool("ext_house_other", map[string]any{}); err == nil {
		t.Fatal("an extension tool the server did not list must be refused")
	}
}

// A refused request says what to try next and still carries its status.
func TestRouterRefusalHintsNextStep(t *testing.T) {
	for code, want := range map[int]string{404: "list or search", 409: "current revision", 403: "ask the user", 418: "(HTTP 418)"} {
		err := error(routerRefusal(code))
		if !strings.Contains(err.Error(), want) || host.HTTPCode(err) != code {
			t.Fatalf("%d: %v", code, err)
		}
	}
}
