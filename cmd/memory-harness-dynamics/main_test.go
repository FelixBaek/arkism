package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	base "github.com/FelixBaek/arkism/internal/memoryharness"
	dyn "github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
)

func hashTyped(t *testing.T, name string, value any) string {
	t.Helper()
	h, e := dyn.CanonicalSHA256(name, value)
	if e != nil {
		t.Fatal(e)
	}
	return h
}
func writeFixture(t *testing.T, dir, name string, value any) dyn.RoleFile {
	t.Helper()
	raw, e := jsonBytes(value)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, name), raw, 0600); e != nil {
		t.Fatal(e)
	}
	return dyn.RoleFile{RelativePath: name, RawSHA256: rawSHA(raw), SizeBytes: int64(len(raw))}
}
func eventHash(t *testing.T, e dyn.EventV2) dyn.EventV2 {
	t.Helper()
	e.PayloadSHA256 = hashTyped(t, "EventLogicalProjectionV2", dyn.EventLogicalProjectionV2{Protocol: e.Protocol, StreamID: e.StreamID, EventID: e.EventID, Key: e.Key, Kind: e.Kind, ClockDomain: e.ClockDomain, ObservedAtNS: e.ObservedAtNS, Task: e.Task, Evidence: e.Evidence, Outcome: e.Outcome, Attribution: e.Attribution, Strength: e.Strength, Reason: e.Reason})
	return e
}

