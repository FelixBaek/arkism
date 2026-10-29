package dynamicsv2

import (
	"errors"
	"math"
	"sort"

	"github.com/FelixBaek/arkism/internal/memoryharness"
)

// RankPolicy ranks an already registered configuration and supplied exact-prefix
// views. It has no registry/feature file access and does not update any state.
func RankPolicy(snapshot memoryharness.CandidateSnapshot, probe ProbeV2, views []StateViewV2, cfg RankerConfigV2) (PolicyDecisionV2, error) {
	return rankPolicy(snapshot, probe, views, nil, cfg, false, nil)
}

func baselinePolicy() memoryharness.Policy {
	return memoryharness.Policy{ContractID: "arkism-offline-b0-v1", PolicyID: "b0-rrf-synthetic-v1", RankOffset: 60, ExactReasonWeight: 2, OtherWeight: 1, ContextUnit: "fixture_units_v0", EligibilityOrder: "validate-filter-preserve-ordinal-fuse-pack"}
}

func baselineProbe(q ProbeV2) memoryharness.Probe {
	return memoryharness.Probe{QueryID: q.QueryID, StreamID: q.StreamID, Scope: q.Scope, AtNS: q.AtNS, CutoffAcceptedSeq: q.CutoffAcceptedSeq, CandidateSnapshotID: q.CandidateSnapshotID, FinalK: q.FinalK, ContextBudget: q.ContextBudget, PolicyID: "b0-rrf-synthetic-v1"}
}

func policyError(err error) error {
	if err == nil {
		return nil
	}
	var dynamic *ContractErrorV2
	if errors.As(err, &dynamic) && contains(fixedEnums["V2ErrorCode"], string(dynamic.Code)) && knownSchemaPath(dynamic.Path) {
		return &ContractErrorV2{Code: dynamic.Code, Path: dynamic.Path}
	}
	var native *memoryharness.ContractError
	if errors.As(err, &native) && contains(fixedEnums["V2ErrorCode"], native.Code) && knownSchemaPath(native.Path) {
		return &ContractErrorV2{Code: V2ErrorCode(native.Code), Path: native.Path}
	}
	return contractError("io_error", "$")
}

func policyInputs(s memoryharness.CandidateSnapshot, q ProbeV2, views []StateViewV2, cfg RankerConfigV2) error {
	if err := memoryharness.ValidateTyped("CandidateSnapshot", s); err != nil {
		return policyError(err)
	}
	if err := ValidateTyped("ProbeV2", q); err != nil {
		return err
	}
	if views == nil {
		return contractError("invalid_type", "$.states")
	}
	if len(views) > 100000 {
		return contractError("resource_limit", "$.states")
	}
	for _, view := range views {
		if err := ValidateTyped("StateViewV2", view); err != nil {
			return err
		}
	}
	if err := ValidateTyped("RankerConfigV2", cfg); err != nil {
		return err
	}
	if cfg.PolicyID != q.PolicyID || cfg.PolicyVersion != q.PolicyVersion {
		return contractError("config_mismatch", "$.policy_id")
	}
	hash, err := memoryharness.CanonicalSHA256("CandidateSnapshot", s)
	if err != nil {
		return policyError(err)
	}
	if q.CandidateSnapshotSHA256 != hash {
		return contractError("hash_mismatch", "$.candidate_snapshot_sha256")
	}
	return policyError(memoryharness.ValidateBaselineInputs(memoryharness.ProbeFile{ContractID: "arkism-offline-b0-v1", Probes: []memoryharness.Probe{baselineProbe(q)}}, memoryharness.CandidateFile{ContractID: "arkism-offline-b0-v1", Snapshots: []memoryharness.CandidateSnapshot{s}}, baselinePolicy()))
}

func policyViews(views []StateViewV2, q ProbeV2, cfg RankerConfigV2) (map[RefV2]StateViewV2, error) {
	result := make(map[RefV2]StateViewV2, len(views))
	for i, view := range views {
		if i > 0 && (view.SnapshotSHA256 != views[0].SnapshotSHA256 || view.SnapshotCursor != views[0].SnapshotCursor || view.ConfigSHA256 != views[0].ConfigSHA256) {
			return nil, contractError("snapshot_binding", "$.snapshot_sha256")
		}
		if view.Scope != q.Scope || view.Key.Scope != q.Scope {
			return nil, contractError("scope_mismatch", "$.scope")
		}
		if view.StreamID != q.StreamID {
			return nil, contractError("stream_mismatch", "$.stream_id")
		}
		if view.ClockDomain != q.ClockDomain {
			return nil, contractError("clock_domain_mismatch", "$.clock_domain")
		}
		if err := policyViewBinding(view, q, cfg); err != nil {
			return nil, err
		}
		if _, exists := result[view.Key.Target]; exists {
			return nil, contractError("snapshot_conflict", "$.states")
		}
		result[view.Key.Target] = view
	}
	return result, nil
}

