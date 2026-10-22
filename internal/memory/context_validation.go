package memory

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"time"
	"unicode/utf8"
)

const ContextValidationRenderer = "capture-stable-v2"
const ContextValidationMaxRefs = 128

type ContextValidationRef struct {
	Collection  string `json:"collection"`
	ID          string `json:"id"`
	Revision    int64  `json:"revision"`
	Digest      string `json:"digest"`
	Fingerprint string `json:"fingerprint,omitempty"`
}
type ContextValidationInput struct {
	Scope       Scope                  `json:"scope"`
	Renderer    string                 `json:"renderer"`
	BlockHash   string                 `json:"block_hash"`
	Fingerprint string                 `json:"fingerprint,omitempty"`
	Refs        []ContextValidationRef `json:"refs"`
}
type ContextValidationItem struct {
	Collection  string `json:"collection"`
	ID          string `json:"id"`
	Revision    int64  `json:"revision"`
	Status      string `json:"status"`
	Fingerprint string `json:"fingerprint,omitempty"`
}
type ContextValidationOutput struct {
	Authority   string                  `json:"authority"`
	Integrity   bool                    `json:"integrity"`
	Fingerprint string                  `json:"fingerprint,omitempty"`
	Items       []ContextValidationItem `json:"items"`
}

// This process-local key detects changes to a previously rendered client cache. It
// does NOT attest that an arbitrary client block_hash describes the referenced text.
// Initial rendering is trusted; authorization and current source state are checked
// independently on every call. Restart/another replica rejects the old seal.
// Clients can discard that cache and render again from current APIs; rejected
// cached bytes must never be re-signed. A failed source/currentness check remains
// unavailable. No persistent key or permission is introduced.
var contextCacheKey struct {
	once sync.Once
	key  [32]byte
	err  error
}

func contextCacheSeal(in ContextValidationInput, actor Decision) (string, error) {
	contextCacheKey.once.Do(func() { _, contextCacheKey.err = rand.Read(contextCacheKey.key[:]) })
	if contextCacheKey.err != nil {
		return "", ErrUnavailable
	}
	in.Fingerprint = ""
	data := structuredText(struct {
		Input           ContextValidationInput
		Principal, Role string
	}{in, actor.PrincipalID, actor.Role})
	m := hmac.New(sha256.New, contextCacheKey.key[:])
	_, _ = m.Write([]byte(data))
	return hex.EncodeToString(m.Sum(nil)), nil
}
func validContextValidation(in ContextValidationInput) bool {
	if !validScope(in.Scope) || containsSecret(structuredText(in.Scope)) || in.Renderer != ContextValidationRenderer || !sourceHashPattern.MatchString(in.BlockHash) || len(in.Refs) > ContextValidationMaxRefs || len(structuredText(in)) > 64*1024 || in.Fingerprint != "" && !sourceHashPattern.MatchString(in.Fingerprint) {
		return false
	}
	seen := map[string]bool{}
	for _, r := range in.Refs {
		if r.Collection != colMemories || !validRunLabel(r.ID, 256) || containsSecret(r.ID) || r.Revision < 1 || !sourceHashPattern.MatchString(r.Digest) || r.Fingerprint != "" && !sourceHashPattern.MatchString(r.Fingerprint) || seen[r.ID] || (in.Fingerprint == "") != (r.Fingerprint == "") {
			return false
		}
		seen[r.ID] = true
	}
	return true
}
func contextMemoryCurrent(r Record, in ContextValidationInput, ref ContextValidationRef) bool {
	return r.ID == ref.ID && r.Scope == in.Scope && live(r) && !r.ContentErased && r.ApprovedBy != "" && r.Revision == ref.Revision && ContentHash(recordContent(r)) == ref.Digest && len(r.Sources) > 0 && len(r.Sources) <= 64
}

