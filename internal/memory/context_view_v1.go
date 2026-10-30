package memory

import "slices"

const ContextViewProtocol = "context-view-exclusion-v1"

// ContextInvocationView is an explicit current-pack selector, not stored state or authority.
type ContextInvocationView struct {
	Protocol     string `json:"protocol"`
	ContextScope Scope  `json:"context_scope"`
	Exclude      []Ref  `json:"exclude"`
	observation  *ContextViewObservation
}
type ContextViewObservation struct {
	Protocol              string   `json:"protocol"`
	ViewSHA               string   `json:"view_sha256"`
	ExcludedSearch        []string `json:"excluded_cleaned_search_scoped_ref_sha256"`
	ExcludedPins          []string `json:"excluded_pinned_scoped_ref_sha256"`
	EmittedCurrent        []string `json:"emitted_current_scoped_ref_sha256"`
	EmittedHistory        []string `json:"emitted_history_scoped_ref_sha256"`
	PriorPathRemoved      bool     `json:"prior_path_removed"`
	ReaderAcknowledgement string   `json:"reader_acknowledgement"`
	ActualUse             string   `json:"actual_use"`
	TaskSuccess           string   `json:"task_success"`
	CausalAttribution     string   `json:"causal_attribution"`
}

func freezeContextView(scope Scope, in *ContextInvocationView) (*ContextInvocationView, error) {
	if in == nil {
		return nil, nil
	}
	if in.Protocol != ContextViewProtocol || !validScope(in.ContextScope) || in.ContextScope != scope || in.Exclude == nil || len(in.Exclude) > 128 {
		return nil, ErrInvalid
	}
	seen := map[Ref]bool{}
	for _, ref := range in.Exclude {
		if ref.Collection != colMemories || !validRunLabel(ref.ID, 256) || ref.Revision < 1 || seen[ref] {
			return nil, ErrInvalid
		}
		seen[ref] = true
	}
	out := *in
	out.Exclude = append([]Ref{}, in.Exclude...)
	return &out, nil
}
func (v *ContextInvocationView) excludes(h Hit) bool {
	return v != nil && h.Scope == v.ContextScope && slices.Contains(v.Exclude, Ref{Collection: h.Collection, ID: h.ID, Revision: h.Revision})
}
func contextScopedRefSHA(scope Scope, ref Ref) string {
	return nativeSearchHash("ScopedRef", struct {
		Scope Scope
		Ref   Ref
	}{scope, ref})
}
func contextHitRefSHA(h Hit) string {
	return contextScopedRefSHA(h.Scope, Ref{Collection: h.Collection, ID: h.ID, Revision: h.Revision})
}
func contextViewSHA(v *ContextInvocationView) string {
	if v == nil {
		return ""
	}
	return ContentHash(ContextViewProtocol + "\x00" + structuredText(v))
}
func (v *ContextInvocationView) observeExcluded(h Hit, pin bool) {
	if v == nil || v.observation == nil {
		return
	}
	digest := contextHitRefSHA(h)
	if pin {
		if !slices.Contains(v.observation.ExcludedPins, digest) {
			v.observation.ExcludedPins = append(v.observation.ExcludedPins, digest)
		}
		return
	}
	if !slices.Contains(v.observation.ExcludedSearch, digest) {
		v.observation.ExcludedSearch = append(v.observation.ExcludedSearch, digest)
	}
}
func filterContextViewItems(items []Hit, v *ContextInvocationView, pin bool) []Hit {
	if v == nil {
		return items
	}
	out := make([]Hit, 0, len(items))
	for _, h := range items {
		if v.excludes(h) {
			v.observeExcluded(h, pin)
			continue
		}
		out = append(out, h)
	}
	return out
}
func filterContextViewLanes(lanes [][]Hit, v *ContextInvocationView) {
	if v == nil {
		return
	}
	for i, lane := range lanes {
		lanes[i] = filterContextViewItems(lane, v, false)
	}
}
func contextViewObservation(v *ContextInvocationView, out ContextBundle) *ContextViewObservation {
	if v == nil {
		return nil
	}
	obs := ContextViewObservation{Protocol: ContextViewProtocol, ViewSHA: contextViewSHA(v), ExcludedSearch: []string{}, ExcludedPins: []string{}, EmittedCurrent: []string{}, EmittedHistory: []string{}, ReaderAcknowledgement: "unknown", ActualUse: "unknown", TaskSuccess: "unknown", CausalAttribution: "unknown"}
	if v.observation != nil {
		obs.ExcludedSearch = append(obs.ExcludedSearch, v.observation.ExcludedSearch...)
		obs.ExcludedPins = append(obs.ExcludedPins, v.observation.ExcludedPins...)
	}
	for _, h := range out.Items {
		obs.EmittedCurrent = append(obs.EmittedCurrent, contextHitRefSHA(h))
	}
	for _, h := range out.History {
		obs.EmittedHistory = append(obs.EmittedHistory, contextHitRefSHA(h.Hit))
	}
	return &obs
}

// Drop excluded records only from this fresh invocation's local history projection.
// The durable record and prior/retained paths are deliberately untouched.
func filterContextViewHistory(history map[string]Record, view *ContextInvocationView) {
	if view == nil {
		return
	}
	for id, r := range history {
		if view.excludes(HitFromRecord(colMemories, r)) {
			delete(history, id)
		}
	}
}
