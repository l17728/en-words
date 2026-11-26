package sqlite // import "modernc.org/sqlite"

import (
	"database/sql/driver"
	"errors"
	"testing"
)

func TestFailedCommitCleanupErrorDiscardsUncleanConnection(t *testing.T) {
	commitErr := errors.New("deferred foreign-key violation")

	for _, tc := range []struct {
		name                    string
		rollbackErr             error
		transactionStillActive  bool
	}{
		{name: "rollback failed", rollbackErr: errors.New("rollback I/O error")},
		{name: "rollback left transaction active", transactionStillActive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := failedCommitCleanupError(commitErr, tc.rollbackErr, tc.transactionStillActive)
			if !errors.Is(got, commitErr) {
				t.Fatalf("cleanup error %v does not preserve COMMIT error", got)
			}
			if !errors.Is(got, driver.ErrBadConn) {
				t.Fatalf("cleanup error %v does not request connection discard", got)
			}
		})
	}
}

func TestFailedCommitCleanupErrorPreservesCleanConnection(t *testing.T) {
	commitErr := errors.New("deferred foreign-key violation")
	got := failedCommitCleanupError(commitErr, nil, false)
	if got != commitErr {
		t.Fatalf("clean connection should return original COMMIT error, got %v", got)
	}
	if errors.Is(got, driver.ErrBadConn) {
		t.Fatalf("clean connection should not be discarded: %v", got)
	}
}
