package memoryharness

import (
	"slices"
	"sort"
	"strings"
)

// indexedCandidate retains the original lane position when eligibility removes
// hits. A compacted slice index must never become a new RRF ordinal.
type indexedCandidate struct {
	candidate      Candidate
	lane, position int
}

type rrfEntry struct {
	ranked RankedCandidate
	key    string
}

// RRFReference reproduces the fixed three-lane ordinal RRF calculation.
// It validates every candidate and same-key metadata before applying limit.
// Eligibility flags are not filters here; RankAndPack applies those filters.
func RRFReference(lanes [][]Candidate, limit int) ([]RankedCandidate, error) {
	if len(lanes) != 3 {
		return nil, contractError("invalid_value", "$.lanes")
	}
	indexed := []indexedCandidate{}
	for lane, list := range lanes {
		if list == nil {
			return nil, contractError("invalid_type", "$.lanes[*]")
		}
		for position, candidate := range list {
			indexed = append(indexed, indexedCandidate{candidate, lane, position})
		}
	}
	if err := rrfValidate(indexed); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []RankedCandidate{}, nil
	}
	ranked := rrfFuse(indexed)
	return ranked[:min(limit, len(ranked))], nil
}

func rrfKey(c Candidate) string { return c.Ref.Origin + ":" + c.Ref.Collection + ":" + c.Ref.ID }

// Trust, updated time and reasons may differ between occurrences. The remaining
// metadata below must agree even for a duplicate that would not add score.
func rrfSameMetadata(a, b Candidate) bool {
	return a.Scope == b.Scope && a.Ref == b.Ref && a.Approved == b.Approved && a.Active == b.Active && a.Deleted == b.Deleted && a.SourceVerified == b.SourceVerified && a.ContextCostUnits == b.ContextCostUnits && a.ObservedSeq == b.ObservedSeq && a.EffectiveAtNS == b.EffectiveAtNS
}

func rrfValidate(indexed []indexedCandidate) error {
	for _, item := range indexed {
		if err := ValidateTyped("Candidate", item.candidate); err != nil {
			return rrfAt(err, "$.lanes[*][*]")
		}
	}
	seen := map[string]Candidate{}
	for _, item := range indexed {
		key := rrfKey(item.candidate)
		if prior, ok := seen[key]; ok && !rrfSameMetadata(prior, item.candidate) {
			return contractError("snapshot_conflict", "$")
		}
		seen[key] = item.candidate
	}
	return nil
}

func rrfAt(err error, path string) error {
	if ce, ok := err.(*ContractError); ok {
		return contractError(ce.Code, path+strings.TrimPrefix(ce.Path, "$"))
	}
	return err
}

func rrfClone(candidate Candidate) Candidate {
	candidate.Reasons = append([]string{}, candidate.Reasons...)
	return candidate
}

func rrfFuse(indexed []indexedCandidate) []RankedCandidate {
	merged := map[string]*rrfEntry{}
	seen := [3]map[string]bool{{}, {}, {}}
	for _, item := range indexed {
		candidate := item.candidate
		key := rrfKey(candidate)
		if seen[item.lane][key] {
			continue
		}
		seen[item.lane][key] = true
		entry := merged[key]
		if entry == nil {
			entry = &rrfEntry{ranked: RankedCandidate{Candidate: rrfClone(candidate), Lanes: []int64{}}, key: key}
			merged[key] = entry
		}
		weight := 1.0
		if slices.Contains(candidate.Reasons, "exact_keyword") {
			weight = 2
		}
		entry.ranked.Score += weight / (60 + float64(item.position) + 1)
		entry.ranked.Lanes = append(entry.ranked.Lanes, int64(item.lane))
		for _, reason := range candidate.Reasons {
			if !slices.Contains(entry.ranked.Candidate.Reasons, reason) {
				entry.ranked.Candidate.Reasons = append(entry.ranked.Candidate.Reasons, reason)
			}
		}
	}
	entries := make([]*rrfEntry, 0, len(merged))
	for _, entry := range merged {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return rrfBefore(entries[i], entries[j]) })
	result := make([]RankedCandidate, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.ranked)
	}
	return result
}

func rrfBefore(a, b *rrfEntry) bool {
	if a.ranked.Score != b.ranked.Score {
		return a.ranked.Score > b.ranked.Score
	}
	if a.ranked.Candidate.Trust != b.ranked.Candidate.Trust {
		return a.ranked.Candidate.Trust > b.ranked.Candidate.Trust
	}
	if a.ranked.Candidate.UpdatedAtNS != b.ranked.Candidate.UpdatedAtNS {
		return a.ranked.Candidate.UpdatedAtNS > b.ranked.Candidate.UpdatedAtNS
	}
	return a.key < b.key
}
