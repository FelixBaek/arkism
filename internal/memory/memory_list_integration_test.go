//go:build integration

package memory

import "testing"

func TestIntegrationMemoryList(t *testing.T) { exerciseMemoryList(t, isolatedPostgres(t)) }
