//go:build integration

package memory

import (
	"testing"
)

func TestIntegrationSearchRelation(t *testing.T) { exerciseSearchRelation(t, isolatedPostgres(t)) }
