package memory

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestSkillComparisonRunner(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) { checkSkillComparisonRunner(t, newFixture(t), existing) })
	}
}
func TestPostgresSkillComparisonRunner(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) {
			f := newFixture(t)
			f.svc.Repo = isolatedPostgres(t)
			checkSkillComparisonRunner(t, f, existing)
		})
	}
}
func checkSkillComparisonRunner(t *testing.T, f *fixture, existing bool) {
	t.Helper()
	// The test binary is the adapter (see fakeAdapter); a sentinel file breaks it.
	adapter, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	broken := filepath.Join(dir, "adapter.broken")
	executable, e := os.ReadFile(adapter)
	if e != nil {
		t.Fatal(e)
	}
	adapterEnv := map[string]string{"ARKISM_TEST_HELPER": "adapter", "ARKISM_TEST_ADAPTER_BROKEN": broken}
	var cases []map[string]any
	for _, kind := range []string{"quality", "regression", "safety"} {
		cases = append(cases, map[string]any{"id": kind, "category": kind, "input": kind, "expected": kind, "held_out": true})
	}
	dataset, _ := json.Marshal(map[string]any{"cases": cases})
	environment, _ := json.Marshal(map[string]any{"argv": []string{adapter}, "files": map[string]string{adapter: ContentHash(string(executable))}, "env": adapterEnv, "timeout_ms": 2000, "max_output_bytes": 1024})
	in := proposalFixture(t, f)
	if existing {
		baseline := in.Skill
		baseline.ComparisonPlan = nil
		baseline.Content = "# Baseline"
		imported, e := f.svc.ImportSkill(testCtx, f.human, baseline)
		if e != nil {
			t.Fatal(e)
		}
		in.Skill.ComparisonPlan.BaselineVersion = imported.Record.ID
	}
	in.Skill.ComparisonPlan.DatasetSHA256 = ContentHash(string(dataset))
	in.Skill.ComparisonPlan.EnvironmentSHA256 = ContentHash(string(environment))
	in.Skill.ComparisonPlan.MaxDurationMS = 10000
	proposal, e := f.svc.ProposeSkill(testCtx, f.agent, in)
	if e != nil {
		t.Fatal(e)
	}
	proposalJSON, _ := json.Marshal(proposal)
	for name, data := range map[string][]byte{"dataset.json": dataset, "environment.json": environment, "proposal.json": proposalJSON, "token": []byte("comparison-service")} {
		if e = os.WriteFile(filepath.Join(dir, name), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	f.auth.actors["comparison-service"] = actor("independent-runner", "service", true, allVerbs...)
	server := httptest.NewServer(Handler(f.svc))
	defer server.Close()
	output := filepath.Join(dir, "result.json")
	runnerEnv := arkismEnv(dir, server.URL, filepath.Join(dir, "token"))
	cmd := exec.Command(arkismBinary(t), "evaluate-skill-candidate", "--url", server.URL, "--token-file", filepath.Join(dir, "token"), "--proposal", filepath.Join(dir, "proposal.json"), "--dataset", filepath.Join(dir, "dataset.json"), "--environment", filepath.Join(dir, "environment.json"), "--execute", "--publish", "--output", output)
	cmd.Env = runnerEnv
	raw, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("runner: %v %s", e, raw)
	}
	receipt, e := os.ReadFile(output + ".receipt")
	if e != nil {
		t.Fatal(e)
	}
	var result struct {
		Evaluation Ref   `json:"evaluation"`
		Runs       []Ref `json:"runs"`
	}
	if e = json.Unmarshal(receipt, &result); e != nil || len(result.Runs) != 2 {
		t.Fatal("receipt", e)
	}
	evaluation, e := f.svc.Repo.Get(testCtx, "evaluations", scopeA, result.Evaluation.ID)
	if e != nil || !independentEvaluation(evaluation) || !comparisonPasses(proposal.Record, evaluation) {
		t.Fatal("evaluation", e)
	}
	if len(evaluation.Relations) != 2 {
		t.Fatal("execution refs absent")
	}
	for _, ref := range result.Runs {
		run, e := f.svc.Repo.Get(testCtx, "agent_runs", scopeA, ref.ID)
		if e != nil || run.Run == nil || run.Run.FinishedAt == nil {
			t.Fatal("completed execution", e)
		}
	}
	current, e := f.svc.Repo.Get(testCtx, "skill_versions", scopeA, proposal.Record.ID)
	if e != nil || current.Status != "candidate" {
		t.Fatal("runner granted approval", e)
	}
	approved, e := f.svc.Transition(testCtx, f.human, "skill_versions", current.ID, ChangeInput{Scope: scopeA, ExpectedRevision: current.Revision, Status: "approved", ValidationID: evaluation.ID})
	if e != nil || approved.Status != "approved" {
		t.Fatal("measured evidence approval", e)
	}
	// A bounded trial pins the approved version without changing global routing.
	pin := in.Skill.SkillID + "@" + strconv.Itoa(proposal.Version)
	trial, e := f.svc.ResolveSkills(testCtx, f.agent, SkillResolveInput{Scope: scopeA, Refs: []string{pin, in.Skill.SkillID + "@latest-approved"}})
	if e != nil || trial.Items[0].Error != "" || trial.Items[0].ValidationStatus != "approved" || trial.Items[0].LatestApproved {
		t.Fatal("approved trial pin", e)
	}
	if existing && trial.Items[1].VersionID != in.Skill.ComparisonPlan.BaselineVersion || !existing && trial.Items[1].Error != "no_approved_version" {
		t.Fatal("trial changed default routing")
	}
	trialInput, _ := json.Marshal(map[string]any{"input": "quality", "skill": trial.Items[0].Instructions})
	trialCommand := exec.Command(adapter)
	trialCommand.Env = []string{"ARKISM_TEST_HELPER=adapter", "ARKISM_TEST_ADAPTER_BROKEN=" + broken}
	trialCommand.Stdin = bytes.NewReader(trialInput)
	trialOutput, e := trialCommand.Output()
	var measured struct {
		Output string `json:"output"`
	}
	if e != nil || json.Unmarshal(trialOutput, &measured) != nil || measured.Output != "quality" {
		t.Fatal("pinned trial execution", e)
	}
	applied, e := f.svc.ApplySkill(testCtx, f.human, in.Skill.SkillID, ChangeInput{Scope: scopeA, ExpectedRevision: proposal.Skill.Revision, VersionID: approved.ID}, false)
	if e != nil || applied.ActiveVersion != approved.ID {
		t.Fatal("apply measured improvement", e)
	}
	if e = os.Remove(output + ".receipt"); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(broken, nil, 0600); e != nil {
		t.Fatal(e)
	}
	resume := exec.Command(arkismBinary(t), "evaluate-skill-candidate", "--url", server.URL, "--token-file", filepath.Join(dir, "token"), "--proposal", filepath.Join(dir, "proposal.json"), "--resume-result", output, "--publish")
	resume.Env = runnerEnv
	if raw, e = resume.CombinedOutput(); e != nil {
		t.Fatalf("resume %v %s", e, raw)
	}
	again, e := os.ReadFile(output + ".receipt")
	if e != nil || string(again) != string(receipt) {
		t.Fatal("receipt changed", e)
	}
	evaluations, e := f.svc.Repo.EvaluationsFor(testCtx, scopeA, Ref{Collection: colSkillVersions, ID: proposal.Record.ID, Revision: proposal.Record.Revision})
	if e != nil || len(evaluations) != 1 {
		t.Fatal("duplicate evaluation after resume", e)
	}
	if existing {
		baseline, err := f.svc.Repo.Get(testCtx, colSkillVersions, scopeA, in.Skill.ComparisonPlan.BaselineVersion)
		if err != nil {
			t.Fatal(err)
		}
		change := ChangeInput{Scope: scopeA, ExpectedRevision: applied.Revision, VersionID: baseline.ID}
		if _, err = f.svc.ApplySkill(testCtx, f.human, in.Skill.SkillID, change, true); err == nil {
			t.Fatal("rollback accepted without baseline evaluation")
		}
		source := f.source()
		evidence := f.create(f.human2, "evaluations", ratingInput(CreateInput{Scope: scopeA, Text: "Independent baseline recovery check", Passed: true, Sources: refs(source), Target: &Ref{Collection: colSkillVersions, ID: baseline.ID, Revision: baseline.Revision}}))
		change.ValidationID = evidence.ID
		if _, err = f.svc.ApplySkill(testCtx, f.agent, in.Skill.SkillID, change, true); err == nil {
			t.Fatal("agent performed owner rollback")
		}
		if _, err = f.svc.ApplySkill(testCtx, f.human, in.Skill.SkillID, change, true); err != nil {
			t.Fatal("evaluated baseline rollback", err)
		}
		resolved, err := f.svc.ResolveSkills(testCtx, f.agent, SkillResolveInput{Scope: scopeA, Refs: []string{in.Skill.SkillID + "@latest-approved"}})
		if err != nil || resolved.Items[0].Error != "" || resolved.Items[0].VersionID != baseline.ID || resolved.Items[0].Instructions != "# Baseline" {
			t.Fatal("baseline routing not restored", err)
		}
		restored, err := f.svc.Repo.Get(testCtx, colSkillVersions, scopeA, baseline.ID)
		if err != nil || restored.ValidationID != evidence.ID {
			t.Fatal("rollback evaluation was not retained", err)
		}
		evidence, err = f.svc.Repo.Get(testCtx, "evaluations", scopeA, evidence.ID)
		if err != nil {
			t.Fatal("read rollback evaluation", err)
		}
		revision := evidence.Revision
		evidence.Status = "expired"
		evidence.Revision++
		if err = f.svc.Repo.Replace(testCtx, "evaluations", evidence, revision); err != nil {
			t.Fatal("expire rollback evaluation", err)
		}
		resolved, err = f.svc.ResolveSkills(testCtx, f.agent, SkillResolveInput{Scope: scopeA, Refs: []string{in.Skill.SkillID + "@latest-approved"}})
		if err != nil || resolved.Items[0].Error != "skill_unavailable" {
			t.Fatal("revoked rollback evaluation still routes", err)
		}
		// Old deployments did not retain the rollback evaluation. They must not
		// be mistaken for a never-replaced, manually imported baseline.
		revision = restored.Revision
		restored.ValidationID = ""
		restored.Revision++
		if err = f.svc.Repo.Replace(testCtx, colSkillVersions, restored, revision); err != nil {
			t.Fatal("legacy rollback fixture", err)
		}
		resolved, err = f.svc.ResolveSkills(testCtx, f.agent, SkillResolveInput{Scope: scopeA, Refs: []string{in.Skill.SkillID + "@latest-approved"}})
		if err != nil || resolved.Items[0].Error != "skill_unavailable" {
			t.Fatal("legacy rollback bypassed evaluation", err)
		}
	}
}
