//go:build !linux

package memory

import "context"

func (s *Service) attachOriginal(context.Context, Decision, Scope, Ref, string, string, []byte) error {
	return ErrUnavailable
}
