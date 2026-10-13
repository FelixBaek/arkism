package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
)

// UsageLearning reports what the hub derived from a failed skill.usage trace.
type UsageLearning struct {
	Evaluation Ref             `json:"evaluation"`
	Learning   *LearningResult `json:"learning,omitempty"` // absent when this run was already learned
}

// reflectFailedUsage turns a stored trace with a failed skill.usage event into
// a deterministic automated_check evaluation and a lesson candidate, under the
// hub's own service principal (ReflectionToken): the evaluator is never the
// run writer, and runtimes only record usage. Learn applies the existing
// distinct-run rule, so one failure stays one candidate. Replays reuse the
// evaluation and leave learning unchanged.
func (s *Service) reflectFailedUsage(ctx context.Context, in TraceIngestInput, stored TraceIngestResult) (*UsageLearning, error) {
	var failed *SkillUsage
	for _, e := range in.Events {
		var u SkillUsage
		if e.Kind == skillUsageEventKind && json.Unmarshal(e.Payload, &u) == nil && (u.Status == "failure" || u.Status == "validation_failed") {
			failed = &u
			break
		}
	}
	if failed == nil || s.ReflectionToken == "" {
		return nil, nil
	}
	token := s.ReflectionToken
	ctx = ContextWithPostgresToken(ctx, token)
	var evaluation *Record
	rows, err := s.Repo.EvaluationsFor(ctx, in.Scope, stored.Run)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Evaluation != nil && rows[i].Evaluation.EvalType == "automated_check" && live(rows[i]) {
			evaluation = &rows[i]
			break
		}
	}
	ref := failed.SkillID + "@" + strconv.Itoa(failed.Version)
	code := failed.ErrorCode
	if code == "" {
		code = "unspecified"
	}
	if evaluation == nil {
		spans := make([]SourceRef, 0, len(stored.SourceEvents))
		for _, src := range stored.SourceEvents {
			record, err := s.Get(ctx, token, "sources", in.Scope, src.ID)
			if err != nil {
				return nil, err
			}
			spans = append(spans, sourceReference(record, "execution_trace:"+stored.Trace.ID))
		}
		passed := false
		summary := "skill.usage " + ref + " reported " + failed.Status + " (" + code + ")"
		created, err := s.Create(ctx, token, "evaluations", CreateInput{Scope: in.Scope, Kind: "trace_evaluation", Text: summary, Summary: summary,
			Sources: spans, Relations: []Ref{stored.Trace}, Target: &stored.Run, Sensitivity: "internal",
			Evaluation: &Evaluation{EvalType: "automated_check", Passed: &passed}})
		if err != nil {
			return nil, err
		}
		evaluation = &created
	}
	out := &UsageLearning{Evaluation: Ref{Collection: "evaluations", ID: evaluation.ID, Revision: evaluation.Revision}}
	learned, err := s.Learn(ctx, token, LearningInput{Scope: in.Scope, Run: stored.Run, Evaluation: out.Evaluation, TaskType: "skill:" + failed.SkillID,
		Lesson: "Skill " + ref + " ended " + failed.Status + " with " + code + "; review its instructions for this failure.", FailureReason: code,
		VerificationMethod: "skill.usage status reported by the runtime (deterministic)"})
	if errors.Is(err, ErrConflict) {
		return out, nil // this run's lesson already exists
	}
	if err != nil {
		return nil, err
	}
	out.Learning = &learned
	return out, nil
}
