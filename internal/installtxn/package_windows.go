package installtxn

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Package is produced after every payload file, including the emitted
// uninstaller, has its final signature. The containing authenticated installer
// authenticates this inventory; this JSON is not independently trusted.
type Package struct {
	Format  int
	Version string
	Payload Manifest
}

var requiredPayload = []string{
	"Arkism.exe", "uninstall.exe", "integration/arkism.exe", "runtime/arkism-installer.exe",
	"runtime/memoryd.exe", "runtime/memory-principal.exe", "runtime/roles.sql",
	"runtime/pgsql/bin/postgres.exe", "runtime/pgsql/bin/pg_ctl.exe", "runtime/pgsql/bin/initdb.exe",
	"runtime/pgsql/bin/psql.exe", "runtime/pgsql/bin/pg_dump.exe", "runtime/pgsql/bin/pg_restore.exe",
	"runtime/pgsql/lib/vector.dll", "runtime/pgsql/share/extension/vector.control",
}

func (p Package) Validate() error {
	if p.Format != 1 || !releaseVersion.MatchString(p.Version) {
		return errors.New("install_package_format_invalid")
	}
	if err := p.Payload.Validate(); err != nil {
		return err
	}
	files := map[string]bool{}
	for _, file := range p.Payload.Files {
		files[file.Path] = true
	}
	for _, name := range requiredPayload {
		if !files[name] {
			return errors.New("install_package_required_file_missing: " + name)
		}
	}
	return nil
}

// WritePackage writes once, outside the payload tree. No live install metadata
// is accessed while packing; a changed file invalidates the final verification.
func WritePackage(source, version, destination string) error {
	if !releaseVersion.MatchString(version) {
		return errors.New("install_package_version_invalid")
	}
	if !filepath.IsAbs(source) || filepath.Clean(source) != source || !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return errors.New("install_package_path_invalid")
	}
	a, b := strings.ToLower(source), strings.ToLower(destination)
	if b == a || strings.HasPrefix(b, a+`\`) {
		return errors.New("install_manifest_inside_payload")
	}
	release, err := pinAncestors(source)
	if err != nil {
		return err
	}
	defer release()
	outputRelease, err := pinAncestors(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer outputRelease()
	inventory, err := Inventory(source)
	if err != nil {
		return err
	}
	p := Package{1, version, inventory}
	if err = p.Validate(); err != nil {
		return err
	}
	if err = writeNewJSON(destination, p); err != nil {
		return err
	}
	return verify(source, p.Payload)
}
func ReadPackage(path string) (Package, error) {
	var p Package
	if err := readJSON(path, &p); err != nil {
		return p, err
	}
	return p, p.Validate()
}

// InstallPackage verifies all bytes before constructing a metadata adapter. The
// NSIS caller extracts this bundle next to the authenticated helper executable.
func InstallPackage(live, source string, p Package) (Result, error) {
	return installPackage(live, source, p, NewMetadata)
}

// Native integration tests bind the same lifecycle to owned registry/shortcut
// namespaces. The exported entry point always supplies the fixed user scope.
func installPackage(live, source string, p Package, metadataFor func(string, string) (Metadata, error)) (Result, error) {
	if err := p.Validate(); err != nil {
		return Result{}, err
	}
	if err := verify(source, p.Payload); err != nil {
		return Result{}, err
	}
	if err := helperOutsidePayload(live); err != nil {
		return Result{}, err
	}
	metadata, err := metadataFor(live, p.Version)
	if err != nil {
		return Result{}, err
	}
	return new(Engine).Install(Request{Live: live, Source: source, Expected: p.Payload, Metadata: metadata, Stop: func(path string) error { return StopPayload(path, 30*time.Second) }})
}

// RepairInstall leaves an untouched installation alone when no checkpoint
// exists. It never creates an ownership directory merely to perform a repair.
func RepairInstall(live string) (Result, error) {
	return repairInstall(live, NewMetadata)
}
func repairInstall(live string, metadataFor func(string, string) (Metadata, error)) (Result, error) {
	l, err := paths(live)
	if err != nil {
		return Result{}, err
	}
	release, err := pinAncestors(filepath.Dir(live))
	if err != nil {
		return Result{}, err
	}
	defer release()
	exists, err := directoryExists(l.versions)
	if err != nil || !exists {
		return Result{}, err
	}
	exists, err = directoryExists(l.pending)
	if err != nil || !exists {
		return Result{}, err
	}
	metadata, err := metadataFor(live, "0.0.0")
	if err != nil {
		return Result{}, err
	}
	return new(Engine).Recover(live, metadata, func(path string) error { return StopPayload(path, 30*time.Second) })
}

// VerifyPackage checks extraction without stopping processes or writing any
// registration. Installers use it before starting a transaction.
func VerifyPackage(source string, p Package) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return verify(source, p.Payload)
}

func helperOutsidePayload(live string) error {
	if _, err := paths(live); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	a, b := strings.ToLower(filepath.Clean(executable)), strings.ToLower(filepath.Clean(live))
	if a == b || strings.HasPrefix(a, b+`\`) {
		return errors.New("installer_helper_inside_payload")
	}
	return nil
}
