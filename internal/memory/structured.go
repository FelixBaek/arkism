package memory

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// Structured summaries deliberately omit raw conversations and tool outputs.
type Reflection struct {
	TaskType           string  `json:"task_type" bson:"task_type"`
	Outcome            string  `json:"outcome" bson:"outcome"`
	FailureReason      string  `json:"failure_reason" bson:"failure_reason"`
	Lesson             string  `json:"lesson" bson:"lesson"`
	VerificationMethod string  `json:"verification_method" bson:"verification_method"`
	EvaluationScore    float64 `json:"evaluation_score" bson:"evaluation_score"`
	Verified           bool    `json:"verified" bson:"verified"`
	RunID              string  `json:"run_id" bson:"run_id"`
	SkillID            string  `json:"skill_id" bson:"skill_id"`
	SkillVersion       string  `json:"skill_version" bson:"skill_version"`
	EvaluationID       string  `json:"evaluation_id" bson:"evaluation_id"`
	SecretChecked      bool    `json:"-" bson:"secret_checked"`
}

type SkillAnalysis struct {
	SkillID             string             `json:"skill_id" bson:"skill_id"`
	Version             string             `json:"version" bson:"version"`
	ContentHash         string             `json:"content_hash" bson:"content_hash"`
	Name                string             `json:"name" bson:"name"`
	Description         string             `json:"description" bson:"description"`
	Problem             string             `json:"problem" bson:"problem"`
	UseWhen             []string           `json:"use_when" bson:"use_when"`
	AvoidWhen           []string           `json:"avoid_when" bson:"avoid_when"`
	InputFormat         string             `json:"input_format" bson:"input_format"`
	OutputFormat        string             `json:"output_format" bson:"output_format"`
	Tools               []string           `json:"tools" bson:"tools"`
	APIs                []string           `json:"apis" bson:"apis"`
	Permissions         []string           `json:"permissions" bson:"permissions"`
	SecretNames         []string           `json:"secret_names" bson:"secret_names"`
	Reads               bool               `json:"reads" bson:"reads"`
	Writes              bool               `json:"writes" bson:"writes"`
	ExternalTransfer    bool               `json:"external_transfer" bson:"external_transfer"`
	IrreversibleActions []string           `json:"irreversible_actions" bson:"irreversible_actions"`
	Agents              []string           `json:"agents" bson:"agents"`
	Environments        []string           `json:"environments" bson:"environments"`
	Dependencies        []string           `json:"dependencies" bson:"dependencies"`
	Conflicts           []string           `json:"conflicts" bson:"conflicts"`
	SuccessCriteria     []string           `json:"success_criteria" bson:"success_criteria"`
	FailureTypes        []string           `json:"failure_types" bson:"failure_types"`
	Verification        []string           `json:"verification" bson:"verification"`
	ExampleRequests     []string           `json:"example_requests" bson:"example_requests"`
	EvaluationStats     map[string]float64 `json:"evaluation_stats" bson:"evaluation_stats"`
	SourcePath          string             `json:"source_path" bson:"source_path"`
	SourceFile          string             `json:"source_file" bson:"source_file"`
}

// Defence in depth; the authorizer remains responsible for full secret/PII
// inspection. Reject the whole payload rather than silently corrupting facts.
var sensitivePattern = regexp.MustCompile(`(?i)(-----BEGIN [A-Z ]*PRIVATE KEY|bearer\s+[a-z0-9._~-]+|\b(?:sk|al)-[a-z0-9_-]{12,}|(?:api[_ -]?key|token|password|secret)["']?\s*[=:]\s*["']?[a-z0-9_./+=-]{8,}|[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}|\b01[016789][- ]?\d{3,4}[- ]?\d{4}\b|\b\d{6}-[1-4]\d{6}\b|\b(?:tool_output|raw_output|raw_conversation)\b)`)

