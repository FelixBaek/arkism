//go:build integration

package memory

import (
	"testing"
	"time"
)

func TestIntegrationPromptsAndAgentToolsOnPostgres(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	p := ImagePrompt{Title: "배경 패턴", Kind: "image", Engine: "comfyui_api", Prompt: "dots on {색}"}
	r, err := f.svc.CreatePrompt(testCtx, f.human, PromptInput{Scope: scopeA, Prompt: p})
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.svc.ListPrompts(testCtx, f.human, scopeA)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != r.ID || page.Items[0].ImagePrompt.Engine != "comfyui_api" {
		t.Fatal("prompt round trip", page.Items, err)
	}
	if _, err = f.svc.RequestAgentTool(testCtx, f.human, AgentToolSwitch{Scope: scopeA, Agent: "claude_code", Kind: "plugin", Name: "example-skill", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.AgentTools(testCtx, f.human, scopeA)
	if err != nil || len(got.AgentTools.Requests) != 1 || got.AgentTools.Requests[0].Status != "pending" {
		t.Fatal("agent tools round trip", got.AgentTools, err)
	}
}

func TestIntegrationCrewOnPostgres(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	f.crew(crewTemplate("릴리스 매니저"))
	team := f.crew(crewWorkflow("출시 팀", "출시팀"))
	got, err := f.svc.ResolveCrew(testCtx, f.agent, scopeA, "$출시팀")
	if err != nil || got.Item.ID != team.ID || got.Item.Crew.Limits.MaxMinutes != 30 || len(got.Item.Crew.Graph.Nodes) != 4 {
		t.Fatal("crew round trip", got, err)
	}
	if _, err = f.svc.UpdateCrew(testCtx, f.human, team.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: team.Revision, Archive: true}); err != nil {
		t.Fatal("archive", err)
	}
	if page, _ := f.svc.ListCrew(testCtx, f.human, scopeA); len(page.Items) != 1 {
		t.Fatal("archived workflow still listed", page.Items)
	}
}

func TestIntegrationCrewRunsOnPostgres(t *testing.T) {
	f, auto := crewRunFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	auto = f.crew(*auto.Crew)
	r, err := f.svc.CreateCrewRun(testCtx, tokEngine, CrewRunInput{Scope: scopeA, Run: newCrewRun(auto.ID)})
	if err != nil {
		t.Fatal("create", err)
	}
	run := *r.CrewRun
	now := time.Now().UTC().Truncate(time.Millisecond)
	run.Status = "waiting"
	run.Nodes = map[string]CrewRunNode{"start": {Status: "done", StartedAt: &now, FinishedAt: &now, Output: `{"x":1}`}, "ok": {Status: "waiting"}}
	run.Waiting = &CrewRunWaiting{NodeID: "ok", Kind: "approval", Question: "배포할까요?"}
	w, err := f.svc.UpdateCrewRun(testCtx, tokEngine, r.ID, CrewRunUpdate{Scope: scopeA, ExpectedRevision: 1, Run: run})
	if err != nil {
		t.Fatal("update", err)
	}
	if _, err = f.svc.DecideCrewRun(testCtx, f.human, r.ID, CrewRunDecisionInput{Scope: scopeA, ExpectedRevision: w.Revision, NodeID: "ok", Approve: true}); err != nil {
		t.Fatal("decide", err)
	}
	got, err := f.svc.GetCrewRun(testCtx, f.agent, scopeA, r.ID)
	if err != nil || !got.CrewRun.Decisions["ok"].Approve || got.CrewRun.Nodes["start"].Output != `{"x":1}` ||
		!got.CrewRun.Nodes["start"].StartedAt.Equal(now) || got.CrewRun.Waiting.Question != "배포할까요?" {
		t.Fatal("crew run round trip", got.CrewRun, err)
	}
}
