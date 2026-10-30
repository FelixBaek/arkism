package memory

import (
	"encoding/json"
	"slices"
)

type nativeSearchSnapshot struct {
	scope   Scope
	lanes   [][]Hit
	history map[string]Record
	unique  []Hit
	entries int
	digest  string
}

func nativeSearchHash(domain string, value any) string {
	raw, e := json.Marshal(value)
	if e != nil {
		return ""
	}
	return ContentHash("sys-native-service-shadow-v1\x00" + domain + "\x00" + string(raw))
}
func nativeFreezeSearch(scope Scope, lanes [][]Hit, history map[string]Record) (*nativeSearchSnapshot, string) {
	if len(lanes) > 4 {
		return nil, "candidate_bound_exceeded"
	}
	entries := 0
	for _, lane := range lanes {
		entries += len(lane)
	}
	if entries > 200 {
		return nil, "candidate_bound_exceeded"
	}
	cloned, e := nativeClone(lanes)
	if e != nil {
		return nil, "invalid_candidate"
	}
	historyCopy, e := nativeClone(history)
	if e != nil {
		return nil, "invalid_candidate"
	}
	snap := &nativeSearchSnapshot{scope: scope, lanes: cloned, history: historyCopy, entries: entries, unique: []Hit{}}
	seen := map[string]bool{}
	for _, lane := range cloned {
		for _, h := range lane {
			if h.Scope != scope {
				return nil, "candidate_scope_mismatch"
			}
			key := nativeBridgeKey(h)
			if !seen[key] {
				seen[key] = true
				snap.unique = append(snap.unique, h)
			}
		}
	}
	snap.digest = nativeSearchHash("CleanedFullLanes", struct {
		Scope   Scope
		Lanes   [][]Hit
		History map[string]Record
	}{scope, cloned, historyCopy})
	if snap.digest == "" {
		return nil, "invalid_candidate"
	}
	return snap, ""
}
func nativeSearchBundleHash(bundle ContextBundle) string {
	bundle.ContextID = ""
	return nativeSearchHash("ActualBaselineResponse", bundle)
}
func nativeCloneSearchObservation(v NativeSearchShadowObservation) NativeSearchShadowObservation {
	v.SelectedRefs = slices.Clone(v.SelectedRefs)
	return v
}
