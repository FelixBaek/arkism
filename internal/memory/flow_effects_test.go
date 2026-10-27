package memory

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"sync"
	"testing"
)

func effectWorkflow() Crew {
	return Crew{Type: "workflow", Name: "Approved outbound notice", Handle: "approved-notice", Graph: &CrewGraph{
		Nodes: []CrewNode{{ID: "start", Type: "start"}, {ID: "notice", Type: "notify", Config: map[string]string{"channel": "slack", "to": "ops", "message": "Synthetic approved message"}}, {ID: "end", Type: "end"}},
		Edges: []CrewEdge{{From: "start", To: "notice"}, {From: "notice", To: "end"}},
	}}
}

func effectFixture(t *testing.T) (*fixture, Record, FlowEffectInput) {
	t.Helper()
	f, _ := crewRunFixture(t)
	item := f.crew(effectWorkflow())
	r, e := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: CrewRun{CrewID: item.ID, CrewRevision: item.Revision, Trigger: "call", Status: "running", InvocationMode: "flow", Nodes: map[string]CrewRunNode{"start": {Status: "pending"}, "notice": {Status: "pending"}, "end": {Status: "pending"}}}})
	if e != nil {
		t.Fatal(e)
	}
	lease := leaseFlow(t, f, r.ID)
	st := *r.CrewRun
	st.Nodes = maps.Clone(st.Nodes)
	st.Nodes["start"] = CrewRunNode{Status: "done"}
	st.Nodes["notice"] = CrewRunNode{Status: "running", Runs: 1}
	r, e = f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: r.Revision, Run: st, LeaseToken: lease})
	if e != nil {
		t.Fatal(e)
	}
	return f, r, FlowEffectInput{Scope: scopeA, LeaseToken: lease, NodeID: "notice", Attempt: 1, Adapter: effectAdapter("slack"), TargetHash: ContentHash("synthetic target"), ArgsHash: ContentHash("synthetic arguments"), Preview: "Synthetic approved message"}
}