// ValidateContext batch-checks exact rendered memory revisions, including sources
// that can change without increasing the memory revision. Management GET existence
// is deliberately insufficient. Only fixed availability states and hashes leave it.
func (s *Service) ValidateContext(ctx context.Context, token string, in ContextValidationInput) (ContextValidationOutput, error) {
	out := ContextValidationOutput{Authority: AuthorityDataOnly, Items: []ContextValidationItem{}}
	in.Refs = slices.Clone(in.Refs)
	if in.Refs == nil {
		in.Refs = []ContextValidationRef{}
	}
	if !validContextValidation(in) {
		return out, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	actor, e := s.authorize(ctx, token, "read:memories", in.Scope, "", "")
	if e != nil {
		return out, e
	}
	ctx = bindRequestActor(ctx, actor)
	if s.Repo == nil {
		return out, ErrUnavailable
	}
	for _, r := range in.Refs {
		out.Items = append(out.Items, ContextValidationItem{Collection: r.Collection, ID: r.ID, Revision: r.Revision, Status: "unavailable"})
	}
	if in.Fingerprint != "" {
		expected, e := contextCacheSeal(in, actor)
		if e != nil {
			return out, e
		}
		if !hmac.Equal([]byte(expected), []byte(in.Fingerprint)) {
			return out, nil
		}
		out.Integrity = true
		out.Fingerprint = in.Fingerprint
	}
	snap, e := s.contextAuthorizedSnapshot(ctx, token, in)
	if e != nil {
		return out, e
	}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return validateContextLocked(ctx, tx, in, actor, snap, &out)
	})
	if e != nil {
		return ContextValidationOutput{}, e
	}
	if ctx.Err() != nil {
		return ContextValidationOutput{}, ErrUnavailable
	}
	if in.Fingerprint == "" {
		for i, item := range out.Items {
			if item.Status != "valid" {
				return out, nil
			}
			in.Refs[i].Fingerprint = item.Fingerprint
		}
		out.Fingerprint, e = contextCacheSeal(in, actor)
		if e != nil {
			return ContextValidationOutput{}, e
		}
		out.Integrity = true
	}
	return out, nil
}

// contextSnapshot holds the authorized memories and sources ValidateContext
// compares against the locked rows, with their ids in lock order.
type contextSnapshot struct {
	memories, sources    map[string]Record
	sourceHashes         map[string]string
	memoryIDs, sourceIDs []string
}

type contextDependency struct{ ID, Hash string }

type contextSourceState struct {
	Hash, TextHash string
	Runes          int
}

func (s *Service) contextAuthorizedSnapshot(ctx context.Context, token string, in ContextValidationInput) (contextSnapshot, error) {
	snap := contextSnapshot{memories: map[string]Record{}, sources: map[string]Record{}, sourceHashes: map[string]string{}}
	// Authorization precedes the transaction: denial auditing must not nest another
	// transaction under its read locks. The transaction then compares and locks the
	// exact authorized snapshots before issuing any availability result or seal.
	for _, ref := range in.Refs {
		if ctx.Err() != nil {
			return snap, ErrUnavailable
		}
		r, e := s.Repo.Get(ctx, colMemories, in.Scope, ref.ID)
		if errors.Is(e, ErrNotFound) {
			continue
		}
		if e != nil {
			return snap, e
		}
		if !contextMemoryCurrent(r, in, ref) {
			continue
		}
		if _, e = s.authorize(ctx, token, "read:memories", in.Scope, r.ID, structuredText(r)); errors.Is(e, ErrDenied) {
			continue
		} else if e != nil {
			return snap, e
		}
		ok, e := s.contextAuthorizedSources(ctx, token, in, r, &snap)
		if e != nil {
			return snap, e
		}
		if ok {
			snap.memories[r.ID] = r
		}
	}
	for id := range snap.memories {
		snap.memoryIDs = append(snap.memoryIDs, id)
	}
	for id := range snap.sources {
		snap.sourceIDs = append(snap.sourceIDs, id)
	}
	slices.Sort(snap.memoryIDs)
	slices.Sort(snap.sourceIDs)
	return snap, nil
}

// contextAuthorizedSources records the memory's readable live sources and
// reports whether all of them are.
func (s *Service) contextAuthorizedSources(ctx context.Context, token string, in ContextValidationInput, r Record, snap *contextSnapshot) (bool, error) {
	for _, ref := range r.Sources {
		if ref.SourceID == "" || ref.Locator == "" {
			return false, nil
		}
		if _, exists := snap.sources[ref.SourceID]; exists {
			continue
		}
		if len(snap.sources) >= 256 {
			return false, ErrUnavailable
		}
		source, e := s.Repo.Get(ctx, "sources", in.Scope, ref.SourceID)
		if errors.Is(e, ErrNotFound) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		if source.Scope != in.Scope || source.ID != ref.SourceID || !live(source) || source.ContentErased || source.Revision < 1 {
			return false, nil
		}
		if _, e = s.authorize(ctx, token, "read:sources", in.Scope, source.ID, structuredText(source)); errors.Is(e, ErrDenied) {
			return false, nil
		} else if e != nil {
			return false, e
		}
		snap.sources[source.ID] = source
		snap.sourceHashes[source.ID] = ContentHash(structuredText(source))
	}
	return true, nil
}

