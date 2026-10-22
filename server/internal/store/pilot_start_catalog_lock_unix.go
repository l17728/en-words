//go:build pilot_e2e && unix

package store

import (
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// pilotCatalogFileLock requires a caller-supplied verified, persistent lock
// handle. It serializes goroutines locally and uses flock for process safety.
type pilotCatalogFileLock struct {
	file *os.File
	gate sync.Mutex
}

func newPilotCatalogFileLock(file *os.File) *pilotCatalogFileLock {
	return &pilotCatalogFileLock{file: file}
}

func (lock *pilotCatalogFileLock) Lock() error {
	if lock == nil || lock.file == nil {
		return os.ErrInvalid
	}
	lock.gate.Lock()
	for {
		err := unix.Flock(int(lock.file.Fd()), unix.LOCK_EX)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			lock.gate.Unlock()
		}
		return err
	}
}

func (lock *pilotCatalogFileLock) TryLock() (bool, error) {
	if lock == nil || lock.file == nil {
		return false, os.ErrInvalid
	}
	if !lock.gate.TryLock() {
		return false, nil
	}
	err := unix.Flock(int(lock.file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
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
	err := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	lock.gate.Unlock()
	return err
}
