package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// lockSpeechCache serializes a cache key across processes sharing the cache
// directory. Kernel locks are released automatically when a worker exits.
func lockSpeechCache(ctx context.Context, root, key string) (func(), error) {
	lockDir := filepath.Join(root, ".locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(lockDir, key+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		unlock, acquired, lockErr := trySpeechCacheFileLock(f)
		if acquired {
			return func() { unlock(); _ = f.Close() }, nil
		}
		if lockErr != nil && !errors.Is(lockErr, errSpeechCacheLockBusy) {
			_ = f.Close()
			return nil, lockErr
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
