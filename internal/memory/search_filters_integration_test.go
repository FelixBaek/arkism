//go:build integration

package memory

import "testing"

func TestIntegrationSearchFilters(t *testing.T) { exerciseSearchFilters(t, isolatedPostgres(t)) }
