package installtxn

import (
	"errors"
	"path/filepath"
	"reflect"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// CheckInstalled binds destructive uninstall work to the registered path and
// exact version. A copied or older uninstaller cannot remove another install.
func CheckInstalled(live, version string) error {
	return checkInstalled(live, version, uninstallRegistration)
}
func checkInstalled(live, version, registration string) error {
	l, err := paths(live)
	if err != nil {
		return err
	}
	if !releaseVersion.MatchString(version) {
		return errors.New("uninstall_version_invalid")
	}
	release, err := pinAncestors(filepath.Dir(l.live))
	if err != nil {
		return err
	}
	defer release()
	current, err := readRegistration(registration)
	if err != nil {
		return err
	}
	return installedClaim(current, live, version)
}
func installedClaim(current registrySnapshot, live, version string) error {
	if !ownsRegistration(current, live) || !reflect.DeepEqual(current.Values["DisplayVersion"], registryString(version)) {
		return errors.New("uninstall_registration_not_owned")
	}
	return nil
}

// RemoveInstalledMetadata runs after program removal. It preserves foreign
// shortcuts and unknown registry values rather than deleting their namespace.
func RemoveInstalledMetadata(live, version string) error {
	if err := CheckInstalled(live, version); err != nil {
		return err
	}
	metadata, err := NewMetadata(live, version)
	if err != nil {
		return err
	}
	return metadata.(*nativeMetadata).removeInstalled()
}
func (m *nativeMetadata) removeInstalled() error {
	release, err := m.pin()
	if err != nil {
		return err
	}
	defer release()
	current, err := readRegistration(m.scope.Registration)
	if err != nil {
		return err
	}
	if err = installedClaim(current, m.scope.Live, m.version); err != nil {
		return err
	}
	var links [2]*linkFile
	defer func() {
		for _, link := range links {
			link.close()
		}
	}()
	for i, path := range m.scope.Links {
		link, err := openLink(path)
		if err != nil {
			return err
		}
		links[i] = link
		if link == nil {
			continue
		}
		target, parseErr := inspectShortcut(link.data)
		if parseErr != nil || !samePath(target.Path, filepath.Join(m.scope.Live, "Arkism.exe")) {
			link.close()
			links[i] = nil
		}
	}
	if err = removeRegistration(m.scope.Registration, current); err != nil {
		return err
	}
	for _, link := range links {
		if err = link.remove(); err != nil {
			return err
		}
	}
	return nil
}
func removeRegistration(path string, expected registrySnapshot) error {
	tx, err := beginRegistryTransaction()
	if err != nil {
		return err
	}
	defer windows.CloseHandle(tx)
	key, err := transactionKey(tx, path, false)
	if err != nil {
		return err
	}
	current, err := readRegistrationKey(key)
	if err != nil {
		key.Close()
		return err
	}
	if !sameRegistry(current, expected) {
		key.Close()
		return errors.New("uninstall_registration_changed")
	}
	for _, name := range registrationNames {
		if err = key.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
			key.Close()
			return err
		}
	}
	remaining, err := readRegistrationKey(key)
	key.Close()
	if err != nil {
		return err
	}
	if len(remaining.Values) == 0 {
		pointer, _ := windows.UTF16PtrFromString(path)
		code, _, _ := deleteTransactedKey.Call(uintptr(registry.CURRENT_USER), uintptr(unsafe.Pointer(pointer)), registry.WOW64_32KEY, 0, uintptr(tx), 0)
		if code != 0 {
			return windows.Errno(code)
		}
	}
	ok, _, err := commitTransaction.Call(uintptr(tx))
	if ok == 0 {
		return err
	}
	return nil
}
