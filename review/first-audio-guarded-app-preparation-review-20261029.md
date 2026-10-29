# First-Audio Guarded App Preparation SDD Review

Date: 2026-10-29

## Architecture — PASS

The slice follows docs/74 initialization order: only guarded `gorm.Open` runs in DRIVER_PROBE; admission drains and STARTUP_PROBE publishes before `NewApp`; PREFLIGHT follows startup verification and three simultaneous physical-connection probes. It stays outside protocol bind, listener creation, JOURNEY, and classroom acceptance.

## Security — PASS

Run root, capability, canonical DB containment, exclusive no-follow creation, and sidecars are rechecked before SQLite opens. Logs exclude capability, password, raw path, SQL, and row contents. Startup failure inspects raw driver autocommit without SQL; only confirmed autocommit permits snapshot reads. Active/unknown transaction state issues no further SQL, terminalizes and retires the exact raw connection; uncertain retirement requires parent kill/reap. The PREFLIGHT rollback permit is never used in STARTUP_PROBE.

## Test quality — PASS

The SDD pins pool capacity at three and requires simultaneous distinct connections and one successful receipt per identity. It covers fixture preseed (including both agents), startup snapshot equality, injected errors/panics, lifecycle and cleanup failures, listener count zero, and no JOURNEY publication. The dependency seams and sanitized outcomes are concrete enough to implement.

## Decision and limits

Approved for implementation. This review approves only the test-binary startup/PREFLIGHT preparation slice. It does not approve listener bind, browser journey, reset, performance, or classroom acceptance.

## Implementation review update (2026-10-29)

- Architecture: PASS. The owner receipt is emitted only after the real `NewApp` startup checks and complete snapshot equality; the exclusive PREPARE phase and exact pinned-connection publication remain within the reviewed boundary. No listener or JOURNEY is introduced.
- Backend: PASS, no P1/P2. Review covered real `NewApp`/StartupError, driver autocommit and balanced transaction counters, full snapshot comparison, closed setup pool, pool size 1 through startup then 3 under PREPARE, simultaneous pins/provenance, and fail-closed cleanup/physical retirement.
- Security: PASS, no P1/P2. Receipt consumption is one-use and context-bound; failed phase/evidence paths terminalize; physical connection IDs and raw identities are validated under the registry lock through publication and arming.
- Test quality: PASS. The missing-startup-receipt test asserts fail-closed terminal behavior and is selected by Linux normal/race and Windows CGO jobs.
- CI: GitHub Actions `40839202122` passed Linux protocol and Windows boundaries at `7ebe6f7244898c7f22036475852a89ca461dce0a`.

This update accepts only the preparation slice. The full harness, guarded JOURNEY write integration, reset races, two-process Playwright run, performance matrix, and human classroom pilot remain unaccepted.
