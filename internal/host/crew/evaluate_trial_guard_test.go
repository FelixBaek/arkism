package crew

import (
	"maps"
	"testing"
)

type comparisonTrialViewAPI struct {
	fakeEvalAPI
	status   string
	revision int
}

func (a *comparisonTrialViewAPI) request(path string, payload any) (any, error) {
	if path != "/v1/skill-trials/qa/inspect" {
		return nil, cmpErr("unexpected_trial_request")
	}
	return map[string]any{"status": a.status, "record": map[string]any{"skill_trial": map[string]any{"candidate": map[string]any{"id": "candidate", "revision": a.revision}}}}, nil
}

func TestFreshTrialComparisonRequiresActiveMatchingRevision(t *testing.T) {
	f := newEvalFixture(t, "adapter-echo")
	row := approvedTrialCandidate(f, 0.1)
	proposal := map[string]any{"record": row, "comparison_plan_sha256": row["comparison_plan_sha256"]}
	for _, tc := range []struct {
		status   string
		revision int
		valid    bool
	}{{"stopped", 1, false}, {"active", 2, false}, {"active", 1, true}} {
		api := &comparisonTrialViewAPI{fakeEvalAPI: fakeEvalAPI{records: map[string]any{"skill_versions": row, "skills": map[string]any{"status": "active", "active_version": ""}, "sources": map[string]any{"text": "verify"}}}, status: tc.status, revision: tc.revision}
		got, err := prepareEvaluation(api, proposal, "qa")
		if tc.valid {
			if err != nil || got["trial_id"] != "qa" {
				t.Fatal(got, err)
			}
		} else if code(err) != "trial_candidate_changed" {
			t.Fatalf("%s/%d: %v", tc.status, tc.revision, err)
		}
	}
}

func TestRollbackQualificationPlanUsesIndependentPins(t *testing.T) {
	contract := map[string]any{"dataset_sha256": "independent", "environment_sha256": "verified", "max_cost": 2, "max_duration_ms": 800}
	original := map[string]any{"dataset_sha256": "original", "holdout": true, "rollback_qualification": contract}
	prepared := map[string]any{"plan": original}
	fixed := measurementPlan(prepared, true)
	for key, want := range contract {
		if fixed[key] != want {
			t.Errorf("%s: got %v, want %v", key, fixed[key], want)
		}
	}
	if _, held := fixed["holdout"]; held {
		t.Fatal("qualification inherited the rollout holdout")
	}
	if !same(measurementPlan(prepared, false), original) || original["dataset_sha256"] != "original" || original["holdout"] != true {
		t.Fatal("qualification modified the normal plan")
	}
}

func TestRollbackQualificationManifestRejectsChangedCases(t *testing.T) {
	testCase := map[string]any{"category": "regression", "input": "original"}
	dataset := map[string]any{"cases": []any{testCase}}
	valid := map[string]any{"case_sha256": digest([]byte(mustCanonical(testCase))), "category": "regression"}
	changed := maps.Clone(testCase)
	changed["input"] = "changed"
	wrongCategory := maps.Clone(valid)
	wrongCategory["category"] = "quality"
	for _, tc := range []struct {
		name  string
		cases map[string]any
		tasks any
		want  string
	}{
		{"valid", dataset, []any{valid}, ""},
		{"changed content", map[string]any{"cases": []any{changed}}, []any{valid}, "rollback_task_manifest_mismatch"},
		{"wrong category", dataset, []any{wrongCategory}, "rollback_task_manifest_mismatch"},
		{"invalid fingerprint", dataset, []any{map[string]any{"case_sha256": "invalid", "category": "regression"}}, "rollback_task_manifest_mismatch"},
		{"wrong manifest size", dataset, []any{}, "rollback_task_manifest_mismatch"},
		{"duplicate task", map[string]any{"cases": []any{testCase, changed}}, []any{valid, valid}, "rollback_task_manifest_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRollbackTasks(tc.cases, map[string]any{"tasks": tc.tasks})
			if tc.want == "" && err == nil {
				return
			}
			if err == nil || code(err) != tc.want {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

type qualificationReceiptAPI struct {
	fakeEvalAPI
	receipt map[string]any
}

func (a *qualificationReceiptAPI) request(path string, payload any) (any, error) {
	a.calls = append(a.calls, []any{path, payload})
	return a.receipt, nil
}

func TestPublishBaselineQualificationRequiresDedicatedReceipt(t *testing.T) {
	api := &qualificationReceiptAPI{receipt: map[string]any{}}
	prepared := map[string]any{"qualify_baseline": true}
	result := map[string]any{"passed": false}
	if _, err := publish(api, prepared, result); code(err) != "baseline_qualification_receipt_missing" {
		t.Fatalf("missing receipt: %v", err)
	}
	api.receipt["baseline_qualification"] = map[string]any{"collection": "evaluations", "id": "qualification", "revision": 1}
	if got, err := publish(api, prepared, result); err != nil || !same(got, api.receipt) {
		t.Fatalf("qualified response: %v, %v", got, err)
	}
	plain := &fakeEvalAPI{}
	if _, err := publish(plain, map[string]any{}, result); err != nil {
		t.Fatal(err)
	}
	first, second := obj(list(api.calls[0])[1]), obj(list(api.calls[1])[1])
	if first["qualify_baseline"] != true || !same(first, second) || first["event_id"] == obj(list(plain.calls[0])[1])["event_id"] {
		t.Fatal("qualification needs a stable event ID distinct from ordinary publication")
	}
}

func TestEvaluateRejectsReplayedQualificationAndTrial(t *testing.T) {
	for _, flags := range [][]string{
		{"--qualify-baseline", "--publish"},
		{"--qualify-baseline", "--execute"},
		{"--qualify-baseline", "--execute", "--publish", "--resume-result", "result"},
		{"--trial-id", "trial", "--publish", "--resume-result", "result"},
	} {
		a, err := parseArgs(append([]string{"--proposal", "proposal", "--token-file", "token"}, flags...),
			[]string{"proposal", "token-file", "trial-id", "resume-result"}, []string{"qualify-baseline", "execute", "publish"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if checkEvaluateArgs(a) == nil {
			t.Fatalf("accepted incompatible arguments %v", flags)
		}
	}
}
