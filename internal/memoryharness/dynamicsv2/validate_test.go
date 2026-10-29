package dynamicsv2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

func ptr[T any](v T) *T          { return &v }
func fixtureSHA(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func fixtureScope() ScopeV2      { return ScopeV2{Workspace: "w", Project: "p", User: "u", Agent: "a"} }
func fixtureRef() RefV2 {
	return RefV2{Origin: "memory", Collection: "memories", ID: "m", Revision: 1, ContentSHA256: fixtureSHA("memory")}
}
func fixtureKey() DynamicsKeyV2 {
	return DynamicsKeyV2{Scope: fixtureScope(), Target: fixtureRef(), Dimension: "work_task", ContractSHA256: ContractSHA256, ConfigSHA256: fixtureSHA("config")}
}

// Input fixture seals only; no expected canonical byte/hash is generated here.
func sealFixture[T any](t *testing.T, name string, value T) T {
	t.Helper()
	v := reflect.ValueOf(&value).Elem()
	dest := fixedProjections[name]
	p, e := project(v, fixedObjects[dest].typ)
	if e != nil {
		t.Fatal(e)
	}
	b, e := canonical(p)
	if e != nil {
		t.Fatal(e)
	}
	v.FieldByName(fixedHashFields[name]).SetString(domainSHA(dest, b))
	return value
}
func fixtureState(t *testing.T) StateV2 {
	return sealFixture(t, "StateV2", StateV2{Key: fixtureKey(), Mode: "scalar", Value: .5, StateRevision: 1, ActivatedAtNS: ptr(int64(10)), ActivationAcceptedSeq: ptr(int64(1)), ActivationEventID: ptr("activation"), ActivationPayloadSHA256: ptr(fixtureSHA("activation")), LastAppliedAtNS: ptr(int64(10)), LastAppliedSeq: ptr(int64(1)), Status: "active", Reason: "activation"})
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var ce *ContractErrorV2
	if !errors.As(err, &ce) || string(ce.Code) != code {
		t.Fatalf("got %v, want %s", err, code)
	}
	if strings.Contains(ce.Path, "payload_private") {
		t.Fatal("untrusted key disclosed")
	}
}
func TestDynamicsContractCanonicalIndependentLiterals(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want string
	}{{"null", (*int64)(nil), "n"}, {"utf8", "한", "s3:한"}, {"negative_zero", math.Copysign(0, -1), "d0000000000000000;"}, {"half", .5, "d3fe0000000000000;"}, {"max_int64", int64(math.MaxInt64), "i9223372036854775807;"}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, e := canonical(reflect.ValueOf(tc.v))
			if e != nil || string(b) != tc.want {
				t.Fatalf("got %q %v", b, e)
			}
		})
	}
	value := StreamInitialPrefixProjectionV2{StreamID: "s", ClockDomain: "c", Scope: fixtureScope(), Dimension: "work_task", ConfigSHA256: strings.Repeat("0", 64)}
	literal := "o5{s12:clock_domains1:cs13:config_sha256s64:" + strings.Repeat("0", 64) + "s9:dimensions9:work_tasks5:scopeo4{s5:agents1:as7:projects1:ps4:users1:us9:workspaces1:w}s9:stream_ids1:s}"
	b, e := CanonicalBytes("StreamInitialPrefixProjectionV2", value)
	if e != nil || string(b) != literal {
		t.Fatalf("projection literal mismatch: %v", e)
	}
	h, e := CanonicalSHA256("StreamInitialPrefixProjectionV2", value)
	if e != nil || h != "f9d550e544c83e4088217c54181830a4b20cbf40b546c3937a5317edc90e42bf" {
		t.Fatalf("independent hash mismatch: %s %v", h, e)
	}
}
func TestDynamicsContractStrictDecoderBoundaries(t *testing.T) {
	valid := `{"code":"snapshot_conflict","path":"$.key"}`
	cases := []struct{ name, raw, code string }{{"valid", valid, ""}, {"unknown", `{"code":"snapshot_conflict","path":"$","payload_private":"secret"}`, "unknown_field"}, {"missing", `{"code":"snapshot_conflict"}`, "missing_field"}, {"duplicate", `{"code":"snapshot_conflict","code":"invalid_state","path":"$"}`, "duplicate_key"}, {"surrogate", `{"code":"invalid_state","path":"\ud800"}`, "invalid_unicode"}, {"trailing", valid + `{}`, "invalid_json"}, {"wrong_type", `{"code":true,"path":"$"}`, "invalid_type"}, {"unknown_code", `{"code":"made_up","path":"$"}`, "invalid_value"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, e := DecodeStrict([]byte(tc.raw), "ContractErrorV2")
			if tc.code == "" {
				if e != nil {
					t.Fatal(e)
				}
			} else {
				requireCode(t, e, tc.code)
			}
		})
	}
}
func TestDynamicsContractStateSemanticNegatives(t *testing.T) {
	good := fixtureState(t)
	if e := ValidateTyped("StateV2", good); e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		name   string
		change func(*StateV2)
	}{{"active_missing_anchor", func(s *StateV2) { s.ActivatedAtNS = nil }}, {"active_revision_zero", func(s *StateV2) { s.StateRevision = 0 }}, {"pending_has_anchor", func(s *StateV2) { s.Status = "pending_activation" }}, {"last_before_activation", func(s *StateV2) { s.LastAppliedAtNS = ptr(int64(9)) }}, {"last_seq_before_activation", func(s *StateV2) { s.LastAppliedSeq = ptr(int64(0)) }}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := good
			tc.change(&v)
			v = sealFixture(t, "StateV2", v)
			if e := ValidateTyped("StateV2", v); e == nil {
				t.Fatal("semantic invalid state accepted")
			}
		})
	}
}
func TestDynamicsContractTaskRawSHAEquality(t *testing.T) {
	ref := EvidenceRef{Namespace: "fixture", Collection: "eval_tasks", ID: "task", Revision: 1, ContentSHA256: fixtureSHA("original task bytes")}
	task := TaskBinding{TaskRef: ref, RunRef: EvidenceRef{Namespace: "fixture", Collection: "eval_runs", ID: "run", Revision: 1, ContentSHA256: fixtureSHA("run")}, TrialRef: EvidenceRef{Namespace: "fixture", Collection: "eval_trials", ID: "trial", Revision: 1, ContentSHA256: fixtureSHA("trial")}, CheckerSHA256: fixtureSHA("checker"), RuleSHA256: fixtureSHA("rule"), Qualification: "synthetic_independent", TaskSHA256: ref.ContentSHA256}
	if e := ValidateTyped("TaskBinding", task); e != nil {
		t.Fatal(e)
	}
	task.TaskSHA256 = fixtureSHA("binding object, not original task bytes")
	if e := ValidateTyped("TaskBinding", task); e == nil {
		t.Fatal("incorrect raw task hash accepted")
	}
}
func TestDynamicsContractOutputManifestBoundary(t *testing.T) {
	p := OutputRoleFile{Role: "prediction_run", RelativePath: "predictions/B0.json", RawSHA256: fixtureSHA("actual bytes"), SizeBytes: 10}
	m := OutputManifest{Protocol: "dynamics-output-manifest-v2", RunID: "run", InputManifestSHA256: fixtureSHA("input"), RunConfigSHA256: fixtureSHA("semantic"), RunConfigRawSHA256: fixtureSHA("raw"), RegistrySHA256: fixtureSHA("registry"), Status: "completed", PlannedRows: 1, FailedRows: 0, Files: []OutputRoleFile{p}}
	if e := ValidateTyped("OutputManifest", m); e != nil {
		t.Fatal(e)
	}
	t.Run("manifest_selfref", func(t *testing.T) {
		m.Files = []OutputRoleFile{{Role: "completion_manifest", RelativePath: "manifest.json", RawSHA256: fixtureSHA("self"), SizeBytes: 10}}
		if e := ValidateTyped("OutputManifest", m); e == nil {
			t.Fatal("manifest self ref accepted")
		}
	})
	t.Run("wrong_status", func(t *testing.T) {
		m.Files = []OutputRoleFile{p}
		m.FailedRows = 1
		if e := ValidateTyped("OutputManifest", m); e == nil {
			t.Fatal("failed rows hidden by completed status")
		}
	})
}
func TestDynamicsContractTypedBoundaryAndNoMutation(t *testing.T) {
	s := fixtureState(t)
	before, _ := json.Marshal(s)
	if _, e := CanonicalBytes("StateV2", &s); e == nil {
		t.Fatal("pointer accepted")
	}
	if _, e := CanonicalBytes("StateV2", map[string]any{}); e == nil {
		t.Fatal("arbitrary map accepted")
	}
	if e := ValidateTyped("Unregistered", s); e == nil {
		t.Fatal("unknown type accepted")
	}
	s.Value = math.NaN()
	requireCode(t, ValidateTyped("StateV2", s), "invalid_value")
	s = fixtureState(t)
	_, e := CanonicalBytes("StateV2", s)
	if e != nil {
		t.Fatal(e)
	}
	after, _ := json.Marshal(s)
	if !bytes.Equal(before, after) {
		t.Fatal("caller state mutated")
	}
}

