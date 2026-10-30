// Command memory-harness-dynamics executes explicit offline synthetic bundles.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	base "github.com/FelixBaek/arkism/internal/memoryharness"
	dyn "github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
)

const maxBytes = 67108864

type options struct {
	manifest, config, registry, out string
	p1                              bool
}

type bundle struct {
	events                       dyn.EventFileV2
	probes                       dyn.ProbeFileV2
	candidates                   base.CandidateFile
	features                     dyn.P1FeatureFileV1
	cfg                          dyn.DynamicsRunConfigV2
	selectedRawSHA, configRawSHA string
}

func problem(code, path string) error {
	return &dyn.ContractErrorV2{Code: dyn.V2ErrorCode(code), Path: path}
}
func rawSHA(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func fixedError(err error) *dyn.ContractErrorV2 {
	var v2 *dyn.ContractErrorV2
	if errors.As(err, &v2) {
		return &dyn.ContractErrorV2{Code: v2.Code, Path: v2.Path}
	}
	var v1 *base.ContractError
	if errors.As(err, &v1) {
		return &dyn.ContractErrorV2{Code: dyn.V2ErrorCode(v1.Code), Path: v1.Path}
	}
	return &dyn.ContractErrorV2{Code: "io_error", Path: "$"}
}

func parse(args []string) (options, error) {
	var o options
	if len(args) != 9 || args[0] != "run" {
		return o, problem("invalid_value", "$")
	}
	flags := map[string]string{}
	for i := 1; i < len(args); i += 2 {
		flag, value := args[i], args[i+1]
		if flag != "--manifest" && flag != "--p1-manifest" && flag != "--config" && flag != "--registry" && flag != "--out" {
			return o, problem("invalid_value", "$")
		}
		if _, exists := flags[flag]; exists || value == "" || strings.HasPrefix(value, "--") {
			return o, problem("invalid_value", "$")
		}
		flags[flag] = value
	}
	_, plain := flags["--manifest"]
	_, p1 := flags["--p1-manifest"]
	if plain == p1 || flags["--config"] == "" || flags["--registry"] == "" || flags["--out"] == "" {
		return o, problem("invalid_value", "$")
	}
	o = options{manifest: flags["--manifest"], config: flags["--config"], registry: flags["--registry"], out: flags["--out"], p1: p1}
	if p1 {
		o.manifest = flags["--p1-manifest"]
	}
	return o, nil
}

func openDirectory(path string) (*os.Root, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return nil, problem("unsafe_path", "$")
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return nil, problem("unsafe_path", "$")
		}
	}
	root, err := os.OpenRoot(filepath.VolumeName(path) + string(filepath.Separator))
	if err != nil {
		return nil, problem("io_error", "$")
	}
	relative, err := filepath.Rel(filepath.VolumeName(path)+string(filepath.Separator), filepath.Clean(path))
	if err != nil {
		root.Close()
		return nil, problem("unsafe_path", "$")
	}
	if relative == "." {
		return root, nil
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		next, stepErr := descend(root, part)
		root.Close()
		if stepErr != nil {
			return nil, stepErr
		}
		root = next
	}
	return root, nil
}

func descend(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, problem("io_error", "$")
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, problem("unsafe_path", "$")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, problem("io_error", "$")
	}
	opened, a := child.Stat(".")
	after, b := parent.Lstat(name)
	if a != nil || b != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		child.Close()
		return nil, problem("unsafe_path", "$")
	}
	return child, nil
}

func absolutePath(path string) bool {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func readRegular(path string) ([]byte, error) {
	if !absolutePath(path) {
		return nil, problem("unsafe_path", "$")
	}
	root, err := openDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return readAt(root, filepath.Base(path))
}

func readAt(root *os.Root, name string) ([]byte, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, problem("io_error", "$")
	}
	if !before.Mode().IsRegular() {
		return nil, problem("unsafe_path", "$")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, problem("io_error", "$")
	}
	defer file.Close()
	opened, a := file.Stat()
	after, b := root.Lstat(name)
	if a != nil || b != nil || !opened.Mode().IsRegular() || !after.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		return nil, problem("unsafe_path", "$")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, problem("io_error", "$")
	}
	if len(raw) > maxBytes {
		return nil, problem("resource_limit", "$")
	}
	return raw, nil
}

