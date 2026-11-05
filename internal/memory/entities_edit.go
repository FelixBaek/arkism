package memory

import (
	"context"
	"slices"
	"strings"
	"time"
)

type EntityPatchInput struct {
	Scope         Scope    `json:"scope"`
	Name          *string  `json:"name,omitempty"`
	Type          *string  `json:"type,omitempty"`
	AddAliases    []string `json:"add_aliases,omitempty"`
	RemoveAliases []string `json:"remove_aliases,omitempty"`
	// Confirm accepts a model-proposed entity and its proposed mentions.
	Confirm bool `json:"confirm,omitempty"`
	// SummaryReview approves (the draft becomes the summary) or rejects the summary draft.
	SummaryReview string `json:"summary_review,omitempty"`
}

// EntitySummaryInput asks for a summary draft; dry_run returns it without storing.
type EntitySummaryInput struct {
	Scope  Scope `json:"scope"`
	DryRun bool  `json:"dry_run,omitempty"`
}

type EntityMergeInput struct {
	Scope Scope  `json:"scope"`
	Into  string `json:"into"`
}

type EntityScopeInput struct {
	Scope Scope `json:"scope"`
}

type EntityDeleteResult struct {
	ID              string `json:"id"`
	RemovedMentions int    `json:"removed_mentions"`
}

// editEntities runs a person's change (or one of roles) under the scope's entity lock.
func (s *Service) editEntities(ctx context.Context, token string, scope Scope, id, action string, fn func(store namedEntityStore, g *entityGraph, actor string, now time.Time) error, roles ...string) error {
	d, err := s.entityEditor(ctx, token, scope, id, roles...)
	if err != nil {
		return err
	}
	ctx = bindRequestActor(ctx, d)
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		store, err := entityTx(tx)
		if err != nil {
			return err
		}
		if err = store.LockNamedEntities(ctx, scope); err != nil {
			return err
		}
		g, err := loadEntityGraph(ctx, store, scope)
		if err != nil {
			return err
		}
		if err = fn(store, g, d.PrincipalID, time.Now().UTC()); err != nil {
			return err
		}
		return audit(ctx, tx, scope, d.PrincipalID, action, id)
	})
}

func validEntityPatch(in EntityPatchInput) bool {
	if in.Name != nil && !validEntityName(normalizeEntityName(*in.Name)) || in.Type != nil && !entityTypes[*in.Type] {
		return false
	}
	if len(in.AddAliases)+len(in.RemoveAliases) > entityMaxAliases {
		return false
	}
	for _, a := range in.AddAliases {
		if !validEntityName(normalizeEntityName(a)) {
			return false
		}
	}
	if in.SummaryReview != "" && in.SummaryReview != mmReviewApprove && in.SummaryReview != mmReviewReject {
		return false
	}
	return in.Name != nil || in.Type != nil || len(in.AddAliases)+len(in.RemoveAliases) > 0 || in.Confirm || in.SummaryReview != ""
}

// reviewEntitySummary applies a person's decision on the summary draft; approval needs every citation to still stand.
func (s *Service) reviewEntitySummary(ctx context.Context, scope Scope, e *NamedEntity, action, actor string, now time.Time) error {
	if action == "" {
		return nil
	}
	if e.SummaryDraft == nil || action == mmReviewApprove && !s.citationsStand(ctx, scope, e.SummaryDraft.Citations) {
		return ErrConflict
	}
	if action == mmReviewApprove {
		decided := *e.SummaryDraft
		decided.Status, decided.Action, decided.By, decided.At = statusActive, action, actor, now
		e.Summary = &decided
	}
	e.SummaryDraft = nil
	return nil
}

