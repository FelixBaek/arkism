package memory

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// use the existing human-authorized privacy lifecycle.
// The serializable transaction covers discovery, quarantine and the job insert.
func (p *Postgres) discoverWorkPrivacyImpact(ctx context.Context, scope Scope, subject bool, impact *PrivacyImpact, limit int) error {
	evidence := impact.Records
	if evidence == nil {
		evidence = []Ref{}
	}
	refs, err := json.Marshal(evidence)
	if err != nil {
		return ErrInvalid
	}
	rows, err := p.selectRecords(ctx, colWorkCheckpoints, scope, `NOT COALESCE((doc->>'content_erased')::boolean,false) AND ($6 OR EXISTS (
 SELECT 1 FROM jsonb_array_elements(COALESCE(doc->'work_checkpoint'->'package'->'source_refs','[]'::jsonb)) w
 JOIN jsonb_array_elements($7::jsonb) evidence ON w->>'collection'=evidence->>'collection' AND w->>'id'=evidence->>'id'
 ))`, []any{subject, string(refs)}, "id ASC", limit+1)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		impact.Records = append(impact.Records, Ref{Collection: colWorkCheckpoints, ID: row.ID, Revision: row.Revision})
		ids = append(ids, row.ID)
	}
	if len(impact.Records) > limit {
		return ErrUnavailable
	}
	return p.appendWorkPrivacyReceipts(ctx, scope, ids, impact, limit)
}

func (p *Postgres) appendWorkPrivacyReceipts(ctx context.Context, scope Scope, ids []string, impact *PrivacyImpact, limit int) error {
	if len(ids) == 0 {
		return nil
	}
	receipts, err := p.selectRecords(ctx, colWorkCheckpointReceipts, scope, `doc->'work_checkpoint'->>'receipt_target_id'=ANY($6)`, []any{ids}, "id ASC", limit+1)
	if err != nil {
		return err
	}
	for _, row := range receipts {
		impact.Records = append(impact.Records, Ref{Collection: colWorkCheckpointReceipts, ID: row.ID, Revision: row.Revision})
	}
	if len(impact.Records) > limit {
		return ErrUnavailable
	}
	return nil
}

func eraseWorkPrivacyImpact(ctx context.Context, tx Tx, scope Scope, impact PrivacyImpact, requestID string) error {
	for _, ref := range impact.Records {
		if ref.Collection != colWorkCheckpoints && ref.Collection != colWorkCheckpointReceipts {
			continue
		}
		row, err := tx.Get(ctx, ref.Collection, scope, ref.ID)
		if err != nil {
			return err
		}
		if ref.Collection == colWorkCheckpoints {
			now := time.Now().UTC()
			receipt := Record{ErasureParentID: requestID, ID: workDeletionID(scope, row.ID), Scope: scope, Kind: workDeletionKind, Status: JobStatusDone, Revision: 1, CreatedAt: now, UpdatedAt: now, Target: &Ref{Collection: colWorkCheckpoints, ID: row.ID, Revision: row.Revision}}
			if err := insertIfAbsent(ctx, tx, colJobs, receipt); err != nil {
				return err
			}
		}
		if row.ContentErased && row.WorkCheckpoint == nil {
			continue
		}
		if !row.PrivacyQuarantined {
			return ErrConflict
		}
		// Keep only a tombstone. Neither request hashes nor package pointers survive.
		tombstone := Record{ID: row.ID, Scope: scope, Kind: row.Kind, Status: "deleted", Revision: row.Revision + 1,
			CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy, CreatedByType: row.CreatedByType, UpdatedAt: time.Now().UTC(), PrivacyQuarantined: true, ContentErased: true}
		if err := tx.Replace(ctx, ref.Collection, tombstone, row.Revision); err != nil {
			return err
		}
	}
	return nil
}

func verifyWorkPrivacyImpact(ctx context.Context, tx Tx, scope Scope, impact PrivacyImpact) error {
	for _, ref := range impact.Records {
		if ref.Collection != colWorkCheckpoints && ref.Collection != colWorkCheckpointReceipts {
			continue
		}
		row, err := tx.Get(ctx, ref.Collection, scope, ref.ID)
		// The tombstone is also the replay/recreation fence, so absence is not proof.
		if errors.Is(err, ErrNotFound) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if !row.ContentErased || !row.PrivacyQuarantined || row.Status != "deleted" || row.WorkCheckpoint != nil || row.Target != nil {
			return ErrConflict
		}
		if ref.Collection == colWorkCheckpoints {
			receipt, err := tx.Get(ctx, colJobs, scope, workDeletionID(scope, ref.ID))
			if err != nil {
				return err
			}
			if !validWorkDeletionReceipt(receipt, scope) {
				return ErrConflict
			}
		}

	}
	return nil
}

// Subject deletion covers unreferenced work too. This predicate read conflicts
// with a concurrent deletion job insertion under PostgreSQL SERIALIZABLE.
func (p *Postgres) FenceWorkSubject(ctx context.Context, scope Scope) error {
	if p.tx == nil || checkScope(scope) != nil {
		return ErrInvalid
	}
	rows, err := p.selectRecords(ctx, colJobs, scope, `kind=$6 AND doc->'privacy_deletion'->>'target_type'='subject' AND doc->'privacy_deletion'->>'state'<>'cancelled'`, []any{privacyDeletionKind}, "id ASC", 1)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		return ErrDenied
	}
	return nil
}

func fenceWorkSubject(ctx context.Context, tx Tx, scope Scope) error {
	store, ok := tx.(interface {
		FenceWorkSubject(context.Context, Scope) error
	})
	if !ok {
		return ErrUnavailable
	}
	return store.FenceWorkSubject(ctx, scope)
}

const workDeletionKind = "work_checkpoint_deletion"

func workDeletionID(scope Scope, id string) string {
	return "work-deletion-" + ContentHash(structuredText(scope)+"\n"+id)
}
func validWorkDeletionReceipt(r Record, scope Scope) bool {
	return r.Scope == scope && r.Kind == workDeletionKind && r.Status == JobStatusDone && r.Target != nil && r.Target.Collection == colWorkCheckpoints && workCheckpointIDPattern.MatchString(r.Target.ID) && r.Target.Revision >= 1 && r.ID == workDeletionID(scope, r.Target.ID)
}

func (p *Postgres) discoverSelectedWorkPrivacyImpact(ctx context.Context, scope Scope, ids []string, limit int) (PrivacyImpact, error) {
	impact := PrivacyImpact{}
	for _, id := range ids {
		if !workCheckpointIDPattern.MatchString(id) {
			return impact, ErrInvalid
		}
		r, err := p.Get(ctx, colWorkCheckpoints, scope, id)
		if err != nil {
			return impact, err
		}
		if r.Kind != kindWorkCheckpoint || (!isWorkCheckpoint(r) && !r.ContentErased) {
			return impact, ErrConflict
		}
		impact.Records = append(impact.Records, Ref{Collection: colWorkCheckpoints, ID: id, Revision: r.Revision})
	}
	if len(impact.Records) > limit {
		return PrivacyImpact{}, ErrUnavailable
	}
	if err := p.appendWorkPrivacyReceipts(ctx, scope, ids, &impact, limit); err != nil {
		return PrivacyImpact{}, err
	}
	return impact, nil
}
