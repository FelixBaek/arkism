//go:build integration

package memory

import "testing"

func TestIntegrationMemoryTags(t *testing.T) { exerciseMemoryTags(t, isolatedPostgres(t)) }
