package memory

import (
	"context"
	"errors"
)

// This companion does not amend the six-field native recovery DTO. A restored
// copy is rebound only by ReconcileDynamicsLoadRecovery.
type DynamicsLoadRecoveryView struct {
	Protocol            string
	Scope               Scope
	StorageIdentity     string
	ConfigSHA256        string
	CoordinatorRevision int64
	QueueDigest         string
	Head                int64
	Tail                int64
	Count               int64
	ActiveBytes         int64
	RepairStartNS       *int64
}

func dynamicsRecoveryViewTx(ctx context.Context, tx Tx, scope Scope) (DynamicsLoadRecoveryView, error) {
	c, err := dynamicsLockCoordinator(ctx, tx, scope)
	if err != nil {
		return DynamicsLoadRecoveryView{}, err
	}
	p := c.DynamicsLoad
	q, ok := tx.(dynamicsLoadQueries)
	if !ok {
		return DynamicsLoadRecoveryView{}, ErrUnavailable
	}
	identity, err := q.BlobStorageIdentity(ctx)
	if err != nil {
		return DynamicsLoadRecoveryView{}, err
	}
	// Bound active pages using the persisted count; retention history is retained
	// and accounted independently. Refuse corrupt cardinalities before allocation.
	if p.Head < 0 || p.Tail < p.Head || p.Count != p.Tail-p.Head || p.Count > 500 {
		return DynamicsLoadRecoveryView{}, ErrConflict
	}
	raw, err := storeMarshal(c)
	if err != nil {
		return DynamicsLoadRecoveryView{}, err
	}
	digest, err := dynamicsRecoveryQueueDigestTx(ctx, tx, scope, p, string(raw))
	if err != nil {
		return DynamicsLoadRecoveryView{}, err
	}
	return DynamicsLoadRecoveryView{Protocol: dynamicsLoadProtocol, Scope: scope, StorageIdentity: identity, ConfigSHA256: p.ConfigSHA256, CoordinatorRevision: c.Revision, QueueDigest: digest, Head: p.Head, Tail: p.Tail, Count: p.Count, ActiveBytes: p.ActiveBytes, RepairStartNS: p.RepairStartNS}, nil
}
func (s *Service) ReadDynamicsLoadRecovery(ctx context.Context, token string, scope Scope) (DynamicsLoadRecoveryView, error) {
	ctx, err := s.nativeActor(ctx, token, scope, "read:jobs")
	if err != nil {
		return DynamicsLoadRecoveryView{}, err
	}
	var out DynamicsLoadRecoveryView
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		out, e = dynamicsRecoveryViewTx(ctx, tx, scope)
		return e
	})
	return out, err
}

// VerifyDynamicsLoadRecovery is read-only and never clears native/general
// markers, changes queue order, or manufactures a repair-origin timestamp.
func (s *Service) VerifyDynamicsLoadRecovery(ctx context.Context, token string, expected DynamicsLoadRecoveryView) error {
	actual, err := s.ReadDynamicsLoadRecovery(ctx, token, expected.Scope)
	if err != nil {
		return err
	}
	if actual.Protocol != expected.Protocol || actual.StorageIdentity != expected.StorageIdentity || actual.ConfigSHA256 != expected.ConfigSHA256 || actual.CoordinatorRevision != expected.CoordinatorRevision || actual.QueueDigest != expected.QueueDigest {
		return ErrConflict
	}
	return nil
}
func dynamicsCoordinatedCaptureTx(ctx context.Context, tx Tx, scope Scope) error {
	if _, canonical := tx.(dynamicsLoadQueries); !canonical {
		return nil
	}
	_, err := dynamicsLockCoordinator(ctx, tx, scope)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return ErrDynamicsRuntimeBinding
}

func dynamicsRecoveryQueueDigestTx(ctx context.Context, tx Tx, scope Scope, p *DynamicsLoadPayload, coordinator string) (string, error) {
	q := tx.(dynamicsLoadQueries)
	commitments := []string{coordinator}
	active := int64(0)
	for ordinal := p.Head; ordinal < p.Tail; {
		ordinal++
		row, e := q.DynamicsLoadLock(ctx, scope, dynamicsEntryID(scope, ordinal))
		if e != nil {
			return "", e
		}
		if e = dynamicsValidateRow(row, scope, "entry"); e != nil {
			return "", e
		}
		ep := row.DynamicsLoad
		if ep.Ordinal != ordinal || ep.ConfigSHA256 != p.ConfigSHA256 || ep.StorageIdentity != p.StorageIdentity || ep.EntryBytes < 1 {
			return "", ErrConflict
		}
		if ep.EntryBytes > p.ActiveBytes-active {
			return "", ErrConflict
		}
		active += ep.EntryBytes
		capture, e := nativeGetTx(ctx, tx, scope, ep.Capture.ID, "capture")
		if e != nil {
			return "", e
		}
		// Applied capture while still outstanding is an unknown commit/recovery
		// condition. Do not invent another successful queue apply.
		if capture.NativeDynamics.Receipt != nil {
			return "", ErrConflict
		}
		entryRaw, e := storeMarshal(row)
		if e != nil {
			return "", e
		}
		captureRaw, e := storeMarshal(capture)
		if e != nil {
			return "", e
		}
		commitments = append(commitments, string(entryRaw), string(captureRaw))
	}
	if active != p.ActiveBytes {
		return "", ErrConflict
	}
	return ContentHash(structuredText(commitments)), nil
}
