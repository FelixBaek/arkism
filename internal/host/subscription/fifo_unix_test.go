//go:build !windows

package subscription

import "golang.org/x/sys/unix"

func testMkfifo(path string) error { return unix.Mkfifo(path, 0o600) }
func testKill(pid int)             { _ = unix.Kill(pid, unix.SIGKILL) }
