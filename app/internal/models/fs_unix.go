//go:build unix

package models

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// openBeneath opens rel below the directory currently at base.
func openBeneath(base, rel string, flag int, perm os.FileMode, createParents bool) (*os.File, error) {
	dir, err := retryEINTR(func() (int, error) {
		return unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBaseUnavailable, err)
	}
	defer func() { _ = unix.Close(dir) }()
	return openRelative(dir, rel, flag, perm, createParents)
}

// openAt opens rel below an already-open directory, so replacing the path that
// directory was opened from cannot redirect the operation.
func openAt(base *os.File, rel string, flag int, perm os.FileMode, createParents bool) (*os.File, error) {
	return openRelative(int(base.Fd()), rel, flag, perm, createParents)
}

// openRelative walks rel without following a symlink at any component.
// O_NONBLOCK keeps FIFOs from hanging the open; only regular files are returned.
func openRelative(start int, rel string, flag int, perm os.FileMode, createParents bool) (*os.File, error) {
	dir := start
	defer func() {
		if dir != start {
			_ = unix.Close(dir)
		}
	}()

	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		next, err := openDirectoryAt(dir, part, createParents)
		if err != nil {
			return nil, classifyOpen(err)
		}
		if dir != start {
			_ = unix.Close(dir)
		}
		dir = next
	}

	fd, err := retryEINTR(func() (int, error) {
		return unix.Openat(dir, parts[len(parts)-1], flag|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, uint32(perm.Perm()))
	})
	if err != nil {
		return nil, classifyOpen(err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return nil, errUnsafeFile
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), parts[len(parts)-1]), nil
}

func openDirectoryAt(dir int, name string, create bool) (int, error) {
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	next, err := retryEINTR(func() (int, error) { return unix.Openat(dir, name, flags, 0) })
	if errors.Is(err, unix.ENOENT) && create {
		if err := unix.Mkdirat(dir, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, err
		}
		next, err = retryEINTR(func() (int, error) { return unix.Openat(dir, name, flags, 0) })
	}
	return next, err
}

func classifyOpen(err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.EMLINK) {
		return fmt.Errorf("%w: %w", errUnsafeFile, err)
	}
	return err
}

func retryEINTR(open func() (int, error)) (int, error) {
	for {
		fd, err := open()
		if !errors.Is(err, unix.EINTR) {
			return fd, err
		}
	}
}

func freeBytes(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}

func lockExclusive(file *os.File) error {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return errLocked
		}
		return err
	}
	return nil
}

func fileIdentity(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Ino)
	}
	return 0
}
