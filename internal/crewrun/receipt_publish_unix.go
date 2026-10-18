//go:build !windows

package crewrun

import (
	"errors"
	"os"
)

// A hard link publishes the complete, synced file without replacing a receipt
// another writer has already committed. Sync its directory before returning.
func publishReceipt(temporary, target, dir string) error {
	if err := os.Link(temporary, target); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
