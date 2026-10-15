package skills

import (
	"errors"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestCollaborationMCPReadScope(t *testing.T) {
	swapRouter(t)
	for _, tc := range []struct{ name, path, args string }{
		{"recent_sessions", "/v1/work-checkpoints/recent", `{"repository":"/project","session_id":"current","limit":2}`},
		{"recall_for_role", "/v1/memory/role-search", `{"role":"reviewer","query":"test evidence","limit":3}`},
		{"expand_knowledge_graph", "/v1/knowledge-graph/expand", `{"start":{"kind":"task","id":"migration"},"max_depth":2,"limit":10}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := fakeRequest(t, `{"authority":"data_only_untrusted","items":[]}`)
			out := call(t, tc.name, tc.args)
			if structured(t, out).(map[string]any)["authority"] != "data_only_untrusted" || len(*calls) != 1 || (*calls)[0].path != tc.path {
				t.Fatal(out, calls)
			}
			payload := (*calls)[0].payload.(map[string]any)
			if _, ok := payload["scope"]; !ok {
				t.Fatal("missing authenticated client scope")
			}
			args := j(t, tc.args)
			args["scope"] = map[string]any{"workspace_id": "foreign"}
			if _, err := callTool(tc.name, args); err == nil || len(*calls) != 1 {
				t.Fatal("caller scope override accepted")
			}
		})
	}
}

func TestCollaborationMCPHandoff(t *testing.T) {
	swapRouter(t)
	previous := host.RunHandoff
	t.Cleanup(func() { host.RunHandoff = previous })
	host.RunHandoff = func(args map[string]any, request host.Requester) (map[string]any, error) {
		if args["id"] != "handoff-1" {
			t.Fatal(args)
		}
		return map[string]any{"authority": "data_only_untrusted", "status": "completed"}, nil
	}
	call(t, "handoff_work", `{"id":"handoff-1"}`)
	host.RunHandoff = func(map[string]any, host.Requester) (map[string]any, error) {
		return nil, errors.New("evidence revoked")
	}
	if _, err := callTool("handoff_work", map[string]any{"id": "handoff-1"}); err == nil {
		t.Fatal("handoff denial hidden")
	}
}
