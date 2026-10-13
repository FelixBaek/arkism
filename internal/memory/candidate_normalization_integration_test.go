//go:build integration

package memory

import "testing"

func TestIntegrationCandidateNormalization(t *testing.T) {
	exerciseCandidateNormalization(t, isolatedPostgres(t))
}
