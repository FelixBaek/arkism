package memory

import "context"

type maintenanceCascadeKey struct{}

func (s *Service) BeginSupersedeMaintenance(ctx context.Context, token, id string, in SupersedeInput) (MemoryMaintenanceStatus, error) {
	p, err := s.maintenanceOwner()
	if err != nil {
		return MemoryMaintenanceStatus{}, err
	}
	return p.maintenanceTransaction(ctx, func(tx *Postgres, _ int64, current *memoryMaintenance) (*memoryMaintenance, error) {
		if current != nil {
			return nil, ErrConflict
		}
		if err := maintenanceToken(ctx, tx, token); err != nil {
			return nil, err
		}
		job := &memoryMaintenance{ID: ID(), Kind: "stale", Scope: in.Scope}
		if _, err := s.maintenanceService(tx).Supersede(context.WithValue(ctx, maintenanceCascadeKey{}, job), token, id, in); err != nil {
			return nil, err
		}
		if job.Mark == nil {
			return nil, ErrUnavailable
		}
		return job, audit(ctx, tx, in.Scope, job.Actor, "begin:memory_maintenance", job.ID)
	})
}

var staleMaintenancePhases = []struct{ col, status string }{
	{colTopicDigests, digestActive}, {colTopicDigests, digestCandidate},
	{colUserDigests, digestActive}, {colUserDigests, digestCandidate},
	{colSkillVersions, "candidate"}, {colSkillVersions, "approved"}, {colSkillVersions, statusActive},
	{colObservations, obsActive}, {colObservations, obsProposed},
}

func advanceStaleMaintenance(ctx context.Context, p *Postgres, j *memoryMaintenance) (bool, error) {
	if j.Mark == nil {
		return false, ErrUnavailable
	}
	if j.Phase == len(staleMaintenancePhases) {
		refs, err := p.MemoryErasureBatch(ctx, j.Scope, j.Mark.Memory.ID, false, j.Cursor, memoryRelationErasureKind)
		if err != nil {
			return false, err
		}
		for _, ref := range refs {
			r, err := p.Get(ctx, "memory_relations", j.Scope, ref.ID)
			if err != nil {
				return false, err
			}
			if r.Kind == GraphSupports && r.Status == statusActive {
				if err = markStale(ctx, p, "memory_relations", j.Scope, r.ID, j.Mark, j.Actor, nil); err != nil {
					return false, err
				}
			}
			j.Cursor = ref.ID
			j.Scanned++
		}
		if len(refs) < sourceMemoryErasureBatch {
			return true, audit(ctx, p, j.Scope, j.Actor, "complete:memory_maintenance", j.ID)
		}
		return false, nil
	}
	if j.Phase < 0 || j.Phase > len(staleMaintenancePhases) {
		return false, ErrUnavailable
	}
	phase := staleMaintenancePhases[j.Phase]
	rows, err := p.StaleDependentsAfter(ctx, j.Scope, phase.col, phase.status, j.Mark.Memory.ID, j.Cursor, 100)
	if err != nil {
		return false, err
	}
	for _, r := range rows {
		j.Cursor = r.ID
		j.Scanned++
		switch phase.col {
		case colTopicDigests, colUserDigests:
			if r.Digest == nil || r.Kind == digestKindState || !namesMemory(r.Digest.Members, j.Mark.Memory.ID) {
				continue
			}
			err = markStale(ctx, p, phase.col, j.Scope, r.ID, j.Mark, j.Actor, func(r *Record) { r.Status, r.Text = digestStale, "" })
			if err == nil && phase.col == colUserDigests {
				err = requestUserDigest(ctx, p, j.Scope, j.Mark.At)
			}
		case colSkillVersions:
			if r.Kind != proposedSkillKind || !namesMemory(r.Relations, j.Mark.Memory.ID) {
				continue
			}
			err = markStale(ctx, p, phase.col, j.Scope, r.ID, j.Mark, j.Actor, nil)
		case colObservations:
			if r.Observation == nil || !namesMemory(r.Observation.SourceMemories, j.Mark.Memory.ID) {
				continue
			}
			err = markStale(ctx, p, phase.col, j.Scope, r.ID, j.Mark, j.Actor, func(r *Record) {
				o := *r.Observation
				o.History = appendRevision(o.History, DerivedRevision{Status: obsStale, Action: "stale", Text: r.Text, Citations: o.SourceMemories, Durability: o.Durability, DurabilityReason: o.DurabilityReason, Confidence: r.Confidence, Generator: r.Generator, By: j.Actor, At: j.Mark.At})
				r.Observation, r.Status = &o, obsStale
			})
		}
		if err != nil {
			return false, err
		}
	}
	if len(rows) < 100 {
		j.Phase++
		j.Cursor = ""
	}
	return false, nil
}