func policyViewBinding(view StateViewV2, q ProbeV2, cfg RankerConfigV2) error {
	if view.Dimension != q.Dimension || view.Key.Dimension != q.Dimension {
		return contractError("config_mismatch", "$.dimension")
	}
	if view.AtNS != q.AtNS || view.CutoffAcceptedSeq != q.CutoffAcceptedSeq || view.SnapshotCursor.PrefixSHA256 != q.PrefixSHA256 {
		return contractError("prefix_mismatch", "$.prefix_sha256")
	}
	if cfg.DynamicsConfigSHA256 != nil && view.ConfigSHA256 != *cfg.DynamicsConfigSHA256 {
		return contractError("config_mismatch", "$.config_sha256")
	}
	if view.Status != "present" {
		return contractError("invalid_state", "$.status")
	}
	return nil
}

// Fuse original lanes before filtering whole eligible keys: removing a hit from
// a lane first would change the RRF ordinal of every later candidate.
func policyBase(s memoryharness.CandidateSnapshot) ([]memoryharness.RankedCandidate, error) {
	count := 0
	for _, lane := range s.Lanes {
		count += len(lane)
	}
	all, err := memoryharness.RRFReference(s.Lanes, count)
	if err != nil {
		return nil, policyError(err)
	}
	eligible := make([]memoryharness.RankedCandidate, 0, len(all))
	for _, row := range all {
		c := row.Candidate
		if c.Approved && c.Active && !c.Deleted && c.SourceVerified {
			eligible = append(eligible, row)
		}
	}
	return eligible, nil
}

func rankPolicy(s memoryharness.CandidateSnapshot, q ProbeV2, views []StateViewV2, features []ImportanceFeatureV1, cfg RankerConfigV2, p1 bool, reads *int64) (PolicyDecisionV2, error) {
	if err := policyInputs(s, q, views, cfg); err != nil {
		return PolicyDecisionV2{}, err
	}
	byRef, err := policyViews(views, q, cfg)
	if err != nil {
		return PolicyDecisionV2{}, err
	}
	if p1 {
		if err := ValidateTyped("P1FeatureFileV1", P1FeatureFileV1{Protocol: "p1-feature-file-v1", FeatureContractSHA256: P1ContractSHA256, Features: features}); err != nil {
			return PolicyDecisionV2{}, err
		}
	} else if cfg.PolicyID == "V3-P1-IR" {
		return PolicyDecisionV2{}, contractError("missing_feature", "$.features")
	}
	base, err := policyBase(s)
	if err != nil {
		return PolicyDecisionV2{}, err
	}
	rows, ranked, err := policyRanks(base, q, byRef, features, cfg, reads)
	if err != nil {
		return PolicyDecisionV2{}, err
	}
	var selection memoryharness.BaselineSelection
	if cfg.PolicyID == "B0" {
		selection, err = memoryharness.RankAndPack(s, baselineProbe(q), baselinePolicy())
	} else {
		selection, err = memoryharness.PackContext(ranked, q.FinalK, q.ContextBudget)
	}
	if err != nil {
		return PolicyDecisionV2{}, policyError(err)
	}
	return policyDecision(rows, selection, q, cfg)
}

func policyRanks(base []memoryharness.RankedCandidate, q ProbeV2, states map[RefV2]StateViewV2, features []ImportanceFeatureV1, cfg RankerConfigV2, reads *int64) ([]RankRow, []memoryharness.RankedCandidate, error) {
	rows := make([]RankRow, len(base))
	baseDense := int64(0)
	for i, candidate := range base {
		if i == 0 || candidate.Score != base[i-1].Score {
			baseDense++
		}
		rows[i] = RankRow{Ref: candidate.Candidate.Ref, BaseRRFScore: candidate.Score, BaseDenseRank: baseDense, Auxiliary: []AuxRank{}}
	}
	for _, lane := range cfg.Lanes {
		if lane.Kind != "relevance" {
			if err := policyAux(rows, base, lane.Kind, q, states, features, cfg, reads); err != nil {
				return nil, nil, err
			}
		}
	}
	for i := range rows {
		if err := policyScore(&rows[i], cfg); err != nil {
			return nil, nil, err
		}
	}
	return policyOrder(rows, base, policyPreservesBaselineOrder(cfg))
}

