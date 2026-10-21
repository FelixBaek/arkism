package installtxn

import (
	"errors"
	"golang.org/x/sys/windows/registry"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func installedRemovalFixture(t *testing.T) metadataFixture {
	t.Helper()
	f := newMetadataFixture(t, false)
	request := f.request()
	request.Metadata = f.metadata
	result, err := new(Engine).Install(request)
	if err != nil || !result.Committed {
		t.Fatalf("%+v %v", result, err)
	}
	return f
}
func TestInstallReceiptRemovalPreservesUnknownFilesAndIsRepeatable(t *testing.T) {
	f := installedRemovalFixture(t)
	mustWrite(t, filepath.Join(f.live, "user-file.txt"), "preserve user file")
	mustWrite(t, filepath.Join(f.live, "runtime", "user-log.txt"), "preserve user log")
	l, err := paths(f.live)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := installedReceipt(l, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if err = removePayloadFiles(f.live, manifest); err != nil {
		t.Fatal(err)
	}
	if err = removePayloadFiles(f.live, manifest); err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest.Files {
		if _, err = os.Lstat(filepath.Join(f.live, filepath.FromSlash(entry.Path))); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("owned program remains")
		}
	}
	if mustRead(t, filepath.Join(f.live, "user-file.txt")) != "preserve user file" || mustRead(t, filepath.Join(f.live, "runtime", "user-log.txt")) != "preserve user log" {
		t.Fatal("unknown data removed")
	}
	if err = f.metadata.removeInstalled(); err != nil {
		t.Fatal(err)
	}
	current, err := readRegistration(f.scope.Registration)
	if err != nil || current.Exists {
		t.Fatal("owned registration remains", err)
	}
}
func TestInstallRemovalPreflightPreservesWholePayloadOnModificationOrLink(t *testing.T) {
	for _, mode := range []string{"changed", "file_link", "directory_link"} {
		t.Run(mode, func(t *testing.T) {
			f := installedRemovalFixture(t)
			target := filepath.Join(f.root, "foreign")
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(target, "foreign.txt"), "preserve outside")
			switch mode {
			case "changed":
				mustWrite(t, filepath.Join(f.live, "runtime", "memoryd.exe"), "user modified bytes")
			case "file_link":
				path := filepath.Join(f.live, "Arkism.exe")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(target, "foreign.txt"), path); err != nil {
					t.Fatal(err)
				}
			case "directory_link":
				path := filepath.Join(f.live, "runtime", "empty")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if err := removePayloadFiles(f.live, f.expected); err == nil {
				t.Fatal("removed unproven payload")
			}
			if mustRead(t, filepath.Join(f.live, "integration", "arkism.exe")) != "synthetic agent client" {
				t.Fatal("deleted before complete preflight")
			}
			if mustRead(t, filepath.Join(target, "foreign.txt")) != "preserve outside" {
				t.Fatal("link target changed")
			}
		})
	}
}
func TestInstallRemovalRejectsWrongVersionAndPreservesForeignMetadata(t *testing.T) {
	f := installedRemovalFixture(t)
	current, err := readRegistration(f.scope.Registration)
	if err != nil {
		t.Fatal(err)
	}
	if err = installedClaim(current, f.live, "0.3.0"); err == nil {
		t.Fatal("old uninstaller can remove a new version")
	}
	mustWrite(t, f.scope.Links[0], "foreign user shortcut")
	key, err := registry.OpenKey(registry.CURRENT_USER, f.scope.Registration, registry.ALL_ACCESS|registry.WOW64_32KEY)
	if err != nil {
		t.Fatal(err)
	}
	if err = key.SetStringValue("UserNote", "preserve"); err != nil {
		t.Fatal(err)
	}
	key.Close()
	if err = f.metadata.removeInstalled(); err != nil {
		t.Fatal(err)
	}
	if mustRead(t, f.scope.Links[0]) != "foreign user shortcut" {
		t.Fatal("removed foreign shortcut")
	}
	left, err := readRegistration(f.scope.Registration)
	if err != nil {
		t.Fatal(err)
	}
	if len(left.Values) != 1 || !reflect.DeepEqual(left.Values["usernote"], registryString("preserve")) {
		t.Fatal("unknown registry data lost")
	}
}
func TestInstallPartialRegisteredPayloadCanBeRepaired(t *testing.T) {
	f := newMetadataFixture(t, true)
	if err := os.Remove(filepath.Join(f.live, "integration", "arkism.exe")); err != nil {
		t.Fatal(err)
	}
	request := f.request()
	request.Metadata = f.metadata
	result, err := new(Engine).Install(request)
	if err != nil || !result.Committed {
		t.Fatalf("%+v %v", result, err)
	}
	if err = verify(f.live, f.expected); err != nil {
		t.Fatal(err)
	}
}