func roleRead(directory string, role dyn.RoleFile) ([]byte, error) {
	// DecodeStrict already validates relative POSIX components; traverse every
	// directory separately so an intermediate symlink is never followed.
	path := filepath.Join(directory, filepath.FromSlash(role.RelativePath))
	raw, err := readRegular(path)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != role.SizeBytes || rawSHA(raw) != role.RawSHA256 {
		return nil, problem("hash_mismatch", "$.files[*]")
	}
	return raw, nil
}

func selectedManifest(o options) (dyn.DynamicInputManifestV2, string, []byte, *dyn.RoleFile, error) {
	raw, err := readRegular(o.manifest)
	if err != nil {
		return dyn.DynamicInputManifestV2{}, "", nil, nil, err
	}
	baseRaw := raw
	identity := ""
	var feature *dyn.RoleFile
	if o.p1 {
		value, decodeErr := dyn.DecodeStrict(raw, "P1InputManifestV1")
		if decodeErr != nil {
			return dyn.DynamicInputManifestV2{}, "", nil, nil, decodeErr
		}
		wrapper := value.(dyn.P1InputManifestV1)
		identity = wrapper.ManifestSHA256
		feature = &wrapper.FeatureFile
		baseRaw, err = readRegular(filepath.Join(filepath.Dir(o.manifest), "dynamics-manifest.json"))
		if err != nil {
			return dyn.DynamicInputManifestV2{}, "", nil, nil, err
		}
		value, decodeErr = dyn.DecodeStrict(baseRaw, "DynamicInputManifestV2")
		if decodeErr != nil {
			return dyn.DynamicInputManifestV2{}, "", nil, nil, decodeErr
		}
		manifest := value.(dyn.DynamicInputManifestV2)
		if manifest.ManifestSHA256 != wrapper.BaseDynamicManifestSHA256 {
			return manifest, "", nil, nil, problem("hash_mismatch", "$.base_dynamic_manifest_sha256")
		}
		return manifest, identity, raw, feature, nil
	}
	value, err := dyn.DecodeStrict(baseRaw, "DynamicInputManifestV2")
	if err != nil {
		return dyn.DynamicInputManifestV2{}, "", nil, nil, err
	}
	manifest := value.(dyn.DynamicInputManifestV2)
	return manifest, manifest.ManifestSHA256, raw, nil, nil
}

func load(o options) (bundle, error) {
	var b bundle
	manifest, identity, selectedRaw, feature, err := selectedManifest(o)
	if err != nil {
		return b, err
	}
	roles, registryRole, err := loadRoles(o, manifest, feature)
	if err != nil {
		return b, err
	}
	cfg, configRaw, err := loadConfig(o, registryRole, identity)
	if err != nil {
		return b, err
	}
	b.cfg = cfg
	if err = loadRecords(&b, roles); err != nil {
		return b, err
	}
	if manifest.StreamID != b.events.StreamID || manifest.ClockDomain != b.events.ClockDomain {
		return b, problem("snapshot_binding", "$.stream_id")
	}
	if err = permittedRoles(b.cfg, roles); err != nil {
		return b, err
	}
	if raw, exists := roles["snapshot"]; exists {
		value, decodeErr := dyn.DecodeStrict(raw, "SnapshotFileV2")
		if decodeErr != nil {
			return b, decodeErr
		}
		if err = validateWitness(value.(dyn.SnapshotFileV2).Snapshot, b.events); err != nil {
			return b, err
		}
	}
	b.selectedRawSHA, b.configRawSHA = rawSHA(selectedRaw), rawSHA(configRaw)
	return b, nil
}

