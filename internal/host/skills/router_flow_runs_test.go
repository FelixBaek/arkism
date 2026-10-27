package skills

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkflowRunListProjectsOnlyReconnectMetadata(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"items":[{"id":"run-one","created_at":"2026-10-28T01:00:00Z","text":"private body","lease_token":"private lease","crew_run":{"crew_id":"agent-one","status":"succeeded","source_type":"agent","input":"private request","summary":"private answer","definition":{"instructions":"private role"},"definitions":{"child":{"instructions":"private child"}},"nodes":{"end":{"output":"private answer"}},"origin":{"channel_id":"private channel"}}},{"id":"run-child","created_at":"2026-10-28T01:01:00Z","crew_run":{"crew_id":"workflow-two","status":"waiting","source_type":"workflow","parent_id":"run-one","waiting":{"question":"private question"}}}]}`)
	out := call(t, "list_workflow_runs", `{"limit":200,"status":"waiting"}`)
	if len(*calls) != 1 || (*calls)[0].payload != nil || (*calls)[0].path != "/v1/crew/runs?"+scopeQueryAll()+"&brief=1&limit=200&status=waiting" {
		t.Fatal("list executed or changed scope", *calls)
	}
	value := asMap(structured(t, out))
	items := asList(value["items"])
	if value["authority"] != "data_only_untrusted" || len(items) != 2 || len(asMap(items[0])) != 7 || field(items[0], "id") != "run-one" || field(items[0], "result_available") != true || field(items[1], "result_available") != false || field(items[1], "parent_id") != "run-one" {
		t.Fatal(value)
	}
	raw, e := json.Marshal(out)
	if e != nil || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "definitions") {
		t.Fatal("run contents leaked in reconnect list", string(raw), e)
	}
}

func TestWorkflowRunListDoesNotTreatFailedResultAsSuccessOrRestart(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"items":[{"id":"failed-run","crew_run":{"crew_id":"flow","status":"failed"}},{"id":"stopped-run","crew_run":{"crew_id":"flow","status":"stopped"}}]}`)
	items := asList(field(structured(t, call(t, "list_workflow_runs", `{}`)), "items"))
	if len(*calls) != 1 || field(items[0], "status") != "failed" || field(items[1], "status") != "stopped" || field(items[0], "result_available") != true {
		t.Fatal(items, *calls)
	}
}

func TestWorkflowRunListRejectsUnboundedOrScopeOverride(t *testing.T) {
	swapRouter(t)
	routerRequest = func(string, any) (map[string]any, error) { t.Fatal("unexpected server call"); return nil, nil }
	for _, args := range []string{`{"limit":0}`, `{"limit":201}`, `{"limit":true}`, `{"status":"unknown"}`, `{"status":4}`, `{"scope":{"workspace_id":"other"}}`, `{"token":"other"}`} {
		if _, e := callTool("list_workflow_runs", j(t, args)); e == nil {
			t.Fatal(args)
		}
	}
}
