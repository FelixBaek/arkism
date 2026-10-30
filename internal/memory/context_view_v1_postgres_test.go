package memory

import (
	"reflect"
	"testing"
)

func viewPGInput(p *nativePGFixture) ContextBuildInput {
	return ContextBuildInput{Scope: p.in.Scope, Task: "searchcanary", TokenEncoding: "cl100k_base", Modes: []string{"exact", "fts"}}
}
func TestContextViewPostgresHTTPAndReaderPhases(t *testing.T) {
	p, r, _ := nativeSearchPGFixture(t)
	in := viewPGInput(p)
	memory, e := p.admin.Get(testCtx, colMemories, p.in.Scope, p.in.Target.ID)
	if e != nil {
		t.Fatal(e)
	}
	in.ContextView = excludeView(in.Scope, memory)
	beforeState := nativeSearchHash("state", p.rows(t, "state"))
	beforeConsume := nativeSearchHash("consume", p.rows(t, "consumption"))
	original, e := p.admin.Get(testCtx, colMemories, p.in.Scope, memory.ID)
	if e != nil {
		t.Fatal(e)
	}
	off, code := contextViewHTTP(t, p.worker, p.workerToken, in)
	if code != 200 || len(off.Items) != 0 {
		t.Fatal("PG current exclusion", code)
	}
	p.worker.NativeSearchShadow = r
	on, code := contextViewHTTP(t, p.worker, p.workerToken, in)
	if code != 200 {
		t.Fatal(code)
	}
	off.ContextID = ""
	on.ContextID = ""
	if !reflect.DeepEqual(off, on) {
		t.Fatal("PG shadow changed response")
	}
	viewReadPack(t, "same", "PG current", on)
	for _, phase := range []string{"next", "new-session"} {
		in.ContextView = nil
		got, status := contextViewHTTP(t, p.worker, p.workerToken, in)
		if status != 200 || !viewHas(got.Items, memory.ID) {
			t.Fatal("PG phase", phase, status)
		}
		viewReadPack(t, phase, "PG unmasked", got)
	}
	current, e := p.admin.Get(testCtx, colMemories, p.in.Scope, memory.ID)
	if e != nil || structuredText(original) != structuredText(current) || beforeState != nativeSearchHash("state", p.rows(t, "state")) || beforeConsume != nativeSearchHash("consume", p.rows(t, "consumption")) {
		t.Fatal("view wrote canonical/native", e)
	}
}
func TestContextViewPostgresFinalFenceAtExactRef(t *testing.T) {
	p, r, _ := nativeSearchPGFixture(t)
	p.worker.NativeSearchShadow = r
	in := viewPGInput(p)
	in.ContextView = &ContextInvocationView{Protocol: ContextViewProtocol, ContextScope: in.Scope, Exclude: []Ref{}}
	p.worker.Repo = &nativeSearchBeforeTxRepo{Repository: p.workerRepo, before: func() { nativeSearchInjectPGFault(t, p, nativeSearchTestInput(), "revision") }}
	got, e := p.worker.BuildContext(testCtx, p.workerToken, in)
	if e == nil || got.ContextData != "" {
		t.Fatal("PG final fence", e)
	}
	v := r.StagesV2()
	if len(v) != 1 || v[0].BaselineReturned || len(v[0].FinalRejected) != 1 || v[0].FinalRejected[0].ScopedRefSHA != contextScopedRefSHA(in.Scope, p.in.Target) {
		t.Fatal("PG per-ref final rejection", v)
	}
}
func TestContextViewPostgresPersistentRejectAndForeignScope(t *testing.T) {
	p, r, _ := nativeSearchPGFixture(t)
	p.worker.NativeSearchShadow = r
	in := viewPGInput(p)
	// Existing explicit human transition, not a new context-view writer. The service adapter uses its existing authorized human fixture.
	f := newFixture(t)
	f.svc.Repo = p.admin
	f.svc.Embedder = nil
	m := f.get(f.human, colMemories, p.in.Scope, p.in.Target.ID)
	if _, e := f.svc.Transition(testCtx, f.agent, colMemories, m.ID, ChangeInput{Scope: m.Scope, ExpectedRevision: m.Revision, Status: "rejected"}); e == nil {
		t.Fatal("agent durable transition")
	}
	if _, e := f.svc.Transition(testCtx, f.human, colMemories, m.ID, ChangeInput{Scope: m.Scope, ExpectedRevision: m.Revision, Status: "rejected"}); e != nil {
		t.Fatal(e)
	}
	for _, phase := range []string{"current", "next", "new-session"} {
		got, e := p.worker.BuildContext(testCtx, p.workerToken, in)
		if e != nil || viewHas(got.Items, m.ID) {
			t.Fatal("persistent reject", phase, e)
		}
	}
	in.ContextView = &ContextInvocationView{Protocol: ContextViewProtocol, ContextScope: scopeB, Exclude: []Ref{p.in.Target}}
	if _, e := p.worker.BuildContext(testCtx, p.workerToken, in); e == nil {
		t.Fatal("foreign view scope accepted")
	}
}

func viewPGHumanFixture(t *testing.T) *fixture {
	t.Helper()
	admin := isolatedPostgres(t)
	app := restrictedPostgres(t, admin)
	actions := []string{"db:read", "db:write", "write:sources", "write:memory_candidates", "read:sources", "read:memory_candidates", "read:memories", "read:memory_relations", "read:jobs", "search:memories", "context:read", "transition:memory_candidates", "transition:memories", "transition:memory_relations", "write:memory_relations", "delete:memories", "delete:memory_candidates", "write:jobs"}
	humanToken := testPrincipalToken("view-pg-human-" + ID())
	agentToken := testPrincipalToken("view-pg-agent-" + ID())
	for _, grant := range []PrincipalGrant{{PrincipalID: "view-human", Role: roleHuman, Token: humanToken, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: actions}, {PrincipalID: "view-agent", Role: roleAgent, Token: agentToken, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: actions}} {
		if e := admin.ProvisionPrincipal(testCtx, grant); e != nil {
			t.Fatal(e)
		}
	}
	repo, e := app.WithToken(humanToken)
	if e != nil {
		t.Fatal(e)
	}
	auth, e := NewPostgresAuthorizer(app)
	if e != nil {
		t.Fatal(e)
	}
	f := newFixture(t)
	f.human = humanToken
	f.agent = agentToken
	f.svc = &Service{Repo: repo, Auth: auth, CandidateDailyLimit: 50}
	return f
}
func TestContextViewPostgresHumanCorrectionAndWithdrawal(t *testing.T) {
	for _, shadow := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "shadow"}[shadow], func(t *testing.T) {
			f := viewPGHumanFixture(t)
			old := viewMemory(t, f, scopeA, "viewcanary old lasting preference", "view:old")
			if shadow {
				f.svc.NativeSearchShadow = nativeSearchTestRuntime(t, "B0")
			}
			candidate, held := viewPendingCorrection(t, f, old)
			viewHeldPhases(t, f, held)
			corrected := viewConfirmCorrection(t, f, candidate, held)
			viewCorrectedPhases(t, f, old, corrected)
			viewWithdrawPhases(t, f, corrected)
			foreign := viewInput(scopeB)
			if _, e := f.svc.BuildContext(testCtx, f.human, foreign); e == nil {
				t.Fatal("foreign project read granted")
			}
		})
	}
}
