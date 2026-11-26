# First-audio schema/read-manifest consistency implementation review — 2026-10-27

## Scope

This review covers the setup-pool check that links the pinned SQLite schema to the authorizer's table/column read manifest. It does not cover row-guard UDF policy, audit trigger execution, the real isolated backend, reset, or classroom journeys.

## Implementation

`firstAudioReadPhysicalColumnManifest` reads each physical table with setup-pool `PRAGMA table_xinfo`, adding SQLite's fixed `sqlite_master` catalog columns explicitly. It keeps every returned column, including hidden/generated columns. `firstAudioCheckReadManifestColumns` compares exact table and physical-column sets, checks the empty `COUNT(*)` column sentinel separately, and sorts diagnostics. The schema digest and this check both run before the setup pool closes or the guarded pool/NewApp is created. No runtime PRAGMA authorization was added.

Regression cases cover a full positive equality, physical columns omitted from the allowlist, allowlist columns absent from SQLite, an absent count sentinel, an unmodeled physical table, and an allowlist table absent from the physical schema.

## Evidence and review

- CI [40459009208](https://github.com/l17728/en-words/actions/runs/40459009208), commit `0a4b4d09f01ab2425c7ae26a0fb75c49e259fd8e`: Linux and Windows CGO jobs passed; Linux formatting, vet, and production-symbol exclusion passed.
- Focused Windows Go 1.25/CGO test: `go test ./internal/api -run '^TestFirstAudioMatrix(ReadManifest|SchemaManifest|NewAppStartsOnGuardedSQLitePool)' -count=1 -v` — passed.
- `go vet ./internal/api` — passed.
- Architecture, security, and regression reviewers: PASS; no outstanding findings for this slice.
- Schema test SHA-256: `6DD239EBB0ACF0F576745D9264F3FF76354DAF9353D2D624330F38CF39BDBD38`.
- Startup test SHA-256: `E46CA23A428DF5091710E1B31D870BE0F7D50505CD72037003373FAB5F34ABE2`.

## Remaining work

The exact `fa_audit_no_replace` authorizer exception still needs implementation and execution tests. The lesson/runtime/audit row guards, immutable context, real Bind/Close backend, reset transaction/fencing, and sequential two-journey Playwright acceptance remain open. The isolated matrix is **NOT ACCEPTED**.
