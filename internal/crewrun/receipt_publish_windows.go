package crewrun

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Windows does not support fsync on an opened directory. Publish the already
// synced temporary file with a write-through move on the same volume. Omitting
// REPLACE_EXISTING preserves the same first-writer-wins receipt contract.
func publishReceipt(temporary, target, _ string) error {
	from, err := receiptWindowsPath(temporary)
	if err != nil {
		return err
	}
	to, err := receiptWindowsPath(target)
	if err != nil {
		return err
	}
	err = windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
	if err == windows.ERROR_ALREADY_EXISTS || err == windows.ERROR_FILE_EXISTS {
		return nil
	}
	return err
}

// Keep Go's normal long-path support when calling a native Windows API directly.
// Verbatim paths require an absolute spelling and preserve UNC shares explicitly.
func receiptWindowsPath(path string) (*uint16, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(absolute, `\\?\`) && !strings.HasPrefix(absolute, `\\.\`) {
		if strings.HasPrefix(absolute, `\\`) {
			absolute = `\\?\UNC\` + absolute[2:]
		} else {
			absolute = `\\?\` + absolute
		}
	}
	return windows.UTF16PtrFromString(absolute)
}
