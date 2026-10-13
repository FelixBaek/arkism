package memory

import (
	"context"
	"slices"
)

const proposedSkillKind = "lesson_skill_proposal"
const proposedSkillSourceKind = "lesson_skill_source"

type ProposeSkillInput struct {
	Skill     ImportSkillInput `json:"skill"`
	Lessons   []Ref            `json:"lessons"`
	Generator *Generator       `json:"generator"`
	// Internal completion receipt, never supplied through the public API.
	receipt           *Record
	generatedBaseline *Ref
}

// Generated text derives from the baseline as well as the lessons. Carry its
// source lineage into both generated records so deletion reaches the copy.
func proposalEvidence(ctx context.Context, tx Tx, scope Scope, proposal *ProposeSkillInput) (skillLessonEvidence, error) {
	out, err := skillLessons(ctx, tx, scope, proposal.Lessons)
	if err != nil || proposal.generatedBaseline == nil {
		return out, err
	}
	ref := *proposal.generatedBaseline
	baseline, err := tx.Get(ctx, colSkillVersions, scope, ref.ID)
	if err != nil {
		return out, err
	}
	if ref.Collection != colSkillVersions || baseline.Revision != ref.Revision || baseline.SkillID != proposal.Skill.SkillID || !validAnalysis(baseline.Analysis) {
		return out, ErrConflict
	}
	if err = checkActiveSkill(ctx, tx, baseline); err != nil {
		return out, err
	}
	out.Basis = &SkillBasis{Version: ref, ContentSHA256: baseline.Analysis.ContentHash}
	if baseline.ValidationID != "" {
		evaluation, e := tx.Get(ctx, "evaluations", scope, baseline.ValidationID)
		if e != nil {
			return out, e
		}
		out.Basis.Validation = &Ref{Collection: "evaluations", ID: evaluation.ID, Revision: evaluation.Revision}
	}
	for _, source := range baseline.Sources {
		found := false
		for _, old := range out.Sources {
			if structuredText(old) == structuredText(source) {
				found = true
				break
			}
		}
		if !found {
			out.Sources = append(out.Sources, source)
		}
	}
	if len(out.Sources) > 63 {
		return out, ErrInvalid
	}
	if baseline.Sensitivity == "restricted" || baseline.Sensitivity == "private" && out.Sensitivity != "restricted" {
		out.Sensitivity = baseline.Sensitivity
	}
	return out, nil
}

type skillLessonEvidence struct {
	Basis       *SkillBasis
	Relations   []Ref
	Sources     []SourceRef
	Sensitivity string
}

// Lesson candidates remain evidence, never an approval of the proposed skill.
func skillLessons(ctx context.Context, tx Tx, scope Scope, lessons []Ref) (skillLessonEvidence, error) {
	out := skillLessonEvidence{Sensitivity: "internal"}
	if len(lessons) < improvementMinRuns || len(lessons) > 16 {
		return out, ErrInvalid
	}
	runs := map[string]bool{}
	taskType, text := "", ""
	for _, ref := range lessons {
		if ref.Collection != "memory_candidates" || !validRunLabel(ref.ID, 256) || ref.Revision < 1 {
			return out, ErrInvalid
		}
		row, err := tx.Get(ctx, ref.Collection, scope, ref.ID)
		if err != nil {
			return out, err
		}
		if row.Scope != scope || row.Revision != ref.Revision || row.PrivacyQuarantined || row.ContentErased || row.ConflictID != "" {
			return out, ErrConflict
		}
		run, evaluation, ok := learningRelationRefs(row.Relations)
		if !ok || runs[run.ID] {
			return out, ErrInvalid
		}
		runs[run.ID] = true
		var reflection *Record
		for _, rel := range row.Relations {
			if rel.Collection != colReflections {
				continue
			}
			if reflection != nil {
				return out, ErrInvalid
			}
			r, e := tx.Get(ctx, rel.Collection, scope, rel.ID)
			if e != nil {
				return out, e
			}
			if r.Reflection == nil || r.Scope != scope || r.Revision != rel.Revision || !live(r) || r.Reflection.RunID != run.ID || r.Reflection.EvaluationID != evaluation.ID || r.Reflection.Lesson != row.Text {
				return out, ErrConflict
			}
			reflection = &r
		}
		if reflection == nil {
			return out, ErrInvalid
		}
		if taskType == "" {
			taskType, text = reflection.Reflection.TaskType, row.Text
		}
		if reflection.Reflection.TaskType != taskType || row.Text != text {
			return out, ErrInvalid
		}
		valid, err := validLearningCandidate(ctx, tx, learningCandidate{row: row, run: run, evaluation: evaluation}, scope, taskType, text)
		if err != nil {
			return out, err
		}
		if !valid {
			return out, ErrConflict
		}
		for _, rel := range append([]Ref{ref}, row.Relations...) {
			if !slices.Contains(out.Relations, rel) {
				out.Relations = append(out.Relations, rel)
			}
		}
		for _, src := range row.Sources {
			exists := false
			for _, old := range out.Sources {
				if structuredText(old) == structuredText(src) {
					exists = true
					break
				}
			}
			if !exists {
				out.Sources = append(out.Sources, src)
			}
		}
		if row.Sensitivity == "restricted" || out.Sensitivity != "restricted" && row.Sensitivity == "private" {
			out.Sensitivity = row.Sensitivity
		}
	}
	// Reserve one source for the proposed SKILL.md itself.
	if len(out.Sources) > 63 {
		return out, ErrInvalid
	}
	return out, nil
}

func proposedLessons(r Record) []Ref {
	var refs []Ref
	for _, ref := range r.Relations {
		if ref.Collection == "memory_candidates" {
			refs = append(refs, ref)
		}
	}
	return refs
}

func (s *Service) ProposeSkill(ctx context.Context, token string, in ProposeSkillInput) (ImportSkillResult, error) {
	if in.Skill.ComparisonPlan == nil || !validGenerator(in.Generator) || !validScope(in.Skill.Scope) || len(in.Lessons) < improvementMinRuns || len(in.Lessons) > 16 {
		return ImportSkillResult{}, ErrInvalid
	}
	in.Lessons = slices.Clone(in.Lessons)
	slices.SortFunc(in.Lessons, func(a, b Ref) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	generator := *in.Generator
	in.Generator = &generator
	return s.importSkill(ctx, token, in.Skill, &in)
}
