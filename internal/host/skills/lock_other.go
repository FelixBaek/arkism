//go:build !unix

package skills

import "os"

// tryLock only opens the file here: the scanners and the minute sync run on the server, not on Windows.
func tryLock(path string) (unlock func(), ok bool, err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return nil, false, err
	}
	return func() { f.Close() }, true, nil
}
