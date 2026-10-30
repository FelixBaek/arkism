package dynamicsv2

import (
	"reflect"
	"sort"
	"testing"

	"github.com/FelixBaek/arkism/internal/memoryharness"
)

func policyFeatures(t *testing.T) []ImportanceFeatureV1 {
	t.Helper()
	s, _, _, _ := policyFixture(t, "V3-P1-IR")
	features := []ImportanceFeatureV1{}
	for i, candidate := range s.Lanes[0] {
		source := EvidenceRef{Namespace: "fixture", Collection: "sources", ID: "source_" + candidate.Ref.ID, Revision: 1, ContentSHA256: fixtureSHA("source_" + candidate.Ref.ID)}
		feature := ImportanceFeatureV1{FeatureID: "feature_" + candidate.Ref.ID, Scope: s.Scope, Target: candidate.Ref, Source: source, ClockDomain: "c", ObservedAtNS: ptr(int64(10)), AvailableAtNS: 10, AvailableAcceptedSeq: 1, FeatureName: "importance", FeatureVersion: "v", RuleSHA256: fixtureSHA("importance rule"), GeneratorConfigSHA256: fixtureSHA("feature config"), ProducerVersion: "v", InputRefs: []EvidenceRef{source}, Status: "present", Value: ptr([]float64{.25, .75}[i]), Unit: "dimensionless"}
		features = append(features, sealFixture(t, "ImportanceFeatureV1", feature))
	}
	policySortFeatures(features)
	return features
}

func policySortFeatures(features []ImportanceFeatureV1) {
	sort.Slice(features, func(i, j int) bool { return featureKey(features[i]) < featureKey(features[j]) })
}

func TestPolicyP1SevenActualAdapters(t *testing.T) {
	for _, id := range []PolicyID{"B0", "B1", "B2", "V3-P1-FD", "V3-P1-IR", "V3-P1-NOTIME", "V3-P1-NOOUT"} {
		t.Run(string(id), func(t *testing.T) {
			s, q, views, cfg := policyFixture(t, id)
			got, err := RankPolicyP1(s, q, views, policyFeatures(t), cfg)
			if err != nil {
				t.Fatal(err)
			}
			want := "b"
			if id == "B0" {
				want = "a"
			}
			if got.Selected[0].ID != want || got.Status != "ok" || got.PolicyID != id || len(got.Ranks) != 2 {
				t.Fatal("registered adapter did not really rank")
			}
		})
	}
}

func TestPolicyP1ZeroAuxiliaryIR(t *testing.T) {
	s, q, views, cfg := policyFixture(t, "V3-P1-IR")
	cfg.Lanes[1].Weight, cfg.Lanes[2].Weight = 0, 0
	cfg = sealFixture(t, "RankerConfigV2", cfg)
	got, err := RankPolicyP1(s, q, views, policyFeatures(t), cfg)
	if err != nil || got.Selected[0].ID != "a" {
		t.Fatal("valid IR zero-auxiliary B0 equivalence failed")
	}
}

func TestPolicyP1FeatureAvailabilityLineageAndUnknown(t *testing.T) {
	s, q, views, cfg := policyFixture(t, "V3-P1-IR")
	cases := []struct {
		name string
		edit func(*ImportanceFeatureV1)
	}{
		{"missing", func(f *ImportanceFeatureV1) {
			f.Status, f.Value, f.MissingReason = "missing", nil, ptr(Reason("feature_missing"))
		}},
		{"new_lineage", func(f *ImportanceFeatureV1) { f.FeatureVersion = "other" }},
		{"future_seq", func(f *ImportanceFeatureV1) { f.AvailableAcceptedSeq = 3 }},
		{"future_time", func(f *ImportanceFeatureV1) { f.AvailableAtNS = q.AtNS + 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			features := policyFeatures(t)
			extra := scalarClone(features[0])
			extra.FeatureID, extra.Source.ID = "new", "new_source"
			extra.AvailableAcceptedSeq, extra.AvailableAtNS = 2, 20
			tc.edit(&extra)
			extra.InputRefs = []EvidenceRef{extra.Source}
			extra = sealFixture(t, "ImportanceFeatureV1", extra)
			features = append(features, extra)
			policySortFeatures(features)
			_, err := RankPolicyP1(s, q, views, features, cfg)
			if tc.name == "future_seq" || tc.name == "future_time" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireCode(t, err, "missing_feature")
			}
		})
	}
}

