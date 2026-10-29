package dynamicsv2

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"

	"github.com/FelixBaek/arkism/internal/memoryharness"
)

func policyFixture(t *testing.T, id PolicyID) (memoryharness.CandidateSnapshot, ProbeV2, []StateViewV2, RankerConfigV2) {
	t.Helper()
	registry := fixtureRegistry(t)
	var cfg RankerConfigV2
	for _, entry := range registry.Entries {
		if entry.PolicyID == id {
			cfg = entry.Ranker
		}
	}
	first := memoryharness.Candidate{Scope: fixtureScope(), Ref: fixtureRef(), Reasons: []string{"exact_keyword"}, Trust: .25, UpdatedAtNS: 0, ObservedSeq: 1, EffectiveAtNS: 0, ContextCostUnits: 2, Approved: true, Active: true, SourceVerified: true, Authority: "data_only_untrusted"}
	first.Ref.ID = "a"
	second := first
	second.Ref.ID, second.Ref.ContentSHA256 = "b", fixtureSHA("b")
	second.Reasons, second.Trust, second.UpdatedAtNS = []string{}, .5, 1000000000
	snapshot := memoryharness.CandidateSnapshot{SnapshotID: "candidates", StreamID: "s", Scope: fixtureScope(), AsOfNS: 1000000000, AsOfSeq: 2, Lanes: [][]memoryharness.Candidate{{first, second}, {}, {}}}
	hash, err := memoryharness.CanonicalSHA256("CandidateSnapshot", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	probe := ProbeV2{Protocol: "dynamics-probe-v2", QueryID: "q", StreamID: "s", Scope: fixtureScope(), Dimension: "work_task", AtNS: 1000000000, ClockDomain: "c", CutoffAcceptedSeq: 2, CandidateSnapshotID: snapshot.SnapshotID, CandidateSnapshotSHA256: hash, PrefixSHA256: fixtureSHA("prefix"), FinalK: 2, ContextBudget: memoryharness.ContextBudget{Unit: "fixture_units_v0", Limit: 4}, PolicyID: id, PolicyVersion: cfg.PolicyVersion}
	views := []StateViewV2{}
	if cfg.DynamicsConfigSHA256 != nil {
		for i, candidate := range snapshot.Lanes[0] {
			key := DynamicsKeyV2{Scope: probe.Scope, Target: candidate.Ref, Dimension: probe.Dimension, ContractSHA256: ContractSHA256, ConfigSHA256: *cfg.DynamicsConfigSHA256}
			views = append(views, StateViewV2{Key: key, AtNS: probe.AtNS, ClockDomain: probe.ClockDomain, CutoffAcceptedSeq: 2, SnapshotSHA256: fixtureSHA("snapshot"), SnapshotCursor: StreamCursorV2{StreamID: "s", ClockDomain: "c", AcceptedAtNS: 10, AcceptedSeq: 2, PrefixSHA256: probe.PrefixSHA256}, SourceStateRevision: 1, ActivatedAtNS: 0, ActivationAcceptedSeq: 1, Value: []float64{.25, .75}[i], Status: "present", Reason: "activation", StreamID: "s", Scope: probe.Scope, Dimension: probe.Dimension, ConfigSHA256: key.ConfigSHA256})
		}
	}
	return snapshot, probe, views, cfg
}

func policySealSnapshot(t *testing.T, snapshot memoryharness.CandidateSnapshot, q *ProbeV2) {
	t.Helper()
	h, err := memoryharness.CanonicalSHA256("CandidateSnapshot", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	q.CandidateSnapshotSHA256 = h
}

func TestPolicyRegistryExactExistingRegression(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/research/memory-harness/policy_registry.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := DecodeStrict(raw, "PolicyRegistryV2")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(value, selectedPolicyRegistry(t)) {
		t.Fatal("registry differs from versioned DYN01 engineering fixture")
	}
	if value.(PolicyRegistryV2).RegistrySHA256 != "17c58cf192536dc7f7fe7d3987cde09ea42265cb92982e3d811649def989b4f5" {
		t.Fatal("independently encoded registry graph literal changed")
	}
}

func TestPolicyBaseLiteralDenseAndFusion(t *testing.T) {
	s, q, v, cfg := policyFixture(t, "B2")
	got, err := RankPolicy(s, q, v, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ranks[0].Ref.ID != "b" || got.Ranks[1].Ref.ID != "a" {
		t.Fatal("exact auxiliary tie must use trust before original key")
	}
	a, b := got.Ranks[1], got.Ranks[0]
	if a.BaseRRFScore != 2.0/61 || b.BaseRRFScore != 1.0/62 || a.BaseDenseRank != 1 || b.BaseDenseRank != 2 {
		t.Fatalf("literal base ordinal/dense mismatch: %+v", got.Ranks)
	}
	want := math.Float64frombits(0x3fa0a6c92bff7560)
	if a.FusionScore != want || b.FusionScore != want || got.ContextCostUsed != 4 {
		t.Fatal("literal two-lane sum/packing mismatch")
	}
}

func TestPolicyB0DirectAndZeroAuxiliaryEquivalence(t *testing.T) {
	for _, id := range []PolicyID{"B0", "B1", "B2", "V3-P1-FD", "V3-P1-NOTIME", "V3-P1-NOOUT"} {
		t.Run(string(id), func(t *testing.T) {
			s, q, views, cfg := policyFixture(t, id)
			for i := range cfg.Lanes {
				if cfg.Lanes[i].Kind != "relevance" {
					cfg.Lanes[i].Weight = 0
				}
			}
			cfg = sealFixture(t, "RankerConfigV2", cfg)
			got, err := RankPolicy(s, q, views, cfg)
			if err != nil {
				t.Fatal(err)
			}
			base, err := memoryharness.RankAndPack(s, baselineProbe(q), baselinePolicy())
			if err != nil {
				t.Fatal(err)
			}
			for i, selected := range base.Selected {
				if selected.Candidate.Ref != got.Selected[i] {
					t.Fatal("zero auxiliary differs from immutable B0")
				}
			}
		})
	}
}

func TestPolicyOriginalOrdinalAfterEligibility(t *testing.T) {
	s, q, views, cfg := policyFixture(t, "B1")
	s.Lanes[0][0].Approved = false
	policySealSnapshot(t, s, &q)
	got, err := RankPolicy(s, q, views, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ranks) != 1 || got.Ranks[0].BaseRRFScore != 1.0/62 {
		t.Fatal("filter compacted the original second ordinal")
	}
}

func TestPolicyPackingSkipsWholeLargeItem(t *testing.T) {
	s, q, views, cfg := policyFixture(t, "B0")
	s.Lanes[0][0].ContextCostUnits = 5
	policySealSnapshot(t, s, &q)
	got, err := RankPolicy(s, q, views, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Selected) != 1 || got.Selected[0].ID != "b" || got.ContextCostUsed != 2 || len(got.Ranks) != 2 {
		t.Fatal("whole-item context packing changed")
	}
}

func TestPolicyMissingStateEvenZeroAuxiliary(t *testing.T) {
	s, q, _, cfg := policyFixture(t, "B2")
	cfg.Lanes[1].Weight = 0
	cfg = sealFixture(t, "RankerConfigV2", cfg)
	_, err := RankPolicy(s, q, []StateViewV2{}, cfg)
	requireCode(t, err, "missing_state")
}

func TestPolicyInputAndOutputDoNotAlias(t *testing.T) {
	s, q, views, cfg := policyFixture(t, "B2")
	before, err := json.Marshal([]any{s, q, views, cfg})
	if err != nil {
		t.Fatal(err)
	}
	got, err := RankPolicy(s, q, views, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got.Selected[0].ID = "changed"
	got.Ranks[0].Auxiliary[0].Value = 0
	after, err := json.Marshal([]any{s, q, views, cfg})
	if err != nil || string(before) != string(after) {
		t.Fatal("input mutation or output alias")
	}
}

func TestPolicyCrossBindingNegatives(t *testing.T) {
	cases := []struct {
		name, code string
		mutate     func(*ProbeV2, []StateViewV2)
	}{
		{"scope", "snapshot_binding", func(q *ProbeV2, _ []StateViewV2) { q.Scope.Project = "other" }},
		{"clock", "clock_domain_mismatch", func(q *ProbeV2, _ []StateViewV2) { q.ClockDomain = "other" }},
		{"prefix", "prefix_mismatch", func(q *ProbeV2, _ []StateViewV2) { q.PrefixSHA256 = fixtureSHA("other") }},
		{"version", "config_mismatch", func(q *ProbeV2, _ []StateViewV2) { q.PolicyVersion = "other" }},
		{"hash", "hash_mismatch", func(q *ProbeV2, _ []StateViewV2) { q.CandidateSnapshotSHA256 = fixtureSHA("other") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, q, views, cfg := policyFixture(t, "B2")
			tc.mutate(&q, views)
			_, err := RankPolicy(s, q, views, cfg)
			requireCode(t, err, tc.code)
		})
	}
}

func TestPolicyNoFeatureAPIIsNotIRDefault(t *testing.T) {
	s, q, views, cfg := policyFixture(t, "V3-P1-IR")
	_, err := RankPolicy(s, q, views, cfg)
	requireCode(t, err, "missing_feature")
}

func TestPolicyEmptyNilAndFinalKBoundaries(t *testing.T) {
	s, q, _, cfg := policyFixture(t, "B0")
	_, err := RankPolicy(s, q, nil, cfg)
	requireCode(t, err, "invalid_type")
	q.FinalK = 0
	got, err := RankPolicy(s, q, []StateViewV2{}, cfg)
	if err != nil || len(got.Selected) != 0 || len(got.Ranks) != 2 {
		t.Fatal("final-k zero did not preserve complete ranks")
	}
}

func TestPolicyNativeErrorIdentity(t *testing.T) {
	codes := []string{"invalid_json", "invalid_unicode", "duplicate_key", "unknown_field", "missing_field", "invalid_type", "invalid_value", "unsupported_version", "unsupported_type", "hash_mismatch", "snapshot_conflict", "snapshot_binding", "schedule_mismatch", "invalid_state", "invalid_time", "invalid_half_life", "unsafe_path", "output_exists", "io_error"}
	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			err := policyError(&memoryharness.ContractError{Code: code, Path: "$.scope"})
			requireCode(t, err, code)
			if err.(*ContractErrorV2).Path != "$.scope" {
				t.Fatal("native known path lost")
			}
		})
	}
}

// Historical binary64 underflow is preserved by the versioned ordering exception.
func TestPolicyLiteralSubnormalFusionObservation(t *testing.T) {
	weight := math.SmallestNonzeroFloat64
	if weight/61 != 0 || weight/62 != 0 || math.Float64bits(weight) != 1 {
		t.Fatal("literal IEEE754 observation changed")
	}
}

func TestPolicyRegisteredTinyZeroAuxiliaryRequiredEquivalence(t *testing.T) {
	s, q, views, _ := policyFixture(t, "B1")
	registry := selectedPolicyRegistry(t)
	registry.Entries[1].Ranker.Lanes[0].Weight = math.SmallestNonzeroFloat64
	registry.Entries[1].Ranker.Lanes[1].Weight = 0
	registry.Entries[1].Ranker = sealFixture(t, "RankerConfigV2", registry.Entries[1].Ranker)
	registry = resealRegistry(t, registry)
	if err := ValidateTyped("PolicyRegistryV2", registry); err != nil {
		t.Fatal("counterexample has invalid actual registry:", err)
	}
	cfg := registry.Entries[1].Ranker
	q.PolicyVersion = cfg.PolicyVersion
	if err := ValidateTyped("RankerConfigV2", cfg); err != nil {
		t.Fatal("counterexample is not a valid bound config:", err)
	}
	got, err := RankPolicy(s, q, views, cfg)
	if err != nil {
		t.Fatal("counterexample rejected before rank:", err)
	}
	baseline, err := memoryharness.RankAndPack(s, baselineProbe(q), baselinePolicy())
	if err != nil {
		t.Fatal("counterexample has invalid B0 inputs:", err)
	}
	evidence, err := json.Marshal(struct {
		Registry PolicyRegistryV2                `json:"registry"`
		Snapshot memoryharness.CandidateSnapshot `json:"snapshot"`
		Probe    ProbeV2                         `json:"probe"`
		Decision PolicyDecisionV2                `json:"decision"`
		Baseline memoryharness.BaselineSelection `json:"baseline"`
	}{registry, s, q, got, baseline})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("literal_typed_counterexample=%s", evidence)
	t.Logf("weight_bits=%016x first_fusion_bits=%016x second_fusion_bits=%016x", math.Float64bits(cfg.Lanes[0].Weight), math.Float64bits(got.Ranks[0].FusionScore), math.Float64bits(got.Ranks[1].FusionScore))
	if got.Selected[0] != baseline.Selected[0].Candidate.Ref {
		t.Fatalf("required equivalence differs despite valid typed registry: dynamic=%s B0=%s", got.Selected[0].ID, baseline.Selected[0].Candidate.Ref.ID)
	}
}

func TestPolicyStateViewsMustShareOneSnapshot(t *testing.T) {
	s, q, views, cfg := policyFixture(t, "B2")
	views[1].SnapshotSHA256 = fixtureSHA("different individually valid snapshot")
	if err := ValidateTyped("StateViewV2", views[1]); err != nil {
		t.Fatal(err)
	}
	_, err := RankPolicy(s, q, views, cfg)
	requireCode(t, err, "snapshot_binding")
}

func TestPolicyAblationFlagsAndUnsupportedFallback(t *testing.T) {
	registry := fixtureRegistry(t)
	if registry.Entries[5].Dynamics.DecayEnabled || !registry.Entries[5].Dynamics.OutcomeJumpEnabled ||
		!registry.Entries[6].Dynamics.DecayEnabled || registry.Entries[6].Dynamics.OutcomeJumpEnabled {
		t.Fatal("ablation switches differ from original B2 inheritance")
	}
	s, q, views, cfg := policyFixture(t, "B2")
	cfg.Fallback = "recorded_b0"
	_, err := RankPolicy(s, q, views, cfg)
	requireCode(t, err, "unsupported_fallback")
}

func TestPolicyExactAuxiliaryFloatTiesNotEpsilon(t *testing.T) {
	s, q, views, cfg := policyFixture(t, "B2")
	views[0].Value, views[1].Value = .25, math.Nextafter(.25, 1)
	cfg.Lanes[0].Weight = 0
	cfg = sealFixture(t, "RankerConfigV2", cfg)
	got, err := RankPolicy(s, q, views, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ranks[0].Ref.ID != "b" || got.Ranks[0].Auxiliary[0].DenseRank != 1 || got.Ranks[1].Auxiliary[0].DenseRank != 2 {
		t.Fatal("adjacent binary64 values were rounded into an equal rank")
	}
}

func TestNativeStatelessThreeScopeEmptySourcePreservesBaseline(t *testing.T) {
	registry := fixtureRegistry(t)
	cfg := DynamicsRunConfigV2{Protocol: "dynamics-run-config-v2", RunID: "three_scope", InputManifestSHA256: fixtureSHA("manifest"), RegistrySHA256: registry.RegistrySHA256, PolicyIDs: []PolicyID{"B0"}, Configs: []PolicyConfig{{PolicyID: "B0", Ranker: registry.Entries[0].Ranker}}, ResourceLimits: fixtureLimits(), Registry: registry}
	cfg = sealFixture(t, "DynamicsRunConfigV2", cfg)
	events := EventFileV2{Protocol: "dynamics-event-file-v2", StreamID: "s", ClockDomain: "c", ConfigSHA256: registry.Entries[2].Dynamics.ConfigSHA256, Events: []EventV2{}}
	probes := ProbeFileV2{Protocol: "dynamics-probe-file-v2", StreamID: "s", ClockDomain: "c", Probes: []ProbeV2{}}
	candidates := memoryharness.CandidateFile{ContractID: "arkism-offline-b0-v1", Snapshots: []memoryharness.CandidateSnapshot{}}
	for _, name := range []string{"one", "two", "three"} {
		scope := fixtureScope()
		scope.Project = name
		snapshot := memoryharness.CandidateSnapshot{SnapshotID: name, StreamID: "s", Scope: scope, AsOfNS: 10, AsOfSeq: 0, Lanes: [][]memoryharness.Candidate{{}, {}, {}}}
		hash, err := memoryharness.CanonicalSHA256("CandidateSnapshot", snapshot)
		if err != nil {
			t.Fatal(err)
		}
		prefix, err := CanonicalSHA256("StreamInitialPrefixProjectionV2", StreamInitialPrefixProjectionV2{StreamID: "s", ClockDomain: "c", Scope: scope, Dimension: "work_task", ConfigSHA256: events.ConfigSHA256})
		if err != nil {
			t.Fatal(err)
		}
		probes.Probes = append(probes.Probes, ProbeV2{Protocol: "dynamics-probe-v2", QueryID: name, StreamID: "s", Scope: scope, Dimension: "work_task", AtNS: 10, ClockDomain: "c", CutoffAcceptedSeq: 0, CandidateSnapshotID: name, CandidateSnapshotSHA256: hash, PrefixSHA256: prefix, FinalK: 6, ContextBudget: memoryharness.ContextBudget{Unit: "fixture_units_v0", Limit: 4}, PolicyID: "B0", PolicyVersion: "v"})
		candidates.Snapshots = append(candidates.Snapshots, snapshot)
	}
	runs, err := RunPolicies(events, probes, candidates, cfg)
	if err != nil || len(runs) != 1 || len(runs[0].Predictions) != 3 {
		t.Fatal("stateless original multi-scope baseline narrowed:", err)
	}
	for _, prediction := range runs[0].Predictions {
		if prediction.Status != "ok" {
			t.Fatal("empty known scope became failed")
		}
	}
}

// The immutable DYN01 fixture remains old-version evidence. Only version labels
// and their consequent two-phase hashes change in the selected registry.
func selectedPolicyRegistry(t *testing.T) PolicyRegistryV2 {
	t.Helper()
	r := fixtureRegistry(t)
	for i := range r.Entries {
		if r.Entries[i].PolicyID == "B0" {
			continue
		}
		r.Entries[i].Version = "p1-zeroaux-order-v1"
		r.Entries[i].Ranker.PolicyVersion = r.Entries[i].Version
		if r.Entries[i].Ranker.InheritedB2ConfigSHA256 != nil {
			r.Entries[i].Ranker.InheritedB2ConfigSHA256 = ptr(r.Entries[2].Ranker.ConfigSHA256)
		}
		r.Entries[i].Ranker = sealFixture(t, "RankerConfigV2", r.Entries[i].Ranker)
	}
	return resealRegistry(t, r)
}

func TestPolicySelectedRegistryOnlyVersionsAndDerivedHashesChange(t *testing.T) {
	old, current := fixtureRegistry(t), selectedPolicyRegistry(t)
	if err := ValidateTyped("PolicyRegistryV2", current); err != nil {
		t.Fatal(err)
	}
	if old.RegistrySHA256 == current.RegistrySHA256 {
		t.Fatal("version not hash bound")
	}
	for i := range current.Entries {
		a, b := old.Entries[i], current.Entries[i]
		if i == 0 && (a.Version != b.Version || a.Ranker.ConfigSHA256 != b.Ranker.ConfigSHA256) {
			t.Fatal("B0 version/parameters changed")
		}
		if i > 0 && (b.Version != "p1-zeroaux-order-v1" || a.Ranker.ConfigSHA256 == b.Ranker.ConfigSHA256) {
			t.Fatal("non-B0 version not hash bound")
		}
		b.Version, b.Ranker.PolicyVersion = a.Version, a.Ranker.PolicyVersion
		b.Ranker.ConfigSHA256, b.Ranker.RegistrySHA256 = a.Ranker.ConfigSHA256, a.Ranker.RegistrySHA256
		b.Ranker.InheritedB2ConfigSHA256 = a.Ranker.InheritedB2ConfigSHA256
		if !reflect.DeepEqual(a, b) {
			t.Fatal("numeric/role/dynamics/other parameter changed", a.PolicyID)
		}
	}
}

func TestPolicyHistoricalRegisteredSubnormalDivergence(t *testing.T) {
	s, q, views, _ := policyFixture(t, "B1")
	registry := fixtureRegistry(t)
	registry.Entries[1].Ranker.Lanes[0].Weight = math.SmallestNonzeroFloat64
	registry.Entries[1].Ranker.Lanes[1].Weight = 0
	registry.Entries[1].Ranker = sealFixture(t, "RankerConfigV2", registry.Entries[1].Ranker)
	registry = resealRegistry(t, registry)
	if err := ValidateTyped("PolicyRegistryV2", registry); err != nil {
		t.Fatal(err)
	}
	got, err := RankPolicy(s, q, views, registry.Entries[1].Ranker)
	if err != nil {
		t.Fatal(err)
	}
	if got.Selected[0].ID != "b" || got.Ranks[0].FusionScore != 0 || got.Ranks[1].FusionScore != 0 {
		t.Fatal("historical math/order changed")
	}
	baseline, err := memoryharness.RankAndPack(s, baselineProbe(q), baselinePolicy())
	if err != nil || baseline.Selected[0].Candidate.Ref.ID != "a" {
		t.Fatal("historical B0 changed")
	}
}

func TestPolicyNewVersionZeroAuxiliaryLiteralVectors(t *testing.T) {
	for _, id := range []PolicyID{"B1", "B2", "V3-P1-FD", "V3-P1-IR", "V3-P1-NOTIME", "V3-P1-NOOUT"} {
		for index, weight := range []float64{math.SmallestNonzeroFloat64, math.Float64frombits(0x0010000000000000), 1} {
			t.Run(string(id)+"/"+[]string{"min_subnormal", "min_normal", "one"}[index], func(t *testing.T) {
				s, q, views, cfg := policyFixture(t, id)
				cfg.PolicyVersion, q.PolicyVersion = "p1-zeroaux-order-v1", "p1-zeroaux-order-v1"
				for i := range cfg.Lanes {
					cfg.Lanes[i].Weight = 0
					if cfg.Lanes[i].Kind == "relevance" {
						cfg.Lanes[i].Weight = weight
					}
				}
				cfg = sealFixture(t, "RankerConfigV2", cfg)
				got, err := RankPolicyP1(s, q, views, policyFeatures(t), cfg)
				if err != nil {
					t.Fatal(err)
				}
				if got.Selected[0].ID != "a" || got.Selected[1].ID != "b" || got.ContextCostUsed != 4 {
					t.Fatal("baseline order/packing changed")
				}
				if math.Float64bits(got.Ranks[0].FusionScore) != math.Float64bits(weight/61) || math.Float64bits(got.Ranks[1].FusionScore) != math.Float64bits(weight/62) {
					t.Fatal("score bits changed")
				}
			})
		}
	}
}

func TestPolicyNewVersionPositiveAuxiliaryKeepsOrdinaryTie(t *testing.T) {
	for _, weight := range []float64{math.SmallestNonzeroFloat64, 1} {
		s, q, views, cfg := policyFixture(t, "B1")
		cfg.PolicyVersion, q.PolicyVersion = "p1-zeroaux-order-v1", "p1-zeroaux-order-v1"
		cfg.Lanes[0].Weight, cfg.Lanes[1].Weight = math.SmallestNonzeroFloat64, weight
		cfg = sealFixture(t, "RankerConfigV2", cfg)
		got, err := RankPolicy(s, q, views, cfg)
		if err != nil || got.Selected[0].ID != "b" {
			t.Fatal("positive auxiliary activated exception", err)
		}
	}
}

func TestPolicyNewVersionSparseTiesAndPacking(t *testing.T) {
	for _, variant := range []string{"base_tie", "ineligible", "empty", "skip_large", "k_one"} {
		t.Run(variant, func(t *testing.T) {
			s, q, views, cfg := policyFixture(t, "B1")
			cfg.PolicyVersion, q.PolicyVersion = "p1-zeroaux-order-v1", "p1-zeroaux-order-v1"
			cfg.Lanes[0].Weight, cfg.Lanes[1].Weight = math.SmallestNonzeroFloat64, 0
			cfg = sealFixture(t, "RankerConfigV2", cfg)
			switch variant {
			case "base_tie":
				s.Lanes[0][0].Reasons = []string{}
				s.Lanes[1] = []memoryharness.Candidate{s.Lanes[0][1], s.Lanes[0][0]}
			case "ineligible":
				s.Lanes[0][0].Approved = false
			case "empty":
				s.Lanes = [][]memoryharness.Candidate{{}, {}, {}}
			case "skip_large":
				s.Lanes[0][0].ContextCostUnits = 5
			case "k_one":
				q.FinalK = 1
			}
			policySealSnapshot(t, s, &q)
			got, err := RankPolicy(s, q, views, cfg)
			if err != nil {
				t.Fatal(err)
			}
			baseline, err := memoryharness.RankAndPack(s, baselineProbe(q), baselinePolicy())
			if err != nil || got.ContextCostUsed != baseline.UsedContextUnits || len(got.Selected) != len(baseline.Selected) {
				t.Fatal("packing/cost differs", err)
			}
			for i, row := range baseline.Selected {
				if got.Selected[i] != row.Candidate.Ref {
					t.Fatal("ordered refs differ")
				}
			}
		})
	}
}

func TestPolicyNewVersionZeroAuxiliaryStillValidates(t *testing.T) {
	for _, failure := range []string{"state", "feature", "scope", "prefix", "revision", "clock"} {
		t.Run(failure, func(t *testing.T) {
			id := PolicyID("B2")
			if failure == "feature" {
				id = "V3-P1-IR"
			}
			s, q, views, cfg := policyFixture(t, id)
			cfg.PolicyVersion, q.PolicyVersion = "p1-zeroaux-order-v1", "p1-zeroaux-order-v1"
			for i := range cfg.Lanes {
				if cfg.Lanes[i].Kind != "relevance" {
					cfg.Lanes[i].Weight = 0
				}
			}
			cfg = sealFixture(t, "RankerConfigV2", cfg)
			features := policyFeatures(t)
			code := "missing_state"
			switch failure {
			case "state":
				views = []StateViewV2{}
			case "feature":
				features = []ImportanceFeatureV1{}
				code = "missing_feature"
			case "scope":
				q.Scope.Project = "other"
				code = "snapshot_binding"
			case "prefix":
				q.PrefixSHA256 = fixtureSHA("other")
				code = "prefix_mismatch"
			case "revision":
				views[0].Key.Target.Revision++
				code = "missing_state"
			case "clock":
				q.ClockDomain = "other"
				code = "clock_domain_mismatch"
			}
			_, err := RankPolicyP1(s, q, views, features, cfg)
			requireCode(t, err, code)
		})
	}
}
