package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func generationFixture(t *testing.T, f *fixture) (GenerateSkillInput, *atomic.Int64) {
	t.Helper()
	p := proposalFixture(t, f)
	in := GenerateSkillInput{Scope: scopeA, EventID: "generate-1", SkillID: p.Skill.SkillID, Path: p.Skill.Path, Lessons: p.Lessons, ComparisonPlan: p.Skill.ComparisonPlan}
	calls := new(atomic.Int64)
	_, answer := draftFixture()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct{ Messages []struct{ Content string } }
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Messages) != 2 {
			t.Error("invalid request")
		}
		var material SkillDraftInput
		if json.Unmarshal([]byte(request.Messages[1].Content), &material) != nil || len(material.Lessons) != 3 || material.Lessons[0].Lesson != "verify the result" || strings.Contains(request.Messages[1].Content, in.ComparisonPlan.DatasetSHA256) {
			t.Error("unverified lessons or evaluation plan sent")
		}
		wire := any(answer)
		if material.BaselineContent != "" {
			wire = skillDraftPatch{Edits: []skillDraftEdit{{OldText: material.BaselineContent, NewText: material.BaselineContent + "\n" + answer.Content, LessonIndex: 0}}, Analysis: answer.Analysis}
		}
		_ = json.NewEncoder(w).Encode(draftResponse(structuredText(wire)))
	}))
	t.Cleanup(server.Close)
	f.svc.Drafter = &ChatSkillDrafter{Config: LLMConfig{Endpoint: server.URL, APIKey: "test", Model: "draft-model"}}
	f.svc.CostRates = CostRates{Revision: "test", SkillLLMInputTokenPicoUSD: 2, SkillLLMOutputTokenPicoUSD: 3}
	return in, calls
}

