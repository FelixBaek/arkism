package installtxn

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type metadataFixture struct {
	fixture
	scope          metadataScope
	metadata       *nativeMetadata
	before         registrySnapshot
	links          [2]linkSnapshot
	keyRoot, owner string
}

func newMetadataFixture(t *testing.T, old bool) metadataFixture {
	t.Helper()
	f := newFixture(t, old)
	for _, root := range []string{f.source, f.live} {
		if root == f.live && !old {
			continue
		}
		if err := os.MkdirAll(filepath.Join(root, "integration"), 0700); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(root, "integration", "arkism.exe"), "synthetic agent client")
		mustWrite(t, filepath.Join(root, "runtime", "memory-principal.exe"), "synthetic token utility")
		mustWrite(t, filepath.Join(root, "uninstall.exe"), "synthetic uninstaller; never executed")
	}
	var err error
	f.expected, err = Inventory(f.source)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		t.Fatal(err)
	}
	owner := hex.EncodeToString(id)
	keyRoot := `Software\ArkismInstallerChecks\metadata-` + owner
	key, opened, err := registry.CreateKey(registry.CURRENT_USER, keyRoot, registry.ALL_ACCESS|registry.WOW64_32KEY)
	if err != nil || opened {
		t.Fatalf("fresh registry namespace required: %v", err)
	}
	if err = key.SetStringValue("TEST-OWNER", owner); err != nil {
		t.Fatal(err)
	}
	key.Close()
	t.Cleanup(func() {
		fmt.Println("Only owned metadata registry cleanup: HKCU\\" + keyRoot)
		key, err := registry.OpenKey(registry.CURRENT_USER, keyRoot, registry.READ|registry.WOW64_32KEY)
		if err != nil {
			t.Error(err)
			return
		}
		marker, _, err := key.GetStringValue("TEST-OWNER")
		key.Close()
		if err != nil || marker != owner {
			t.Error("metadata registry ownership changed")
			return
		}
		if err = removeMetadataTestKey(keyRoot); err != nil {
			t.Error(err)
		}
	})
	l, err := paths(f.live)
	if err != nil {
		t.Fatal(err)
	}
	scope := metadataScope{Live: f.live, SID: l.owner, Registration: keyRoot + `\Uninstall`}
	for i, name := range []string{"Programs", "Desktop"} {
		dir := filepath.Join(f.root, name)
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		scope.Links[i] = filepath.Join(dir, "Arkism.lnk")
	}
	if old {
		key, _, err = registry.CreateKey(registry.CURRENT_USER, scope.Registration, registry.ALL_ACCESS|registry.WOW64_32KEY)
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range expectedRegistration(f.live, "0.1.0") {
			if err = setRawValue(key, name, value); err != nil {
				t.Fatal(err)
			}
		}
		if err = key.SetBinaryValue("FixtureExtra", []byte{1, 2, 3, 4}); err != nil {
			t.Fatal(err)
		}
		key.Close()
		for _, path := range scope.Links {
			data, err := newShortcut(filepath.Join(f.live, "Arkism.exe"))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	metadata, err := scopedMetadata(scope, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	before, err := readRegistration(scope.Registration)
	if err != nil {
		t.Fatal(err)
	}
	links, err := readOldLinks(scope)
	if err != nil {
		t.Fatal(err)
	}
	return metadataFixture{f, scope, metadata, before, links, keyRoot, owner}
}
func removeMetadataTestKey(path string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.READ|registry.WOW64_32KEY)
	if err != nil {
		return err
	}
	names, err := key.ReadSubKeyNames(-1)
	key.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		if err = removeMetadataTestKey(path + `\` + name); err != nil {
			return err
		}
	}
	return registry.DeleteKey(registry.CURRENT_USER, path)
}
func (f metadataFixture) assertMetadataOld(t *testing.T) {
	t.Helper()
	current, err := readRegistration(f.scope.Registration)
	if err != nil {
		t.Fatal(err)
	}
	if !sameRegistry(current, f.before) {
		t.Fatalf("registry not restored: %+v", current)
	}
	for i, path := range f.scope.Links {
		file, err := openLink(path)
		if err != nil {
			t.Fatal(err)
		}
		match := matchesLink(file, f.links[i])
		file.close()
		if !match {
			t.Fatal("shortcut bytes not restored")
		}
	}
}
func TestInstallNativeMetadataApplyRestoreAndCommittedCleanup(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprint(old), func(t *testing.T) {
			f := newMetadataFixture(t, old)
			saved, err := f.metadata.Capture()
			if err != nil {
				t.Fatal(err)
			}
			if err = f.metadata.Apply(); err != nil {
				t.Fatal(err)
			}
			if err = f.metadata.Verify(); err != nil {
				t.Fatal(err)
			}
			for _, path := range f.scope.Links {
				pointer, _ := windows.UTF16PtrFromString(path)
				attributes, err := windows.GetFileAttributes(pointer)
				if err != nil || attributes&windows.FILE_ATTRIBUTE_HIDDEN != 0 {
					t.Fatal("published shortcut is hidden", err)
				}
			}
			receiver, err := scopedMetadata(f.scope, "0.3.0")
			if err != nil {
				t.Fatal(err)
			}
			if err = receiver.Restore(saved); err != nil {
				t.Fatal(err)
			}
			f.assertMetadataOld(t)
			if err = receiver.Restore(saved); err != nil {
				t.Fatal("not idempotent:", err)
			}
			saved, err = f.metadata.Capture()
			if err != nil {
				t.Fatal(err)
			}
			if err = f.metadata.Apply(); err != nil {
				t.Fatal(err)
			}
			if err = f.metadata.Verify(); err != nil {
				t.Fatal(err)
			}
			for _, path := range f.scope.Links {
				pointer, _ := windows.UTF16PtrFromString(path)
				attributes, err := windows.GetFileAttributes(pointer)
				if err != nil || attributes&windows.FILE_ATTRIBUTE_HIDDEN != 0 {
					t.Fatal("published shortcut is hidden", err)
				}
			}
			if err = f.metadata.Commit(saved); err != nil {
				t.Fatal(err)
			}
			if err = f.metadata.Commit(saved); err != nil {
				t.Fatal("commit not idempotent:", err)
			}
			for i, path := range f.scope.Links {
				backup, pending := linkTemporaryPaths(path, f.metadata.saved.ID, i)
				for _, p := range []string{backup, pending} {
					if _, err = os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("temporary link remains")
					}
				}
			}
		})
	}
}
func TestInstallNativeMetadataTransactionWithPayload(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprint(old), func(t *testing.T) {
			f := newMetadataFixture(t, old)
			request := f.request()
			request.Metadata = f.metadata
			result, err := new(Engine).Install(request)
			if err != nil || !result.Committed {
				t.Fatalf("%+v %v", result, err)
			}
			if err = verify(f.live, f.expected); err != nil {
				t.Fatal(err)
			}
			current, err := readRegistration(f.scope.Registration)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(current.Values["DisplayVersion"], registryString("0.2.0")) {
				t.Fatal("registration version not published")
			}
		})
	}
}
func TestInstallNativeMetadataKTMAbortKeepsAllRegistryValues(t *testing.T) {
	f := newMetadataFixture(t, true)
	saved, err := f.metadata.Capture()
	if err != nil {
		t.Fatal(err)
	}
	f.metadata.checkpoint = func(point string) error {
		if point == "registry_value_DisplayVersion" {
			return errors.New("injected before registry commit")
		}
		return nil
	}
	if err = f.metadata.Apply(); err == nil {
		t.Fatal("missing KTM failure")
	}
	current, err := readRegistration(f.scope.Registration)
	if err != nil {
		t.Fatal(err)
	}
	if !sameRegistry(current, f.before) {
		t.Fatal("partial registry transaction became visible")
	}
	f.metadata.checkpoint = nil
	if err = f.metadata.Restore(saved); err != nil {
		t.Fatal(err)
	}
	f.assertMetadataOld(t)
}
func TestInstallNativeMetadataPreservesForeignChanges(t *testing.T) {
	for _, mode := range []string{"link", "registry", "commit_current"} {
		t.Run(mode, func(t *testing.T) {
			f := newMetadataFixture(t, true)
			saved, err := f.metadata.Capture()
			if err != nil {
				t.Fatal(err)
			}
			if err = f.metadata.Apply(); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "link", "commit_current":
				mustWrite(t, f.scope.Links[0], "foreign current shortcut bytes")
			case "registry":
				key, err := registry.OpenKey(registry.CURRENT_USER, f.scope.Registration, registry.ALL_ACCESS|registry.WOW64_32KEY)
				if err != nil {
					t.Fatal(err)
				}
				if err = key.SetStringValue("UserChange", "preserve"); err != nil {
					t.Fatal(err)
				}
				key.Close()
			}
			if mode == "commit_current" {
				if err = f.metadata.Commit(saved); err != nil {
					t.Fatal(err)
				}
				if mustRead(t, f.scope.Links[0]) != "foreign current shortcut bytes" {
					t.Fatal("commit changed user link")
				}
				return
			}
			if err = f.metadata.Restore(saved); err == nil {
				t.Fatal("overwrote foreign change")
			}
			if mode == "link" && mustRead(t, f.scope.Links[0]) != "foreign current shortcut bytes" {
				t.Fatal("foreign link lost")
			}
			if mode == "registry" {
				key, _ := registry.OpenKey(registry.CURRENT_USER, f.scope.Registration, registry.READ|registry.WOW64_32KEY)
				value, _, err := key.GetStringValue("UserChange")
				key.Close()
				if err != nil || value != "preserve" {
					t.Fatal("foreign registry value lost")
				}
			}
		})
	}
}
func TestInstallNativeMetadataRejectsUnownedOriginalResources(t *testing.T) {
	for _, mode := range []string{"link", "registration", "unregistered_payload", "registration_child"} {
		t.Run(mode, func(t *testing.T) {
			f := newMetadataFixture(t, true)
			switch mode {
			case "link":
				mustWrite(t, f.scope.Links[0], "foreign file called Arkism.lnk")
			case "registration":
				key, _ := registry.OpenKey(registry.CURRENT_USER, f.scope.Registration, registry.ALL_ACCESS|registry.WOW64_32KEY)
				key.SetStringValue("InstallLocation", filepath.Join(f.root, "foreign app"))
				key.Close()
			case "registration_child":
				key, _, err := registry.CreateKey(registry.CURRENT_USER, f.scope.Registration+`\foreign`, registry.ALL_ACCESS|registry.WOW64_32KEY)
				if err != nil {
					t.Fatal(err)
				}
				key.Close()
			case "unregistered_payload":
				if err := registry.DeleteKey(registry.CURRENT_USER, f.scope.Registration); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.metadata.Capture(); err == nil {
				t.Fatal("adopted foreign original resource")
			}
		})
	}
}
func TestInstallNativeMetadataRejectsCheckpointRedirection(t *testing.T) {
	for _, mode := range []string{"registry_path", "link_path", "version", "id", "new_link", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			f := newMetadataFixture(t, true)
			saved, err := f.metadata.Capture()
			if err != nil {
				t.Fatal(err)
			}
			var snapshot metadataCheckpoint
			if err = json.Unmarshal(saved, &snapshot); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "registry_path":
				snapshot.Scope.Registration = `Software\unrelated`
			case "link_path":
				snapshot.Scope.Links[0] = filepath.Join(f.root, "foreign.lnk")
			case "version":
				snapshot.Version = "version with command text"
			case "id":
				snapshot.ID = "../escape"
			case "new_link":
				snapshot.NewLink, err = newShortcut(filepath.Join(f.root, "Other.exe"))
				if err != nil {
					t.Fatal(err)
				}
			}
			saved, err = json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "trailing" {
				saved = append(saved, []byte(" {}")...)
			}
			if err = f.metadata.Restore(saved); err == nil {
				t.Fatal("accepted redirected checkpoint")
			}
			f.assertMetadataOld(t)
		})
	}
}
func TestInstallNativeMetadataNoReplacePreservesConcurrentCreation(t *testing.T) {
	f := newMetadataFixture(t, true)
	saved, err := f.metadata.Capture()
	if err != nil {
		t.Fatal(err)
	}
	f.metadata.checkpoint = func(point string) error {
		if point == "link_0_old_moved" {
			mustWrite(t, f.scope.Links[0], "concurrent foreign creation")
		}
		return nil
	}
	if err = f.metadata.Apply(); err == nil {
		t.Fatal("replaced a concurrent file")
	}
	if mustRead(t, f.scope.Links[0]) != "concurrent foreign creation" {
		t.Fatal("foreign creation overwritten")
	}
	if err = f.metadata.Restore(saved); err == nil {
		t.Fatal("overwrote concurrent file on restore")
	}
}
func TestInstallNativeMetadataInterruptedChild(t *testing.T) {
	root := os.Getenv("ARKISM_META_TEST_CHILD_ROOT")
	if root == "" {
		return
	}
	if mustRead(t, filepath.Join(root, "TEST-OWNER")) != "arkism-native-metadata-child" {
		t.Fatal("unowned child root")
	}
	var scope metadataScope
	data, err := os.ReadFile(filepath.Join(root, "scope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scope); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(scope.Registration, `Software\ArkismInstallerChecks\metadata-`) {
		t.Fatal("unowned registry fixture")
	}
	metadata, err := scopedMetadata(scope, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	point := os.Getenv("ARKISM_META_TEST_CHILD_POINT")
	metadata.checkpoint = func(name string) error {
		if name == point {
			os.Exit(77)
		}
		return nil
	}
	engine := &Engine{checkpoint: func(name string) error {
		if point == "engine_committed" && name == "committed" {
			os.Exit(77)
		}
		return nil
	}}
	source := filepath.Join(root, "source")
	expected, err := Inventory(source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Install(Request{scope.Live, source, expected, metadata, func(string) error { return nil }})
	t.Fatalf("interruption not reached: %v", err)
}
func TestInstallNativeMetadataActualProcessExitRecovery(t *testing.T) {
	for _, old := range []bool{false, true} {
		for _, point := range []string{"registry_value_DisplayVersion", "registration_applied", "link_0_old_moved", "link_0_published", "link_1_old_moved", "link_1_published", "engine_committed"} {
			t.Run(fmt.Sprintf("old=%v/%s", old, point), func(t *testing.T) {
				f := newMetadataFixture(t, old)
				mustWrite(t, filepath.Join(f.root, "TEST-OWNER"), "arkism-native-metadata-child")
				data, _ := json.Marshal(f.scope)
				if err := os.WriteFile(filepath.Join(f.root, "scope.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
				command := exec.Command(os.Args[0], "-test.run=^TestInstallNativeMetadataInterruptedChild$")
				command.Env = append(os.Environ(), "ARKISM_META_TEST_CHILD_ROOT="+f.root, "ARKISM_META_TEST_CHILD_POINT="+point)
				output, err := command.CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 77 {
					t.Fatalf("child did not exit at checkpoint: %v %s", err, output)
				}
				receiver, err := scopedMetadata(f.scope, "0.3.0")
				if err != nil {
					t.Fatal(err)
				}
				result, err := new(Engine).Recover(f.live, receiver, func(string) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				if point == "engine_committed" {
					if !result.Committed {
						t.Fatal("lost committed installation")
					}
					if err = verify(f.live, f.expected); err != nil {
						t.Fatal(err)
					}
				} else {
					f.assertMetadataOld(t)
					if old {
						if mustRead(t, filepath.Join(f.live, "Arkism.exe")) != "old app" {
							t.Fatal("old payload not restored")
						}
					} else if _, err = os.Lstat(f.live); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("uncommitted fresh payload remains")
					}
				}
			})
		}
	}
}

func TestInstallNativeMetadataEmptyDestinationAndRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			f := newMetadataFixture(t, false)
			if err := os.Mkdir(f.live, 0700); err != nil {
				t.Fatal(err)
			}
			engine := &Engine{checkpoint: func(point string) error {
				if fail && point == "metadata_applied" {
					return errors.New("injected empty directory rollback")
				}
				return nil
			}}
			request := f.request()
			request.Metadata = f.metadata
			result, err := engine.Install(request)
			if fail {
				if err == nil || result.Committed {
					t.Fatalf("%+v %v", result, err)
				}
				entries, err := os.ReadDir(f.live)
				if err != nil || len(entries) != 0 {
					t.Fatal("original empty directory not restored", err)
				}
				f.assertMetadataOld(t)
			} else {
				if err != nil || !result.Committed {
					t.Fatalf("%+v %v", result, err)
				}
				if err = verify(f.live, f.expected); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestInstallNativeMetadataPartialTemporaryFileCleanup(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprint(foreign), func(t *testing.T) {
			f := newMetadataFixture(t, true)
			saved, err := f.metadata.Capture()
			if err != nil {
				t.Fatal(err)
			}
			_, pending := linkTemporaryPaths(f.scope.Links[0], f.metadata.saved.ID, 0)
			data := f.metadata.saved.NewLink[:len(f.metadata.saved.NewLink)/2]
			if foreign {
				data = []byte("unrelated bytes at reserved name")
			}
			if err = os.WriteFile(pending, data, 0600); err != nil {
				t.Fatal(err)
			}
			err = f.metadata.Restore(saved)
			if foreign {
				if err == nil {
					t.Fatal("removed foreign temporary file")
				}
				if mustRead(t, pending) != string(data) {
					t.Fatal("foreign temporary bytes changed")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = os.Lstat(pending); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("partial owned temporary file remains")
				}
			}
			f.assertMetadataOld(t)
		})
	}
}
func TestInstallNativeMetadataCommittedCleanupFailureNeverRollsBack(t *testing.T) {
	f := newMetadataFixture(t, true)
	var backup string
	engine := &Engine{checkpoint: func(point string) error {
		if point == "committed" {
			backup, _ = linkTemporaryPaths(f.scope.Links[0], f.metadata.saved.ID, 0)
			mustWrite(t, backup, "foreign changed backup")
		}
		return nil
	}}
	request := f.request()
	request.Metadata = f.metadata
	result, err := engine.Install(request)
	if err == nil || !result.Committed {
		t.Fatalf("%+v %v", result, err)
	}
	if err = verify(f.live, f.expected); err != nil {
		t.Fatal(err)
	}
	if mustRead(t, backup) != "foreign changed backup" {
		t.Fatal("committed cleanup deleted foreign backup")
	}
	receiver, _ := scopedMetadata(f.scope, "0.3.0")
	result, err = new(Engine).Recover(f.live, receiver, func(string) error { return nil })
	if err == nil || !result.Committed {
		t.Fatalf("lost durable commit: %+v %v", result, err)
	}
	if err = os.WriteFile(backup, f.links[0].Data, 0600); err != nil {
		t.Fatal(err)
	}
	result, err = new(Engine).Recover(f.live, receiver, func(string) error { return nil })
	if err != nil || !result.Committed {
		t.Fatalf("cleanup repair did not resume: %+v %v", result, err)
	}
	if err = verify(f.live, f.expected); err != nil {
		t.Fatal(err)
	}
}

func TestInstallNativeMetadataRejectsMalformedSnapshotResources(t *testing.T) {
	for _, mode := range []string{"format", "id_hex", "nil_registry", "false_registry", "registry_name", "registry_value_size", "absent_link_bytes", "original_link", "extra_json_field"} {
		t.Run(mode, func(t *testing.T) {
			f := newMetadataFixture(t, true)
			saved, err := f.metadata.Capture()
			if err != nil {
				t.Fatal(err)
			}
			var snapshot metadataCheckpoint
			if err = json.Unmarshal(saved, &snapshot); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "format":
				snapshot.Format = 2
			case "id_hex":
				snapshot.ID = strings.Repeat("z", 32)
			case "nil_registry":
				snapshot.Registry.Values = nil
			case "false_registry":
				snapshot.Registry.Exists = false
			case "registry_name":
				snapshot.Registry.Values["DISPLAYNAME"] = snapshot.Registry.Values["DisplayName"]
			case "registry_value_size":
				snapshot.Registry.Values["oversized"] = registryValue{registry.BINARY, make([]byte, (64<<10)+1)}
			case "absent_link_bytes":
				snapshot.Links[0].Exists = false
			case "original_link":
				snapshot.Links[0].Data = []byte("foreign original bytes")
			}
			saved, err = json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "extra_json_field" {
				saved = append(saved[:len(saved)-1], []byte(`,"unexpected":true}`)...)
			}
			if err = f.metadata.Restore(saved); err == nil {
				t.Fatal("accepted malformed checkpoint resource")
			}
			f.assertMetadataOld(t)
		})
	}
}
func TestInstallNativeMetadataResolvesConfiguredFolderWithoutFollowingLaterRedirects(t *testing.T) {
	f := newMetadataFixture(t, false)
	directory := filepath.Dir(f.scope.Links[0])
	alias := filepath.Join(f.root, "configured-folder")
	if err := os.Symlink(directory, alias); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveKnownDirectory(alias)
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(resolved, directory) {
		t.Fatal("configured known folder not resolved")
	}
	release, err := pinMetadataDirectory(resolved)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if release, err = pinMetadataDirectory(alias); err == nil {
		release()
		t.Fatal("accepted a redirected mutation path")
	}
}

func TestShortcutFolderCheckIsReadOnlyAndRejectsOtherFolders(t *testing.T) {
	actual, err := shortcutFolders()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckShortcutFolders(actual[0], actual[1]); err != nil {
		t.Fatal(err)
	}
	owned := t.TempDir()
	if err := CheckShortcutFolders(owned, owned); err == nil {
		t.Fatal("accepted unrelated isolated profile")
	}
	entries, err := os.ReadDir(owned)
	if err != nil || len(entries) != 0 {
		t.Fatal("folder preflight wrote files")
	}
	if err := CheckShortcutFolders("relative", actual[1]); err == nil {
		t.Fatal("accepted relative expected folder")
	}
}