func TestDynamicsContractStrictIntegerAndFiniteFloat(t *testing.T) {
	step := StreamAcceptedStepProjectionV2{PreviousPrefixSHA256: fixtureSHA("prefix"), StreamID: "s", ClockDomain: "c", AcceptedSeq: 1, AcceptedAtNS: 10, PayloadSHA256: fixtureSHA("payload")}
	base, _ := json.Marshal(step)
	cases := []struct{ name, lexical, code string }{{"max", "9223372036854775807", ""}, {"overflow", "9223372036854775808", "invalid_value"}, {"fraction", "1.0", "invalid_type"}, {"exponent", "1e0", "invalid_type"}, {"bool", "true", "invalid_type"}, {"negative", "-1", "invalid_value"}, {"negative_zero", "-0", ""}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := bytes.Replace(base, []byte(`"accepted_at_ns":10`), []byte(`"accepted_at_ns":`+tc.lexical), 1)
			_, e := DecodeStrict(b, "StreamAcceptedStepProjectionV2")
			if tc.code == "" {
				if e != nil {
					t.Fatal(e)
				}
			} else {
				requireCode(t, e, tc.code)
			}
		})
	}
	row := RankRow{Ref: fixtureRef(), BaseRRFScore: 1, BaseDenseRank: 1, Auxiliary: []AuxRank{}, FusionScore: 0, FinalOrdinal: 1}
	raw, _ := json.Marshal(row)
	floats := []struct{ name, lexical, code string }{{"finite309", "1" + strings.Repeat("0", 308), ""}, {"overflow310", "1" + strings.Repeat("0", 309), "invalid_value"}, {"exponent_finite", "1e308", ""}, {"exponent_overflow", "1e309", "invalid_value"}, {"bool", "true", "invalid_type"}, {"negative", "-1", "invalid_value"}}
	for _, tc := range floats {
		t.Run(tc.name, func(t *testing.T) {
			b := bytes.Replace(raw, []byte(`"base_rrf_score":1`), []byte(`"base_rrf_score":`+tc.lexical), 1)
			v, e := DecodeStrict(b, "RankRow")
			if tc.code == "" {
				if e != nil {
					t.Fatal(e)
				}
				if v.(RankRow).BaseRRFScore != 1e308 {
					t.Fatal("finite309 conversion changed")
				}
			} else {
				requireCode(t, e, tc.code)
			}
		})
	}
}
func TestDynamicsContractResourcesAndUnicode(t *testing.T) {
	for _, n := range []int{31, 32} {
		t.Run("depth_"+string(rune('a'+n)), func(t *testing.T) {
			b := []byte(`{"payload_private":` + strings.Repeat("[", n) + `null` + strings.Repeat("]", n) + `}`)
			_, e := DecodeStrict(b, "ContractErrorV2")
			want := "unknown_field"
			if n == 32 {
				want = "resource_limit"
			}
			requireCode(t, e, want)
		})
	}
	t.Run("string_bytes", func(t *testing.T) {
		b := []byte(`{"payload_private":"` + strings.Repeat("한", 1366) + `"}`)
		_, e := DecodeStrict(b, "ContractErrorV2")
		requireCode(t, e, "resource_limit")
	})
	t.Run("invalid_utf8", func(t *testing.T) {
		_, e := DecodeStrict([]byte{'{', '"', 0xff, '"', ':', '1', '}'}, "ContractErrorV2")
		requireCode(t, e, "invalid_unicode")
	})
	for _, s := range []string{`\udc00`, `\ud800x`, `\ud800\u1234`} {
		t.Run(s, func(t *testing.T) {
			_, e := DecodeStrict([]byte(`{"code":"invalid_state","path":"`+s+`"}`), "ContractErrorV2")
			requireCode(t, e, "invalid_unicode")
		})
	}
	t.Run("paired_unicode_path", func(t *testing.T) {
		b := []byte(`{"role":"events","relative_path":"\ud83d\ude00.json","raw_sha256":"` + fixtureSHA("file") + `","size_bytes":1}`)
		if _, e := DecodeStrict(b, "RoleFile"); e != nil {
			t.Fatal(e)
		}
	})
}
func TestDynamicsContractUnsupportedBranches(t *testing.T) {
	cfg := DynamicsConfigV2{Protocol: "dynamics-config-v2", Version: "v", Mode: "scalar", Clock: "accepted_time", ClockDomain: "c", Initial: .5, TauNS: 1000000000, EtaPositive: .25, EtaNegative: .5, UnitStrength: 1, NegativePolicy: "only_memory_attributable_independent", DecayEnabled: true, OutcomeJumpEnabled: true, Atol: 1e-12, Rtol: 1e-12, EvidenceRuleSHA256: fixtureSHA("rule")}
	cfg = sealFixture(t, "DynamicsConfigV2", cfg)
	if e := ValidateTyped("DynamicsConfigV2", cfg); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"fastslow", "capture", "continuous"} {
		t.Run(mode, func(t *testing.T) {
			c := cfg
			c.Mode = mode
			requireCode(t, ValidateTyped("DynamicsConfigV2", c), "unsupported_mode")
		})
	}
}
func TestDynamicsContractPendingAndSnapshotView(t *testing.T) {
	s := fixtureState(t)
	s.Status = "pending_activation"
	s.StateRevision = 0
	s.ActivatedAtNS = nil
	s.ActivationAcceptedSeq = nil
	s.ActivationEventID = nil
	s.ActivationPayloadSHA256 = nil
	s.LastAppliedAtNS = nil
	s.LastAppliedSeq = nil
	s = sealFixture(t, "StateV2", s)
	if e := ValidateTyped("StateV2", s); e != nil {
		t.Fatal(e)
	}
	t.Run("pending_revision", func(t *testing.T) {
		s.StateRevision = 1
		s = sealFixture(t, "StateV2", s)
		requireCode(t, ValidateTyped("StateV2", s), "invalid_state")
	})
	v := StateViewV2{Key: fixtureKey(), AtNS: 20, ClockDomain: "c", CutoffAcceptedSeq: 1, SnapshotSHA256: fixtureSHA("snapshot"), SnapshotCursor: StreamCursorV2{StreamID: "s", ClockDomain: "c", AcceptedAtNS: 10, AcceptedSeq: 1, PrefixSHA256: fixtureSHA("prefix")}, SourceStateRevision: 1, ActivatedAtNS: 10, ActivationAcceptedSeq: 1, Value: .4, Status: "present", Reason: "none", StreamID: "s", Scope: fixtureScope(), Dimension: "work_task", ConfigSHA256: fixtureKey().ConfigSHA256}
	if e := ValidateTyped("StateViewV2", v); e != nil {
		t.Fatal(e)
	}
	t.Run("future_prefix", func(t *testing.T) {
		v.CutoffAcceptedSeq = 0
		requireCode(t, ValidateTyped("StateViewV2", v), "prefix_mismatch")
	})
}
func TestDynamicsContractNativeAliasesAndErrorMapping(t *testing.T) {
	t.Run("scope_alias", func(t *testing.T) {
		s := fixtureScope()
		s.User = "한"
		requireCode(t, ValidateTyped("ScopeV2", s), "invalid_value")
	})
	t.Run("ref_collection_not_widened", func(t *testing.T) {
		r := fixtureRef()
		r.Collection = "sources"
		requireCode(t, ValidateTyped("RefV2", r), "invalid_value")
	})
	t.Run("budget_original_bound", func(t *testing.T) {
		requireCode(t, ValidateTyped("LegacyContextBudget", LegacyContextBudget{Unit: "fixture_units_v0", Limit: 1000001}), "invalid_value")
	})
	codes := []string{"invalid_json", "invalid_type", "missing_field", "unknown_field", "duplicate_key", "invalid_unicode", "invalid_value", "unsupported_version", "unsupported_type", "snapshot_conflict", "snapshot_binding", "schedule_mismatch", "hash_mismatch", "unsafe_path", "io_error", "output_exists", "invalid_state", "invalid_time", "invalid_half_life"}
	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			v, e := DecodeStrict([]byte(`{"code":"`+code+`","path":"$.key"}`), "ContractErrorV2")
			if e != nil {
				t.Fatal(e)
			}
			if string(v.(ContractErrorV2).Code) != code {
				t.Fatal("native error remapped")
			}
		})
	}
}

