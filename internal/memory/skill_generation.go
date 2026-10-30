package memory

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
)

const skillGenerationReceiptKind = "skill_generation_receipt"

type GenerateSkillInput struct {
	Scope          Scope                `json:"scope"`
	EventID        string               `json:"event_id"`
	SkillID        string               `json:"skill_id"`
	Path           string               `json:"path"`
	Lessons        []Ref                `json:"lessons"`
	ComparisonPlan *SkillComparisonPlan `json:"comparison_plan"`
}

func (s *Service) GenerateSkill(ctx context.Context, token string, in GenerateSkillInput) (ImportSkillResult, error) {
	var out ImportSkillResult
	if invalidGenerateSkillInput(in) {
		return out, ErrInvalid
	}
	in.Lessons = slices.Clone(in.Lessons)
	slices.SortFunc(in.Lessons, func(a, b Ref) int { return strings.Compare(a.ID, b.ID) })
	plan := *in.ComparisonPlan
	in.ComparisonPlan = &plan
	actor, err := s.authorize(ctx, token, "analyze:skills", in.Scope, in.SkillID, "")
	if err != nil {
		return out, err
	}
	if !slices.Contains([]string{roleHuman, "agent", "service", "pipeline"}, actor.Role) {
		return out, ErrDenied
	}
	ctx = bindRequestActor(ctx, actor)
	for _, action := range []string{"write:sources", "write:skills", "write:skill_versions", "read:skills"} {
		if _, err = s.authorize(ctx, token, action, in.Scope, in.SkillID, ""); err != nil {
			return out, err
		}
	}
	// Serialize paid calls only in this service instance. A completed receipt is
	// durable; a crash before the candidate transaction commits can cost a retry.
	s.draftMu.Lock()
	defer s.draftMu.Unlock()
	requestHash := ContentHash(structuredText(in))
	receiptID := "skill-generation-" + ContentHash(structuredText(in.Scope)+"\n"+actor.PrincipalID+"\n"+actor.Role+"\n"+in.EventID)
	if receipt, e := s.Repo.Get(ctx, colJobs, in.Scope, receiptID); e == nil {
		if skillGenerationReceiptConflicts(receipt, requestHash, actor) {
			return out, ErrConflict
		}
		return s.reuseSkillGeneration(ctx, token, in, receipt)
	} else if !errors.Is(e, ErrNotFound) {
		return out, e
	}
	material, err := s.skillGenerationMaterial(ctx, token, in)
	if err != nil {
		return out, err
	}
	snapshot := structuredText(material)
	allowed, err := s.authorize(ctx, token, "analyze:skills", in.Scope, in.SkillID, snapshot)
	if err != nil {
		return out, err
	}
	if !allowed.ExternalTransfer || !allowed.ContentSafe || !safeTransfer(snapshot) {
		return out, ErrDenied
	}
	if s.Drafter == nil {
		return out, ErrUnavailable
	}
	generation := s.Drafter.Generator()
	if !validGenerator(&generation) {
		return out, ErrUnavailable
	}
	preparedHash := ContentHash(requestHash + "\n" + snapshot + "\n" + structuredText(generation))
	draft, err := s.draftSkill(ctx, token, in, actor, material, generation, preparedHash)
	if err != nil {
		return out, err
	}
	// An adapter does not confer authority. Recheck mutable evidence and policy
	// after the paid call; the importer repeats the evidence checks in its tx.
	current, err := s.recheckSkillGenerationMaterial(ctx, token, in, snapshot)
	if err != nil {
		return out, err
	}
	if err = normalizeSkillDraft(current, &draft); err != nil {
		return out, err
	}
	now := time.Now().UTC()
	receipt := Record{ID: receiptID, Scope: in.Scope, Kind: skillGenerationReceiptKind, Status: JobStatusDone, Revision: 1, RequestHash: requestHash, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role, CreatedAt: now, UpdatedAt: now}
	proposal := ProposeSkillInput{Skill: ImportSkillInput{Scope: in.Scope, SkillID: in.SkillID, Path: in.Path, Content: draft.Content, Analysis: &draft.Analysis, ComparisonPlan: in.ComparisonPlan}, Lessons: in.Lessons, Generator: &generation, receipt: &receipt}
	if err = s.attachGeneratedBaseline(ctx, in, current, &proposal); err != nil {
		return out, err
	}
	return s.ProposeSkill(ctx, token, proposal)
}

