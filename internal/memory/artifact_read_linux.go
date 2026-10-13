package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"syscall"
)

func (s *Service) ReadArtifact(ctx context.Context, token string, scope Scope, sourceID, id string) ([]byte, error) {
	if !validScope(scope) || !validRunLabel(sourceID, 256) || !validRunLabel(id, 256) {
		return nil, ErrInvalid
	}
	var actor Decision
	authorize := func(ctx context.Context) error {
		for _, target := range []struct{ action, id string }{{"read:sources", sourceID}, {"read:artifacts", id}} {
			d, e := s.authorize(ctx, token, target.action, scope, target.id, "")
			if e != nil {
				return e
			}
			if actor.PrincipalID == "" {
				actor = d
			} else if d.PrincipalID != actor.PrincipalID || d.Role != actor.Role {
				return ErrDenied
			}
		}
		return nil
	}
	if err := authorize(ctx); err != nil {
		return nil, err
	}
	load := func(ctx context.Context, tx Tx) (Record, error) {
		a, e := tx.Get(ctx, colArtifacts, scope, id)
		if e != nil {
			return Record{}, e
		}
		if !validArtifactRecord(a) || a.Scope != scope || a.Target.ID != sourceID {
			return Record{}, ErrNotFound
		}
		source, e := tx.Get(ctx, "sources", scope, sourceID)
		if e != nil {
			return Record{}, e
		}
		if source.OriginProduct == "synthetic-knowledge" && actor.Role != "human" {
			return Record{}, ErrDenied
		}
		if !live(source) {
			return Record{}, ErrNotFound
		}
		if source.Revision != a.Target.Revision {
			return Record{}, ErrConflict
		}
		if source.Sensitivity != a.Sensitivity {
			return Record{}, ErrUnavailable
		}
		return a, nil
	}
	var result []byte
	err := s.withBlobs(ctx, func(b *lockedBlobs) error {
		a, e := load(ctx, s.Repo)
		if e != nil {
			return e
		}
		content, e := b.readArtifactBytes(*a.Artifact)
		if e != nil {
			return ErrUnavailable
		}
		e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			current, e := load(ctx, tx)
			if e != nil {
				return e
			}
			if current.ArtifactGeneration != a.ArtifactGeneration || current.Revision != a.Revision || current.Kind != a.Kind || *current.Artifact != *a.Artifact || *current.Target != *a.Target {
				return ErrConflict
			}
			blob, e := tx.Get(ctx, colBlobs, blobScope(), a.Artifact.Digest)
			if e != nil {
				return ErrUnavailable
			}
			count, e := tx.BlobReferences(ctx, a.Artifact.Digest)
			if e != nil {
				return e
			}
			if blob.Kind != "blob" || blob.Status != statusActive || blob.Blob == nil || blob.Blob.Bytes != a.Artifact.Bytes || count < 1 || blob.Blob.References != count {
				return ErrUnavailable
			}
			if e = authorize(ctx); e != nil {
				return e
			}
			if e = tx.FenceSource(ctx, scope, sourceID); e != nil {
				return e
			}
			if e = audit(ctx, tx, scope, actor.PrincipalID, "read:sources", sourceID); e != nil {
				return e
			}
			return audit(ctx, tx, scope, actor.PrincipalID, "read:artifacts", id)
		})
		if e == nil {
			result = content
		}
		return e
	})
	if err != nil {
		if errors.Is(err, ErrInvalid) {
			err = ErrUnavailable
		}
		return nil, err
	}
	return result, nil
}

func (b *lockedBlobs) readArtifactBytes(data artifactMetadata) ([]byte, error) {
	if !sourceHashPattern.MatchString(data.Digest) || data.Bytes < 1 || data.Bytes > b.maxBytes {
		return nil, ErrInvalid
	}
	file, err := openPrivateBlob(b.root, data.Digest, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content := make([]byte, int(data.Bytes)+1)
	n, err := io.ReadFull(blobContextReader{b.ctx, file}, content)
	if err != io.ErrUnexpectedEOF || int64(n) != data.Bytes {
		return nil, ErrInvalid
	}
	content = content[:n]
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != data.Digest {
		return nil, ErrInvalid
	}
	return content, nil
}
