package memory

import (
	"bytes"
	"context"
)

func (s *Service) persistTextArtifact(ctx context.Context, token string, in CreateInput, r Record, d Decision, file textArtifact) (Record, error) {
	var result Record
	err := s.withBlobs(ctx, func(b *lockedBlobs) error {
		var err error
		result, err = b.ingestSource(s, token, in, r, d, file.metadata, bytes.NewReader(file.content))
		return err
	})
	return result, err
}
