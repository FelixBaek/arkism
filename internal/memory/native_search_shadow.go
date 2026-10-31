package memory

import (
	"context"
	_ "embed"
	"sync"
	"time"

	dyn "github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
)

//go:embed native_search_shadow_contract.json
var nativeSearchShadowContract []byte

//go:embed native_search_fastslow_contract.json
var nativeSearchFastSlowContract []byte

// NativeSearchShadowConfig is an explicit in-process engineering configuration; no wire or environment loader enables it.
type NativeSearchShadowConfig struct {
	Mode                     string
	Registry                 dyn.PolicyRegistryV2
	Policy                   dyn.PolicyID
	NativeVersion, Dimension string
	Deadline                 time.Duration
	MaxSteps                 int64
	Capacity                 int
	// FastSlow, when set, reads the native slow lane and ranks on the mixed
	// value. Mode "on" requires it and serves the ranking; "shadow" only records.
	FastSlow *NativeSearchFastSlow
}

// NativeSearchFastSlow is the DYN-02/03 lane pair: the fast lane is the policy's
// own dynamics config, Slow the sealed slow lane config.
type NativeSearchFastSlow struct {
	Slow       dyn.DynamicsConfigV2
	FastWeight float64
}
type NativeSearchShadowObservation struct {
	Protocol         string   `json:"protocol"`
	ContractSHA      string   `json:"contract_sha256"`
	Mode             string   `json:"mode,omitempty"`
	ScopeSHA         string   `json:"scope_sha256"`
	CandidateSHA     string   `json:"candidate_sha256"`
	PublishedSHA     string   `json:"published_sha256"`
	ArithmeticSHA    string   `json:"arithmetic_only_sha256"`
	BaselineSHA      string   `json:"actual_baseline_sha256"`
	Status           string   `json:"status"`
	Reason           string   `json:"reason"`
	BaselineReturned bool     `json:"baseline_returned"`
	CandidateCount   int      `json:"candidate_count"`
	EntryCount       int      `json:"entry_count"`
	SourceCount      int      `json:"source_count"`
	Steps            int64    `json:"local_steps"`
	SelectedRefs     []string `json:"selected_scoped_ref_sha256"`
	FastSlow         bool     `json:"fast_slow,omitempty"`
	Served           bool     `json:"served,omitempty"`
}
type NativeSearchShadowRuntime struct {
	config  NativeSearchShadowConfig
	mu      sync.Mutex
	records []NativeSearchShadowObservation
	stages  []NativeSearchShadowStages
}

func NewNativeSearchShadowRuntime(cfg NativeSearchShadowConfig) (*NativeSearchShadowRuntime, error) {
	if cfg.Mode == "off" {
		return &NativeSearchShadowRuntime{config: cfg}, nil
	}
	if (cfg.Mode != "shadow" && cfg.Mode != "on") || (cfg.Mode == "on" && cfg.FastSlow == nil) || cfg.Deadline <= 0 || cfg.Deadline > 5*time.Second || cfg.MaxSteps < 1 || cfg.Capacity < 1 || cfg.Capacity > 1000 {
		return nil, ErrInvalid
	}
	if e := dyn.ValidateTyped("PolicyRegistryV2", cfg.Registry); e != nil {
		return nil, ErrInvalid
	}
	entry, ok := nativeSearchPolicy(cfg)
	if !ok {
		return nil, ErrInvalid
	}
	if _, e := nativeSearchFastSlowConfig(cfg, entry); e != nil {
		return nil, e
	}
	copyCfg, e := nativeClone(cfg)
	if e != nil {
		return nil, e
	}
	return &NativeSearchShadowRuntime{config: copyCfg, records: []NativeSearchShadowObservation{}}, nil
}
func nativeSearchPolicy(cfg NativeSearchShadowConfig) (dyn.PolicyEntry, bool) {
	for _, entry := range cfg.Registry.Entries {
		if entry.PolicyID == cfg.Policy {
			return entry, true
		}
	}
	return dyn.PolicyEntry{}, false
}

