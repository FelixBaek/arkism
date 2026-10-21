package memory

import (
	"context"
	"sync"
)

// Read validation and its usage/audit writes stay Serializable. Concurrent
// searches in a small table otherwise repeatedly invalidate each other's SSI
// predicates. Coordinate only this final phase, scoped to one service/scope;
// retrieval, provider calls and unrelated scopes remain concurrent.
type searchCommitGate struct {
	mu      sync.Mutex
	entries map[Scope]*searchCommitEntry
}

type searchCommitEntry struct {
	permit chan struct{}
	refs   int
}

func (g *searchCommitGate) reference(scope Scope) *searchCommitEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.entries == nil {
		g.entries = make(map[Scope]*searchCommitEntry)
	}
	entry := g.entries[scope]
	if entry == nil {
		entry = &searchCommitEntry{permit: make(chan struct{}, 1)}
		g.entries[scope] = entry
	}
	entry.refs++
	return entry
}

func (g *searchCommitGate) unreference(scope Scope, entry *searchCommitEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	entry.refs--
	if entry.refs == 0 {
		delete(g.entries, scope)
	}
}

func (g *searchCommitGate) acquire(ctx context.Context, scope Scope) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry := g.reference(scope)
	select {
	case entry.permit <- struct{}{}:
	case <-ctx.Done():
		g.unreference(scope, entry)
		return nil, ctx.Err()
	}
	var once sync.Once
	release := func() { once.Do(func() { <-entry.permit; g.unreference(scope, entry) }) }
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