func TestSkillGeneration(t *testing.T) { checkSkillGeneration(t, newFixture(t)) }
func TestPostgresSkillGeneration(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	checkSkillGeneration(t, f)
}
func checkSkillGeneration(t *testing.T, f *fixture) {
	t.Helper()
	in, calls := generationFixture(t, f)
	page, err := f.svc.ListReflections(testCtx, f.human, scopeA, "review")
	if err != nil || len(page.Items) != 1 || len(page.Items[0].CandidateRefs) != 3 {
		t.Fatal("generation lesson discovery", err)
	}
	in.Lessons = page.Items[0].CandidateRefs
	req := httptest.NewRequest("POST", "/v1/skills/generate", strings.NewReader(structuredText(in)))
	req.Header.Set("Authorization", "Bearer "+f.agent)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	var got ImportSkillResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Record.Status != "candidate" || got.Record.Generator == nil || got.Record.Generator.Model != "draft-model" || got.Skill.ActiveVersion != "" {
		t.Fatal(w.Code, w.Body.String())
	}
	f.svc.Drafter = nil
	slices.Reverse(in.Lessons)
	retry, err := f.svc.GenerateSkill(testCtx, f.agent, in)
	if err != nil || !retry.Reused || retry.Record.ID != got.Record.ID || calls.Load() != 1 {
		t.Fatal("durable retry", err)
	}
	changed := in
	changed.Path = "/other/SKILL.md"
	if _, err = f.svc.GenerateSkill(testCtx, f.agent, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("event mutation", err)
	}
	passed := true
	target := Ref{Collection: colSkillVersions, ID: got.Record.ID, Revision: got.Record.Revision}
	ev := f.create(f.human2, "evaluations", CreateInput{Scope: scopeA, Text: "independent comparison", Sources: refs(f.source()), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &passed, ComparisonReport: &SkillComparisonReport{PlanSHA256: got.ComparisonPlanSHA256, BaselineScore: .5, CandidateScore: .8, CandidateCost: 1, CandidateDurationMS: 500, RegressionPassed: true, SafetyPassed: true}}})
	approved, err := f.svc.Transition(testCtx, f.human, colSkillVersions, got.Record.ID, ChangeInput{Scope: scopeA, ExpectedRevision: got.Record.Revision, Status: "approved", ValidationID: ev.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.GenerateSkill(testCtx, f.agent, in); err != nil {
		t.Fatal("approved retry", err)
	}
	if _, err = f.svc.ApplySkill(testCtx, f.human, in.SkillID, ChangeInput{Scope: scopeA, ExpectedRevision: got.Skill.Revision, VersionID: approved.ID}, false); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.GenerateSkill(testCtx, f.agent, in); err != nil {
		t.Fatal("applied retry", err)
	}
	row, err := f.svc.Repo.Get(testCtx, "memory_candidates", scopeA, in.Lessons[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	previous := row.Revision
	row.Revision++
	row.Status = "rejected"
	if err = f.svc.Repo.Replace(testCtx, "memory_candidates", row, previous); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.GenerateSkill(testCtx, f.agent, in); err == nil || calls.Load() != 1 {
		t.Fatal("revoked receipt revived")
	}
}

func TestSkillGenerationRejectsBeforeCall(t *testing.T) {
	for _, kind := range []string{"duplicate", "stale", "no_plan", "missing_event", "denied_evaluation", "denied_source", "denied_transfer", "wrong_scope", "wrong_role"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			in, calls := generationFixture(t, f)
			switch kind {
			case "duplicate":
				in.Lessons[2] = in.Lessons[0]
			case "stale":
				in.Lessons[0].Revision++
			case "no_plan":
				in.ComparisonPlan = nil
			case "missing_event":
				in.EventID = ""
			case "denied_evaluation":
				f.svc.Auth = &learningPolicy{Authorizer: f.auth, deny: "read:evaluations"}
			case "denied_source":
				f.svc.Auth = &learningPolicy{Authorizer: f.auth, deny: "read:sources"}
			case "denied_transfer":
				a := f.auth.actors[f.agent]
				a.external = false
				f.auth.actors[f.agent] = a
			case "wrong_scope":
				in.Scope = scopeFar
			case "wrong_role":
				a := f.auth.actors[f.agent]
				a.role = "observer"
				f.auth.actors[f.agent] = a
			}
			if _, err := f.svc.GenerateSkill(testCtx, f.agent, in); err == nil || calls.Load() != 0 || len(f.repo.rows(colSkillVersions)) != 0 {
				t.Fatal("invalid generation reached provider", err)
			}
		})
	}
}

type generationHookDrafter struct {
	SkillDrafter
	hook   func()
	change func(*SkillDraft, *ProviderUsage, *error)
}

func (d generationHookDrafter) Draft(ctx context.Context, in SkillDraftInput) (SkillDraft, ProviderUsage, error) {
	out, usage, err := d.SkillDrafter.Draft(ctx, in)
	if d.hook != nil {
		d.hook()
	}
	if d.change != nil {
		d.change(&out, &usage, &err)
	}
	return out, usage, err
}

func TestSkillGenerationChecksAdapterOutput(t *testing.T) {
	for _, kind := range []string{"capability", "error_usage", "invalid_usage", "empty_usage"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			in, _ := generationFixture(t, f)
			f.svc.Drafter = generationHookDrafter{SkillDrafter: f.svc.Drafter, change: func(d *SkillDraft, u *ProviderUsage, err *error) {
				switch kind {
				case "capability":
					d.Analysis.Writes = true
				case "error_usage":
					*err = ErrUnavailable
				case "invalid_usage":
					u.Model = "invented-model"
				case "empty_usage":
					u.TotalTokens, u.InputTokens, u.OutputTokens = 0, 0, 0
				}
			}}
			if _, err := f.svc.GenerateSkill(testCtx, f.agent, in); err == nil || len(f.repo.rows(colSkillVersions)) != 0 {
				t.Fatal("untrusted adapter accepted")
			}
			rows := f.repo.rows(colProviderUsage)
			if len(rows) != 1 {
				t.Fatal("missing usage")
			}
			if kind == "error_usage" && (rows[0].Status != JobStatusFailed || rows[0].Cost.TotalTokens != 27) {
				t.Fatal("failed call usage lost")
			}
		})
	}
}