func policyAux(rows []RankRow, base []memoryharness.RankedCandidate, kind string, q ProbeV2, states map[RefV2]StateViewV2, features []ImportanceFeatureV1, cfg RankerConfigV2, reads *int64) error {
	values := make([]float64, len(rows))
	for i, row := range base {
		value, err := policyValue(row.Candidate, kind, q, states, features, cfg)
		if err != nil {
			return err
		}
		if kind == "importance" && reads != nil {
			*reads++
		}
		values[i] = value
	}
	ordered := append([]float64{}, values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] > ordered[j] })
	dense := map[float64]int64{}
	for _, value := range ordered {
		if _, exists := dense[value]; !exists {
			dense[value] = int64(len(dense) + 1)
		}
	}
	for i, value := range values {
		rows[i].Auxiliary = append(rows[i].Auxiliary, AuxRank{Kind: kind, Value: value, DenseRank: dense[value]})
	}
	return nil
}

func policyValue(candidate memoryharness.Candidate, kind string, q ProbeV2, states map[RefV2]StateViewV2, features []ImportanceFeatureV1, cfg RankerConfigV2) (float64, error) {
	switch kind {
	case "recency":
		if candidate.UpdatedAtNS > q.AtNS {
			return 0, contractError("future_event", "$.updated_at_ns")
		}
		return scalarDecay(1, q.AtNS-candidate.UpdatedAtNS, cfg.RecencyTauNS)
	case "state":
		view, exists := states[candidate.Ref]
		if !exists {
			return 0, contractError("missing_state", "$.states")
		}
		return view.Value, nil
	case "importance":
		return policyImportance(features, candidate.Ref, q)
	default:
		return 0, contractError("config_mismatch", "$.lanes")
	}
}

func policyScore(row *RankRow, cfg RankerConfigV2) error {
	if cfg.PolicyID == "B0" {
		row.FusionScore = row.BaseRRFScore
		return nil
	}
	aux := 0
	for i, lane := range cfg.Lanes {
		dense := row.BaseDenseRank
		if lane.Kind != "relevance" {
			dense = row.Auxiliary[aux].DenseRank
			aux++
		}
		term := lane.Weight / (60 + float64(dense))
		if i == 0 {
			row.FusionScore = term
		} else {
			row.FusionScore += term
		}
		if math.IsNaN(row.FusionScore) || math.IsInf(row.FusionScore, 0) {
			return contractError("invalid_value", "$.fusion_score")
		}
	}
	return nil
}

// policyPreservesBaselineOrder is the versioned, owner-selected exception.
// It is evaluated only after every auxiliary value and score was computed.
func policyPreservesBaselineOrder(cfg RankerConfigV2) bool {
	if cfg.PolicyID == "B0" || cfg.PolicyVersion != "p1-zeroaux-order-v1" {
		return false
	}
	for _, lane := range cfg.Lanes {
		if lane.Kind != "relevance" && lane.Weight != 0 {
			return false
		}
	}
	return true
}

func policyOrder(rows []RankRow, base []memoryharness.RankedCandidate, preserveBaseline bool) ([]RankRow, []memoryharness.RankedCandidate, error) {
	byRef := make(map[RefV2]memoryharness.RankedCandidate, len(base))
	for _, row := range base {
		byRef[row.Candidate.Ref] = row
	}
	if !preserveBaseline {
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := rows[i], rows[j]
			if a.FusionScore != b.FusionScore {
				return a.FusionScore > b.FusionScore
			}
			x, y := byRef[a.Ref].Candidate, byRef[b.Ref].Candidate
			if x.Trust != y.Trust {
				return x.Trust > y.Trust
			}
			if x.UpdatedAtNS != y.UpdatedAtNS {
				return x.UpdatedAtNS > y.UpdatedAtNS
			}
			return policyRefKey(a.Ref) < policyRefKey(b.Ref)
		})
	}
	ranked := make([]memoryharness.RankedCandidate, len(rows))
	for i := range rows {
		rows[i].FinalOrdinal = int64(i + 1)
		ranked[i] = byRef[rows[i].Ref]
		ranked[i].Score = rows[i].FusionScore
	}
	return rows, ranked, nil
}

