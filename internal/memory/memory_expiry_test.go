package memory

import (
	"errors"
	"testing"
	"time"
)

func TestMemoryExpirySetAndClear(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("expiryneedle keep", "expiryneedle")
	at := time.Now().Add(7 * 24 * time.Hour)
	in := MemoryExpiryInput{Scope: scopeA, ExpectedRevision: m.Revision, ExpiresAt: &at}
	if _, e := f.svc.SetMemoryExpiry(testCtx, f.agent, m.ID, in); !errors.Is(e, ErrDenied) {
		t.Fatal("agent set expiry", e)
	}
	past := time.Now().Add(-time.Minute)
	if _, e := f.svc.SetMemoryExpiry(testCtx, f.human, m.ID, MemoryExpiryInput{Scope: scopeA, ExpectedRevision: m.Revision, ExpiresAt: &past}); !errors.Is(e, ErrInvalid) {
		t.Fatal("past expiry accepted", e)
	}
	set, e := f.svc.SetMemoryExpiry(testCtx, f.human, m.ID, in)
	if e != nil || set.ExpiresAt == nil || !set.ExpiresAt.Equal(at.UTC().Truncate(time.Millisecond)) || set.Revision != m.Revision+1 {
		t.Fatal("set expiry", set.ExpiresAt, e)
	}
	if _, e = f.svc.SetMemoryExpiry(testCtx, f.human, m.ID, in); !errors.Is(e, ErrConflict) {
		t.Fatal("stale revision accepted", e)
	}
	stored := f.get(f.human, colMemories, scopeA, m.ID)
	if stored.ExpiresAt == nil || !live(stored) {
		t.Fatal("future expiry not persisted or hid the memory early")
	}
	cleared, e := f.svc.SetMemoryExpiry(testCtx, f.human, m.ID, MemoryExpiryInput{Scope: scopeA, ExpectedRevision: set.Revision})
	if e != nil || cleared.ExpiresAt != nil || cleared.Revision != set.Revision+1 {
		t.Fatal("clear expiry", e)
	}
	if f.get(f.human, colMemories, scopeA, m.ID).ExpiresAt != nil {
		t.Fatal("clear not persisted")
	}
}

func TestExpiredMemoryIsNotSearchedOrListed(t *testing.T) {
	f := newFixture(t)
	f.svc.Embedder = nil
	keep := f.activeMemory("expiryneedle keep", "expiryneedle")
	gone := f.activeMemory("expiryneedle gone", "expiryneedle")
	search := SearchInput{Scope: scopeA, Query: "expiryneedle", BudgetTokens: 2000, TokenEncoding: "cl100k_base"}
	if got := f.search(f.human, search); len(got.Items) != 2 {
		t.Fatal("setup search", got.Items)
	}
	soon := time.Now().Add(150 * time.Millisecond)
	if _, e := f.svc.SetMemoryExpiry(testCtx, f.human, gone.ID, MemoryExpiryInput{Scope: scopeA, ExpectedRevision: gone.Revision, ExpiresAt: &soon}); e != nil {
		t.Fatal(e)
	}
	if got := f.search(f.human, search); len(got.Items) != 2 {
		t.Fatal("memory hidden before its expiry", got.Items)
	}
	time.Sleep(250 * time.Millisecond)
	got := f.search(f.human, search)
	if len(got.Items) != 1 || got.Items[0].ID != keep.ID {
		t.Fatal("expired memory still searched", got.Items)
	}
	page, e := f.svc.ListMemories(testCtx, f.human, MemoryListInput{Scope: scopeA})
	if e != nil || len(page.Items) != 1 || page.Items[0].ID != keep.ID {
		t.Fatal("expired memory still listed as active", page.Items, e)
	}
	current := f.get(f.human, colMemories, scopeA, gone.ID)
	if _, e = f.svc.SetMemoryExpiry(testCtx, f.human, gone.ID, MemoryExpiryInput{Scope: scopeA, ExpectedRevision: current.Revision}); !errors.Is(e, ErrConflict) {
		t.Fatal("expired memory revived by clearing", e)
	}
}