func TestPostgresSkillGenerationBaselineDeletion(t *testing.T) {
	repo := isolatedPostgres(t)
	logBasisJobsOnFailure(t, repo)
	f := deletionReplayFixture(t, repo)
	in, calls := generationFixture(t, f)
	base, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: in.SkillID, Path: in.Path, Content: "# Original procedure", Analysis: detailedSkillAnalysis()})
	if err != nil {
		t.Fatal(err)
	}
	in.ComparisonPlan.BaselineVersion = base.Record.ID
	got, err := f.svc.GenerateSkill(testCtx, f.agent, in)
	if err != nil || got.Record.BaseVersion != base.Record.ID {
		t.Fatal("existing baseline generation", err)
	}
	for _, row := range []Record{got.Record} {
		found := false
		for _, ref := range row.Sources {
			if ref.SourceID == base.Record.Sources[0].SourceID {
				found = true
			}
		}
		if !found {
			t.Fatal("missing baseline lineage")
		}
	}
	sourceID := base.Record.Sources[0].SourceID
	source, err := f.svc.Repo.Get(testCtx, "sources", scopeA, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.Delete(testCtx, f.human, "sources", sourceID, ChangeInput{Scope: scopeA, ExpectedRevision: source.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.GenerateSkill(testCtx, f.agent, in); err == nil || calls.Load() != 1 {
		t.Fatal("deleted source reused")
	}
	drainDeletionReplayFixture(t, f, repo)
	for _, ref := range []Ref{{Collection: colSkillVersions, ID: got.Record.ID}, {Collection: "sources", ID: got.Record.Sources[0].SourceID}} {
		if _, err = f.svc.Repo.Get(testCtx, ref.Collection, scopeA, ref.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("derived baseline content remains", ref, err)
		}
	}
	if _, err = f.svc.GenerateSkill(testCtx, f.agent, in); err == nil || calls.Load() != 1 {
		t.Fatal("erased candidate regenerated")
	}
}

func TestSkillGenerationRechecksAfterCall(t *testing.T) {
	for _, kind := range []string{"lesson", "baseline", "actor", "transfer"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			in, calls := generationFixture(t, f)
			f.svc.Drafter = generationHookDrafter{SkillDrafter: f.svc.Drafter, hook: func() {
				switch kind {
				case "lesson":
					row, err := f.repo.Get(testCtx, "memory_candidates", scopeA, in.Lessons[0].ID)
					if err != nil {
						t.Fatal(err)
					}
					row.Status = "rejected"
					if err = f.repo.Replace(testCtx, "memory_candidates", row, row.Revision); err != nil {
						t.Fatal(err)
					}
				case "baseline":
					_, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: in.SkillID, Path: in.Path, Content: "# Concurrent baseline", Analysis: detailedSkillAnalysis()})
					if err != nil {
						t.Fatal(err)
					}
				case "actor":
					a := f.auth.actors[f.agent]
					a.principal = "other-agent"
					f.auth.actors[f.agent] = a
				case "transfer":
					a := f.auth.actors[f.agent]
					a.external = false
					f.auth.actors[f.agent] = a
				}
			}}
			if _, err := f.svc.GenerateSkill(testCtx, f.agent, in); err == nil || calls.Load() != 1 {
				t.Fatal("changed evidence accepted", err)
			}
			for _, row := range f.repo.rows(colSkillVersions) {
				if row.Kind == proposedSkillKind {
					t.Fatal("candidate stored after revocation")
				}
			}
			rows := f.repo.rows(colProviderUsage)
			if len(rows) != 1 || rows[0].Cost.TotalTokens != 27 || rows[0].Cost.EstimatedCostPicoUSD != 69 {
				t.Fatal("paid usage lost", rows)
			}
		})
	}
}