// nativeSearchFastSlowConfig returns nil when no lane pair is configured.
func nativeSearchFastSlowConfig(cfg NativeSearchShadowConfig, entry dyn.PolicyEntry) (*dyn.FastSlowConfig, error) {
	if cfg.FastSlow == nil {
		return nil, nil
	}
	if entry.Dynamics == nil {
		return nil, ErrInvalid
	}
	pair := dyn.FastSlowConfig{Fast: *entry.Dynamics, Slow: cfg.FastSlow.Slow, FastWeight: cfg.FastSlow.FastWeight}
	if pair.Validate() != nil {
		return nil, ErrInvalid
	}
	return &pair, nil
}
func (r *NativeSearchShadowRuntime) Observations() []NativeSearchShadowObservation {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]NativeSearchShadowObservation, len(r.records))
	for i, v := range r.records {
		out[i] = nativeCloneSearchObservation(v)
	}
	return out
}
func (r *NativeSearchShadowRuntime) remember(v NativeSearchShadowObservation, stage NativeSearchShadowStages) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.records) == r.config.Capacity {
		copy(r.records, r.records[1:])
		r.records = r.records[:len(r.records)-1]
		copy(r.stages, r.stages[1:])
		r.stages = r.stages[:len(r.stages)-1]
	}
	r.records = append(r.records, nativeCloneSearchObservation(v))
	r.stages = append(r.stages, cloneNativeSearchStages(stage))
}

type nativeSearchAttempt struct {
	serving  bool
	served   *nativeBridgeResult
	runtime  *NativeSearchShadowRuntime
	snapshot *nativeSearchSnapshot
	record   NativeSearchShadowObservation
	stages   NativeSearchShadowStages
}

func (s *Service) beginNativeSearchShadow(ctx context.Context, in SearchInput, collection string, lanes [][]Hit, history map[string]Record) *nativeSearchAttempt {
	runtime := s.NativeSearchShadow
	if runtime == nil || (runtime.config.Mode != "shadow" && runtime.config.Mode != "on") {
		return nil
	}
	a := &nativeSearchAttempt{runtime: runtime, record: NativeSearchShadowObservation{Protocol: "sys-native-service-shadow-v1", ContractSHA: ContentHash(string(nativeSearchShadowContract)), ScopeSHA: nativeSearchHash("Scope", in.Scope), Status: "fallback", Reason: "not_computed", SelectedRefs: []string{}, FastSlow: runtime.config.FastSlow != nil}}
	if runtime.config.FastSlow != nil {
		a.record.Protocol = "sys-native-service-fast-slow-v1"
		a.record.ContractSHA = ContentHash(string(nativeSearchFastSlowContract))
		a.record.Mode = runtime.config.Mode
	}
	a.stages = NativeSearchShadowStages{Protocol: NativeSearchStagesProtocol, ContractSHA: ContentHash(string(nativeSearchStagesContract)), ScopeSHA: a.record.ScopeSHA, ContextViewSHA: contextViewSHA(in.contextView), FinalErrorCode: "not_entered", ReaderAcknowledgement: "unknown", ActualUse: "unknown", TaskSuccess: "unknown", CausalAttribution: "unknown"}
	if ctx.Err() != nil {
		a.record.Reason = "caller_canceled"
		return a
	}
	if collection != colMemories {
		a.record.Reason = "unsupported_collection"
		return a
	}
	snap, code := nativeFreezeSearch(in.Scope, lanes, history)
	if code != "" {
		if ctx.Err() != nil {
			code = "caller_canceled"
		}
		a.record.Reason = code
		return a
	}
	a.snapshot = snap
	a.record.CandidateSHA = snap.digest
	a.stages.CandidateSHA = snap.digest
	a.record.CandidateCount = len(snap.unique)
	a.record.EntryCount = snap.entries
	return a
}
func (a *nativeSearchAttempt) finish() {
	if a == nil {
		return
	}
	if !a.record.BaselineReturned && !a.record.Served && a.record.Status == "selected" {
		a.record.Status = "computed_not_returned"
	}
	a.stages.ArithmeticSHA = a.record.ArithmeticSHA
	a.stages.BaselineSHA = a.record.BaselineSHA
	a.stages.PublishedSHA = a.record.PublishedSHA
	a.stages.Status = a.record.Status
	a.stages.Reason = a.record.Reason
	a.runtime.remember(a.record, a.stages)
}
func (a *nativeSearchAttempt) compute(ctx context.Context, s *Service, token string, in SearchInput, actor Decision, baseline ContextBundle) {
	if a == nil {
		return
	}
	a.stages.ContextIDHash = ContentHash(baseline.ContextID)
	a.stages.BaselinePreFinalCurrentRefs = []string{}
	a.stages.BaselinePreFinalHistoryRefs = []string{}
	for _, h := range baseline.Items {
		a.stages.BaselinePreFinalCurrentRefs = append(a.stages.BaselinePreFinalCurrentRefs, contextHitRefSHA(h))
	}
	for _, h := range baseline.History {
		a.stages.BaselinePreFinalHistoryRefs = append(a.stages.BaselinePreFinalHistoryRefs, contextHitRefSHA(h.Hit))
	}
	if a.snapshot == nil {
		return
	}
	a.record.BaselineSHA = nativeSearchBundleHash(baseline)
	if ctx.Err() != nil {
		a.record.Reason = "caller_canceled"
		return
	}
	bounded, cancel := context.WithTimeout(ctx, a.runtime.config.Deadline)
	defer cancel()
	published, cutoff, sourceSHA, code := s.nativePublishSearchValues(bounded, token, in, actor, a.snapshot, a.runtime.config)
	if code != "" {
		if ctx.Err() != nil {
			code = "caller_canceled"
		}
		a.record.Reason = code
		return
	}
	result := runNativeBridgeCandidate(bounded, nativeBridgeInput{Scope: in.Scope, Lanes: a.snapshot.lanes, History: a.snapshot.history, Limit: in.Limit, MaxChars: in.MaxChars, BudgetTokens: in.BudgetTokens, TokenEncoding: in.TokenEncoding, Now: cutoff, ClockDomain: "native-server-read-publication-v1", Registry: a.runtime.config.Registry, Policy: a.runtime.config.Policy, Published: published, MaxSteps: a.runtime.config.MaxSteps})
	a.record.PublishedSHA = sourceSHA
	a.record.SourceCount = len(published)
	a.record.ArithmeticSHA = result.ResultSHA
	a.record.Steps = result.Steps
	if ctx.Err() != nil {
		a.record.Reason = "caller_canceled"
		return
	}
	if bounded.Err() != nil {
		a.record.Reason = "shadow_deadline"
		return
	}
	if result.Status != "selected" {
		a.record.Reason = result.Reason
		return
	}
	a.computedStages(in.Scope, result)
	a.served = &result
	a.record.Status = "selected"
	a.record.Reason = "none"
	a.record.SelectedRefs = make([]string, 0, len(result.Ranked))
	for _, row := range result.Ranked {
		a.record.SelectedRefs = append(a.record.SelectedRefs, nativeSearchHash("ScopedRef", struct {
			Scope Scope
			Ref   Ref
		}{in.Scope, Ref{Collection: row.Material.Collection, ID: row.Material.ID, Revision: row.Material.Revision}}))
	}
}

