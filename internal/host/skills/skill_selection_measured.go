package skills

import (
	"encoding/json"
	"regexp"
	"sort"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/memory"
)

var measuredEvaluationID = regexp.MustCompile("^comparison-[a-f0-9]{64}-evaluation$")

func decodeSelectionRecord(raw map[string]any) (memory.Record, bool) {
	var r memory.Record
	data, err := json.Marshal(raw)
	if err != nil {
		return r, false
	}
	err = json.Unmarshal(data, &r)
	return r, err == nil
}

func selectionEvidenceRecord(collection, id string) (memory.Record, bool) {
	raw, err := reviewCall("GET", "/v1/"+collection+"/"+id+reviewQuery(), nil, "")
	if err != nil {
		return memory.Record{}, false
	}
	r, ok := decodeSelectionRecord(raw)
	return r, ok && r.ID == id
}

func selectedMeasurements(left, right map[string]any) map[string]any {

	if left["id"] == right["id"] {
		return unknownMeasurement("same_version")
	}
	if asMap(left["comparison_plan"])["baseline_version"] != right["id"] {
		left, right = right, left
	}
	if asMap(left["comparison_plan"])["baseline_version"] != right["id"] {
		return unknownMeasurement("no_registered_pair")
	}
	candidate, ok := decodeSelectionRecord(left)
	base, okBase := decodeSelectionRecord(right)
	scopeMap := host.Scope()
	scope := memory.Scope{WorkspaceID: str(scopeMap["workspace_id"]), ProjectID: str(scopeMap["project_id"]), UserID: str(scopeMap["user_id"]), AgentID: str(scopeMap["agent_id"])}
	if !ok || !okBase || candidate.Scope != scope || base.Scope != scope {
		return unknownMeasurement("unverified_version_records")
	}
	ids, haveSummary := discoverSelectionEvaluations(scopeMap, candidate)
	if len(ids) == 0 {
		if !haveSummary {
			return unknownMeasurement("evidence_unavailable")
		}
		return unknownMeasurement("no_linked_measurement")
	}
	if len(ids) > 32 {
		return unknownMeasurement("too_many_measurements")
	}
	var keys []string
	for id := range ids {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	var verified []map[string]any
	for _, id := range keys {
		row, reason := readSelectionMeasurement(id, str(left["comparison_plan_sha256"]), scope, base, candidate)
		if reason != "" {
			return unknownMeasurement(reason)
		}
		verified = append(verified, row)
	}
	return map[string]any{"status": "verified_recorded_measurements", "records": verified, "current_revision_summary_available": haveSummary}
}

func discoverSelectionEvaluations(scopeMap map[string]any, candidate memory.Record) (map[string]bool, bool) {
	ids := map[string]bool{}
	// Approval can retain a measured result from an earlier record revision.
	// Its target is checked independently below and the output labels that revision.
	if measuredEvaluationID.MatchString(candidate.ValidationID) {
		ids[candidate.ValidationID] = true
	}
	target := map[string]any{"collection": "skill_versions", "id": candidate.ID, "revision": candidate.Revision}
	summary, err := reviewCall("POST", "/v1/evaluations/summary", map[string]any{"scope": scopeMap, "target": target}, "")
	haveSummary := err == nil && summary["authority"] == "data_only_untrusted" && sameJSON(target, summary["target"])
	if haveSummary {
		for _, x := range asList(summary["evaluation_refs"]) {
			ref := asMap(x)
			if ref["collection"] == "evaluations" && measuredEvaluationID.MatchString(str(ref["id"])) {
				ids[str(ref["id"])] = true
			}
		}
	}
	return ids, haveSummary
}

func unknownMeasurement(reason string) map[string]any {
	return map[string]any{"status": "undetermined", "reason": reason}
}
func readSelectionMeasurement(id, planHash string, scope memory.Scope, base, candidate memory.Record) (map[string]any, string) {
	e, ok := selectionEvidenceRecord("evaluations", id)
	if !ok || len(e.Sources) == 0 || len(e.Relations) != 2 || e.Target == nil {
		return nil, "evidence_unavailable_or_inconsistent"
	}
	if id != candidate.ValidationID && e.Target.Revision != candidate.Revision {
		return nil, "evidence_revision_mismatch"
	}
	prefix := id[:len(id)-len("-evaluation")]
	if e.Sources[0].SourceID != prefix+"-source" {
		return nil, "evidence_source_mismatch"
	}
	s, ok := selectionEvidenceRecord("sources", e.Sources[0].SourceID)
	if !ok {
		return nil, "evidence_unavailable_or_inconsistent"
	}
	var runs [2]memory.Record
	for i, variant := range []string{"baseline", "candidate"} {
		ref := e.Relations[i]
		if ref.Collection != "agent_runs" || ref.ID != prefix+"-"+variant {
			return nil, "evidence_run_mismatch"
		}
		runs[i], ok = selectionEvidenceRecord("agent_runs", ref.ID)
		if !ok {
			return nil, "evidence_unavailable_or_inconsistent"
		}
	}
	measured, valid := memory.InspectRecordedSkillComparison(scope, base, candidate, e, s, runs)
	if !valid || measured.Report.PlanSHA256 != planHash {
		return nil, "evidence_unavailable_or_inconsistent"
	}
	return map[string]any{"evaluation": map[string]any{"collection": "evaluations", "id": id, "revision": e.Revision}, "baseline_version_id": base.ID, "candidate_version_id": candidate.ID, "evaluated_baseline_revision": measured.BaselineRevision, "evaluated_candidate_revision": measured.Candidate.Revision, "current_revisions_match": base.Revision == measured.BaselineRevision && candidate.Revision == measured.Candidate.Revision, "dataset_sha256": measured.DatasetSHA256, "environment_sha256": measured.EnvironmentSHA256, "plan_sha256": measured.Report.PlanSHA256, "passed": measured.Passed, "baseline_score": measured.Report.BaselineScore, "candidate_score": measured.Report.CandidateScore, "paired_cases": len(measured.Cases) / 2, "companion_coverage": measured.Report.CompanionCoverage}, ""
}
