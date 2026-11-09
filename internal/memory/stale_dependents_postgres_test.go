package memory

import (
	"context"
	"fmt"
	"testing"
)

func TestStaleDependentPagesOnlyVisitMatchingRecords(t *testing.T) {
	p := isolatedPostgres(t)
	scope := Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u"}
	other := scope
	other.UserID = "other"
	for _, col := range []string{colTopicDigests, colUserDigests, colSkillVersions, colObservations} {
		seedStaleDependents(t, p, col, scope, other)
		if err := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
			return markStaleDependents(t, ctx, tx, col, scope)
		}); err != nil {
			t.Fatal(err)
		}
		rows, err := p.StatusRecordsAfter(testCtx, scope, col, "active", "", 1000)
		if err != nil || len(rows) != 102 {
			t.Fatalf("%s unrelated rows changed: %d %v", col, len(rows), err)
		}
	}
}

// seedStaleDependents writes 205 rows in col, every other one referencing the
// replaced memory, plus one foreign-scope copy of the first.
func seedStaleDependents(t *testing.T, p *Postgres, col string, scope, other Scope) {
	t.Helper()
	for i := 0; i < 205; i++ {
		id := "unrelated"
		if i%2 == 0 {
			id = "replaced"
		}
		r := Record{ID: fmt.Sprintf("row-%04d", i), Scope: scope, Kind: "test", Status: "active", Revision: 1}
		ref := []Ref{{Collection: colMemories, ID: id, Revision: 1}}
		switch col {
		case colTopicDigests, colUserDigests:
			r.Digest = &DigestData{Members: ref}
		case colSkillVersions:
			r.Relations = ref
		case colObservations:
			r.Observation = &ObservationData{SourceMemories: ref}
		}
		if err := p.Insert(testCtx, col, r); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			r.Scope = other
			r.ID = "foreign-row"
			if err := p.Insert(testCtx, col, r); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func markStaleDependents(t *testing.T, ctx context.Context, tx Tx, col string, scope Scope) error {
	t.Helper()
	visited := 0
	err := walkStaleDependents(ctx, tx.(*Postgres), scope, col, "active", "replaced", func(r Record) error {
		visited++
		if r.Scope != scope {
			t.Fatal("cross scope result")
		}
		old := r.Revision
		r.Revision++
		r.Status = "stale"
		return tx.Replace(ctx, col, r, old)
	})
	if err == nil && visited != 103 {
		t.Fatalf("%s visited %d, want 103", col, visited)
	}
	return err
}
