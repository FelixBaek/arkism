//go:build !unix && !windows

package subscription

import (
	"errors"
	"os"
	"os/exec"
)

var errNoGroups = errors.New("process groups unavailable")

// The usage probe needs a process group it can kill as a whole.
func newSession(*exec.Cmd) error { return errNoGroups }

func killGroup(int) error { return errNoGroups }

func openNoFollow(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err != nil {
		return nil, err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("credential file is a link")
	}
	return os.Open(path)
}