func TestPolicyP1LatestTieOnlyNotOlderTie(t *testing.T) {
	s, q, views, cfg := policyFixture(t, "V3-P1-IR")
	features := policyFeatures(t)
	olderTie := scalarClone(features[0])
	olderTie.FeatureID, olderTie.Source.ID = "older_tie", "source_aa"
	olderTie.InputRefs = []EvidenceRef{olderTie.Source}
	olderTie = sealFixture(t, "ImportanceFeatureV1", olderTie)
	newest := scalarClone(features[0])
	newest.FeatureID, newest.Source.ID = "newest", "source_ab"
	newest.AvailableAcceptedSeq, newest.AvailableAtNS = 2, 20
	newest.InputRefs = []EvidenceRef{newest.Source}
	newest = sealFixture(t, "ImportanceFeatureV1", newest)
	features = append(features, olderTie, newest)
	policySortFeatures(features)
	if _, err := RankPolicyP1(s, q, views, features, cfg); err != nil {
		t.Fatal("only a tie at selected newest availability is ambiguous:", err)
	}
}

func TestPolicyP1NewestTieAmbiguous(t *testing.T) {
	s, q, views, cfg := policyFixture(t, "V3-P1-IR")
	features := policyFeatures(t)
	tie := scalarClone(features[0])
	tie.FeatureID, tie.Source.ID = "tie", "source_aa"
	tie.InputRefs = []EvidenceRef{tie.Source}
	tie = sealFixture(t, "ImportanceFeatureV1", tie)
	features = append(features, tie)
	policySortFeatures(features)
	_, err := RankPolicyP1(s, q, views, features, cfg)
	requireCode(t, err, "missing_feature")
}

func TestPolicyP1FeatureHashAndInputImmutability(t *testing.T) {
	s, q, views, cfg := policyFixture(t, "V3-P1-IR")
	features := policyFeatures(t)
	before := scalarClone(features)
	if _, err := RankPolicyP1(s, q, views, features, cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, features) {
		t.Fatal("feature input mutated")
	}
	features[0].Source.ContentSHA256 = fixtureSHA("tampered")
	_, err := RankPolicyP1(s, q, views, features, cfg)
	requireCode(t, err, "hash_mismatch")
}

func TestPolicyP1OldRefCannotFillMissingNewRef(t *testing.T) {
	s, q, views, cfg := policyFixture(t, "V3-P1-IR")
	features := policyFeatures(t)
	features[0].Target.Revision++
	features[0] = sealFixture(t, "ImportanceFeatureV1", features[0])
	policySortFeatures(features)
	_, err := RankPolicyP1(s, q, views, features, cfg)
	requireCode(t, err, "missing_feature")
}

func TestPolicyP1GlobalInvalidFutureIsNotIgnored(t *testing.T) {
	s, q, views, cfg := policyFixture(t, "V3-P1-IR")
	features := policyFeatures(t)
	features[0].AvailableAtNS = q.AtNS + 1 // Deliberately stale global selfhash.
	_, err := RankPolicyP1(s, q, views, features, cfg)
	requireCode(t, err, "hash_mismatch")
}

