package memory

import (
	"reflect"
	"testing"
)

// convertedWrapper is what arkism crew convert saves for a named agent or team: a workflow of the same
// name whose one step is the graph the server already runs the agent or team as.
func convertedWrapper(t *testing.T, r Record) Crew {
	t.Helper()
	g, err := flowGraphFor(*r.Crew, r.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return Crew{Type: "workflow", Name: r.Crew.Name, Handle: r.Crew.Handle, Role: r.Crew.Role, Graph: g.Graph}
}

func TestAtCallReachesTheWorkflowWrappingTheAgentOrTeam(t *testing.T) {
	f := newMeetingFixture(t)
	before, err := f.svc.FlowCapabilities(testCtx, f.human, scopeA, f.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	wrapA := f.crew(convertedWrapper(t, f.a))
	wrapTeam := f.crew(convertedWrapper(t, f.team))
	for _, c := range []struct {
		handle string
		item   string
		part   string
	}{{"@리뷰어", wrapA.ID, f.a.ID}, {"@리뷰팀", wrapTeam.ID, f.team.ID}} {
		res, err := f.svc.ResolveCrew(testCtx, f.human, scopeA, c.handle)
		if err != nil || res.Item.ID != c.item || res.Part == nil || res.Part.ID != c.part {
			t.Fatal(c.handle, res.Item.ID, res.Part, err)
		}
		if res, err = f.svc.ResolveCrew(testCtx, f.human, scopeA, "$"+c.handle[1:]); err != nil || res.Item.ID != c.item || res.Part != nil {
			t.Fatal("$", c.handle, err)
		}
	}
	// The wrapper runs the same: its definitions bring in the agent, so the same capability check applies.
	after, err := f.svc.FlowCapabilities(testCtx, f.human, scopeA, wrapA.ID)
	if err != nil || after.Supported != before.Supported || !reflect.DeepEqual(stripIDs(after.Unsupported, wrapA.ID, f.a.ID), stripIDs(before.Unsupported, f.a.ID, f.a.ID)) {
		t.Fatal(before, after, err)
	}
	// Pins stay on the agent: "@리뷰어" still reads them through the wrapper.
	if pins, err := f.svc.ResolvePins(testCtx, f.human, scopeA, "@리뷰어", ""); err != nil || pins.Crew.ID != f.a.ID || pins.Type != "agent" {
		t.Fatal("pins", pins.Crew, err)
	}
	// The agent and team records are untouched parts.
	for _, r := range []Record{f.a, f.team} {
		got, err := f.svc.Repo.Get(testCtx, colCrew, scopeA, r.ID)
		if err != nil || got.Revision != r.Revision || got.Status != statusActive {
			t.Fatal("part changed", r.ID, err)
		}
	}
}

func stripIDs(list []string, wrap, part string) []string {
	out := []string{}
	for _, s := range list {
		if len(s) > len(part) && s[:len(part)+1] == part+":" {
			out = append(out, s[len(part)+1:])
		} else if len(s) > len(wrap) && s[:len(wrap)+1] == wrap+":" {
			out = append(out, s[len(wrap)+1:])
		}
	}
	return out
}

func TestAtCallKeepsTheAgentWhenASameNamedWorkflowDoesNotWrapIt(t *testing.T) {
	f := newMeetingFixture(t)
	f.crew(crewWorkflow("리뷰 워크플로", "리뷰어")) // a clash crew convert reports for renaming
	res, err := f.svc.ResolveCrew(testCtx, f.human, scopeA, "@리뷰어")
	if err != nil || res.Item.ID != f.a.ID || res.Part != nil {
		t.Fatal(res.Item.ID, err)
	}
}
