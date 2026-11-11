package memory

import "context"

// Keep the admission record, not a later reread of a potentially changed record,
// bound to text retained by Search and subsequently shortened for the prompt.
func reflectMemoryRecordHash(r Record) string {
	normalizeMemoryProvenance(&r)
	r.MemoryReferenceFence = 0
	return ContentHash(structuredText(r))
}

// Each model boundary rechecks the first admitted record and its dependencies.
// A source-date fallback changes prompt metadata, never this original snapshot.
func (s *Service) verifyReflectMemories(ctx context.Context, token string, run *reflectRun) error {
	if len(run.memoryRecords) == 0 {
		return nil
	}
	in := reflectSearch(run.in, run.in.Query)
	actor, err := s.authorize(ctx, token, "search:memories", in.Scope, "", in.Query)
	if err != nil {
		return err
	}
	for ref, hash := range run.memoryRecords {
		if err := s.reflectMemoryReadable(ctx, token, in, actor, ref, hash); err != nil {
			return err
		}
	}
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return verifyReflectMemoriesTx(ctx, searchReadTx{Tx: tx}, in, run.memoryRecords)
	})
}

func (s *Service) reflectMemoryReadable(ctx context.Context, token string, in SearchInput, actor Decision, ref Ref, hash string) error {
	current, err := s.Repo.Get(ctx, ref.Collection, in.Scope, ref.ID)
	if err != nil {
		return err
	}
	if hash == "" || !reflectMemoryUnchanged(current, in.Scope, ref, hash) {
		return ErrConflict
	}
	h := HitFromRecord(ref.Collection, current)
	allowed, ok, err := s.authorizeSearchHit(ctx, token, in, h, Record{}, false, &ContextBundle{})
	if err != nil {
		return err
	}
	if !ok || allowed.PrincipalID != actor.PrincipalID || allowed.Role != actor.Role {
		return ErrDenied
	}
	return nil
}

func reflectMemoryUnchanged(current Record, scope Scope, ref Ref, hash string) bool {
	return current.Scope == scope && current.Revision == ref.Revision && !current.ContentErased && !current.PrivacyQuarantined && live(current) && reflectMemoryRecordHash(current) == hash
}

func verifyReflectMemoriesTx(ctx context.Context, checked searchReadTx, in SearchInput, snapshots map[Ref]string) error {
	for ref, hash := range snapshots {
		current, err := checked.Get(ctx, ref.Collection, in.Scope, ref.ID)
		if err != nil {
			return err
		}
		if !reflectMemoryUnchanged(current, in.Scope, ref, hash) || !in.SearchCriteria.matchesRecord(current) || ref.Collection == colMemories && current.ApprovedBy == "" {
			return ErrConflict
		}
		if err := verifySearchItemSources(ctx, checked, in, nil, ref.Collection, current); err != nil {
			return err
		}
	}
	return nil
}