func fixtureConfig(t *testing.T, policy PolicyID) DynamicsConfigV2 {
	c := DynamicsConfigV2{Protocol: "dynamics-config-v2", Version: "v", Mode: "scalar", Clock: "accepted_time", ClockDomain: "c", Initial: .5, TauNS: 1000000000, EtaPositive: .25, EtaNegative: .5, UnitStrength: 1, NegativePolicy: "only_memory_attributable_independent", DecayEnabled: policy != "V3-P1-NOTIME", OutcomeJumpEnabled: policy != "V3-P1-NOOUT" && policy != "V3-P1-FD", Atol: 1e-12, Rtol: 1e-12, EvidenceRuleSHA256: fixtureSHA("rule")}
	return sealFixture(t, "DynamicsConfigV2", c)
}
func fixtureLimits() ResourceLimits {
	return ResourceLimits{MaxFileBytes: 67108864, MaxEvents: 100000, MaxStates: 100000, MaxReceipts: 200000, MaxProbes: 100000, MaxFeatures: 100000, MaxArrayItems: 200000, MaxStringBytes: 4096, MaxJSONDepth: 32}
}
func fixtureRankParameters(policy PolicyID, dynamics *DynamicsConfigV2, inherited *string) RankerConfigParametersProjectionV2 {
	p := RankerConfigParametersProjectionV2{PolicyID: policy, PolicyVersion: "v", OuterOffset: 60, Lanes: []RankLane{}, MissingState: "error", MissingFeature: "error", RecencyAnchor: "updated_at_ns", RecencyTauNS: 1000000000, BaseEqualRank: "exact_score_dense", FeatureEqualRank: "exact_value_dense", FinalTie: "trust_updated_key", Fallback: "error", Atol: 1e-12, Rtol: 1e-12, InheritedB2ConfigSHA256: inherited, Ablation: "none"}
	// These lane literals are independent of the implementation's laneShape helper.
	lanes := map[PolicyID][]string{"B0": {}, "B1": {"relevance", "recency"}, "B2": {"relevance", "state"}, "V3-P1-FD": {"relevance", "state"}, "V3-P1-IR": {"relevance", "importance", "recency"}, "V3-P1-NOTIME": {"relevance", "state"}, "V3-P1-NOOUT": {"relevance", "state"}}
	for _, kind := range lanes[policy] {
		p.Lanes = append(p.Lanes, RankLane{Kind: kind, Weight: 1})
	}
	if dynamics != nil {
		p.DynamicsConfigSHA256 = ptr(dynamics.ConfigSHA256)
	}
	if policy == "V3-P1-IR" {
		p.FeatureContractSHA256 = ptr(P1ContractSHA256)
	}
	if policy == "V3-P1-NOTIME" {
		p.Ablation = "disable_decay"
	}
	if policy == "V3-P1-NOOUT" {
		p.Ablation = "disable_outcome_jumps"
	}
	return p
}
func fixtureFullRanker(p RankerConfigParametersProjectionV2, configSHA, registrySHA string) RankerConfigV2 {
	return RankerConfigV2{PolicyID: p.PolicyID, PolicyVersion: p.PolicyVersion, RegistrySHA256: registrySHA, DynamicsConfigSHA256: p.DynamicsConfigSHA256, FeatureContractSHA256: p.FeatureContractSHA256, OuterOffset: p.OuterOffset, Lanes: p.Lanes, MissingState: p.MissingState, MissingFeature: p.MissingFeature, RecencyAnchor: p.RecencyAnchor, RecencyTauNS: p.RecencyTauNS, BaseEqualRank: p.BaseEqualRank, FeatureEqualRank: p.FeatureEqualRank, FinalTie: p.FinalTie, Fallback: p.Fallback, Atol: p.Atol, Rtol: p.Rtol, InheritedB2ConfigSHA256: p.InheritedB2ConfigSHA256, Ablation: p.Ablation, ConfigSHA256: configSHA}
}
func fixtureRegistry(t *testing.T) PolicyRegistryV2 {
	t.Helper()
	ids := []PolicyID{"B0", "B1", "B2", "V3-P1-FD", "V3-P1-IR", "V3-P1-NOTIME", "V3-P1-NOOUT"}
	graph := PolicyRegistryGraphProjectionV2{Protocol: "dynamics-policy-registry-v2", Entries: []RegistryPolicyEntryProjectionV2{}, ResourceLimits: fixtureLimits()}
	params := map[PolicyID]RankerConfigParametersProjectionV2{}
	hashes := map[PolicyID]string{}
	dynamics := map[PolicyID]*DynamicsConfigV2{}
	for _, id := range ids {
		var d *DynamicsConfigV2
		if id == "B2" || id == "V3-P1-FD" || id == "V3-P1-NOTIME" || id == "V3-P1-NOOUT" {
			c := fixtureConfig(t, id)
			d = &c
		}
		dynamics[id] = d
		var inherited *string
		if id == "V3-P1-NOTIME" || id == "V3-P1-NOOUT" {
			inherited = ptr(hashes["B2"])
		}
		p := fixtureRankParameters(id, d, inherited)
		params[id] = p
		h, e := CanonicalSHA256("RankerConfigParametersProjectionV2", p)
		if e != nil {
			t.Fatal(e)
		}
		hashes[id] = h
		// The bound projection has no registry association at all, never a zero/empty placeholder.
		bound := fixtureBoundRanker(p, h)
		mode := "none"
		outcomes, decay := false, false
		if d != nil {
			mode = "scalar"
			outcomes = d.OutcomeJumpEnabled
			decay = d.DecayEnabled
		}
		graph.Entries = append(graph.Entries, RegistryPolicyEntryProjectionV2{PolicyID: id, Version: "v", ImplementedMode: mode, UsesOutcomes: outcomes, UsesElapsedDecay: decay, AllowedRoles: []InputRole{"events", "probes", "candidates", "snapshot", "registry", "features"}, Dynamics: d, Ranker: bound, RuleSHA256: fixtureSHA("policy rule"), ParameterProvenance: "engineering_regression_only"})
	}
	registrySHA, e := CanonicalSHA256("PolicyRegistryGraphProjectionV2", graph)
	if e != nil {
		t.Fatal(e)
	}
	r := PolicyRegistryV2{Protocol: graph.Protocol, Entries: []PolicyEntry{}, ResourceLimits: graph.ResourceLimits, RegistrySHA256: registrySHA}
	for _, entry := range graph.Entries {
		r.Entries = append(r.Entries, PolicyEntry{PolicyID: entry.PolicyID, Version: entry.Version, ImplementedMode: entry.ImplementedMode, UsesOutcomes: entry.UsesOutcomes, UsesElapsedDecay: entry.UsesElapsedDecay, AllowedRoles: entry.AllowedRoles, Dynamics: dynamics[entry.PolicyID], Ranker: fixtureFullRanker(params[entry.PolicyID], hashes[entry.PolicyID], registrySHA), RuleSHA256: entry.RuleSHA256, ParameterProvenance: entry.ParameterProvenance})
	}
	return r
}
func TestDynamicsContractRegistryTwoPhaseAndAblations(t *testing.T) {
	r := fixtureRegistry(t)
	if e := ValidateTyped("PolicyRegistryV2", r); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(r)
	if _, e := DecodeStrict(raw, "PolicyRegistryV2"); e != nil {
		t.Fatal(e)
	}
	t.Run("association_mismatch", func(t *testing.T) {
		v := fixtureRegistry(t)
		v.Entries[0].Ranker.RegistrySHA256 = fixtureSHA("other registry")
		if e := ValidateTyped("PolicyRegistryV2", v); e == nil {
			t.Fatal("registry association ignored")
		}
	})
	t.Run("wrong_inherited_domain", func(t *testing.T) {
		v := fixtureRegistry(t)
		v.Entries[5].Ranker.InheritedB2ConfigSHA256 = ptr(v.Entries[2].Dynamics.ConfigSHA256)
		v.Entries[5].Ranker = sealFixture(t, "RankerConfigV2", v.Entries[5].Ranker)
		v = resealRegistry(t, v)
		requireCode(t, ValidateTyped("PolicyRegistryV2", v), "config_mismatch")
	})
	t.Run("retuned_ablation", func(t *testing.T) {
		v := fixtureRegistry(t)
		v.Entries[5].Dynamics.EtaPositive = .75
		*v.Entries[5].Dynamics = sealFixture(t, "DynamicsConfigV2", *v.Entries[5].Dynamics)
		v.Entries[5].Ranker.DynamicsConfigSHA256 = ptr(v.Entries[5].Dynamics.ConfigSHA256)
		v.Entries[5].Ranker = sealFixture(t, "RankerConfigV2", v.Entries[5].Ranker)
		v = resealRegistry(t, v)
		requireCode(t, ValidateTyped("PolicyRegistryV2", v), "config_mismatch")
	})
}
func resealRegistry(t *testing.T, r PolicyRegistryV2) PolicyRegistryV2 {
	r = sealFixture(t, "PolicyRegistryV2", r)
	for i := range r.Entries {
		r.Entries[i].Ranker.RegistrySHA256 = r.RegistrySHA256
	}
	return r
}
func TestDynamicsContractRequiredEmbeddedRegistry(t *testing.T) {
	r := fixtureRegistry(t)
	c := DynamicsRunConfigV2{Protocol: "dynamics-run-config-v2", RunID: "run", InputManifestSHA256: fixtureSHA("input"), RegistrySHA256: r.RegistrySHA256, PolicyIDs: []PolicyID{"B0"}, Configs: []PolicyConfig{{PolicyID: "B0", Ranker: r.Entries[0].Ranker, Dynamics: nil}}, ResourceLimits: fixtureLimits(), Registry: r}
	c = sealFixture(t, "DynamicsRunConfigV2", c)
	if e := ValidateTyped("DynamicsRunConfigV2", c); e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(c)
	t.Run("missing_actual_registry", func(t *testing.T) {
		var m map[string]json.RawMessage
		if e := json.Unmarshal(b, &m); e != nil {
			t.Fatal(e)
		}
		delete(m, "registry")
		raw, _ := json.Marshal(m)
		_, e := DecodeStrict(raw, "DynamicsRunConfigV2")
		requireCode(t, e, "missing_field")
	})
	t.Run("wrong_registered_config", func(t *testing.T) {
		v := c
		v.Configs = []PolicyConfig{{PolicyID: "B0", Ranker: fixtureFullRanker(fixtureRankParameters("B0", nil, nil), r.Entries[0].Ranker.ConfigSHA256, r.RegistrySHA256)}}
		v.Configs[0].Ranker.PolicyVersion = "other"
		v.Configs[0].Ranker = sealFixture(t, "RankerConfigV2", v.Configs[0].Ranker)
		v = sealFixture(t, "DynamicsRunConfigV2", v)
		requireCode(t, ValidateTyped("DynamicsRunConfigV2", v), "config_mismatch")
	})
}
func TestDynamicsContractMalformedRawHashAndOrdering(t *testing.T) {
	s := fixtureState(t)
	s.StateSHA256 = fixtureSHA("wrong projection")
	requireCode(t, ValidateTyped("StateV2", s), "hash_mismatch")
	t.Run("unknown_before_missing", func(t *testing.T) {
		_, e := DecodeStrict([]byte(`{"payload_private":0}`), "StateV2")
		requireCode(t, e, "unknown_field")
	})
	t.Run("duplicate_before_unknown", func(t *testing.T) {
		_, e := DecodeStrict([]byte(`{"payload_private":0,"payload_private":1}`), "StateV2")
		requireCode(t, e, "duplicate_key")
	})
	t.Run("no_unicode_normalization", func(t *testing.T) {
		a, _ := canonical(reflect.ValueOf("é"))
		b, _ := canonical(reflect.ValueOf("e\u0301"))
		if bytes.Equal(a, b) {
			t.Fatal("UTF8 normalization changed bytes")
		}
	})
}