func policyRefKey(ref RefV2) string { return ref.Origin + ":" + ref.Collection + ":" + ref.ID }

func policyDecision(rows []RankRow, selected memoryharness.BaselineSelection, q ProbeV2, cfg RankerConfigV2) (PolicyDecisionV2, error) {
	refs := make([]RefV2, 0, len(selected.Selected))
	for _, row := range selected.Selected {
		refs = append(refs, row.Candidate.Ref)
	}
	projection := PolicyDecisionV2HashProjection{QueryID: q.QueryID, PolicyID: cfg.PolicyID, PolicyVersion: cfg.PolicyVersion, CandidateSnapshotSHA256: q.CandidateSnapshotSHA256, PrefixSHA256: q.PrefixSHA256, RankerConfigSHA256: cfg.ConfigSHA256, Ranks: rows, Selected: refs, ContextCostUsed: selected.UsedContextUnits, ContextBudget: q.ContextBudget, Status: "ok", Reason: "none"}
	return scalarSeal[PolicyDecisionV2]("PolicyDecisionV2", projection)
}

// RunPolicies runs registered policies over explicit native inputs. It never
// opens files and never reads a hidden checkpoint or feature store.
func RunPolicies(events EventFileV2, probes ProbeFileV2, candidates memoryharness.CandidateFile, cfg DynamicsRunConfigV2) ([]PredictionRunV2, error) {
	return runPolicies(events, probes, candidates, nil, cfg, false)
}

type policyWork struct {
	snapshot   SnapshotV2
	position   int
	seen       map[string]bool
	prefix     string
	seq        int64
	cost       OfflineCost
	at         int64
	runtimeErr error
}

func runPolicies(events EventFileV2, probes ProbeFileV2, candidates memoryharness.CandidateFile, features []ImportanceFeatureV1, cfg DynamicsRunConfigV2, p1 bool) ([]PredictionRunV2, error) {
	groups, err := runInputs(events, probes, candidates, cfg)
	if err != nil {
		return nil, err
	}
	if p1 {
		if err = ValidateTyped("P1FeatureFileV1", P1FeatureFileV1{Protocol: "p1-feature-file-v1", FeatureContractSHA256: P1ContractSHA256, Features: features}); err != nil {
			return nil, err
		}
	}
	snapshots := make(map[string]memoryharness.CandidateSnapshot, len(candidates.Snapshots))
	for _, snapshot := range candidates.Snapshots {
		snapshots[snapshot.SnapshotID] = snapshot
	}
	result := make([]PredictionRunV2, 0, len(cfg.Configs))
	for _, policy := range cfg.Configs {
		run, runErr := runOnePolicy(events, groups[policy.PolicyID], snapshots, features, cfg, policy, p1)
		if runErr != nil {
			return nil, runErr
		}
		result = append(result, run)
	}
	return result, nil
}

func runInputs(events EventFileV2, probes ProbeFileV2, candidates memoryharness.CandidateFile, cfg DynamicsRunConfigV2) (map[PolicyID][]ProbeV2, error) {
	for _, input := range []struct {
		name  string
		value any
	}{{"EventFileV2", events}, {"ProbeFileV2", probes}, {"DynamicsRunConfigV2", cfg}} {
		if err := ValidateTyped(input.name, input.value); err != nil {
			return nil, err
		}
	}
	if err := memoryharness.ValidateTyped("CandidateFile", candidates); err != nil {
		return nil, policyError(err)
	}
	if events.StreamID != probes.StreamID {
		return nil, contractError("stream_mismatch", "$.stream_id")
	}
	if events.ClockDomain != probes.ClockDomain {
		return nil, contractError("clock_domain_mismatch", "$.clock_domain")
	}
	if err := sourceAliases(events.Events); err != nil {
		return nil, err
	}
	groups := make(map[PolicyID][]ProbeV2, len(cfg.PolicyIDs))
	versions := make(map[PolicyID]string, len(cfg.Configs))
	for _, policy := range cfg.Configs {
		groups[policy.PolicyID] = []ProbeV2{}
		versions[policy.PolicyID] = policy.Ranker.PolicyVersion
		if policy.Dynamics != nil && policy.Dynamics.ClockDomain != events.ClockDomain {
			return nil, contractError("clock_domain_mismatch", "$.configs[*].dynamics.clock_domain")
		}
	}
	for _, q := range probes.Probes {
		if version, found := versions[q.PolicyID]; !found || version != q.PolicyVersion {
			return nil, contractError("config_mismatch", "$.probes[*].policy_id")
		}
		groups[q.PolicyID] = append(groups[q.PolicyID], q)
	}
	if err := runSchedules(groups, cfg, events); err != nil {
		return nil, err
	}
	if err := runCandidateBindings(groups[cfg.PolicyIDs[0]], candidates); err != nil {
		return nil, err
	}

	return groups, nil
}

