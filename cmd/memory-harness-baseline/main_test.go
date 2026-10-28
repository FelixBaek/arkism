package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	harness "github.com/FelixBaek/arkism/internal/memoryharness"
)

func cliFixture(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	fixture := filepath.Join(parent, "fixture")
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", filepath.Join(repository, "evaluation", "memory-harness-v1", "fixture_gen.py"),
		"--seed", "17", "--contract", filepath.Join(repository, "evaluation", "memory-harness-v1", "contracts", "implementation_contract.json"), "--out", fixture)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture generation failed: %v: %s", err, output)
	}
	return fixture
}

func cliArgs(fixture, output string) []string {
	return []string{"-manifest", filepath.Join(fixture, "input_manifest.json"), "-run-id", "baseline-seed17", "-output", output}
}

func cliCall(args []string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func cliError(t *testing.T, args []string, wantCode int, wantError string) {
	t.Helper()
	code, stdout, stderr := cliCall(args)
	if code != wantCode || stdout != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var problem map[string]string
	if err := json.Unmarshal([]byte(stderr), &problem); err != nil {
		t.Fatalf("error JSON: %v", err)
	}
	if len(problem) != 3 || problem["code"] != wantError {
		t.Fatalf("error=%v", problem)
	}
	if !strings.HasSuffix(stderr, "\n") || strings.Count(stderr, "\n") != 1 {
		t.Fatalf("not one JSON line: %q", stderr)
	}
	if strings.Contains(stderr, "secret") {
		t.Fatalf("raw input leaked: %s", stderr)
	}
}

func cliRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func cliWrite(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func cliManifest(t *testing.T, fixture string) harness.InputManifest {
	t.Helper()
	value, err := harness.DecodeStrict(cliRead(t, filepath.Join(fixture, "input_manifest.json")), "InputManifest")
	if err != nil {
		t.Fatal(err)
	}
	return value.(harness.InputManifest)
}

func cliRehash(t *testing.T, fixture string) {
	t.Helper()
	manifest := cliManifest(t, fixture)
	for i := range manifest.Files {
		data := cliRead(t, filepath.Join(fixture, manifest.Files[i].LogicalPath))
		manifest.Files[i].SHA256 = hash(data)
		manifest.Files[i].ByteLength = int64(len(data))
	}
	cliWrite(t, filepath.Join(fixture, "input_manifest.json"), manifest)
}

func TestCLIFixtureAllScheduledQueriesAndIndependentSelection(t *testing.T) {
	fixture := cliFixture(t)
	output := filepath.Join(t.TempDir(), "predictions.json")
	code, stdout, stderr := cliCall(cliArgs(fixture, output))
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	var status struct {
		Status, Output string
		Queries        int
	}
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "success" || status.Output != output || status.Queries != 7 {
		t.Fatalf("status=%+v", status)
	}
	data := cliRead(t, output)
	if data[len(data)-1] != '\n' || !bytes.Contains(data, []byte("\n  \"contract_id\"")) {
		t.Fatal("output formatting")
	}
	value, err := harness.DecodeStrict(data, "PredictionRun")
	if err != nil {
		t.Fatal(err)
	}
	prediction := value.(harness.PredictionRun)
	if prediction.InputManifestSHA256 != hash(cliRead(t, filepath.Join(fixture, "input_manifest.json"))) || prediction.PolicySHA256 != hash(cliRead(t, filepath.Join(fixture, "policy.json"))) {
		t.Fatal("raw hash binding")
	}
	inputs, err := load(filepath.Join(fixture, "input_manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Hand-transcribed GV-RRF-04/05/08/09 and GV-PACK-01/02. No Python RRF.
	expected := map[string][]string{"q01": {"memory:a", "memory:c", "memory:b"}, "q02": {"fixture_alt:a", "memory:a"}, "q03": {"memory:b"}, "q04": {"memory:b", "memory:c"}, "q05": {}, "q06": {}, "q07": {"memory:a"}}
	if len(prediction.Predictions) != len(inputs.probes.Probes) {
		t.Fatal("schedule coverage")
	}
	for i, row := range prediction.Predictions {
		probe := inputs.probes.Probes[i]
		if row.QueryID != probe.QueryID || row.StreamID != probe.StreamID || row.Scope != probe.Scope || row.Status != "success" || row.ErrorCode != "none" {
			t.Fatalf("row binding: %+v", row)
		}
		selected := []string{}
		for _, ref := range row.SelectedRefs {
			selected = append(selected, ref.Origin+":"+ref.ID)
			if ref.Revision != 1 || ref.ContentSHA256 != hash([]byte(ref.ID)) {
				t.Fatal("full ref identity")
			}
		}
		if !reflect.DeepEqual(selected, expected[row.QueryID]) {
			t.Fatalf("query %s selected=%v expected=%v", row.QueryID, selected, expected[row.QueryID])
		}
	}
}

func TestCLIDeterministicReplayAndInputBytesUnchanged(t *testing.T) {
	fixture := cliFixture(t)
	before := map[string][]byte{}
	entries, err := os.ReadDir(fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		before[entry.Name()] = cliRead(t, filepath.Join(fixture, entry.Name()))
	}
	a, b := filepath.Join(t.TempDir(), "a.json"), filepath.Join(t.TempDir(), "b.json")
	for _, output := range []string{a, b} {
		code, _, stderr := cliCall(cliArgs(fixture, output))
		if code != 0 {
			t.Fatal(stderr)
		}
	}
	if !bytes.Equal(cliRead(t, a), cliRead(t, b)) {
		t.Fatal("same input and run ID changed raw output")
	}
	for name, data := range before {
		if !bytes.Equal(cliRead(t, filepath.Join(fixture, name)), data) {
			t.Fatalf("input changed: %s", name)
		}
	}
}

func TestCLINeverNeedsGoldSplitOrEvaluationManifest(t *testing.T) {
	fixture := cliFixture(t)
	for _, name := range []string{"gold.json", "split_map.json", "evaluation_manifest.json"} {
		if err := os.Remove(filepath.Join(fixture, name)); err != nil {
			t.Fatal(err)
		}
	}
	code, _, stderr := cliCall(cliArgs(fixture, filepath.Join(t.TempDir(), "predictions.json")))
	if code != 0 {
		t.Fatal(stderr)
	}
}

func TestCLIFlagsAndHelp(t *testing.T) {
	fixture := cliFixture(t)
	args := cliArgs(fixture, filepath.Join(t.TempDir(), "out.json"))
	invalid := [][]string{nil, args[:2], append(append([]string{}, args...), "-run-id", "other"), append(append([]string{}, args...), "-gold", "secret-value"), append(append([]string{}, args...), "-ode", "secret"), append(append([]string{}, args...), "secret-positional"), {"-manifest="}, {"-h", "secret"}}
	for i, value := range invalid {
		t.Run(string(rune('a'+i)), func(t *testing.T) { cliError(t, value, 2, "invalid_value") })
	}
	for _, help := range []string{"-h", "--help", "-help"} {
		code, stdout, stderr := cliCall([]string{help})
		if code != 0 || stdout == "" || stderr != "" {
			t.Fatalf("help code%d", code)
		}
	}
	inline := []string{"--manifest=" + filepath.Join(fixture, "input_manifest.json"), "--run-id=inline", "--output=" + filepath.Join(t.TempDir(), "out.json")}
	if code, _, stderr := cliCall(inline); code != 0 {
		t.Fatal(stderr)
	}
}

func TestCLIInvalidRunID(t *testing.T) {
	fixture := cliFixture(t)
	output := filepath.Join(t.TempDir(), "out.json")
	args := cliArgs(fixture, output)
	args[3] = "secret Invalid"
	cliError(t, args, 2, "invalid_value")
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid run ID published output")
	}
}

func TestCLIHashMismatchAndFixedContractPin(t *testing.T) {
	for _, mode := range []string{"newline", "contract"} {
		t.Run(mode, func(t *testing.T) {
			fixture := cliFixture(t)
			output := filepath.Join(t.TempDir(), "out.json")
			if mode == "newline" {
				path := filepath.Join(fixture, "candidates.json")
				if err := os.WriteFile(path, append(cliRead(t, path), '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(filepath.Join(fixture, "implementation_contract.json"), []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
				cliRehash(t, fixture)
			}
			cliError(t, cliArgs(fixture, output), 2, "hash_mismatch")
			if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("hash failure published output")
			}
		})
	}
}

func TestCLIMalformedAndUnsupportedPolicy(t *testing.T) {
	for _, mode := range []string{"json", "policy", "version", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			fixture := cliFixture(t)
			path := filepath.Join(fixture, "policy.json")
			want := "invalid_value"
			switch mode {
			case "json":
				if err := os.WriteFile(path, []byte("{\"secret\":"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "invalid_json"
			case "duplicate":
				if err := os.WriteFile(path, []byte("{\"secret\":1,\"secret\":2}"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "duplicate_key"
			default:
				var policy harness.Policy
				if err := json.Unmarshal(cliRead(t, path), &policy); err != nil {
					t.Fatal(err)
				}
				if mode == "policy" {
					policy.PolicyID = "ode-secret"
				} else {
					policy.ContractID = "secret-version"
					want = "unsupported_version"
				}
				cliWrite(t, path, policy)
			}
			cliRehash(t, fixture)
			output := filepath.Join(t.TempDir(), "out.json")
			cliError(t, cliArgs(fixture, output), 2, want)
			if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid policy published output")
			}
		})
	}
}

func TestCLIWholeInputValidatedBeforePublication(t *testing.T) {
	fixture := cliFixture(t)
	path := filepath.Join(fixture, "probes.json")
	value, err := harness.DecodeStrict(cliRead(t, path), "ProbeFile")
	if err != nil {
		t.Fatal(err)
	}
	probes := value.(harness.ProbeFile)
	probes.Probes[len(probes.Probes)-1].AtNS++
	cliWrite(t, path, probes)
	cliRehash(t, fixture)
	output := filepath.Join(t.TempDir(), "out.json")
	cliError(t, cliArgs(fixture, output), 2, "snapshot_binding")
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("late malformed query published partial run")
	}
}

func TestCLIManifestRoleCannotExpandInputs(t *testing.T) {
	fixture := cliFixture(t)
	manifest := cliManifest(t, fixture)
	data := cliRead(t, filepath.Join(fixture, "gold.json"))
	manifest.Files = append(manifest.Files, harness.ArtifactEntry{LogicalPath: "gold.json", SHA256: hash(data), ByteLength: int64(len(data))})
	cliWrite(t, filepath.Join(fixture, "input_manifest.json"), manifest)
	cliError(t, cliArgs(fixture, filepath.Join(t.TempDir(), "out.json")), 2, "invalid_value")
}

func TestCLIExistingOutputFileDirectoryAndLinkPreserved(t *testing.T) {
	fixture := cliFixture(t)
	parent := t.TempDir()
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(parent, "directory")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{file, directory, link} {
		cliError(t, cliArgs(fixture, output), 2, "output_exists")
	}
	if string(cliRead(t, file)) != "original" {
		t.Fatal("existing output overwritten")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 3 {
		t.Fatal("temporary output leaked")
	}
}

func TestCLISymlinkAndTraversalPathsRejected(t *testing.T) {
	fixture := cliFixture(t)
	parent := t.TempDir()
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(fixture, alias); err != nil {
		t.Fatal(err)
	}
	args := cliArgs(alias, filepath.Join(parent, "out.json"))
	cliError(t, args, 2, "unsafe_path")
	outAlias := filepath.Join(parent, "out-alias")
	if err := os.Symlink(parent, outAlias); err != nil {
		t.Fatal(err)
	}
	cliError(t, cliArgs(fixture, filepath.Join(outAlias, "out.json")), 2, "unsafe_path")
	args = cliArgs(fixture, filepath.Join(parent, "sub", "..", "out.json"))
	// filepath.Join cleans '..'; supply the explicit traversal string itself.
	args[5] = parent + string(filepath.Separator) + "sub" + string(filepath.Separator) + ".." + string(filepath.Separator) + "out.json"
	cliError(t, args, 2, "unsafe_path")
	policy := filepath.Join(fixture, "policy.json")
	original := cliRead(t, policy)
	if err := os.Remove(policy); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fixture, "other.json")
	if err := os.WriteFile(target, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, policy); err != nil {
		t.Fatal(err)
	}
	cliError(t, cliArgs(fixture, filepath.Join(parent, "out.json")), 2, "unsafe_path")
}

func TestCLIMissingAndNonregularInputs(t *testing.T) {
	for _, mode := range []string{"missing", "directory"} {
		t.Run(mode, func(t *testing.T) {
			fixture := cliFixture(t)
			path := filepath.Join(fixture, "policy.json")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			code, want := 1, "io_error"
			if mode == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				code, want = 2, "unsafe_path"
			}
			cliError(t, cliArgs(fixture, filepath.Join(t.TempDir(), "out.json")), code, want)
		})
	}
}

func TestCLIOutputParentMissingIsIOFailure(t *testing.T) {
	fixture := cliFixture(t)
	output := filepath.Join(t.TempDir(), "missing", "out.json")
	cliError(t, cliArgs(fixture, output), 1, "io_error")
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("I/O failure left output")
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("secret write failure") }

func TestCLIStatusWriteFailureRetractsOnlyOwnedOutput(t *testing.T) {
	fixture := cliFixture(t)
	parent := t.TempDir()
	output := filepath.Join(parent, "out.json")
	var stderr bytes.Buffer
	if code := run(cliArgs(fixture, output), brokenWriter{}, &stderr); code != 1 {
		t.Fatalf("code%d", code)
	}
	if strings.Contains(stderr.String(), "secret") {
		t.Fatal("raw writer error leaked")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed run left output/temp")
	}
}

func TestCLIAtomicWriterFailureAndExclusiveRace(t *testing.T) {
	parent := t.TempDir()
	root, err := os.OpenRoot(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	partial := func(file *os.File, data []byte) error {
		if _, err := file.Write(data[:1]); err != nil {
			return err
		}
		return errors.New("injected")
	}
	if _, err := writeNew(root, "out.json", []byte("complete"), partial); err == nil {
		t.Fatal("injected failure accepted")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("partial output remains")
	}
	competing := func(file *os.File, data []byte) error {
		if err := completeWrite(file, data); err != nil {
			return err
		}
		return root.WriteFile("out.json", []byte("original"), 0600)
	}
	_, err = writeNew(root, "out.json", []byte("complete"), competing)
	var problem *harness.ContractError
	if !errors.As(err, &problem) || problem.Code != "output_exists" {
		t.Fatalf("error%v", err)
	}
	if string(cliRead(t, filepath.Join(parent, "out.json"))) != "original" {
		t.Fatal("racing output replaced")
	}
	entries, err = os.ReadDir(parent)
	if err != nil || len(entries) != 1 {
		t.Fatal("temp leaked")
	}
}

func TestCLIStderrWriteFailureIsExitOne(t *testing.T) {
	if code := run(nil, io.Discard, brokenWriter{}); code != 1 {
		t.Fatalf("code%d", code)
	}
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) { return len(data) / 2, nil }

func TestCLIShortStatusWriteIsIOFailure(t *testing.T) {
	fixture := cliFixture(t)
	parent := t.TempDir()
	output := filepath.Join(parent, "out.json")
	var stderr bytes.Buffer
	if code := run(cliArgs(fixture, output), shortWriter{}, &stderr); code != 1 {
		t.Fatalf("short status write code=%d", code)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("short status write left output/temp")
	}
}
