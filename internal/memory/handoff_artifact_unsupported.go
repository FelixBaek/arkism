//go:build !linux && !windows

package memory

import "context"

func (s *Service) withHandoffArtifactWriter(context.Context, func(handoffArtifactWriter) error) error {
	return ErrUnavailable
}
