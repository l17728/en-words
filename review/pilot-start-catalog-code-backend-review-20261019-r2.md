# Pilot catalog backend review — 2026-10-19, round 2

**Verdict: PASS WITH ONE P2 IMPLEMENTATION GAP.** The previous blockers for limit poisoning, explicit shared health, waiter rechecks, unlock-window poisoning, canonical JSON rejection, and cross-process platform locking are closed by the current slice. The catalog implementation remains gated on full supervisor integration and execution of the tagged tests on both Windows and POSIX CI.

## Previous findings

| Finding | Status | Evidence |
|---|---|---|
| Frame/byte limit did not poison shared supervisor | **Closed** | `Transition` poisons health for frame and byte caps (`pilot_start_catalog.go:238-259`); frame-cap test verifies subsequent Snapshot is denied (`pilot_start_catalog_test.go:208-232`). Byte-cap append is rejected before seek/write. |
| Health could be omitted or checked only before lock | **Closed** | Both constructors require non-nil health and check it after acquiring the catalog lock (`pilot_start_catalog.go:100-122, 140-155`); Snapshot and Transition recheck after lock (`187-203, 238-250`). |
| Sync failure did not affect sibling handles / queued waiter | **Closed** | Shared Sync failure test denies sibling Snapshot and Transition (`pilot_start_catalog_test.go:500-526`); waiter barrier holds writer A in Sync, queues B, and confirms B cannot append after A poisons (`528-568`). |
| Startup/Snapshot failure could expose an unlock window before poison | **Closed** | Both poison before releasing the OS lock; the two-mode barrier test checks a sibling transition while the failing operation is paused after OS unlock but before return (`571-619`). |
| Noncanonical JSON with valid frame checksum could pass | **Closed** | Decoder requires audit JSON parse and byte-for-byte canonical marshal; negative vectors cover field order, whitespace, unknown/duplicate fields, and trailing object (`pilot_start_catalog.go:392-418`, test `152-170`). |
| Only same-process lock behavior was exercised | **Closed in test design** | The self-spawned child-process test uses a separately opened lock file and proves the child cannot enter until the parent releases (`pilot_start_catalog_test.go:720-780`). Unix uses `flock`; Windows uses exclusive `LockFileEx` (`pilot_start_catalog_lock_unix.go:1-43`, `pilot_start_catalog_lock_windows.go:1-43`). This is not runtime-verified here; run the build-tagged test on both OS families before integration acceptance. |

## Remaining finding

**P2 — failed lock acquisition can poison health concurrently with an already-authorized writer.** `Lock()` failures poison shared health immediately in startup/create/Snapshot/Transition (`pilot_start_catalog.go:105-107, 143-145, 189-191, 240-242`). Those failures occur without owning the catalog lock. A separate local operation may already hold the OS lock, pass its under-lock health check, and continue a write while another caller experiences a lock-acquisition error and sets `health.poisoned`. The waiter tests cover a caller queued behind a writer that poisons during Sync, but do not cover this out-of-lock poison race. This is a narrow fail-closed guarantee gap rather than a generation-chain race: the active writer still owns the global OS lock.

**Recommendation:** either make all local journal operations participate in a supervisor-wide operation gate that serializes health poisoning with the full critical section, or document that a lock-acquisition failure only prevents subsequent operations and that an operation already holding the catalog lock may finish. Given §109's global fail-closed wording, the operation gate plus a deterministic injected `Lock` failure while a second writer is paused after its health recheck is the stronger match.

## Scope and implementation gates

The frame format, contiguous generation replay, per-case state validation, short-write/Sync/unlock poisoning, startup sync order, and fixed-size append checks are internally consistent with the catalog contract in `docs/41-试点课堂Start持久迁移内核SDD.md` §109. No test was run for this review. The OS lock child-process test is time-bounded polling (150 ms exclusion window); it is useful evidence but must be run on Windows and POSIX CI. The production supervisor still owns verified-handle identity, protected lock-file creation/ACLs, bootstrap ordering, and correct sharing of one `pilotCatalogHealth`; this slice explicitly does not implement those guarantees.
