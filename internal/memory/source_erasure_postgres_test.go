package memory

import (
	"errors"
	"testing"
)

// A deletion marker must be honored even while old source bytes still remain.
func TestPostgresSourceErasureMarkerBlocksRetainedBody(t *testing.T) {
	repo := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = repo
	memory := f.activeMemory("synthetic source erasure boundary")
	source, err := repo.Get(testCtx, "sources", scopeA, memory.Sources[0].SourceID)
	if err != nil || source.Text == "" || !live(source) {
		t.Fatal("synthetic active source not established", err)
	}
	source.ContentErased = true
	if err := repo.Replace(testCtx, "sources", source, source.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceMetadata(testCtx, repo, scopeA, memory.Sources); !errors.Is(err, ErrDenied) {
		t.Fatal("erased source with retained body was accepted", err)
	}
}
