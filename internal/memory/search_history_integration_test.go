//go:build integration

package memory

import (
	"context"
	"testing"
)

func TestIntegrationSearchHistory(t *testing.T) { exerciseSearchHistory(t, isolatedPostgres(t)) }

func (t searchFenceRaceTx) FenceRelationMember(ctx context.Context, scope Scope, col, id string) error {
	if col == t.r.target.Collection && id == t.r.target.ID {
		t.r.wait(ctx)
	}
	return t.Tx.FenceRelationMember(ctx, scope, col, id)
}
