package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/memory"
)

type selectionMeasurementFixture struct {
	base, candidate map[string]any
	records         map[string]map[string]any
	summary         map[string]any
	evaluationID    string
}

func measurementMap(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err = json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}
func syntheticSelectedMeasurement(t *testing.T) *selectionMeasurementFixture {
	t.Helper()
	t.Setenv("ARKISM_WORKSPACE", "workspace")
	t.Setenv("ARKISM_PROJECT", "project")
	t.Setenv("ARKISM_USER", "user")
	t.Setenv("ARKISM_AGENT", "")
	scope := memory.Scope{WorkspaceID: "workspace", ProjectID: "project", UserID: "user"}
	record := func(id, kind string) memory.Record {
		return memory.Record{ID: id, Kind: kind, Scope: scope, Status: "active", Revision: 1, CreatedBy: "judge", CreatedByType: "service"}
	}
	base := record("skill-"+digest([]byte("base")), "version")
	candidate := record("skill-"+digest([]byte("candidate")), "version")
	base.SkillID, candidate.SkillID = "test/pair", "test/pair"
	base.CreatedBy, candidate.CreatedBy = "owner", "owner"
	base.Analysis = &memory.SkillAnalysis{ContentHash: digest([]byte("old instructions"))}
	candidate.Analysis = &memory.SkillAnalysis{ContentHash: digest([]byte("new instructions"))}
	plan := &memory.SkillComparisonPlan{BaselineVersion: base.ID, DatasetSHA256: digest([]byte("dataset")), EnvironmentSHA256: digest([]byte("environment")), MinScoreGain: 0.5, MaxCost: 1, MaxDurationMS: 100}
	candidate.ComparisonPlan = plan
	planJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	report := memory.SkillComparisonReport{PlanSHA256: digest(planJSON), BaselineScore: 0, CandidateScore: 1, CandidateDurationMS: 3, RegressionPassed: true, SafetyPassed: true}
	target := memory.Ref{Collection: "skill_versions", ID: candidate.ID, Revision: 1}
	v := memory.SkillComparisonResult{FormatVersion: 1, BaselineID: base.ID, BaselineRevision: 1, Candidate: target, ContentSHA256: map[string]string{"baseline": base.Analysis.ContentHash, "candidate": candidate.Analysis.ContentHash}, DatasetSHA256: plan.DatasetSHA256, EnvironmentSHA256: plan.EnvironmentSHA256, Report: &report, Passed: true}
	end := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	start := end.Add(-time.Millisecond)
	for _, category := range []string{"quality", "regression", "safety"} {
		for _, variant := range []string{"baseline", "candidate"} {
			v.Cases = append(v.Cases, memory.SkillComparisonCase{CaseSHA256: digest([]byte(category)), OutputSHA256: digest([]byte(category + variant)), Category: category, Variant: variant, Passed: category != "quality" || variant == "candidate", DurationMS: 1, StartedAt: start, FinishedAt: end})
		}
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "comparison-" + digest([]byte("measured pair"))
	source := record(prefix+"-source", "skill_comparison_result")
	source.SourceRef, source.OriginProduct, source.Text, source.ContentHash = prefix, "arkism", string(data), digest(data)
	source.Sources = []memory.SourceRef{{SourceID: source.ID, Locator: "comparison", Span: &memory.SourceSpan{ContentHash: source.ContentHash, End: len(data)}}}
	e := record(prefix+"-evaluation", "skill_comparison")
	measured := report
	measured.BaselineDurationMS = 3
	e.Target, e.Passed, e.Sources = &target, true, source.Sources
	e.Evaluation = &memory.Evaluation{EvalType: "automated_check", Passed: &v.Passed, ComparisonReport: &measured}
	records := map[string]map[string]any{source.ID: measurementMap(t, source)}
	for i, outcome := range []string{"failed", "success"} {
		run := record(prefix+"-"+[]string{"baseline", "candidate"}[i], "skill_comparison")
		run.Sources = source.Sources
		run.Run = &memory.RunDetails{AgentID: "skill-comparison", TaskType: "skill_comparison", Outcome: outcome, StartedAt: start, FinishedAt: &end}
		e.Relations = append(e.Relations, memory.Ref{Collection: "agent_runs", ID: run.ID, Revision: 1})
		records[run.ID] = measurementMap(t, run)
	}
	records[e.ID] = measurementMap(t, e)
	raw := measurementMap(t, candidate)
	raw["comparison_plan_sha256"] = report.PlanSHA256
	return &selectionMeasurementFixture{measurementMap(t, base), raw, records, map[string]any{"authority": "data_only_untrusted", "target": map[string]any{"collection": target.Collection, "id": target.ID, "revision": target.Revision}, "evaluation_refs": []any{map[string]any{"collection": "evaluations", "id": e.ID, "revision": 1}}}, e.ID}
}
func TestSelectionMeasuredEvidenceLinks(t *testing.T) {
	scenarios := []struct{ name, reason string }{
		{"valid", ""}, {"retained historical", ""}, {"same", "same_version"}, {"scope", "unverified_version_records"},
		{"summary denied", "evidence_unavailable"}, {"empty", "no_linked_measurement"}, {"limit", "too_many_measurements"},
		{"missing source", "evidence_unavailable_or_inconsistent"}, {"wrong source", "evidence_source_mismatch"}, {"wrong revision", "evidence_revision_mismatch"},
		{"wrong run", "evidence_run_mismatch"}, {"missing run", "evidence_unavailable_or_inconsistent"}, {"wrong plan", "evidence_unavailable_or_inconsistent"},
	}
	for _, tc := range scenarios {
		t.Run(tc.name, func(t *testing.T) { checkSelectedMeasurementScenario(t, tc.name, tc.reason) })
	}
}
func checkSelectedMeasurementScenario(t *testing.T, name, reason string) {
	t.Helper()
	f := syntheticSelectedMeasurement(t)
	e := f.records[f.evaluationID]
	switch name {
	case "retained historical":
		f.candidate["revision"] = 2
		f.candidate["validation_id"] = f.evaluationID
		asMap(f.summary["target"])["revision"] = 2
		f.summary["evaluation_refs"] = []any{}
	case "same":
		f.base = f.candidate
	case "scope":
		asMap(f.candidate["scope"])["user_id"] = "other"
	case "empty":
		f.summary["evaluation_refs"] = []any{}
	case "limit":
		var refs []any
		for n := 0; n < 33; n++ {
			refs = append(refs, map[string]any{"collection": "evaluations", "id": fmt.Sprintf("comparison-%064x-evaluation", n)})
		}
		f.summary["evaluation_refs"] = refs
	case "missing source":
		delete(f.records, str(asMap(asList(e["sources"])[0])["source_id"]))
	case "wrong source":
		asMap(asList(e["sources"])[0])["source_id"] = "unrelated"
	case "wrong revision":
		asMap(e["target"])["revision"] = 9
	case "wrong run":
		asMap(asList(e["relations"])[0])["collection"] = "other"
	case "missing run":
		delete(f.records, str(asMap(asList(e["relations"])[0])["id"]))
	case "wrong plan":
		f.candidate["comparison_plan_sha256"] = digest([]byte("unrelated"))
	}
	saved := reviewCall
	t.Cleanup(func() { reviewCall = saved })
	reviewCall = func(method, path string, body any, token string) (map[string]any, error) {
		if method == "POST" && path == "/v1/evaluations/summary" && name != "summary denied" {
			return f.summary, nil
		}
		if method == "GET" {
			url, _, _ := strings.Cut(path, "?")
			parts := strings.Split(url, "/")
			if r := f.records[parts[len(parts)-1]]; r != nil {
				return r, nil
			}
		}
		return nil, errors.New("not accessible")
	}
	got := selectedMeasurements(f.base, f.candidate)
	if reason != "" {
		if got["reason"] != reason || got["status"] != "undetermined" {
			t.Fatalf("%s: got %+v", name, got)
		}
		return
	}
	rows, ok := got["records"].([]map[string]any)
	if !ok || len(rows) != 1 || got["status"] != "verified_recorded_measurements" {
		t.Fatal(got)
	}
	n, _ := host.Int(rows[0]["paired_cases"])
	if n != 3 || rows[0]["passed"] != true || rows[0]["current_revisions_match"] != (name != "retained historical") {
		t.Fatal(rows[0])
	}
}
