package memory

import (
	"context"
	"math"
	"reflect"
)

type Evaluation struct {
	ComparisonReport *SkillComparisonReport `json:"comparison_report,omitempty" bson:"comparison_report,omitempty"`
	EvalType         string                 `json:"eval_type" bson:"eval_type"`
	Passed           *bool                  `json:"passed,omitempty" bson:"passed,omitempty"`
	Score            *float64               `json:"score,omitempty" bson:"score,omitempty"`
	ScoreMax         *float64               `json:"score_max,omitempty" bson:"score_max,omitempty"`
	SpecRef          *Ref                   `json:"spec_ref,omitempty" bson:"spec_ref,omitempty"`
	TrialRef         *Ref                   `json:"trial_ref,omitempty" bson:"trial_ref,omitempty"`
	CalibrationRef   *Ref                   `json:"calibration_ref,omitempty" bson:"calibration_ref,omitempty"`
}

func validEvaluation(x *Evaluation) bool {
	if x == nil || !validComparisonReport(x.ComparisonReport) || x.EvalType != "user_rating" && x.EvalType != "automated_check" && x.EvalType != "model_self" && x.EvalType != "llm_judge" || (x.Score == nil) != (x.ScoreMax == nil) {
		return false
	}
	// A model judge is only usable with the calibration it was measured against.
	if (x.EvalType == "llm_judge") != (x.CalibrationRef != nil) || !validEvaluationRef(x.SpecRef, colEvalTasks) || !validEvaluationRef(x.TrialRef, colEvalTrials) || !validEvaluationRef(x.CalibrationRef, colJudgeCalibrations) {
		return false
	}
	if x.Score == nil {
		return true
	}
	return !math.IsNaN(*x.Score) && !math.IsInf(*x.Score, 0) && !math.IsNaN(*x.ScoreMax) && !math.IsInf(*x.ScoreMax, 0) && *x.ScoreMax > 0 && *x.Score >= 0 && *x.Score <= *x.ScoreMax
}
func evaluationRole(kind, role string) bool {
	switch kind {
	case "user_rating":
		return role == "human"
	case "automated_check", "llm_judge":
		return role == "service" || role == "pipeline"
	case "model_self":
		return role == "agent" || role == "service" || role == "pipeline"
	}
	return false
}
func independentEvaluation(r Record) bool {
	if r.Evaluation == nil {
		return r.Passed
	} // Existing independent legacy reviews remain readable/usable.
	// shortcut: llm_judge verdicts are summary data only and never verify skills or reflections; open this after calibration thresholds are decided.
	return validEvaluation(r.Evaluation) && r.Evaluation.EvalType != "model_self" && r.Evaluation.EvalType != "llm_judge" && evaluationRole(r.Evaluation.EvalType, r.CreatedByType) && r.Evaluation.Passed != nil && *r.Evaluation.Passed && r.Passed
}

type EvaluationSummaryInput struct {
	Scope  Scope `json:"scope"`
	Target Ref   `json:"target"`
}
type EvaluationTotals struct {
	Count     int      `json:"count"`
	Passed    int      `json:"passed"`
	Failed    int      `json:"failed"`
	Unrated   int      `json:"unrated"`
	Scored    int      `json:"scored"`
	MeanScore *float64 `json:"mean_score"`
}
type EvaluationSummary struct {
	Tracking       *MemoryEvaluationTracking   `json:"tracking,omitempty"`
	EvaluationRefs []Ref                       `json:"evaluation_refs"`
	Target         Ref                         `json:"target"`
	Groups         map[string]EvaluationTotals `json:"groups"`
	PreferredTypes []string                    `json:"preferred_types"`
	Effective      EvaluationTotals            `json:"effective"`
	Passed         *bool                       `json:"passed"`
	LegacyCount    int                         `json:"legacy_count"`
	Authority      string                      `json:"authority"`
}

const evaluationSummaryLimit = 1000

// Include expiry in the snapshot selection; no client-side sampling of a larger set.

func addEvaluation(total EvaluationTotals, x *Evaluation) EvaluationTotals {
	total.Count++
	if x.Passed == nil {
		total.Unrated++
	} else if *x.Passed {
		total.Passed++
	} else {
		total.Failed++
	}
	if x.Score != nil {
		n := *x.Score / *x.ScoreMax
		if total.MeanScore != nil {
			n = (*total.MeanScore*float64(total.Scored) + n) / float64(total.Scored+1)
		}
		total.Scored++
		total.MeanScore = &n
	}
	return total
}

// A still-live evaluation cannot validate a decision after its direct evidence
// has been deleted, superseded or revoked. Target checks stay with each caller.
func checkEvaluationEvidence(ctx context.Context, tx Tx, r Record) error {
	if e := checkSources(ctx, tx, r); e != nil {
		return e
	}
	if e := checkDirectRelations(ctx, tx, Record{Scope: r.Scope, Relations: evaluationRefs(r.Evaluation)}); e != nil {
		return e
	}
	return checkRelations(ctx, tx, r)
}

