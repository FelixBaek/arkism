package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSkillTrial(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) { checkSkillTrial(t, newFixture(t), existing) })
	}
}
func TestPostgresSkillTrial(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) {
			f := newFixture(t)
			f.svc.Repo = isolatedPostgres(t)
			checkSkillTrial(t, f, existing)
		})
	}
}
func checkSkillTrial(t *testing.T, f *fixture, existing bool) {
	t.Helper()
	in := proposalFixture(t, f)
	in.Skill.Bundle = bundleForTest(SkillBundleFile{Path: "scripts/run.py", Content: "print('trial')\n"})
	baseline := in.Skill
	baseline.ComparisonPlan = nil
	baseline.Content = "# Baseline"
	var base ImportSkillResult
	var err error
	if existing {
		base, err = f.svc.ImportSkill(testCtx, f.human, baseline)
		if err != nil {
			t.Fatal("import baseline", err)
		}
	}
	in.Skill.ComparisonPlan.BaselineVersion = base.Record.ID
	proposed, err := f.svc.ProposeSkill(testCtx, f.agent, in)
	if err != nil {
		t.Fatal("propose candidate", err)
	}
	start := SkillTrialInput{Scope: scopeA, EventID: "one", SkillID: in.Skill.SkillID, Candidate: Ref{Collection: colSkillVersions, ID: proposed.Record.ID, Revision: proposed.Record.Revision}, Runtime: "codex", EndsAt: time.Now().UTC().Add(time.Hour)}
	if _, err = f.svc.StartSkillTrial(testCtx, f.human, start); !errors.Is(err, ErrConflict) {
		t.Fatal("unapproved trial", err)
	}
	pass := true
	target := start.Candidate
	ev := f.create(f.human2, "evaluations", CreateInput{Scope: scopeA, Text: "Independent comparison", Sources: refs(f.source()), Target: &target, Evaluation: &Evaluation{EvalType: "user_rating", Passed: &pass, ComparisonReport: &SkillComparisonReport{PlanSHA256: proposed.ComparisonPlanSHA256, BaselineScore: .5, CandidateScore: .8, CandidateCost: 1, CandidateDurationMS: 500, RegressionPassed: true, SafetyPassed: true, CompanionCoverage: "materialized", BundleSHA256: map[string]string{"baseline": base.Record.SkillBundleSHA256, "candidate": proposed.Record.SkillBundleSHA256}}}})
	approved, err := f.svc.Transition(testCtx, f.human, colSkillVersions, target.ID, ChangeInput{Scope: scopeA, ExpectedRevision: target.Revision, Status: "approved", ValidationID: ev.ID})
	if err != nil {
		t.Fatal("approve candidate", err)
	}
	start.Candidate.Revision = approved.Revision
	if _, err = f.svc.StartSkillTrial(testCtx, f.agent, start); !errors.Is(err, ErrDenied) {
		t.Fatal("agent start", err)
	}
	trial, err := f.svc.StartSkillTrial(testCtx, f.human, start)
	if err != nil {
		t.Fatal("start", err)
	}
	again, err := f.svc.StartSkillTrial(testCtx, f.human, start)
	if err != nil || again.ID != trial.ID || again.Revision != 1 {
		t.Fatal("retry", err)
	}

	raw, _ := json.Marshal(start)
	request := httptest.NewRequest("POST", "/v1/skill-trials", bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+f.human)
	response := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(response, request)
	var viaHTTP Record
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &viaHTTP) != nil || viaHTTP.ID != trial.ID || viaHTTP.SkillTrial == nil {
		t.Fatal("HTTP trial replay", response.Code)
	}
	changed := start
	changed.Runtime = "gemini"
	if _, err = f.svc.StartSkillTrial(testCtx, f.human, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed retry", err)
	}
	load := SkillResolveInput{Scope: scopeA, Refs: []string{in.Skill.SkillID + "@latest-approved"}, TrialID: trial.ID, Runtime: "codex"}
	assertLoad := func(version, status string) {
		t.Helper()
		got, e := f.svc.ResolveSkills(testCtx, f.agent, load)
		if e != nil || len(got.Items) != 1 || got.Items[0].VersionID != version || got.Items[0].TrialStatus != status || (got.Items[0].Error != "" && !(version == "" && got.Items[0].Error == "no_approved_version")) {
			t.Fatal("resolve trial", status, e, got)
		}
	}
	assertLoad(approved.ID, "active")
	view, err := f.svc.InspectSkillTrial(testCtx, f.human, trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: in.Skill.SkillID})
	if err != nil || view.Status != "active" || view.ResolvedVersionID != approved.ID {
		t.Fatal("inspect active", err)
	}

	// Real router binary -> HTTP -> repository, without replacing its transport.
	server := httptest.NewServer(Handler(f.svc))
	defer server.Close()
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err = os.WriteFile(tokenFile, []byte(f.agent), 0600); err != nil {
		t.Fatal(err)
	}
	ref := fmt.Sprintf("%s@%d#%s", in.Skill.SkillID, proposed.Version, trial.ID)
	responses := arkismMCP(t, arkismEnv(dir, server.URL, tokenFile, "ARKISM_RUNTIME=codex", "ARKISM_SKILL_CACHE="+dir),
		mcpCall{"load_skill", map[string]any{"skill_id": in.Skill.SkillID, "trial_id": trial.ID}},
		mcpCall{"read_skill_file", map[string]any{"ref": ref, "path": "scripts/run.py"}})
	var item struct {
		TrialStatus string `json:"trial_status"`
		TrialID     string `json:"trial_id"`
		ContentHash string `json:"content_hash"`
		Ref         string `json:"ref"`
		Version     int    `json:"version"`
	}
	mcpResult(t, responses[0], &item)
	if item.TrialStatus != "active" || item.TrialID != trial.ID || item.ContentHash != approved.Analysis.ContentHash || item.Ref != ref || item.Version != proposed.Version {
		t.Fatalf("router trial: %+v", item)
	}
	var file struct {
		Content string `json:"content"`
	}
	mcpResult(t, responses[1], &file)
	if file.Content != "print('trial')\n" {
		t.Fatalf("router trial file: %+v", file)
	}

	now := time.Now().UTC()
	usage := SkillUsage{TrialID: trial.ID, SkillID: in.Skill.SkillID, Version: proposed.Version, VersionID: approved.ID, ContentHash: approved.Analysis.ContentHash, Runtime: "codex", Status: "success", StartedAt: now, CompletedAt: now}
	checkUsage := func() error {
		raw, _ := json.Marshal(usage)
		return checkSkillUsageEvents(testCtx, f.svc.Repo, scopeA, []TraceEventInput{{Kind: skillUsageEventKind, Payload: raw}}, Scope{})
	}
	if err = checkUsage(); err != nil {
		t.Fatal("trial usage", err)
	}
	usage.Runtime = "gemini"
	if err = checkUsage(); !errors.Is(err, ErrConflict) {
		t.Fatal("trial usage runtime", err)
	}
	usage.Runtime = "codex"
	usage.OfflineCache = true
	if err = checkUsage(); !errors.Is(err, ErrConflict) {
		t.Fatal("offline trial usage", err)
	}
	usage.OfflineCache = false

	normal, err := f.svc.ResolveSkills(testCtx, f.agent, SkillResolveInput{Scope: scopeA, Refs: load.Refs})
	if err != nil || normal.Items[0].VersionID != base.Record.ID {
		t.Fatal("global routing changed", err)
	}
	load.Runtime = "claude_code"
	if _, err = f.svc.ResolveSkills(testCtx, f.agent, load); !errors.Is(err, ErrDenied) {
		t.Fatal("runtime boundary", err)
	}
	load.Runtime = "codex"
	load.Scope = scopeB
	if _, err = f.svc.ResolveSkills(testCtx, f.agent, load); err == nil {
		t.Fatal("scope boundary")
	}
	load.Scope = scopeA
	// A changed/withdrawn independent evaluation immediately removes trial eligibility.
	ev, err = f.svc.Repo.Get(testCtx, "evaluations", scopeA, ev.ID)
	if err != nil {
		t.Fatal("read current evaluation", err)
	}
	prior := ev.Revision
	ev.Revision++
	ev.Status = "expired"
	if err = f.svc.Repo.Replace(testCtx, "evaluations", ev, prior); err != nil {
		t.Fatal("withdraw evaluation", err)
	}
	assertLoad(base.Record.ID, "invalidated")
	if _, err = f.svc.StopSkillTrial(testCtx, f.agent, trial.ID, SkillTrialStopInput{Scope: scopeA, SkillID: in.Skill.SkillID, ExpectedRevision: 1}); !errors.Is(err, ErrDenied) {
		t.Fatal("agent stop", err)
	}
	stopped, err := f.svc.StopSkillTrial(testCtx, f.human, trial.ID, SkillTrialStopInput{Scope: scopeA, SkillID: in.Skill.SkillID, ExpectedRevision: 1})
	if err != nil || stopped.Status != "stopped" {
		t.Fatal("stop invalid trial", err)
	}
	assertLoad(base.Record.ID, "stopped")
	view, err = f.svc.InspectSkillTrial(testCtx, f.human, trial.ID, SkillTrialReadInput{Scope: scopeA, SkillID: in.Skill.SkillID})
	if err != nil || view.Status != "stopped" || view.ResolvedVersionID != base.Record.ID {
		t.Fatal("inspect fallback", err)
	}

	if err = checkUsage(); err != nil {
		t.Fatal("completion of already loaded trial", err)
	}
	usage.StartedAt = time.Now().UTC()
	usage.CompletedAt = usage.StartedAt
	if err = checkUsage(); !errors.Is(err, ErrConflict) {
		t.Fatal("new usage after stop", err)
	}

	if _, err = f.svc.StopSkillTrial(testCtx, f.human, trial.ID, SkillTrialStopInput{Scope: scopeA, SkillID: in.Skill.SkillID, ExpectedRevision: 1}); err != nil {
		t.Fatal("stop retry", err)
	}
	again, err = f.svc.StartSkillTrial(testCtx, f.human, start)
	if err != nil || again.Status != "stopped" {
		t.Fatal("start resurrected trial", err)
	}
	// Time expiry requires neither a background worker nor an API mutation.
	r, err := f.svc.Repo.Get(testCtx, colSkillTrials, scopeA, trial.ID)
	if err != nil {
		t.Fatal("read trial", err)
	}
	prev := r.Revision
	bound := *r.SkillTrial
	bound.EndsAt = time.Now().Add(-time.Minute)
	r.SkillTrial = &bound
	r.Status = "active"
	r.Revision++
	if err = f.svc.Repo.Replace(testCtx, colSkillTrials, r, prev); err != nil {
		t.Fatal("expire trial", err)
	}
	assertLoad(base.Record.ID, "expired")

	if existing {
		for n := 0; n < 103; n++ {
			copy := r
			copy.ID = "trial-" + ContentHash(fmt.Sprint(n))
			if err = f.svc.Repo.Insert(testCtx, colSkillTrials, copy); err != nil {
				t.Fatal("seed page", err)
			}
		}
		foreign := r
		foreign.ID = "trial-other"
		foreign.SkillID = "different"
		if err = f.svc.Repo.Insert(testCtx, colSkillTrials, foreign); err != nil {
			t.Fatal(err)
		}
		input := SkillTrialListInput{Scope: scopeA, SkillID: in.Skill.SkillID, CandidateID: approved.ID, Limit: 100}
		page, e := f.svc.ListSkillTrials(testCtx, f.human, input)
		if e != nil || len(page.Items) != 100 || page.NextCursor == "" {
			t.Fatal("first trial page", e, len(page.Items))
		}
		seen := map[string]bool{}
		for _, item := range page.Items {
			seen[item.Record.ID] = true
			if item.Status != "expired" || item.ResolvedVersionID != base.Record.ID {
				t.Fatal("page status")
			}
		}
		input.After = page.NextCursor
		next, e := f.svc.ListSkillTrials(testCtx, f.human, input)
		if e != nil || len(next.Items) != 4 || next.NextCursor != "" {
			t.Fatal("second trial page", e, len(next.Items))
		}
		for _, item := range next.Items {
			if seen[item.Record.ID] {
				t.Fatal("duplicate page item")
			}
		}
		input.SkillID = "different"
		if _, e = f.svc.ListSkillTrials(testCtx, f.human, input); !errors.Is(e, ErrInvalid) {
			t.Fatal("cursor binding", e)
		}
	}
	current, err := f.svc.Repo.Get(testCtx, colSkills, scopeA, in.Skill.SkillID)
	if err != nil || current.ActiveVersion != base.Record.ID || current.Revision != proposed.Skill.Revision {
		t.Fatal("trial mutated baseline", err)
	}
}

func (f *fakeRepo) ListSkillTrialRecords(_ context.Context, scope Scope, skillID, candidateID string, limit int, after string) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := []Record{}
	for _, r := range f.rowsLocked(colSkillTrials) {
		if r.Scope == scope && r.SkillID == skillID && r.SkillTrial != nil && r.SkillTrial.Candidate.ID == candidateID && r.ID > after {
			rows = append(rows, r)
		}
	}
	if len(rows) > limit+1 {
		rows = rows[:limit+1]
	}
	return rows, nil
}

func TestStopSkillTrialClosesCheckJob(t *testing.T) {
	f, candidate, trial, _ := skillAutomationFixture(t)
	if _, e := f.svc.StopSkillTrial(testCtx, f.human, trial.ID, SkillTrialStopInput{Scope: scopeA, SkillID: candidate.SkillID, ExpectedRevision: trial.Revision}); e != nil {
		t.Fatal(e)
	}
	if job, e := f.repo.Get(testCtx, colJobs, scopeA, skillTrialCheckID(scopeA, trial.ID)); e != nil || job.Status != JobStatusDone {
		t.Fatal("stopped trial left its check job open", job.Status, e)
	}
}
