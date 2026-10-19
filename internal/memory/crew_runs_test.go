package memory

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const tokEngine, tokPipeline = "tok-engine", "tok-pipeline"

func crewRunFixture(t *testing.T) (*fixture, Record) {
	t.Helper()
	f := newFixture(t)
	f.auth.actors[tokEngine] = actor("crew-engine", "service", true, "read", "write")
	f.auth.actors[tokPipeline] = actor("pipeline-1", "pipeline", true, allVerbs...)
	w := crewWorkflow("아침 브리핑", "아침브리핑")
	w.Trigger, w.Enabled = &CrewTrigger{Kind: "schedule", Cron: "0 8 * * *"}, true
	return f, f.crew(w)
}

func newCrewRun(crewID string) CrewRun {
	return CrewRun{CrewID: crewID, CrewRevision: 1, Trigger: "schedule", Status: "running",
		Nodes: map[string]CrewRunNode{"start": {Status: "pending"}, "a": {Status: "pending"}, "ok": {Status: "pending"}, "end": {Status: "pending"}}}
}

func TestCrewRunCreateUpdateList(t *testing.T) {
	f, auto := crewRunFixture(t)
	if _, err := f.svc.CreateCrewRun(testCtx, tokPipeline, CrewRunInput{Scope: scopeA, Run: newCrewRun(auto.ID)}); !errors.Is(err, ErrDenied) {
		t.Fatal("a pipeline started a run", err)
	}
	if _, err := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: newCrewRun("no-such-crew")}); !errors.Is(err, ErrNotFound) {
		t.Fatal("run of a missing crew item", err)
	}
	for _, c := range []Crew{crewTemplate("리뷰어"), {Type: "persona", Name: "편집자", Instructions: "차분하게"}} {
		it := f.crew(c)
		if _, err := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: newCrewRun(it.ID)}); !errors.Is(err, ErrInvalid) {
			t.Fatal("run of a non-workflow accepted", c.Type, err)
		}
	}
	r, err := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: newCrewRun(auto.ID)})
	if err != nil || r.CrewRun.Status != "running" || r.CreatedByType != "service" {
		t.Fatal("create", r, err)
	}
	run := *r.CrewRun
	now := time.Now().UTC().Truncate(time.Millisecond)
	run.Nodes = map[string]CrewRunNode{"start": {Status: "done", StartedAt: &now, FinishedAt: &now, Output: `{"x":1}`},
		"a": {Status: "running", StartedAt: &now}, "ok": {Status: "pending"}, "end": {Status: "pending"}}
	up, err := f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: 1, Run: run})
	if err != nil || up.Revision != 2 || up.CrewRun.Nodes["start"].Output != `{"x":1}` {
		t.Fatal("update", up, err)
	}
	if _, err = f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: 1, Run: run}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision accepted", err)
	}
	moved := run
	moved.Trigger = "webhook"
	if _, err = f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: 2, Run: moved}); !errors.Is(err, ErrInvalid) {
		t.Fatal("trigger changed after start", err)
	}
	time.Sleep(2 * time.Millisecond) // records order by millisecond time; keep "newest" unambiguous
	other, _ := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: newCrewRun(auto.ID)})
	page, err := f.svc.ListCrewRuns(testCtx, f.agent, scopeA, auto.ID, "", 10)
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != other.ID {
		t.Fatal("list newest first", page.Items, err)
	}
	if page, _ = f.svc.ListCrewRuns(testCtx, f.agent, scopeA, "", "succeeded", 10); len(page.Items) != 0 {
		t.Fatal("status filter", page.Items)
	}
	done := *up.CrewRun
	done.Status, done.Summary = "succeeded", "끝"
	fin, err := f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: 2, Run: done})
	if err != nil {
		t.Fatal("finish", err)
	}
	if _, err = f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: fin.Revision, Run: done}); !errors.Is(err, ErrConflict) {
		t.Fatal("finished run changed", err)
	}
	got, err := f.svc.GetCrewRun(testCtx, f.agent, scopeA, r.ID)
	if err != nil || got.CrewRun.Summary != "끝" {
		t.Fatal("get", got, err)
	}
}

