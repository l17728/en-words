//go:build windows

package api

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

var errSpeechCacheLockBusy = errors.New("speech cache lock busy")

func trySpeechCacheFileLock(file *os.File) (func(), bool, error) {
	overlapped := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return nil, false, errSpeechCacheLockBusy
	}
	if err != nil {
		return nil, false, err
	}
	return func() { _ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped) }, true, nil
}
