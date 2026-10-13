//go:build integration

package memory

import (
	"testing"
)

func TestIntegrationEffectiveAtLifecycle(t *testing.T) { exerciseEffectiveAt(t, isolatedPostgres(t)) }
