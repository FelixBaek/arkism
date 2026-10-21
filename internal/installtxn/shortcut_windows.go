package installtxn

// Shell link persistence is kept in one small COM boundary. The interface order
// follows IShellLinkW, IShellLinkDataList, IPersistStream and IStream in the SDK.
// No shell command is composed, and reading a shortcut never calls Resolve.
import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const maxShortcutBytes = 64 << 10
const fixedShortcutFlags = 0x00040000 | 0x00100000 | 0x00200000

var shellLinkCLSID = constantGUID("{00021401-0000-0000-C000-000000000046}")
var shellLinkIID = constantGUID("{000214F9-0000-0000-C000-000000000046}")
var persistStreamIID = constantGUID("{00000109-0000-0000-C000-000000000046}")
var linkDataIID = constantGUID("{45E2B4AE-B1C3-11D0-B92F-00A0C90312E1}")
var createCOM = windows.NewLazySystemDLL("ole32.dll").NewProc("CoCreateInstance")
var createMemStream = windows.NewLazySystemDLL("ole32.dll").NewProc("CreateStreamOnHGlobal")

type comObject struct{ methods *[21]uintptr }

func constantGUID(value string) windows.GUID {
	guid, err := windows.GUIDFromString(value)
	if err != nil {
		panic(err)
	}
	return guid
}
func hresult(value uintptr) error {
	if int32(value) < 0 {
		return fmt.Errorf("install_shortcut_hresult_%08x", uint32(value))
	}
	return nil
}

//go:uintptrescapes
func (object *comObject) call(method int, args ...uintptr) error {
	arguments := append([]uintptr{uintptr(unsafe.Pointer(object))}, args...)
	value, _, _ := syscall.SyscallN(object.methods[method], arguments...)
	runtime.KeepAlive(object)
	return hresult(value)
}
func (object *comObject) release() { object.call(2) }
func (object *comObject) query(iid *windows.GUID) (*comObject, error) {
	var result *comObject
	err := object.call(0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&result)))
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("install_shortcut_interface_missing")
	}
	return result, nil
}
func withShellLink(action func(*comObject) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err := windows.CoInitializeEx(0, 2)
	if err != nil && !errors.Is(err, syscall.Errno(1)) {
		return err
	}
	defer windows.CoUninitialize()
	var object *comObject
	hr, _, _ := createCOM.Call(uintptr(unsafe.Pointer(&shellLinkCLSID)), 0, 1, uintptr(unsafe.Pointer(&shellLinkIID)), uintptr(unsafe.Pointer(&object)))
	if err = hresult(hr); err != nil {
		return err
	}
	if object == nil {
		return errors.New("install_shortcut_creation_failed")
	}
	defer object.release()
	return action(object)
}
func (object *comObject) setString(method int, value string) error {
	data, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return err
	}
	return object.call(method, uintptr(unsafe.Pointer(data)))
}
func (object *comObject) getString(method int, rawPath bool) (string, error) {
	buffer := make([]uint16, 32768)
	var err error
	if rawPath {
		err = object.call(method, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, 4)
	} else {
		err = object.call(method, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	}
	runtime.KeepAlive(buffer)
	if err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer), nil
}
func memoryStream(data []byte) (*comObject, error) {
	var stream *comObject
	hr, _, _ := createMemStream.Call(0, 1, uintptr(unsafe.Pointer(&stream)))
	if err := hresult(hr); err != nil {
		return nil, err
	}
	if stream == nil {
		return nil, errors.New("install_shortcut_stream_failed")
	}
	if len(data) > 0 {
		var written uint32
		err := stream.call(4, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&written)))
		runtime.KeepAlive(data)
		if err == nil && written != uint32(len(data)) {
			err = errors.New("install_shortcut_stream_short_write")
		}
		if err == nil {
			err = rewindStream(stream)
		}
		if err != nil {
			stream.release()
			return nil, err
		}
	}
	return stream, nil
}
func rewindStream(stream *comObject) error {
	// LARGE_INTEGER occupies one native argument on 64-bit Windows, two on x86.
	var position uint64
	if unsafe.Sizeof(uintptr(0)) == 4 {
		return stream.call(5, 0, 0, 0, uintptr(unsafe.Pointer(&position)))
	}
	return stream.call(5, 0, 0, uintptr(unsafe.Pointer(&position)))
}
func saveLink(object *comObject) ([]byte, error) {
	persist, err := object.query(&persistStreamIID)
	if err != nil {
		return nil, err
	}
	defer persist.release()
	stream, err := memoryStream(nil)
	if err != nil {
		return nil, err
	}
	defer stream.release()
	if err = persist.call(6, uintptr(unsafe.Pointer(stream)), 1); err != nil {
		return nil, err
	}
	if err = rewindStream(stream); err != nil {
		return nil, err
	}
	data := make([]byte, maxShortcutBytes+1)
	var size uint32
	if err = stream.call(3, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&size))); err != nil {
		return nil, err
	}
	if size == 0 || size > maxShortcutBytes {
		return nil, errors.New("install_shortcut_size_invalid")
	}
	return data[:size], nil
}
func newShortcut(target string) (data []byte, err error) {
	err = withShellLink(func(object *comObject) error {
		flags, err := object.query(&linkDataIID)
		if err != nil {
			return err
		}
		defer flags.release()
		if err = object.setString(20, target); err != nil {
			return err
		}
		if err = object.setString(9, filepath.Dir(target)); err != nil {
			return err
		}
		// SetPath may populate its own low flags. Preserve those while forcing
		// the documented tracking suppressions after setting the pathname.
		var current uint32
		if err = flags.call(6, uintptr(unsafe.Pointer(&current))); err != nil {
			return err
		}
		if err = flags.call(7, uintptr(current|fixedShortcutFlags)); err != nil {
			return err
		}
		data, err = saveLink(object)
		return err
	})
	return data, err
}

type shortcutTarget struct {
	Path, Arguments string
	Flags           uint32
}

func inspectShortcut(data []byte) (result shortcutTarget, err error) {
	if len(data) == 0 || len(data) > maxShortcutBytes {
		return result, errors.New("install_shortcut_size_invalid")
	}
	err = withShellLink(func(object *comObject) error {
		persist, err := object.query(&persistStreamIID)
		if err != nil {
			return err
		}
		defer persist.release()
		stream, err := memoryStream(data)
		if err != nil {
			return err
		}
		defer stream.release()
		if err = persist.call(5, uintptr(unsafe.Pointer(stream))); err != nil {
			return err
		}
		if result.Path, err = object.getString(3, true); err != nil {
			return err
		}
		if result.Arguments, err = object.getString(10, false); err != nil {
			return err
		}
		flags, err := object.query(&linkDataIID)
		if err != nil {
			return err
		}
		defer flags.release()
		return flags.call(6, uintptr(unsafe.Pointer(&result.Flags)))
	})
	return result, err
}
