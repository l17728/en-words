# Pilot SQLite Adapter Security Review — Current Snapshot

Review date: 2026-10-21. Scope: `sqlite.go`, `sqlite_test.go`, `sqlite_nocgo.go`, and `schema_inventory_v3.json`. Read-only code review; no implementation files changed. CGO execution is unavailable because Go is not installed on this host.

## Decision: BLOCK pending startup file/schema binding

The earlier static findings are addressed in the reviewed snapshot: `preflightSchema` now inventories `view` objects as well as tables, indexes, and triggers; `TestOpenRejectsUnexpectedPersistentView` locks that behavior; the stale `withCapability` argument is gone; the `!cgo` implementation still fails closed; and `protectedConn` owns per-connection authorizer state without a Store-level map retaining retired connections.

**BLOCK — schema preflight is not bound to the production database instance.** `Open` performs `preflightSchema(dsn)` using a read-only connection, closes that connection, then opens the production pool by path. The check only uses `os.Stat` (which follows symlinks) and does not verify ownership/mode, acquire an exclusive startup lock, or otherwise pin the file identity. If an untrusted local principal can replace the file/symlink or alter its schema between preflight and pool creation, the production hook does not re-run inventory validation; it only checks WAL and connection PRAGMAs. Yet production `Open` sets `trustedTrigger: true`, and the authorizer trusts the trigger name `pilot_allocation_capacity_after_insert`. A replacement database can provide a trigger with that name on an alias table and mutate protected quota counters during an allowed alias insert. Keep readiness/capabilities disabled until the caller proves an owner-only, non-replaceable database path under the singleton writer lock, or bind preflight to the same protected file instance and revalidate before enabling the trusted-trigger exception. Add a deterministic test that swaps/replaces the database after preflight and proves no capability is admitted.

This is conditional on the deployment trust boundary: if an independently enforced OS policy guarantees the DB and parent directory cannot be modified/replaced by any untrusted principal for the entire process lifetime, record and test that guarantee as an A0 prerequisite. The adapter itself currently does not enforce it.

## Verification limits

The source statically shows read-only preflight, view inventory, exact object hashes/count, metadata/version checks, WAL readback, per-connection authorizer installation, and capability clearing. Tests were not executed; CGO driver compatibility, lock/race behavior, and the file-replacement scenario remain runtime-unverified.
