# Pilot SQLite Adapter Security Review — Schema Binding Update

Review date: 2026-10-21. Scope: current `server/internal/pilotcontrol/sqlite.go`, `sqlite_test.go`, `sqlite_nocgo.go`, and `schema_inventory_v3.json`. Read-only source review; CGO runtime tests were not run because Go is unavailable on this host.

## Decision: BLOCK on embedded inventory integrity binding

The previous preflight-to-pool race is materially mitigated: each production connection now re-runs the full schema/version/metadata/FK/quick-check inventory on that same SQLite connection before registering the authorizer or enabling `trustedTrigger`. The final path component is checked with `Lstat`, and docs/54 makes Reaper-only database/sidecar directory permissions a readiness precondition. The persistent-view escape is closed by including `view` in `sqlite_master` enumeration and testing rejection of an extra view. Per-connection state is carried by `protectedConn`; the stale test call is fixed.

**BLOCK — approved DDL metadata is not cryptographically bound to the embedded schema inventory.** `Open` verifies that `inventory.DDLManifestSHA256` equals the compiled `approvedDDLManifestSHA256` and that `ExplicitObjectCount == len(Objects)`. It does not verify the bytes/hash of `schema_inventory_v3.json` or compare its 95 object tuples against a separately trusted manifest. Therefore the embedded inventory can be edited to change accepted object names, table names, or SQL hashes while retaining the approved manifest-hash string and count; that modified inventory is then used to authorize schema objects and enable the trusted trigger exception. Add a compiled expected SHA-256 for the embedded inventory bytes (and a test), or embed and verify the actual approved manifest from which the inventory is derived. Refresh that pin only as part of a reviewed manifest update.

The OS ownership rule in docs/54 remains an external A0 readiness condition. The adapter does not itself verify directory ownership/mode or the `-wal`/`-shm` sidecar ACLs, so the service launcher must enforce that contract before publishing readiness.

## Runtime status

The source statically shows read-only WAL/schema preflight, exact persistent-object enumeration including views, and per-physical-connection revalidation before trusted-trigger enablement. `mattn/go-sqlite3` hook behavior, permission enforcement, lock/race behavior, and the CGO test suite remain unverified on this host.
