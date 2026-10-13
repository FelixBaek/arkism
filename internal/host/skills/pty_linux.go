//go:build linux

package skills

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// openTerminal starts argv on a new pseudo-terminal, 1000 columns wide so the sign-in address is never wrapped.
func openTerminal(argv []string) (*terminal, error) {
	// Non-blocking, so os.NewFile puts it on the poller and read deadlines work.
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	master := os.NewFile(uintptr(fd), "/dev/ptmx")
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		return nil, err
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		master.Close()
		return nil, err
	}
	if err := unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: 50, Col: 1000}); err != nil {
		master.Close()
		return nil, err
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, err
	}
	defer slave.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.Env = append(os.Environ(), "ARKISM_CAPTURE_CHILD=1", "BROWSER=true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		master.Close()
		return nil, err
	}
	t := &terminal{master: master, cmd: cmd, done: make(chan struct{})}
	go func() { cmd.Wait(); close(t.done) }()
	return t, nil
}
