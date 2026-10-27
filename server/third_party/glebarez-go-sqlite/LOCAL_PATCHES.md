# Local patches

This directory is based on `github.com/glebarez/go-sqlite` v1.21.2. It is a
small local fork selected by `server/go.mod` so transaction cleanup can be
patched without changing the GORM dialector.

`sqlite.go:tx.Commit` checks SQLite's autocommit state after a failed COMMIT.
If SQLite still has an active transaction (for example, a deferred foreign-key
constraint failure), it issues ROLLBACK before `database/sql` returns that
connection to the pool. If rollback fails or autocommit remains disabled, it
wraps `driver.ErrBadConn` with the original COMMIT error so `database/sql`
discards the physical connection. Callers can still inspect the original
COMMIT failure and classify the outcome as uncertain.

`commit_cleanup_test.go` locks the clean/unclean cleanup classification,
including `errors.Is(err, driver.ErrBadConn)` when cleanup cannot be proven.
The application regression is
`server/internal/api/TestDeferredCommitFailureStopsTeacherContinuationAndSnapshot`.
It verifies the failed write does not leak and a connection-scoped TEMP table
remains usable after recovery through the single-connection application pool.