func prepareEffect(t *testing.T, f *fixture, r Record, in FlowEffectInput) FlowEffectView {
	t.Helper()
	v, e := f.svc.PrepareFlowEffect(testCtx, tokEngine, r.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func approveEffect(t *testing.T, f *fixture, r Record, in FlowEffectInput) Record {
	t.Helper()
	r, e := f.svc.GetCrewRun(testCtx, f.human, scopeA, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	r, e = f.svc.DecideCrewRun(testCtx, f.human, r.ID, CrewRunDecisionInput{Scope: scopeA, ExpectedRevision: r.Revision, NodeID: in.NodeID, Approve: true})
	if e != nil {
		t.Fatal(e)
	}
	st := *r.CrewRun
	st.Nodes = maps.Clone(st.Nodes)
	st.Nodes[in.NodeID] = CrewRunNode{Status: "running", Runs: in.Attempt}
	st.Status, st.Waiting = "running", nil
	r, e = f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: r.Revision, Run: st, LeaseToken: in.LeaseToken})
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func effectClaimInput(in FlowEffectInput, ref FlowEffectView) FlowEffectClaimInput {
	return FlowEffectClaimInput{Scope: in.Scope, LeaseToken: in.LeaseToken, EffectHash: ref.EffectHash, Preview: in.Preview}
}

func TestFlowEffectHumanExactApprovalSingleClaimAndBodyFreeReceipt(t *testing.T) {
	f, run, in := effectFixture(t)
	ref := prepareEffect(t, f, run, in)
	again := prepareEffect(t, f, run, in)
	if ref.ID != again.ID || ref.Status != "awaiting_approval" {
		t.Fatal(ref, again)
	}
	wait, _ := f.svc.GetCrewRun(testCtx, f.human, scopeA, run.ID)
	if wait.CrewRun.Waiting.Question != in.Preview || wait.CrewRun.Waiting.EffectHash != ref.EffectHash {
		t.Fatal(wait.CrewRun.Waiting)
	}
	if _, e := f.svc.DecideCrewRun(testCtx, tokEngine, run.ID, CrewRunDecisionInput{Scope: scopeA, ExpectedRevision: wait.Revision, NodeID: in.NodeID, Approve: true}); !errors.Is(e, ErrDenied) {
		t.Fatal("service approved an effect", e)
	}
	run = approveEffect(t, f, run, in)
	claim := effectClaimInput(in, ref)
	first, e := f.svc.ClaimFlowEffect(testCtx, tokEngine, run.ID, ref.ID, claim)
	if e != nil || !first.DispatchAllowed {
		t.Fatal(first, e)
	}
	second, e := f.svc.ClaimFlowEffect(testCtx, tokEngine, run.ID, ref.ID, claim)
	if e != nil || second.DispatchAllowed {
		t.Fatal(second, e)
	}
	claim.Preview = ""
	settle := FlowEffectSettleInput{FlowEffectClaimInput: claim, FlowEffectReceipt: FlowEffectReceipt{Status: "succeeded", HTTPStatus: 200, ResponseHash: ContentHash("ok")}}
	if _, e = f.svc.SettleFlowEffect(testCtx, tokEngine, run.ID, ref.ID, settle); e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.SettleFlowEffect(testCtx, tokEngine, run.ID, ref.ID, settle); e != nil {
		t.Fatal("same receipt is not idempotent", e)
	}
	for _, col := range []string{colJobs, "audit_events"} {
		b, _ := json.Marshal(f.repo.rows(col))
		if strings.Contains(string(b), in.Preview) {
			t.Fatal("message copied into", col)
		}
	}
}

func TestFlowEffectChangedArgumentsTargetVersionAndPreviewCannotReuseApproval(t *testing.T) {
	f, run, in := effectFixture(t)
	ref := prepareEffect(t, f, run, in)
	for _, change := range []func(*FlowEffectInput){func(i *FlowEffectInput) { i.ArgsHash = ContentHash("changed args") }, func(i *FlowEffectInput) { i.TargetHash = ContentHash("different destination") }, func(i *FlowEffectInput) { i.Adapter = "notify.slack.v2" }, func(i *FlowEffectInput) { i.Preview = "different preview" }} {
		bad := in
		change(&bad)
		if _, e := f.svc.PrepareFlowEffect(testCtx, tokEngine, run.ID, bad); e == nil {
			t.Fatal("changed approval accepted")
		}
	}
	wait, _ := f.svc.GetCrewRun(testCtx, f.human, scopeA, run.ID)
	st := *wait.CrewRun
	w := *st.Waiting
	w.Question = "different preview"
	st.Waiting = &w
	if _, e := f.svc.UpdateCrewRun(testCtx, tokEngine, run.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: wait.Revision, Run: st, LeaseToken: in.LeaseToken}); !errors.Is(e, ErrConflict) {
		t.Fatal("preview replaced", e)
	}
	run = approveEffect(t, f, run, in)
	bad := effectClaimInput(in, ref)
	bad.EffectHash = ContentHash("not the approval")
	if _, e := f.svc.ClaimFlowEffect(testCtx, tokEngine, run.ID, ref.ID, bad); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	bad = effectClaimInput(in, ref)
	bad.LeaseToken = "another-executor-0123456789"
	if _, e := f.svc.ClaimFlowEffect(testCtx, tokEngine, run.ID, ref.ID, bad); !errors.Is(e, ErrConflict) {
		t.Fatal("wrong lease", e)
	}
}

func TestFlowEffectUnknownCannotBecomeSuccessOrNewAttempt(t *testing.T) {
	f, run, in := effectFixture(t)
	ref := prepareEffect(t, f, run, in)
	run = approveEffect(t, f, run, in)
	claim := effectClaimInput(in, ref)
	if _, e := f.svc.ClaimFlowEffect(testCtx, tokEngine, run.ID, ref.ID, claim); e != nil {
		t.Fatal(e)
	}
	claim.Preview = ""
	u := FlowEffectSettleInput{FlowEffectClaimInput: claim, FlowEffectReceipt: FlowEffectReceipt{Status: "unknown"}}
	if _, e := f.svc.SettleFlowEffect(testCtx, tokEngine, run.ID, ref.ID, u); e != nil {
		t.Fatal(e)
	}
	u.FlowEffectReceipt = FlowEffectReceipt{Status: "succeeded", HTTPStatus: 200, ResponseHash: ContentHash("ok")}
	if _, e := f.svc.SettleFlowEffect(testCtx, tokEngine, run.ID, ref.ID, u); !errors.Is(e, ErrConflict) {
		t.Fatal("unknown upgraded without evidence", e)
	}
	st := *run.CrewRun
	st.Nodes = maps.Clone(st.Nodes)
	st.Nodes[in.NodeID] = CrewRunNode{Status: "done", Runs: 1}
	st.Status = "succeeded"
	if _, e := f.svc.UpdateCrewRun(testCtx, tokEngine, run.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: run.Revision, LeaseToken: in.LeaseToken, Run: st}); !errors.Is(e, ErrConflict) {
		t.Fatal("unknown claimed success", e)
	}
	st.Status = "running"
	st.Nodes[in.NodeID] = CrewRunNode{Status: "running", Runs: 2}
	run, e := f.svc.UpdateCrewRun(testCtx, tokEngine, run.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: run.Revision, LeaseToken: in.LeaseToken, Run: st})
	if e != nil {
		t.Fatal(e)
	}
	in.Attempt = 2
	if _, e = f.svc.PrepareFlowEffect(testCtx, tokEngine, run.ID, in); !errors.Is(e, errFlowEffectUnknown) {
		t.Fatal("new attempt bypassed unknown", e)
	}
}

func TestFlowEffectConcurrentClaimsPermitOneDispatch(t *testing.T) {
	f, run, in := effectFixture(t)
	ref := prepareEffect(t, f, run, in)
	run = approveEffect(t, f, run, in)
	var wg sync.WaitGroup
	permits := make(chan bool, 4)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.svc.ClaimFlowEffect(testCtx, tokEngine, run.ID, ref.ID, effectClaimInput(in, ref))
			permits <- r.DispatchAllowed
			errs <- e
		}()
	}
	wg.Wait()
	close(permits)
	close(errs)
	n := 0
	for v := range permits {
		if v {
			n++
		}
	}
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if n != 1 {
		t.Fatal("permits", n)
	}
}

func TestFlowEffectRechecksSourceAndExternalAuthority(t *testing.T) {
	f, run, in := effectFixture(t)
	ref := prepareEffect(t, f, run, in)
	run = approveEffect(t, f, run, in)
	f.svc.Auth = flowChildReadDeny{Authorizer: f.auth, id: run.CrewRun.CrewID}
	if _, e := f.svc.ClaimFlowEffect(testCtx, tokEngine, run.ID, ref.ID, effectClaimInput(in, ref)); !errors.Is(e, ErrDenied) {
		t.Fatal("revoked source", e)
	}
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, a Authorization) (Decision, error) {
		d, e := f.auth.Authorize(ctx, token, a)
		if strings.HasPrefix(a.Context.Destination, "external:") {
			d.ExternalTransfer = false
		}
		return d, e
	})
	if _, e := f.svc.ClaimFlowEffect(testCtx, tokEngine, run.ID, ref.ID, effectClaimInput(in, ref)); !errors.Is(e, ErrDenied) {
		t.Fatal("revoked transfer", e)
	}
}
