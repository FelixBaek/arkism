//go:build unix

// Package processlock supplies a crash-released singleton lock for host daemons.
package processlock

import (
	"golang.org/x/sys/unix"
	"os"
)

func Acquire(path string) (func(), error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}
