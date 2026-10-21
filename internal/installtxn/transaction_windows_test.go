package installtxn

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type diskMetadata struct {
	path                               string
	failApply, failVerify, failRestore bool
	stopCalls                          int
	failStop                           int
}

func (m *diskMetadata) Commit([]byte) error      { return nil }
func (m *diskMetadata) Capture() ([]byte, error) { return os.ReadFile(m.path) }
func (m *diskMetadata) Apply() error {
	if err := os.WriteFile(m.path, []byte("new metadata"), 0600); err != nil {
		return err
	}
	if m.failApply {
		return errors.New("injected_apply")
	}
	return nil
}
func (m *diskMetadata) Verify() error {
	if m.failVerify {
		return errors.New("injected_verify")
	}
	data, err := os.ReadFile(m.path)
	if err != nil {
		return err
	}
	if string(data) != "new metadata" {
		return errors.New("metadata_mismatch")
	}
	return nil
}
func (m *diskMetadata) Restore(data []byte) error {
	if m.failRestore {
		return errors.New("injected_restore")
	}
	return os.WriteFile(m.path, data, 0600)
}
func (m *diskMetadata) stop(string) error {
	m.stopCalls++
	if m.failStop == m.stopCalls {
		return errors.New("injected_stop")
	}
	return nil
}

type fixture struct {
	root, live, source string
	metadata           *diskMetadata
	expected           Manifest
}

func newFixture(t *testing.T, old bool) fixture {
	t.Helper()
	root := t.TempDir()
	fmt.Println("Only owned transaction test cleanup root: " + root)
	live := filepath.Join(root, "installed Arkism")
	source := filepath.Join(root, "source")
	writePayload(t, source, "new")
	if old {
		writePayload(t, live, "old")
		mustWrite(t, filepath.Join(live, "unrelated-kept.txt"), "preserve prior unknown bytes")
	}
	metadata := &diskMetadata{path: filepath.Join(root, "metadata.fixture")}
	mustWrite(t, metadata.path, "old metadata")
	expected, err := Inventory(source)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{root, live, source, metadata, expected}
}
func writePayload(t *testing.T, root, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "runtime", "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "Arkism.exe"), version+" app")
	mustWrite(t, filepath.Join(root, "runtime", "memoryd.exe"), version+" runtime")
}
func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func (f fixture) request() Request {
	return Request{f.live, f.source, f.expected, f.metadata, f.metadata.stop}
}
func assertOld(t *testing.T, f fixture, old bool) {
	t.Helper()
	if old {
		if mustRead(t, filepath.Join(f.live, "Arkism.exe")) != "old app" || mustRead(t, filepath.Join(f.live, "runtime", "memoryd.exe")) != "old runtime" {
			t.Fatal("mixed payload")
		}
		if mustRead(t, filepath.Join(f.live, "unrelated-kept.txt")) != "preserve prior unknown bytes" {
			t.Fatal("unknown prior file lost")
		}
	} else if _, err := os.Lstat(f.live); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fresh failed install left live payload")
	}
	if mustRead(t, f.metadata.path) != "old metadata" {
		t.Fatal("metadata not restored")
	}
}
func TestInstallPublishesWholePayloadAndRetainsPrevious(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprint(old), func(t *testing.T) {
			f := newFixture(t, old)
			result, err := new(Engine).Install(f.request())
			if err != nil || !result.Committed || result.Archive == "" {
				t.Fatalf("%+v %v", result, err)
			}
			if err = verify(f.live, f.expected); err != nil {
				t.Fatal(err)
			}
			if err = f.metadata.Verify(); err != nil {
				t.Fatal(err)
			}
			if old && mustRead(t, filepath.Join(result.Archive, "previous", "unrelated-kept.txt")) != "preserve prior unknown bytes" {
				t.Fatal("previous bytes lost")
			}
			if _, err = os.Lstat(filepath.Join(f.live+".versions", "pending")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("pending remains")
			}
		})
	}
}
func TestInstallEveryPrecommitFailureRestoresWholeVersion(t *testing.T) {
	for _, old := range []bool{false, true} {
		for _, point := range []string{"staged", "prepared", "old_moved", "new_moved", "metadata_applied", "apply", "verify", "stop"} {
			t.Run(fmt.Sprintf("old=%v/%s", old, point), func(t *testing.T) {
				f := newFixture(t, old)
				f.metadata.failApply = point == "apply"
				f.metadata.failVerify = point == "verify"
				if point == "stop" {
					f.metadata.failStop = 1
				}
				engine := &Engine{checkpoint: func(name string) error {
					if name == point {
						return errors.New("injected_" + point)
					}
					return nil
				}}
				result, err := engine.Install(f.request())
				if err == nil || result.Committed || !result.Recovered {
					t.Fatalf("%+v %v", result, err)
				}
				assertOld(t, f, old)
			})
		}
	}
}
func TestInstallRecoveryFailureRetainsEvidenceAndRetries(t *testing.T) {
	f := newFixture(t, true)
	f.metadata.failApply = true
	f.metadata.failRestore = true
	result, err := new(Engine).Install(f.request())
	if err == nil || result.Committed || !strings.Contains(err.Error(), "injected_apply") || !strings.Contains(err.Error(), "injected_restore") {
		t.Fatalf("%+v %v", result, err)
	}
	if mustRead(t, filepath.Join(f.live, "Arkism.exe")) != "old app" {
		t.Fatal("payload not restored first")
	}
	f.metadata.failRestore = false
	result, err = new(Engine).Recover(f.live, f.metadata, f.metadata.stop)
	if err != nil || !result.Recovered {
		t.Fatalf("%+v %v", result, err)
	}
	assertOld(t, f, true)
}
func TestInstallRecoveryStopFailureDoesNotMoveEitherPayload(t *testing.T) {
	f := newFixture(t, true)
	f.metadata.failApply = true
	f.metadata.failStop = 2
	_, err := new(Engine).Install(f.request())
	if err == nil || !strings.Contains(err.Error(), "injected_stop") {
		t.Fatal(err)
	}
	if mustRead(t, filepath.Join(f.live, "Arkism.exe")) != "new app" || mustRead(t, filepath.Join(f.live+".versions", "pending", "previous", "Arkism.exe")) != "old app" {
		t.Fatal("moved payload without process proof")
	}
	f.metadata.failStop = 0
	if _, err = new(Engine).Recover(f.live, f.metadata, f.metadata.stop); err != nil {
		t.Fatal(err)
	}
	assertOld(t, f, true)
}
func TestInstallCommittedArchiveFailureDoesNotRollBack(t *testing.T) {
	f := newFixture(t, true)
	engine := &Engine{checkpoint: func(name string) error {
		if name == "committed" {
			return errors.New("injected_archive")
		}
		return nil
	}}
	result, err := engine.Install(f.request())
	if err == nil || !result.Committed {
		t.Fatalf("%+v %v", result, err)
	}
	if err = verify(f.live, f.expected); err != nil {
		t.Fatal(err)
	}
	result, err = new(Engine).Recover(f.live, f.metadata, f.metadata.stop)
	if err != nil || !result.Committed {
		t.Fatalf("%+v %v", result, err)
	}
	if err = f.metadata.Verify(); err != nil {
		t.Fatal(err)
	}
}

