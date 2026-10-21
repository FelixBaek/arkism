package installtxn

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type registryValue struct {
	Kind uint32
	Data []byte
}
type registrySnapshot struct {
	Exists bool
	Values map[string]registryValue
}

var registrationNames = []string{"DisplayName", "DisplayVersion", "Publisher", "DisplayIcon", "InstallLocation", "UninstallString", "NoModify", "NoRepair"}
var createTransaction = windows.NewLazySystemDLL("ktmw32.dll").NewProc("CreateTransaction")
var commitTransaction = windows.NewLazySystemDLL("ktmw32.dll").NewProc("CommitTransaction")
var createTransactedKey = windows.NewLazySystemDLL("advapi32.dll").NewProc("RegCreateKeyTransactedW")
var openTransactedKey = windows.NewLazySystemDLL("advapi32.dll").NewProc("RegOpenKeyTransactedW")
var deleteTransactedKey = windows.NewLazySystemDLL("advapi32.dll").NewProc("RegDeleteKeyTransactedW")
var writeRegistryValue = windows.NewLazySystemDLL("advapi32.dll").NewProc("RegSetValueExW")

func normalizeRegistryName(name string) string {
	for _, known := range registrationNames {
		if strings.EqualFold(name, known) {
			return known
		}
	}
	return strings.ToLower(name)
}
func registryString(value string) registryValue {
	runes := utf16.Encode([]rune(value + "\x00"))
	data := make([]byte, len(runes)*2)
	for i, c := range runes {
		binary.LittleEndian.PutUint16(data[i*2:], c)
	}
	return registryValue{registry.SZ, data}
}
func registryDWord(value uint32) registryValue {
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, value)
	return registryValue{registry.DWORD, data}
}
func expectedRegistration(live, version string) map[string]registryValue {
	return map[string]registryValue{
		"DisplayName": registryString("Arkism"), "DisplayVersion": registryString(version), "Publisher": registryString("Arkism contributors"),
		"DisplayIcon": registryString(live + `\Arkism.exe`), "InstallLocation": registryString(live), "UninstallString": registryString(`"` + live + `\uninstall.exe"`),
		"NoModify": registryDWord(1), "NoRepair": registryDWord(1),
	}
}
func readRegistrationKey(key registry.Key) (registrySnapshot, error) {
	info, err := key.Stat()
	if err != nil {
		return registrySnapshot{}, err
	}
	if info.SubKeyCount != 0 || info.ValueCount > 256 || info.MaxValueLen > 64<<10 {
		return registrySnapshot{}, errors.New("install_registration_scope_changed")
	}
	names, err := key.ReadValueNames(-1)
	if err != nil {
		return registrySnapshot{}, err
	}
	if len(names) > 256 {
		return registrySnapshot{}, errors.New("install_registration_scope_changed")
	}
	result := registrySnapshot{Exists: true, Values: map[string]registryValue{}}
	total := 0
	for _, name := range names {
		size, kind, err := key.GetValue(name, nil)
		if err != nil {
			return result, err
		}
		if size > 64<<10 {
			return result, errors.New("install_registration_value_too_large")
		}
		data := make([]byte, size)
		size, again, err := key.GetValue(name, data)
		if err != nil {
			return result, err
		}
		if again != kind || size > len(data) {
			return result, errors.New("install_registration_changed")
		}
		total += size
		if total > 256<<10 {
			return result, errors.New("install_registration_snapshot_too_large")
		}
		result.Values[normalizeRegistryName(name)] = registryValue{kind, data[:size]}
	}
	return result, nil
}
func readRegistration(path string) (registrySnapshot, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.READ|registry.WOW64_32KEY)
	if errors.Is(err, registry.ErrNotExist) {
		return registrySnapshot{Values: map[string]registryValue{}}, nil
	}
	if err != nil {
		return registrySnapshot{}, err
	}
	defer key.Close()
	return readRegistrationKey(key)
}
func sameRegistry(a, b registrySnapshot) bool {
	return a.Exists == b.Exists && reflect.DeepEqual(a.Values, b.Values)
}
func registryRecoveryMatches(current, old registrySnapshot, target map[string]registryValue) bool {
	if !current.Exists {
		return !old.Exists
	}
	for name, value := range current.Values {
		previous, hadPrevious := old.Values[name]
		next, isOwned := target[name]
		if !isOwned {
			if !hadPrevious || !reflect.DeepEqual(value, previous) {
				return false
			}
			continue
		}
		if !reflect.DeepEqual(value, next) && (!hadPrevious || !reflect.DeepEqual(value, previous)) {
			return false
		}
	}
	for name := range old.Values {
		if _, exists := current.Values[name]; !exists {
			return false
		}
	}
	return true
}
func beginRegistryTransaction() (windows.Handle, error) {
	handle, _, err := createTransaction.Call(0, 0, 0, 0, 0, 0, 0)
	if windows.Handle(handle) == windows.InvalidHandle {
		return 0, err
	}
	return windows.Handle(handle), nil
}
func transactionKey(tx windows.Handle, path string, create bool) (registry.Key, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var key registry.Key
	var code uintptr
	access := uintptr(registry.ALL_ACCESS | registry.WOW64_32KEY)
	if create {
		var disposition uint32
		code, _, _ = createTransactedKey.Call(uintptr(registry.CURRENT_USER), uintptr(unsafe.Pointer(pointer)), 0, 0, 0, access, 0, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&disposition)), uintptr(tx), 0)
	} else {
		code, _, _ = openTransactedKey.Call(uintptr(registry.CURRENT_USER), uintptr(unsafe.Pointer(pointer)), 0, access, uintptr(unsafe.Pointer(&key)), uintptr(tx), 0)
	}
	if code != 0 {
		return 0, windows.Errno(code)
	}
	return key, nil
}
func setRawValue(key registry.Key, name string, value registryValue) error {
	pointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var address *byte
	if len(value.Data) > 0 {
		address = &value.Data[0]
	}
	code, _, _ := writeRegistryValue.Call(uintptr(key), uintptr(unsafe.Pointer(pointer)), 0, uintptr(value.Kind), uintptr(unsafe.Pointer(address)), uintptr(len(value.Data)))
	if code != 0 {
		return windows.Errno(code)
	}
	return nil
}
func mutateRegistration(path string, old registrySnapshot, target map[string]registryValue, restore bool, point func(string) error) error {
	tx, err := beginRegistryTransaction()
	if err != nil {
		return err
	}
	defer windows.CloseHandle(tx)
	key, err := transactionKey(tx, path, false)
	current := registrySnapshot{Values: map[string]registryValue{}}
	if err == nil {
		current, err = readRegistrationKey(key)
		key.Close()
	} else if errors.Is(err, registry.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return err
	}
	if restore {
		if !registryRecoveryMatches(current, old, target) {
			return errors.New("install_registration_foreign_change")
		}
	} else if !sameRegistry(current, old) {
		return errors.New("install_registration_changed_before_apply")
	}
	if restore && !old.Exists {
		if current.Exists {
			pointer, _ := windows.UTF16PtrFromString(path)
			code, _, _ := deleteTransactedKey.Call(uintptr(registry.CURRENT_USER), uintptr(unsafe.Pointer(pointer)), registry.WOW64_32KEY, 0, uintptr(tx), 0)
			if code != 0 {
				return windows.Errno(code)
			}
		}
	} else {
		key, err = transactionKey(tx, path, true)
		if err != nil {
			return err
		}
		err = writeOwnedRegistration(key, old, target, restore, point)
		closeErr := key.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	ok, _, err := commitTransaction.Call(uintptr(tx))
	if ok == 0 {
		return err
	}
	return nil
}
func writeOwnedRegistration(key registry.Key, old registrySnapshot, target map[string]registryValue, restore bool, point func(string) error) error {
	for _, name := range registrationNames {
		value := target[name]
		if restore {
			var exists bool
			value, exists = old.Values[name]
			if !exists {
				if err := key.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
					return err
				}
				continue
			}
		}
		if err := setRawValue(key, name, value); err != nil {
			return err
		}
		if point != nil {
			if err := point("registry_value_" + name); err != nil {
				return err
			}
		}
	}
	return nil
}
func ownsRegistration(snapshot registrySnapshot, live string) bool {
	if !snapshot.Exists {
		return false
	}
	expected := expectedRegistration(live, "unused")
	for _, name := range []string{"DisplayName", "Publisher", "DisplayIcon", "InstallLocation", "UninstallString"} {
		actual, ok := snapshot.Values[name]
		if !ok || actual.Kind != registry.SZ || !bytes.Equal(actual.Data, expected[name].Data) {
			return false
		}
	}
	return true
}
