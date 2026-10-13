package memory

import (
	"context"
	"regexp"
)

var destinationPattern = regexp.MustCompile(`^external:[a-zA-Z0-9_.-]{1,64}$`)

// One gate for record content leaving the local trust boundary. Missing legacy
// classification is not permission to export; policy must inspect the exact data.
// Return the decision so multi-step callers can enforce a consistent actor.
func (s *Service) authorizeTransfer(ctx context.Context, token, action string, scope Scope, id, sensitivity, destination, content string, sources []SourceRef) (Decision, error) {
	if sensitivity == "" || !validSensitivity(sensitivity) || sensitivity == "restricted" || !safeTransfer(content) {
		return Decision{}, ErrDenied
	}
	if err := checkTransferSources(ctx, s.Repo, scope, sensitivity, sources); err != nil {
		return Decision{}, err
	}
	d, e := s.authorizeAt(ctx, token, action, scope, id, structuredText(map[string]string{"destination": destination, "sensitivity": sensitivity, "content": content}), destination)
	if e != nil {
		return Decision{}, e
	}
	if !d.ExternalTransfer || !d.ContentSafe {
		return Decision{}, ErrDenied
	}
	return d, nil
}

func checkTransferSources(ctx context.Context, tx Tx, scope Scope, sensitivity string, sources []SourceRef) error {
	// A derived record cannot declassify its evidence merely by claiming a
	// lower sensitivity. Explicit declassification is not implemented.
	rows, err := sourceMetadata(ctx, tx, scope, sources)
	if err != nil {
		return err
	}
	for _, source := range rows {
		if source.Sensitivity == "" || !validSensitivity(source.Sensitivity) || source.Sensitivity == "restricted" || source.Sensitivity == "private" && sensitivity != "private" || source.Sensitivity == "internal" && sensitivity == "public" {
			return ErrDenied
		}
	}
	return nil
}
