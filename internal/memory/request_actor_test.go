package memory

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var requestActorModes = []string{"usage_scope", "usage_row", "usage_run", "evaluation_scope", "evaluation_row", "source_trace", "lineage", "tool_create", "evaluation_create", "analysis", "source_duplicate", "source_replay", "preview_target", "preview_row", "expiry"}

func exerciseRequestActor(t *testing.T, repo Repository, mode, field string) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	f.svc.Embedder = nil
	m := f.activeMemory("actorneedle", "actorneedle")
	run := f.create(f.agent, "agent_runs", completedRunInput(CreateInput{Scope: scopeA, Text: "actor run", Sources: m.Sources}))
	action, id := "", ""
	var call func() (bool, error)
	var failedAnalysisID string
	switch mode {
	case "usage_scope", "usage_row", "usage_run":
		bundle := f.search(f.human, SearchInput{Scope: scopeA, Query: "actorneedle", RunID: run.ID})
		action = "read:usage"
		if mode == "usage_row" {
			id = bundle.ContextID
		}
		if mode == "usage_run" {
			action, id = "read:agent_runs", run.ID
		}
		call = func() (bool, error) {
			r, e := f.svc.RecentMemoryUsage(testCtx, f.human, scopeA, m.ID)
			return len(r.Items) > 0, e
		}
	case "evaluation_scope", "evaluation_row", "evaluation_create":
		in := CreateInput{Scope: scopeA, Text: "actor evaluation", Sources: m.Sources, Target: &Ref{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}, Evaluation: &Evaluation{EvalType: "user_rating", Passed: boolValue(true)}}
		evaluation := f.create(f.human, "evaluations", in)
		action = "read:evaluations"
		if mode == "evaluation_row" {
			id = evaluation.ID
		}
		call = func() (bool, error) {
			r, e := f.svc.SummarizeEvaluations(testCtx, f.human, EvaluationSummaryInput{Scope: scopeA, Target: *in.Target})
			return structuredText(r) != structuredText(EvaluationSummary{}), e
		}
		if mode == "evaluation_create" {
			action, id = "read:agent_runs", run.ID
			call = func() (bool, error) { r, e := f.svc.Create(testCtx, f.human, "evaluations", in); return r.ID != "", e }
		}
	case "source_trace":
		action, id = "read:sources", m.Sources[0].SourceID
		call = func() (bool, error) {
			r, e := f.svc.TraceMemorySources(testCtx, f.human, scopeA, m.ID)
			return r.Memory.ID != "", e
		}
	case "lineage":
		action, id = "read:memory_candidates", m.Proposal.ID
		call = func() (bool, error) {
			r, e := f.svc.MemoryLineage(testCtx, f.human, scopeA, m.ID)
			return r.Memory.ID != "", e
		}
	case "tool_create":
		action, id = "read:agent_runs", run.ID
		call = func() (bool, error) {
			r, e := f.svc.Create(testCtx, f.human, "tool_calls", toolInput(run))
			return r.ID != "", e
		}
	case "analysis":
		action = "write:skill_versions"
		f.svc.Analyzer = &countingAnalyzer{}
		calls := 0
		call = func() (bool, error) {
			calls++
			in := AnalyzeSkillInput{Scope: scopeA, SkillID: "actor-analysis", Path: "/skills/actor/SKILL.md", Content: fmt.Sprintf("# Actor analysis %d", calls)}
			id = analysisID(in)
			failedAnalysisID = id
			r, e := f.svc.AnalyzeSkill(testCtx, f.human, in)
			return r.Record.ID != "", e
		}
	case "preview_target", "preview_row":
		source, err := repo.Get(testCtx, "sources", scopeA, m.Sources[0].SourceID)
		if err != nil {
			t.Fatal(err)
		}
		action, id = "read:sources", source.ID
		if mode == "preview_row" {
			action, id = "read:memories", m.ID
		}
		call = func() (bool, error) {
			r, e := f.svc.PreviewDeletion(testCtx, f.human, "sources", source.ID, ChangeInput{Scope: scopeA, ExpectedRevision: source.Revision})
			return r.Target.ID != "", e
		}
	case "expiry":
		action = "expire:memory_candidates"
		before := time.Now().Add(-30 * 24 * time.Hour).UTC().Truncate(time.Millisecond)
		call = func() (bool, error) {
			row := Record{ID: ID(), Scope: scopeA, Kind: "decision", Status: "candidate", Revision: 1, CreatedAt: before.Add(-time.Hour), UpdatedAt: before.Add(-time.Hour), Text: "old proposal"}
			id = row.ID
			if e := repo.Insert(testCtx, "memory_candidates", row); e != nil {
				t.Fatal(e)
			}
			r, e := f.svc.ExpireCandidates(testCtx, f.human, ExpireCandidatesInput{Scope: scopeA, Before: before, Limit: 1})
			return len(r.Expired) > 0, e
		}
	case "source_duplicate", "source_replay":
		in := sourceInput(CreateInput{Scope: scopeA, Text: "reused actor source"})
		if mode == "source_replay" {
			in.IdempotencyKey = "actor-replay"
		}
		src := f.create(f.human, "sources", in)
		action, id = "read:sources", src.ID
		call = func() (bool, error) { r, e := f.svc.Create(testCtx, f.human, "sources", in); return r.ID != "", e }
	default:
		t.Fatal(mode)
	}
	if present, e := call(); e != nil || !present {
		t.Fatal("positive control", mode, present, e)
	}
	changed := false
	f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(in Authorization, d *Decision) error {
		if in.Action == action && in.ResourceID == id {
			changed = true
			if field == "role" {
				d.Role = "agent"
				if mode == "expiry" {
					d.Role = "service"
				}
			} else {
				d.PrincipalID = "other-user"
			}
		}
		return nil
	}}
	before := map[string]int{}
	if fake, ok := repo.(*fakeRepo); ok {
		for _, col := range []string{"sources", "skills", "skill_versions", "evaluations", "tool_calls"} {
			before[col] = len(fake.rows(col))
		}
	}
	present, e := call()
	if !changed || !errors.Is(e, ErrDenied) || present {
		t.Fatal("mixed actor returned", mode, field, changed, present, e)
	}
	if fake, ok := repo.(*fakeRepo); ok {
		for col, count := range before {
			if len(fake.rows(col)) != count {
				t.Fatal("denied request wrote record", col)
			}
		}
	}
	if mode == "expiry" {
		r, e := repo.Get(testCtx, "memory_candidates", scopeA, id)
		if e != nil || r.Status != "candidate" || r.Revision != 1 {
			t.Fatal("denied expiry changed candidate", r, e)
		}
	}
	if mode == "analysis" {
		for _, target := range []Ref{{Collection: colSkillVersions, ID: failedAnalysisID}, {Collection: "sources", ID: "source-" + failedAnalysisID}} {
			if _, e := repo.Get(testCtx, target.Collection, scopeA, target.ID); !errors.Is(e, ErrNotFound) {
				t.Fatal("failed analysis published", target, e)
			}
		}
	}
}
func TestRequestActorConsistency(t *testing.T) {
	for _, mode := range requestActorModes {
		for _, field := range []string{"principal", "role"} {
			t.Run(mode+"/"+field, func(t *testing.T) { exerciseRequestActor(t, newFakeRepo(), mode, field) })
		}
	}
}

// Unlike the row-only source-list test adapter, this also visits scope decisions.
type requestActorAuth struct {
	Authorizer
	change func(Authorization, *Decision) error
}

func (a requestActorAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	d, e := a.Authorizer.Authorize(ctx, token, in)
	if e == nil {
		e = a.change(in, &d)
	}
	return d, e
}

func TestSourceReplayActorHTTP(t *testing.T) {
	f := newFixture(t)
	in := sourceInput(CreateInput{Scope: scopeA, Text: "original replay body", IdempotencyKey: "http-actor"})
	src := f.create(f.human, "sources", in)
	f.svc.Auth = &searchActorAuth{Authorizer: f.auth, action: "read:sources", id: src.ID, field: "role"}
	req := httptest.NewRequest("POST", "/v1/sources", strings.NewReader(structuredText(in)))
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 403 || strings.Contains(w.Body.String(), src.ID) || strings.Contains(w.Body.String(), src.Text) {
		t.Fatal("replay actor HTTP", w.Code, w.Body.String())
	}
}