// serve returns the fast/slow ranking in mode "on" once it was selected; every
// other case keeps the baseline. Served items get their own final-fence
// snapshots, so the commit transaction checks them like baseline items.
func (a *nativeSearchAttempt) serve(out ContextBundle, snapshots map[Ref]string) ContextBundle {
	if a == nil || a.runtime.config.Mode != "on" || a.served == nil || a.record.Status != "selected" {
		return out
	}
	served := a.served.served
	for _, m := range a.served.Prepack {
		snapshots[Ref{Collection: m.Collection, ID: m.ID, Revision: m.Revision}] = m.GuardSHA
	}
	served.ContextID, served.Degraded = out.ContextID, out.Degraded
	a.serving = true
	return served
}

func (a *nativeSearchAttempt) returned(out ContextBundle) {
	if a == nil {
		return
	}
	// A selected native response is only served after the final transaction
	// succeeds. Preserve the computed baseline hash and leave actual baseline
	// return refs unobserved when that baseline was not returned.
	if a.serving {
		a.record.Served = true
		return
	}
	a.record.BaselineReturned = true
	a.stages.BaselineReturned = true
	a.stages.BaselineCurrentRefs = []string{}
	a.stages.BaselineHistoryRefs = []string{}
	for _, h := range out.Items {
		a.stages.BaselineCurrentRefs = append(a.stages.BaselineCurrentRefs, contextHitRefSHA(h))
	}
	for _, h := range out.History {
		a.stages.BaselineHistoryRefs = append(a.stages.BaselineHistoryRefs, contextHitRefSHA(h.Hit))
	}
	a.record.BaselineSHA = nativeSearchBundleHash(out)
}