func cliFixture(t *testing.T, missing bool) (options, []string) {
	t.Helper()
	dir := t.TempDir()
	raw, e := os.ReadFile("../../docs/research/memory-harness/policy_registry.json")
	if e != nil {
		t.Fatal(e)
	}
	value, e := dyn.DecodeStrict(raw, "PolicyRegistryV2")
	if e != nil {
		t.Fatal(e)
	}
	registry := value.(dyn.PolicyRegistryV2)
	if e = os.WriteFile(filepath.Join(dir, "registry.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	scope := base.Scope{Workspace: "w", Project: "p", User: "u", Agent: "a"}
	c := base.Candidate{Scope: scope, Ref: base.Ref{Origin: "memory", Collection: "memories", ID: "a", Revision: 1, ContentSHA256: rawSHA([]byte("a"))}, Reasons: []string{"exact_keyword"}, Trust: .25, ObservedSeq: 1, ContextCostUnits: 2, Approved: true, Active: true, SourceVerified: true, Authority: "data_only_untrusted"}
	other := c
	other.Ref.ID, other.Ref.ContentSHA256 = "b", rawSHA([]byte("b"))
	other.Trust, other.UpdatedAtNS, other.Reasons = .5, 1000000000, []string{}
	snapshot := base.CandidateSnapshot{SnapshotID: "snapshot", StreamID: "s", Scope: scope, AsOfNS: 1000000000, AsOfSeq: 2, Lanes: [][]base.Candidate{{c, other}, {}, {}}}
	source := *registry.Entries[2].Dynamics
	events := dyn.EventFileV2{Protocol: "dynamics-event-file-v2", StreamID: "s", ClockDomain: "c", ConfigSHA256: source.ConfigSHA256, Events: []dyn.EventV2{}}
	features := dyn.P1FeatureFileV1{Protocol: "p1-feature-file-v1", FeatureContractSHA256: dyn.P1ContractSHA256, Features: []dyn.ImportanceFeatureV1{}}
	for i, candidate := range snapshot.Lanes[0] {
		key := dyn.DynamicsKeyV2{Scope: scope, Target: candidate.Ref, Dimension: "work_task", ContractSHA256: dyn.ContractSHA256, ConfigSHA256: source.ConfigSHA256}
		if missing {
			key.Target.ID = "unseen_" + key.Target.ID
		}
		events.Events = append(events.Events, eventHash(t, dyn.EventV2{Protocol: "dynamics-event-v2", StreamID: "s", EventID: "activation_" + candidate.Ref.ID, TransportAttemptID: "delivery_" + candidate.Ref.ID, Key: key, Kind: "activate", ClockDomain: "c", AcceptedAtNS: int64((i + 1) * 10), AcceptedSeq: int64(i + 1), Outcome: "not_applicable", Attribution: "not_applicable", Reason: "activation"}))
		sourceRef := dyn.EvidenceRef{Namespace: "fixture", Collection: "sources", ID: "source_" + candidate.Ref.ID, Revision: 1, ContentSHA256: rawSHA([]byte(candidate.Ref.ID))}
		importance := []float64{.25, .75}[i]
		observed := int64(10)
		feature := dyn.ImportanceFeatureV1{FeatureID: "feature_" + candidate.Ref.ID, Scope: scope, Target: candidate.Ref, Source: sourceRef, ClockDomain: "c", ObservedAtNS: &observed, AvailableAtNS: 10, AvailableAcceptedSeq: 1, FeatureName: "importance", FeatureVersion: "v", RuleSHA256: rawSHA([]byte("rule")), GeneratorConfigSHA256: rawSHA([]byte("feature config")), ProducerVersion: "v", InputRefs: []dyn.EvidenceRef{sourceRef}, Status: "present", Value: &importance, Unit: "dimensionless"}
		feature.FeatureSHA256 = hashTyped(t, "ImportanceFeatureV1HashProjection", dyn.ImportanceFeatureV1HashProjection{FeatureID: feature.FeatureID, Scope: scope, Target: feature.Target, Source: sourceRef, ClockDomain: "c", ObservedAtNS: &observed, AvailableAtNS: 10, AvailableAcceptedSeq: 1, FeatureName: "importance", FeatureVersion: "v", RuleSHA256: feature.RuleSHA256, GeneratorConfigSHA256: feature.GeneratorConfigSHA256, ProducerVersion: "v", InputRefs: feature.InputRefs, Status: "present", Value: &importance, Unit: "dimensionless"})
		features.Features = append(features.Features, feature)
	}
	featureKeys := map[dyn.RefV2]string{}
	for _, feature := range features.Features {
		key, keyErr := dyn.CanonicalBytes("RefV2", feature.Target)
		if keyErr != nil {
			t.Fatal(keyErr)
		}
		featureKeys[feature.Target] = string(key)
	}
	sort.Slice(features.Features, func(i, j int) bool {
		return featureKeys[features.Features[i].Target] < featureKeys[features.Features[j].Target]
	})

	snapshotHash, e := base.CanonicalSHA256("CandidateSnapshot", snapshot)
	if e != nil {
		t.Fatal(e)
	}
	probes := dyn.ProbeFileV2{Protocol: "dynamics-probe-file-v2", StreamID: "s", ClockDomain: "c", Probes: []dyn.ProbeV2{}}
	cfg := dyn.DynamicsRunConfigV2{Protocol: "dynamics-run-config-v2", RunID: "job", RegistrySHA256: registry.RegistrySHA256, Registry: registry, ResourceLimits: registry.ResourceLimits, PolicyIDs: []dyn.PolicyID{}, Configs: []dyn.PolicyConfig{}}
	for _, entry := range registry.Entries {
		configSHA := source.ConfigSHA256
		if entry.Dynamics != nil {
			configSHA = entry.Dynamics.ConfigSHA256
		}
		prefix := hashTyped(t, "StreamInitialPrefixProjectionV2", dyn.StreamInitialPrefixProjectionV2{StreamID: "s", ClockDomain: "c", Scope: scope, Dimension: "work_task", ConfigSHA256: configSHA})
		for _, original := range events.Events {
			event := original
			event.Key.ConfigSHA256 = configSHA
			event = eventHash(t, event)
			prefix = hashTyped(t, "StreamAcceptedStepProjectionV2", dyn.StreamAcceptedStepProjectionV2{PreviousPrefixSHA256: prefix, StreamID: "s", ClockDomain: "c", AcceptedSeq: event.AcceptedSeq, AcceptedAtNS: event.AcceptedAtNS, PayloadSHA256: event.PayloadSHA256})
		}
		probes.Probes = append(probes.Probes, dyn.ProbeV2{Protocol: "dynamics-probe-v2", QueryID: "q", StreamID: "s", Scope: scope, Dimension: "work_task", AtNS: 1000000000, ClockDomain: "c", CutoffAcceptedSeq: 2, CandidateSnapshotID: snapshot.SnapshotID, CandidateSnapshotSHA256: snapshotHash, PrefixSHA256: prefix, FinalK: 2, ContextBudget: base.ContextBudget{Unit: "fixture_units_v0", Limit: 4}, PolicyID: entry.PolicyID, PolicyVersion: entry.Version})
		cfg.PolicyIDs = append(cfg.PolicyIDs, entry.PolicyID)
		cfg.Configs = append(cfg.Configs, dyn.PolicyConfig{PolicyID: entry.PolicyID, Ranker: entry.Ranker, Dynamics: entry.Dynamics})
	}
	roles := []dyn.RoleFile{}
	for _, item := range []struct {
		role dyn.InputRole
		name string
		data any
	}{{"events", "events.json", events}, {"probes", "probes.json", probes}, {"candidates", "candidates.json", base.CandidateFile{ContractID: "arkism-offline-b0-v1", Snapshots: []base.CandidateSnapshot{snapshot}}}, {"registry", "registry.json", registry}} {
		role := writeFixture(t, dir, item.name, item.data)
		role.Role = item.role
		roles = append(roles, role)
	}
	manifest := dyn.DynamicInputManifestV2{Protocol: "dynamics-input-manifest-v2", DatasetID: "development", StreamID: "s", ClockDomain: "c", BaselineBundleSHA256: rawSHA([]byte("baseline bundle")), PolicyContractSHA256: dyn.ContractSHA256, Files: roles}
	manifest.ManifestSHA256 = hashTyped(t, "DynamicInputManifestV2HashProjection", dyn.DynamicInputManifestV2HashProjection{Protocol: manifest.Protocol, DatasetID: manifest.DatasetID, StreamID: "s", ClockDomain: "c", BaselineBundleSHA256: manifest.BaselineBundleSHA256, PolicyContractSHA256: dyn.ContractSHA256, Files: roles})
	writeFixture(t, dir, "dynamics-manifest.json", manifest)
	featureRole := writeFixture(t, dir, "features.json", features)
	featureRole.Role = "features"
	wrapper := dyn.P1InputManifestV1{Protocol: "p1-input-manifest-v1", BaseDynamicManifestSHA256: manifest.ManifestSHA256, FeatureContractSHA256: dyn.P1ContractSHA256, FeatureFile: featureRole}
	wrapper.ManifestSHA256 = hashTyped(t, "P1InputManifestV1HashProjection", dyn.P1InputManifestV1HashProjection{Protocol: wrapper.Protocol, BaseDynamicManifestSHA256: wrapper.BaseDynamicManifestSHA256, FeatureContractSHA256: wrapper.FeatureContractSHA256, FeatureFile: featureRole})
	writeFixture(t, dir, "p1.json", wrapper)
	cfg.InputManifestSHA256 = wrapper.ManifestSHA256
	cfg.ConfigSHA256 = hashTyped(t, "DynamicsRunConfigV2HashProjection", dyn.DynamicsRunConfigV2HashProjection{Protocol: cfg.Protocol, RunID: cfg.RunID, InputManifestSHA256: cfg.InputManifestSHA256, RegistrySHA256: cfg.RegistrySHA256, PolicyIDs: cfg.PolicyIDs, Configs: cfg.Configs, ResourceLimits: cfg.ResourceLimits, Registry: cfg.Registry})
	writeFixture(t, dir, "config.json", cfg)
	o := options{manifest: filepath.Join(dir, "p1.json"), config: filepath.Join(dir, "config.json"), registry: filepath.Join(dir, "registry.json"), out: filepath.Join(dir, "output"), p1: true}
	return o, []string{"run", "--p1-manifest", o.manifest, "--config", o.config, "--registry", o.registry, "--out", o.out}
}

func TestDynamicsCLINormalSevenAndInterop(t *testing.T) {
	o, args := cliFixture(t, false)
	var stdout, stderr bytes.Buffer
	if exit := run(args, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit=%d %s", exit, stderr.String())
	}
	statusValue, e := dyn.DecodeStrict(stdout.Bytes(), "CLIStatus")
	if e != nil {
		t.Fatal(e)
	}
	status := statusValue.(dyn.CLIStatus)
	if status.Status != "completed" || len(status.Outputs) != 8 {
		t.Fatal("actual seven-policy completion missing")
	}
	raw, e := os.ReadFile(filepath.Join(o.out, "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	value, e := dyn.DecodeStrict(raw, "OutputManifest")
	if e != nil {
		t.Fatal(e)
	}
	manifest := value.(dyn.OutputManifest)
	if manifest.PlannedRows != 7 || manifest.FailedRows != 0 || len(manifest.Files) != 7 || rawSHA(raw) != status.Outputs[7].RawSHA256 {
		t.Fatal("manifest identity/counts mismatch")
	}
	selected, e := os.ReadFile(o.manifest)
	if e != nil {
		t.Fatal(e)
	}
	config, e := os.ReadFile(o.config)
	if e != nil {
		t.Fatal(e)
	}
	if manifest.InputManifestSHA256 != rawSHA(selected) || manifest.RunConfigRawSHA256 != rawSHA(config) {
		t.Fatal("raw identities confused with semantic hashes")
	}
	runs := []json.RawMessage{}
	for _, file := range manifest.Files {
		data, readErr := os.ReadFile(filepath.Join(o.out, filepath.FromSlash(file.RelativePath)))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if rawSHA(data) != file.RawSHA256 {
			t.Fatal("published prediction bytes mismatch")
		}
		runs = append(runs, data)
	}
	registry, e := os.ReadFile(o.registry)
	if e != nil {
		t.Fatal(e)
	}
	interop, e := json.Marshal(struct {
		Registry json.RawMessage   `json:"registry"`
		Runs     []json.RawMessage `json:"runs"`
	}{registry, runs})
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("native_interop_fixture=%s", interop)
}

func TestDynamicsCLIFailedRowsStillComplete(t *testing.T) {
	o, args := cliFixture(t, true)
	var stdout, stderr bytes.Buffer
	if exit := run(args, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit=%d %s", exit, stderr.String())
	}
	raw, e := os.ReadFile(filepath.Join(o.out, "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	value, e := dyn.DecodeStrict(raw, "OutputManifest")
	if e != nil {
		t.Fatal(e)
	}
	m := value.(dyn.OutputManifest)
	if m.Status != "completed_with_failures" || m.FailedRows != 4 || m.PlannedRows != 7 {
		t.Fatal("missing-state failures dropped from denominator")
	}
}

type closedWriter struct{}

func (closedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestDynamicsCLIStdoutFailurePreservesCompletion(t *testing.T) {
	o, args := cliFixture(t, false)
	var stderr bytes.Buffer
	if exit := run(args, closedWriter{}, &stderr); exit != 1 {
		t.Fatal("transport failure was success")
	}
	if _, e := os.Stat(filepath.Join(o.out, "manifest.json")); e != nil {
		t.Fatal("published manifest rolled back")
	}
	if _, e := os.Stat(filepath.Join(o.out, "predictions/B0.json")); e != nil {
		t.Fatal("published prediction rolled back")
	}
}

func TestDynamicsCLINoOverwriteAndBadFlags(t *testing.T) {
	o, args := cliFixture(t, false)
	var stdout, stderr bytes.Buffer
	if run(args, &stdout, &stderr) != 0 {
		t.Fatal(stderr.String())
	}
	before, e := os.ReadFile(filepath.Join(o.out, "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	if run(args, &stdout, &stderr) != 2 {
		t.Fatal("existing output overwritten")
	}
	after, e := os.ReadFile(filepath.Join(o.out, "manifest.json"))
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("existing bytes changed")
	}
	for _, bad := range [][]string{{"run"}, {"run", "--manifest", "a", "--p1-manifest", "b", "--config", "c", "--out", "d"}, {"run", "--man", "a", "--config", "b", "--registry", "c", "--out", "d"}} {
		if _, e := parse(bad); e == nil {
			t.Fatal("invalid flags accepted")
		}
	}
}

func TestDynamicsCLIRawRoleMismatchAndSymlink(t *testing.T) {
	o, args := cliFixture(t, false)
	var stdout, stderr bytes.Buffer
	path := filepath.Join(filepath.Dir(o.manifest), "events.json")
	file, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = file.WriteString(" "); e != nil {
		t.Fatal(e)
	}
	if e = file.Close(); e != nil {
		t.Fatal(e)
	}
	if run(args, &stdout, &stderr) != 2 {
		t.Fatal("raw whitespace hash mismatch accepted")
	}
	if _, e = os.Stat(o.out); !os.IsNotExist(e) {
		t.Fatal("invalid input created output")
	}
	link := filepath.Join(filepath.Dir(o.manifest), "link.json")
	if e = os.Symlink(o.manifest, link); e != nil {
		t.Fatal(e)
	}
	if _, e = readRegular(link); e == nil || fixedError(e).Code != "unsafe_path" {
		t.Fatal("symlink input followed")
	}
}

func sealCLI(t *testing.T, name, projection, field string, value any) any {
	t.Helper()
	raw, e := json.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	var record map[string]json.RawMessage
	if e = json.Unmarshal(raw, &record); e != nil {
		t.Fatal(e)
	}
	delete(record, field)
	raw, e = json.Marshal(record)
	if e != nil {
		t.Fatal(e)
	}
	p, e := dyn.DecodeStrict(raw, projection)
	if e != nil {
		t.Fatal(e)
	}
	h := hashTyped(t, projection, p)
	record[field], e = json.Marshal(h)
	if e != nil {
		t.Fatal(e)
	}
	raw, e = json.Marshal(record)
	if e != nil {
		t.Fatal(e)
	}
	result, e := dyn.DecodeStrict(raw, name)
	if e != nil {
		t.Fatal(e)
	}
	return result
}

func witnessCLI(t *testing.T, b bundle, forged bool) dyn.SnapshotV2 {
	t.Helper()
	scope := b.events.Events[0].Key.Scope
	dimension := b.events.Events[0].Key.Dimension
	prefix := hashTyped(t, "StreamInitialPrefixProjectionV2", dyn.StreamInitialPrefixProjectionV2{StreamID: b.events.StreamID, ClockDomain: b.events.ClockDomain, Scope: scope, Dimension: dimension, ConfigSHA256: b.events.ConfigSHA256})
	states := []dyn.StateV2{}
	receipts := []dyn.ReceiptV2{}
	entries := []dyn.FenceEntry{}
	for _, event := range b.events.Events {
		at, seq, id, payload := event.AcceptedAtNS, event.AcceptedSeq, event.EventID, event.PayloadSHA256
		value := .5
		if forged {
			value = .25
		}
		state := dyn.StateV2{Key: event.Key, Mode: "scalar", Value: value, StateRevision: 1, ActivatedAtNS: &at, ActivationAcceptedSeq: &seq, ActivationEventID: &id, ActivationPayloadSHA256: &payload, LastAppliedAtNS: &at, LastAppliedSeq: &seq, Status: "active", Reason: "activation"}
		states = append(states, sealCLI(t, "StateV2", "StateV2HashProjection", "state_sha256", state).(dyn.StateV2))
		receipt := dyn.ReceiptV2{StreamID: event.StreamID, EventID: event.EventID, PayloadSHA256: event.PayloadSHA256, Key: event.Key, AcceptedAtNS: at, AcceptedSeq: seq, Disposition: "applied", BeforeStateRevision: 0, AfterStateRevision: 1, Reason: "activation"}
		receipts = append(receipts, sealCLI(t, "ReceiptV2", "ReceiptV2HashProjection", "receipt_sha256", receipt).(dyn.ReceiptV2))
		entries = append(entries, dyn.FenceEntry{Scope: scope, Target: event.Key.Target, Status: "current", SourceRefs: []dyn.EvidenceRef{}, Reason: "none"})
		prefix = hashTyped(t, "StreamAcceptedStepProjectionV2", dyn.StreamAcceptedStepProjectionV2{PreviousPrefixSHA256: prefix, StreamID: event.StreamID, ClockDomain: event.ClockDomain, AcceptedSeq: seq, AcceptedAtNS: at, PayloadSHA256: payload})
	}
	stateKeys := map[dyn.DynamicsKeyV2]string{}
	refKeys := map[dyn.RefV2]string{}
	for _, state := range states {
		raw, e := dyn.CanonicalBytes("DynamicsKeyV2", state.Key)
		if e != nil {
			t.Fatal(e)
		}
		stateKeys[state.Key] = string(raw)
		raw, e = dyn.CanonicalBytes("RefV2", state.Key.Target)
		if e != nil {
			t.Fatal(e)
		}
		refKeys[state.Key.Target] = string(raw)
	}
	sort.Slice(states, func(i, j int) bool { return stateKeys[states[i].Key] < stateKeys[states[j].Key] })
	sort.Slice(entries, func(i, j int) bool { return refKeys[entries[i].Target] < refKeys[entries[j].Target] })
	cursor := dyn.StreamCursorV2{StreamID: b.events.StreamID, ClockDomain: b.events.ClockDomain, AcceptedAtNS: 20, AcceptedSeq: 2, PrefixSHA256: prefix}
	fence := sealCLI(t, "LifecycleFence", "LifecycleFenceHashProjection", "fence_sha256", dyn.LifecycleFence{Namespace: "synthetic", Epoch: 1, Entries: entries}).(dyn.LifecycleFence)
	index := sealCLI(t, "ReceiptIndexV2", "ReceiptIndexV2HashProjection", "index_sha256", dyn.ReceiptIndexV2{StreamID: b.events.StreamID, ClockDomain: b.events.ClockDomain, Cursor: cursor, Receipts: receipts, EvidenceConsumptions: []dyn.EvidenceConsumption{}, LifecycleFence: fence}).(dyn.ReceiptIndexV2)
	return sealCLI(t, "SnapshotV2", "SnapshotV2HashProjection", "snapshot_sha256", dyn.SnapshotV2{Protocol: "dynamics-snapshot-v2", StreamID: b.events.StreamID, Scope: scope, Dimension: dimension, ConfigSHA256: b.events.ConfigSHA256, Cursor: cursor, States: states, Receipts: index}).(dyn.SnapshotV2)
}

func TestDynamicsCLIOptionalWitnessNeverSeedsNumericState(t *testing.T) {
	o, args := cliFixture(t, false)
	b, e := load(o)
	if e != nil {
		t.Fatal(e)
	}
	witness := witnessCLI(t, b, true)
	if e = validateWitness(witness, b.events); e != nil {
		t.Fatal("basic declared prefix witness rejected:", e)
	}
	expected, e := dyn.RunPoliciesP1(b.events, b.probes, b.candidates, b.features, b.cfg)
	if e != nil {
		t.Fatal(e)
	}
	// Extend only the explicit native role manifest and rebind its semantic
	// wrapper/config identities. The forged witness is never a runner seed.
	manifest, _, _, _, e := selectedManifest(o)
	if e != nil {
		t.Fatal(e)
	}
	role := writeFixture(t, filepath.Dir(o.manifest), "snapshot.json", dyn.SnapshotFileV2{Protocol: "dynamics-snapshot-file-v2", Snapshot: witness})
	role.Role = "snapshot"
	manifest.Files = append(manifest.Files, role)
	manifest = sealCLI(t, "DynamicInputManifestV2", "DynamicInputManifestV2HashProjection", "manifest_sha256", manifest).(dyn.DynamicInputManifestV2)
	writeFixture(t, filepath.Dir(o.manifest), "dynamics-manifest.json", manifest)
	raw, e := os.ReadFile(o.manifest)
	if e != nil {
		t.Fatal(e)
	}
	value, e := dyn.DecodeStrict(raw, "P1InputManifestV1")
	if e != nil {
		t.Fatal(e)
	}
	wrapper := value.(dyn.P1InputManifestV1)
	wrapper.BaseDynamicManifestSHA256 = manifest.ManifestSHA256
	wrapper = sealCLI(t, "P1InputManifestV1", "P1InputManifestV1HashProjection", "manifest_sha256", wrapper).(dyn.P1InputManifestV1)
	writeFixture(t, filepath.Dir(o.manifest), "p1.json", wrapper)
	cfg := b.cfg
	cfg.InputManifestSHA256 = wrapper.ManifestSHA256
	cfg = sealCLI(t, "DynamicsRunConfigV2", "DynamicsRunConfigV2HashProjection", "config_sha256", cfg).(dyn.DynamicsRunConfigV2)
	writeFixture(t, filepath.Dir(o.manifest), "config.json", cfg)
	var stdout, stderr bytes.Buffer
	if run(args, &stdout, &stderr) != 0 {
		t.Fatal(stderr.String())
	}
	for i, run := range expected {
		raw, e = os.ReadFile(filepath.Join(o.out, "predictions", string(run.PolicyID)+".json"))
		if e != nil {
			t.Fatal(e)
		}
		actual, e := dyn.DecodeStrict(raw, "PredictionRunV2")
		if e != nil {
			t.Fatal(e)
		}
		got := actual.(dyn.PredictionRunV2)
		want, e := json.Marshal(run.Predictions)
		if e != nil {
			t.Fatal(e)
		}
		current, e := json.Marshal(got.Predictions)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(want, current) {
			t.Fatalf("witness numeric value contaminated policy %d", i)
		}
	}
}

func TestDynamicsCLINoFeatureFlagPreservesIRFailure(t *testing.T) {
	o, args := cliFixture(t, false)
	b, e := load(o)
	if e != nil {
		t.Fatal(e)
	}
	manifest, _, _, _, e := selectedManifest(o)
	if e != nil {
		t.Fatal(e)
	}
	b.cfg.InputManifestSHA256 = manifest.ManifestSHA256
	b.cfg = sealCLI(t, "DynamicsRunConfigV2", "DynamicsRunConfigV2HashProjection", "config_sha256", b.cfg).(dyn.DynamicsRunConfigV2)
	writeFixture(t, filepath.Dir(o.manifest), "config.json", b.cfg)
	args[1], args[2] = "--manifest", filepath.Join(filepath.Dir(o.manifest), "dynamics-manifest.json")
	var stdout, stderr bytes.Buffer
	if run(args, &stdout, &stderr) != 0 {
		t.Fatal(stderr.String())
	}
	raw, e := os.ReadFile(filepath.Join(o.out, "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	value, e := dyn.DecodeStrict(raw, "OutputManifest")
	if e != nil {
		t.Fatal(e)
	}
	if value.(dyn.OutputManifest).FailedRows != 1 {
		t.Fatal("no-feature API used a hidden sidecar/default")
	}
}

func TestDynamicsCLIRollbackOwnPartialOutput(t *testing.T) {
	o, _ := cliFixture(t, false)
	b, e := load(o)
	if e != nil {
		t.Fatal(e)
	}
	runs, e := dyn.RunPoliciesP1(b.events, b.probes, b.candidates, b.features, b.cfg)
	if e != nil {
		t.Fatal(e)
	}
	runs[1].RunSHA256 = rawSHA([]byte("invalid output forces controlled early-publication failure"))
	if _, e = publish(o, b, runs); e == nil {
		t.Fatal("invalid second artifact accepted")
	}
	if _, e = os.Stat(o.out); !os.IsNotExist(e) {
		t.Fatal("owned incomplete output remained")
	}
}
