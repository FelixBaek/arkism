//go:build unix

package main

import (
	"os"
	"syscall"
)

// A regular-file Lstat can race replacement by a FIFO. Open nonblocking and
// reject symlink leaves atomically; readAt then checks opened type and identity.
func openReadFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
