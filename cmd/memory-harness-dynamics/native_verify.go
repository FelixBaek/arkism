package main

// EXP-03 uses the accepted Go codec and policy engine to verify recorded offline
// outputs. This command writes no artifact and grants no product/research authority.
import (
	"io"
	"path/filepath"
	"reflect"

	dyn "github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
)

type verificationArtifact struct {
	Role      string `json:"role"`
	RawSHA256 string `json:"raw_sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

type verifiedPolicy struct {
	Artifact verificationArtifact `json:"artifact"`
	Run      dyn.PredictionRunV2  `json:"run"`
}

type nativeVerification struct {
	Protocol                 string                 `json:"protocol"`
	Status                   string                 `json:"status"`
	Scope                    string                 `json:"scope"`
	ContractSHA256           string                 `json:"contract_sha256"`
	InputManifestSHA256      string                 `json:"input_manifest_sha256"`
	ConfigSHA256             string                 `json:"config_sha256"`
	RegistrySHA256           string                 `json:"registry_sha256"`
	Inputs                   []verificationArtifact `json:"inputs"`
	Completion               verificationArtifact   `json:"completion"`
	Probes                   dyn.ProbeFileV2        `json:"probes"`
	Policies                 []verifiedPolicy       `json:"policies"`
	ProductAuthorityVerified bool                   `json:"product_authority_verified"`
	ScientificLockVerified   bool                   `json:"scientific_lock_verified"`
}

func artifactIdentity(role string, raw []byte) verificationArtifact {
	return verificationArtifact{Role: role, RawSHA256: rawSHA(raw), SizeBytes: int64(len(raw))}
}

// Pin the full selected input graph, including the base manifest behind a P1
// wrapper. Hash original bytes: typed hashes never substitute for raw file hashes.
func verificationInputs(o options) ([]verificationArtifact, error) {
	result := []verificationArtifact{}
	paths := []struct{ role, path string }{{"selected_manifest", o.manifest}, {"config", o.config}, {"registry", o.registry}}
	if o.p1 {
		paths = append(paths, struct{ role, path string }{"base_manifest", filepath.Join(filepath.Dir(o.manifest), "dynamics-manifest.json")})
	}
	for _, entry := range paths {
		raw, err := readRegular(entry.path)
		if err != nil {
			return nil, err
		}
		result = append(result, artifactIdentity(entry.role, raw))
	}
	manifest, _, _, feature, err := selectedManifest(o)
	if err != nil {
		return nil, err
	}
	for _, role := range manifest.Files {
		raw, readErr := roleRead(filepath.Dir(o.manifest), role)
		if readErr != nil {
			return nil, readErr
		}
		result = append(result, artifactIdentity("native_"+string(role.Role), raw))
	}
	if feature != nil {
		raw, readErr := roleRead(filepath.Dir(o.manifest), *feature)
		if readErr != nil {
			return nil, readErr
		}
		result = append(result, artifactIdentity("p1_features", raw))
	}
	return result, nil
}

func verificationManifest(o options, b bundle) (dyn.OutputManifest, verificationArtifact, error) {
	var empty dyn.OutputManifest
	raw, err := readRegular(filepath.Join(o.out, "manifest.json"))
	if err != nil {
		return empty, verificationArtifact{}, err
	}
	value, err := dyn.DecodeStrict(raw, "OutputManifest")
	if err != nil {
		return empty, verificationArtifact{}, err
	}
	m := value.(dyn.OutputManifest)
	if m.RunID != b.cfg.RunID || m.InputManifestSHA256 != b.selectedRawSHA ||
		m.RunConfigRawSHA256 != b.configRawSHA || m.RunConfigSHA256 != b.cfg.ConfigSHA256 || m.RegistrySHA256 != b.cfg.RegistrySHA256 {
		return empty, verificationArtifact{}, problem("snapshot_binding", "$")
	}
	return m, artifactIdentity("completion_manifest", raw), nil
}

func verifyRecordedPolicies(o options, manifest dyn.OutputManifest, expected []dyn.PredictionRunV2) ([]verifiedPolicy, error) {
	if len(manifest.Files) != len(expected) {
		return nil, problem("schedule_mismatch", "$")
	}
	result := make([]verifiedPolicy, 0, len(expected))
	planned, failed := int64(0), int64(0)
	for i, want := range expected {
		file := manifest.Files[i]
		if file.Role != "prediction_run" || file.RelativePath != "predictions/"+string(want.PolicyID)+".json" {
			return nil, problem("snapshot_binding", "$")
		}
		raw, err := readRegular(filepath.Join(o.out, filepath.FromSlash(file.RelativePath)))
		if err != nil {
			return nil, err
		}
		if int64(len(raw)) != file.SizeBytes || rawSHA(raw) != file.RawSHA256 {
			return nil, problem("hash_mismatch", "$")
		}
		value, err := dyn.DecodeStrict(raw, "PredictionRunV2")
		if err != nil {
			return nil, err
		}
		got := value.(dyn.PredictionRunV2)
		// Compare actual re-execution, not only attacker-resealable commitments.
		// No cost or failed row is omitted from this deterministic comparison.
		if !reflect.DeepEqual(got, want) {
			return nil, problem("snapshot_conflict", "$")
		}
		planned += int64(len(got.Predictions))
		failed += got.Cost.FailedPredictions
		result = append(result, verifiedPolicy{Artifact: artifactIdentity("prediction_run", raw), Run: got})
	}
	if planned != manifest.PlannedRows || failed != manifest.FailedRows || (failed > 0) != (manifest.Status == "completed_with_failures") {
		return nil, problem("schedule_mismatch", "$")
	}
	return result, nil
}

func executeVerify(args []string) (nativeVerification, error) {
	var empty nativeVerification
	if len(args) == 0 || args[0] != "verify" {
		return empty, problem("invalid_value", "$")
	}
	normal := append([]string{"run"}, args[1:]...)
	o, err := parse(normal)
	if err != nil {
		return empty, err
	}
	before, err := verificationInputs(o)
	if err != nil {
		return empty, err
	}
	b, err := load(o)
	if err != nil {
		return empty, err
	}
	if before[0].RawSHA256 != b.selectedRawSHA || before[1].RawSHA256 != b.configRawSHA {
		return empty, problem("snapshot_conflict", "$")
	}
	manifest, completion, err := verificationManifest(o, b)
	if err != nil {
		return empty, err
	}
	var expected []dyn.PredictionRunV2
	if o.p1 {
		expected, err = dyn.RunPoliciesP1(b.events, b.probes, b.candidates, b.features, b.cfg)
	} else {
		expected, err = dyn.RunPolicies(b.events, b.probes, b.candidates, b.cfg)
	}
	if err != nil {
		return empty, err
	}
	policies, err := verifyRecordedPolicies(o, manifest, expected)
	if err != nil {
		return empty, err
	}
	after, err := verificationInputs(o)
	if err != nil {
		return empty, err
	}
	_, finalCompletion, err := verificationManifest(o, b)
	if err != nil {
		return empty, err
	}
	if !reflect.DeepEqual(before, after) || completion != finalCompletion {
		return empty, problem("snapshot_conflict", "$")
	}
	finalBundle, err := load(o)
	if err != nil {
		return empty, err
	}
	finalPolicies, err := verifyRecordedPolicies(o, manifest, expected)
	if err != nil {
		return empty, err
	}
	if !reflect.DeepEqual(b, finalBundle) || !reflect.DeepEqual(policies, finalPolicies) {
		return empty, problem("snapshot_conflict", "$")
	}
	return nativeVerification{Protocol: "exp03-native-verification-v1", Status: "replay_matched",
		Scope: "offline_synthetic_not_product_authority_or_scientific_approval", ContractSHA256: dyn.ContractSHA256,
		InputManifestSHA256: b.cfg.InputManifestSHA256, ConfigSHA256: b.cfg.ConfigSHA256, RegistrySHA256: b.cfg.RegistrySHA256,
		Inputs: before, Completion: completion, Probes: b.probes, Policies: policies}, nil
}

func runVerify(args []string, stdout, stderr io.Writer) int {
	result, err := executeVerify(args)
	if err == nil {
		raw, encodeErr := jsonBytes(result)
		if encodeErr != nil {
			return 1
		}
		if len(raw) > maxBytes {
			err = problem("resource_limit", "$")
		} else {
			if n, writeErr := stdout.Write(raw); writeErr != nil || n != len(raw) {
				return 1
			}
			return 0
		}
	}
	issue := fixedError(err)
	// Keep arbitrary paths/keys/values out of both output and diagnostics.
	raw, encodeErr := jsonBytes(struct {
		Protocol string `json:"protocol"`
		Code     string `json:"code"`
		Path     string `json:"path"`
	}{"exp03-native-verification-error-v1", string(issue.Code), "$"})
	if encodeErr != nil {
		return 1
	}
	if n, writeErr := stderr.Write(raw); writeErr != nil || n != len(raw) {
		return 1
	}
	if issue.Code == "io_error" {
		return 1
	}
	return 2
}