// DraftEntitySummary asks the server model for a cited summary of an entity from the approved
// memories and observations its names recall (each one passes the transfer gate). The
// draft waits on the entity for a person (PATCH summary_review); it is never published by itself.
func (s *Service) DraftEntitySummary(ctx context.Context, token, id string, in EntitySummaryInput) (NamedEntity, error) {
	d, err := s.entityEditor(ctx, token, in.Scope, id, roleService)
	if err != nil {
		return NamedEntity{}, err
	}
	if s.Wiki == nil {
		return NamedEntity{}, ErrModelNotConfigured
	}
	store, err := s.entityStore()
	if err != nil {
		return NamedEntity{}, err
	}
	g, err := loadEntityGraph(ctx, store, in.Scope)
	if err != nil {
		return NamedEntity{}, err
	}
	e, err := errEntityNotFound(g, id)
	if err != nil {
		return NamedEntity{}, err
	}
	query := strings.Join(append([]string{e.Name}, e.Aliases...), " ")
	draft, err := s.citedSummaryDraft(ctx, token, d, summaryRequest{scope: in.Scope, title: e.Type + ": " + e.Name, query: query}, &WikiJob{MaxTotalTokens: obsRunTokens})
	if err != nil {
		return NamedEntity{}, err
	}
	if in.DryRun {
		out := publicEntity(*e)
		out.SummaryDraft = draft
		return out, nil
	}
	var out NamedEntity
	err = s.editEntities(ctx, token, in.Scope, id, "summarize:entities", func(store namedEntityStore, g *entityGraph, actor string, now time.Time) error {
		cur, err := errEntityNotFound(g, id)
		if err != nil {
			return err
		}
		next := *cur
		next.SummaryDraft, next.UpdatedAt = draft, now
		if err = store.PutNamedEntity(ctx, in.Scope, next); err != nil {
			return err
		}
		out = publicEntity(next)
		return nil
	}, roleService)
	return out, err
}

// patchedEntity applies a patch; a renamed entity keeps its old name as an alias.
func patchedEntity(e NamedEntity, in EntityPatchInput) NamedEntity {
	e.Aliases = slices.Clone(e.Aliases)
	if in.Name != nil {
		if name := normalizeEntityName(*in.Name); name != e.Name {
			e.Aliases = append(e.Aliases, e.Name)
			e.Name = name
		}
	}
	if in.Type != nil {
		e.Type = *in.Type
	}
	e.Aliases = append(e.Aliases, in.AddAliases...)
	aliases := []string{}
	for _, a := range e.Aliases {
		a = normalizeEntityName(a)
		drop := strings.EqualFold(a, e.Name) || slices.ContainsFunc(in.RemoveAliases, func(r string) bool { return strings.EqualFold(normalizeEntityName(r), a) })
		if !drop && !slices.ContainsFunc(aliases, func(x string) bool { return strings.EqualFold(x, a) }) {
			aliases = append(aliases, a)
		}
	}
	e.Aliases = aliases
	if in.Confirm {
		e.Proposed = false
	}
	e.Keys = entityKeys(e)
	return e
}

// keyTaken reports whether another live entity of the same type owns one of the keys.
func keyTaken(g *entityGraph, e NamedEntity, except ...string) bool {
	for _, k := range e.Keys {
		for _, other := range g.byKey[k] {
			if other.ID != e.ID && !other.Deleted && other.Type == e.Type && !slices.Contains(except, other.ID) {
				return true
			}
		}
	}
	return false
}

