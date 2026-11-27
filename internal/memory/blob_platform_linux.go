package memory

import (
	"errors"
	"os"
	"syscall"
)

const blobOpenFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK

func privateBlobRoot(_ *os.Root, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm() != 0700 || !ok || stat.Uid != uint32(os.Geteuid()) {
		return ErrInvalid
	}
	return nil
}

func privateBlobFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !ok || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) {
		return ErrInvalid
	}
	return nil
}

func tryBlobLock(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR) {
		return false, nil
	}
	return err == nil, err
}

func syncBlobDirectory(directory *os.File) error { return directory.Sync() }