func runCandidateBindings(queries []ProbeV2, candidates memoryharness.CandidateFile) error {
	snapshots := map[string]memoryharness.CandidateSnapshot{}
	for _, snapshot := range candidates.Snapshots {
		snapshots[snapshot.SnapshotID] = snapshot
	}
	referenced := map[string]bool{}
	for _, q := range queries {
		snapshot, exists := snapshots[q.CandidateSnapshotID]
		if !exists {
			return contractError("schedule_mismatch", "$.probes[*].candidate_snapshot_id")
		}
		if err := memoryharness.ValidateBaselineInputs(memoryharness.ProbeFile{ContractID: "arkism-offline-b0-v1", Probes: []memoryharness.Probe{baselineProbe(q)}}, memoryharness.CandidateFile{ContractID: "arkism-offline-b0-v1", Snapshots: []memoryharness.CandidateSnapshot{snapshot}}, baselinePolicy()); err != nil {
			return policyError(err)
		}
		hash, hashErr := memoryharness.CanonicalSHA256("CandidateSnapshot", snapshot)
		if hashErr != nil {
			return policyError(hashErr)
		}
		if hash != q.CandidateSnapshotSHA256 {
			return contractError("hash_mismatch", "$.probes[*].candidate_snapshot_sha256")
		}
		referenced[q.CandidateSnapshotID] = true
	}
	if len(referenced) != len(snapshots) {
		return contractError("schedule_mismatch", "$.snapshots")
	}

	return nil
}

func sourceAliases(events []EventV2) error {
	type identity struct {
		scope                  ScopeV2
		dimension              Dimension
		origin, collection, id string
		revision               int64
	}
	seen := map[identity]string{}
	for _, event := range events {
		ref := event.Key.Target
		key := identity{event.Key.Scope, event.Key.Dimension, ref.Origin, ref.Collection, ref.ID, ref.Revision}
		if prior, exists := seen[key]; exists && prior != ref.ContentSHA256 {
			return contractError("revision_mismatch", "$.events[*].key.target")
		}
		seen[key] = ref.ContentSHA256
	}
	return nil
}

func sameRunQuery(a, b ProbeV2) bool {
	return a.QueryID == b.QueryID && a.StreamID == b.StreamID && a.Scope == b.Scope && a.Dimension == b.Dimension && a.AtNS == b.AtNS && a.ClockDomain == b.ClockDomain && a.CutoffAcceptedSeq == b.CutoffAcceptedSeq && a.CandidateSnapshotID == b.CandidateSnapshotID && a.CandidateSnapshotSHA256 == b.CandidateSnapshotSHA256 && a.FinalK == b.FinalK && a.ContextBudget == b.ContextBudget
}

func runSchedules(groups map[PolicyID][]ProbeV2, cfg DynamicsRunConfigV2, events EventFileV2) error {
	first := groups[cfg.PolicyIDs[0]]
	reference := make(map[string]ProbeV2, len(first))
	for _, q := range first {
		reference[q.QueryID] = q
	}
	for _, id := range cfg.PolicyIDs {
		if len(groups[id]) != len(first) {
			return contractError("schedule_mismatch", "$.probes")
		}
		for _, q := range groups[id] {
			other, found := reference[q.QueryID]
			if !found || !sameRunQuery(q, other) {
				return contractError("schedule_mismatch", "$.probes[*]")
			}
		}
	}
	stateful := false
	for _, policy := range cfg.Configs {
		stateful = stateful || policy.Dynamics != nil
	}
	if !stateful {
		return nil // Do not narrow the original multi-scope stateless baseline.
	}
	var scope ScopeV2
	var dimension Dimension
	if len(events.Events) > 0 {
		scope, dimension = events.Events[0].Key.Scope, events.Events[0].Key.Dimension
	} else if len(first) > 0 {
		scope, dimension = first[0].Scope, first[0].Dimension
	}
	for _, event := range events.Events {
		if event.Key.Scope != scope || event.Key.Dimension != dimension {
			return contractError("scope_mismatch", "$.events[*].key")
		}
	}
	for _, q := range first {
		if q.Scope != scope || q.Dimension != dimension {
			return contractError("scope_mismatch", "$.probes[*].scope")
		}
	}
	return nil
}

