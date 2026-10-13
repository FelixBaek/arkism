//go:build integration

package memory

import "testing"

func TestIntegrationCaptureLoop(t *testing.T) { exerciseCapture(t, isolatedPostgres(t)) }
