package memory

import (
	"errors"
	"testing"
)

// crewTemplate is a reusable definition by category (the old expert fields).
func crewTemplate(name string) Crew {
	return Crew{Type: "template", Name: name, Role: "릴리스 관리", Category: "개발",
		Avatar: &CrewAvatar{Art: "042", Bg: "sage"}, Instructions: "릴리스 노트를 쓴다", Model: "gpt-6.1-sol",
		Effort: "high", RunMode: "fixed", Skills: []string{"release-notes"}, Agents: []string{"codex", "claude_code"}}
}

func crewGraph() *CrewGraph {
	return &CrewGraph{Nodes: []CrewNode{{ID: "start", Type: "start"}, {ID: "a", Type: "ai", Config: map[string]string{"goal": "{{input}}"}},
		{ID: "ok", Type: "approval"}, {ID: "end", Type: "end"}},
		Edges: []CrewEdge{{From: "start", To: "a"}, {From: "a", To: "ok"}, {From: "ok", To: "end"}}}
}

func crewWorkflow(name, handle string) Crew {
	return Crew{Type: "workflow", Name: name, Handle: handle, Role: "주제를 받아 정리까지", Category: "글쓰기·문서", Model: "deepseek-v4-flash",
		Limits: &CrewLimits{MaxSteps: 60, MaxMinutes: 30}, Graph: crewGraph()}
}

func (f *fixture) crew(c Crew) Record {
	f.t.Helper()
	r, err := f.svc.CreateCrew(testCtx, f.human, CrewInput{Scope: scopeA, Crew: c})
	if err != nil {
		f.t.Fatalf("create crew %s: %v", c.Name, err)
	}
	return r
}

func TestCrewCreateListUpdateArchive(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.CreateCrew(testCtx, f.agent, CrewInput{Scope: scopeA, Crew: crewWorkflow("리서치", "리서치")}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent created a workflow", err)
	}
	w := f.crew(crewWorkflow("리서치", "리서치"))
	page, err := f.svc.ListCrew(testCtx, f.agent, scopeA)
	if err != nil || len(page.Items) != 1 || page.Items[0].Crew.Limits.MaxSteps != 60 {
		t.Fatal("list", page.Items, err)
	}
	c := *w.Crew
	c.Model = "opus 5.5"
	if _, err = f.svc.UpdateCrew(testCtx, f.agent, w.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: w.Revision, Crew: &c}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent changed a workflow", err)
	}
	up, err := f.svc.UpdateCrew(testCtx, f.human, w.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: w.Revision, Crew: &c})
	if err != nil || up.Crew.Model != "opus 5.5" || up.Revision != 2 {
		t.Fatal("update", up, err)
	}
	if _, err = f.svc.UpdateCrew(testCtx, f.human, w.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: 1, Archive: true}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision accepted", err)
	}
	persona := Crew{Type: "persona", Name: "x", Instructions: "차분하게"}
	if _, err = f.svc.UpdateCrew(testCtx, f.human, w.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: 2, Crew: &persona}); !errors.Is(err, ErrInvalid) {
		t.Fatal("a workflow turned into a persona", err)
	}
	if _, err = f.svc.UpdateCrew(testCtx, f.human, w.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: 2, Archive: true}); err != nil {
		t.Fatal("archive", err)
	}
	if page, _ = f.svc.ListCrew(testCtx, f.human, scopeA); len(page.Items) != 0 {
		t.Fatal("archived item still listed")
	}
}

func TestCrewWorkflowShapes(t *testing.T) {
	good := crewWorkflow("리서치", "리서치")
	good.Trigger, good.Enabled = &CrewTrigger{Kind: "schedule", Cron: "0 9 * * *"}, true
	if !validCrew(good) {
		t.Fatal("good workflow refused")
	}
	noTrigger := crewWorkflow("리서치", "리서치")
	noTrigger.Limits = nil
	if !validCrew(noTrigger) {
		t.Fatal("a workflow without trigger and limits refused")
	}
	for i, mutate := range []func(c *Crew){
		func(c *Crew) { c.Type = "expert" },
		func(c *Crew) { c.Type = "team" },
		func(c *Crew) { c.Type = "automation" },
		func(c *Crew) { c.Name = "" },
		func(c *Crew) { c.Handle = "" },
		func(c *Crew) { c.Handle = "이름 에 공백" },
		func(c *Crew) { c.Graph = nil },
		func(c *Crew) { c.Graph = &CrewGraph{} },
		func(c *Crew) { c.Model = "gpt;rm -rf /" },
		func(c *Crew) { c.Trigger = &CrewTrigger{Kind: "schedule", Cron: "every morning"} },
		func(c *Crew) { c.Trigger = &CrewTrigger{Kind: "webhook", Cron: "0 8 * * *"} },
		func(c *Crew) { c.Limits = &CrewLimits{MaxSteps: 501} },
		func(c *Crew) { c.Limits = &CrewLimits{MaxMinutes: 1441} },
		func(c *Crew) { c.Limits = &CrewLimits{MaxSteps: -1} },
		func(c *Crew) { c.Instructions = "전문가 지시문" },
		func(c *Crew) { c.Effort = "high" },
		func(c *Crew) { c.Skills = []string{"x"} },
		func(c *Crew) {
			g := crewGraph()
			g.Nodes[1].Type = "expert"
			c.Graph = g
		},
		func(c *Crew) {
			g := crewGraph()
			g.Nodes[1].Type = "shell_anything"
			c.Graph = g
		},
	} {
		c := good
		mutate(&c)
		if validCrew(c) {
			t.Fatal("bad workflow accepted", i)
		}
	}
	for _, typ := range []string{"ai_once", "subflow"} {
		c := crewWorkflow("x", "x")
		c.Graph.Nodes[1].Type = typ
		if !validCrew(c) {
			t.Fatal("node type refused", typ)
		}
	}
	tpl := crewTemplate("편집자")
	tpl.Limits = &CrewLimits{MaxSteps: 1}
	if validCrew(tpl) {
		t.Fatal("a template took limits")
	}
	tpl = crewTemplate("편집자")
	tpl.Handle = "편집자"
	if validCrew(tpl) {
		t.Fatal("a template took a handle")
	}
}