func TestCrewRunRejectsBadShapesAndSecrets(t *testing.T) {
	f, auto := crewRunFixture(t)
	bad := []func(c *CrewRun){
		func(c *CrewRun) { c.Trigger = "cron" },
		func(c *CrewRun) { c.Status = "paused" },
		func(c *CrewRun) { c.CrewRevision = 0 },
		func(c *CrewRun) { c.Trigger = "caller" },
		func(c *CrewRun) { c.Nodes = map[string]CrewRunNode{"a": {Status: "pending", Runs: -1}} },
		func(c *CrewRun) { c.Loops = map[string]int{"a>b:": 10001} },
		func(c *CrewRun) { c.Loops = map[string]int{strings.Repeat("k", 201): 1} },
		func(c *CrewRun) { c.Nodes = map[string]CrewRunNode{"a": {Status: "half"}} },
		func(c *CrewRun) {
			c.Nodes = map[string]CrewRunNode{"a": {Status: "done", Output: strings.Repeat("가", 20001)}}
		},
		func(c *CrewRun) {
			c.Nodes = map[string]CrewRunNode{"a": {Status: "failed", Error: strings.Repeat("e", 501)}}
		},
		func(c *CrewRun) { c.Waiting = &CrewRunWaiting{NodeID: "a", Kind: "approval"} },
	}
	for i, mutate := range bad {
		c := newCrewRun(auto.ID)
		mutate(&c)
		if _, err := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: c}); !errors.Is(err, ErrInvalid) {
			t.Fatal("bad run accepted", i, err)
		}
	}
	r, _ := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: newCrewRun(auto.ID)})
	run := *r.CrewRun
	run.Nodes = map[string]CrewRunNode{"a": {Status: "done", Output: `{"text":"키는 sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`}}
	if _, err := f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: 1, Run: run}); !errors.Is(err, ErrSecretDetected) {
		t.Fatal("a key in a node output was stored", err)
	}
	run.Nodes = map[string]CrewRunNode{"a": {Status: "waiting"}}
	run.Status, run.Waiting = "waiting", &CrewRunWaiting{NodeID: "a", Kind: "callback", CallbackHash: "not-a-hash"}
	if _, err := f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: 1, Run: run}); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad callback hash accepted", err)
	}
}

func TestCrewRunDecisionHumanOnlyWhileWaiting(t *testing.T) {
	f, auto := crewRunFixture(t)
	r, _ := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: newCrewRun(auto.ID)})
	decide := func(token string, rev int64, node string) (Record, error) {
		return f.svc.DecideCrewRun(testCtx, token, r.ID, CrewRunDecisionInput{Scope: scopeA, ExpectedRevision: rev, NodeID: node, Approve: true, Note: "배포해도 됨"})
	}
	if _, err := decide(f.human, 1, "ok"); !errors.Is(err, ErrConflict) {
		t.Fatal("decision on a running run", err)
	}
	run := *r.CrewRun
	run.Status = "waiting"
	run.Nodes = map[string]CrewRunNode{"start": {Status: "done"}, "a": {Status: "done"}, "ok": {Status: "waiting"}, "end": {Status: "pending"}}
	run.Waiting = &CrewRunWaiting{NodeID: "ok", Kind: "approval", Question: "배포할까요?"}
	w, err := f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: 1, Run: run})
	if err != nil {
		t.Fatal("wait", err)
	}
	for _, tok := range []string{f.agent, tokEngine} {
		if _, err = decide(tok, w.Revision, "ok"); !errors.Is(err, ErrDenied) {
			t.Fatal("a non-human approved", tok, err)
		}
	}
	if _, err = decide(f.human, w.Revision, "a"); !errors.Is(err, ErrConflict) {
		t.Fatal("decision on a node not waiting", err)
	}
	d, err := decide(f.human, w.Revision, "ok")
	if err != nil || !d.CrewRun.Decisions["ok"].Approve || d.CrewRun.Decisions["ok"].DecidedBy != "human-1" {
		t.Fatal("decide", d, err)
	}
	if _, err = decide(f.human, d.Revision, "ok"); !errors.Is(err, ErrConflict) {
		t.Fatal("second decision accepted", err)
	}
	// The engine cannot forge or drop a decision through an update.
	forged := *d.CrewRun
	forged.Decisions = map[string]CrewRunDecision{"ok": {Approve: false, DecidedBy: "crew-engine"}}
	up, err := f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: d.Revision, Run: forged})
	if err != nil || !up.CrewRun.Decisions["ok"].Approve {
		t.Fatal("update changed a decision", up.CrewRun.Decisions, err)
	}
}