func TestPolicyP1FDImmutableAnchor(t *testing.T) {
	cfg := fixtureConfig(t, "V3-P1-FD")
	snapshot := fixtureSnapshot(t)
	// Build a genuine FD activation prefix with the existing replay primitive.
	key := snapshot.States[0].Key
	key.ConfigSHA256 = cfg.ConfigSHA256
	initial := scalarClone(snapshot)
	initial.ConfigSHA256 = cfg.ConfigSHA256
	initial.Scope, initial.Dimension = key.Scope, key.Dimension
	initial.States = []StateV2{}
	initial.Receipts.Receipts, initial.Receipts.EvidenceConsumptions = []ReceiptV2{}, []EvidenceConsumption{}
	initial.Cursor.AcceptedSeq, initial.Cursor.AcceptedAtNS = 0, 0
	prefix, err := CanonicalSHA256("StreamInitialPrefixProjectionV2", StreamInitialPrefixProjectionV2{StreamID: "s", ClockDomain: "c", Scope: key.Scope, Dimension: key.Dimension, ConfigSHA256: cfg.ConfigSHA256})
	if err != nil {
		t.Fatal(err)
	}
	initial.Cursor.PrefixSHA256 = prefix
	initial.Receipts.Cursor = initial.Cursor
	initial.Receipts = sealFixture(t, "ReceiptIndexV2", initial.Receipts)
	initial = sealFixture(t, "SnapshotV2", initial)
	event := sealFixture(t, "EventV2", EventV2{Protocol: "dynamics-event-v2", StreamID: "s", EventID: "fd_activate", TransportAttemptID: "fd_delivery", Key: key, Kind: "activate", ClockDomain: "c", AcceptedAtNS: 10, AcceptedSeq: 1, Outcome: "not_applicable", Attribution: "not_applicable", Reason: "activation"})
	actual, _, err := ReplayPrefix(initial, []EventV2{event}, 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	before := scalarClone(actual)
	view, err := policyFDView(actual, 1000000010, key.Target, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Independent tau-equation literal e^-1 * initial (.5), explicit tolerance.
	if view.Value < .18393972058572113 || view.Value > .18393972058572120 || view.ActivatedAtNS != 10 {
		t.Fatal("FD was not derived from immutable activation anchor")
	}
	if !reflect.DeepEqual(before, actual) {
		t.Fatal("FD read mutated actual accepted prefix")
	}
}

func TestPolicyP1FutureDifferentLineageCannotAffectEarlierQuery(t *testing.T) {
	s, q, views, cfg := policyFixture(t, "V3-P1-IR")
	features := policyFeatures(t)
	baseline, err := RankPolicyP1(s, q, views, features, cfg)
	if err != nil {
		t.Fatal(err)
	}
	future := scalarClone(features[0])
	future.FeatureID, future.FeatureVersion = "future", "other_version"
	future.AvailableAtNS, future.AvailableAcceptedSeq = q.AtNS+1, q.CutoffAcceptedSeq+1
	future = sealFixture(t, "ImportanceFeatureV1", future)
	features = append(features, future)
	policySortFeatures(features)
	got, err := RankPolicyP1(s, q, views, features, cfg)
	if err != nil || !reflect.DeepEqual(got, baseline) {
		t.Fatal("valid future lineage changed the earlier result")
	}
}

func TestPolicyP1FeatureScopeClockAndDuplicateGuards(t *testing.T) {
	for _, name := range []string{"scope", "clock", "duplicate", "nil"} {
		t.Run(name, func(t *testing.T) {
			s, q, views, cfg := policyFixture(t, "V3-P1-IR")
			features := policyFeatures(t)
			code := "duplicate_feature"
			switch name {
			case "scope":
				features[0].Scope.Project = "other"
				code = "scope_mismatch"
			case "clock":
				features[0].ClockDomain = "other"
				code = "clock_domain_mismatch"
			case "duplicate":
				features = append(features, scalarClone(features[0]))
			case "nil":
				features, code = nil, "invalid_type"
			}
			if len(features) > 0 {
				features[0] = sealFixture(t, "ImportanceFeatureV1", features[0])
				policySortFeatures(features)
			}
			_, err := RankPolicyP1(s, q, views, features, cfg)
			requireCode(t, err, code)
		})
	}
}

func nativeFixture(t *testing.T, emptyEvents bool) (EventFileV2, ProbeFileV2, memoryharness.CandidateFile, P1FeatureFileV1, DynamicsRunConfigV2) {
	t.Helper()
	registry := selectedPolicyRegistry(t)
	snapshot, q, _, _ := policyFixture(t, "B0")
	source := *registry.Entries[2].Dynamics
	events := EventFileV2{Protocol: "dynamics-event-file-v2", StreamID: "s", ClockDomain: "c", ConfigSHA256: source.ConfigSHA256, Events: []EventV2{}}
	for i, candidate := range snapshot.Lanes[0] {
		key := DynamicsKeyV2{Scope: q.Scope, Target: candidate.Ref, Dimension: q.Dimension, ContractSHA256: ContractSHA256, ConfigSHA256: source.ConfigSHA256}
		event := EventV2{Protocol: "dynamics-event-v2", StreamID: "s", EventID: "activation_" + candidate.Ref.ID, TransportAttemptID: "delivery_" + candidate.Ref.ID, Key: key, Kind: "activate", ClockDomain: "c", AcceptedAtNS: int64((i + 1) * 10), AcceptedSeq: int64(i + 1), Outcome: "not_applicable", Attribution: "not_applicable", Reason: "activation"}
		events.Events = append(events.Events, sealFixture(t, "EventV2", event))
	}
	if emptyEvents {
		// Accepted events activate other keys. Candidate flags do not create
		// missing scalar states; immutable v1 ObservedSeq remains positive.
		for i := range events.Events {
			events.Events[i].Key.Target.ID = "unseen_" + events.Events[i].Key.Target.ID
			events.Events[i] = sealFixture(t, "EventV2", events.Events[i])
		}
	}

	probes := ProbeFileV2{Protocol: "dynamics-probe-file-v2", StreamID: "s", ClockDomain: "c", Probes: []ProbeV2{}}
	cfg := DynamicsRunConfigV2{Protocol: "dynamics-run-config-v2", RunID: "job", InputManifestSHA256: fixtureSHA("manifest"), RegistrySHA256: registry.RegistrySHA256, PolicyIDs: []PolicyID{}, Configs: []PolicyConfig{}, ResourceLimits: fixtureLimits(), Registry: registry}
	for _, entry := range registry.Entries {
		cfg.PolicyIDs = append(cfg.PolicyIDs, entry.PolicyID)
		cfg.Configs = append(cfg.Configs, PolicyConfig{PolicyID: entry.PolicyID, Ranker: entry.Ranker, Dynamics: entry.Dynamics})
		query := q
		query.PolicyID, query.PolicyVersion = entry.PolicyID, entry.Version
		configSHA := source.ConfigSHA256
		if entry.Dynamics != nil {
			configSHA = entry.Dynamics.ConfigSHA256
		}
		prefix, err := CanonicalSHA256("StreamInitialPrefixProjectionV2", StreamInitialPrefixProjectionV2{StreamID: "s", ClockDomain: "c", Scope: q.Scope, Dimension: q.Dimension, ConfigSHA256: configSHA})
		if err != nil {
			t.Fatal(err)
		}
		for _, original := range events.Events {
			event := scalarClone(original)
			event.Key.ConfigSHA256 = configSHA
			event = sealFixture(t, "EventV2", event)
			prefix, err = CanonicalSHA256("StreamAcceptedStepProjectionV2", StreamAcceptedStepProjectionV2{PreviousPrefixSHA256: prefix, StreamID: "s", ClockDomain: "c", AcceptedSeq: event.AcceptedSeq, AcceptedAtNS: event.AcceptedAtNS, PayloadSHA256: event.PayloadSHA256})
			if err != nil {
				t.Fatal(err)
			}
		}
		query.PrefixSHA256 = prefix
		probes.Probes = append(probes.Probes, query)
	}
	cfg = sealFixture(t, "DynamicsRunConfigV2", cfg)
	return events, probes, memoryharness.CandidateFile{ContractID: "arkism-offline-b0-v1", Snapshots: []memoryharness.CandidateSnapshot{snapshot}}, P1FeatureFileV1{Protocol: "p1-feature-file-v1", FeatureContractSHA256: P1ContractSHA256, Features: policyFeatures(t)}, cfg
}

func TestNativePoliciesSevenFullReplayAndCosts(t *testing.T) {
	events, probes, candidates, features, cfg := nativeFixture(t, false)
	before := scalarClone(events)
	runs, err := RunPoliciesP1(events, probes, candidates, features, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 7 {
		t.Fatal("missing actual registered policies")
	}
	for _, run := range runs {
		if len(run.Predictions) != 1 || run.Predictions[0].Status != "ok" || run.Cost.FailedPredictions != 0 || run.Cost.EventInputs != 2 || run.Cost.UniqueEvents != 2 || run.Cost.ProviderUSD != nil || run.Cost.ElapsedWallNS != nil {
			t.Fatalf("not actual complete replay: %+v", run)
		}
		if run.PolicyID == "V3-P1-IR" && run.Cost.FeatureReads != 2 {
			t.Fatal("importance value reads not counted")
		}
		if run.PolicyID == "B2" && run.Cost.StateUpdates != 2 {
			t.Fatal("actual activation transition updates not counted")
		}
	}
	if !reflect.DeepEqual(before, events) {
		t.Fatal("raw source projection mutated events")
	}
}

func TestNativePoliciesMissingStatePreservesEveryPolicy(t *testing.T) {
	events, probes, candidates, features, cfg := nativeFixture(t, true)
	features.Features = []ImportanceFeatureV1{}
	runs, err := RunPoliciesP1(events, probes, candidates, features, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 7 {
		t.Fatal("failure erased independent policy outputs")
	}
	for _, run := range runs {
		if len(run.Predictions) != 1 {
			t.Fatal("scheduled denominator missing")
		}
		if run.PolicyID == "B0" || run.PolicyID == "B1" {
			if run.Predictions[0].Status != "ok" {
				t.Fatal("independent stateless policy failed")
			}
			continue
		}
		want := "missing_state"
		if run.PolicyID == "V3-P1-IR" {
			want = "missing_feature"
		}
		if run.Cost.FailedPredictions != 1 || string(run.Predictions[0].Error.Code) != want {
			t.Fatal("failure replaced or denominator dropped")
		}
	}
}

func TestNativePoliciesBundleMismatchAndEmptySchedule(t *testing.T) {
	events, probes, candidates, features, cfg := nativeFixture(t, false)
	probes.Probes[1].ContextBudget.Limit--
	_, err := RunPoliciesP1(events, probes, candidates, features, cfg)
	requireCode(t, err, "schedule_mismatch")
	probes.Probes = []ProbeV2{}
	candidates.Snapshots = []memoryharness.CandidateSnapshot{}
	runs, err := RunPoliciesP1(events, probes, candidates, features, cfg)
	if err != nil || len(runs) != 7 {
		t.Fatal("explicit empty schedule omitted policy files", err)
	}
	for _, run := range runs {
		if run.Predictions == nil || len(run.Predictions) != 0 || run.Cost.EventInputs != 0 {
			t.Fatal("empty work fabricated counters")
		}
	}
}

func TestNativePoliciesPolicySpecificReplayMissingStatePreserved(t *testing.T) {
	events, probes, candidates, features, cfg := nativeFixture(t, false)
	// A valid source outcome for an unseen key is not activation. Its missing
	// scalar state must become a failed policy row, not erase all seven runs.
	key := events.Events[0].Key
	key.Target.ID = "never_activated"
	event := EventV2{Protocol: "dynamics-event-v2", StreamID: "s", EventID: "unseen_outcome", TransportAttemptID: "delivery", Key: key, Kind: "outcome", ClockDomain: "c", AcceptedAtNS: 30, AcceptedSeq: 3, Outcome: "unknown", Attribution: "unattributable", Reason: "unknown_outcome"}
	events.Events = append(events.Events, sealFixture(t, "EventV2", event))
	candidates.Snapshots[0].AsOfSeq = 3
	for i := range probes.Probes {
		probes.Probes[i].CutoffAcceptedSeq = 3
		policySealSnapshot(t, candidates.Snapshots[0], &probes.Probes[i])
		configSHA := events.ConfigSHA256
		if cfg.Configs[i].Dynamics != nil {
			configSHA = cfg.Configs[i].Dynamics.ConfigSHA256
		}
		prefix := hashTypedNativeFixture(t, events, probes.Probes[i], configSHA)
		probes.Probes[i].PrefixSHA256 = prefix
	}
	runs, err := RunPoliciesP1(events, probes, candidates, features, cfg)
	if err != nil {
		t.Fatal("runtime missing state erased all runs:", err)
	}
	for _, run := range runs {
		if len(run.Predictions) != 1 {
			t.Fatal("scheduled row dropped")
		}
		if run.PolicyID == "B2" || run.PolicyID == "V3-P1-FD" || run.PolicyID == "V3-P1-NOTIME" || run.PolicyID == "V3-P1-NOOUT" {
			if run.Predictions[0].Status != "failed" || run.Predictions[0].Error.Code != "missing_state" {
				t.Fatal("specific replay failure changed")
			}
		} else if run.Predictions[0].Status != "ok" {
			t.Fatal("independent nonstate policy suppressed")
		}
	}
}

func hashTypedNativeFixture(t *testing.T, events EventFileV2, q ProbeV2, configSHA string) string {
	t.Helper()
	prefix, err := CanonicalSHA256("StreamInitialPrefixProjectionV2", StreamInitialPrefixProjectionV2{StreamID: events.StreamID, ClockDomain: events.ClockDomain, Scope: q.Scope, Dimension: q.Dimension, ConfigSHA256: configSHA})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, raw := range events.Events {
		if seen[raw.EventID] {
			continue
		}
		if raw.AcceptedSeq > q.CutoffAcceptedSeq {
			break
		}
		seen[raw.EventID] = true
		event := scalarClone(raw)
		event.Key.ConfigSHA256 = configSHA
		event = sealFixture(t, "EventV2", event)
		prefix, err = CanonicalSHA256("StreamAcceptedStepProjectionV2", StreamAcceptedStepProjectionV2{PreviousPrefixSHA256: prefix, StreamID: events.StreamID, ClockDomain: events.ClockDomain, AcceptedSeq: event.AcceptedSeq, AcceptedAtNS: event.AcceptedAtNS, PayloadSHA256: event.PayloadSHA256})
		if err != nil {
			t.Fatal(err)
		}
	}
	return prefix
}

func TestNativePoliciesPhysicalRetryAndFutureInvalidation(t *testing.T) {
	events, probes, candidates, features, cfg := nativeFixture(t, false)
	original := scalarClone(events.Events[0])
	original.TransportAttemptID = "late_retry"
	original.ArrivedAtNS = ptr(int64(100))
	events.Events = append(events.Events, original)
	runs, err := RunPoliciesP1(events, probes, candidates, features, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.Cost.EventInputs != 3 || run.Cost.UniqueEvents != 2 || run.Cost.DuplicateEvents != 1 {
			t.Fatal("physical retry miscounted or renumbered")
		}
	}
	future := EventV2{Protocol: "dynamics-event-v2", StreamID: "s", EventID: "future_invalidation", TransportAttemptID: "future_delivery", Key: events.Events[0].Key, Kind: "invalidate", ClockDomain: "c", AcceptedAtNS: 200, AcceptedSeq: 3, Outcome: "not_applicable", Attribution: "not_applicable", Reason: "source_deleted"}
	events.Events = append(events.Events, sealFixture(t, "EventV2", future))
	again, err := RunPoliciesP1(events, probes, candidates, features, cfg)
	if err != nil || !reflect.DeepEqual(runs, again) {
		t.Fatal("future invalidation affected earlier prefix", err)
	}
}

func TestNativePoliciesRegisteredEvidenceRuleMismatchIsSpecificFailure(t *testing.T) {
	events, probes, candidates, features, cfg := nativeFixture(t, false)
	registry := scalarClone(cfg.Registry)
	fd := registry.Entries[3]
	fd.Dynamics.EvidenceRuleSHA256 = fixtureSHA("different engineering evidence rule")
	*fd.Dynamics = sealFixture(t, "DynamicsConfigV2", *fd.Dynamics)
	fd.Ranker.DynamicsConfigSHA256 = ptr(fd.Dynamics.ConfigSHA256)
	fd.Ranker = sealFixture(t, "RankerConfigV2", fd.Ranker)
	registry.Entries[3] = fd
	registry = resealRegistry(t, registry)
	if err := ValidateTyped("PolicyRegistryV2", registry); err != nil {
		t.Fatal(err)
	}
	cfg.Registry, cfg.RegistrySHA256 = registry, registry.RegistrySHA256
	for i, entry := range registry.Entries {
		cfg.Configs[i] = PolicyConfig{PolicyID: entry.PolicyID, Ranker: entry.Ranker, Dynamics: entry.Dynamics}
	}
	cfg = sealFixture(t, "DynamicsRunConfigV2", cfg)
	event := fixtureOutcome(t)
	event.EventID, event.AcceptedSeq, event.AcceptedAtNS = "qualified_rule_case", 3, 30
	event.Key = events.Events[0].Key
	event.Evidence.Target = event.Key.Target
	*event.Evidence = sealFixture(t, "EvidenceBinding", *event.Evidence)
	event = sealFixture(t, "EventV2", event)
	events.Events = append(events.Events, event)
	if err := ValidateTyped("EventFileV2", events); err != nil {
		t.Fatal(err)
	}
	candidates.Snapshots[0].AsOfSeq = 3
	for i := range probes.Probes {
		q := &probes.Probes[i]
		q.CutoffAcceptedSeq = 3
		policySealSnapshot(t, candidates.Snapshots[0], q)
		digest := events.ConfigSHA256
		if cfg.Configs[i].Dynamics != nil {
			digest = cfg.Configs[i].Dynamics.ConfigSHA256
		}
		q.PrefixSHA256 = hashTypedNativeFixture(t, events, *q, digest)
	}
	runs, err := RunPoliciesP1(events, probes, candidates, features, cfg)
	if err != nil {
		t.Fatal("registered per-policy rule mismatch erased the bundle:", err)
	}
	for _, run := range runs {
		if run.PolicyID == "V3-P1-FD" {
			prediction := run.Predictions[0]
			if prediction.Status != "failed" || prediction.Error.Code != "hash_mismatch" || prediction.Error.Path != "$.task.rule_sha256" || run.Cost.FailedPredictions != 1 {
				t.Fatal("native qualification error identity/denominator changed")
			}
		} else if run.Predictions[0].Status != "ok" {
			t.Fatal("other registered policy suppressed")
		}
	}
}

func TestPolicyNewVersionZeroAuxiliaryIRLineageRemainsRequired(t *testing.T) {
	for _, kind := range []string{"unknown", "lineage", "future_only"} {
		t.Run(kind, func(t *testing.T) {
			s, q, views, cfg := policyFixture(t, "V3-P1-IR")
			cfg.PolicyVersion, q.PolicyVersion = "p1-zeroaux-order-v1", "p1-zeroaux-order-v1"
			cfg.Lanes[1].Weight, cfg.Lanes[2].Weight = 0, 0
			cfg = sealFixture(t, "RankerConfigV2", cfg)
			features := policyFeatures(t)
			extra := scalarClone(features[0])
			extra.FeatureID, extra.Source.ID = "new", "new_source"
			extra.AvailableAcceptedSeq, extra.AvailableAtNS = 2, 20
			switch kind {
			case "unknown":
				extra.Status, extra.Value, extra.MissingReason = "missing", nil, ptr(Reason("feature_missing"))
			case "lineage":
				extra.FeatureVersion = "different"
			case "future_only":
				features = features[1:]
				extra.AvailableAcceptedSeq = 3
			}
			extra.InputRefs = []EvidenceRef{extra.Source}
			extra = sealFixture(t, "ImportanceFeatureV1", extra)
			features = append(features, extra)
			policySortFeatures(features)
			_, err := RankPolicyP1(s, q, views, features, cfg)
			requireCode(t, err, "missing_feature")
		})
	}
}

func TestPolicyNewVersionPositiveAuxiliaryEqualScoreBitsUnchanged(t *testing.T) {
	s, q, views, cfg := policyFixture(t, "B2")
	cfg.PolicyVersion, q.PolicyVersion = "p1-zeroaux-order-v1", "p1-zeroaux-order-v1"
	cfg = sealFixture(t, "RankerConfigV2", cfg)
	got, err := RankPolicyP1(s, q, views, policyFeatures(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.Selected[0].ID != "b" || got.Ranks[0].FusionScore != got.Ranks[1].FusionScore || got.Ranks[0].FusionScore != 0.03252247488101534 {
		t.Fatal("ordinary exact tie changed")
	}
}