func parentSeed(events EventFileV2, q ProbeV2, cfg DynamicsConfigV2) (SnapshotV2, error) {
	initial, err := CanonicalSHA256("StreamInitialPrefixProjectionV2", StreamInitialPrefixProjectionV2{StreamID: events.StreamID, ClockDomain: events.ClockDomain, Scope: q.Scope, Dimension: q.Dimension, ConfigSHA256: cfg.ConfigSHA256})
	if err != nil {
		return SnapshotV2{}, err
	}
	cursor := StreamCursorV2{StreamID: events.StreamID, ClockDomain: events.ClockDomain, PrefixSHA256: initial}
	fence, err := scalarSeal[LifecycleFence]("LifecycleFence", LifecycleFenceHashProjection{Namespace: "synthetic", Epoch: 1, MinimumAcceptedSeq: 0, Entries: []FenceEntry{}})
	if err != nil {
		return SnapshotV2{}, err
	}
	index, err := scalarSeal[ReceiptIndexV2]("ReceiptIndexV2", ReceiptIndexV2HashProjection{StreamID: events.StreamID, ClockDomain: events.ClockDomain, Cursor: cursor, Receipts: []ReceiptV2{}, EvidenceConsumptions: []EvidenceConsumption{}, LifecycleFence: fence})
	if err != nil {
		return SnapshotV2{}, err
	}
	return scalarSeal[SnapshotV2]("SnapshotV2", SnapshotV2HashProjection{Protocol: "dynamics-snapshot-v2", StreamID: events.StreamID, Scope: q.Scope, Dimension: q.Dimension, ConfigSHA256: cfg.ConfigSHA256, Cursor: cursor, States: []StateV2{}, Receipts: index})
}

func parentAdmit(snapshot SnapshotV2, event EventV2) (SnapshotV2, error) {
	if event.Kind != "activate" {
		return snapshot, nil
	}
	if err := replayTargetAlias(snapshot, event.Key.Target); err != nil {
		return SnapshotV2{}, err
	}
	for _, entry := range snapshot.Receipts.LifecycleFence.Entries {
		if entry.Scope == event.Key.Scope && entry.Target == event.Key.Target {
			return snapshot, nil // Original transition checks current/invalidated status.
		}
	}
	snapshot = scalarClone(snapshot)
	fence := snapshot.Receipts.LifecycleFence
	fence.Entries = append(fence.Entries, FenceEntry{Scope: event.Key.Scope, Target: event.Key.Target, Status: "current", SourceRefs: []EvidenceRef{}, Reason: "none"})
	sort.Slice(fence.Entries, func(i, j int) bool {
		return scopeRefKey(fence.Entries[i].Scope, fence.Entries[i].Target) < scopeRefKey(fence.Entries[j].Scope, fence.Entries[j].Target)
	})
	var err error
	snapshot.Receipts.LifecycleFence, err = scalarSeal[LifecycleFence]("LifecycleFence", LifecycleFenceHashProjection{Namespace: fence.Namespace, Epoch: fence.Epoch, MinimumAcceptedSeq: fence.MinimumAcceptedSeq, Entries: fence.Entries})
	if err != nil {
		return SnapshotV2{}, err
	}
	index := snapshot.Receipts
	snapshot.Receipts, err = scalarSeal[ReceiptIndexV2]("ReceiptIndexV2", ReceiptIndexV2HashProjection{StreamID: index.StreamID, ClockDomain: index.ClockDomain, Cursor: index.Cursor, Receipts: index.Receipts, EvidenceConsumptions: index.EvidenceConsumptions, LifecycleFence: index.LifecycleFence})
	if err != nil {
		return SnapshotV2{}, err
	}
	return replaySeal(snapshot)
}

