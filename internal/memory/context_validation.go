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
	memories, sources := map[string]Record{}, map[string]Record{}
	sourceHashes := map[string]string{}
	// Authorization precedes the transaction: denial auditing must not nest another
	// transaction under its read locks. The transaction then compares and locks the
	// exact authorized snapshots before issuing any availability result or seal.
	for _, ref := range in.Refs {
		if ctx.Err() != nil {
			return out, ErrUnavailable
		}
		r, e := s.Repo.Get(ctx, colMemories, in.Scope, ref.ID)
		if errors.Is(e, ErrNotFound) {
			continue
		}
		if e != nil {
			return out, e
		}
		if !contextMemoryCurrent(r, in, ref) {
			continue
		}
		if _, e = s.authorize(ctx, token, "read:memories", in.Scope, r.ID, structuredText(r)); errors.Is(e, ErrDenied) {
			continue
		} else if e != nil {
			return out, e
		}
		ok := true
		for _, ref := range r.Sources {
			if ref.SourceID == "" || ref.Locator == "" {
				ok = false
				break
			}
			if _, exists := sources[ref.SourceID]; exists {
				continue
			}
			if len(sources) >= 256 {
				return out, ErrUnavailable
			}
			source, e := s.Repo.Get(ctx, "sources", in.Scope, ref.SourceID)
			if errors.Is(e, ErrNotFound) {
				ok = false
				break
			}
			if e != nil {
				return out, e
			}
			if source.Scope != in.Scope || source.ID != ref.SourceID || !live(source) || source.ContentErased || source.Revision < 1 {
				ok = false
				break
			}
			if _, e = s.authorize(ctx, token, "read:sources", in.Scope, source.ID, structuredText(source)); errors.Is(e, ErrDenied) {
				ok = false
				break
			} else if e != nil {
				return out, e
			}
			sources[source.ID] = source
			sourceHashes[source.ID] = ContentHash(structuredText(source))
		}
		if ok {
			memories[r.ID] = r
		}
	}
	memoryIDs, sourceIDs := []string{}, []string{}
	for id := range memories {
		memoryIDs = append(memoryIDs, id)
	}
	for id := range sources {
		sourceIDs = append(sourceIDs, id)
	}
	slices.Sort(memoryIDs)
	slices.Sort(sourceIDs)
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		lockedMem, lockedSources := map[string]Record{}, map[string]Record{}
		if locker, ok := tx.(readLocker); ok {
			var e error
			if len(memoryIDs) > 0 {
				lockedMem, e = locker.LockForRead(ctx, in.Scope, colMemories, memoryIDs)
				if e != nil {
					return e
				}
			}
			if len(sourceIDs) > 0 {
				lockedSources, e = locker.LockForRead(ctx, in.Scope, "sources", sourceIDs)
				if e != nil {
					return e
				}
			}
		} else {
			for _, id := range memoryIDs {
				r, e := tx.Get(ctx, colMemories, in.Scope, id)
				if errors.Is(e, ErrNotFound) {
					continue
				}
				if e != nil {
					return e
				}
				lockedMem[id] = r
			}
			for _, id := range sourceIDs {
				r, e := tx.Get(ctx, "sources", in.Scope, id)
				if errors.Is(e, ErrNotFound) {
					continue
				}
				if e != nil {
					return e
				}
				lockedSources[id] = r
			}
		}
		// Hash each unique source once, rather than re-marshalling full originals
		// for every memory/source edge in a fanout-heavy batch.
		type dependency struct{ ID, Hash string }
		type sourceState struct {
			Hash, TextHash string
			Runes          int
		}
		currentSources := map[string]sourceState{}
		for _, id := range sourceIDs {
			if ctx.Err() != nil {
				return ErrUnavailable
			}
			row, exists := lockedSources[id]
			if !exists || !live(row) || row.ContentErased {
				continue
			}
			hash := ContentHash(structuredText(row))
			if hash != sourceHashes[id] {
				continue
			}
			currentSources[id] = sourceState{Hash: hash, TextHash: ContentHash(row.Text), Runes: utf8.RuneCountInString(row.Text)}
		}
		for i, ref := range in.Refs {
			if ctx.Err() != nil {
				return ErrUnavailable
			}
			before, ok := memories[ref.ID]
			if !ok {
				continue
			}
			r, ok := lockedMem[ref.ID]
			if !ok || structuredText(before) != structuredText(r) || !contextMemoryCurrent(r, in, ref) {
				continue
			}
			deps := []dependency{}
			ok = true
			for _, sr := range r.Sources {
				source, exists := currentSources[sr.SourceID]
				if !exists || !live(lockedSources[sr.SourceID]) {
					ok = false
					break
				}
				if sr.Span != nil && validateSourceSpan(sr.Span, source.TextHash, source.Runes) != nil {
					ok = false
					break
				}
				deps = append(deps, dependency{ID: sr.SourceID, Hash: source.Hash})
			}
			if !ok {
				continue
			}
			slices.SortFunc(deps, func(a, b dependency) int {
				if a.ID < b.ID {
					return -1
				}
				if a.ID > b.ID {
					return 1
				}
				return 0
			})
			fingerprint := ContentHash(structuredText(struct {
				Memory  string
				Sources []dependency
			}{ContentHash(structuredText(r)), deps}))
			if ref.Fingerprint != "" && ref.Fingerprint != fingerprint {
				continue
			}
			if _, hasLocks := tx.(readLocker); !hasLocks {
				if e := tx.FenceRelationMember(ctx, in.Scope, colMemories, r.ID); e != nil {
					return e
				}
				for _, source := range deps {
					if e := tx.FenceSource(ctx, in.Scope, source.ID); e != nil {
						return e
					}
				}
			}
			out.Items[i].Status = "valid"
			out.Items[i].Fingerprint = fingerprint
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "read:context_validation", "")
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