func (s *Service) SummarizeEvaluations(ctx context.Context, token string, in EvaluationSummaryInput) (EvaluationSummary, error) {
	if !validEvaluationSummaryInput(in) {
		return EvaluationSummary{}, ErrInvalid
	}
	if s.Repo == nil {
		return EvaluationSummary{}, ErrUnavailable
	}
	actor, e := s.authorize(ctx, token, "read:"+in.Target.Collection, in.Scope, in.Target.ID, "")
	if e != nil {
		return EvaluationSummary{}, e
	}
	d, e := s.authorize(ctx, token, "read:evaluations", in.Scope, "", "")
	if e != nil {
		return EvaluationSummary{}, e
	}
	if d.PrincipalID != actor.PrincipalID || d.Role != actor.Role {
		return EvaluationSummary{}, ErrDenied
	}
	rows, e := s.Repo.EvaluationsFor(ctx, in.Scope, in.Target)
	if e != nil {
		return EvaluationSummary{}, e
	}
	// shortcut: fail closed above 1000 rows; add a stable paged protocol when needed.
	if len(rows) > evaluationSummaryLimit {
		return EvaluationSummary{}, ErrUnavailable
	}
	refs := make([]Ref, 0, len(rows))
	for _, r := range rows {
		if r.Scope != in.Scope || r.Target == nil || *r.Target != in.Target {
			return EvaluationSummary{}, ErrUnavailable
		}
		d, e := s.authorize(ctx, token, "read:evaluations", in.Scope, r.ID, "")
		if e != nil {
			return EvaluationSummary{}, e
		}
		if d.PrincipalID != actor.PrincipalID || d.Role != actor.Role {
			return EvaluationSummary{}, ErrDenied
		}
		refs = append(refs, Ref{Collection: "evaluations", ID: r.ID, Revision: r.Revision})
	}
	var out EvaluationSummary
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		target, e := tx.Get(ctx, in.Target.Collection, in.Scope, in.Target.ID)
		if e != nil {
			return e
		}
		if target.Revision != in.Target.Revision {
			return ErrConflict
		}
		if e = checkSources(ctx, tx, target); e != nil {
			return e
		}
		current, e := tx.EvaluationsFor(ctx, in.Scope, in.Target)
		if e != nil {
			return e
		}
		actual := make([]Ref, 0, len(current))
		for _, r := range current {
			actual = append(actual, Ref{Collection: "evaluations", ID: r.ID, Revision: r.Revision})
		}
		if !reflect.DeepEqual(actual, refs) {
			return ErrConflict
		}
		out = EvaluationSummary{EvaluationRefs: refs, Target: in.Target, Groups: map[string]EvaluationTotals{}, PreferredTypes: []string{}, Authority: AuthorityDataOnly}
		out.Tracking, e = memoryEvaluationTracking(ctx, tx, in.Scope, in.Target)
		if e != nil {
			return e
		}
		if e = groupEvaluations(ctx, tx, in, target, current, &out); e != nil {
			return e
		}
		preferEvaluations(current, &out)
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "read:evaluation_summary", ContentHash(structuredText(in.Target)))
	})
	if e != nil {
		return EvaluationSummary{}, e
	}
	return out, nil
}
func validEvaluationSummaryInput(in EvaluationSummaryInput) bool {
	return validScope(in.Scope) && knownCollection(in.Target.Collection) && in.Target.Collection != "jobs" && in.Target.Collection != "audit_events" && validRunLabel(in.Target.ID, 256) && in.Target.Revision >= 1
}
func groupEvaluations(ctx context.Context, tx Tx, in EvaluationSummaryInput, target Record, current []Record, out *EvaluationSummary) error {
	for _, r := range current {
		if !live(r) || r.Scope != in.Scope || r.Target == nil || *r.Target != in.Target {
			return ErrConflict
		}
		if e := checkEvaluationEvidence(ctx, tx, r); e != nil {
			return e
		}
		if r.Evaluation == nil {
			out.LegacyCount++
			continue
		}
		x := r.Evaluation
		if !validEvaluation(x) || !evaluationRole(x.EvalType, r.CreatedByType) || (x.EvalType == "model_self") != (r.CreatedBy == target.CreatedBy) {
			return ErrUnavailable
		}
		out.Groups[x.EvalType] = addEvaluation(out.Groups[x.EvalType], x)
	}
	return nil
}
func preferEvaluations(current []Record, out *EvaluationSummary) {
	for _, kind := range []string{"user_rating", "automated_check"} {
		if out.Groups[kind].Count > 0 {
			out.PreferredTypes = append(out.PreferredTypes, kind)
		}
	}
	if len(out.PreferredTypes) == 0 && out.Groups["model_self"].Count > 0 {
		out.PreferredTypes = append(out.PreferredTypes, "model_self")
	}
	for _, r := range current {
		if r.Evaluation != nil {
			for _, kind := range out.PreferredTypes {
				if r.Evaluation.EvalType == kind {
					out.Effective = addEvaluation(out.Effective, r.Evaluation)
				}
			}
		}
	}
	if out.Effective.Passed+out.Effective.Failed > 0 {
		passed := out.Effective.Failed == 0
		out.Passed = &passed
	}
}
