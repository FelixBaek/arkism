//go:build integration

package memory

import "testing"

func TestIntegrationTrustLevels(t *testing.T) { exerciseTrustLevels(t, isolatedPostgres(t)) }
