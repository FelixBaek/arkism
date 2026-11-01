package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	dyn "github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
)

func verifyFixture(t *testing.T, missing bool) (options, []string) {
	t.Helper()
	o, args := cliFixture(t, missing)
	var stdout, stderr bytes.Buffer
	if exit := run(args, &stdout, &stderr); exit != 0 {
		t.Fatalf("create fixture exit%d: %s", exit, stderr.String())
	}
	args[0] = "verify"
	return o, args
}

func filePins(t *testing.T, directory string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			name, relErr := filepath.Rel(directory, path)
			if relErr != nil {
				return relErr
			}
			result[name] = rawSHA(raw)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestNativeVerifyActualReplayAndReadOnly(t *testing.T) {
	o, args := verifyFixture(t, false)
	before := filePins(t, filepath.Dir(o.manifest))
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("verify exit%d: %s", code, stderr.String())
	}
	var report nativeVerification
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Protocol != "exp03-native-verification-v1" || report.Status != "replay_matched" || len(report.Policies) != 7 ||
		report.ProductAuthorityVerified || report.ScientificLockVerified || len(report.Probes.Probes) != 7 {
		t.Fatal("invalid scope or expected seven-policy coverage")
	}
	probeRaw, err := os.ReadFile(filepath.Join(filepath.Dir(o.manifest), "probes.json"))
	if err != nil {
		t.Fatal(err)
	}
	probeValue, err := dyn.DecodeStrict(probeRaw, "ProbeFileV2")
	if err != nil || !reflect.DeepEqual(report.Probes, probeValue) {
		t.Fatal("verification report changed the original per-policy probe metadata")
	}
	if strings.Contains(stdout.String()+stderr.String(), filepath.Dir(o.manifest)) {
		t.Fatal("absolute source path leaked")
	}
	if !reflect.DeepEqual(before, filePins(t, filepath.Dir(o.manifest))) {
		t.Fatal("read-only verification changed input/output files")
	}
	for _, policy := range report.Policies {
		if len(policy.Run.Predictions) != 1 || policy.Run.Predictions[0].Status != "ok" || policy.Artifact.RawSHA256 == "" {
			t.Fatal("prediction status or raw identity lost")
		}
		if policy.Run.PolicyID == "B0" {
			decision := policy.Run.Predictions[0].Decision
			if decision == nil || len(decision.Selected) != 2 || decision.Selected[0].ID != "a" ||
				decision.Selected[1].ID != "b" || decision.ContextCostUsed != 4 {
				t.Fatal("actual two-memory lookup or packing cost differs from the independent fixture expectation")
			}
		}
	}
}

func TestNativeVerifyFailureRowsAreNotTaskSuccess(t *testing.T) {
	_, args := verifyFixture(t, true)
	report, err := executeVerify(args)
	if err != nil {
		t.Fatal(err)
	}
	failed := int64(0)
	for _, policy := range report.Policies {
		failed += policy.Run.Cost.FailedPredictions
		for _, row := range policy.Run.Predictions {
			if row.Status == "failed" && (row.Decision != nil || row.Error == nil) {
				t.Fatal("failed prediction became an empty success")
			}
		}
	}
	if failed == 0 || len(report.Policies) != 7 {
		t.Fatal("failure or planned denominator dropped")
	}
}

func TestNativeVerifyPlainManifestKeepsMissingFeatureFailure(t *testing.T) {
	o, args := cliFixture(t, false)
	b, err := load(o)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, _, _, err := selectedManifest(o)
	if err != nil {
		t.Fatal(err)
	}
	b.cfg.InputManifestSHA256 = manifest.ManifestSHA256
	b.cfg = sealCLI(t, "DynamicsRunConfigV2", "DynamicsRunConfigV2HashProjection", "config_sha256", b.cfg).(dyn.DynamicsRunConfigV2)
	writeFixture(t, filepath.Dir(o.manifest), "config.json", b.cfg)
	args[1], args[2] = "--manifest", filepath.Join(filepath.Dir(o.manifest), "dynamics-manifest.json")
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("plain fixture run exit%d: %s", code, stderr.String())
	}
	args[0] = "verify"
	report, err := executeVerify(args)
	if err != nil {
		t.Fatal(err)
	}
	failed := int64(0)
	for _, policy := range report.Policies {
		failed += policy.Run.Cost.FailedPredictions
		if policy.Run.PolicyID == "V3-P1-IR" && (policy.Run.Cost.FailedPredictions != 1 || policy.Run.Predictions[0].Status != "failed") {
			t.Fatal("plain verify manufactured a missing importance feature")
		}
	}
	if failed != 1 || len(report.Policies) != 7 {
		t.Fatal("plain replay lost its exact failure denominator")
	}
	for _, input := range report.Inputs {
		if input.Role == "p1_features" || input.Role == "base_manifest" {
			t.Fatal("plain verification used an implicit P1 input")
		}
	}
}