func invalidGenerateSkillInput(in GenerateSkillInput) bool {
	return !validScope(in.Scope) || !workCheckpointIDPattern.MatchString(in.EventID) || !validRunLabel(in.SkillID, 256) || len(in.Path) > 4096 || strings.TrimSpace(in.Path) != in.Path || strings.IndexFunc(in.Path, unicode.IsControl) >= 0 || !strings.HasSuffix(in.Path, "SKILL.md") || in.ComparisonPlan == nil || !validComparisonPlan(in.ComparisonPlan) || len(in.Lessons) < improvementMinRuns || len(in.Lessons) > 16 || !safeTransfer(structuredText(in))
}

func skillGenerationReceiptConflicts(receipt Record, requestHash string, actor Decision) bool {
	return receipt.Kind != skillGenerationReceiptKind || receipt.Status != JobStatusDone || receipt.ContentErased || receipt.PrivacyQuarantined || receipt.RequestHash != requestHash || receipt.CreatedBy != actor.PrincipalID || receipt.CreatedByType != actor.Role || receipt.Target == nil || receipt.Target.Collection != colSkillVersions
}

// draftSkill recovers a durable prepared draft or makes one metered provider call.
func (s *Service) draftSkill(ctx context.Context, token string, in GenerateSkillInput, actor Decision, material SkillDraftInput, generation Generator, preparedHash string) (SkillDraft, error) {
	_, durable := s.Drafter.(*AnthropicSkillDrafter)
	if durable {
		draft, recovered, err := s.readPreparedDraft(ctx, token, in, actor, preparedHash)
		if err != nil || recovered {
			return draft, err
		}
	}
	reserved, err := s.beginProviderCall(ctx, in.Scope, "skill_draft", "skill_llm", generation.Model)
	if err != nil {
		return SkillDraft{}, err
	}
	var draft SkillDraft
	var usage ProviderUsage
	var draftErr error
	if direct, ok := s.Drafter.(interface {
		DraftAuthorized(context.Context, string, Scope, string, []Ref, SkillDraftInput) (SkillDraft, ProviderUsage, error)
	}); ok {
		draft, usage, draftErr = direct.DraftAuthorized(ctx, token, in.Scope, in.EventID, in.Lessons, material)
	} else {
		draft, usage, draftErr = s.Drafter.Draft(ctx, material)
	}
	if invalidSkillDraftUsage(usage, generation.Model) {
		usage = ProviderUsage{Provider: "skill_llm", Model: generation.Model, Requests: 1}
		if draftErr == nil {
			draftErr = ErrUnavailable
		}
	}
	if err = s.finishProviderCall(ctx, reserved, usage, draftErr == nil); err != nil {
		return draft, err
	}
	if draftErr != nil {
		return draft, draftErr
	}
	if durable {
		if err = normalizeSkillDraft(material, &draft); err != nil {
			return draft, err
		}
		if err = s.savePreparedDraft(ctx, token, in, actor, preparedHash, generation, draft); err != nil {
			return draft, err
		}
	}
	return draft, nil
}

func invalidSkillDraftUsage(usage ProviderUsage, model string) bool {
	return !validProviderUsage(usage) || usage.Requests != 1 || usage.TotalTokens == 0 || usage.InputTokens > usage.TotalTokens || usage.OutputTokens > usage.TotalTokens-usage.InputTokens || usage.Provider != "skill_llm" || usage.Model != model
}

func (s *Service) recheckSkillGenerationMaterial(ctx context.Context, token string, in GenerateSkillInput, snapshot string) (SkillDraftInput, error) {
	current, err := s.skillGenerationMaterial(ctx, token, in)
	if err != nil {
		return current, err
	}
	if structuredText(current) != snapshot {
		return current, ErrConflict
	}
	allowed, err := s.authorize(ctx, token, "analyze:skills", in.Scope, in.SkillID, snapshot)
	if err != nil {
		return current, err
	}
	if !allowed.ExternalTransfer || !allowed.ContentSafe {
		return current, ErrDenied
	}
	return current, nil
}

func (s *Service) attachGeneratedBaseline(ctx context.Context, in GenerateSkillInput, current SkillDraftInput, proposal *ProposeSkillInput) error {
	if in.ComparisonPlan.BaselineVersion == "" {
		return nil
	}
	baseline, e := s.Repo.Get(ctx, colSkillVersions, in.Scope, in.ComparisonPlan.BaselineVersion)
	if e != nil {
		return e
	}
	if structuredText(baseline.Analysis) != structuredText(current.BaselineAnalysis) {
		return ErrConflict
	}
	proposal.generatedBaseline = &Ref{Collection: colSkillVersions, ID: baseline.ID, Revision: baseline.Revision}
	// The model rewrites SKILL.md only; the candidate keeps the baseline's exact companion files.
	if len(baseline.Sources) == 0 {
		return ErrConflict
	}
	source, e := s.Repo.Get(ctx, "sources", in.Scope, baseline.Sources[0].SourceID)
	if e != nil {
		return e
	}
	if (source.SkillBundle == nil) != (baseline.SkillBundleSHA256 == "") || source.SkillBundle != nil && (source.SkillBundle.SHA256 != baseline.SkillBundleSHA256 || !validSkillBundle(source.SkillBundle)) {
		return ErrConflict
	}
	proposal.Skill.Bundle = source.SkillBundle
	return nil
}

