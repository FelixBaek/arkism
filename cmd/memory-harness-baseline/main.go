// Command memory-harness-baseline executes the fixed offline synthetic policy.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	harness "github.com/FelixBaek/arkism/internal/memoryharness"
)

const contractID = "arkism-offline-b0-v1"
const policyID = "b0-rrf-synthetic-v1"

func fail(code, path string) error { return &harness.ContractError{Code: code, Path: path} }
func hash(data []byte) string      { value := sha256.Sum256(data); return hex.EncodeToString(value[:]) }

type options struct{ manifest, runID, output string }

func parse(args []string) (options, bool, error) {
	var result options
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "-help") {
		return result, true, nil
	}
	values := map[string]string{}
	for i := 0; i < len(args); i++ {
		raw := args[i]
		if !strings.HasPrefix(raw, "-") {
			return result, false, fail("invalid_value", "$")
		}
		name, value, inline := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(raw, "-"), "-"), "=")
		if name != "manifest" && name != "run-id" && name != "output" {
			return result, false, fail("invalid_value", "$")
		}
		if _, exists := values[name]; exists {
			return result, false, fail("invalid_value", "$")
		}
		if !inline {
			i++
			if i >= len(args) {
				return result, false, fail("invalid_value", "$")
			}
			value = args[i]
		}
		if value == "" {
			return result, false, fail("invalid_value", "$")
		}
		values[name] = value
	}
	if len(values) != 3 {
		return result, false, fail("invalid_value", "$")
	}
	result = options{values["manifest"], values["run-id"], values["output"]}
	// Use the accepted DTO validator for ID syntax rather than duplicating it.
	empty := harness.PredictionRun{ContractID: contractID, RunID: result.runID, PolicyID: policyID,
		InputManifestSHA256: strings.Repeat("0", 64), PolicySHA256: strings.Repeat("0", 64), Predictions: []harness.Prediction{}}
	if err := harness.ValidateTyped("PredictionRun", empty); err != nil {
		return result, false, err
	}
	return result, false, nil
}

// openParent pins each explicitly supplied directory and rejects symlink path
// components. SameFile checks also reject replacement during opening, without
// introducing platform-specific syscalls or product dependencies.
func openParent(path string) (*os.Root, string, string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return nil, "", "", fail("unsafe_path", "$")
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return nil, "", "", fail("unsafe_path", "$")
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, "", "", fail("unsafe_path", "$")
	}
	name := filepath.Base(absolute)
	if name == "." || name == string(filepath.Separator) {
		return nil, "", "", fail("unsafe_path", "$")
	}
	anchor := filepath.VolumeName(absolute) + string(filepath.Separator)
	root, err := os.OpenRoot(anchor)
	if err != nil {
		return nil, "", "", fail("io_error", "$")
	}
	relative, err := filepath.Rel(anchor, filepath.Dir(absolute))
	if err != nil {
		root.Close()
		return nil, "", "", fail("unsafe_path", "$")
	}
	if relative != "." {
		for _, part := range strings.Split(relative, string(filepath.Separator)) {
			child, stepErr := descend(root, part)
			root.Close()
			if stepErr != nil {
				return nil, "", "", stepErr
			}
			root = child
		}
	}
	return root, name, absolute, nil
}

func descend(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, fail("io_error", "$")
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fail("unsafe_path", "$")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, fail("io_error", "$")
	}
	opened, openedErr := child.Stat(".")
	after, afterErr := parent.Lstat(name)
	if openedErr != nil || afterErr != nil {
		child.Close()
		return nil, fail("io_error", "$")
	}
	if after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		child.Close()
		return nil, fail("unsafe_path", "$")
	}
	return child, nil
}

func readRegular(root *os.Root, name string) ([]byte, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, fail("io_error", "$")
	}
	if !before.Mode().IsRegular() {
		return nil, fail("unsafe_path", "$")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fail("io_error", "$")
	}
	defer file.Close()
	opened, openedErr := file.Stat()
	after, afterErr := root.Lstat(name)
	if openedErr != nil || afterErr != nil {
		return nil, fail("io_error", "$")
	}
	if !opened.Mode().IsRegular() || !after.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		return nil, fail("unsafe_path", "$")
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fail("io_error", "$")
	}
	return data, nil
}

type inputs struct {
	manifest                 harness.InputManifest
	manifestHash, policyHash string
	probes                   harness.ProbeFile
	candidates               harness.CandidateFile
	policy                   harness.Policy
}

func load(path string) (inputs, error) {
	var result inputs
	root, name, _, err := openParent(path)
	if err != nil {
		return result, err
	}
	defer root.Close()
	manifestBytes, err := readRegular(root, name)
	if err != nil {
		return result, err
	}
	value, err := harness.DecodeStrict(manifestBytes, "InputManifest")
	if err != nil {
		return result, err
	}
	result.manifest = value.(harness.InputManifest)
	result.manifestHash = hash(manifestBytes)
	raw := map[string][]byte{}
	for _, entry := range result.manifest.Files {
		// The compiled InputManifest validates the four fixed basenames and order.
		data, readErr := readRegular(root, entry.LogicalPath)
		if readErr != nil {
			return result, readErr
		}
		if int64(len(data)) != entry.ByteLength || hash(data) != entry.SHA256 {
			return result, fail("hash_mismatch", "$.files[*]")
		}
		raw[entry.LogicalPath] = data
	}
	if hash(raw["implementation_contract.json"]) != harness.ContractFileSHA256 {
		return result, fail("hash_mismatch", "$.files[*]")
	}
	// Match the common Go/Python fixed argument order on multiply-invalid inputs.
	value, err = harness.DecodeStrict(raw["probes.json"], "ProbeFile")
	if err != nil {
		return result, err
	}
	result.probes = value.(harness.ProbeFile)
	value, err = harness.DecodeStrict(raw["candidates.json"], "CandidateFile")
	if err != nil {
		return result, err
	}
	result.candidates = value.(harness.CandidateFile)
	value, err = harness.DecodeStrict(raw["policy.json"], "Policy")
	if err != nil {
		return result, err
	}
	result.policy = value.(harness.Policy)
	result.policyHash = hash(raw["policy.json"])
	if err := harness.ValidateBaselineInputs(result.probes, result.candidates, result.policy); err != nil {
		return result, err
	}
	return result, nil
}