func validateContextLocked(ctx context.Context, tx Tx, in ContextValidationInput, actor Decision, snap contextSnapshot, out *ContextValidationOutput) error {
	lockedMem, lockedSources, e := contextLockSnapshot(ctx, tx, in.Scope, snap)
	if e != nil {
		return e
	}
	currentSources, e := contextCurrentSources(ctx, snap, lockedSources)
	if e != nil {
		return e
	}
	for i, ref := range in.Refs {
		if ctx.Err() != nil {
			return ErrUnavailable
		}
		before, ok := snap.memories[ref.ID]
		if !ok {
			continue
		}
		r, ok := lockedMem[ref.ID]
		if !ok || structuredText(before) != structuredText(r) || !contextMemoryCurrent(r, in, ref) {
			continue
		}
		deps, ok := contextDependencies(r, currentSources, lockedSources)
		if !ok {
			continue
		}
		fingerprint := ContentHash(structuredText(struct {
			Memory  string
			Sources []contextDependency
		}{ContentHash(structuredText(r)), deps}))
		if ref.Fingerprint != "" && ref.Fingerprint != fingerprint {
			continue
		}
		if e := contextFence(ctx, tx, in.Scope, r.ID, deps); e != nil {
			return e
		}
		out.Items[i].Status = "valid"
		out.Items[i].Fingerprint = fingerprint
	}
	return audit(ctx, tx, in.Scope, actor.PrincipalID, "read:context_validation", "")
}

func contextLockSnapshot(ctx context.Context, tx Tx, scope Scope, snap contextSnapshot) (map[string]Record, map[string]Record, error) {
	lockedMem, lockedSources := map[string]Record{}, map[string]Record{}
	if locker, ok := tx.(readLocker); ok {
		var e error
		if len(snap.memoryIDs) > 0 {
			lockedMem, e = locker.LockForRead(ctx, scope, colMemories, snap.memoryIDs)
			if e != nil {
				return nil, nil, e
			}
		}
		if len(snap.sourceIDs) > 0 {
			lockedSources, e = locker.LockForRead(ctx, scope, "sources", snap.sourceIDs)
			if e != nil {
				return nil, nil, e
			}
		}
		return lockedMem, lockedSources, nil
	}
	if e := contextGetRows(ctx, tx, scope, colMemories, snap.memoryIDs, lockedMem); e != nil {
		return nil, nil, e
	}
	if e := contextGetRows(ctx, tx, scope, "sources", snap.sourceIDs, lockedSources); e != nil {
		return nil, nil, e
	}
	return lockedMem, lockedSources, nil
}

func contextGetRows(ctx context.Context, tx Tx, scope Scope, collection string, ids []string, into map[string]Record) error {
	for _, id := range ids {
		r, e := tx.Get(ctx, collection, scope, id)
		if errors.Is(e, ErrNotFound) {
			continue
		}
		if e != nil {
			return e
		}
		into[id] = r
	}
	return nil
}

// contextCurrentSources hashes each unique source once, rather than
// re-marshalling full originals for every memory/source edge in a fanout-heavy batch.
func contextCurrentSources(ctx context.Context, snap contextSnapshot, lockedSources map[string]Record) (map[string]contextSourceState, error) {
	currentSources := map[string]contextSourceState{}
	for _, id := range snap.sourceIDs {
		if ctx.Err() != nil {
			return nil, ErrUnavailable
		}
		row, exists := lockedSources[id]
		if !exists || !live(row) || row.ContentErased {
			continue
		}
		hash := ContentHash(structuredText(row))
		if hash != snap.sourceHashes[id] {
			continue
		}
		currentSources[id] = contextSourceState{Hash: hash, TextHash: ContentHash(row.Text), Runes: utf8.RuneCountInString(row.Text)}
	}
	return currentSources, nil
}

// contextDependencies returns the memory's current sources sorted by id, or
// false when any of them changed or its span no longer fits.
func contextDependencies(r Record, currentSources map[string]contextSourceState, lockedSources map[string]Record) ([]contextDependency, bool) {
	deps := []contextDependency{}
	for _, sr := range r.Sources {
		source, exists := currentSources[sr.SourceID]
		if !exists || !live(lockedSources[sr.SourceID]) {
			return nil, false
		}
		if sr.Span != nil && validateSourceSpan(sr.Span, source.TextHash, source.Runes) != nil {
			return nil, false
		}
		deps = append(deps, contextDependency{ID: sr.SourceID, Hash: source.Hash})
	}
	slices.SortFunc(deps, func(a, b contextDependency) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return deps, true
}

func contextFence(ctx context.Context, tx Tx, scope Scope, memoryID string, deps []contextDependency) error {
	if _, hasLocks := tx.(readLocker); hasLocks {
		return nil
	}
	if e := tx.FenceRelationMember(ctx, scope, colMemories, memoryID); e != nil {
		return e
	}
	for _, source := range deps {
		if e := tx.FenceSource(ctx, scope, source.ID); e != nil {
			return e
		}
	}
	return nil
}