// Issue #149: a committed checkpoint whose live files were later replaced.
func staleCommittedFixture(t *testing.T) (fixture, Request) {
	t.Helper()
	f := newFixture(t, true)
	engine := &Engine{checkpoint: func(name string) error {
		if name == "committed" {
			return errors.New("injected_archive")
		}
		return nil
	}}
	if result, err := engine.Install(f.request()); err == nil || !result.Committed {
		t.Fatalf("%+v %v", result, err)
	}
	mustWrite(t, filepath.Join(f.live, "Arkism.exe"), "replaced outside the installer")
	source := filepath.Join(f.root, "source-next")
	writePayload(t, source, "next")
	expected, err := Inventory(source)
	if err != nil {
		t.Fatal(err)
	}
	return f, Request{f.live, source, expected, f.metadata, f.metadata.stop}
}
func TestInstallSupersedesStaleCommittedJournalWithDriftedPayload(t *testing.T) {
	f, next := staleCommittedFixture(t)
	result, err := new(Engine).Install(next)
	if err != nil || !result.Committed || result.Archive == "" {
		t.Fatalf("%+v %v", result, err)
	}
	if err = verify(f.live, next.Expected); err != nil {
		t.Fatal(err)
	}
	versions, err := filepath.Glob(filepath.Join(f.live+".versions", "version-*"))
	if err != nil || len(versions) != 2 {
		t.Fatalf("stale checkpoint not retained beside the new one: %v %v", versions, err)
	}
	// Strict repair still refuses to vouch for a drifted committed payload.
	f2, _ := staleCommittedFixture(t)
	if result, err = new(Engine).Recover(f2.live, f2.metadata, f2.metadata.stop); err == nil || !strings.Contains(err.Error(), "install_payload_mismatch") {
		t.Fatalf("%+v %v", result, err)
	}
}
func TestInstallPriorRecoveryFailureIsNotReportedAsCommitted(t *testing.T) {
	f, next := staleCommittedFixture(t)
	mustWrite(t, filepath.Join(f.live+".versions", "pending", "foreign"), "blocks archive")
	result, err := new(Engine).Install(next)
	if err == nil || result.Committed || !strings.Contains(err.Error(), "install_prior_recovery_failed") {
		t.Fatalf("%+v %v", result, err)
	}
	if mustRead(t, filepath.Join(f.live, "Arkism.exe")) != "replaced outside the installer" {
		t.Fatal("changed live payload after refusing the install")
	}
}
func TestInstallRefusesTamperingWithoutChangingOldPayload(t *testing.T) {
	for _, mode := range []string{"bad_hash", "extra_source", "source_overlap", "foreign_versions", "corrupt_journal", "foreign_pending", "busy"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, true)
			request := f.request()
			var unlock func()
			switch mode {
			case "bad_hash":
				request.Expected.Files[0].SHA256 = strings.Repeat("0", 64)
			case "extra_source":
				mustWrite(t, filepath.Join(f.source, "foreign.txt"), "extra")
			case "source_overlap":
				request.Source = f.live
			case "foreign_versions":
				if err := os.Mkdir(f.live+".versions", 0700); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, filepath.Join(f.live+".versions", "foreign.txt"), "keep")
			default:
				l, err := paths(f.live)
				if err != nil {
					t.Fatal(err)
				}
				unlock, err = openLayout(l)
				if err != nil {
					t.Fatal(err)
				}
				if mode != "busy" {
					unlock()
					unlock = nil
					if err = os.Mkdir(l.pending, 0700); err != nil {
						t.Fatal(err)
					}
					if mode == "corrupt_journal" {
						mustWrite(t, filepath.Join(l.pending, "journal.json"), "{bad")
					} else {
						mustWrite(t, filepath.Join(l.pending, "foreign.txt"), "keep")
					}
				}
			}
			if unlock != nil {
				defer unlock()
			}
			if _, err := new(Engine).Install(request); err == nil {
				t.Fatal("accepted tampered request")
			}
			assertOld(t, f, true)
		})
	}
}
func TestInstallManifestRejectsAliasesAndMissingParents(t *testing.T) {
	for _, name := range []string{"../escape", "C:/escape", "a\\b", "bad:stream", "CON.txt", "trail. ", "missing/parent.exe"} {
		t.Run(name, func(t *testing.T) {
			m := Manifest{Files: []File{{name, 1, strings.Repeat("0", 64)}}}
			if err := m.Validate(); err == nil {
				t.Fatal("accepted invalid path")
			}
		})
	}
	m := Manifest{Files: []File{{"Arkism.exe", 1, strings.Repeat("0", 64)}, {"arkism.exe", 1, strings.Repeat("0", 64)}}}
	if err := m.Validate(); err == nil {
		t.Fatal("accepted case alias")
	}
}
func TestInstallRejectsSourceAndParentLinks(t *testing.T) {
	for _, where := range []string{"source_file", "source_dir", "live_parent"} {
		t.Run(where, func(t *testing.T) {
			f := newFixture(t, true)
			target := filepath.Join(f.root, "unrelated")
			writePayload(t, target, "unrelated")
			before, err := Inventory(target)
			if err != nil {
				t.Fatal(err)
			}
			request := f.request()
			switch where {
			case "source_file":
				p := filepath.Join(f.source, "Arkism.exe")
				if err = os.Remove(p); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(filepath.Join(target, "Arkism.exe"), p)
			case "source_dir":
				err = os.Symlink(target, filepath.Join(f.source, "linked"))
			case "live_parent":
				link := filepath.Join(f.root, "linked-parent")
				err = os.Symlink(target, link)
				request.Live = filepath.Join(link, "installed Arkism")
			}
			if err != nil {
				t.Fatal("native link setup required:", err)
			}
			if _, err = new(Engine).Install(request); err == nil {
				t.Fatal("followed a link")
			}
			after, err := Inventory(target)
			if err != nil {
				t.Fatal(err)
			}
			a, _ := json.Marshal(before)
			b, _ := json.Marshal(after)
			if !bytes.Equal(a, b) {
				t.Fatal("link target changed")
			}
			assertOld(t, f, true)
		})
	}
}

