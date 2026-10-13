package memory

import (
	"context"
	"errors"
	"time"
)

const colBlobs = "blobs"
const colArtifacts = "artifacts"

type blobMetadata struct {
	Bytes      int64 `bson:"bytes"`
	References int64 `bson:"references"`
}
type artifactMetadata struct {
	Digest string `bson:"digest"`
	MIME   string `bson:"mime"`
	Bytes  int64  `bson:"bytes"`
}

// Internal bookkeeping only; no public collection or authority is attached to it.
func blobScope() Scope { return Scope{WorkspaceID: "__memory_blobs__", ProjectID: "__memory_blobs__"} }
func artifactID(scope Scope, source, digest string) string {
	return "artifact-" + ContentHash(structuredText(scope)+"\n"+source+"\n"+digest)
}
func validArtifact(kind string, data artifactMetadata) bool {
	if !sourceHashPattern.MatchString(data.Digest) || data.Bytes < 1 || data.Bytes > 200<<20 {
		return false
	}
	switch data.MIME {
	case "text/plain":
		return kind == "code" || kind == "ocr" || kind == "tool_output"
	case "application/json":
		return kind == "tool_output"
	case "application/pdf":
		return kind == "pdf"
	case "image/png", "image/jpeg", "image/webp":
		return kind == "image" || kind == "screenshot_region"
	case "audio/mpeg", "audio/wav", "audio/ogg":
		return kind == "audio"
	}
	return false
}
func validArtifactRecord(r Record) bool {
	return sourceHashPattern.MatchString(r.ArtifactGeneration) && validScope(r.Scope) && r.Revision > 0 && r.Status == statusActive && r.Artifact != nil && validArtifact(r.Kind, *r.Artifact) && r.Target != nil && r.Target.Collection == "sources" && r.Target.Revision > 0 && len(r.Sources) == 1 && r.Sources[0].Span == nil && r.Sources[0].Locator == "original" && r.Target.ID == r.Sources[0].SourceID && validRunLabel(r.Sources[0].SourceID, 256) && r.ID == artifactID(r.Scope, r.Sources[0].SourceID, r.Artifact.Digest)
}

func validArtifactActor(actor Decision, scope Scope) bool {
	return actor.Allowed && actor.Scope == scope && (actor.Role == "human" || actor.Role == "service" || actor.Role == "pipeline") && validRunLabel(actor.PrincipalID, 256) && !containsSecret(actor.PrincipalID)
}

