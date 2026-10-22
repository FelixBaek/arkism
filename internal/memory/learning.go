package memory

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
)

// LearningInput turns one independently evaluated run into reviewable records.
// Lesson text is caller supplied; this method does not claim to generate it.
type LearningInput struct {
	Scope              Scope  `json:"scope"`
	Run                Ref    `json:"run"`
	Evaluation         Ref    `json:"evaluation"`
	TaskType           string `json:"task_type"`
	Lesson             string `json:"lesson"`
	FailureReason      string `json:"failure_reason,omitempty"`
	VerificationMethod string `json:"verification_method"`
}

type LearningResult struct {
	Reflection      Ref  `json:"reflection"`
	Lesson          Ref  `json:"lesson"`
	CompatibleRuns  int  `json:"compatible_runs"`
	ReviewSuggested bool `json:"review_suggested"`
}

type learningCandidate struct {
	row        Record
	run        Ref
	evaluation Ref
}

type agentRunTraceStore interface {
	TraceForAgentRun(context.Context, Scope, string) (string, error)
}

func (s *Service) Learn(ctx context.Context, token string, in LearningInput) (LearningResult, error) {
	if !validLearningInput(in) {
		return LearningResult{}, ErrInvalid
	}
	if s.Repo == nil {
		return LearningResult{}, ErrUnavailable
	}

	actor, ctx, err := s.authorizeLearning(ctx, token, in)
	if err != nil {
		return LearningResult{}, err
	}

	run, evaluation, err := learningEvidence(ctx, s.Repo, in.Run, in.Evaluation, in.Scope)
	if err != nil {
		return LearningResult{}, err
	}
	sources, sensitivity, err := learningSources(ctx, s.Repo, run, evaluation)
	if err != nil {
		return LearningResult{}, err
	}
	traceID, err := s.learningTraceID(ctx, in.Scope, run.ID)
	if err != nil {
		return LearningResult{}, err
	}
	sourceEventID := sources[0].SourceID
	reflectionRelations := learningRelations(in, traceID, run.ID)
	reflection := learningReflection(in, run, evaluation)
	generator := &Generator{Model: "arkism-local", PromptVersion: "experience-v1", PipelineVersion: "experience-v1"}
	reflectionID := ID()
	reflectionRef := Ref{Collection: colReflections, ID: reflectionID, Revision: 1}
	reflectionInput, reflectionInspection, err := inspectCreateInput(CreateInput{
		Scope: in.Scope, Kind: "reflection", Text: structuredText(reflection), Summary: in.Lesson,
		Keywords: []string{in.TaskType, run.Run.Outcome}, Sources: sources, Relations: reflectionRelations,
		Sensitivity: sensitivity, Generated: true, Generator: generator, Reflection: &reflection,
		SourceEventID: sourceEventID, TraceID: traceID,
	}, false)
	if err != nil {
		return LearningResult{}, err
	}
	if reflectionInput.Reflection == nil || !safeTransfer(structuredText(reflectionInput.Reflection)) {
		return LearningResult{}, ErrInvalid
	}
	lessonID := ContentHash(structuredText(struct {
		Scope    Scope
		Run      Ref
		TaskType string
		Lesson   string
	}{in.Scope, in.Run, in.TaskType, in.Lesson}))
	now := time.Now().UTC().Truncate(time.Millisecond)
	lessonInput, lessonInspection, err := inspectCreateInput(CreateInput{
		Scope: in.Scope, Kind: "lesson", Text: in.Lesson, Summary: in.Lesson, Keywords: []string{in.TaskType, run.Run.Outcome},
		Tags: []string{learningTag(in.TaskType)}, Sources: sources, Relations: append(append([]Ref{}, reflectionRelations...), reflectionRef),
		Sensitivity: reflectionInput.Sensitivity, Generated: true, Generator: generator, EffectiveAt: &now,
		SourceEventID: sourceEventID, TraceID: traceID,
	}, false)
	if err != nil {
		return LearningResult{}, err
	}
	if !validTags(lessonInput.Tags) || !validGenerator(lessonInput.Generator) {
		return LearningResult{}, ErrInvalid
	}

	raw, _ := json.Marshal(reflectionInput)
	reflectionDecision, err := s.authorize(ctx, token, "write:reflections", in.Scope, "", string(raw))
	if err != nil {
		return LearningResult{}, err
	}
	raw, _ = json.Marshal(struct {
		Candidate  CreateInput `json:"candidate"`
		Reflection *Reflection `json:"reflection"`
	}{lessonInput, reflectionInput.Reflection})
	if _, err = s.authorize(ctx, token, "write:memory_candidates", in.Scope, "", string(raw)); err != nil {
		return LearningResult{}, err
	}

	prior, err := s.priorLearningCandidates(ctx, token, in, lessonInput)
	if err != nil {
		return LearningResult{}, err
	}

	reflection = *reflectionInput.Reflection
	reflection.SecretChecked = reflectionDecision.ContentSafe && reflectionDecision.ExternalTransfer
	var out LearningResult
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		currentRun, currentEvaluation, err := learningEvidence(ctx, tx, in.Run, in.Evaluation, in.Scope)
		if err != nil {
			return err
		}
		currentSources, currentSensitivity, err := learningSources(ctx, tx, currentRun, currentEvaluation)
		if err != nil {
			return err
		}
		if currentSensitivity != reflectionInput.Sensitivity || structuredText(currentSources) != structuredText(reflectionInput.Sources) {
			return ErrConflict
		}

		distinctRuns := map[string]bool{}
		for _, candidate := range prior {
			valid, err := validLearningCandidate(ctx, tx, candidate, in.Scope, in.TaskType, lessonInput.Text)
			if err != nil {
				return err
			}
			if valid {
				distinctRuns[candidate.run.ID] = true
			}
		}
		if distinctRuns[in.Run.ID] {
			return ErrConflict
		}
		distinctRuns[in.Run.ID] = true

		reflectionRecord := Record{ID: reflectionID, Scope: in.Scope, Kind: "reflection", Status: "active", Revision: 1, Text: reflectionInput.Text, Summary: reflectionInput.Summary, Keywords: reflectionInput.Keywords, Sources: reflectionInput.Sources, Relations: reflectionInput.Relations, SourceEventID: sourceEventID, TraceID: traceID, Sensitivity: reflectionInput.Sensitivity, Inspection: &reflectionInspection, CreatedAt: now, UpdatedAt: now, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role, Trust: 0.5, Generator: generator, Reflection: &reflection}
		if err = checkSources(ctx, tx, reflectionRecord); err != nil {
			return err
		}
		if err = checkRelations(ctx, tx, reflectionRecord); err != nil {
			return err
		}
		if err = insertSourceRecord(ctx, tx, colReflections, reflectionRecord); err != nil {
			return err
		}
		sourceRows, err := sourceMetadata(ctx, tx, in.Scope, lessonInput.Sources)
		if err != nil {
			return err
		}
		lessonRecord := Record{ID: lessonID, Scope: in.Scope, Kind: "lesson", Status: "candidate", VerificationStatus: VerificationCandidate, Revision: 1, Text: lessonInput.Text, Summary: lessonInput.Summary, Keywords: lessonInput.Keywords, Tags: lessonInput.Tags, Sources: lessonInput.Sources, Relations: lessonInput.Relations, SourceEventID: sourceEventID, TraceID: traceID, Sensitivity: lessonInput.Sensitivity, Inspection: &lessonInspection, EffectiveAt: lessonInput.EffectiveAt, CreatedAt: now, UpdatedAt: now, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role, Trust: 0.5, TrustLevel: candidateTrustLevel(Record{Sources: lessonInput.Sources}, sourceRows), Generator: generator}
		if err = memoryProvenance(ctx, tx, lessonRecord); err != nil {
			return err
		}
		lessonRecord.NormalizedKey = candidateNormalizedKey(lessonRecord)
		if err = checkRelations(ctx, tx, lessonRecord); err != nil {
			return err
		}
		if err = reserveCandidate(ctx, tx, in.Scope, now, s.CandidateDailyLimit); err != nil {
			return err
		}
		if err = insertSourceRecord(ctx, tx, "memory_candidates", lessonRecord); err != nil {
			return err
		}
		if err = audit(ctx, tx, in.Scope, actor.PrincipalID, "create:reflections", reflectionRecord.ID); err != nil {
			return err
		}
		if err = audit(ctx, tx, in.Scope, actor.PrincipalID, "create:memory_candidates", lessonRecord.ID); err != nil {
			return err
		}
		out = LearningResult{Reflection: reflectionRef, Lesson: Ref{Collection: "memory_candidates", ID: lessonRecord.ID, Revision: 1}, CompatibleRuns: len(distinctRuns), ReviewSuggested: len(distinctRuns) >= 3}
		return nil
	})
	if err != nil {
		return LearningResult{}, err
	}
	return out, nil
}

