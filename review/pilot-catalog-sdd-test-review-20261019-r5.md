# Pilot Catalog SDD Test Review — R5

- **Date:** 2026-10-19
- **Document:** `docs/41-试点课堂Start持久迁移内核SDD.md`
- **Scope:** §109.3–§109.4 catalog acceptance matrix, deterministic fault injection, and multi-process race oracles
- **Verdict:** **PASS**
- **Tests run:** None; this is a read-only test-design review.

## Findings

The r4 nonblocking gap is closed. Same-case Start contention now gates the lock race on both child supervisors independently observing optimistic `ready` snapshots, then requires the loser to re-read under the stable case lock and return before any DB, sidecar, key, Planner, or Registry side effect. This gives a deterministic stale-read oracle without timing sleeps.

Concurrent provisioning recovery now requires both processes to verify the same stable lock identity and complete initial members, then asserts exactly one `provisioning → ready` frame and generation. The other process must observe `ready` without a duplicate transition. This closes the remaining P3 from r4.

The acceptance matrix also specifies restart outcomes for catalog creation and frame crash boundaries, zero/partial/full writes, file and parent-directory Sync failures, replay and identity failures, capacity boundaries, cleanup recovery, response loss, and cross-case writer/reader races. Golden vectors use test-only fixed IDs and keys; production identifiers remain CSPRNG-generated.

No new test-design blocker was found in the reviewed sections. Windows and POSIX verified-handle, ACL/mode, file-identity, lock-release, and parent-directory durability checks remain explicit platform acceptance gates; passing this document review does not claim those implementation gates have run.

## Closed review item

- `CAT-TEST-1094-05` (r4 P3): concurrent provisioning recovery must prove one `provisioning → ready` append/generation and no duplicate transition by the observer — **CLOSED**.

## Reviewed coverage

- Deterministic two-stage barriers for same-case stale-read/Start contention.
- Concurrent provisioning recovery and unique state transition/generation.
- Child-process crash/restart oracles and no-side-effect open counters.
- Framing/schema boundaries, journal size/frame/generation limits, and write/Sync fault outcomes.
- Response-loss convergence, cleanup resume, and fixed test-only golden vectors.

