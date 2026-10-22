//go:build pilot_e2e && windows

package store

import (
	"os"
	"sync"

	"golang.org/x/sys/windows"
)

// pilotCatalogFileLock requires a caller-supplied verified, persistent lock
// handle. It serializes goroutines locally and uses an exclusive LockFileEx
// range so independent processes converge on the same lock object.
type pilotCatalogFileLock struct {
	file       *os.File
	gate       sync.Mutex
	overlapped windows.Overlapped
}

func newPilotCatalogFileLock(file *os.File) *pilotCatalogFileLock {
	return &pilotCatalogFileLock{file: file}
}

func (lock *pilotCatalogFileLock) Lock() error {
	if lock == nil || lock.file == nil {
		return os.ErrInvalid
	}
	lock.gate.Lock()
	err := windows.LockFileEx(windows.Handle(lock.file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &lock.overlapped)
	if err != nil {
		lock.gate.Unlock()
	}
	return err
}

func (lock *pilotCatalogFileLock) TryLock() (bool, error) {
	if lock == nil || lock.file == nil {
		return false, os.ErrInvalid
	}
	if !lock.gate.TryLock() {
		return false, nil
	}
	err := windows.LockFileEx(windows.Handle(lock.file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &lock.overlapped)
	if err == windows.ERROR_LOCK_VIOLATION {
		lock.gate.Unlock()
		return false, nil
	}
	if err != nil {
		lock.gate.Unlock()
		return false, err
	}
	return true, nil
}

func (lock *pilotCatalogFileLock) Unlock() error {
	if lock == nil || lock.file == nil {
		return os.ErrInvalid
	}
	err := windows.UnlockFileEx(windows.Handle(lock.file.Fd()), 0, 1, 0, &lock.overlapped)
	lock.gate.Unlock()
	return err
}
