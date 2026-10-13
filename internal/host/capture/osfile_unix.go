//go:build !windows

package capture

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// tryLock takes an exclusive flock without waiting (fcntl.flock LOCK_EX|LOCK_NB, so it
// still interlocks with Python processes); a held lock is errBusy.
func tryLock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return errBusy
	}
	return err
}

// waitLock blocks until the exclusive flock is ours.
func waitLock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX) }

// openNoFollow opens path read-only, refusing a final symlink and never blocking on a FIFO.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
}

func stampOf(fi os.FileInfo) fileStamp {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileStamp{Size: fi.Size(), MtimeNs: fi.ModTime().UnixNano()}
	}
	return fileStamp{Dev: uint64(st.Dev), Ino: uint64(st.Ino), Nlink: uint64(st.Nlink), Size: st.Size,
		MtimeNs: mtimeNs(st), CtimeNs: ctimeNs(st)}
}

// pathStamp is os.stat (follow) or os.lstat identity of path.
func pathStamp(path string, follow bool) (fileStamp, error) {
	stat := os.Lstat
	if follow {
		stat = os.Stat
	}
	fi, err := stat(path)
	if err != nil {
		return fileStamp{}, err
	}
	return stampOf(fi), nil
}

// handleStamp is os.fstat of an open file.
func handleStamp(f *os.File) (fileStamp, error) {
	fi, err := f.Stat()
	if err != nil {
		return fileStamp{}, err
	}
	return stampOf(fi), nil
}

// detach starts cmd in its own session (start_new_session=True).
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// killGroup ends the process group cmd leads (os.killpg(pid, SIGKILL)).
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// syncDir fsyncs a directory so a rename survives a crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
