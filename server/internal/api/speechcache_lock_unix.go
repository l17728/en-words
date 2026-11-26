//go:build !windows

package api

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var errSpeechCacheLockBusy = errors.New("speech cache lock busy")

func trySpeechCacheFileLock(file *os.File) (func(), bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return nil, false, errSpeechCacheLockBusy
	}
	if err != nil {
		return nil, false, err
	}
	return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }, true, nil
}
