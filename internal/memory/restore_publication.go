package memory

import (
	"context"
	"encoding/hex"
)

type RestoredRecoveryIdentity struct {
	RestoredIdentity string `json:"restored_identity"`
	RestoreManifest  string `json:"restore_manifest"`
}

// Read both values in one statement. This identifies the inspected metadata;
// it does not distinguish clones or fence writes after this read.
func (p *Postgres) RecoveryIdentity(ctx context.Context) (RestoredRecoveryIdentity, error) {
	var identity, manifest []byte
	err := p.q().QueryRow(ctx, `SELECT i.value,m.value FROM memory_storage_metadata i
CROSS JOIN memory_storage_metadata m WHERE i.key='storage_identity'
AND m.key='restore_review_pending'`).Scan(&identity, &manifest)
	if err != nil || len(identity) != 32 || !sourceHashPattern.Match(manifest) {
		return RestoredRecoveryIdentity{}, ErrUnavailable
	}
	return RestoredRecoveryIdentity{hex.EncodeToString(identity), string(manifest)}, nil
}

// A verified blob inventory is not evidence that later deletions were replayed.
// The canonical Postgres store persists this gate independently of file config.
type restoreReviewStore interface {
	RequireRestoreReview(context.Context, string) error
	CheckRestorePublication(context.Context) error
}

func (s *Service) CheckRestorePublication(ctx context.Context) error {
	if store, ok := s.Repo.(restoreReviewStore); ok {
		return store.CheckRestorePublication(ctx)
	}
	// Noncanonical test repositories do not have a restored database. Adoption
	// explicitly requires the capability and cannot silently skip persistence.
	return nil
}

func (p *Postgres) RequireRestoreReview(ctx context.Context, digest string) error {
	if !sourceHashPattern.MatchString(digest) || p == nil || p.pool == nil {
		return ErrInvalid
	}
	return p.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		view, ok := tx.(*Postgres)
		if !ok {
			return ErrUnavailable
		}
		tag, err := view.q().Exec(ctx, `INSERT INTO memory_storage_metadata(key,value) VALUES('restore_review_pending',$1) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value WHERE memory_storage_metadata.value=EXCLUDED.value`, []byte(digest))
		if err != nil {
			return pgErr(err)
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return view.requireNativeRestoreReviewTx(ctx, digest)
	})
}

func (p *Postgres) CheckRestorePublication(ctx context.Context) error {
	gate, err := p.recoveryWriteGate(ctx, "")
	if err != nil || gate.Gate.State != "open" {
		return ErrUnavailable
	}
	if err := p.checkRestorePublicationMarker(ctx); err != nil {
		return err
	}
	return p.checkNativeRestorePublication(ctx)
}

func (p *Postgres) checkRestorePublicationMarker(ctx context.Context) error {
	var pending bool
	err := p.q().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memory_storage_metadata WHERE key='restore_review_pending')`).Scan(&pending)
	if err != nil {
		return pgErr(err)
	}
	if pending {
		return ErrUnavailable
	}
	return nil
}