// The child exits without running defers. This proves journal recovery across a
// real process interruption at the rename/metadata boundaries, not power loss.
func TestInstallInterruptedChild(t *testing.T) {
	root := os.Getenv("ARKISM_TXN_TEST_CHILD_ROOT")
	if root == "" {
		return
	}
	if mustRead(t, filepath.Join(root, "TEST-OWNER")) != "arkism-installtxn-test" {
		t.Fatal("unowned child root")
	}
	live := filepath.Join(root, "installed Arkism")
	source := filepath.Join(root, "source")
	manifest, err := Inventory(source)
	if err != nil {
		t.Fatal(err)
	}
	metadata := &diskMetadata{path: filepath.Join(root, "metadata.fixture")}
	point := os.Getenv("ARKISM_TXN_TEST_CHILD_POINT")
	engine := &Engine{checkpoint: func(name string) error {
		if name == point {
			os.Exit(77)
		}
		return nil
	}}
	_, err = engine.Install(Request{live, source, manifest, metadata, metadata.stop})
	t.Fatalf("interruption point not reached: %v", err)
}
func TestInstallRecoversAfterActualProcessExit(t *testing.T) {
	for _, old := range []bool{false, true} {
		for _, point := range []string{"staged", "prepared", "old_moved", "new_moved", "metadata_applied", "committed"} {
			t.Run(fmt.Sprintf("old=%v/%s", old, point), func(t *testing.T) {
				f := newFixture(t, old)
				mustWrite(t, filepath.Join(f.root, "TEST-OWNER"), "arkism-installtxn-test")
				command := exec.Command(os.Args[0], "-test.run=^TestInstallInterruptedChild$", "-test.v")
				command.Env = append(os.Environ(), "ARKISM_TXN_TEST_CHILD_ROOT="+f.root, "ARKISM_TXN_TEST_CHILD_POINT="+point)
				output, err := command.CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 77 {
					t.Fatalf("child did not interrupt: %v %s", err, output)
				}
				result, err := new(Engine).Recover(f.live, f.metadata, f.metadata.stop)
				if err != nil {
					t.Fatal(err)
				}
				if point == "committed" {
					if !result.Committed {
						t.Fatal("lost durable commit")
					}
					if err = verify(f.live, f.expected); err != nil {
						t.Fatal(err)
					}
					if err = f.metadata.Verify(); err != nil {
						t.Fatal(err)
					}
				} else {
					if !result.Recovered || result.Committed {
						t.Fatalf("%+v", result)
					}
					assertOld(t, f, old)
				}
			})
		}
	}

}