func safeTransfer(text string) bool {
	if len(text) > 64000 {
		return false
	}
	// Inspect JSON string content too, not only the outer serialization.
	text = strings.ReplaceAll(text, `\"`, `"`)
	return !containsSecret(text) && !sensitivePattern.MatchString(text)
}
func structuredText(v any) string { b, _ := json.Marshal(v); return string(b) }
func reflectionEligible(r Record) bool {
	x := r.Reflection
	return x != nil && (x.Outcome == "success" || x.Outcome == "partial" || x.Outcome == "failed") && x.Verified && x.SecretChecked && x.RunID != "" && x.EvaluationID != "" && x.TaskType != "" && x.Lesson != "" && x.VerificationMethod != "" && !math.IsNaN(x.EvaluationScore) && !math.IsInf(x.EvaluationScore, 0) && safeTransfer(structuredText(x))
}
func checkReflection(ctx context.Context, tx Tx, r Record) error {
	if !reflectionEligible(r) {
		return ErrDenied
	}
	if e := checkRelations(ctx, tx, r); e != nil {
		return e
	}
	x := r.Reflection
	run, e := tx.Get(ctx, "agent_runs", r.Scope, x.RunID)
	if e != nil || !live(run) || run.Run != nil && (!validRun(run.Run, run.Scope, time.Now()) || run.Run.Outcome == "running" || run.Run.Outcome != x.Outcome) {
		return ErrDenied
	}
	evaluation, e := tx.Get(ctx, "evaluations", r.Scope, x.EvaluationID)
	if e != nil || !live(evaluation) || !independentEvaluation(evaluation) || evaluation.Target == nil || evaluation.Target.Collection != "agent_runs" || evaluation.Target.ID != run.ID || evaluation.Target.Revision != run.Revision || evaluation.CreatedBy == run.CreatedBy || checkEvaluationEvidence(ctx, tx, evaluation) != nil {
		return ErrDenied
	}
	if (x.SkillID == "") != (x.SkillVersion == "") {
		return ErrDenied
	}
	if x.SkillVersion != "" {
		version, err := tx.Get(ctx, colSkillVersions, r.Scope, x.SkillVersion)
		if err != nil || version.SkillID != x.SkillID || !live(version) || checkSources(ctx, tx, version) != nil {
			return ErrDenied
		}
	}
	hasRun, hasEvaluation := false, false
	for _, ref := range r.Relations {
		if ref.Collection == "agent_runs" && ref.ID == run.ID && ref.Revision == run.Revision {
			hasRun = true
		}
		if ref.Collection == "evaluations" && ref.ID == evaluation.ID && ref.Revision == evaluation.Revision {
			hasEvaluation = true
		}
	}
	if !hasRun || !hasEvaluation {
		return ErrDenied
	}
	return nil
}
func validAnalysis(a *SkillAnalysis) bool {
	if a == nil || a.SkillID == "" || a.Version == "" || len(a.ContentHash) != 64 || strings.TrimSpace(a.Name) == "" || a.Description == "" || a.Problem == "" || len(a.UseWhen) == 0 || a.InputFormat == "" || a.OutputFormat == "" || len(a.SuccessCriteria) == 0 || len(a.Verification) == 0 || a.SourceFile != "SKILL.md" || a.SourcePath == "" {
		return false
	}
	return safeTransfer(structuredText(a))
}

// Shared local eligibility for consumers of an already-active skill version.
// Approval/apply validate their prospective states separately.
func checkActiveSkill(ctx context.Context, tx Tx, r Record) error {
	if !live(r) {
		return ErrConflict
	}
	parent, e := tx.Get(ctx, colSkills, r.Scope, r.SkillID)
	if e != nil {
		return e
	}
	if !live(parent) || parent.ActiveVersion != r.ID {
		return ErrConflict
	}
	if e = checkSources(ctx, tx, parent); e != nil {
		return e
	}
	// The parent and version usually cite identical spans of the same sources.
	// Reuse that validation only when the complete references match.
	if !reflect.DeepEqual(parent.Sources, r.Sources) {
		if e = checkSources(ctx, tx, r); e != nil {
			return e
		}
	}
	if e = checkRelations(ctx, tx, r); e != nil {
		return e
	}
	if r.ComparisonPlan == nil && r.Kind == importedSkillBaselineKind && r.Revision == 1 && r.ValidationID == "" && r.ApprovedBy != "" && r.CreatedByType == roleHuman && validAnalysis(r.Analysis) {
		return nil
	}
	return validation(ctx, tx, r, r.ValidationID, false)
}
