// Package logfile opens and rotates daemon.log. The daemon's standard output
// and error are the log, so whoever starts the daemon opens it, and the
// daemon rotates it while it runs.
package logfile

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// MaxBytes is the size at which the log is archived. It is a variable so
// rotation tests need not write megabytes.
var MaxBytes int64 = 3 << 20

// Open opens the daemon log for appending, rotating it first when it has
// reached MaxBytes. It refuses anything but a regular file owned only by the
// current user, so a link planted at the path cannot redirect the log.
func Open(path string) (*os.File, error) {
	file, info, err := openPrivate(path)
	if err != nil {
		return nil, err
	}
	if info.Size() < MaxBytes {
		return file, nil
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := archive(path); err != nil {
		return nil, err
	}
	file, _, err = openPrivate(path)
	return file, err
}

// RotateIfFull archives the log at path when it has reached MaxBytes and
// returns a new, empty log in its place. It returns nil when the log still
// has room. A running daemon calls it, so a log does not grow between starts.
func RotateIfFull(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if info.Size() < MaxBytes {
		return nil, nil
	}
	if err := archive(path); err != nil {
		return nil, err
	}
	file, _, err := openPrivate(path)
	return file, err
}

// archive keeps one previous log beside the current one.
func archive(path string) error {
	previous := path + ".1"
	if err := os.Remove(previous); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove archived daemon log: %w", err)
	}
	if err := os.Rename(path, previous); err != nil {
		return fmt.Errorf("archive daemon log: %w", err)
	}
	return nil
}

func openPrivate(path string) (*os.File, os.FileInfo, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_APPEND|syscall.O_WRONLY|
		syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		file.Close()
		return nil, nil, fmt.Errorf("daemon log must be a regular file owned only by the current user")
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, nil, err
	}
	if err := syscall.SetNonblock(fd, false); err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, info, nil
}
