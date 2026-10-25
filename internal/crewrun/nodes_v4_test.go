package crewrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExpertNodeUsesExpertDefaults(t *testing.T) {
	ex := CrewItem{ID: "ex1", Revision: 1, Crew: Crew{Type: "agent", Name: "리서처", Model: "deepseek-v4", Effort: "high", Instructions: "출처를 확인한다."}}
	g := &Graph{Nodes: []Node{node("start", "start"), node("a", "expert", "expert", "ex1", "instructions", "짧게.", "goal", "조사: {{in.q}}"),
		node("b", "expert", "expert", "ex1", "model", "glm-5", "effort", "low"), node("end", "end")},
		Edges: []Edge{edge("start", "a"), edge("a", "b"), edge("b", "end")}}
	e := testEngine(t, newFakeStore(ex, wf("w", g)))
	log := fakeAI(t, e)
	r := runOnce(t, e, "w", map[string]any{"q": "x"})
	got := specs(t, log)
	if r.State.Status != "succeeded" || len(got) != 2 {
		t.Fatal("expert run", r.State.Status, statuses(r), len(got))
	}
	if got[0].Model != "deepseek-v4" || got[0].Effort != "high" || got[0].Instructions != "출처를 확인한다.\n\n짧게." || got[0].Task != "조사: x" {
		t.Fatalf("defaults %+v", got[0])
	}
	if got[1].Model != "glm-5" || got[1].Effort != "low" {
		t.Fatalf("node wins %+v", got[1])
	}
	if r.State.Nodes["a"].Tokens != 120 || r.State.Nodes["start"].Tokens != 0 {
		t.Fatal("node tokens", r.State.Nodes["a"].Tokens)
	}
	g.Nodes[1].Config["expert"] = "nope"
	if r = runOnce(t, e, "w", nil); r.State.Status != "failed" || !strings.HasPrefix(r.State.Nodes["a"].Error, "bad_config") {
		t.Fatal("missing expert", r.State.Nodes["a"].Error)
	}
}

type creatingMeetings struct{ *fakeMeetings }

func (c creatingMeetings) CreateMeeting(_ context.Context, teamID, title, topic string) (MeetingRecord, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec := MeetingRecord{ID: "m-new", Revision: 1, Meeting: Meeting{Status: "pending", TeamID: teamID, TeamRevision: 3, Title: title, Topic: topic,
		Trigger: "workflow", MaxRounds: 1, Turns: []MeetingTurn{}, NotesPending: []MeetingNote{}}}
	c.recs[rec.ID] = rec
	return rec, nil
}

func TestTeamNodeRunsMeetingAndOutputsConclusion(t *testing.T) {
	rig := newMeetingRig(t, baseItems(teamItem("round_robin", &TeamEnding{MaxRounds: 1}, threeMembers()...))...)
	rig.e.Meetings = creatingMeetings{rig.store}
	out, err := rig.e.runTeam(context.Background(), map[string]string{"team": "team", "topic": "검토: {{in.pr}}"}, map[string]any{"pr": 12})
	m, _ := out.(map[string]any)
	if err != nil || m["summary"] != "머지한다" || m["meeting_id"] != "m-new" {
		t.Fatal("team", out, err)
	}
	if got := rig.store.recs["m-new"].Meeting; got.Topic != "검토: 12" || got.Trigger != "workflow" {
		t.Fatalf("meeting %+v", got)
	}
	if _, err = rig.e.runTeam(context.Background(), map[string]string{"team": "agent-x"}, nil); err == nil || !strings.HasPrefix(err.Error(), "bad_config") {
		t.Fatal("unknown team", err)
	}
}

func TestTeamNodeScopeAndRetries(t *testing.T) {
	rig := newMeetingRig(t, baseItems(teamItem("round_robin", &TeamEnding{MaxRounds: 1}, threeMembers()...))...)
	rig.e.Meetings = creatingMeetings{rig.store}
	_, err := rig.e.runTeam(context.Background(), map[string]string{"team": "team", "topic": "검토", "fence": "읽기만", "done": "결정 하나", "retries": "2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := rig.store.recs["m-new"].Meeting.Topic; got != "검토\n\n울타리: 읽기만\n\n완료 기준: 결정 하나" {
		t.Fatalf("topic %q", got)
	}
}

func TestN8NSyncAndAsync(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		if cb, _ := seen["callback_url"].(string); cb != "" {
			go func() { // n8n answers later, to the callback
				time.Sleep(50 * time.Millisecond)
				resp, err := http.Post(cb, "application/json", bytes.NewReader([]byte(`{"done":true}`)))
				if err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}()
			w.WriteHeader(200)
			return
		}
		_, _ = w.Write([]byte(`{"echo":"` + seen["input"].(string) + `"}`))
	}))
	defer srv.Close()
	e := testEngine(t, newFakeStore())
	out, err := e.runN8N(context.Background(), map[string]string{"url": srv.URL, "message": "안녕 {{in.a}}"}, map[string]any{"a": "x"})
	if m, _ := out.(map[string]any); err != nil || m["echo"] != "안녕 x" {
		t.Fatal("sync", out, err)
	}
	cb := httptest.NewServer(e.Handler(context.Background()))
	defer cb.Close()
	e.PublicURL = cb.URL
	out, err = e.runN8N(context.Background(), map[string]string{"url": srv.URL, "mode": "async", "timeout": "5s"}, "hi")
	if m, _ := out.(map[string]any); err != nil || m["done"] != true {
		t.Fatal("async", out, err)
	}
	if _, err = e.runN8N(context.Background(), map[string]string{"url": "ftp://x"}, nil); err == nil || !strings.HasPrefix(err.Error(), "bad_config") {
		t.Fatal("bad url", err)
	}
	resp, _ := http.Post(cb.URL+"/callbacks/unknown", "application/json", strings.NewReader("{}"))
	if resp.StatusCode != 404 {
		t.Fatal("unknown token", resp.StatusCode)
	}
	e.PublicURL = ""
	if _, err = e.runN8N(context.Background(), map[string]string{"url": srv.URL, "mode": "async"}, nil); err == nil {
		t.Fatal("async needs a public url")
	}
}

