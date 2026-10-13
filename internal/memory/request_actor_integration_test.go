//go:build integration

package memory

import "testing"

func TestIntegrationRequestActorConsistency(t *testing.T) {
	for _, mode := range requestActorModes {
		for _, field := range []string{"principal", "role"} {
			t.Run(mode+"/"+field, func(t *testing.T) { exerciseRequestActor(t, isolatedPostgres(t), mode, field) })
		}
	}
}