func fixtureBoundRanker(p RankerConfigParametersProjectionV2, h string) RegistryBoundRankerProjectionV2 {
	return RegistryBoundRankerProjectionV2{PolicyID: p.PolicyID, PolicyVersion: p.PolicyVersion, DynamicsConfigSHA256: p.DynamicsConfigSHA256, FeatureContractSHA256: p.FeatureContractSHA256, OuterOffset: p.OuterOffset, Lanes: p.Lanes, MissingState: p.MissingState, MissingFeature: p.MissingFeature, RecencyAnchor: p.RecencyAnchor, RecencyTauNS: p.RecencyTauNS, BaseEqualRank: p.BaseEqualRank, FeatureEqualRank: p.FeatureEqualRank, FinalTie: p.FinalTie, Fallback: p.Fallback, Atol: p.Atol, Rtol: p.Rtol, InheritedB2ConfigSHA256: p.InheritedB2ConfigSHA256, Ablation: p.Ablation, ConfigSHA256: h}
}
func TestDynamicsContractKnownErrorPathOnly(t *testing.T) {
	_, e := DecodeStrict([]byte(`{"code":"invalid_state","path":"$.payload_private"}`), "ContractErrorV2")
	requireCode(t, e, "invalid_value")
}

func TestDynamicsContractCompiledSchemaMatchesPinnedContract(t *testing.T) {
	raw, e := os.ReadFile("../../../docs/research/memory-harness/dynamics_policy_contract.json")
	if e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256(raw)
	if hex.EncodeToString(h[:]) != ContractSHA256 {
		t.Fatal("contract bytes drifted")
	}
	type field struct {
		Type     string `json:"type"`
		Ref      string `json:"ref"`
		Nullable bool   `json:"nullable"`
	}
	type object struct {
		Required []string         `json:"required"`
		Fields   map[string]field `json:"fields"`
	}
	var doc struct {
		Models      map[string]object `json:"models"`
		Inline      map[string]object `json:"closed_inline_records"`
		Projections map[string]object `json:"hash_projections"`
	}
	if e = json.Unmarshal(raw, &doc); e != nil {
		t.Fatal(e)
	}
	if len(doc.Models) != 28 || len(doc.Inline) != 20 || len(doc.Projections) != 23 || len(fixedObjects) != 72 {
		t.Fatal("fixed type inventory changed")
	}
	for _, group := range []map[string]object{doc.Models, doc.Inline, doc.Projections} {
		for name, o := range group {
			t.Run(name, func(t *testing.T) {
				r, ok := fixedObjects[name]
				if !ok || r.typ.NumField() != len(o.Fields) {
					t.Fatal("missing concrete record/field")
				}
				for i, tag := range o.Required {
					f, ok := o.Fields[tag]
					if !ok || r.typ.Field(i).Tag.Get("json") != tag {
						t.Fatal("field tag/order mismatch")
					}
					nullable := r.typ.Field(i).Type.Kind() == reflect.Pointer
					if nullable != f.Nullable {
						t.Fatal("nullable mismatch")
					}
				}
			})
		}
	}
	p1, e := os.ReadFile("../../../docs/research/memory-harness/p1_feature_contract_v1.json")
	if e != nil {
		t.Fatal(e)
	}
	h = sha256.Sum256(p1)
	if hex.EncodeToString(h[:]) != P1ContractSHA256 {
		t.Fatal("P1 bytes drifted")
	}
}
func fixtureTask() TaskBinding {
	return TaskBinding{TaskRef: EvidenceRef{Namespace: "fixture", Collection: "eval_tasks", ID: "task", Revision: 1, ContentSHA256: fixtureSHA("original task")}, RunRef: EvidenceRef{Namespace: "fixture", Collection: "eval_runs", ID: "run", Revision: 1, ContentSHA256: fixtureSHA("run")}, TrialRef: EvidenceRef{Namespace: "fixture", Collection: "eval_trials", ID: "trial", Revision: 1, ContentSHA256: fixtureSHA("trial")}, CheckerSHA256: fixtureSHA("checker"), RuleSHA256: fixtureSHA("rule"), Qualification: "synthetic_independent", TaskSHA256: fixtureSHA("original task")}
}
func fixtureOutcome(t *testing.T) EventV2 {
	task := fixtureTask()
	evidence := EvidenceBinding{Refs: []EvidenceRef{{Namespace: "fixture", Collection: "sources", ID: "source", Revision: 1, ContentSHA256: fixtureSHA("source")}}, Target: fixtureRef(), TaskSHA256: task.TaskSHA256, Dimension: "work_task", RuleSHA256: fixtureSHA("rule"), Independence: "synthetic_independent", AvailableAcceptedSeq: 1, SourceStatus: "available"}
	evidence = sealFixture(t, "EvidenceBinding", evidence)
	return sealFixture(t, "EventV2", EventV2{Protocol: "dynamics-event-v2", StreamID: "s", EventID: "outcome", TransportAttemptID: "delivery", Key: fixtureKey(), Kind: "outcome", ClockDomain: "c", ObservedAtNS: ptr(int64(10)), ArrivedAtNS: ptr(int64(20)), AcceptedAtNS: 20, AcceptedSeq: 2, Task: &task, Evidence: &evidence, Outcome: "passed", Attribution: "memory", Strength: ptr(1.0), Reason: "passed_independent"})
}
func TestDynamicsContractQualifiedMetadataAndTaskCrossBindings(t *testing.T) {
	e := fixtureOutcome(t)
	if err := ValidateTyped("EventV2", e); err != nil {
		t.Fatal(err)
	}
	table := []struct {
		name        string
		outcome     Outcome
		attribution Attribution
		want        bool
	}{{"passed", "passed", "memory", true}, {"failed_memory", "failed", "memory", true}, {"failed_tool", "failed", "tool", false}, {"failed_unknown_cause", "failed", "unattributable", false}, {"unknown", "unknown", "unattributable", false}}
	for _, tc := range table {
		t.Run(tc.name, func(t *testing.T) {
			x := fixtureOutcome(t)
			x.Outcome = tc.outcome
			x.Attribution = tc.attribution
			x = sealFixture(t, "EventV2", x)
			if err := ValidateTyped("EventV2", x); err != nil {
				t.Fatal(err)
			}
			if qualified(x) != tc.want {
				t.Fatal("qualified metadata predicate changed")
			}
		})
	}
	t.Run("wrong_task_rawSHA", func(t *testing.T) {
		x := fixtureOutcome(t)
		x.Evidence.TaskSHA256 = fixtureSHA("other task")
		*x.Evidence = sealFixture(t, "EvidenceBinding", *x.Evidence)
		x = sealFixture(t, "EventV2", x)
		requireCode(t, ValidateTyped("EventV2", x), "hash_mismatch")
	})
	t.Run("foreign_target", func(t *testing.T) {
		x := fixtureOutcome(t)
		x.Evidence.Target.Revision = 2
		*x.Evidence = sealFixture(t, "EvidenceBinding", *x.Evidence)
		x = sealFixture(t, "EventV2", x)
		requireCode(t, ValidateTyped("EventV2", x), "snapshot_binding")
	})
	t.Run("future_observed", func(t *testing.T) {
		x := fixtureOutcome(t)
		x.ObservedAtNS = ptr(int64(21))
		x = sealFixture(t, "EventV2", x)
		requireCode(t, ValidateTyped("EventV2", x), "future_event")
	})
	t.Run("missing_strength", func(t *testing.T) {
		x := fixtureOutcome(t)
		x.Strength = nil
		x = sealFixture(t, "EventV2", x)
		requireCode(t, ValidateTyped("EventV2", x), "invalid_value")
	})
	t.Run("no_task_not_product_qualified", func(t *testing.T) {
		x := fixtureOutcome(t)
		x.Task = nil
		x = sealFixture(t, "EventV2", x)
		if err := ValidateTyped("EventV2", x); err != nil {
			t.Fatal(err)
		}
		if qualified(x) {
			t.Fatal("missing task was treated as qualified")
		}
	})
}
func fixtureSnapshot(t *testing.T) SnapshotV2 {
	key := fixtureKey()
	event := sealFixture(t, "EventV2", EventV2{Protocol: "dynamics-event-v2", StreamID: "s", EventID: "activate", TransportAttemptID: "delivery", Key: key, Kind: "activate", ClockDomain: "c", AcceptedAtNS: 10, AcceptedSeq: 1, Outcome: "not_applicable", Attribution: "not_applicable", Reason: "activation"})
	state := fixtureState(t)
	state.ActivationEventID = ptr(event.EventID)
	state.ActivationPayloadSHA256 = ptr(event.PayloadSHA256)
	state = sealFixture(t, "StateV2", state)
	receipt := sealFixture(t, "ReceiptV2", ReceiptV2{StreamID: "s", EventID: event.EventID, PayloadSHA256: event.PayloadSHA256, Key: key, AcceptedAtNS: 10, AcceptedSeq: 1, Disposition: "applied", BeforeStateRevision: 0, AfterStateRevision: 1, Reason: "activation"})
	initial := StreamInitialPrefixProjectionV2{StreamID: "s", ClockDomain: "c", Scope: key.Scope, Dimension: key.Dimension, ConfigSHA256: key.ConfigSHA256}
	h, e := CanonicalSHA256("StreamInitialPrefixProjectionV2", initial)
	if e != nil {
		t.Fatal(e)
	}
	step := StreamAcceptedStepProjectionV2{PreviousPrefixSHA256: h, StreamID: "s", ClockDomain: "c", AcceptedSeq: 1, AcceptedAtNS: 10, PayloadSHA256: event.PayloadSHA256}
	h, e = CanonicalSHA256("StreamAcceptedStepProjectionV2", step)
	if e != nil {
		t.Fatal(e)
	}
	cursor := StreamCursorV2{StreamID: "s", ClockDomain: "c", AcceptedAtNS: 10, AcceptedSeq: 1, PrefixSHA256: h}
	fence := sealFixture(t, "LifecycleFence", LifecycleFence{Namespace: "synthetic", Epoch: 1, MinimumAcceptedSeq: 0, Entries: []FenceEntry{{Scope: key.Scope, Target: key.Target, Status: "current", SourceRefs: []EvidenceRef{}, Reason: "none"}}})
	index := sealFixture(t, "ReceiptIndexV2", ReceiptIndexV2{StreamID: "s", ClockDomain: "c", Cursor: cursor, Receipts: []ReceiptV2{receipt}, EvidenceConsumptions: []EvidenceConsumption{}, LifecycleFence: fence})
	return sealFixture(t, "SnapshotV2", SnapshotV2{Protocol: "dynamics-snapshot-v2", Scope: key.Scope, Dimension: key.Dimension, ConfigSHA256: key.ConfigSHA256, Cursor: cursor, States: []StateV2{state}, Receipts: index, StreamID: "s"})
}
func TestDynamicsContractPrefixCatalogAndStateDomain(t *testing.T) {
	s := fixtureSnapshot(t)
	if e := ValidateTyped("SnapshotV2", s); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if _, e := CanonicalBytes("SnapshotV2", s); e != nil {
			t.Fatal(e)
		}
	}
	t.Run("pending_not_persisted", func(t *testing.T) {
		v := fixtureSnapshot(t)
		q := v.States[0]
		q.Status = "pending_activation"
		q.StateRevision = 0
		q.ActivatedAtNS = nil
		q.ActivationAcceptedSeq = nil
		q.ActivationEventID = nil
		q.ActivationPayloadSHA256 = nil
		q.LastAppliedAtNS = nil
		q.LastAppliedSeq = nil
		v.States[0] = sealFixture(t, "StateV2", q)
		v = sealFixture(t, "SnapshotV2", v)
		requireCode(t, ValidateTyped("SnapshotV2", v), "invalid_state")
	})
	t.Run("foreign_scope", func(t *testing.T) {
		v := fixtureSnapshot(t)
		v.Scope.User = "other"
		v = sealFixture(t, "SnapshotV2", v)
		requireCode(t, ValidateTyped("SnapshotV2", v), "snapshot_binding")
	})
	t.Run("forged_prefix", func(t *testing.T) {
		v := fixtureSnapshot(t)
		v.Cursor.PrefixSHA256 = fixtureSHA("forged")
		v.Receipts.Cursor = v.Cursor
		v.Receipts = sealFixture(t, "ReceiptIndexV2", v.Receipts)
		v = sealFixture(t, "SnapshotV2", v)
		requireCode(t, ValidateTyped("SnapshotV2", v), "prefix_mismatch")
	})
	t.Run("deleted_catalog", func(t *testing.T) {
		v := fixtureSnapshot(t)
		v.Receipts.LifecycleFence.Entries[0].Status = "invalidated"
		v.Receipts.LifecycleFence = sealFixture(t, "LifecycleFence", v.Receipts.LifecycleFence)
		v.Receipts = sealFixture(t, "ReceiptIndexV2", v.Receipts)
		v = sealFixture(t, "SnapshotV2", v)
		requireCode(t, ValidateTyped("SnapshotV2", v), "invalidated_target")
	})
}
func TestDynamicsContractEventOriginalCoordinates(t *testing.T) {
	x := fixtureOutcome(t)
	x.AcceptedSeq = 1
	x = sealFixture(t, "EventV2", x)
	retry := x
	retry.TransportAttemptID = "retry"
	retry.ArrivedAtNS = ptr(int64(19))
	retry = sealFixture(t, "EventV2", retry)
	file := EventFileV2{Protocol: "dynamics-event-file-v2", StreamID: "s", ClockDomain: "c", ConfigSHA256: x.Key.ConfigSHA256, Events: []EventV2{x, retry}}
	if e := ValidateTyped("EventFileV2", file); e != nil {
		t.Fatal(e)
	}
	retry.AcceptedSeq = 2
	retry = sealFixture(t, "EventV2", retry)
	file.Events[1] = retry
	requireCode(t, ValidateTyped("EventFileV2", file), "event_conflict")
}

