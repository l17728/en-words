# Pilot Control Pinned VFS SDD Review Gate (2026-10-21)

## Reviewed snapshot

- SDD: `docs/55-pilot-control-pinned-sqlite-vfs-sdd.md`
- SHA-256: `420280B85A7086623A06FE24FA4E6D21E40802FA1A115B7ECE1765BC021F6DA2`
- Cross-document boundaries checked: docs/43, docs/50 and docs/54.

## Domain decisions

Architecture, backend, security and test-design reviewers all approved this exact SDD snapshot. The review converged the private VFS contract around the pinned SQLite 3.45.1 ABI, a single Reaper-owned main-file creation path, exact URI and flag trace requirements, root-relative no-follow operations, sidecar generation/refcount handling, SQLite-specific rollback-journal lock states, SHM lifecycle locking, deterministic crash/reopen barriers, and per-callback assertions.

The zero-byte obsolete-journal exception is narrow: RESERVED lock only, nonblocking no-follow open, regular-file and owner/mode/link/mount checks, FD-to-name identity comparison, and descriptor retention through unlink and directory sync. Missing-file idempotency and unregistered-nonempty rejection are separate synthetic callback cases. Hot rollback and commit deletion require EXCLUSIVE according to separate pinned pager traces.

## Implementation gate: BLOCKED / NOT RUN

The SDD remains design-only. All five required artifacts are absent, so implementation approval and native VFS acceptance remain blocked:

- `server/internal/pilotcontrol/testdata/sqlite3451-vfs-flags.golden.json`
- `server/internal/pilotcontrol/testdata/sqlite3451-unix-lock-trace.golden.json`
- `server/internal/pilotcontrol/testdata/sqlite3451-shm-lock-trace.golden.json`
- `server/internal/pilotcontrol/testdata/sqlite3451-pager-delete-trace.golden.json`
- `server/internal/pilotcontrol/testdata/vfs-callback-cases.json`

No VFS implementation or product/A0 wiring was approved by this review. Next, build the pinned-source trace/callback harness, generate and review these artifacts, then implement the Linux adapter and run the required native amd64/arm64 ext4/XFS normal, race and coverage matrix. Do not infer PASS from unavailable hosts or missing fixtures; record those rows as NOT RUN.

`git diff --check -- docs/55-pilot-control-pinned-sqlite-vfs-sdd.md` passed. No runtime VFS test was run in this review stage.

## Callback inventory follow-up (2026-10-21)

The callback-case inventory is drafted at `server/internal/pilotcontrol/testdata/vfs-callback-cases.json` (current SHA-256 `6E10FA2DD0F70700C03467C8C9B65340B2B005DAC492F509515402BAF5C156D2`). The SDD now fixes the exact opaque `xFullPathname` token as `operations.sqlite`, requires exact `xOpen`/`pOutFlags` values from the runtime golden, and assigns deterministic injection to the `xSleep` minimum-duration assertion. The inventory adds `xLock`, `xUnlock`, and `xCheckReservedLock` cases after test-design review found their absence. It remains labeled `DESIGN_CASES_NOT_EXECUTED`; no callback runtime test has run.

This callback-coverage correction first changed docs/55 to `1F0890E6AF26168884549696A37B8F2AB0C18EDB39C4DBC123DD5273FC90B81B`; architecture review then identified missing syscall-audit, sidecar `pOutFlags`, and delete `syncDir` assertions. The current docs/55 hash is `4386D1F6F3E272A1D0DB213CA81708767E89FD471FACB12E91BAF3D58B883451`, and current callback manifest hash is `909912BF194867BC535021EEC15D32ECFD5AD14E2BE80232A2FE51AFCEBC2F42`. The inventory now asserts rootFD-relative/no-cwd/no-default-VFS operations, golden-bound `pOutFlags` for all main/WAL/journal open cases, and `syncDir` false/true plus `SQLITE_IOERR_DIR_FSYNC` fencing. Fresh exact-hash architecture and test-design review is pending; prior approvals apply only to earlier snapshots. The four runtime-derived SQLite goldens remain absent. No VFS implementation or product/A0 wiring is authorized by this follow-up.

### Exact inventory architecture re-review

After review found three remaining matrix gaps, the callback manifest added successful WAL creation, JOURNAL existing-open, and JOURNAL EEXIST/same-inode reopen rows with exact pinned-golden `pOutFlags` assertions. The syscall audit now explicitly includes the unregistered zero-byte obsolete-journal `openat2` path. Current manifest SHA-256 is `BF5214C06F709C6FBD92C4D09E09B6DC3513327EA96F1CFE18EE3587762EF152` (61 unique cases), still bound to docs/55 SHA-256 `4386D1F6F3E272A1D0DB213CA81708767E89FD471FACB12E91BAF3D58B883451`. Architecture re-review: **PASS for this exact-hash design inventory only**; no VFS implementation or product wiring approval. Test-design re-review and the four runtime-generated SQLite goldens remain pending.

Test-design re-review of the same exact hashes: **PASS**. The JSON parses, all 61 IDs are unique, all rows have the required schema, and the four additions above close the prior omissions. The reviewer noted only that the aggregate syscall audit row's `SQLITE_OK` could be read as the result for each listed operation despite also including error paths; per-error callback rows separately assert failure results. No execution claim was made.
