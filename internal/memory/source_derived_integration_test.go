//go:build integration

package memory

import "testing"

func TestIntegrationSourceDerived(t *testing.T)      { exerciseSourceDerived(t, isolatedPostgres(t)) }
func TestIntegrationSourceDerivedCount(t *testing.T) { exerciseDerivedCount(t, isolatedPostgres(t)) }
func TestIntegrationSourceDerivedCountLimit(t *testing.T) {
	exerciseDerivedCountLimit(t, isolatedPostgres(t))
}
