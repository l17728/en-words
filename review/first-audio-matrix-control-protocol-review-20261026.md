# First-Audio Matrix Control Protocol Review: 2026-10-26

## Reviewed artifact

- Specification: `docs/73-first-audio-matrix-control-protocol-sdd.md`
- SHA-256: `A1984FFAD6C8BF3B8940DA9B8CD8B1C08A37077842CE3BD01164509ACE3041BF`
- Scope: dedicated Go test-binary JSONL control protocol, isolated fixture/database startup, guarded SQLite pool, idempotent reset, terminal failure and child lifecycle.

## Domain decisions

| Domain | Result | Review scope |
| --- | --- | --- |
| Architecture | PASS | Lifecycle transitions and shutdown precedence; fixture/trigger/authorizer order; replay before current-lesson checks; dedicated test binary and process cleanup. |
| Backend | PASS | Setup and guarded pool connection hooks; replay semantics after lesson replacement; exact harness entry, stdout trailer, and process-tree reaping. |
| Security | PASS | Environment allowlist and explicit harness gate; capability isolation; private canonical database path; immutable authorizer; failure cleanup. |

All reviewers checked the exact SHA-256 above. Two protocol ambiguities were found and closed: the explicit environment gate is allowlisted as `EN_WORDS_FIRST_AUDIO_MATRIX_HARNESS=1`, and `shutdown_failed` is part of the fixed error-code map only for cleanup failure during a successful shutdown. Reviewers returned PASS with no P0/P1/P2 findings.

## Decision and remaining gates

The protocol SDD is approved for implementation. This approval does not establish reset correctness, SQLite guard coverage, E2E journey correctness, or performance. The 30-cold/100-warm matrix remains **NOT RUN** until its independent unit/integration, Playwright, database-baseline, and latency gates pass. Real classroom pilot and acoustic onset remain separate.

## Control-protocol implementation checkpoint

The implementation review below covers only the isolated parent/child control boundary and its fail-closed placeholder backend. Architecture and security reviewers returned **PASS**, with no remaining P0/P1/P2 findings in this scope. Both reviewed the current implementation after the fixes for command-specific error mapping, terminal shutdown cleanup, Windows ACL verification, and truthful E2E manifest status.

| Artifact | SHA-256 |
| --- | --- |
| `review/first_audio_matrix_control.py` | `DD2D0C89A643A7956EA3BAAFF3855E9EB4994108E92CDC91B4159B38D023B01B` |
| `review/test_first_audio_matrix_control.py` | `4A28C988C85FBC4BADFF16B5D47724E63B7636E9F300F74DD04EC44363C724D3` |
| `server/internal/api/first_audio_matrix_harness_test.go` | `FC4F591F26C88A39869DA2FF88AB2BBF73797870C8E934DF14FE7EBE1B84B012` |
| `server/internal/api/first_audio_matrix_security_windows_test.go` | `F96FDABDD0E20DB9C6E181C6ABE8D91E5C742F3FB3D7DB3B8AD8ABBDE16E60DE` |
| `.github/workflows/first-audio-matrix-protocol.yml` | `64E37A2EEE08E95374B0D7E04BCD837284BFE383F03562C93731FC5F2E5A9273` |

The parent helper suite passed **10/10** on 2026-10-26 (0.320 seconds). GitHub Actions caught and closed a duplicate Go identifier, a missing Unix compile stub, and delayed command-argument validation. Linux Go/production-symbol checks passed on commits `ee3ad5f`, `46e22dc`, and `e7e27d5`. Windows Go exposed an invalid `LocalFree` on Go-owned security-descriptor copies; that ownership error is fixed, and the isolated ACL test passed locally. Windows Actions then exposed an extended-path identity issue. The current Go hash uses `os.SameFile` on existing directories for identity checks and keeps drive/UNC prefix normalization only in explicit tests. The latest targeted and two-platform runs are pending; no final Windows Go pass is claimed yet. The Go harness still uses `unavailableFirstAudioBackend`, so `bind` fails closed. The dedicated Playwright journey is absent; generic E2E files deliberately produce `browser_journey_status=NOT_IMPLEMENTED`. SQLite fixture/guard implementation, browser journeys, latency matrix, and real-classroom pilot remain **NOT RUN**.