func TestCrewRunHTTPRoutesAndAudit(t *testing.T) {
	f, auto := crewRunFixture(t)
	h := Handler(f.svc)
	call := func(method, path, token string, body any) *httptest.ResponseRecorder {
		var raw string
		if body != nil {
			b, _ := json.Marshal(body)
			raw = string(b)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	w := call("POST", "/v1/crew/runs", tokEngine, CrewRunInput{Scope: scopeA, Run: newCrewRun(auto.ID)})
	var rec Record
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rec) != nil || rec.CrewRun == nil {
		t.Fatal("create over http", w.Code, w.Body.String())
	}
	if w = call("GET", "/v1/crew/runs?workspace_id=ws-a&project_id=proj-a&crew_id="+auto.ID+"&limit=5", f.agent, nil); w.Code != 200 || !strings.Contains(w.Body.String(), rec.ID) {
		t.Fatal("list over http", w.Code, w.Body.String())
	}
	if w = call("GET", "/v1/crew/runs?workspace_id=ws-a&project_id=proj-a&limit=x", f.agent, nil); w.Code != 400 {
		t.Fatal("bad limit", w.Code)
	}
	if w = call("GET", "/v1/crew/runs/"+rec.ID+"?workspace_id=ws-a&project_id=proj-a", f.agent, nil); w.Code != 200 {
		t.Fatal("get over http", w.Code)
	}
	if w = call("POST", "/v1/crew/runs/"+rec.ID+"/decision", f.agent, CrewRunDecisionInput{Scope: scopeA, ExpectedRevision: 1, NodeID: "ok", Approve: true}); w.Code != 403 {
		t.Fatal("agent decision over http", w.Code)
	}
	run := *rec.CrewRun
	run.Nodes["start"] = CrewRunNode{Status: "done", Output: `"hello"`}
	if w = call("POST", "/v1/crew/runs/"+rec.ID+"/progress", tokEngine, CrewRunUpdate{Scope: scopeA, ExpectedRevision: 1, Run: run}); w.Code != 200 {
		t.Fatal("update over http", w.Code, w.Body.String())
	}
	// Audit rows name the action and the run only, never outputs.
	for _, a := range f.repo.rows("audit_events") {
		if strings.Contains(a.Kind, "crew_runs") && (strings.Contains(a.Text, "hello") || a.CrewRun != nil) {
			t.Fatal("run body in audit", a)
		}
	}
}

func TestCrewRunProgressCannotSkipApproval(t *testing.T) {
	f, auto := crewRunFixture(t)
	r, err := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: newCrewRun(auto.ID)})
	if err != nil {
		t.Fatal(err)
	}
	approved := *r.CrewRun
	approved.Nodes = map[string]CrewRunNode{"start": {Status: "done"}, "a": {Status: "done"}, "ok": {Status: "done", Port: "approve"}, "end": {Status: "pending"}}
	if _, err = f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: 1, Run: approved}); !errors.Is(err, ErrInvalid) {
		t.Fatal("approval finished without a decision", err)
	}
	past := time.Now().Add(-time.Hour)
	timer := *r.CrewRun
	timer.Status, timer.Waiting = "waiting", &CrewRunWaiting{NodeID: "ok", Kind: "wait", ResumeAt: &past}
	timer.Nodes = map[string]CrewRunNode{"start": {Status: "done"}, "a": {Status: "done"}, "ok": {Status: "waiting"}, "end": {Status: "pending"}}
	if _, err = f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: 1, Run: timer}); !errors.Is(err, ErrInvalid) {
		t.Fatal("approval node turned into a timer", err)
	}
	asks := *r.CrewRun
	asks.Status, asks.Waiting = "waiting", &CrewRunWaiting{NodeID: "ok", Kind: "approval", Question: "보낼까요?"}
	asks.Nodes = map[string]CrewRunNode{"start": {Status: "done"}, "a": {Status: "done"}, "ok": {Status: "waiting"}, "end": {Status: "pending"}}
	if _, err = f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: 1, Run: asks}); err != nil {
		t.Fatal("waiting for approval refused", err)
	}
}

