package memory

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Synthetic graph: no account data, original prompts or private record IDs.
func researchFixture() Crew {
	c := Crew{Type: "workflow", Name: "Research", Handle: "research", Limits: &CrewLimits{MaxMinutes: 90, MaxSteps: 40}, Budget: &CrewBudget{MonthlyUSD: 50, AskOverUSD: 2}, Trigger: &CrewTrigger{Kind: "webhook"}, Graph: &CrewGraph{}}
	for _, pair := range [][2]string{{"in", "webhook"}, {"queries", "ai_once"}, {"search", "http"}, {"answer", "ai"}, {"done", "end"}, {"plan", "ai_once"}, {"approve", "approval"}, {"deep", "http"}, {"op_a", "ai"}, {"op_b", "ai"}, {"final", "ai"}, {"end1", "end"}, {"gap1", "ai_once"}, {"deep2", "http"}, {"final2", "ai"}, {"end2", "end"}, {"gap2", "ai_once"}, {"deep3", "http"}, {"final3", "ai"}} {
		n := CrewNode{ID: pair[0], Type: pair[1], Config: map[string]string{}}
		switch n.Type {
		case "ai", "ai_once":
			n.Config = map[string]string{"model": "saved-" + n.ID, "access": "read", "goal": "original goal", "prompt": "original prompt", "timeout": "20m"}
		case "http":
			path := "/deep-gap"
			if n.ID == "search" || n.ID == "deep" {
				path = "/" + n.ID
			}
			n.Config = map[string]string{"method": "POST", "url": "http://127.0.0.1:8096" + path, "body": "{{input}}", "timeout": "4m"}
		case "approval":
			n.Config["question"] = "Original approval question"
		}
		c.Graph.Nodes = append(c.Graph.Nodes, n)
	}
	for _, triple := range [][3]string{{"in", "queries", ""}, {"queries", "search", ""}, {"search", "answer", ""}, {"answer", "done", "done"}, {"answer", "plan", "deep"}, {"plan", "approve", ""}, {"plan", "deep", ""}, {"approve", "deep", "approve"}, {"deep", "op_a", ""}, {"deep", "op_b", ""}, {"deep", "final", ""}, {"op_a", "final", ""}, {"op_b", "final", ""}, {"final", "end1", "done"}, {"final", "gap1", "more"}, {"final", "final2", "more"}, {"gap1", "deep2", ""}, {"deep2", "final2", ""}, {"final2", "end2", "done"}, {"final2", "gap2", "more"}, {"final2", "final3", "more"}, {"gap2", "deep3", ""}, {"deep3", "final3", ""}} {
		c.Graph.Edges = append(c.Graph.Edges, CrewEdge{From: triple[0], To: triple[1], Port: triple[2]})
	}
	return c
}

func TestResearchProposalPreservesExistingGraphModelsApprovalsAndBudgets(t *testing.T) {
	original := researchFixture()
	before, _ := json.Marshal(original)
	proposal, changes, err := ProposeResearch(original)
	if err != nil || len(changes) == 0 || len(proposal.Graph.Nodes) != 19 || !reflect.DeepEqual(original.Graph.Edges, proposal.Graph.Edges) || !reflect.DeepEqual(original.Limits, proposal.Limits) || !reflect.DeepEqual(original.Budget, proposal.Budget) || !reflect.DeepEqual(original.Trigger, proposal.Trigger) {
		t.Fatal(err, changes, proposal)
	}
	after, _ := json.Marshal(original)
	if string(before) != string(after) {
		t.Fatal("proposal mutated the saved definition")
	}
	for i, n := range proposal.Graph.Nodes {
		prev := original.Graph.Nodes[i]
		for _, key := range []string{"model", "prompt", "url", "body", "timeout", "question", "access"} {
			if prev.Config[key] != n.Config[key] {
				t.Fatal(n.ID, key, prev.Config[key], n.Config[key])
			}
		}
		if n.Type == "http" && n.ID != "search" && n.Config["research_approval"] != "approve" {
			t.Fatal("deep helper lost approval gate", n.ID)
		}
	}
	if proposal.Research.QuickNode != "answer" || proposal.Research.DeepNode != "plan" || proposal.Research.HelperOrigin != "http://127.0.0.1:8096" {
		t.Fatal(proposal.Research)
	}
	again, _, err := ProposeResearch(proposal)
	if err != nil || !reflect.DeepEqual(again, proposal) {
		t.Fatal("proposal is not idempotent", err)
	}
}

