package memory

import "slices"

// Shared evaluation records: what was asked (eval_tasks), one batch of attempts
// (eval_runs), one attempt at one task inside a run (eval_trials), and how far
// a model judge agrees with human labels (judge_calibrations). They are plain
// memory_records written through POST /v1/{collection}; evaluations point at
// them with spec_ref, trial_ref and calibration_ref.
const (
	colEvalTasks         = "eval_tasks"
	colEvalRuns          = "eval_runs"
	colEvalTrials        = "eval_trials"
	colJudgeCalibrations = "judge_calibrations"
)

var evalRecordCollections = []string{colEvalTasks, colEvalRuns, colEvalTrials, colJudgeCalibrations}

func isEvalRecordCollection(col string) bool { return slices.Contains(evalRecordCollections, col) }

// A trial names the task it attempted and the run it belongs to.
func validEvalRecord(col string, r Record) bool {
	if col != colEvalTrials {
		return true
	}
	return slices.ContainsFunc(r.Relations, func(x Ref) bool { return x.Collection == colEvalTasks }) &&
		slices.ContainsFunc(r.Relations, func(x Ref) bool { return x.Collection == colEvalRuns })
}

func validEvaluationRef(ref *Ref, col string) bool {
	return ref == nil || ref.Collection == col && validRunLabel(ref.ID, 256) && ref.Revision >= 1
}

// evaluationRefs are the eval records an evaluation points at, checked like relations.
func evaluationRefs(x *Evaluation) []Ref {
	var out []Ref
	if x == nil {
		return out
	}
	for _, ref := range []*Ref{x.SpecRef, x.TrialRef, x.CalibrationRef} {
		if ref != nil {
			out = append(out, *ref)
		}
	}
	return out
}