func learningRelations(in LearningInput, traceID, runID string) []Ref {
	relations := []Ref{in.Run, in.Evaluation}
	if traceID != "" && traceID != runID {
		relations = append(relations, Ref{Collection: "execution_traces", ID: traceID, Revision: 1})
	}
	return relations
}

func learningReflection(in LearningInput, run, evaluation Record) Reflection {
	reflection := Reflection{TaskType: in.TaskType, Outcome: run.Run.Outcome, FailureReason: in.FailureReason, Lesson: in.Lesson, VerificationMethod: in.VerificationMethod, RunID: run.ID, EvaluationID: evaluation.ID}
	if evaluation.Evaluation.Score != nil {
		reflection.EvaluationScore = *evaluation.Evaluation.Score / *evaluation.Evaluation.ScoreMax
	} else if *evaluation.Evaluation.Passed {
		reflection.EvaluationScore = 1
	}
	return reflection
}

func validLearningInput(in LearningInput) bool {
	return validScope(in.Scope) && in.Run.Collection == "agent_runs" && in.Evaluation.Collection == "evaluations" &&
		validRunLabel(in.Run.ID, 256) && validRunLabel(in.Evaluation.ID, 256) && in.Run.Revision >= 1 && in.Evaluation.Revision >= 1 &&
		validRunLabel(in.TaskType, 128) && validLearningText(in.Lesson, 64000, true) && validLearningText(in.VerificationMethod, 2000, true) &&
		validLearningText(in.FailureReason, 2000, false) && !containsSecret(structuredText(in.Scope))
}