func loadRoles(o options, manifest dyn.DynamicInputManifestV2, feature *dyn.RoleFile) (map[dyn.InputRole][]byte, dyn.RoleFile, error) {
	roles := map[dyn.InputRole][]byte{}
	var registry dyn.RoleFile
	for _, role := range manifest.Files {
		if role.Role == "features" && feature != nil && *feature != role {
			return nil, registry, problem("snapshot_conflict", "$.feature_file")
		}
		raw, err := roleRead(filepath.Dir(o.manifest), role)
		if err != nil {
			return nil, registry, err
		}
		roles[role.Role] = raw
		if role.Role == "registry" {
			registry = role
		}
	}
	if feature != nil {
		raw, err := roleRead(filepath.Dir(o.manifest), *feature)
		if err != nil {
			return nil, registry, err
		}
		roles["features"] = raw
	}
	for _, role := range []dyn.InputRole{"events", "probes", "candidates", "registry"} {
		if _, found := roles[role]; !found {
			return nil, registry, problem("missing_field", "$.files")
		}
	}
	return roles, registry, nil
}

func loadConfig(o options, registryRole dyn.RoleFile, identity string) (dyn.DynamicsRunConfigV2, []byte, error) {
	var empty dyn.DynamicsRunConfigV2
	registryRaw, err := readRegular(o.registry)
	if err != nil {
		return empty, nil, err
	}
	if rawSHA(registryRaw) != registryRole.RawSHA256 || int64(len(registryRaw)) != registryRole.SizeBytes {
		return empty, nil, problem("hash_mismatch", "$.registry")
	}
	raw, err := readRegular(o.config)
	if err != nil {
		return empty, nil, err
	}
	value, err := dyn.DecodeStrict(raw, "DynamicsRunConfigV2")
	if err != nil {
		return empty, nil, err
	}
	cfg := value.(dyn.DynamicsRunConfigV2)
	value, err = dyn.DecodeStrict(registryRaw, "PolicyRegistryV2")
	if err != nil {
		return empty, nil, err
	}
	if !reflect.DeepEqual(value, cfg.Registry) || cfg.InputManifestSHA256 != identity {
		return empty, nil, problem("config_mismatch", "$.registry")
	}
	return cfg, raw, nil
}

func permittedRoles(cfg dyn.DynamicsRunConfigV2, roles map[dyn.InputRole][]byte) error {
	for _, entry := range cfg.Configs {
		for role := range roles {
			if !allowedRole(entry.PolicyID, role, cfg.Registry) {
				return problem("input_role_forbidden", "$.files[*].role")
			}
		}
	}
	return nil
}

func allowedRole(id dyn.PolicyID, role dyn.InputRole, registry dyn.PolicyRegistryV2) bool {
	for _, entry := range registry.Entries {
		if entry.PolicyID == id {
			for _, allowed := range entry.AllowedRoles {
				if allowed == role {
					return true
				}
			}
		}
	}
	return false
}

func loadRecords(b *bundle, roles map[dyn.InputRole][]byte) error {
	value, err := dyn.DecodeStrict(roles["events"], "EventFileV2")
	if err != nil {
		return err
	}
	b.events = value.(dyn.EventFileV2)
	value, err = dyn.DecodeStrict(roles["probes"], "ProbeFileV2")
	if err != nil {
		return err
	}
	b.probes = value.(dyn.ProbeFileV2)
	value, err = base.DecodeStrict(roles["candidates"], "CandidateFile")
	if err != nil {
		return err
	}
	b.candidates = value.(base.CandidateFile)
	if raw, exists := roles["features"]; exists {
		value, err = dyn.DecodeStrict(raw, "P1FeatureFileV1")
		if err != nil {
			return err
		}
		b.features = value.(dyn.P1FeatureFileV1)
	}
	return nil
}