func TestPostgresSkillGenerationConcurrent(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	in, calls := generationFixture(t, f)
	type result struct {
		out ImportSkillResult
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { out, err := f.svc.GenerateSkill(testCtx, f.agent, in); results <- result{out, err} }()
	}
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.out.Record.ID != b.out.Record.ID || calls.Load() != 1 {
		t.Fatal("concurrent retry", a.err, b.err, calls.Load())
	}
}

func TestPostgresSkillGenerationRollback(t *testing.T) {
	f := newFixture(t)
	repo := isolatedPostgres(t)
	f.svc.Repo = repo
	in, _ := generationFixture(t, f)
	before := map[string]int{}
	for _, col := range []string{"sources", colSkills, colSkillVersions, colJobs} {
		rows, err := repo.ListRecords(testCtx, scopeA, col, 100)
		if err != nil {
			t.Fatal(err)
		}
		before[col] = len(rows)
	}
	f.svc.Repo = relationAuditFailureRepo{Repository: repo, action: "propose:skills"}
	if _, err := f.svc.GenerateSkill(testCtx, f.agent, in); !errors.Is(err, ErrUnavailable) {
		t.Fatal("injected audit failure", err)
	}
	for col, count := range before {
		rows, err := repo.ListRecords(testCtx, scopeA, col, 100)
		if err != nil || len(rows) != count {
			t.Fatal("partial transaction", col, err)
		}
	}
	f.svc.Repo = repo
	got, err := f.svc.GenerateSkill(testCtx, f.agent, in)
	if err != nil {
		t.Fatal("retry after rollback", err)
	}
	rows, err := repo.ListRecords(testCtx, scopeA, colJobs, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, r := range rows {
		if r.Kind == skillGenerationReceiptKind {
			found++
			if r.Text != "" || r.Summary != "" || r.Target == nil || r.Target.ID != got.Record.ID || r.RequestHash == "" {
				t.Fatal("receipt contract")
			}
		}
	}
	if found != 1 {
		t.Fatal("receipt not committed", found)
	}
}

func TestSkillGenerationInheritsBaselineBundle(t *testing.T) {
	checkSkillGenerationBundle(t, newFixture(t))
}
func TestPostgresSkillGenerationBundle(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	checkSkillGenerationBundle(t, f)
}
func checkSkillGenerationBundle(t *testing.T, f *fixture) {
	t.Helper()
	in, _ := generationFixture(t, f)
	files := []SkillBundleFile{{Path: "scripts/logo.png", SHA256: strings.Repeat("ab", 32), Size: 9, Omitted: "binary"}, {Path: "scripts/run.py", Content: "print('pinned')\n", SHA256: ContentHash("print('pinned')\n")}}
	bundle := &SkillBundle{Files: files, SHA256: skillBundleHash(files)}
	base, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: in.SkillID, Path: in.Path, Content: "# Original procedure", Analysis: detailedSkillAnalysis(), Bundle: bundle})
	if err != nil || base.Record.SkillBundleSHA256 != bundle.SHA256 {
		t.Fatal("pinned baseline", err)
	}
	in.ComparisonPlan.BaselineVersion = base.Record.ID
	got, err := f.svc.GenerateSkill(testCtx, f.agent, in)
	if err != nil || got.Record.SkillBundleSHA256 != bundle.SHA256 {
		t.Fatal("generated candidate lost the baseline companions", err)
	}
	resolved, err := f.svc.ResolveSkills(testCtx, f.agent, SkillResolveInput{Scope: scopeA, Refs: []string{in.SkillID + "@2"}})
	if err != nil || len(resolved.Items) != 1 || resolved.Items[0].Bundle == nil || structuredText(resolved.Items[0].Bundle) != structuredText(bundle) {
		t.Fatal("candidate bundle", err)
	}
	report := func(coverage, baseline, candidate string) Record {
		r := &SkillComparisonReport{PlanSHA256: got.ComparisonPlanSHA256, BaselineScore: .5, CandidateScore: .9, CandidateCost: 1, CandidateDurationMS: 1, RegressionPassed: true, SafetyPassed: true}
		if coverage != "" {
			r.CompanionCoverage, r.BundleSHA256 = coverage, map[string]string{"baseline": baseline, "candidate": candidate}
		}
		return Record{Evaluation: &Evaluation{ComparisonReport: r}}
	}
	// Same companions as the baseline: a body-only measurement covers the change.
	if !comparisonPasses(got.Record, report("body_only", bundle.SHA256, bundle.SHA256)) {
		t.Fatal("inherited companions need no file execution")
	}
	if comparisonPasses(got.Record, report("", "", "")) {
		t.Fatal("legacy report accepted for a pinned candidate")
	}
	changed := got.Record
	changed.SkillBundleSHA256 = strings.Repeat("cd", 32)
	for _, coverage := range []string{"body_only", "partial"} {
		if comparisonPasses(changed, report(coverage, bundle.SHA256, changed.SkillBundleSHA256)) {
			t.Fatal("changed companions passed without materialized files", coverage)
		}
	}
	if !comparisonPasses(changed, report("materialized", bundle.SHA256, changed.SkillBundleSHA256)) {
		t.Fatal("materialized comparison rejected")
	}
	if comparisonPasses(changed, report("materialized", bundle.SHA256, bundle.SHA256)) {
		t.Fatal("report bound to another bundle accepted")
	}
}