func (s *Service) authorizeLearning(ctx context.Context, token string, in LearningInput) (Decision, context.Context, error) {
	actor, err := s.authorize(ctx, token, "read:agent_runs", in.Scope, in.Run.ID, "")
	if err != nil {
		return actor, ctx, err
	}
	if actor.Role != "agent" && actor.Role != "service" && actor.Role != "pipeline" {
		return actor, ctx, ErrDenied
	}
	ctx = bindRequestActor(ctx, actor)
	if _, err = s.authorize(ctx, token, "read:evaluations", in.Scope, in.Evaluation.ID, ""); err != nil {
		return actor, ctx, err
	}
	if _, err = s.authorize(ctx, token, "read:memory_candidates", in.Scope, "", ""); err != nil {
		return actor, ctx, err
	}
	return actor, ctx, nil
}

func (s *Service) learningTraceID(ctx context.Context, scope Scope, runID string) (string, error) {
	traces, ok := s.Repo.(agentRunTraceStore)
	if !ok {
		return runID, nil
	}
	traceID, err := traces.TraceForAgentRun(ctx, scope, runID)
	if errors.Is(err, ErrNotFound) {
		// Legacy runs predate canonical execution traces; their validated
		// source event remains the evidence until they are re-ingested.
		traceID, err = "", nil
	}
	return traceID, err
}

func (s *Service) priorLearningCandidates(ctx context.Context, token string, in LearningInput, lessonInput CreateInput) ([]learningCandidate, error) {
	rows, err := s.Repo.ListCandidates(ctx, ReviewInput{Scope: in.Scope, Status: "candidate", Kind: "lesson", Limit: 100})
	if err != nil {
		return nil, err
	}
	if len(rows) > 100 {
		return nil, ErrUnavailable
	}
	prior := make([]learningCandidate, 0, len(rows))
	for _, row := range rows {
		if _, err = s.authorize(ctx, token, "read:memory_candidates", in.Scope, row.ID, ""); err != nil {
			return nil, err
		}
		if row.Scope != in.Scope || row.Kind != "lesson" || row.Status != "candidate" || row.Summary != lessonInput.Summary || row.Text != lessonInput.Text || !slices.Contains(row.Tags, learningTag(in.TaskType)) {
			continue
		}
		runRef, evalRef, ok := learningRelationRefs(row.Relations)
		if !ok {
			continue
		}
		if _, err = s.authorize(ctx, token, "read:agent_runs", in.Scope, runRef.ID, ""); err != nil {
			return nil, err
		}
		if _, err = s.authorize(ctx, token, "read:evaluations", in.Scope, evalRef.ID, ""); err != nil {
			return nil, err
		}
		prior = append(prior, learningCandidate{row: row, run: runRef, evaluation: evalRef})
	}
	return prior, nil
}

func learningEvidence(ctx context.Context, tx Tx, runRef, evaluationRef Ref, scope Scope) (Record, Record, error) {
	run, err := tx.Get(ctx, "agent_runs", scope, runRef.ID)
	if err != nil {
		return Record{}, Record{}, err
	}
	if !validLearningRun(run, runRef, scope) {
		return Record{}, Record{}, ErrConflict
	}
	if err = checkSources(ctx, tx, run); err != nil {
		return Record{}, Record{}, err
	}
	evaluation, err := tx.Get(ctx, "evaluations", scope, evaluationRef.ID)
	if err != nil {
		return Record{}, Record{}, err
	}
	if !validLearningEvaluation(evaluation, evaluationRef, runRef, run, scope) {
		return Record{}, Record{}, ErrDenied
	}
	if err = checkEvaluationEvidence(ctx, tx, evaluation); err != nil {
		return Record{}, Record{}, err
	}
	return run, evaluation, nil
}