func TestExpertNodeOnBackendCarriesStepKey(t *testing.T) {
	ex := CrewItem{ID: "ex1", Revision: 1, Crew: Crew{Type: "agent", Name: "리서처", Backend: "mine"}}
	g := &Graph{Nodes: []Node{node("start", "start"), node("a", "expert", "expert", "ex1", "access", "read", "goal", "조사"),
		node("b", "expert", "expert", "ex1", "backend", "local", "goal", "로컬"), node("end", "end")},
		Edges: []Edge{edge("start", "a"), edge("a", "b"), edge("b", "end")}}
	e := testEngine(t, newFakeStore(ex, wf("w", g)))
	log := fakeAI(t, e)
	r := runOnce(t, e, "w", nil)
	got := specs(t, log)
	if r.State.Status != "succeeded" || len(got) != 2 {
		t.Fatal(r.State.Status, statuses(r), len(got))
	}
	run := got[0].Run
	if got[0].Backend != "mine" || run["kind"] != "workflow" || run["step_id"] != "a" || run["expert_id"] != "ex1" || run["key"] != r.ID+"/a/1" {
		t.Fatalf("backend step %+v", got[0])
	}
	if got[1].Backend != "" || got[1].Run != nil {
		t.Fatalf("a step set to local runs on Arkism's own path %+v", got[1])
	}
	// One step is one external task: collab and retries are refused, not run as several tasks under one key.
	g.Nodes[1] = node("a", "expert", "expert", "ex1", "access", "read", "retries", "2")
	if r = runOnce(t, e, "w", nil); r.State.Status != "failed" || !strings.HasPrefix(r.State.Nodes["a"].Error, "bad_config") {
		t.Fatal("retries on a backend", r.State.Nodes["a"].Error)
	}
}

// The run record keeps each backend step's trace (names and ids only), done or failed, and a step's start marks it so
// Discord can tell a long external step from a stuck run.
func TestBackendStepTraceOnRunRecord(t *testing.T) {
	ex := CrewItem{ID: "ex1", Revision: 1, Crew: Crew{Type: "agent", Name: "리서처", Backend: "mine"}}
	g := &Graph{Nodes: []Node{node("start", "start"), node("a", "expert", "expert", "ex1", "access", "read", "goal", "조사"), node("end", "end")},
		Edges: []Edge{edge("start", "a"), edge("a", "end")}}
	e := testEngine(t, newFakeStore(ex, wf("w", g)))
	fakeAI(t, e)
	r := runOnce(t, e, "w", nil)
	tr := r.State.Nodes["a"].Backend
	if r.State.Status != "succeeded" || tr["backend"] != "mine" || tr["task_id"] != "t-1" || tr["state"] != "succeeded" || tr["fallback"] != false {
		t.Fatal(r.State.Status, tr)
	}
	if _, ok := tr["model"]; ok || tr["status_failures"] != nil || fmt.Sprint(tr["not_applied"]) != "[cwd]" {
		t.Fatalf("only server trace fields with label values are kept: %v", tr)
	}
	g.Nodes[1] = node("a", "expert", "expert", "ex1", "access", "read", "goal", "FAIL")
	r = runOnce(t, e, "w", nil)
	if tr = r.State.Nodes["a"].Backend; r.State.Status != "failed" || tr["state"] != "failed" || tr["task_id"] != "t-1" {
		t.Fatal("a failed external step keeps its trace", r.State.Status, tr)
	}
	if r.State.Nodes["start"].Backend != nil {
		t.Fatal("a local step has no trace")
	}
}

// A read step on a connection, interrupted by a restart, starts again under the same key: the host runner then asks the
// external runner about the task it already started instead of starting a second one.
func TestInterruptedBackendStepKeepsItsKey(t *testing.T) {
	ex := CrewItem{ID: "ex1", Revision: 1, Crew: Crew{Type: "agent", Name: "리서처", Backend: "mine"}}
	g := &Graph{Nodes: []Node{node("start", "start"), node("a", "expert", "expert", "ex1", "access", "read", "goal", "조사")},
		Edges: []Edge{edge("start", "a")}}
	fs := newFakeStore(ex, wf("w", g))
	ctx := context.Background()
	r, _ := fs.CreateRun(ctx, RunState{CrewID: "w", CrewRevision: 1, Trigger: "manual", Status: "running",
		Nodes: map[string]*NodeState{"start": {Status: "done", Output: "{}"}, "a": {Status: "running", Runs: 1}}})
	e := testEngine(t, fs)
	log := fakeAI(t, e)
	_ = e.LoadCrew(ctx)
	if err := e.ResumeRunning(ctx); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	got, _ := fs.GetRun(ctx, r.ID)
	s := specs(t, log)
	if got.State.Status != "succeeded" || len(s) != 1 || s[0].Run["key"] != r.ID+"/a/1" {
		t.Fatal(got.State.Status, statuses(got), s)
	}
}
