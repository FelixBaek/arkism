//go:build linux

package crew

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// newSession starts cmd in its own session and process group (start_new_session=True).
func newSession(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// killGroup sends sig to the whole process group led by pid; a gone group is not an error.
func killGroup(pid int, sig syscall.Signal) { _ = syscall.Kill(-pid, sig) }

const sigKill, sigTerm = syscall.SIGKILL, syscall.SIGTERM

// flock takes an exclusive flock on f; held=false when another holder has it (non-blocking only).
func flock(f *os.File, blocking bool) (held bool, err error) {
	how := syscall.LOCK_EX
	if !blocking {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ownedByMe reports whether info is owned by this user.
func ownedByMe(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

func linkCount(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 0
}

func inode(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}

// statIdentity is device, inode and change time: what moves when a binary is replaced.
func statIdentity(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprint(st.Dev, " ", st.Ino, " ", st.Ctim.Nano())
}

func canExecute(path string) bool { return syscall.Access(path, 1) == nil } // X_OK

func uid() int { return os.Getuid() }

const oNoFollow = syscall.O_NOFOLLOW

// exitCode is Python's returncode: the exit status, or minus the signal that ended the process.
func exitCode(ps *os.ProcessState) int {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -int(ws.Signal())
	}
	return ps.ExitCode()
}
