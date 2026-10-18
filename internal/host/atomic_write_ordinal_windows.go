package host

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var atomicWriteOrdinalProc = windows.NewLazySystemDLL("kernel32.dll").NewProc("CompareStringOrdinal")

func atomicWriteOrdinalReady() error { return atomicWriteOrdinalProc.Find() }

// The caller resolves the procedure outside the registry lock. This compares
// UTF-16 with the Windows uppercase table; no Go Unicode case folding is used.
func atomicWriteOrdinalEqual(left, right []uint16) (bool, error) {
	if len(left) == 0 || len(right) == 0 || uint64(len(left)) > 1<<31-1 || uint64(len(right)) > 1<<31-1 {
		return false, errAtomicWriteAdmissionKey
	}
	result, _, err := atomicWriteOrdinalProc.Call(uintptr(unsafe.Pointer(&left[0])), uintptr(len(left)),
		uintptr(unsafe.Pointer(&right[0])), uintptr(len(right)), 1)
	runtime.KeepAlive(left)
	runtime.KeepAlive(right)
	if result < 1 || result > 3 {
		if err != nil && err != windows.ERROR_SUCCESS {
			return false, err
		}
		return false, errors.New("atomic_write_ordinal_comparison_failed")
	}
	return result == 2, nil
}