func attachArtifactMetadata(ctx context.Context, tx Tx, scope Scope, source Ref, kind string, data artifactMetadata, actor Decision) (Record, error) {
	if !validScope(scope) || containsSecret(structuredText(scope)) || containsSecret(source.ID) || source.Collection != "sources" || !validRunLabel(source.ID, 256) || source.Revision < 1 || !validArtifact(kind, data) {
		return Record{}, ErrInvalid
	}
	if !validArtifactActor(actor, scope) {
		return Record{}, ErrDenied
	}
	current, err := tx.Get(ctx, "sources", scope, source.ID)
	if err != nil {
		return Record{}, err
	}
	if !live(current) || current.Revision != source.Revision {
		return Record{}, ErrConflict
	}
	if err = tx.FenceSource(ctx, scope, source.ID); err != nil {
		return Record{}, err
	}
	id := artifactID(scope, source.ID, data.Digest)
	previous, err := tx.Get(ctx, colArtifacts, scope, id)
	duplicate := err == nil
	if duplicate {
		if !validArtifactRecord(previous) || *previous.Artifact != data || previous.Kind != kind || *previous.Target != source {
			return Record{}, ErrConflict
		}
	} else if !errors.Is(err, ErrNotFound) {
		return Record{}, err
	}
	blob, err := tx.Get(ctx, colBlobs, blobScope(), data.Digest)
	fresh := errors.Is(err, ErrNotFound)
	if err != nil && !fresh {
		return Record{}, err
	}
	count, err := tx.BlobReferences(ctx, data.Digest)
	if err != nil {
		return Record{}, err
	}
	if duplicate {
		if fresh || blob.Kind != "blob" || blob.Status != statusActive || blob.Blob == nil || blob.Blob.Bytes != data.Bytes || blob.Blob.References < 1 || blob.Blob.References != count {
			return Record{}, ErrUnavailable
		}
		return previous, audit(ctx, tx, scope, actor.PrincipalID, "artifact:duplicate", id)
	}
	now := time.Now().UTC()
	if fresh {
		if count != 0 {
			return Record{}, ErrUnavailable
		}
		blob = Record{ID: data.Digest, Scope: blobScope(), Kind: "blob", Status: statusActive, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: "memoryd", CreatedByType: "service", Blob: &blobMetadata{Bytes: data.Bytes, References: 1}}
		err = tx.Insert(ctx, colBlobs, blob)
	} else {
		if blob.Kind != "blob" || blob.Status != statusActive || blob.Blob == nil || blob.Blob.Bytes != data.Bytes || blob.Blob.References < 0 || blob.Blob.References != count {
			return Record{}, ErrUnavailable
		}
		meta := *blob.Blob
		meta.References++
		if meta.References <= 0 {
			return Record{}, ErrUnavailable
		}
		blob.Blob = &meta
		blob.Revision++
		blob.UpdatedAt = now
		err = tx.Replace(ctx, colBlobs, blob, blob.Revision-1)
	}
	if err != nil {
		return Record{}, err
	}
	result := Record{ArtifactGeneration: ContentHash(ID()), ID: id, Scope: scope, Kind: kind, Status: statusActive, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role, Sensitivity: current.Sensitivity, Generator: current.Generator, OriginProduct: current.OriginProduct, SourceRef: current.SourceRef, CollectedAt: current.CollectedAt, Sources: []SourceRef{{SourceID: current.ID, Locator: "original"}}, Target: &source, Artifact: &data}
	if err = tx.Insert(ctx, colArtifacts, result); err != nil {
		return Record{}, err
	}
	if err = audit(ctx, tx, scope, actor.PrincipalID, "artifact:attach", id); err != nil {
		return Record{}, err
	}
	return tx.Get(ctx, colArtifacts, scope, id)
}

func detachArtifactMetadata(ctx context.Context, tx Tx, expected Record, actor string) error {
	if !validArtifactRecord(expected) || !validRunLabel(actor, 256) || containsSecret(actor) {
		return ErrInvalid
	}
	current, err := tx.Get(ctx, colArtifacts, expected.Scope, expected.ID)
	if errors.Is(err, ErrNotFound) {
		return nil
	} // Idempotent worker retry with its saved identity.
	if err != nil {
		return err
	}
	if !validArtifactRecord(current) || current.ArtifactGeneration != expected.ArtifactGeneration || current.Revision != expected.Revision || *current.Artifact != *expected.Artifact {
		return ErrConflict
	}
	blob, err := tx.Get(ctx, colBlobs, blobScope(), current.Artifact.Digest)
	if err != nil {
		return err
	}
	count, err := tx.BlobReferences(ctx, current.Artifact.Digest)
	if err != nil {
		return err
	}
	if blob.Kind != "blob" || blob.Status != statusActive || blob.Blob == nil || blob.Blob.References < 1 || blob.Blob.References != count || blob.Blob.Bytes != current.Artifact.Bytes {
		return ErrUnavailable
	}
	meta := *blob.Blob
	meta.References--
	blob.Blob = &meta
	blob.Revision++
	blob.UpdatedAt = time.Now().UTC()
	if err = tx.Replace(ctx, colBlobs, blob, blob.Revision-1); err != nil {
		return err
	}
	if err = tx.Delete(ctx, colArtifacts, current.Scope, current.ID, current.Revision); err != nil {
		return err
	}
	return audit(ctx, tx, current.Scope, actor, "artifact:detach", current.ID)
}