func TestCrewRunOpenRunsOutliveNewerFinishedOnes(t *testing.T) {
	f, auto := crewRunFixture(t)
	open, err := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: newCrewRun(auto.ID)})
	if err != nil {
		t.Fatal(err)
	}
	// A thousand runs that finished after it would push it out of a plain newest-1000 page.
	later := open.UpdatedAt.Add(time.Second)
	for i := 0; i < 1001; i++ {
		run := newCrewRun(auto.ID)
		run.Status = "succeeded"
		r := Record{ID: ID(), Scope: scopeA, Kind: colCrewRuns, Status: statusActive, Revision: 1, CrewRun: &run, CreatedAt: later, UpdatedAt: later}
		f.repo.store[colCrewRuns][recKey(scopeA, r.ID)] = r
	}
	page, err := f.svc.ListCrewRuns(testCtx, tokEngine, scopeA, "", "running", 50)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != open.ID {
		t.Fatal("an open run fell out of the list", len(page.Items), err)
	}
}

// A decided approval node must carry the decision itself in its output, not
// only its port; a back edge that sends it to pending again needs a new decision.
func TestCrewRunApprovalOutputAndLoopBack(t *testing.T) {
	f, w := crewRunFixture(t)
	r, err := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: newCrewRun(w.ID)})
	if err != nil {
		t.Fatal(err)
	}
	wait := *r.CrewRun
	wait.Status, wait.Waiting = "waiting", &CrewRunWaiting{NodeID: "ok", Kind: "approval"}
	wait.Nodes = map[string]CrewRunNode{"start": {Status: "done"}, "a": {Status: "done"}, "ok": {Status: "waiting"}, "end": {Status: "pending"}}
	wr, err := f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: 1, Run: wait})
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.svc.DecideCrewRun(testCtx, f.human, r.ID, CrewRunDecisionInput{Scope: scopeA, ExpectedRevision: wr.Revision, NodeID: "ok", Approve: false, Note: "아직"})
	if err != nil {
		t.Fatal(err)
	}
	next := *d.CrewRun
	next.Status, next.Waiting = "running", nil
	next.Nodes = map[string]CrewRunNode{"start": {Status: "done"}, "a": {Status: "done"}, "ok": {Status: "done", Port: "reject", Output: `{"approved":true,"note":"아직"}`}, "end": {Status: "pending"}}
	if _, err = f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: d.Revision, Run: next}); !errors.Is(err, ErrInvalid) {
		t.Fatal("output said approved after a rejection", err)
	}
	// The reject port goes back to "a": "a" and "ok" are pending again, run twice so far.
	next.Nodes = map[string]CrewRunNode{"start": {Status: "done", Runs: 1}, "a": {Status: "pending", Runs: 1}, "ok": {Status: "pending", Runs: 1}, "end": {Status: "pending"}}
	next.Loops = map[string]int{"ok>a:reject": 1}
	back, err := f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: d.Revision, Run: next})
	if err != nil || back.CrewRun.Decisions["ok"].DecidedBy != "" || back.CrewRun.Loops["ok>a:reject"] != 1 || back.CrewRun.Nodes["a"].Runs != 1 {
		t.Fatal("loop back kept the old decision", back.CrewRun, err)
	}
	wait.Nodes["a"] = CrewRunNode{Status: "done", Runs: 2}
	wait.Loops = next.Loops
	wr, err = f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: back.Revision, Run: wait})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.DecideCrewRun(testCtx, f.human, r.ID, CrewRunDecisionInput{Scope: scopeA, ExpectedRevision: wr.Revision, NodeID: "ok", Approve: true}); err != nil {
		t.Fatal("the second round could not be decided", err)
	}
}

// 실패한 곳부터 다시: a person may start a run with the finished nodes of a
// failed one; a copied approval carries that person's decision.
func TestCrewRunResumedFromFinishedNodes(t *testing.T) {
	f, w := crewRunFixture(t)
	resumed := newCrewRun(w.ID)
	resumed.Trigger = "manual"
	resumed.Nodes = map[string]CrewRunNode{"start": {Status: "done", Output: `{"request":"x"}`}, "a": {Status: "done", Output: `"초안"`},
		"ok": {Status: "done", Port: "approve", Output: `{"approved":true,"note":"좋아요"}`}, "end": {Status: "pending"}}
	if _, err := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: resumed}); !errors.Is(err, ErrDenied) {
		t.Fatal("the engine started past finished nodes", err)
	}
	r, err := f.svc.CreateCrewRun(testCtx, f.human, CrewRunInput{Scope: scopeA, Run: resumed})
	if err != nil || r.CrewRun.Nodes["a"].Output != `"초안"` || !r.CrewRun.Decisions["ok"].Approve || r.CrewRun.Decisions["ok"].DecidedBy != "human-1" {
		t.Fatal("resumed run", r.CrewRun, err)
	}
	// The engine's progress keeps the copied approval valid.
	run := *r.CrewRun
	run.Nodes["end"] = CrewRunNode{Status: "done"}
	run.Status = "succeeded"
	if _, err = f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: 1, Run: run}); err != nil {
		t.Fatal("progress after a copied approval", err)
	}
	for i, n := range []CrewRunNode{{Status: "running"}, {Status: "failed"}, {Status: "done", Port: "approve", Output: `{"approved":false}`}, {Status: "done", Port: "approve"}} {
		bad := resumed
		bad.Nodes = map[string]CrewRunNode{"start": {Status: "done"}, "a": {Status: "done"}, "ok": n, "end": {Status: "pending"}}
		if _, err = f.svc.CreateCrewRun(testCtx, f.human, CrewRunInput{Scope: scopeA, Run: bad}); !errors.Is(err, ErrInvalid) {
			t.Fatal("bad resumed run accepted", i, err)
		}
	}
}