func TestDynamicsContractRuleMetadataCrossBinding(t *testing.T) {
	cases := []struct {
		name   string
		change func(*EventV2)
	}{
		{"task_rule_changed", func(e *EventV2) { e.Task.RuleSHA256 = fixtureSHA("different qualification rule") }},
		{"evidence_rule_changed", func(e *EventV2) { e.Evidence.RuleSHA256 = fixtureSHA("different evidence rule") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := fixtureOutcome(t)
			if err := ValidateTyped("EventV2", e); err != nil {
				t.Fatal(err)
			}
			tc.change(&e)
			*e.Evidence = sealFixture(t, "EvidenceBinding", *e.Evidence)
			e = sealFixture(t, "EventV2", e)
			requireCode(t, ValidateTyped("EventV2", e), "hash_mismatch")
			if qualified(e) {
				t.Fatal("mismatched rule metadata qualified")
			}
		})
	}
	t.Run("non_learning_kind_does_not_bypass_binding", func(t *testing.T) {
		e := fixtureOutcome(t)
		e.Kind = "exposure"
		e.Outcome = "not_applicable"
		e.Attribution = "not_applicable"
		e.Strength = nil
		e.Task.RuleSHA256 = fixtureSHA("different rule")
		e = sealFixture(t, "EventV2", e)
		requireCode(t, ValidateTyped("EventV2", e), "hash_mismatch")
	})
}
