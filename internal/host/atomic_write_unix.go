//go:build !windows

package host

import "os"

func replaceAtomicWrite(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}