func TestCrewGraphBackEdgesAndCycles(t *testing.T) {
	back := crewGraph()
	back.Edges = append(back.Edges, CrewEdge{From: "ok", Port: "reject", To: "a", Max: 2})
	if !validCrewGraph(back) {
		t.Fatal("a back edge was refused")
	}
	cyc := crewGraph()
	cyc.Edges = append(cyc.Edges, CrewEdge{From: "ok", To: "a"})
	self := crewGraph()
	self.Edges = append(self.Edges, CrewEdge{From: "a", To: "a", Max: 1})
	tooMany := crewGraph()
	tooMany.Edges = append(tooMany.Edges, CrewEdge{From: "ok", To: "a", Max: 11})
	negative := crewGraph()
	negative.Edges = append(negative.Edges, CrewEdge{From: "ok", To: "a", Max: -1})
	dangling := crewGraph()
	dangling.Edges = append(dangling.Edges, CrewEdge{From: "a", To: "ghost", Max: 1})
	dup := crewGraph()
	dup.Nodes = append(dup.Nodes, CrewNode{ID: "a", Type: "wait"})
	for name, g := range map[string]*CrewGraph{"forward cycle": cyc, "self loop": self, "max over 10": tooMany, "negative max": negative,
		"dangling back edge": dangling, "duplicate": dup} {
		if validCrewGraph(g) {
			t.Fatal("graph accepted:", name)
		}
	}
	if !validCrewGraph(crewGraph()) || !validCrewGraph(nil) {
		t.Fatal("a good graph was refused")
	}
}

func TestCrewHandlesAreUniqueAmongWorkflows(t *testing.T) {
	f := newFixture(t)
	w := f.crew(crewWorkflow("리서치", "리서치"))
	if _, err := f.svc.CreateCrew(testCtx, f.human, CrewInput{Scope: scopeA, Crew: crewWorkflow("다른 것", "$리서치")}); err == nil {
		t.Fatal("handle with $ prefix accepted")
	}
	if _, err := f.svc.CreateCrew(testCtx, f.human, CrewInput{Scope: scopeA, Crew: crewWorkflow("다른 것", "리서치")}); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate handle accepted", err)
	}
	// Changing a record keeps its own handle.
	same := *w.Crew
	same.Role = "조사"
	if _, err := f.svc.UpdateCrew(testCtx, f.human, w.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: w.Revision, Crew: &same}); err != nil {
		t.Fatal("update kept its own handle", err)
	}
}

func TestCrewRefusesSecrets(t *testing.T) {
	f := newFixture(t)
	c := crewTemplate("배포 담당")
	c.Instructions = "이 키를 써: sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := f.svc.CreateCrew(testCtx, f.human, CrewInput{Scope: scopeA, Crew: c}); !errors.Is(err, ErrSecretDetected) {
		t.Fatal("a key in instructions was stored", err)
	}
	w := Crew{Type: "workflow", Name: "알림", Handle: "알림", Trigger: &CrewTrigger{Kind: "webhook"}, Graph: &CrewGraph{
		Nodes: []CrewNode{{ID: "h", Type: "http", Config: map[string]string{"headers": "Authorization: Bearer ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}}}}
	if _, err := f.svc.CreateCrew(testCtx, f.human, CrewInput{Scope: scopeA, Crew: w}); !errors.Is(err, ErrSecretDetected) {
		t.Fatal("a token in a node config was stored", err)
	}
}

func TestCrewResolveFindsWorkflow(t *testing.T) {
	f := newFixture(t)
	w := f.crew(crewWorkflow("백엔드 리뷰", "백엔드리뷰"))
	f.crew(Crew{Type: "persona", Name: "차분한 편집자", Instructions: "차분하게"})
	got, err := f.svc.ResolveCrew(testCtx, f.agent, scopeA, "$백엔드 리뷰")
	if err != nil || got.Item.ID != w.ID || got.Members == nil || len(got.Members) != 0 || got.LeaderModels == nil {
		t.Fatal("workflow resolve", got, err)
	}
	if _, err = f.svc.ResolveCrew(testCtx, f.agent, scopeA, "$없는것"); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown handle", err)
	}
	if _, err = f.svc.ResolveCrew(testCtx, f.agent, scopeA, "a;b"); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad handle", err)
	}
}

func TestAgentWorkflowIsAHandleOnAgentsOnly(t *testing.T) {
	a := crewAgent("리서처", "리서처")
	a.Workflow = "리서치"
	if !validCrew(a) {
		t.Fatal("agent with a workflow refused")
	}
	a.Workflow = "not a handle!"
	if validCrew(a) {
		t.Fatal("bad workflow handle accepted")
	}
	w := crewWorkflow("x", "x")
	w.Workflow = "리서치"
	if validCrew(w) {
		t.Fatal("a workflow took a workflow")
	}
}
