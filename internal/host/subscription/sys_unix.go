//go:build unix

package subscription

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

func newSession(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}

func killGroup(pid int) error { return unix.Kill(-pid, unix.SIGKILL) }

// openNoFollow refuses links, and does not block on a FIFO.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
}

// lockFile takes an exclusive flock on path; without wait a held lock fails at once.
func lockFile(path string, perm os.FileMode, wait bool) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, perm)
	if err != nil {
		return nil, err
	}
	how := unix.LOCK_EX
	if !wait {
		how |= unix.LOCK_NB
	}
	if err := unix.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}