func validateWitness(snapshot dyn.SnapshotV2, events dyn.EventFileV2) error {
	if snapshot.StreamID != events.StreamID || snapshot.Cursor.ClockDomain != events.ClockDomain || snapshot.ConfigSHA256 != events.ConfigSHA256 {
		return problem("snapshot_binding", "$.snapshot")
	}
	prefix, seen, invalid, seq, err := witnessPrefix(snapshot, events)
	if err != nil {
		return err
	}
	if seq != snapshot.Cursor.AcceptedSeq || prefix != snapshot.Cursor.PrefixSHA256 || len(snapshot.Receipts.Receipts) != len(seen) {
		return problem("prefix_mismatch", "$.snapshot.cursor")
	}
	if err = witnessReceipts(snapshot, seen); err != nil {
		return err
	}
	if err = witnessInvalidated(snapshot, invalid); err != nil {
		return err
	}
	return nil // Basic source-prefix witness only; never a numeric-history proof.
}

func witnessReceipts(snapshot dyn.SnapshotV2, seen map[string]dyn.EventV2) error {
	lastAt := int64(0)
	for _, receipt := range snapshot.Receipts.Receipts {
		event, exists := seen[receipt.EventID]
		if exists && event.AcceptedSeq == snapshot.Cursor.AcceptedSeq {
			lastAt = event.AcceptedAtNS
		}
		if !exists || receipt.Key != event.Key || receipt.PayloadSHA256 != event.PayloadSHA256 || receipt.AcceptedSeq != event.AcceptedSeq || receipt.AcceptedAtNS != event.AcceptedAtNS {
			return problem("snapshot_binding", "$.snapshot.receipts")
		}
	}
	if snapshot.Cursor.AcceptedAtNS != lastAt {
		return problem("prefix_mismatch", "$.snapshot.cursor.accepted_at_ns")
	}
	return nil
}

func witnessInvalidated(snapshot dyn.SnapshotV2, invalid map[dyn.RefV2]bool) error {
	for target := range invalid {
		found := false
		for _, entry := range snapshot.Receipts.LifecycleFence.Entries {
			if entry.Scope == snapshot.Scope && entry.Target == target && entry.Status == "invalidated" {
				found = true
			}
		}
		if !found {
			return problem("invalidated_target", "$.snapshot.receipts.lifecycle_fence")
		}
	}
	return nil
}

func witnessPrefix(snapshot dyn.SnapshotV2, events dyn.EventFileV2) (string, map[string]dyn.EventV2, map[dyn.RefV2]bool, int64, error) {
	prefix, err := dyn.CanonicalSHA256("StreamInitialPrefixProjectionV2", dyn.StreamInitialPrefixProjectionV2{StreamID: events.StreamID, ClockDomain: events.ClockDomain, Scope: snapshot.Scope, Dimension: snapshot.Dimension, ConfigSHA256: events.ConfigSHA256})
	if err != nil {
		return "", nil, nil, 0, err
	}
	seen := map[string]dyn.EventV2{}
	invalid := map[dyn.RefV2]bool{}
	seq := int64(0)
	for _, event := range events.Events {
		if _, retry := seen[event.EventID]; retry {
			continue
		}
		if event.AcceptedSeq > snapshot.Cursor.AcceptedSeq {
			break
		}
		if event.Key.Scope != snapshot.Scope || event.Key.Dimension != snapshot.Dimension {
			return "", nil, nil, 0, problem("snapshot_binding", "$.snapshot")
		}
		seen[event.EventID] = event
		seq = event.AcceptedSeq
		prefix, err = dyn.CanonicalSHA256("StreamAcceptedStepProjectionV2", dyn.StreamAcceptedStepProjectionV2{PreviousPrefixSHA256: prefix, StreamID: events.StreamID, ClockDomain: events.ClockDomain, AcceptedSeq: event.AcceptedSeq, AcceptedAtNS: event.AcceptedAtNS, PayloadSHA256: event.PayloadSHA256})
		if err != nil {
			return "", nil, nil, 0, err
		}
		if event.Kind == "invalidate" {
			invalid[event.Key.Target] = true
		}
	}
	return prefix, seen, invalid, seq, nil
}

func jsonBytes(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, problem("io_error", "$")
	}
	raw = append(raw, '\n')
	if len(raw) > maxBytes {
		return nil, problem("resource_limit", "$")
	}
	return raw, nil
}