// Pressing 시작 again after 버리기 brings the discarded draft back as a candidate instead of failing.
func TestSkillGenerationAfterDiscard(t *testing.T) {
	f := newFixture(t)
	in, calls := generationFixture(t, f)
	page, _ := f.svc.ListReflections(testCtx, f.human, scopeA, "review")
	in.Lessons = page.Items[0].CandidateRefs
	reject := func(v Record) {
		t.Helper()
		if _, e := f.svc.Transition(testCtx, f.human, colSkillVersions, v.ID, ChangeInput{Scope: scopeA, ExpectedRevision: v.Revision, Status: "rejected"}); e != nil {
			t.Fatal(e)
		}
	}
	first, err := f.svc.GenerateSkill(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	reject(first.Record)
	same, err := f.svc.GenerateSkill(testCtx, f.human, in)
	if err != nil || same.Record.ID != first.Record.ID || same.Record.Status != "candidate" || same.Record.Revision <= first.Record.Revision || calls.Load() != 1 {
		t.Fatal("same request after discard", err, same.Record.Status, calls.Load())
	}
	reject(same.Record)
	in.EventID = "generate-2"
	again, err := f.svc.GenerateSkill(testCtx, f.human, in)
	if err != nil || again.Record.ID != first.Record.ID || again.Record.Status != "candidate" || again.Record.ApprovedBy != "" || again.Record.ValidationID != "" || calls.Load() != 2 {
		t.Fatal("new request after discard", err, again.Record.Status)
	}
	skill, _ := f.repo.Get(testCtx, colSkills, scopeA, in.SkillID)
	if len(skill.VersionHistory) != 1 {
		t.Fatal("discarded draft duplicated in history", skill.VersionHistory)
	}
	// An agent's draft the owner discarded stays discarded.
	in.EventID = "generate-agent"
	byAgent, err := f.svc.GenerateSkill(testCtx, f.agent, in)
	if err != nil {
		t.Fatal(err)
	}
	reject(byAgent.Record)
	if _, err = f.svc.GenerateSkill(testCtx, f.agent, in); !errors.Is(err, ErrConflict) {
		t.Fatal("an agent revived a discarded draft", err)
	}
}