func TestResearchDirectDeepUsesSameDefinitionAndKeepsApproval(t *testing.T) {
	c, _, err := ProposeResearch(researchFixture())
	if err != nil {
		t.Fatal(err)
	}
	auto, err := ResearchDefinition(c, "auto")
	if err != nil || !reflect.DeepEqual(auto, c) {
		t.Fatal(err, auto)
	}
	implicit, err := ResearchDefinition(c, "")
	if err != nil || !reflect.DeepEqual(implicit, auto) {
		t.Fatal("omitted mode must keep the ordinary research entry", err)
	}
	deep, err := ResearchDefinition(c, "deep")
	if err != nil || deep.Handle != c.Handle || !reflect.DeepEqual(deep.Limits, c.Limits) || len(c.Graph.Nodes) != 19 {
		t.Fatal(err, deep)
	}
	ids := map[string]bool{}
	for _, n := range deep.Graph.Nodes {
		ids[n.ID] = true
	}
	for _, id := range []string{"queries", "search", "answer", "done"} {
		if ids[id] {
			t.Fatal("deep mode retained quick branch", id)
		}
	}
	for _, id := range []string{"in", "plan", "approve", "deep", "op_a", "op_b", "gap1", "gap2", "final3"} {
		if !ids[id] {
			t.Fatal("deep mode dropped required step", id)
		}
	}
	entry, approval := false, false
	for _, e := range deep.Graph.Edges {
		entry = entry || e.From == "in" && e.To == "plan"
		approval = approval || e.From == "approve" && e.To == "deep" && e.Port == "approve"
	}
	if !entry || !approval {
		t.Fatal(deep.Graph.Edges)
	}
}

func TestResearchProposalRejectsUnknownHelperAndMissingApproval(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8096/admin", "https://example.com/search", "http://user:pass@127.0.0.1:8096/search", "http://127.0.0.1:8096/search?operation=write"} {
		c := researchFixture()
		for i := range c.Graph.Nodes {
			if c.Graph.Nodes[i].ID == "search" {
				c.Graph.Nodes[i].Config["url"] = raw
			}
		}
		if _, _, err := ProposeResearch(c); err == nil {
			t.Fatal(raw)
		}
	}
	c := researchFixture()
	for i := range c.Graph.Edges {
		if c.Graph.Edges[i].Port == "approve" {
			c.Graph.Edges[i].Port = "reject"
		}
	}
	if _, _, err := ProposeResearch(c); err == nil {
		t.Fatal("accepted deep search without approval edge")
	}
	if strings.Contains(researchReportInstruction, "elapsed") {
		t.Fatal("time must not decide evidence sufficiency")
	}
}

func TestResearchPolicyDoesNotAdmitOtherEffects(t *testing.T) {
	for _, mutate := range []func(*Crew){
		func(c *Crew) { c.Graph.Nodes = append(c.Graph.Nodes, CrewNode{ID: "extra", Type: "code"}) },
		func(c *Crew) { c.Graph.Nodes[3].Config["access"] = "write" },
		func(c *Crew) { c.Graph.Nodes[1].Config["backend"] = "external" },
		func(c *Crew) { c.Graph.Nodes[2].Config["headers"] = "Host: another-service" },
	} {
		c, _, err := ProposeResearch(researchFixture())
		if err != nil {
			t.Fatal(err)
		}
		mutate(&c)
		if validResearchPolicy(c) {
			t.Fatal("research policy admitted a non-research effect")
		}
	}
}

func TestResearchDirectApprovalDescribesSkippedQuickSearchTruthfully(t *testing.T) {
	original := researchFixture()
	question := "빠른 검색으로는 근거가 부족해요. 아래 계획으로 딥리서치를 할까요? (최대 60분)\n\n{{input}}"
	for i := range original.Graph.Nodes {
		if original.Graph.Nodes[i].ID == "approve" {
			original.Graph.Nodes[i].Config["question"] = question
		}
	}
	proposal, _, err := ProposeResearch(original)
	if err != nil {
		t.Fatal(err)
	}
	deep, err := ResearchDefinition(proposal, "deep")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []Crew{original, proposal} {
		for _, n := range c.Graph.Nodes {
			if n.ID == "approve" && n.Config["question"] != question {
				t.Fatal("saved or auto definition was changed")
			}
		}
	}
	found := false
	for _, n := range deep.Graph.Nodes {
		if n.ID == "approve" {
			found = true
			want := strings.Replace(question, "빠른 검색으로는 근거가 부족해요", "직접 딥리서치를 요청하셨어요", 1)
			if n.Type != "approval" || n.Config["question"] != want {
				t.Fatal(n)
			}
		}
	}
	if !found {
		t.Fatal("direct research lost its approval")
	}
}
