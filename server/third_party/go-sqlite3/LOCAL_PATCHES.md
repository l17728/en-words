# Local changes

This is the upstream `github.com/mattn/go-sqlite3` v1.14.22 source, retained
under its upstream MIT license. The local patch adds
`RegisterAuthorizerWithTriggerName` and passes SQLite's fifth authorizer
callback argument (trigger/view name) through the cgo trampoline. The original
`RegisterAuthorizer` API remains source-compatible.

Pilot Control uses the trigger name to permit only the reviewed capacity
trigger's derived writes. Without this argument the upstream callback reports
the database name in its last exposed string and cannot distinguish trigger
writes from direct SQL. Keep the fork version pinned; review and test any
upstream update against the in-capability negative-write regression in
`internal/pilotcontrol/sqlite_test.go`.
