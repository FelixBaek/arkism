//go:build integration

package memory

import "testing"

func TestIntegrationCandidateConfidence(t *testing.T) {
	exerciseCandidateConfidence(t, isolatedPostgres(t))
}
