//go:build !linux

package memory

import "context"

func (s *Service) ReadArtifact(context.Context, string, Scope, string, string) ([]byte, error) {
	return nil, ErrUnavailable
}

func (s *Service) persistTextArtifact(context.Context, string, CreateInput, Record, Decision, textArtifact) (Record, error) {
	return Record{}, ErrUnavailable
}

func (s *Service) erasePendingArtifacts(context.Context, string, Record, Decision, string) error {
	return ErrUnavailable
}

func (s *Service) CheckBlobStorage(context.Context) error {
	if s.BlobDirectory == "" {
		return nil
	}
	return ErrUnavailable
}

func (s *Service) CollectBlobGarbage(context.Context, string) (BlobGCResult, error) {
	return BlobGCResult{}, ErrUnavailable
}

func (s *Service) AdoptRestoredBlobs(context.Context, string) error { return ErrUnavailable }
