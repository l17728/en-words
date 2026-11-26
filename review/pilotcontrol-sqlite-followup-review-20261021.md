# Pilot SQLite Adapter Follow-up Review

Review date: 2026-10-21. Read-only review of the revised provisional adapter. CGO runtime testing remains pending because this Windows host has no Go toolchain in PATH.

## Finding

**BLOCK — exact inventory preflight ignores views.** `sqlite.go` preflight line 278 filters `sqlite_master` to `type IN ('table','index','trigger')`. SQLite views have `type='view'`, so an extra persistent view is omitted from `actualCount` and `expected`; a tampered v3 database with an added view can pass the exact-object inventory check. The manifest explicitly rejects views, and SCHEMA-07 requires extra views to be rejected. Include all user object types (or enumerate `view` too) so every unlisted object fails closed, and add a preflight test with a sentinel view.

## Snapshot and other checks

The prior stale test call is corrected. The old WAL-before-preflight and retained-connection-state-map findings appear resolved: preflight is read-only before pool construction, ConnectHook verifies rather than sets WAL, and connector wrapping transfers then deletes per-connection authorizer state. PRAGMA checks, scoped capability clearing/rollback defers, and `!cgo` fail-closed behavior are structurally sound. The workflow runs package tests, race tests, vet, and full backend tests on Linux with CGO, but it cannot catch the omitted-view case until a targeted preflight test is added.

No source code changed during this review.
