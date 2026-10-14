package memory

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// Prepared bodies belong to canonical, erasable sources, never jobs/audit.
func preparedDraftID(scope Scope, actor, event string) string {
	return "skill-prepared-" + ContentHash(structuredText(scope)+"\n"+actor+"\n"+event)
}
func (s *Service) readPreparedDraft(ctx context.Context, token string, in GenerateSkillInput, actor Decision, hash string) (SkillDraft, bool, error) {
	var draft SkillDraft
	id := preparedDraftID(in.Scope, actor.PrincipalID, in.EventID)
	row, e := s.Get(ctx, token, "sources", in.Scope, id)
	if errors.Is(e, ErrNotFound) {
		return draft, false, nil
	}
	if e != nil {
		return draft, false, e
	}
	if row.Kind != "skill_draft_prepared" || row.RequestHash != hash || row.CreatedBy != actor.PrincipalID || !live(row) || row.ContentErased || row.PrivacyQuarantined || row.ContentHash != ContentHash(row.Text) {
		return draft, false, ErrConflict
	}
	if e = checkPreparedDraftEvidence(ctx, s.Repo, row); e != nil {
		return draft, false, e
	}
	if e = checkSources(ctx, s.Repo, row); e != nil {
		return draft, false, e
	}
	if json.Unmarshal([]byte(row.Text), &draft) != nil || !safeTransfer(row.Text) {
		return draft, false, ErrConflict
	}
	return draft, true, nil
}
func (s *Service) savePreparedDraft(ctx context.Context, token string, in GenerateSkillInput, actor Decision, hash string, generator Generator, draft SkillDraft) error {
	id := preparedDraftID(in.Scope, actor.PrincipalID, in.EventID)
	text := structuredText(draft)
	if !safeTransfer(text) {
		return ErrDenied
	}
	if _, e := s.authorize(ctx, token, "write:sources", in.Scope, id, text); e != nil {
		return e
	}
	evidence, e := skillLessons(ctx, s.Repo, in.Scope, in.Lessons)
	if e != nil {
		return e
	}
	refs := append([]Ref{}, evidence.Relations...)
	if in.ComparisonPlan.BaselineVersion != "" {
		base, e := s.Repo.Get(ctx, colSkillVersions, in.Scope, in.ComparisonPlan.BaselineVersion)
		if e != nil {
			return e
		}
		refs = append(refs, Ref{Collection: colSkillVersions, ID: base.ID, Revision: base.Revision})
		evidence.Sources = append(evidence.Sources, base.Sources...)
	}
	now := time.Now().UTC()
	row := Record{ID: id, Scope: in.Scope, Kind: "skill_draft_prepared", Status: "active", Revision: 1, Text: text, ContentHash: ContentHash(text), RequestHash: hash, OriginProduct: "arkism", SourceRef: "skill-prepared/" + ContentHash(in.EventID), CollectedAt: &now, Sensitivity: "private", Generator: &generator, Sources: evidence.Sources, Relations: refs, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role, CreatedAt: now, UpdatedAt: now}
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		old, e := tx.Get(ctx, "sources", in.Scope, id)
		if e == nil {
			if old.RequestHash != hash || old.ContentHash != row.ContentHash || !live(old) || old.ContentErased || old.PrivacyQuarantined {
				return ErrConflict
			}
			return checkPreparedDraftEvidence(ctx, tx, old)
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		if e = checkSources(ctx, tx, row); e != nil {
			return e
		}
		if e = checkPreparedDraftEvidence(ctx, tx, row); e != nil {
			return e
		}
		if e = tx.Insert(ctx, "sources", row); e != nil {
			return e
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "prepare:skill_draft", id)
	})
}

// Candidate evidence is allowed in its precise verified learning state, not
// treated as an active memory. Preserve it for erasure traversal and revocation.
func checkPreparedDraftEvidence(ctx context.Context, tx Tx, r Record) error {
	if r.Kind != "skill_draft_prepared" {
		return ErrInvalid
	}
	evidence, err := skillLessons(ctx, tx, r.Scope, proposedLessons(r))
	if err != nil {
		return err
	}
	for _, ref := range evidence.Relations {
		if !slices.Contains(r.Relations, ref) {
			return ErrConflict
		}
	}
	if err = checkSources(ctx, tx, r); err != nil {
		return err
	}
	copy := r
	copy.Relations = []Ref{}
	for _, ref := range r.Relations {
		if ref.Collection != "memory_candidates" {
			copy.Relations = append(copy.Relations, ref)
		}
	}
	return checkRelations(ctx, tx, copy)
}
