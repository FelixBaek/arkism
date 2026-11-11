package memory

import "context"

// A tool summary retains its first admitted model and each cited record. The
// model's revision alone cannot detect a later change to its citations.
type reflectModelSnapshot struct {
	modelHash string
	citations map[Ref]string
}

func reflectModelUsable(r Record) bool {
	return live(r) && !r.ContentErased && !r.PrivacyQuarantined && r.MentalModel != nil && r.MentalModel.Active != nil
}

func (s *Service) keepReflectMentalModel(ctx context.Context, token string, run *reflectRun, view MentalModelView) error {
	if _, exists := run.modelRecords[view.ID]; exists {
		return s.verifyReflectMentalModels(ctx, token, run)
	}
	r, err := s.mentalModelRead(ctx, token, run.in.Scope, view.ID)
	if err != nil {
		return err
	}
	if r.Scope != run.in.Scope || !reflectModelUsable(r) || r.Revision != view.Revision || r.MentalModel.Title != view.Title || structuredText(r.MentalModel.Active) != structuredText(view.Active) {
		return ErrConflict
	}
	snapshot := reflectModelSnapshot{modelHash: reflectMemoryRecordHash(r), citations: map[Ref]string{}}
	for _, ref := range view.Active.Citations {
		cited, err := s.Repo.Get(ctx, ref.Collection, run.in.Scope, ref.ID)
		if err != nil {
			return err
		}
		if cited.Revision != ref.Revision {
			return ErrConflict
		}
		snapshot.citations[ref] = reflectMemoryRecordHash(cited)
	}
	if run.modelRecords == nil {
		run.modelRecords = map[string]reflectModelSnapshot{}
	}
	run.modelRecords[view.ID] = snapshot
	return s.verifyReflectMentalModels(ctx, token, run)
}

func (s *Service) verifyReflectMentalModels(ctx context.Context, token string, run *reflectRun) error {
	if len(run.modelRecords) == 0 {
		return nil
	}
	for id, snapshot := range run.modelRecords {
		if err := s.authorizeReflectMentalModel(ctx, token, run.in.Scope, id, snapshot); err != nil {
			return err
		}
	}
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		checked := searchReadTx{Tx: tx}
		for id, snapshot := range run.modelRecords {
			if err := verifyReflectMentalModelTx(ctx, checked, run.in.Scope, id, snapshot); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) authorizeReflectMentalModel(ctx context.Context, token string, scope Scope, id string, snapshot reflectModelSnapshot) error {
	r, err := s.mentalModelRead(ctx, token, scope, id)
	if err != nil {
		return err
	}
	if r.Scope != scope || !reflectModelUsable(r) || reflectMemoryRecordHash(r) != snapshot.modelHash {
		return ErrConflict
	}
	view := s.mentalModelView(ctx, r)
	ok, err := s.mentalModelTransferable(ctx, token, scope, view)
	if err != nil {
		return err
	}
	if !ok {
		return ErrDenied
	}
	return nil
}

func verifyReflectMentalModelTx(ctx context.Context, checked searchReadTx, scope Scope, id string, snapshot reflectModelSnapshot) error {
	r, err := checked.Get(ctx, colMentalModels, scope, id)
	if err != nil {
		return err
	}
	if r.Scope != scope || !reflectModelUsable(r) || reflectMemoryRecordHash(r) != snapshot.modelHash {
		return ErrConflict
	}
	for ref, hash := range snapshot.citations {
		if err := verifyReflectModelCitation(ctx, checked, scope, ref, hash); err != nil {
			return err
		}
	}
	return nil
}

func verifyReflectModelCitation(ctx context.Context, checked searchReadTx, scope Scope, ref Ref, hash string) error {
	cited, err := checked.Get(ctx, ref.Collection, scope, ref.ID)
	if err != nil {
		return err
	}
	if cited.Scope != scope || !live(cited) || cited.ContentErased || cited.PrivacyQuarantined || reflectMemoryRecordHash(cited) != hash {
		return ErrConflict
	}
	if ref.Collection != colMemories && ref.Collection != colObservations || ref.Collection == colMemories && cited.ApprovedBy == "" {
		return ErrConflict
	}
	return verifySearchItemSources(ctx, checked, SearchInput{Scope: scope, Destination: wikiDestination}, nil, ref.Collection, cited)
}

// Every prompt and response boundary validates all retained evidence classes.
func (s *Service) verifyReflectEvidence(ctx context.Context, token string, run *reflectRun) error {
	if err := s.verifyReflectMentalModels(ctx, token, run); err != nil {
		return err
	}
	if err := s.verifyReflectMemories(ctx, token, run); err != nil {
		return err
	}
	return s.verifyReflectSources(ctx, token, run)
}