func validLearningRun(run Record, runRef Ref, scope Scope) bool {
	return run.Scope == scope && run.Revision == runRef.Revision && live(run) && run.Run != nil && run.Run.FinishedAt != nil && terminalOutcome(run.Run.Outcome) && validRun(run.Run, run.Scope, time.Now())
}

func validLearningEvaluation(evaluation Record, evaluationRef, runRef Ref, run Record, scope Scope) bool {
	return evaluation.Scope == scope && evaluation.Revision == evaluationRef.Revision && live(evaluation) && evaluation.Target != nil && *evaluation.Target == runRef && evaluation.Evaluation != nil && validEvaluation(evaluation.Evaluation) && evaluation.Evaluation.EvalType != "model_self" && evaluationRole(evaluation.Evaluation.EvalType, evaluation.CreatedByType) && evaluation.Evaluation.Passed != nil && evaluation.CreatedBy != run.CreatedBy
}

func learningSources(ctx context.Context, tx Tx, run, evaluation Record) ([]SourceRef, string, error) {
	sources := append(append([]SourceRef{}, run.Sources...), evaluation.Sources...)
	unique := sources[:0]
	seen := map[string]bool{}
	for _, source := range sources {
		if source.Span == nil {
			return nil, "", ErrInvalid
		}
		key := structuredText(source)
		if !seen[key] {
			seen[key] = true
			unique = append(unique, source)
		}
	}
	if len(unique) == 0 || len(unique) > 64 {
		return nil, "", ErrInvalid
	}
	rows, err := sourceMetadata(ctx, tx, run.Scope, unique)
	if err != nil {
		return nil, "", err
	}
	sensitivity := "internal"
	for _, value := range append([]Record{run, evaluation}, rows...) {
		if value.Sensitivity == "restricted" {
			sensitivity = "restricted"
		} else if sensitivity != "restricted" && (value.Sensitivity == "private" || value.Sensitivity == "" || !validSensitivity(value.Sensitivity)) {
			sensitivity = "private"
		}
	}
	return unique, sensitivity, nil
}

func learningRelationRefs(relations []Ref) (Ref, Ref, bool) {
	var run, evaluation Ref
	for _, ref := range relations {
		switch ref.Collection {
		case "agent_runs":
			if run.ID != "" {
				return Ref{}, Ref{}, false
			}
			run = ref
		case "evaluations":
			if evaluation.ID != "" {
				return Ref{}, Ref{}, false
			}
			evaluation = ref
		}
	}
	return run, evaluation, run.ID != "" && evaluation.ID != ""
}

func validLearningCandidate(ctx context.Context, tx Tx, candidate learningCandidate, scope Scope, taskType, lesson string) (bool, error) {
	row, err := tx.Get(ctx, "memory_candidates", scope, candidate.row.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
			return false, nil
		}
		return false, err
	}
	now := time.Now()
	if !currentLearningCandidate(row, candidate, taskType, lesson, now) {
		return false, nil
	}
	runRef, evaluationRef, ok := learningRelationRefs(row.Relations)
	if !ok || runRef != candidate.run || evaluationRef != candidate.evaluation {
		return false, nil
	}
	if err = checkSources(ctx, tx, row); err != nil {
		return learningCandidateErr(err)
	}
	if err = checkRelations(ctx, tx, row); err != nil {
		return learningCandidateErr(err)
	}
	run, evaluation, err := learningEvidence(ctx, tx, runRef, evaluationRef, scope)
	if err != nil {
		return learningCandidateErr(err)
	}
	sources, sensitivity, err := learningSources(ctx, tx, run, evaluation)
	if err != nil {
		return learningCandidateErr(err)
	}
	if row.Sensitivity != sensitivity || structuredText(row.Sources) != structuredText(sources) {
		return false, nil
	}
	return true, nil
}

func currentLearningCandidate(row Record, candidate learningCandidate, taskType, lesson string, now time.Time) bool {
	return row.Revision == candidate.row.Revision && row.Status == "candidate" && (row.EffectiveAt == nil || !row.EffectiveAt.After(now)) && (row.ExpiresAt == nil || row.ExpiresAt.After(now)) && row.Kind == "lesson" && row.Text == lesson && row.Summary == lesson && slices.Contains(row.Tags, learningTag(taskType))
}

// Only an unavailable store fails learning; other errors just disqualify the candidate.
func learningCandidateErr(err error) (bool, error) {
	if errors.Is(err, ErrUnavailable) {
		return false, err
	}
	return false, nil
}

func learningTag(taskType string) string { return "learning:" + ContentHash(taskType) }

func validLearningText(value string, maxN int, required bool) bool {
	if len(value) > maxN || strings.TrimSpace(value) != value || required && value == "" || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	return true
}
