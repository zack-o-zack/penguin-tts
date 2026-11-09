//go:build !unix

package models

import (
	"errors"
	"os"
)

var errUnsupportedPlatform = errors.New("model storage is not supported on this platform")

func openBeneath(string, string, int, os.FileMode, bool) (*os.File, error) {
	return nil, errUnsupportedPlatform
}

func openAt(*os.File, string, int, os.FileMode, bool) (*os.File, error) {
	return nil, errUnsupportedPlatform
}

func freeBytes(string) (uint64, error) {
	return 0, errUnsupportedPlatform
}

func lockExclusive(*os.File) error {
	return errUnsupportedPlatform
}

func fileIdentity(os.FileInfo) uint64 {
	return 0
}