func removeOwned(root *os.Root, name string, identity os.FileInfo) bool {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	return err == nil && info.Mode().IsRegular() && os.SameFile(info, identity) && root.Remove(name) == nil
}

func writeNew(root *os.Root, name string, data []byte) (os.FileInfo, error) {
	temporary := ".dynamics-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, problem("io_error", "$")
	}
	identity, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, problem("io_error", "$")
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if err == nil {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err == nil {
		var actual []byte
		actual, err = io.ReadAll(io.LimitReader(file, maxBytes+1))
		if err == nil && !bytes.Equal(data, actual) {
			err = io.ErrShortWrite
		}
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		removeOwned(root, temporary, identity)
		return nil, problem("io_error", "$")
	}
	if err = root.Link(temporary, name); err != nil {
		removeOwned(root, temporary, identity)
		if errors.Is(err, os.ErrExist) {
			return nil, problem("output_exists", "$")
		}
		return nil, problem("io_error", "$")
	}
	if !removeOwned(root, temporary, identity) {
		removeOwned(root, name, identity)
		return nil, problem("io_error", "$")
	}
	return identity, nil
}

func publish(o options, b bundle, runs []dyn.PredictionRunV2) (published dyn.CLIStatus, resultErr error) {
	var empty dyn.CLIStatus
	if !absolutePath(o.out) || filepath.Base(o.out) == "." || filepath.Base(o.out) == string(filepath.Separator) {
		return empty, problem("unsafe_path", "$")
	}
	parent, err := openDirectory(filepath.Dir(o.out))
	if err != nil {
		return empty, err
	}
	defer parent.Close()
	name := filepath.Base(o.out)
	if err = parent.Mkdir(name, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return empty, problem("output_exists", "$")
		}
		return empty, problem("io_error", "$")
	}
	identity, err := parent.Lstat(name)
	if err != nil {
		return empty, problem("io_error", "$")
	}
	root, err := descend(parent, name)
	if err != nil {
		return empty, err
	}
	defer root.Close()
	owned := map[string]os.FileInfo{}
	var predictionDirectory os.FileInfo
	complete := false
	defer func() {
		if !complete && !cleanupPublication(parent, root, name, identity, predictionDirectory, owned) {
			published, resultErr = empty, problem("io_error", "$")
		}
	}()
	if err = root.Mkdir("predictions", 0700); err != nil {
		return empty, problem("io_error", "$")
	}
	predictionDirectory, err = root.Lstat("predictions")
	if err != nil {
		return empty, problem("io_error", "$")
	}
	refs, planned, failed, err := publishPredictions(root, runs, owned)
	if err != nil {
		return empty, err
	}
	result, err := finishPublication(b, parent, root, name, identity, refs, planned, failed, owned)
	if err != nil {
		return empty, err
	}
	complete = true
	return result, nil
}

func publishPredictions(root *os.Root, runs []dyn.PredictionRunV2, owned map[string]os.FileInfo) ([]dyn.OutputRoleFile, int64, int64, error) {
	refs := []dyn.OutputRoleFile{}
	failed, planned := int64(0), int64(0)
	for _, run := range runs {
		if err := dyn.ValidateTyped("PredictionRunV2", run); err != nil {
			return nil, 0, 0, err
		}
		raw, marshalErr := jsonBytes(run)
		if marshalErr != nil {
			return nil, 0, 0, marshalErr
		}
		path := "predictions/" + string(run.PolicyID) + ".json"
		id, writeErr := writeNew(root, path, raw)
		if writeErr != nil {
			return nil, 0, 0, writeErr
		}
		owned[path] = id
		refs = append(refs, dyn.OutputRoleFile{Role: "prediction_run", RelativePath: path, RawSHA256: rawSHA(raw), SizeBytes: int64(len(raw))})
		planned += int64(len(run.Predictions))
		failed += run.Cost.FailedPredictions
	}
	return refs, planned, failed, nil
}

