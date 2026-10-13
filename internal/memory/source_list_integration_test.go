//go:build integration

package memory

import (
	"context"
	"testing"
	"time"
)

func TestIntegrationSourceList(t *testing.T) { exerciseSourceList(t, isolatedPostgres(t)) }

func TestIntegrationSourceListDeletionFence(t *testing.T) {
	repo := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = repo
	src, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "original list excerpt"}))
	if e != nil {
		t.Fatal(e)
	}
	race := &memoryFenceRaceRepo{Repository: repo, reached: make(chan struct{}), release: make(chan struct{})}
	reader := &Service{Repo: race, Auth: f.auth}
	ctx, cancel := context.WithTimeout(testCtx, 10*time.Second)
	defer cancel()
	type result struct {
		page SourcePage
		err  error
	}
	done := make(chan result, 1)
	go func() {
		page, e := reader.ListSources(ctx, f.human, SourceListInput{Scope: scopeA, Query: "list excerpt"})
		done <- result{page, e}
	}()
	released := false
	defer func() {
		if !released {
			close(race.release)
		}
	}()
	select {
	case <-race.reached:
	case <-ctx.Done():
		t.Fatal("list never reached fence")
	}
	if e = f.svc.Delete(ctx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
		t.Fatal(e)
	}
	close(race.release)
	released = true
	select {
	case got := <-done:
		if got.err == nil || len(got.page.Items) != 0 || got.page.NextCursor != "" {
			t.Fatal("committed deletion leaked stale list", got.err)
		}
	case <-ctx.Done():
		t.Fatal("list did not finish")
	}
}

func TestIntegrationArtifactList(t *testing.T) { exerciseArtifactList(t, isolatedPostgres(t)) }

func TestIntegrationArtifactListDeletionFence(t *testing.T) {
	repo := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = repo
	src, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "original list excerpt"}))
	if e != nil {
		t.Fatal(e)
	}
	race := &memoryFenceRaceRepo{Repository: repo, reached: make(chan struct{}), release: make(chan struct{})}
	reader := &Service{Repo: race, Auth: f.auth}
	ctx, cancel := context.WithTimeout(testCtx, 10*time.Second)
	defer cancel()
	type result struct {
		page SourceArtifacts
		err  error
	}
	done := make(chan result, 1)
	go func() {
		page, e := reader.ListSourceArtifacts(ctx, f.human, src.ID, ArtifactListInput{Scope: scopeA})
		done <- result{page, e}
	}()
	released := false
	defer func() {
		if !released {
			close(race.release)
		}
	}()
	select {
	case <-race.reached:
	case <-ctx.Done():
		t.Fatal("list never reached fence")
	}
	if e = f.svc.Delete(ctx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
		t.Fatal(e)
	}
	close(race.release)
	released = true
	select {
	case got := <-done:
		if got.err == nil || len(got.page.Items) != 0 || got.page.NextCursor != "" {
			t.Fatal("committed deletion leaked stale list", got.err)
		}
	case <-ctx.Done():
		t.Fatal("list did not finish")
	}
}

func TestIntegrationSourceSearchChangedBody(t *testing.T) {
	exerciseSourceSearchChangedBody(t, isolatedPostgres(t))
}