func projectedEvent(source EventV2, cfg DynamicsConfigV2) (EventV2, error) {
	event := scalarClone(source)
	event.Key.ConfigSHA256 = cfg.ConfigSHA256
	p := EventLogicalProjectionV2{Protocol: event.Protocol, StreamID: event.StreamID, EventID: event.EventID, Key: event.Key, Kind: event.Kind, ClockDomain: event.ClockDomain, ObservedAtNS: event.ObservedAtNS, Task: event.Task, Evidence: event.Evidence, Outcome: event.Outcome, Attribution: event.Attribution, Strength: event.Strength, Reason: event.Reason}
	hash, err := CanonicalSHA256("EventLogicalProjectionV2", p)
	event.PayloadSHA256 = hash
	return event, err
}

func runOnePolicy(events EventFileV2, queries []ProbeV2, snapshots map[string]memoryharness.CandidateSnapshot, features []ImportanceFeatureV1, runCfg DynamicsRunConfigV2, policy PolicyConfig, p1 bool) (PredictionRunV2, error) {
	predictions := make([]PredictionV2, len(queries))
	work := policyWork{seen: map[string]bool{}}
	order := make([]int, len(queries))
	for i := range queries {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return queries[order[i]].CutoffAcceptedSeq < queries[order[j]].CutoffAcceptedSeq })
	if len(order) > 0 && policy.Dynamics != nil {
		var err error
		work.snapshot, err = parentSeed(events, queries[order[0]], *policy.Dynamics)
		if err != nil {
			return PredictionRunV2{}, err
		}
	}
	if policy.Dynamics != nil && len(order) > 0 {
		work.prefix = work.snapshot.Cursor.PrefixSHA256
	}
	for _, index := range order {
		q := queries[index]
		if err := work.advance(events, q, policy.Dynamics); err != nil {
			return PredictionRunV2{}, err
		}
		candidate := snapshots[q.CandidateSnapshotID]
		if err := sourceCandidateFence(events, q, candidate); err != nil {
			return PredictionRunV2{}, err
		}
		var prediction PredictionV2
		reads := int64(0)
		if work.runtimeErr != nil {
			prediction = failedPolicyPrediction(q.QueryID, work.runtimeErr)
		} else {
			prediction, reads = runPrediction(candidate, q, work.snapshot, features, policy, p1)
		}
		predictions[index] = prediction
		work.cost.FeatureReads += reads
		if prediction.Status == "failed" {
			work.cost.FailedPredictions++
		}
	}
	p := PredictionRunV2HashProjection{Protocol: "dynamics-prediction-run-v2", RunID: runCfg.RunID, PolicyID: policy.PolicyID, PolicyVersion: policy.Ranker.PolicyVersion, InputManifestSHA256: runCfg.InputManifestSHA256, RunConfigSHA256: runCfg.ConfigSHA256, RegistrySHA256: runCfg.RegistrySHA256, Predictions: predictions, Cost: work.cost}
	return scalarSeal[PredictionRunV2]("PredictionRunV2", p)
}

func (work *policyWork) advance(events EventFileV2, q ProbeV2, cfg *DynamicsConfigV2) error {
	if cfg == nil && (work.prefix == "" || len(events.Events) == 0) {
		scope, dimension := q.Scope, q.Dimension
		if len(events.Events) > 0 {
			scope, dimension = events.Events[0].Key.Scope, events.Events[0].Key.Dimension
		}
		var err error
		work.prefix, err = CanonicalSHA256("StreamInitialPrefixProjectionV2", StreamInitialPrefixProjectionV2{StreamID: events.StreamID, ClockDomain: events.ClockDomain, Scope: scope, Dimension: dimension, ConfigSHA256: events.ConfigSHA256})
		if err != nil {
			return err
		}
	}
	for work.position < len(events.Events) {
		event := events.Events[work.position]
		retry := work.seen[event.EventID]
		if !retry && event.AcceptedSeq > q.CutoffAcceptedSeq {
			break
		}
		if err := work.delivery(event, retry, cfg); err != nil {
			return err
		}
		work.position++
	}
	if work.seq != q.CutoffAcceptedSeq || work.prefix != q.PrefixSHA256 {
		return contractError("prefix_mismatch", "$.probes[*].prefix_sha256")
	}
	if q.AtNS < work.at {
		return contractError("time_regression", "$.probes[*].at_ns")
	}
	return nil
}