func cleanupPublication(parent, root *os.Root, name string, identity, predictionDirectory os.FileInfo, owned map[string]os.FileInfo) bool {
	clean := true
	for path, id := range owned {
		if !removeOwned(root, path, id) {
			clean = false
		}
	}
	if predictionDirectory != nil {
		info, err := root.Lstat("predictions")
		if err != nil || !os.SameFile(info, predictionDirectory) || root.Remove("predictions") != nil {
			clean = false
		}
	}
	info, err := parent.Lstat(name)
	if err != nil || !os.SameFile(info, identity) || parent.Remove(name) != nil {
		clean = false
	}
	return clean
}

func syncDirectories(root *os.Root) error {
	for _, path := range []string{"predictions", "."} {
		dir, err := root.Open(path)
		if err != nil {
			return problem("io_error", "$")
		}
		syncErr, closeErr := dir.Sync(), dir.Close()
		if syncErr != nil || closeErr != nil {
			return problem("io_error", "$")
		}
	}
	return nil
}

func run(args []string, stdout, stderr io.Writer) int {
	status, err := executeRun(args)
	if err == nil {
		raw, writeErr := jsonBytes(status)
		if writeErr != nil {
			return 1
		}
		count, writeErr := stdout.Write(raw)
		if writeErr != nil || count != len(raw) {
			return 1
		}
		return 0
	}
	issue := fixedError(err)
	status = dyn.CLIStatus{Status: "failed", Outputs: []dyn.OutputRoleFile{}, Error: issue}
	raw, marshalErr := jsonBytes(status)
	if marshalErr != nil {
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

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func finishPublication(b bundle, parent, root *os.Root, name string, identity os.FileInfo, refs []dyn.OutputRoleFile, planned, failed int64, owned map[string]os.FileInfo) (dyn.CLIStatus, error) {
	var empty dyn.CLIStatus
	status := dyn.OutputStatus("completed")
	if failed > 0 {
		status = "completed_with_failures"
	}
	manifest := dyn.OutputManifest{Protocol: "dynamics-output-manifest-v2", RunID: b.cfg.RunID, InputManifestSHA256: b.selectedRawSHA, RunConfigSHA256: b.cfg.ConfigSHA256, RunConfigRawSHA256: b.configRawSHA, RegistrySHA256: b.cfg.RegistrySHA256, Status: status, PlannedRows: planned, FailedRows: failed, Files: refs}
	if err := dyn.ValidateTyped("OutputManifest", manifest); err != nil {
		return empty, err
	}
	raw, err := jsonBytes(manifest)
	if err != nil {
		return empty, err
	}
	id, err := writeNew(root, "manifest.json", raw)
	if err != nil {
		return empty, err
	}
	owned["manifest.json"] = id
	info, err := parent.Lstat(name)
	if err != nil || !os.SameFile(info, identity) {
		return empty, problem("unsafe_path", "$")
	}
	refs = append(refs, dyn.OutputRoleFile{Role: "completion_manifest", RelativePath: "manifest.json", RawSHA256: rawSHA(raw), SizeBytes: int64(len(raw))})
	result := dyn.CLIStatus{Status: string(status), RunID: &b.cfg.RunID, Outputs: refs}
	if err = dyn.ValidateTyped("CLIStatus", result); err != nil {
		return empty, err
	}
	if err = syncDirectories(root); err != nil {
		return empty, err
	}
	return result, nil
}

func executeRun(args []string) (dyn.CLIStatus, error) {
	var empty dyn.CLIStatus
	o, err := parse(args)
	if err != nil {
		return empty, err
	}
	b, err := load(o)
	if err != nil {
		return empty, err
	}
	var runs []dyn.PredictionRunV2
	if o.p1 {
		runs, err = dyn.RunPoliciesP1(b.events, b.probes, b.candidates, b.features, b.cfg)
	} else {
		runs, err = dyn.RunPolicies(b.events, b.probes, b.candidates, b.cfg)
	}
	if err != nil {
		return empty, err
	}
	return publish(o, b, runs)
}
