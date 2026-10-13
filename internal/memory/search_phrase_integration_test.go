//go:build integration

package memory

import "testing"

func TestIntegrationExactWholeKeyword(t *testing.T) {
	exerciseExactWholeKeyword(t, isolatedPostgres(t))
}
