//go:build integration

package memory

import "testing"

func TestIntegrationBatchBoundActor(t *testing.T) {
	for _, field := range []string{"none", "principal", "role"} {
		t.Run(field, func(t *testing.T) { exerciseBatchBoundActor(t, isolatedPostgres(t), field) })
	}
}
func TestIntegrationFileBoundActor(t *testing.T) {
	for _, field := range []string{"principal", "role"} {
		t.Run(field, func(t *testing.T) { exerciseFileBoundActor(t, isolatedPostgres(t), field) })
	}
}