// Authorize metadata references individually before they can supply model data
// or be returned in a reused result. The request actor is bound by the caller.
func (s *Service) generationReadRefs(ctx context.Context, token string, scope Scope, refs []Ref, sources []SourceRef) error {
	for _, ref := range refs {
		if _, err := s.authorize(ctx, token, "read:"+ref.Collection, scope, ref.ID, ""); err != nil {
			return err
		}
	}
	for _, source := range sources {
		if _, err := s.authorize(ctx, token, "read:sources", scope, source.SourceID, ""); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) skillGenerationMaterial(ctx context.Context, token string, in GenerateSkillInput) (SkillDraftInput, error) {
	out := SkillDraftInput{SkillID: in.SkillID, Path: in.Path}
	if err := s.generationReadRefs(ctx, token, in.Scope, in.Lessons, nil); err != nil {
		return out, err
	}
	evidence, err := skillLessons(ctx, s.Repo, in.Scope, in.Lessons)
	if err != nil {
		return out, err
	}
	if err = s.generationReadRefs(ctx, token, in.Scope, evidence.Relations, evidence.Sources); err != nil {
		return out, err
	}
	for _, ref := range evidence.Relations {
		if ref.Collection != colReflections {
			continue
		}
		r, e := s.Repo.Get(ctx, ref.Collection, in.Scope, ref.ID)
		if e != nil {
			return out, e
		}
		if r.Revision != ref.Revision || !live(r) || r.Reflection == nil {
			return out, ErrConflict
		}
		x := r.Reflection
		out.Lessons = append(out.Lessons, SkillDraftLesson{TaskType: x.TaskType, Lesson: x.Lesson, VerificationMethod: x.VerificationMethod})
	}
	parent, err := s.Repo.Get(ctx, colSkills, in.Scope, in.SkillID)
	if errors.Is(err, ErrNotFound) && in.ComparisonPlan.BaselineVersion == "" {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if !live(parent) || parent.ActiveVersion != in.ComparisonPlan.BaselineVersion {
		return out, ErrConflict
	}
	if err = s.generationReadRefs(ctx, token, in.Scope, nil, parent.Sources); err != nil {
		return out, err
	}
	if err = checkSources(ctx, s.Repo, parent); err != nil {
		return out, err
	}
	if parent.ActiveVersion == "" {
		return out, nil
	}
	out.BaselineContent, out.BaselineAnalysis, err = s.skillGenerationBaseline(ctx, token, in, parent.ActiveVersion)
	if err != nil {
		return out, err
	}
	return out, nil
}

func (s *Service) skillGenerationBaseline(ctx context.Context, token string, in GenerateSkillInput, activeVersion string) (string, *SkillAnalysis, error) {
	if err := s.generationReadRefs(ctx, token, in.Scope, []Ref{{Collection: colSkillVersions, ID: activeVersion}}, nil); err != nil {
		return "", nil, err
	}
	baseline, err := s.Repo.Get(ctx, colSkillVersions, in.Scope, activeVersion)
	if err != nil {
		return "", nil, err
	}
	if err = s.generationReadRefs(ctx, token, in.Scope, baseline.Relations, baseline.Sources); err != nil {
		return "", nil, err
	}
	if err = checkActiveSkill(ctx, s.Repo, baseline); err != nil {
		return "", nil, err
	}
	if baseline.SkillID != in.SkillID || !validAnalysis(baseline.Analysis) || len(baseline.Sources) == 0 {
		return "", nil, ErrConflict
	}
	source, err := s.Repo.Get(ctx, "sources", in.Scope, baseline.Sources[0].SourceID)
	if err != nil {
		return "", nil, err
	}
	if !live(source) || source.ContentErased || ContentHash(source.Text) != baseline.Analysis.ContentHash || source.ContentHash != baseline.Analysis.ContentHash {
		return "", nil, ErrConflict
	}
	return source.Text, baseline.Analysis, nil
}

func (s *Service) reuseSkillGeneration(ctx context.Context, token string, in GenerateSkillInput, receipt Record) (ImportSkillResult, error) {
	var out ImportSkillResult
	if err := s.generationReadRefs(ctx, token, in.Scope, []Ref{*receipt.Target}, nil); err != nil {
		return out, err
	}
	version, err := s.Repo.Get(ctx, colSkillVersions, in.Scope, receipt.Target.ID)
	if err != nil {
		return out, err
	}
	if reusedSkillVersionConflicts(version, receipt, in) {
		return out, ErrConflict
	}
	if err = s.generationReadRefs(ctx, token, in.Scope, version.Relations, version.Sources); err != nil {
		return out, err
	}
	if err = checkSources(ctx, s.Repo, version); err != nil {
		return out, err
	}
	if err = checkRelations(ctx, s.Repo, version); err != nil {
		return out, err
	}
	if version.Status == "approved" {
		if err = validation(ctx, s.Repo, version, version.ValidationID, false); err != nil {
			return out, err
		}
	}
	if version.Status == "active" {
		if err = checkActiveSkill(ctx, s.Repo, version); err != nil {
			return out, err
		}
	}
	parent, err := s.Repo.Get(ctx, colSkills, in.Scope, in.SkillID)
	if err != nil {
		return out, err
	}
	if !live(parent) {
		return out, ErrConflict
	}
	if err = s.generationReadRefs(ctx, token, in.Scope, nil, parent.Sources); err != nil {
		return out, err
	}
	if err = checkSources(ctx, s.Repo, parent); err != nil {
		return out, err
	}
	if err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if version.Status == "rejected" {
			var e error
			if version, e = reopenDiscardedDraft(ctx, tx, version, Decision{PrincipalID: receipt.CreatedBy, Role: receipt.CreatedByType}); e != nil {
				return e
			}
		}
		return audit(ctx, tx, in.Scope, receipt.CreatedBy, "generate:skills:reuse", version.ID)
	}); err != nil {
		return out, err
	}
	return ImportSkillResult{Skill: parent, Record: version, Reused: true, Version: slices.Index(skillHistory(parent), version.ID) + 1, ComparisonPlanSHA256: comparisonPlanHash(version.ComparisonPlan)}, nil
}

