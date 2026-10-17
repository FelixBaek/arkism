package memory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"
)

var idempotencyPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,128}$`)

type sourceArtifact struct {
	Kind string
	Data artifactMetadata
}

func checkSourceArtifact(in CreateInput, proposed Record, decision Decision, file sourceArtifact) error {
	if !validArtifactActor(decision, in.Scope) || proposed.Scope != in.Scope || proposed.CreatedBy != decision.PrincipalID || proposed.CreatedByType != decision.Role {
		return ErrDenied
	}
	if !validArtifact(file.Kind, file.Data) || proposed.Inspection == nil || proposed.Text != in.Text || proposed.Kind != in.Kind || proposed.Sensitivity != in.Sensitivity {
		return ErrInvalid
	}
	return nil
}

// errSourceOtherProvenance: a live source already holds this exact text in the
// scope, saved with other provenance (kind, origin, generator...). Capture reads
// it as a duplicate; every other source conflict stays a plain ErrConflict.
var errSourceOtherProvenance = fmt.Errorf("%w: same text is saved with other provenance", ErrConflict)

// errSourceDeleted: the digest still points at a deleted or erased source, so
// the text can never be saved again in this scope (it stays an ErrConflict).
var errSourceDeleted = fmt.Errorf("%w: the source with this text was deleted", ErrConflict)

// Registration pointers contain hashes and references only. Keeping them after
// source deletion prevents the same digest from resurrecting an old source ID.
func (s *Service) createSource(ctx context.Context, token string, in CreateInput, proposed Record, decision Decision, file *sourceArtifact) (Record, error) {
	ctx = bindRequestActor(ctx, decision)
	headers, normalized, err := s.validateArchiveMessageRefs(ctx, token, in.Scope, proposed.ArchiveMessages, "", "")
	if err != nil {
		return Record{}, err
	}
	proposed.ArchiveMessages = normalized
	actor := decision.PrincipalID
	if file != nil {
		if err := checkSourceArtifact(in, proposed, decision, *file); err != nil {
			return Record{}, err
		}
	}
	if proposed.ExpiresAt != nil {
		normalized := proposed.ExpiresAt.UTC().Truncate(time.Millisecond)
		proposed.ExpiresAt = &normalized
	}
	proposed.ContentHash = ContentHash(proposed.Text)
	digestID := "source-digest-" + ContentHash(structuredText(in.Scope)+"\n"+proposed.ContentHash)
	receiptID := "source-receipt-" + ContentHash(structuredText(in.Scope)+"\n"+actor+"\n"+in.IdempotencyKey)
	requestHash := ContentHash(structuredText(in))
	if file != nil {
		digestID = "source-digest-" + ContentHash(structuredText(in.Scope)+"\nsource-file-v1\n"+proposed.ContentHash+"\n"+file.Data.Digest)
		requestHash = ContentHash(structuredText(in) + "\nsource-file-v1\n" + structuredText(file))
	}
	attach := func(ctx context.Context, tx Tx, source Record, existing bool) error {
		if in.capture != nil {
			if err := in.capture.prepareSource(ctx, tx, source); err != nil {
				return err
			}
		}
		if file == nil {
			return nil
		}
		if existing {
			// A receipt/pointer is not permission to recreate a missing reference.
			if _, err := tx.Get(ctx, colArtifacts, in.Scope, artifactID(in.Scope, source.ID, file.Data.Digest)); err != nil {
				if errors.Is(err, ErrNotFound) {
					return ErrUnavailable
				}
				return err
			}
		}
		_, err := attachArtifactMetadata(ctx, tx, in.Scope, Ref{Collection: "sources", ID: source.ID, Revision: source.Revision}, file.Kind, file.Data, decision)
		return err
	}
	var result Record
	var replay bool
	// A concurrent unique-ID insert surfaces as a conflict (Postgres 23505).
	// Re-read in a fresh transaction once; never ignore conflicts or replace data.
	for attempt := 0; attempt < 2; attempt++ {
		result = Record{}
		replay = false
		err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			result = Record{}
			replay = false
			if e := s.fenceSourceArchives(ctx, tx, headers); e != nil {
				return e
			}
			if in.IdempotencyKey != "" {
				receipt, e := tx.Get(ctx, "jobs", in.Scope, receiptID)
				if e == nil {
					if receipt.Kind != "source_receipt" || receipt.RequestHash != requestHash || receipt.Target == nil {
						return ErrConflict
					}
					saved, e := tx.Get(ctx, "sources", in.Scope, receipt.Target.ID)
					if errors.Is(e, ErrNotFound) {
						return ErrConflict
					}
					if e != nil {
						return e
					}
					if !live(saved) || saved.Revision != receipt.Target.Revision {
						return ErrConflict
					}
					saved.Duplicate = receipt.Duplicate
					result = saved
					replay = true
					if e = attach(ctx, tx, saved, true); e != nil {
						return e
					}
					return audit(ctx, tx, in.Scope, actor, "ingest:replay", saved.ID)
				}
				if !errors.Is(e, ErrNotFound) {
					return e
				}
			}
			pointer, e := tx.Get(ctx, "jobs", in.Scope, digestID)
			if e == nil {
				if pointer.Kind != "source_digest" || pointer.Target == nil {
					return ErrConflict
				}
				saved, e := tx.Get(ctx, "sources", in.Scope, pointer.Target.ID)
				if errors.Is(e, ErrNotFound) {
					return errSourceDeleted
				}
				if e != nil {
					return e
				}
				if !live(saved) || saved.ContentErased {
					return errSourceDeleted
				}
				if saved.Revision != pointer.Target.Revision || saved.ContentHash != proposed.ContentHash {
					return ErrConflict
				}
				if sourceTrustLevel(saved.TrustLevel) != sourceTrustLevel(proposed.TrustLevel) || saved.Sensitivity != proposed.Sensitivity || saved.Kind != proposed.Kind || saved.OriginProduct != proposed.OriginProduct || saved.SourceRef != proposed.SourceRef || !sameSourceArchiveMessages(saved.ArchiveMessages, proposed.ArchiveMessages) || structuredText(saved.Generator) != structuredText(proposed.Generator) || structuredText(saved.ExpiresAt) != structuredText(proposed.ExpiresAt) || structuredText(saved.Relations) != structuredText(proposed.Relations) {
					return errSourceOtherProvenance
				}
				saved.Duplicate = true
				result = saved
			} else if errors.Is(e, ErrNotFound) {
				if e = checkRelations(ctx, tx, proposed); e != nil {
					return e
				}
				if e = tx.Insert(ctx, "sources", proposed); e != nil {
					return e
				}
				// Return the stored representation (stored timestamps and normalized keywords),
				// so a later retry can return precisely the same JSON without copying bodies.
				result, e = tx.Get(ctx, "sources", in.Scope, proposed.ID)
				if e != nil {
					return e
				}
				pointer = Record{ID: digestID, Scope: in.Scope, Kind: "source_digest", Status: "done", Revision: 1, ContentHash: proposed.ContentHash, Target: &Ref{Collection: "sources", ID: result.ID, Revision: result.Revision}, CreatedAt: proposed.CreatedAt, UpdatedAt: proposed.CreatedAt, CreatedBy: actor}
				if e = tx.Insert(ctx, "jobs", pointer); e != nil {
					return e
				}
			} else {
				return e
			}
			if e = attach(ctx, tx, result, result.Duplicate); e != nil {
				return e
			}
			if in.IdempotencyKey != "" {
				receipt := Record{ID: receiptID, Scope: in.Scope, Kind: "source_receipt", Status: "done", Revision: 1, RequestHash: requestHash, Duplicate: result.Duplicate, Target: &Ref{Collection: "sources", ID: result.ID, Revision: result.Revision}, CreatedAt: proposed.CreatedAt, UpdatedAt: proposed.CreatedAt, CreatedBy: actor}
				if e = tx.Insert(ctx, "jobs", receipt); e != nil {
					return e
				}
			}
			if proposed.Inspection.Redacted || proposed.Inspection.PersonalData {
				if e = audit(ctx, tx, in.Scope, actor, "inspection:processed:"+inspectionVersion, result.ID); e != nil {
					return e
				}
			}
			action := "create:sources"
			if result.Duplicate {
				action = "ingest:duplicate"
			}
			return audit(ctx, tx, in.Scope, actor, action, result.ID)
		})
		if !errors.Is(err, ErrConflict) {
			break
		}
	}
	if err != nil {
		return Record{}, err
	}
	if result.Duplicate || replay {
		reader, err := s.authorize(ctx, token, "read:sources", in.Scope, result.ID, structuredText(result))
		if err != nil {
			return Record{}, err
		}
		if reader.PrincipalID != decision.PrincipalID || reader.Role != decision.Role {
			return Record{}, ErrDenied
		}
		// The read policy authorized this snapshot, not later source contents.
		// Registration already committed; this transaction only finalizes reuse.
		err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			current, err := tx.Get(ctx, "sources", in.Scope, result.ID)
			if errors.Is(err, ErrNotFound) {
				return ErrConflict
			}
			if err != nil {
				return err
			}
			current.Duplicate = result.Duplicate // Response-only receipt flag.
			if current.Scope != in.Scope || !live(current) || structuredText(current) != structuredText(result) {
				return ErrConflict
			}
			if err = tx.FenceSource(ctx, in.Scope, result.ID); err != nil {
				return err
			}
			return audit(ctx, tx, in.Scope, actor, "read:sources:reuse", result.ID)
		})
		if err != nil {
			return Record{}, err
		}
	}
	return result, nil
}

// Server-owned, body-free deletion receipt; no audit-reader DB role is needed.
func sourceDeletionID(scope Scope, id string) string {
	return "source-deletion-" + ContentHash(structuredText(scope)+"\n"+id)
}
func sourceDeletion(ctx context.Context, tx Tx, scope Scope, id string) (time.Time, error) {
	r, err := tx.Get(ctx, "jobs", scope, sourceDeletionID(scope, id))
	if err != nil {
		return time.Time{}, err
	}
	if r.Kind != "source_deletion" || r.Status != "done" || r.Target == nil || r.Target.Collection != "sources" || r.Target.ID != id || r.Target.Revision < 1 || r.CreatedAt.IsZero() || r.CreatedAt.After(time.Now()) {
		return time.Time{}, ErrUnavailable
	}
	return r.CreatedAt, nil
}
