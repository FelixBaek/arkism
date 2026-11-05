package skills

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func stubSpaceTools(t *testing.T, names []string, err error) {
	t.Helper()
	old := spaceToolsFetch
	spaceToolsFetch = func() ([]string, error) { return names, err }
	spaceTools.at, spaceTools.allow = time.Time{}, nil
	t.Cleanup(func() { spaceToolsFetch, spaceTools.at, spaceTools.allow = old, time.Time{}, nil })
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

func TestSpaceToolAllowlistEmptyOrUnreadableKeepsAll(t *testing.T) {
	all := len(routerToolNames())
	stubSpaceTools(t, nil, nil)
	if tools, _ := routerTools(); len(tools) < all-1 {
		t.Fatalf("empty allowlist hid tools: %d of %d", len(tools), all)
	}
	stubSpaceTools(t, nil, errors.New("down"))
	if !spaceToolAllowed("remember") {
		t.Fatal("unreadable settings hid a tool")
	}
}