func (work *policyWork) delivery(event EventV2, retry bool, cfg *DynamicsConfigV2) error {
	work.cost.EventInputs++
	if retry {
		work.cost.DuplicateEvents++
	} else {
		work.cost.UniqueEvents++
	}
	if cfg != nil {
		projected, err := projectedEvent(event, *cfg)
		if err != nil {
			return err
		}
		event = projected
	}
	if !retry {
		var err error
		work.prefix, err = CanonicalSHA256("StreamAcceptedStepProjectionV2", StreamAcceptedStepProjectionV2{PreviousPrefixSHA256: work.prefix, StreamID: event.StreamID, ClockDomain: event.ClockDomain, AcceptedSeq: event.AcceptedSeq, AcceptedAtNS: event.AcceptedAtNS, PayloadSHA256: event.PayloadSHA256})
		if err != nil {
			return err
		}
		work.seq, work.at = event.AcceptedSeq, event.AcceptedAtNS
	}
	if cfg != nil && work.runtimeErr == nil {
		// A policy-specific unavailable/qualification failure cannot erase
		// scheduled rows or prevent independent policies from executing.
		work.runtimeErr = work.scalarDelivery(event, retry, *cfg)
	}
	work.seen[event.EventID] = true
	return nil
}

func (work *policyWork) scalarDelivery(event EventV2, retry bool, cfg DynamicsConfigV2) error {
	var err error
	if !retry {
		work.snapshot, err = parentAdmit(work.snapshot, event)
		if err != nil {
			return err
		}
	}
	cutoff := max(work.snapshot.Cursor.AcceptedSeq, event.AcceptedSeq)
	next, stats, err := ReplayPrefix(work.snapshot, []EventV2{event}, cutoff, cfg)
	if err != nil {
		return err
	}
	work.snapshot = next
	work.cost.StateUpdates += stats.AppliedCount
	work.cost.DuplicateEvidence += stats.DuplicateEvidenceCount
	return nil
}

func runPrediction(candidate memoryharness.CandidateSnapshot, q ProbeV2, snapshot SnapshotV2, features []ImportanceFeatureV1, policy PolicyConfig, p1 bool) (PredictionV2, int64) {
	views := []StateViewV2{}
	if policy.Dynamics != nil {
		base, err := policyBase(candidate)
		if err != nil {
			return failedPolicyPrediction(q.QueryID, err), 0
		}
		for _, row := range base {
			var view StateViewV2
			var err error
			if policy.PolicyID == "V3-P1-FD" {
				view, err = policyFDView(snapshot, q.AtNS, row.Candidate.Ref, *policy.Dynamics)
			} else {
				view, err = ReadAt(snapshot, q.AtNS, row.Candidate.Ref, *policy.Dynamics)
			}
			if err != nil {
				return failedPolicyPrediction(q.QueryID, err), 0
			}
			views = append(views, view)
		}
	}
	reads := int64(0)
	decision, err := rankPolicy(candidate, q, views, features, policy.Ranker, p1, &reads)
	if err != nil {
		return failedPolicyPrediction(q.QueryID, err), reads
	}
	return PredictionV2{QueryID: q.QueryID, Status: "ok", Decision: &decision, Reason: "none"}, reads
}

func failedPolicyPrediction(query string, err error) PredictionV2 {
	problem := policyError(err).(*ContractErrorV2)
	return PredictionV2{QueryID: query, Status: "failed", Error: problem, Reason: "none"}
}

// This is a negative consistency check on supplied native source observations,
// not authority acquisition or a second scalar transition engine.
func sourceCandidateFence(events EventFileV2, q ProbeV2, snapshot memoryharness.CandidateSnapshot) error {
	invalid := map[RefV2]bool{}
	seen := map[string]bool{}
	for _, event := range events.Events {
		if !seen[event.EventID] && event.AcceptedSeq > q.CutoffAcceptedSeq {
			break
		}
		seen[event.EventID] = true
		if event.Kind == "invalidate" && event.Key.Scope == q.Scope {
			invalid[event.Key.Target] = true
		}
	}
	for _, lane := range snapshot.Lanes {
		for _, candidate := range lane {
			if invalid[candidate.Ref] && candidate.Approved && candidate.Active && !candidate.Deleted && candidate.SourceVerified {
				return contractError("invalidated_target", "$.snapshots[*].lanes[*][*]")
			}
		}
	}
	return nil
}