func reusedSkillVersionConflicts(version, receipt Record, in GenerateSkillInput) bool {
	return version.Scope != in.Scope || version.SkillID != in.SkillID || version.Kind != proposedSkillKind || version.CreatedBy != receipt.CreatedBy || version.CreatedByType != receipt.CreatedByType || version.ContentErased || version.PrivacyQuarantined || version.Revision < receipt.Target.Revision || version.Analysis == nil || comparisonPlanHash(version.ComparisonPlan) != comparisonPlanHash(in.ComparisonPlan) || !slices.Contains([]string{"candidate", "approved", "active"}, version.Status) && (version.Status != "rejected" || receipt.CreatedByType != roleHuman)
}

// reopenDiscardedDraft makes a draft the owner discarded a candidate again when the owner asks for it once
// more. It is only a candidate: approval needs a new comparison of this revision, and apply is unchanged.
func reopenDiscardedDraft(ctx context.Context, tx Tx, version Record, d Decision) (Record, error) {
	parent, e := tx.Get(ctx, colSkills, version.Scope, version.SkillID)
	if e != nil {
		return version, e
	}
	if d.Role != roleHuman || !live(parent) || version.Status != "rejected" || version.Kind != proposedSkillKind || version.BaseVersion != parent.ActiveVersion || !slices.Contains(skillHistory(parent), version.ID) {
		return version, ErrConflict
	}
	prev := version.Revision
	version.Status, version.ApprovedBy, version.ValidationID = "candidate", "", ""
	version.Revision++
	version.UpdatedAt = time.Now().UTC()
	if e = tx.Replace(ctx, colSkillVersions, version, prev); e != nil {
		return version, e
	}
	return version, audit(ctx, tx, version.Scope, d.PrincipalID, "reopen:skill_versions", version.ID)
}

func saveSkillGenerationReceipt(ctx context.Context, tx Tx, proposal *ProposeSkillInput, version Record) error {
	if proposal == nil || proposal.receipt == nil {
		return nil
	}
	r := *proposal.receipt
	r.Target = &Ref{Collection: colSkillVersions, ID: version.ID, Revision: version.Revision}
	if old, err := tx.Get(ctx, colJobs, r.Scope, r.ID); err == nil {
		if old.Kind != r.Kind || old.Status != JobStatusDone || old.ContentErased || old.PrivacyQuarantined || old.RequestHash != r.RequestHash || old.Target == nil || old.Target.ID != version.ID || old.CreatedBy != r.CreatedBy || old.CreatedByType != r.CreatedByType {
			return ErrConflict
		}
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	return tx.Insert(ctx, colJobs, r)
}
