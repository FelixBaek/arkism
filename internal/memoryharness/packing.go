package memoryharness

// PackContext scans the supplied rank order, skips duplicate keys and oversized
// whole items, and counts costs in fixture_units_v0. It never estimates tokens.
// A nil ranked slice is rejected consistently with the typed array boundary;
// use a nonnil empty slice to represent an empty ranked list.
func PackContext(ranked []RankedCandidate, finalK int64, budget ContextBudget) (BaselineSelection, error) {
	empty := BaselineSelection{Selected: []RankedCandidate{}}
	if err := ValidateTyped("ContextBudget", budget); err != nil {
		return empty, rrfAt(err, "$.budget")
	}
	if finalK < 0 || finalK > 64 {
		return empty, contractError("invalid_value", "$.final_k")
	}
	if ranked == nil {
		return empty, contractError("invalid_type", "$.ranked")
	}
	if err := packValidate(ranked); err != nil {
		return empty, err
	}
	keys := map[string]bool{}
	for _, item := range ranked {
		keys[rrfKey(item.Candidate)] = true
	}
	result := BaselineSelection{Selected: []RankedCandidate{}, EligibleUniqueCount: int64(len(keys))}
	seen := map[string]bool{}
	for _, item := range ranked {
		if int64(len(result.Selected)) >= finalK {
			break
		}
		key := rrfKey(item.Candidate)
		if seen[key] {
			continue
		}
		seen[key] = true
		if item.Candidate.ContextCostUnits > budget.Limit-result.UsedContextUnits {
			continue
		}
		item.Candidate = rrfClone(item.Candidate)
		item.Lanes = append([]int64{}, item.Lanes...)
		result.Selected = append(result.Selected, item)
		result.UsedContextUnits += item.Candidate.ContextCostUnits
	}
	return result, nil
}

func packValidate(ranked []RankedCandidate) error {
	for _, item := range ranked {
		if err := ValidateTyped("RankedCandidate", item); err != nil {
			return rrfAt(err, "$.ranked[*]")
		}
	}
	seen := map[string]Candidate{}
	for _, item := range ranked {
		key := rrfKey(item.Candidate)
		if prior, ok := seen[key]; ok && !rrfSameMetadata(prior, item.Candidate) {
			return contractError("snapshot_conflict", "$.ranked")
		}
		seen[key] = item.Candidate
	}
	return nil
}

// RankAndPack validates the whole snapshot and its exact probe binding before
// excluding ineligible hits. It fuses every eligible key before packing, keeping
// the original lane ordinal throughout. It does not use gold or dynamic scores.
func RankAndPack(snapshot CandidateSnapshot, probe Probe, policy Policy) (BaselineSelection, error) {
	const contractID = "arkism-offline-b0-v1"
	if err := ValidateBaselineInputs(ProbeFile{contractID, []Probe{probe}}, CandidateFile{contractID, []CandidateSnapshot{snapshot}}, policy); err != nil {
		return BaselineSelection{}, err
	}
	indexed := []indexedCandidate{}
	for lane, list := range snapshot.Lanes {
		for position, candidate := range list {
			if candidate.Approved && candidate.Active && !candidate.Deleted && candidate.SourceVerified {
				indexed = append(indexed, indexedCandidate{candidate, lane, position})
			}
		}
	}
	return PackContext(rrfFuse(indexed), probe.FinalK, probe.ContextBudget)
}
