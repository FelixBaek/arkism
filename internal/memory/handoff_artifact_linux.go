package memory

import (
	"bytes"
	"context"
)

func (s *Service) withHandoffArtifactWriter(ctx context.Context, fn func(handoffArtifactWriter) error) error {
	return s.withBlobs(ctx, func(b *lockedBlobs) error {
		written := map[string]bool{}
		err := fn(func(ctx context.Context, tx Tx, scope Scope, source Ref, text string, actor Decision) (Record, error) {
			expected := ContentHash(text)
			written[expected] = true
			digest, size, err := b.put(newFullScanReader(bytes.NewReader([]byte(text))), expected)
			if err != nil {
				return Record{}, err
			}
			return attachArtifactMetadata(ctx, tx, scope, source, "tool_output", artifactMetadata{Digest: digest, MIME: "text/plain", Bytes: size}, actor)
		})
		if err != nil {
			// Existing GC fencing preserves referenced files even after an ambiguous
			// commit, and removes metadata-free writes after a confirmed rollback.
			for digest := range written {
				_ = b.collect(s.Repo, digest)
			}
		}
		return err
	})
}
