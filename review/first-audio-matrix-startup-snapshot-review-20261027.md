# First-audio startup snapshot review — 2026-10-27

## Scope

This review covers full database row snapshots immediately before and after guarded `NewApp` startup, SQLite schema/table-set linkage, and cross-driver canonicalization. It does not approve reset, lesson-boundary snapshots, the full guarded backend, or the two-journey Playwright matrix.

## Implementation and evidence

`first_audio_matrix_snapshot_cgo_test.go` snapshots every table named by the checked-in `sqlite_master` manifest, including `sqlite_sequence`. It sorts rows by primary key (or all columns when no primary key exists), records SQLite storage classes and values, compares canonical JSON and SHA-256, and checks snapshot table names against the manifest in both directions. Duplicate specs fail explicitly. `time.Time` is represented as UTC RFC3339Nano.

Regression coverage includes unmodeled table rejection, duplicate spec rejection, UTC normalization, and the guarded startup boundary snapshot. The setup/modernc snapshot is captured before the setup pool closes; the post-startup snapshot is read through the guarded mattn pool after `NewApp` and denied-write probes. The snapshots matched byte-for-byte after canonical encoding.

- CI: [40455978986](https://github.com/l17728/en-words/actions/runs/40455978986), commit `91f6f80cf554cf0faa0548f760584fc4ceea9b87` — Linux and Windows CGO jobs passed. Linux also passed `gofmt`, `go vet`, and production-symbol exclusion.
- Focused CGO test: `go test ./internal/api -run '^TestFirstAudioMatrix(NewAppStartsOnGuardedSQLitePool|Snapshot)' -count=1 -v` — passed with Go 1.25 and CGO enabled.
- Architecture review: PASS for exact table set, duplicate detection, and UTC normalization.
- Regression review: PASS for startup cross-driver digest and canonical JSON equality.
- Snapshot test SHA-256: `ECDC5E4D3853856B1E764B6DFE63B575CD535CE22D3BCCE9518C47D7FF8C2244`.

## Remaining limits

The baseline represents the repository's fresh migrated schema. Legacy upgrades and cross-SQLite-version compatibility remain untested. This snapshot proves only that `NewApp` startup made no database changes under this fixture; it does not prove reset fencing, stale-operation containment, lesson lifecycle invariants, or performance/readiness. The isolated matrix remains **NOT ACCEPTED** until the backend, reset, and sequential two-journey Playwright gates pass.
