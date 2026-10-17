package memory

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"
)

// captureWrite is server-owned and never decoded from an API request. It joins
// capture bookkeeping to the existing source/candidate transaction: there is no
// outer transaction, nested Create call, or replacement of the bound Tx/actor.
type captureWrite struct {
	key           string
	scope         Scope
	previous      *Record
	preparationID string
	prefixSources []SourceRef
	sourceSpanEnd int
	prepared      Record
	correction    bool
}

func newCaptureWrite(key string, scope Scope, previous *Record, c CaptureClaim) *captureWrite {
	revision := int64(0)
	if previous != nil {
		revision = previous.Revision
	}
	return &captureWrite{key: key, scope: scope, previous: previous, preparationID: "capture-preparation-" + ContentHash(key+"\n"+structuredText(revision)), sourceSpanEnd: utf8.RuneCountInString(c.Text), correction: c.Supersedes != nil}
}

func (w *captureWrite) prepareSource(ctx context.Context, tx Tx, source Record) error {
	if source.Scope != w.scope || source.ID == "" || !live(source) || source.ContentErased {
		return ErrConflict
	}
	ref := sourceReference(source, "original")
	if ref.Span == nil || w.sourceSpanEnd <= 0 || w.sourceSpanEnd > ref.Span.End {
		return ErrInvalid
	}
	ref.Span.End = w.sourceSpanEnd
	refs := append(append([]SourceRef(nil), w.prefixSources...), ref)
	return w.prepare(ctx, tx, refs, source.ID)
}

func (w *captureWrite) prepare(ctx context.Context, tx Tx, refs []SourceRef, sourceID string) error {
	if sourceID == "" || len(refs) == 0 {
		return ErrInvalid
	}
	old, err := tx.Get(ctx, "jobs", w.scope, w.preparationID)
	if err == nil {
		if old.Kind != "capture_preparation" || old.SourceEventID != sourceID || structuredText(old.Sources) != structuredText(refs) || (old.Status != "prepared" && old.Status != "done") {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	evidence := Record{Scope: w.scope, Sources: refs}
	if err := checkSources(ctx, tx, evidence); err != nil {
		return err
	}
	for _, ref := range refs {
		if err := tx.FenceSource(ctx, w.scope, ref.SourceID); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	return tx.Insert(ctx, "jobs", Record{ID: w.preparationID, Scope: w.scope, Kind: "capture_preparation", Status: "prepared", Revision: 1, Sources: refs, SourceEventID: sourceID, CreatedAt: now, UpdatedAt: now})
}

func (s *Service) loadCapturePreparation(ctx context.Context, token string, w *captureWrite) (Record, Record, error) {
	prepared, err := s.Repo.Get(ctx, "jobs", w.scope, w.preparationID)
	if err != nil {
		return Record{}, Record{}, err
	}
	if prepared.Kind != "capture_preparation" || prepared.Scope != w.scope || prepared.Revision < 1 || len(prepared.Sources) == 0 || prepared.SourceEventID == "" || (prepared.Status != "prepared" && prepared.Status != "done") {
		return Record{}, Record{}, ErrConflict
	}
	var source Record
	for _, ref := range prepared.Sources {
		r, e := s.Get(ctx, token, "sources", w.scope, ref.SourceID)
		if e != nil {
			return Record{}, Record{}, e
		}
		if !live(r) || r.ContentErased {
			return Record{}, Record{}, ErrConflict
		}
		if r.ID == prepared.SourceEventID {
			source = r
		}
	}
	if source.ID == "" {
		return Record{}, Record{}, ErrConflict
	}
	if err := checkSources(ctx, s.Repo, Record{Scope: w.scope, Sources: prepared.Sources}); err != nil {
		return Record{}, Record{}, err
	}
	return prepared, source, nil
}

// complete is invoked after the candidate insert, before that same transaction
// commits. A competing capture must win both the pointer and this preparation;
// a failed CAS rolls back the candidate, quota reservation, and all audit rows.
func (w *captureWrite) complete(ctx context.Context, tx Tx, candidate Record) error {
	if candidate.Scope != w.scope || candidate.Status != "candidate" || candidate.ID == "" || structuredText(candidate.Sources) != structuredText(w.prepared.Sources) {
		return ErrConflict
	}
	current, err := tx.Get(ctx, "jobs", w.scope, w.preparationID)
	if err != nil {
		return err
	}
	if current.Kind != "capture_preparation" || current.Status != "prepared" || current.Revision != w.prepared.Revision || structuredText(current.Sources) != structuredText(w.prepared.Sources) || current.SourceEventID != candidate.SourceEventID {
		return ErrConflict
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	pointer := Record{ID: w.key, Scope: w.scope, Kind: "capture_pointer", Status: "done", Revision: 1, Sources: current.Sources, Target: &Ref{Collection: "memory_candidates", ID: candidate.ID, Revision: candidate.Revision}, CreatedAt: now, UpdatedAt: now}
	if w.correction {
		pointer.Status = "correction_pending"
	}
	if w.previous != nil {
		pointer.Revision, pointer.CreatedAt, pointer.Sources = w.previous.Revision+1, w.previous.CreatedAt, w.previous.Sources
		err = tx.Replace(ctx, "jobs", pointer, w.previous.Revision)
	} else {
		err = tx.Insert(ctx, "jobs", pointer)
	}
	if err != nil {
		return err
	}
	current.Status, current.Target, current.UpdatedAt = "done", pointer.Target, now
	current.Revision++
	return tx.Replace(ctx, "jobs", current, w.prepared.Revision)
}
