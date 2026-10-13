//go:build integration

package memory

import "testing"

func TestIntegrationAuditRequest(t *testing.T) { exerciseAuditRequest(t, isolatedPostgres(t)) }
