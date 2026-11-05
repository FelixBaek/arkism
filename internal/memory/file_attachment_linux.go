package memory

import (
	"bytes"
	"context"
)

// attachOriginal stores the uploaded bytes in the blob store and attaches them
// to the source that holds the file's text; the full secret scan runs on the way in.
func (s *Service) attachOriginal(ctx context.Context, d Decision, scope Scope, source Ref, kind, mime string, raw []byte) error {
	return s.withBlobs(ctx, func(b *lockedBlobs) error {
		data := artifactMetadata{Digest: ContentHash(string(raw)), MIME: mime, Bytes: int64(len(raw))}
		_, err := b.publishArtifact(s.Repo, data, bytes.NewReader(raw), func() (Record, error) {
			var out Record
			err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
				var e error
				out, e = attachArtifactMetadata(ctx, tx, scope, source, kind, data, d)
				return e
			})
			return out, err
		})
		return err
	})
}
