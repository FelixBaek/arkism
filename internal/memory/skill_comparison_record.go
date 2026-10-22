package memory

import (
	"context"
	"errors"
	"math"
	"slices"
	"sort"
	"time"
)

type SkillComparisonCase struct {
	CaseSHA256   string    `json:"case_sha256"`
	Category     string    `json:"category"`
	Variant      string    `json:"variant"`
	Passed       bool      `json:"passed"`
	OutputSHA256 string    `json:"output_sha256"`
	Cost         float64   `json:"cost"`
	DurationMS   int64     `json:"duration_ms"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
}

func (v *SkillComparisonCase) UnmarshalJSON(data []byte) error {
	type plain SkillComparisonCase
	return decodeComparison(data, (*plain)(v), []string{"case_sha256", "category", "variant", "passed", "output_sha256", "cost", "duration_ms", "started_at", "finished_at"})
}

type SkillComparisonResult struct {
	FormatVersion     int                    `json:"format_version"`
	Candidate         Ref                    `json:"candidate"`
	BaselineID        string                 `json:"baseline_id"`
	BaselineRevision  int64                  `json:"baseline_revision"`
	ContentSHA256     map[string]string      `json:"content_sha256"`
	DatasetSHA256     string                 `json:"dataset_sha256"`
	EnvironmentSHA256 string                 `json:"environment_sha256"`
	Report            *SkillComparisonReport `json:"comparison_report"`
	Passed            bool                   `json:"passed"`
	Cases             []SkillComparisonCase  `json:"cases"`
}

func (v *SkillComparisonResult) UnmarshalJSON(data []byte) error {
	type plain SkillComparisonResult
	return decodeComparison(data, (*plain)(v), []string{"format_version", "candidate", "baseline_id", "baseline_revision", "content_sha256", "dataset_sha256", "environment_sha256", "comparison_report", "passed", "cases"})
}

type RecordSkillComparisonInput struct {
	TrialID string                `json:"trial_id,omitempty"`
	Scope   Scope                 `json:"scope"`
	EventID string                `json:"event_id"`
	Result  SkillComparisonResult `json:"result"`
}
type RecordSkillComparisonResult struct {
	SourceID   string `json:"source_id"`
	Runs       []Ref  `json:"runs"`
	Evaluation Ref    `json:"evaluation"`
	Authority  string `json:"authority"`
}

type comparisonPair struct {
	category string
	seen     [2]bool
}

func invalidComparisonResult(r SkillComparisonResult) bool {
	return r.FormatVersion != 1 || r.Candidate.Collection != colSkillVersions || !validRunLabel(r.Candidate.ID, 256) || r.Candidate.Revision < 1 || len(r.Cases) < 6 || len(r.Cases) > 128 || len(r.Cases)%2 != 0 || r.Report == nil || !validComparisonReport(r.Report) || len(r.ContentSHA256) != 2 || !sourceHashPattern.MatchString(r.ContentSHA256["baseline"]) || !sourceHashPattern.MatchString(r.ContentSHA256["candidate"]) || !sourceHashPattern.MatchString(r.DatasetSHA256) || !sourceHashPattern.MatchString(r.EnvironmentSHA256)
}

func invalidComparisonCase(row SkillComparisonCase, now time.Time) bool {
	return !sourceHashPattern.MatchString(row.CaseSHA256) || !sourceHashPattern.MatchString(row.OutputSHA256) || !slices.Contains([]string{"quality", "regression", "safety"}, row.Category) || !slices.Contains([]string{"baseline", "candidate"}, row.Variant) || !finiteNonnegative(row.Cost) || row.DurationMS < 0 || row.StartedAt.IsZero() || row.FinishedAt.Before(row.StartedAt) || row.FinishedAt.After(now) || !storableTime(row.StartedAt) || !storableTime(row.FinishedAt) || row.DurationMS > row.FinishedAt.Sub(row.StartedAt).Milliseconds()+2
}

func pairComparisonCase(pairs map[string]comparisonPair, row SkillComparisonCase, index int) bool {
	p := pairs[row.CaseSHA256]
	if p.seen[index] || p.category != "" && p.category != row.Category {
		return false
	}
	p.category, p.seen[index] = row.Category, true
	pairs[row.CaseSHA256] = p
	return true
}

func addComparisonCase(run *RunDetails, cost *float64, duration *int64, row SkillComparisonCase) bool {
	*cost += row.Cost
	if !finiteNonnegative(*cost) || row.DurationMS > math.MaxInt64-*duration {
		return false
	}
	*duration += row.DurationMS
	if !row.Passed {
		run.Outcome = "failed"
	}
	if run.StartedAt.IsZero() || row.StartedAt.Before(run.StartedAt) {
		run.StartedAt = row.StartedAt
	}
	if run.FinishedAt == nil || row.FinishedAt.After(*run.FinishedAt) {
		end := row.FinishedAt
		run.FinishedAt = &end
	}
	return true
}

func comparisonCoverageComplete(pairs map[string]comparisonPair, categories map[string]bool, quality [2]int) bool {
	for _, p := range pairs {
		if !p.seen[0] || !p.seen[1] {
			return false
		}
	}
	return len(categories) == 3 && quality[0] != 0 && quality[0] == quality[1]
}

func comparisonReportMatches(v *SkillComparisonReport, baselineScore, candidateScore, cost float64, duration int64, regression, safety bool) bool {
	near := func(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b))) }
	return near(v.BaselineScore, baselineScore) && near(v.CandidateScore, candidateScore) && near(v.CandidateCost, cost) && v.CandidateDurationMS == duration && v.RegressionPassed == regression && v.SafetyPassed == safety
}

func comparisonMeasurements(r SkillComparisonResult, scope Scope) ([2]RunDetails, SkillComparisonReport, error) {
	var runs [2]RunDetails
	if invalidComparisonResult(r) {
		return runs, SkillComparisonReport{}, ErrInvalid
	}
	pairs := map[string]comparisonPair{}
	categories := map[string]bool{}
	var quality, passed [2]int
	var cost [2]float64
	var duration [2]int64
	regression, safety := true, true
	agent := scope.AgentID
	if agent == "" {
		agent = "skill-comparison"
	}
	for i := range runs {
		runs[i] = RunDetails{AgentID: agent, TaskType: "skill_comparison", Outcome: "success"}
	}
	now := time.Now()
	for _, row := range r.Cases {
		if invalidComparisonCase(row, now) {
			return runs, SkillComparisonReport{}, ErrInvalid
		}
		index := 0
		if row.Variant == "candidate" {
			index = 1
		}
		if !pairComparisonCase(pairs, row, index) {
			return runs, SkillComparisonReport{}, ErrInvalid
		}
		categories[row.Category] = true
		if row.Category == "quality" {
			quality[index]++
			if row.Passed {
				passed[index]++
			}
		}
		if index == 1 && row.Category == "regression" {
			regression = regression && row.Passed
		}
		if index == 1 && row.Category == "safety" {
			safety = safety && row.Passed
		}
		if !addComparisonCase(&runs[index], &cost[index], &duration[index], row) {
			return runs, SkillComparisonReport{}, ErrInvalid
		}
	}
	if !comparisonCoverageComplete(pairs, categories, quality) {
		return runs, SkillComparisonReport{}, ErrInvalid
	}
	baselineScore, candidateScore := float64(passed[0])/float64(quality[0]), float64(passed[1])/float64(quality[1])
	v := r.Report
	if !comparisonReportMatches(v, baselineScore, candidateScore, cost[1], duration[1], regression, safety) {
		return runs, SkillComparisonReport{}, ErrInvalid
	}
	measured := *v
	measured.BaselineScore, measured.CandidateScore = baselineScore, candidateScore
	measured.CandidateCost = cost[1]
	return runs, measured, nil
}

func validRecordSkillComparisonInput(in RecordSkillComparisonInput) bool {
	return validScope(in.Scope) && workCheckpointIDPattern.MatchString(in.EventID) && !containsSecret(in.EventID) && (in.TrialID == "" || validRunLabel(in.TrialID, 128))
}

func (s *Service) comparisonBaseline(ctx context.Context, auth func(action, id, content string) error, in RecordSkillComparisonInput, candidate Record) (Record, error) {
	if in.Result.BaselineID == "" {
		return candidate, nil
	}
	if err := auth("read:skill_versions", in.Result.BaselineID, ""); err != nil {
		return Record{}, err
	}
	return s.Repo.Get(ctx, colSkillVersions, in.Scope, in.Result.BaselineID)
}

func authorizeComparisonWrites(auth func(action, id, content string) error, out RecordSkillComparisonResult, body string) error {
	for _, ref := range append(append([]Ref{{Collection: "sources", ID: out.SourceID}}, out.Runs...), out.Evaluation) {
		if err := auth("read:"+ref.Collection, ref.ID, ""); err != nil {
			return err
		}
		if err := auth("write:"+ref.Collection, ref.ID, body); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) RecordSkillComparison(ctx context.Context, token string, in RecordSkillComparisonInput) (RecordSkillComparisonResult, error) {
	var out RecordSkillComparisonResult
	if !validRecordSkillComparisonInput(in) {
		return out, ErrInvalid
	}
	runs, measured, err := comparisonMeasurements(in.Result, in.Scope)
	if err != nil {
		return out, err
	}
	body := structuredText(in.Result)
	if in.TrialID != "" {
		body = structuredText(struct {
			TrialID string                `json:"trial_id"`
			Result  SkillComparisonResult `json:"result"`
		}{in.TrialID, in.Result})
	}
	if len(body) > 64000 || !safeTransfer(body) {
		return out, ErrInvalid
	}
	actor, err := s.authorize(ctx, token, "read:skill_versions", in.Scope, in.Result.Candidate.ID, "")
	if err != nil {
		return out, err
	}
	if actor.Role != "service" && actor.Role != "pipeline" {
		return out, ErrDenied
	}
	ctx = bindRequestActor(ctx, actor)
	auth := func(action, id, content string) error {
		d, e := s.authorize(ctx, token, action, in.Scope, id, content)
		if e != nil {
			return e
		}
		if d.PrincipalID != actor.PrincipalID || d.Role != actor.Role || content != "" && !d.ContentSafe {
			return ErrDenied
		}
		return nil
	}
	candidate, err := s.Repo.Get(ctx, colSkillVersions, in.Scope, in.Result.Candidate.ID)
	if err != nil {
		return out, err
	}
	if candidate.CreatedBy == actor.PrincipalID {
		return out, ErrDenied
	}
	if err = auth("read:skills", candidate.SkillID, ""); err != nil {
		return out, err
	}
	baseline, err := s.comparisonBaseline(ctx, auth, in, candidate)
	if err != nil {
		return out, err
	}
	evidence, sensitivity, err := learningSources(ctx, s.Repo, candidate, baseline)
	if err != nil {
		return out, err
	}
	if len(evidence) > 63 {
		return out, ErrInvalid
	}
	for _, src := range evidence {
		if err = auth("read:sources", src.SourceID, ""); err != nil {
			return out, err
		}
	}
	prefix := "comparison-" + ContentHash(structuredText(in.Scope)+"\n"+actor.PrincipalID+"\n"+actor.Role+"\n"+in.EventID)
	out = RecordSkillComparisonResult{SourceID: prefix + "-source", Runs: []Ref{{Collection: "agent_runs", ID: prefix + "-baseline", Revision: 1}, {Collection: "agent_runs", ID: prefix + "-candidate", Revision: 1}}, Evaluation: Ref{Collection: "evaluations", ID: prefix + "-evaluation", Revision: 1}, Authority: AuthorityDataOnly}
	if err = authorizeComparisonWrites(auth, out, body); err != nil {
		return RecordSkillComparisonResult{}, err
	}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		old, e := tx.Get(ctx, "sources", in.Scope, out.SourceID)
		if e == nil {
			return checkRecordedSkillComparison(ctx, tx, in.Scope, out, old, body, actor.PrincipalID, candidate.ID)
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		if _, e = sourceDeletion(ctx, tx, in.Scope, out.SourceID); !errors.Is(e, ErrNotFound) {
			if e != nil {
				return e
			}
			return ErrConflict
		}
		current, e := comparisonCandidate(ctx, tx, in, candidate.ID, actor.PrincipalID)
		if e != nil {
			return e
		}
		if e = fenceComparisonCandidate(ctx, tx, in.Scope, current); e != nil {
			return e
		}
		plan, e := checkComparisonPlan(ctx, tx, in, current)
		if e != nil {
			return e
		}
		base, e := comparisonBase(ctx, tx, in, current, plan)
		if e != nil {
			return e
		}
		refs, level, e := learningSources(ctx, tx, current, base)
		if e != nil {
			return e
		}
		if structuredText(refs) != structuredText(evidence) || level != sensitivity {
			return ErrConflict
		}
		if e = fenceComparisonSources(ctx, tx, in.Scope, refs); e != nil {
			return e
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		source := Record{ID: out.SourceID, Scope: in.Scope, Kind: "skill_comparison_result", Status: statusActive, Revision: 1, Text: body, ContentHash: ContentHash(body), Sensitivity: level, TrustLevel: "external_untrusted", OriginProduct: "arkism", SourceRef: prefix, CollectedAt: &now, CreatedAt: now, UpdatedAt: now, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role}
		source.Sources = append([]SourceRef{sourceReference(source, "comparison")}, refs...)
		if e = tx.Insert(ctx, "sources", source); e != nil {
			return e
		}
		for i, ref := range out.Runs {
			r := Record{ID: ref.ID, Scope: in.Scope, Kind: "skill_comparison", Status: statusActive, Revision: 1, Text: ref.ID, Run: &runs[i], Sources: source.Sources, Sensitivity: level, CreatedAt: now, UpdatedAt: now, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role}
			if e = insertSourceRecord(ctx, tx, "agent_runs", r); e != nil {
				return e
			}
		}
		evaluation := Record{ID: out.Evaluation.ID, Scope: in.Scope, Kind: "skill_comparison", Status: statusActive, Revision: 1, Text: "Measured fixed skill comparison", Sources: source.Sources, Relations: out.Runs, Target: &in.Result.Candidate, Evaluation: &Evaluation{EvalType: "automated_check", Passed: &in.Result.Passed, ComparisonReport: &measured}, Passed: in.Result.Passed, Sensitivity: level, CreatedAt: now, UpdatedAt: now, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role}
		if comparisonPasses(current, evaluation) != in.Result.Passed {
			return ErrInvalid
		}
		if e = insertSourceRecord(ctx, tx, "evaluations", evaluation); e != nil {
			return e
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "record:skill_comparison", out.Evaluation.ID)
	})
	if err != nil {
		return RecordSkillComparisonResult{}, err
	}
	return out, nil
}

func checkRecordedSkillComparison(ctx context.Context, tx Tx, scope Scope, out RecordSkillComparisonResult, old Record, body, actorID, candidateID string) error {
	if old.Kind != "skill_comparison_result" || old.ContentHash != ContentHash(body) || old.Text != body || old.CreatedBy != actorID {
		return ErrConflict
	}
	if !live(old) || old.ContentErased {
		return ErrConflict
	}
	if e := checkSources(ctx, tx, old); e != nil {
		return e
	}
	for _, ref := range append(slices.Clone(out.Runs), out.Evaluation) {
		r, e := tx.Get(ctx, ref.Collection, scope, ref.ID)
		if e != nil {
			return e
		}
		if r.Revision != ref.Revision || !live(r) || r.ContentErased || r.CreatedBy != actorID {
			return ErrConflict
		}
		if e = checkSources(ctx, tx, r); e != nil {
			return e
		}
	}
	current, e := tx.Get(ctx, colSkillVersions, scope, candidateID)
	if e != nil {
		return e
	}
	if current.PrivacyQuarantined || current.ContentErased || !slices.Contains([]string{"candidate", "approved", "active", "superseded"}, current.Status) {
		return ErrConflict
	}
	if e = checkSources(ctx, tx, current); e != nil {
		return e
	}
	return checkRelations(ctx, tx, current)
}

func comparisonCandidate(ctx context.Context, tx Tx, in RecordSkillComparisonInput, candidateID, actorID string) (Record, error) {
	current, e := tx.Get(ctx, colSkillVersions, in.Scope, candidateID)
	if e != nil {
		return Record{}, e
	}
	if current.Revision != in.Result.Candidate.Revision || (current.Status != "candidate" && !(in.TrialID != "" && current.Status == "approved")) || current.PrivacyQuarantined || current.ContentErased || current.CreatedBy == actorID || current.Analysis == nil || current.Analysis.ContentHash != in.Result.ContentSHA256["candidate"] {
		return Record{}, ErrConflict
	}
	if in.TrialID != "" {
		if e = checkComparisonTrial(ctx, tx, in, current, actorID); e != nil {
			return Record{}, e
		}
	}
	return current, nil
}

func checkComparisonTrial(ctx context.Context, tx Tx, in RecordSkillComparisonInput, current Record, actorID string) error {
	trial, e := tx.Get(ctx, colSkillTrials, in.Scope, in.TrialID)
	if e != nil {
		return e
	}
	if trial.SkillTrial == nil || trial.Status != "active" || trial.ContentErased || trial.PrivacyQuarantined || trial.SkillID != current.SkillID || trial.SkillTrial.Candidate.ID != current.ID || trial.SkillTrial.Candidate.Revision != current.Revision || trial.CreatedBy == actorID {
		return ErrDenied
	}
	_, e = skillTrialCandidate(ctx, tx, in.Scope, current.SkillID, trial.SkillTrial)
	return e
}

func fenceComparisonCandidate(ctx context.Context, tx Tx, scope Scope, current Record) error {
	// Same-revision write fences approval/deletion without changing logical history.
	if e := tx.Replace(ctx, colSkillVersions, current, current.Revision); e != nil {
		return e
	}
	if e := checkSources(ctx, tx, current); e != nil {
		return e
	}
	if current.Kind == proposedSkillKind {
		for _, ref := range proposedLessons(current) {
			if e := tx.FenceRelationMember(ctx, scope, ref.Collection, ref.ID); e != nil {
				return e
			}
		}
	}
	return checkRelations(ctx, tx, current)
}

func checkComparisonPlan(ctx context.Context, tx Tx, in RecordSkillComparisonInput, current Record) (*SkillComparisonPlan, error) {
	plan := current.ComparisonPlan
	if plan == nil || comparisonPlanHash(plan) != in.Result.Report.PlanSHA256 || plan.DatasetSHA256 != in.Result.DatasetSHA256 || plan.EnvironmentSHA256 != in.Result.EnvironmentSHA256 || plan.BaselineVersion != in.Result.BaselineID {
		return nil, ErrConflict
	}
	parent, e := tx.Get(ctx, colSkills, in.Scope, current.SkillID)
	if e != nil {
		return nil, e
	}
	if !live(parent) || parent.ActiveVersion != plan.BaselineVersion {
		return nil, ErrConflict
	}
	if e = tx.Replace(ctx, colSkills, parent, parent.Revision); e != nil {
		return nil, e
	}
	return plan, nil
}

func comparisonBase(ctx context.Context, tx Tx, in RecordSkillComparisonInput, current Record, plan *SkillComparisonPlan) (Record, error) {
	base := current
	if plan.BaselineVersion != "" {
		var e error
		base, e = tx.Get(ctx, colSkillVersions, in.Scope, plan.BaselineVersion)
		if e != nil {
			return Record{}, e
		}
		if base.Revision != in.Result.BaselineRevision || base.SkillID != current.SkillID || base.Analysis == nil || base.Analysis.ContentHash != in.Result.ContentSHA256["baseline"] {
			return Record{}, ErrConflict
		}
		if e = checkActiveSkill(ctx, tx, base); e != nil {
			return Record{}, e
		}
	} else if in.Result.BaselineRevision != 0 || in.Result.ContentSHA256["baseline"] != ContentHash("") {
		return Record{}, ErrInvalid
	}
	baseBundle := ""
	if plan.BaselineVersion != "" {
		baseBundle = base.SkillBundleSHA256
	}
	if in.Result.Report.BundleSHA256["candidate"] != current.SkillBundleSHA256 || in.Result.Report.BundleSHA256["baseline"] != baseBundle {
		return Record{}, ErrConflict
	}
	return base, nil
}

func fenceComparisonSources(ctx context.Context, tx Tx, scope Scope, refs []SourceRef) error {
	ids := []string{}
	for _, src := range refs {
		if !slices.Contains(ids, src.SourceID) {
			ids = append(ids, src.SourceID)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if e := tx.FenceSource(ctx, scope, id); e != nil {
			return e
		}
	}
	return nil
}