func TestNativeVerifyP1BindsFeatureAndBaseGraph(t *testing.T) {
	for _, name := range []string{"features.json", "dynamics-manifest.json"} {
		t.Run(name, func(t *testing.T) {
			o, args := verifyFixture(t, false)
			path := filepath.Join(filepath.Dir(o.manifest), name)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if name == "features.json" {
				raw = append(raw, '\n') // Same JSON meaning, different required raw bytes.
			} else {
				raw = bytes.Replace(raw, []byte(`"development"`), []byte(`"changed"`), 1)
			}
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "hash_mismatch") {
				t.Fatalf("P1 source graph change accepted: exit%d %s", code, stderr.String())
			}
		})
	}
}

// Reseal the tampered run with valid native projections and update its raw
// manifest commitment. The verifier must still detect incorrect actual replay.
func resealChangedSelection(t *testing.T, o options) {
	t.Helper()
	path := filepath.Join(o.out, "predictions", "B0.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	value, err := dyn.DecodeStrict(raw, "PredictionRunV2")
	if err != nil {
		t.Fatal(err)
	}
	run := value.(dyn.PredictionRunV2)
	decision := run.Predictions[0].Decision
	if len(decision.Selected) == 0 {
		t.Fatal("fixture must really return a memory")
	}
	decision.Selected = []dyn.RefV2{}
	decision.ContextCostUsed = 0
	data, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	var decisionProjection dyn.PolicyDecisionV2HashProjection
	if err = json.Unmarshal(data, &decisionProjection); err != nil {
		t.Fatal(err)
	}
	decision.DecisionSHA256 = hashTyped(t, "PolicyDecisionV2HashProjection", decisionProjection)
	data, err = json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	var runProjection dyn.PredictionRunV2HashProjection
	if err = json.Unmarshal(data, &runProjection); err != nil {
		t.Fatal(err)
	}
	run.RunSHA256 = hashTyped(t, "PredictionRunV2HashProjection", runProjection)
	data, err = jsonBytes(run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dyn.DecodeStrict(data, "PredictionRunV2"); err != nil {
		t.Fatalf("negative fixture must have valid typed shape/self hashes: %v", err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(o.out, "manifest.json")
	manifestRaw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest dyn.OutputManifest
	if err = json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	for i := range manifest.Files {
		if manifest.Files[i].RelativePath == "predictions/B0.json" {
			manifest.Files[i].RawSHA256, manifest.Files[i].SizeBytes = rawSHA(data), int64(len(data))
		}
	}
	data, err = jsonBytes(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(manifestPath, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNativeVerifyRejectsResealedWrongMemorySelection(t *testing.T) {
	o, args := verifyFixture(t, false)
	resealChangedSelection(t, o)
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "snapshot_conflict") {
		t.Fatalf("resealed wrong selection was accepted: exit%d %s %s", code, stdout.String(), stderr.String())
	}
}

func TestNativeVerifyRejectsRawResultCorruption(t *testing.T) {
	o, args := verifyFixture(t, false)
	if err := os.WriteFile(filepath.Join(o.out, "predictions", "B0.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "hash_mismatch") || stdout.Len() != 0 {
		t.Fatalf("raw corruption accepted: exit%d %s", code, stderr.String())
	}
}

func TestNativeVerifyRawConfigCannotBeReplacedBySemanticIdentity(t *testing.T) {
	o, args := verifyFixture(t, false)
	raw, err := os.ReadFile(o.config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(o.config, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "snapshot_binding") {
		t.Fatalf("changed raw config accepted: exit%d %s", code, stderr.String())
	}
}

func TestNativeVerifyRejectsOutputSymlink(t *testing.T) {
	o, args := verifyFixture(t, false)
	path := filepath.Join(o.out, "predictions", "B0.json")
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".original", path); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "unsafe_path") {
		t.Fatalf("output symlink accepted: exit%d %s", code, stderr.String())
	}
}

type rejectVerifyOutput struct{}

func (rejectVerifyOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestNativeVerifyOutputFailureNeverChangesArtifacts(t *testing.T) {
	o, args := verifyFixture(t, false)
	before := filePins(t, filepath.Dir(o.manifest))
	if code := run(args, rejectVerifyOutput{}, io.Discard); code != 1 {
		t.Fatalf("stdout failure exit%d", code)
	}
	if !reflect.DeepEqual(before, filePins(t, filepath.Dir(o.manifest))) {
		t.Fatal("verification output failure changed source artifacts")
	}
}

func TestNativeVerifyBadArgumentsHaveClosedErrors(t *testing.T) {
	for _, args := range [][]string{{"verify"}, {"verify", "--unknown", "/private/sentinel"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || strings.Contains(stderr.String(), "sentinel") {
			t.Fatalf("bad argument output: exit%d %s", code, stderr.String())
		}
	}
}