// PatchEntity renames, retypes, edits aliases or confirms an entity. People only.
func (s *Service) PatchEntity(ctx context.Context, token, id string, in EntityPatchInput) (NamedEntity, error) {
	if !validEntityPatch(in) {
		return NamedEntity{}, ErrInvalid
	}
	var out NamedEntity
	err := s.editEntities(ctx, token, in.Scope, id, "update:entities", func(store namedEntityStore, g *entityGraph, actor string, now time.Time) error {
		e, err := errEntityNotFound(g, id)
		if err != nil {
			return err
		}
		next := patchedEntity(*e, in)
		if len(next.Aliases) > entityMaxAliases || keyTaken(g, next) {
			return ErrConflict
		}
		if err = s.reviewEntitySummary(ctx, in.Scope, &next, in.SummaryReview, actor, now); err != nil {
			return err
		}
		next.EditedBy, next.UpdatedAt = actor, now
		if err = store.PutNamedEntity(ctx, in.Scope, next); err != nil {
			return err
		}
		out = publicEntity(next)
		if !in.Confirm {
			return nil
		}
		var remove, add []EntityMention
		for _, m := range g.mentions {
			if m.EntityID == id && m.Relation == relMentionsProposed {
				remove = append(remove, m)
				m.Relation, m.Origin = relMentions, originHuman
				add = append(add, m)
			}
		}
		return store.ReplaceEntityMentions(ctx, in.Scope, remove, add)
	})
	return out, err
}

// MergeEntity moves every mention of id onto into and keeps id's names as aliases.
func (s *Service) MergeEntity(ctx context.Context, token, id string, in EntityMergeInput) (NamedEntity, error) {
	if !graphID.MatchString(in.Into) || in.Into == id {
		return NamedEntity{}, ErrInvalid
	}
	var out NamedEntity
	err := s.editEntities(ctx, token, in.Scope, id, "merge:entities", func(store namedEntityStore, g *entityGraph, actor string, now time.Time) error {
		src, err := errEntityNotFound(g, id)
		if err != nil {
			return err
		}
		dst, err := errEntityNotFound(g, in.Into)
		if err != nil {
			return err
		}
		next := patchedEntity(*dst, EntityPatchInput{AddAliases: append([]string{src.Name}, src.Aliases...), Confirm: true})
		if len(next.Aliases) > entityMaxAliases || keyTaken(g, next, src.ID) {
			return ErrConflict
		}
		next.EditedBy, next.UpdatedAt = actor, now
		if err = store.PutNamedEntity(ctx, in.Scope, next); err != nil {
			return err
		}
		remove, add := movedMentions(g.mentions, id, in.Into)
		if err = store.ReplaceEntityMentions(ctx, in.Scope, remove, add); err != nil {
			return err
		}
		out = publicEntity(next)
		return store.DeleteNamedEntity(ctx, in.Scope, id)
	})
	return out, err
}

// movedMentions retargets from's edges to into, skipping edges into already has.
func movedMentions(all []EntityMention, from, into string) (remove, add []EntityMention) {
	has := map[[2]string]bool{}
	for _, m := range all {
		if m.EntityID == into {
			has[[2]string{m.MemoryID, m.Relation}] = true
		}
	}
	for _, m := range all {
		if m.EntityID != from {
			continue
		}
		remove = append(remove, m)
		if k := [2]string{m.MemoryID, m.Relation}; !has[k] {
			has[k] = true
			m.EntityID = into
			add = append(add, m)
		}
	}
	return remove, add
}

// DeleteEntity removes an entity's mention edges, never memories. A tombstone
// keeps only hashed name keys, so extraction does not bring the name back.
func (s *Service) DeleteEntity(ctx context.Context, token, id string, in EntityScopeInput) (EntityDeleteResult, error) {
	out := EntityDeleteResult{ID: id}
	err := s.editEntities(ctx, token, in.Scope, id, "delete:entities", func(store namedEntityStore, g *entityGraph, actor string, now time.Time) error {
		e, err := errEntityNotFound(g, id)
		if err != nil {
			return err
		}
		remove, _ := movedMentions(g.mentions, id, "")
		if err = store.ReplaceEntityMentions(ctx, in.Scope, remove, nil); err != nil {
			return err
		}
		out.RemovedMentions = len(remove)
		return store.PutNamedEntity(ctx, in.Scope, NamedEntity{ID: id, Type: e.Type, Aliases: []string{}, Deleted: true, Keys: e.Keys, EditedBy: actor, CreatedAt: e.CreatedAt, UpdatedAt: now})
	})
	return out, err
}
