# Provisional Pilot SQLite Adapter Review

Review date: 2026-10-21. Scope: read-only review of `server/internal/pilotcontrol/sqlite.go`, `sqlite_nocgo.go`, and `sqlite_test.go`. No source code was changed. CGO-enabled runtime verification is pending: this Windows host has no Go executable available in PATH, so neither the package tests nor driver integration probes could be run.

## Findings

1. **BLOCK — the CGO test file does not compile.** `sqlite_test.go:63` calls `withCapability(ctx, capResolvePrincipal, nil, callback)`, while `sqlite.go` declares exactly three parameters `(ctx, cap, fn)`. Remove the stale `nil` argument or intentionally revise the signature and all callers before tests can run.

2. **BLOCK — connection setup mutates database-wide journal mode before schema preflight.** `configureConnection` executes `PRAGMA journal_mode=WAL` from `ConnectHook` on the first connection. The ledger contract requires read-only inventory/version validation before any journal-mode change, and requires a non-empty incompatible schema to remain unmodified. `Open` has no preflight phase and its own comment leaves validation to callers, so callers must not invoke it on an unvalidated database. Move WAL selection behind a successful read-only preflight (or make that ordering an enforced constructor contract); keep connection-local PRAGMA setup distinct.

3. **SHOULD FIX — per-connection authorizer state is retained after physical connection close.** `s.states` stores strong `*sqlite3.SQLiteConn` keys at ConnectHook registration and has no removal path when `database/sql` retires/replaces a connection. Repeated connection churn therefore retains every old connection and its state until Store.Close. Add a close-aware driver connection wrapper/registry cleanup while preserving access to the underlying SQLiteConn in `Conn.Raw`, or use another bounded lifecycle design.

### Additional static finding

**BLOCK — the trigger exception trusts a trigger name before schema validation.** `connectionState.authorize` permits writes to protected capacity tables whenever SQLite reports source `pilot_allocation_capacity_after_insert`; it does not bind the exception to a validated trigger definition. Since `Open` currently does not perform exact-schema/DDL validation before returning the store, a pre-existing forged trigger with that name could alter quota counters during an otherwise permitted alias insert. Validate exact normalized DDL before enabling readiness or capabilities, or remove this exception until validation is active. Add a regression fixture with the approved trigger name and a tampered body; prove Open/admission rejects it without writes.

## Reviewed behavior

The adapter correctly uses a `database/sql/driver.Connector`, installs the authorizer from `ConnectHook`, verifies WAL/foreign-key/recursive-trigger/trusted-schema/synchronous/busy-timeout settings, warms the configured pool before returning, ties scoped permissions to one pinned physical connection, and defers rollback and capability clearing across returned errors and panic unwinding. The `!cgo` stub fails closed with `ErrCGODisabled`.

Runtime/API compatibility with mattn/go-sqlite3 v1.14.22, hook failure cleanup by the driver, connection-churn behavior, lock/race behavior, and all CGO tests remain unverified until a CGO-enabled host runs the suite.
