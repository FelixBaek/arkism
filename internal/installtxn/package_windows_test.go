package installtxn

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func packageFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	t.Log("Only package fixture root: " + root)
	source := filepath.Join(root, "payload")
	for _, name := range requiredPayload {
		path := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, path, "inert signed-payload fixture "+name)
	}
	return source, filepath.Join(root, "payload.json")
}
func TestInstallerPackageInventoryBindsEveryByteAndUninstaller(t *testing.T) {
	source, out := packageFixture(t)
	if err := WritePackage(source, "0.2.0", out); err != nil {
		t.Fatal(err)
	}
	p, err := ReadPackage(out)
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyPackage(source, p); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(source, "uninstall.exe"), "changed emitted uninstaller")
	if err = VerifyPackage(source, p); err == nil {
		t.Fatal("accepted changed uninstaller")
	}
	if err = WritePackage(source, "0.2.0", out); err == nil {
		t.Fatal("overwrote manifest")
	}
}
func TestInstallerPackageRejectsMissingRoleExtraFileAndBadVersion(t *testing.T) {
	for _, mode := range []string{"missing", "extra", "version", "inside", "relative", "manifest_unknown"} {
		t.Run(mode, func(t *testing.T) {
			source, out := packageFixture(t)
			if mode == "missing" {
				if err := os.Remove(filepath.Join(source, "uninstall.exe")); err != nil {
					t.Fatal(err)
				}
			}
			version := "0.2.0"
			if mode == "version" {
				version = "0.2.0 command"
			}
			if mode == "inside" {
				out = filepath.Join(source, "payload.json")
			}
			if mode == "relative" {
				source = "relative"
			}
			err := WritePackage(source, version, out)
			if mode == "extra" || mode == "manifest_unknown" {
				if err != nil {
					t.Fatal(err)
				}
				if mode == "extra" {
					mustWrite(t, filepath.Join(source, "unknown.txt"), "unknown")
					p, err := ReadPackage(out)
					if err != nil {
						t.Fatal(err)
					}
					if err = VerifyPackage(source, p); err == nil {
						t.Fatal("accepted extra payload")
					}
				} else {
					data, err := os.ReadFile(out)
					if err != nil {
						t.Fatal(err)
					}
					data = append(data[:len(data)-1], []byte(`,"unknown":true}`)...)
					if err = os.WriteFile(out, data, 0600); err != nil {
						t.Fatal(err)
					}
					if _, err = ReadPackage(out); err == nil {
						t.Fatal("accepted unknown schema field")
					}
				}
				return
			}
			if err == nil {
				t.Fatal("accepted unsafe package")
			}
		})
	}
}
func TestInstallerPackageBadSourceDoesNotReachLiveMetadata(t *testing.T) {
	source, out := packageFixture(t)
	if err := WritePackage(source, "0.2.0", out); err != nil {
		t.Fatal(err)
	}
	p, err := ReadPackage(out)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(source, "Arkism.exe"), "tamper")
	live := filepath.Join(filepath.Dir(source), "missing live")
	if _, err = InstallPackage(live, source, p); err == nil {
		t.Fatal("accepted tampered source")
	}
	if _, err = os.Lstat(live + ".versions"); !os.IsNotExist(err) {
		t.Fatal("touched live transaction metadata")
	}
}
func TestInstallerPackageAllowsRootLevelDirectoryButNotDriveRoot(t *testing.T) {
	if _, err := paths(`C:\Arkism-valid-target`); err != nil {
		t.Fatal(err)
	}
	if _, err := paths(`C:\`); err == nil {
		t.Fatal("accepted drive root")
	}
}
func TestInstallerPackageRepairWithoutCheckpointDoesNotCreateState(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "not installed")
	if result, err := RepairInstall(live); err != nil || result.Committed {
		t.Fatalf("%+v %v", result, err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatal("repair created state", err)
	}
	if err := WritePackage(root, "0.2.0", filepath.Join(root, "bad.json")); err == nil || !strings.Contains(err.Error(), "inside_payload") {
		t.Fatal(err)
	}
}

func TestInstallerPackageLifecycleUsesNativeScopedMetadataAndReceipt(t *testing.T) {
	f := newMetadataFixture(t, false)
	source, output := packageFixture(t)
	if err := WritePackage(source, "0.2.0", output); err != nil {
		t.Fatal(err)
	}
	p, err := ReadPackage(output)
	if err != nil {
		t.Fatal(err)
	}
	factory := func(live, version string) (Metadata, error) {
		if live != f.live {
			t.Fatal("wrong install scope")
		}
		return scopedMetadata(f.scope, version)
	}
	check := func(live, version string) error { return checkInstalled(live, version, f.scope.Registration) }
	if err := check(f.live, p.Version); err == nil {
		t.Fatal("accepted missing registration")
	}
	result, err := installPackage(f.live, source, p, factory)
	if err != nil || !result.Committed {
		t.Fatalf("%+v %v", result, err)
	}
	if err := check(f.live, p.Version); err != nil {
		t.Fatal(err)
	}
	if err := check(f.live, "0.3.0"); err == nil {
		t.Fatal("accepted wrong version")
	}
	if result, err = repairInstall(f.live, factory); err != nil || result.Recovered {
		t.Fatalf("%+v %v", result, err)
	}
	mustWrite(t, filepath.Join(f.live, "user data.txt"), "preserve")
	if err := removeInstalledPayload(f.live, p.Version, check); err != nil {
		t.Fatal(err)
	}
	if mustRead(t, filepath.Join(f.live, "user data.txt")) != "preserve" {
		t.Fatal("lost user file")
	}
	if err := f.metadata.removeInstalled(); err != nil {
		t.Fatal(err)
	}
	if err := check(f.live, p.Version); err == nil {
		t.Fatal("registration remains")
	}
	if err := removeInstalledPayload(f.live, p.Version, check); err == nil {
		t.Fatal("removed unregistered payload")
	}
}

func TestInstallerPackageRepairFinishesCommittedCheckpoint(t *testing.T) {
	f := newMetadataFixture(t, false)
	engine := &Engine{checkpoint: func(point string) error {
		if point == "committed" {
			return errors.New("injected_archive_failure")
		}
		return nil
	}}
	r := f.request()
	r.Metadata = f.metadata
	if result, err := engine.Install(r); err == nil || !result.Committed {
		t.Fatalf("%+v %v", result, err)
	}
	factory := func(live, version string) (Metadata, error) {
		if live != f.live || version != "0.0.0" {
			t.Fatal("repair must use checkpoint version")
		}
		return scopedMetadata(f.scope, version)
	}
	if result, err := repairInstall(f.live, factory); err != nil || !result.Committed {
		t.Fatalf("%+v %v", result, err)
	}
	if err := verify(f.live, f.expected); err != nil {
		t.Fatal(err)
	}
}

func TestInstallerHelperAndMetadataFactoriesCannotSelectAnotherUserScope(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "new install")
	if err := helperOutsidePayload(live); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := helperOutsidePayload(filepath.Dir(exe)); err == nil {
		t.Fatal("accepted helper inside payload")
	}
	adapter, err := NewMetadata(live, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	m := adapter.(*nativeMetadata)
	folders, err := shortcutFolders()
	if err != nil {
		t.Fatal(err)
	}
	if m.scope.Registration != uninstallRegistration || m.scope.Live != live || m.scope.Links != [2]string{filepath.Join(folders[0], "Arkism.lnk"), filepath.Join(folders[1], "Arkism.lnk")} {
		t.Fatal("factory escaped fixed current user scope")
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatal("scope construction wrote state")
	}
	if _, err := NewMetadata(live, "invalid"); err == nil {
		t.Fatal("accepted invalid version")
	}
}