func TestOneNodeRunsArePeopleOnly(t *testing.T) {
	f, auto := crewRunFixture(t)
	run := newCrewRun(auto.ID)
	for id := range run.Nodes {
		run.Only = id
		break
	}
	if _, err := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: run}); !errors.Is(err, ErrDenied) {
		t.Fatal("the engine asked for a one-node run", err)
	}
	r, err := f.svc.CreateCrewRun(testCtx, f.human, CrewRunInput{Scope: scopeA, Run: run})
	if err != nil {
		t.Fatal("a person's one-node run", err)
	}
	changed := *r.CrewRun
	changed.Only = ""
	if _, err = f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: r.Revision, Run: changed}); !errors.Is(err, ErrInvalid) {
		t.Fatal("only was changed after creation", err)
	}
	bad := run
	bad.Only = "no-such-node"
	if _, err = f.svc.CreateCrewRun(testCtx, f.human, CrewRunInput{Scope: scopeA, Run: bad}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown node accepted", err)
	}
}

func TestWorkflowOfAnAgentRunsOnlyThroughThatAgent(t *testing.T) {
	f, wf := crewRunFixture(t)
	if _, err := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: newCrewRun(wf.ID)}); err != nil {
		t.Fatal("a free workflow refused", err)
	}
	// An agent takes only a workflow with no live trigger, so turn the schedule off first.
	off := *wf.Crew
	off.Enabled = false
	if _, err := f.svc.UpdateCrew(testCtx, f.human, wf.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: wf.Revision, Crew: &off}); err != nil {
		t.Fatal("turn the schedule off", err)
	}
	bot := crewAgent("리서처", "리서처")
	bot.Workflow = "아침브리핑"
	f.crew(bot)
	for _, trigger := range []string{"schedule", "call", "manual", "discord"} {
		run := newCrewRun(wf.ID)
		run.Trigger = trigger
		if _, err := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: run}); !errors.Is(err, ErrInvalid) {
			t.Fatal("an owned workflow ran alone by", trigger, err)
		}
	}
	run := newCrewRun(wf.ID)
	run.Trigger = "agent"
	if _, err := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: run}); err != nil {
		t.Fatal("the agent could not run its workflow", err)
	}
	// A person's 시험 실행 still works: the engine fakes every outside step.
	run = newCrewRun(wf.ID)
	run.Trigger = "test"
	if _, err := f.svc.CreateCrewRun(testCtx, f.human, CrewRunInput{Scope: scopeA, Run: run}); err != nil {
		t.Fatal("a test run of an owned workflow was refused", err)
	}
}

func TestCrewUsageLeavesOutTestRuns(t *testing.T) {
	f, auto := crewRunFixture(t)
	for _, trigger := range []string{"manual", "test"} {
		run := newCrewRun(auto.ID)
		run.Trigger = trigger
		if _, err := f.svc.CreateCrewRun(testCtx, f.human, CrewRunInput{Scope: scopeA, Run: run}); err != nil {
			t.Fatal(trigger, err)
		}
	}
	u, err := f.svc.CrewUsage(testCtx, f.human, scopeA, "today", "workflow", "", "")
	if err != nil || u.KPI.Runs != 1 || len(u.Workflows) != 1 || u.Workflows[0].Runs != 1 {
		t.Fatal("usage", err, u.KPI.Runs, u.Workflows)
	}
}