func predict(input inputs, runID string) (harness.PredictionRun, error) {
	result := harness.PredictionRun{ContractID: contractID, RunID: runID, PolicyID: policyID,
		InputManifestSHA256: input.manifestHash, PolicySHA256: input.policyHash, Predictions: []harness.Prediction{}}
	snapshots := map[string]harness.CandidateSnapshot{}
	for _, snapshot := range input.candidates.Snapshots {
		snapshots[snapshot.SnapshotID] = snapshot
	}
	for _, probe := range input.probes.Probes {
		selected, err := harness.RankAndPack(snapshots[probe.CandidateSnapshotID], probe, input.policy)
		if err != nil {
			return result, err
		}
		refs := []harness.Ref{}
		for _, ranked := range selected.Selected {
			refs = append(refs, ranked.Candidate.Ref)
		}
		result.Predictions = append(result.Predictions, harness.Prediction{QueryID: probe.QueryID,
			StreamID: probe.StreamID, Scope: probe.Scope, Status: "success", SelectedRefs: refs, ErrorCode: "none"})
	}
	return result, harness.ValidateTyped("PredictionRun", result)
}

func removeOwned(root *os.Root, name string, identity os.FileInfo) bool {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	return err == nil && info.Mode().IsRegular() && os.SameFile(info, identity) && root.Remove(name) == nil
}

func completeWrite(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

// writeNew completes a temporary inode before atomic non-overwriting hard-link
// publication. The returned identity permits rollback of our output only.
func writeNew(root *os.Root, name string, data []byte, writer func(*os.File, []byte) error) (os.FileInfo, error) {
	if _, err := root.Lstat(name); err == nil {
		return nil, fail("output_exists", "$")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fail("io_error", "$")
	}
	temporary := ".arkism-harness-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fail("io_error", "$")
	}
	identity, err := file.Stat()
	if err != nil {
		file.Close()
		detail := ""
		if cleanupErr := root.Remove(temporary); cleanupErr != nil {
			detail = "owned temporary artifact remains"
		}
		return nil, &harness.ContractError{Code: "io_error", Path: "$", Detail: detail}
	}
	writeErr := writer(file, data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		detail := ""
		if !removeOwned(root, temporary, identity) {
			detail = "owned temporary artifact remains"
		}
		return nil, &harness.ContractError{Code: "io_error", Path: "$", Detail: detail}
	}
	if err := root.Link(temporary, name); err != nil {
		code, detail := "io_error", ""
		if errors.Is(err, os.ErrExist) {
			code = "output_exists"
		}
		if !removeOwned(root, temporary, identity) {
			detail = "owned temporary artifact remains"
		}
		return nil, &harness.ContractError{Code: code, Path: "$", Detail: detail}
	}
	if !removeOwned(root, temporary, identity) {
		removeOwned(root, name, identity)
		return nil, &harness.ContractError{Code: "io_error", Path: "$", Detail: "owned temporary artifact remains"}
	}
	return identity, nil
}

func writeJSON(writer io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	count, err := writer.Write(data)
	if err == nil && count != len(data) {
		return io.ErrShortWrite
	}
	return err
}

func execute(options options, stdout io.Writer) error {
	input, err := load(options.manifest)
	if err != nil {
		return err
	}
	result, err := predict(input, options.runID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fail("io_error", "$")
	}
	data = append(data, '\n')
	root, name, absolute, err := openParent(options.output)
	if err != nil {
		return err
	}
	defer root.Close()
	identity, err := writeNew(root, name, data, completeWrite)
	if err != nil {
		return err
	}
	status := struct {
		Status  string `json:"status"`
		Output  string `json:"output"`
		Queries int    `json:"queries"`
	}{"success", absolute, len(result.Predictions)}
	if err := writeJSON(stdout, status); err != nil {
		detail := ""
		if !removeOwned(root, name, identity) {
			detail = "output cleanup incomplete"
		}
		return &harness.ContractError{Code: "io_error", Path: "$", Detail: detail}
	}
	return nil
}

func run(args []string, stdout, stderr io.Writer) int {
	options, help, err := parse(args)
	if help {
		text := "usage: memory-harness-baseline -manifest FILE -run-id ID -output NEW_FILE\n"
		count, err := io.WriteString(stdout, text)
		if err != nil || count != len(text) {
			return 1
		}
		return 0
	}
	if err == nil {
		err = execute(options, stdout)
	}
	if err == nil {
		return 0
	}
	problem := &harness.ContractError{Code: "io_error", Path: "$"}
	var typed *harness.ContractError
	if errors.As(err, &typed) {
		problem = typed
	}
	// Only fixed explanations and schema paths reach stderr, never raw OS errors.
	report := struct {
		Code   string `json:"code"`
		Path   string `json:"path"`
		Detail string `json:"detail"`
	}{problem.Code, problem.Path, problem.Detail}
	if encodeErr := writeJSON(stderr, report); encodeErr != nil {
		return 1
	}
	if problem.Code == "io_error" {
		return 1
	}
	return 2
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
