package crew

import "testing"

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
