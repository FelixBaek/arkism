package memory

// FLOW contracts use the same production Handler collector as management
// contracts. Model and transport fixtures stay local; no live provider is used.
import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func flowHTTPContractResponses(t *testing.T) []apiContractCase {
	t.Helper()
	c := &contractHTTP{t: t, scope: scopeA, seen: map[string]bool{}}
	contractFlowResults(c)
	contractFlowChildren(c)
	contractFlowAttempts(c)
	contractFlowImprovements(c)
	contractFlowEffects(c)
	if len(c.seen) != 14 {
		t.Fatalf("FLOW contract coverage: %d/14", len(c.seen))
	}
	t.Logf("all %d FLOW operations exercised successfully and anonymously", len(c.seen))
	return c.cases
}

func flowContractCall(c *contractHTTP, op, token string, params map[string]string, body any, extra url.Values) json.RawMessage {
	c.t.Helper()
	method, path, _ := strings.Cut(op, " ")
	for name, value := range params {
		path = strings.ReplaceAll(path, "{"+name+"}", value)
	}
	q := url.Values{}
	if method == "GET" {
		q = url.Values{"workspace_id": {c.scope.WorkspaceID}, "project_id": {c.scope.ProjectID}, "user_id": {c.scope.UserID}, "agent_id": {c.scope.AgentID}}
	}
	for k, v := range extra {
		q[k] = v
	}
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
	}
	c.send(op, method, path, q, raw, "", 401)
	if method == "POST" {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			c.t.Fatal(err)
		}
		fields["unexpected_contract_field"] = json.RawMessage("true")
		malformed, err := json.Marshal(fields)
		if err != nil {
			c.t.Fatal(err)
		}
		c.send(op, method, path, q, malformed, token, 400)
	}
	out := c.send(op, method, path, q, raw, token, 200)
	c.seen[op] = true
	return out
}

func contractFlowResults(c *contractHTTP) {
	f, _, run, delivery := uncertainDelivery(c.t)
	c.f = f
	flowContractCall(c, "GET /v1/flow/capabilities/{id}", f.human, map[string]string{"id": run.CrewRun.CrewID}, nil, nil)
	out := flowContractCall(c, "GET /v1/crew/runs/{id}/result", f.human, map[string]string{"id": run.ID}, nil, nil)
	result := decodeContract[FlowResultView](c.t, out)
	if result.Result == "" || result.Delivery == nil {
		c.t.Fatal("result contract must contain saved report and delivery receipt")
	}
	in := FlowDeliveryRecovery{Scope: scopeA, ExpectedRevision: delivery.Revision, Action: "retry", Confirmed: true}
	flowContractCall(c, "POST /v1/crew/deliveries/{id}/recover", f.human, map[string]string{"id": delivery.ID}, in, nil)
}

func contractFlowChildren(c *contractHTTP) {
	f, h, run, _, _ := nestedFlowFixture(c.t)
	c.f = f
	lease := leaseFlow(c.t, f, run.ID)
	state := *run.CrewRun
	state.Nodes = map[string]CrewRunNode{"start": {Status: "done"}, "delegate": {Status: "running", Runs: 1}, "end": {Status: "pending"}}
	h.call("POST", "/v1/crew/runs/"+run.ID+"/progress", tokEngine, CrewRunUpdate{Scope: scopeA, ExpectedRevision: run.Revision, Run: state, LeaseToken: lease}, 200, &run)
	target := flowDefinitionByHandle(run.CrewRun.Definitions, "fixture-middle")
	in := FlowChildInput{Scope: scopeA, LeaseToken: lease, NodeID: "delegate", Attempt: 1, Kind: "workflow", CrewID: target.ID, Input: "Fixture child task"}
	flowContractCall(c, "POST /v1/crew/runs/{id}/children", tokEngine, map[string]string{"id": run.ID}, in, nil)
	flowContractCall(c, "POST /v1/crew/runs/{id}/children/cancel", tokEngine, map[string]string{"id": run.ID}, FlowChildrenCancel{Scope: scopeA, LeaseToken: lease}, nil)
}

func contractFlowAttempts(c *contractHTTP) {
	f, run, in := flowAttemptFixture(c.t)
	c.f = f
	in.MaxInputTokens, in.MaxOutputTokens = 100, 100
	out := flowContractCall(c, "POST /v1/crew/runs/{id}/attempts", tokEngine, map[string]string{"id": run.ID}, in, nil)
	reservation := decodeContract[FlowAttemptReservation](c.t, out)
	cost := .02
	settle := FlowAttemptSettleInput{Scope: scopeA, LeaseToken: in.LeaseToken, FlowAttemptUsage: FlowAttemptUsage{Status: "succeeded", UsageKnown: true, InputTokens: 10, OutputTokens: 20, CostUSD: &cost}}
	flowContractCall(c, "POST /v1/crew/runs/{id}/attempts/{attempt_id}/settle", tokEngine, map[string]string{"id": run.ID, "attempt_id": reservation.ID}, settle, nil)
}

func contractFlowImprovements(c *contractHTTP) {
	f, _, in, _ := improvementFixture(c.t, false)
	c.f = f
	out := flowContractCall(c, "POST /v1/flow/improvements", f.agent, nil, in, nil)
	v := decodeContract[FlowImprovementView](c.t, out)
	params := map[string]string{"id": v.ID}
	flowContractCall(c, "GET /v1/flow/improvements", f.human, nil, nil, url.Values{"limit": {"1"}})
	flowContractCall(c, "GET /v1/flow/improvements/{id}", f.human, params, nil, nil)
	advance := FlowImprovementAdvance{Scope: scopeA, ExpectedRevision: v.Revision, ActionID: "contract-generate", Action: "generate"}
	flowContractCall(c, "POST /v1/flow/improvements/{id}/advance", f.agent, params, advance, nil)
}

func contractFlowEffects(c *contractHTTP) {
	f, run, in := effectFixture(c.t)
	c.f = f
	params := map[string]string{"id": run.ID}
	out := flowContractCall(c, "POST /v1/crew/runs/{id}/effects", tokEngine, params, in, nil)
	ref := decodeContract[FlowEffectView](c.t, out)
	if ref.Status != "awaiting_approval" || ref.DispatchAllowed {
		c.t.Fatal("preparation bypassed human approval", ref)
	}
	approveEffect(c.t, f, run, in)
	params["effect_id"] = ref.ID
	claim := effectClaimInput(in, ref)
	out = flowContractCall(c, "POST /v1/crew/runs/{id}/effects/{effect_id}/claim", tokEngine, params, claim, nil)
	if !decodeContract[FlowEffectView](c.t, out).DispatchAllowed {
		c.t.Fatal("approved effect did not allow its first dispatch")
	}
	out = flowContractCall(c, "POST /v1/crew/runs/{id}/effects/{effect_id}/claim", tokEngine, params, claim, nil)
	if decodeContract[FlowEffectView](c.t, out).DispatchAllowed {
		c.t.Fatal("duplicate claim allowed another dispatch")
	}
	claim.Preview = ""
	settle := FlowEffectSettleInput{FlowEffectClaimInput: claim, FlowEffectReceipt: FlowEffectReceipt{Status: "succeeded", HTTPStatus: 200, ResponseHash: ContentHash("synthetic response")}}
	flowContractCall(c, "POST /v1/crew/runs/{id}/effects/{effect_id}/settle", tokEngine, params, settle, nil)
}
