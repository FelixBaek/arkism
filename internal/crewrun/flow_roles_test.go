package crewrun

import (
	"context"
	"strings"
	"testing"
)

func TestFlowExpertUsesPinnedRoleAndSkillVersions(t *testing.T) {
	e, fs, ctx := attemptFixture(t)
	pinned := CrewItem{ID: "expert", Revision: 2, Crew: Crew{Type: "agent", Name: "expert", Model: "saved-model", ModelMode: "auto",
		Role: "saved role", Instructions: "saved instructions", Skills: []string{"skill@3"}, OutputFormat: "markdown", Effort: "high"}}
	live := pinned
	live.Crew.Instructions, live.Crew.Skills = "changed live instructions", []string{"different@9"}
	fs.crew = []CrewItem{live}
	if err := e.LoadCrew(context.Background()); err != nil {
		t.Fatal(err)
	}
	scope := ctx.Value(flowCallScopeKey{}).(flowCallScope)
	scope.definitions = map[string]CrewItem{pinned.ID: pinned}
	ctx = context.WithValue(ctx, flowCallScopeKey{}, scope)
	log := fakeAI(t, e)
	n := node("work", "expert", "expert", pinned.ID, "access", "read")
	x := &runExec{e: e, item: pinned, run: Run{State: RunState{SourceType: "agent", Nodes: map[string]*NodeState{}}}}
	x.index(&Graph{Nodes: []Node{n}})
	if _, err := e.runExpert(ctx, x, n, "question"); err != nil {
		t.Fatal(err)
	}
	sent := specs(t, log)
	if len(sent) != 1 || sent[0].Model != "auto" || sent[0].Effort != "high" || len(sent[0].Skills) != 1 || sent[0].Skills[0] != "skill@3" {
		t.Fatal("saved role options lost", sent)
	}
	if instructions := sent[0].Instructions; strings.Count(instructions, "saved role") != 1 || strings.Count(instructions, "saved instructions") != 1 || !strings.Contains(instructions, "markdown") || strings.Contains(instructions, "changed live") {
		t.Fatal("pinned role was replaced by live configuration", instructions)
	}
}

func TestFlowOrdinaryAINodeInheritsWorkflowRoleEffortAndSkills(t *testing.T) {
	e, _, ctx := attemptFixture(t)
	log := fakeAI(t, e)
	n := node("work", "ai_once", "instructions", "node instructions", "model", "node-model", "skills", "node@2,common@1")
	x := &runExec{e: e, item: CrewItem{Crew: Crew{Type: "workflow", Model: "root-model", ModelMode: "auto", Effort: "high",
		Role: "workflow role", Instructions: "workflow instructions", Skills: []string{"common@1", "workflow@3"}}},
		run: Run{State: RunState{SourceType: "workflow", Nodes: map[string]*NodeState{}}}}
	x.index(&Graph{Nodes: []Node{n}})
	if _, err := e.runAI(ctx, x, n, "question"); err != nil {
		t.Fatal(err)
	}
	sent := specs(t, log)
	if len(sent) != 1 || sent[0].Model != "node-model" || sent[0].Effort != "high" || len(sent[0].Skills) != 3 {
		t.Fatal("workflow defaults or node override lost", sent)
	}
	for _, text := range []string{"workflow role", "workflow instructions", "node instructions"} {
		if !strings.Contains(sent[0].Instructions, text) {
			t.Fatal("missing workflow instructions", sent[0])
		}
	}
}

func TestFlowPersonaMissingSnapshotDoesNotUseLiveDefinition(t *testing.T) {
	e, fs, ctx := attemptFixture(t)
	fs.crew = []CrewItem{{ID: "persona", Crew: Crew{Type: "persona", Instructions: "live instruction"}}}
	if err := e.LoadCrew(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.instructions(ctx, map[string]string{"persona": "persona"}); err == nil {
		t.Fatal("FLOW silently read an unpinned live persona")
	}
	if text, err := e.instructions(context.Background(), map[string]string{"persona": "persona"}); err != nil || text != "live instruction" {
		t.Fatal("legacy persona lookup changed", text, err)
	}
}

func TestFlowExpertDefaultsReadAndRefusesWritesBeforeDispatch(t *testing.T) {
	e, fs, ctx := attemptFixture(t)
	role := CrewItem{ID: "expert", Crew: Crew{Type: "agent", Model: "test-model"}}
	scope := ctx.Value(flowCallScopeKey{}).(flowCallScope)
	scope.definitions = map[string]CrewItem{role.ID: role}
	ctx = context.WithValue(ctx, flowCallScopeKey{}, scope)
	log := fakeAI(t, e)
	n := node("work", "expert", "expert", role.ID)
	x := &runExec{e: e, run: Run{State: RunState{SourceType: "agent", Nodes: map[string]*NodeState{}}}}
	x.index(&Graph{Nodes: []Node{n}})
	if _, err := e.runExpert(ctx, x, n, "question"); err != nil {
		t.Fatal(err)
	}
	if sent := specs(t, log); len(sent) != 1 || sent[0].Access != "read" {
		t.Fatal("default expert inherited CLI write permission", sent)
	}
	n.Config["access"] = "write"
	if _, err := e.runExpert(ctx, x, n, "question"); err == nil || len(fs.attempts) != 1 {
		t.Fatal("write expert dispatched without an effect gateway", err)
	}
}