func TestInstallRecoveryRejectsForeignCheckpointBeforeStoppingProcesses(t *testing.T) {
	for _, field := range []string{"SID", "Live", "ID", "Phase", "previous_bytes", "new_bytes"} {
		t.Run(field, func(t *testing.T) {
			f := newFixture(t, true)
			mustWrite(t, filepath.Join(f.root, "TEST-OWNER"), "arkism-installtxn-test")
			command := exec.Command(os.Args[0], "-test.run=^TestInstallInterruptedChild$")
			command.Env = append(os.Environ(), "ARKISM_TXN_TEST_CHILD_ROOT="+f.root, "ARKISM_TXN_TEST_CHILD_POINT=new_moved")
			output, err := command.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 77 {
				t.Fatalf("child interruption failed: %v %s", err, output)
			}
			l, err := paths(f.live)
			if err != nil {
				t.Fatal(err)
			}
			var j journal
			if err = readJSON(filepath.Join(l.pending, "journal.json"), &j); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "SID":
				j.SID = "S-1-0-0"
			case "Live":
				j.Live = filepath.Join(f.root, "unrelated")
			case "ID":
				j.ID = "../../escape"
			case "Phase":
				j.Phase = "unknown"
			case "previous_bytes":
				mustWrite(t, filepath.Join(l.previous, "Arkism.exe"), "foreign changed previous")
			case "new_bytes":
				mustWrite(t, filepath.Join(l.live, "Arkism.exe"), "foreign changed current")
			}
			if field != "previous_bytes" && field != "new_bytes" {
				data, _ := json.Marshal(j)
				if err = os.WriteFile(filepath.Join(l.pending, "journal.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			current := mustRead(t, filepath.Join(l.live, "Arkism.exe"))
			previous := mustRead(t, filepath.Join(l.previous, "Arkism.exe"))
			if _, err = new(Engine).Recover(f.live, f.metadata, f.metadata.stop); err == nil {
				t.Fatal("accepted foreign recovery state")
			}
			if f.metadata.stopCalls != 0 || mustRead(t, filepath.Join(l.live, "Arkism.exe")) != current || mustRead(t, filepath.Join(l.previous, "Arkism.exe")) != previous {
				t.Fatal("changed or stopped unproven payload")
			}
		})
	}
}
