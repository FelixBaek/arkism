package skills

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestFlowImprovementToolsForwardEvidenceWithConfiguredScope(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"id":"flow-improvement-one","revision":1,"improvement":{"stage":"evidence_ready"}}`)
	args := j(t, `{"request_id":"request-one","source_run":{"collection":"crew_runs","id":"run-one","revision":7},"generation":{"event_id":"generation-one","skill_id":"skill-one","path":"/fixture/SKILL.md","lessons":[{"collection":"memory_candidates","id":"lesson-one","revision":3}],"comparison_plan":{"baseline_version":"version-one","dataset_sha256":"fixture-data","environment_sha256":"fixture-env","min_score_gain":0.1,"max_cost":0.2,"max_duration_ms":100}},"runtime":"codex","trial_ends_at":"2026-10-28T10:00:00Z"}`)
	original := asMap(args["generation"])
	out, e := callTool("start_flow_improvement", args)
	if e != nil {
		t.Fatal(e)
	}
	if len(*calls) != 1 || (*calls)[0].path != "/v1/flow/improvements" {
		t.Fatal("start advanced automatically", *calls)
	}
	input := asMap((*calls)[0].payload)
	gen := asMap(input["generation"])
	if !reflect.DeepEqual(input["scope"], host.Scope()) || !reflect.DeepEqual(gen["scope"], host.Scope()) || !reflect.DeepEqual(input["source_run"], args["source_run"]) || !reflect.DeepEqual(gen["comparison_plan"], original["comparison_plan"]) || !reflect.DeepEqual(gen["lessons"], original["lessons"]) {
		t.Fatal("caller evidence or configured scope changed", input)
	}
	if _, changed := original["scope"]; changed || field(structured(t, out), "authority") != "data_only_untrusted" {
		t.Fatal("caller input mutated or authority missing", out)
	}
}

func TestFlowImprovementToolsReadPaginationAndAdvanceOneSuppliedAction(t *testing.T) {
	swapRouter(t)
	calls := fakeRequest(t, `{"items":[],"next_cursor":"flow-next"}`, `{"id":"flow-one","revision":4,"improvement":{"stage":"approval_required"}}`)
	out := call(t, "list_flow_improvements", `{"limit":1,"after":"previous"}`)
	if len(*calls) != 1 || (*calls)[0].payload != nil || !strings.Contains((*calls)[0].path, "&limit=1&after=previous") || field(structured(t, out), "next_cursor") != "flow-next" {
		t.Fatal(*calls, out)
	}
	call(t, "get_flow_improvement", `{"id":"flow-one"}`)
	if (*calls)[1].path != "/v1/flow/improvements/flow-one?"+scopeQueryAll() || (*calls)[1].payload != nil {
		t.Fatal(*calls)
	}
	args := j(t, `{"id":"flow-one","expected_revision":4,"action_id":"recover-one","action":"rollback","recover":true,"application":{"validation_id":"verified-baseline"}}`)
	out, e := callTool("advance_flow_improvement", args)
	if e != nil {
		t.Fatal(e)
	}
	if len(*calls) != 3 || (*calls)[2].path != "/v1/flow/improvements/flow-one/advance" {
		t.Fatal("advance repeated or chose another action", *calls)
	}
	input := asMap((*calls)[2].payload)
	if input["action"] != "rollback" || input["recover"] != true || input["action_id"] != "recover-one" || !reflect.DeepEqual(field(input["application"], "scope"), host.Scope()) || field(input["application"], "validation_id") != "verified-baseline" {
		t.Fatal(input)
	}
	if field(structured(t, out), "authority") != "data_only_untrusted" {
		t.Fatal(out)
	}
}

func TestFlowImprovementToolsDoNotEscalateOrRetryServerDenial(t *testing.T) {
	swapRouter(t)
	calls := 0
	routerRequest = func(path string, payload any) (map[string]any, error) {
		calls++
		if path != "/v1/flow/improvements/flow-one/advance" || field(payload, "action") != "approve" {
			t.Fatal(path, payload)
		}
		return nil, errors.New("Arkism request failed (HTTP 403)")
	}
	_, e := callTool("advance_flow_improvement", j(t, `{"id":"flow-one","expected_revision":3,"action_id":"approve-one","action":"approve"}`))
	if e == nil || calls != 1 {
		t.Fatal("server denial was bypassed or repeated", calls, e)
	}
}

func TestFlowImprovementToolsRejectScopeAndCredentialOverrides(t *testing.T) {
	swapRouter(t)
	routerRequest = func(string, any) (map[string]any, error) { t.Fatal("unexpected server call"); return nil, nil }
	for _, tc := range []struct{ name, args string }{
		{"start_flow_improvement", `{"generation":{"scope":{"workspace_id":"another"}}}`},
		{"start_flow_improvement", `{"generation":null}`},
		{"get_flow_improvement", `{"id":"flow-one","scope":{"workspace_id":"another"}}`},
		{"get_flow_improvement", `{}`},
		{"advance_flow_improvement", `{"id":"flow-one","action":"approve","token":"other"}`},
		{"advance_flow_improvement", `{"id":"flow-one","action":"apply","application":{"scope":{"workspace_id":"another"}}}`},
		{"advance_flow_improvement", `{"id":"flow-one","action":"auto_approve"}`},
		{"list_flow_improvements", `{"limit":101}`},
		{"list_flow_improvements", `{"limit":true}`},
		{"list_flow_improvements", `{"after":3}`},
	} {
		if _, e := callTool(tc.name, j(t, tc.args)); e == nil {
			t.Fatal(tc)
		}
	}
}
