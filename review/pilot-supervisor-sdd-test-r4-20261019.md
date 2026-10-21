# Pilot supervisor SDD test-domain review, revision 4

- **Reviewed document:** `docs/42-Pilot隔离运行监督器SDD.md`
- **SHA-256:** `4775cf8be9d5d69314b01f542ea318e5218a2cb482e244f578f06192766f9352`
- **Verdict:** PASS for the test-design review gate
- **Findings:** 0 open P1/P2
- **Scope:** Read-only review of the requested SHA, especially §§7–13 and the failure/recovery matrix. No implementation tests were run and no design/code files were changed.

## Finding status

| Finding | Status | Evidence |
|---|---|---|
| R3 F1 — crash cuts, inventory and restart oracle | **CLOSED** | §13 now separates bootstrap/catalog, case directory, DB, sidecar, key, ready transition, cleanup transition, each fixed cleanup member, directory removal, tombstone, and each close/lock resource. It defines syscall-error and barrier-kill variants, persisted catalog state/generation/frame bytes, member identity inventory, stable error, poison scope and business counters. `db_open_app` and `db_open_recovery` distinguish pre-effect failures from permitted read-only recovery. |
| R3 F2 — per-operation error result and stable error code | **CLOSED** | The former conditional DB/sidecar/key rows are split by create/write/Sync/identity stages. Explicit no-entry vs uncertain create, short/full-write plus Sync, mismatch, cleanup and close outcomes map to stable codes and restart checks. Close entries now define `closed`/`still_open`/`unknown`, retry ownership, stop order and lock retention. |
| R3 F3 — C1–C5 schedules | **CLOSED** | Named barriers, lock-holder termination, current-state revalidation, unique generations, bounded waits and zero-effect assertions remain specified. |
| R3 F4 — native platform evidence | **CLOSED** | Linux and Windows native execution and evidence requirements remain concrete, with unsupported/skipped security properties treated as failures. |
| R3 F5 — E2E mapping | **CLOSED** | E2E-PILOT-01..07 map later browser journeys and explicitly prohibit treating supervisor tests as classroom acceptance. |
| R3 F6 — coverage/logging | **CLOSED** | Coverage scope, state-decision threshold, platform adapter coverage, structured trace linkage, event counts and sensitive-data scans remain specified. |
| R2 N1 — case ID width | **CLOSED** | The contract consistently defines 16-byte/128-bit IDs encoded as 32 lowercase hex characters. |
| R2 N2 — SQLite identity oracle | **CLOSED** | The contract consistently accepts canonical-path-only verification under the stated trusted-private-directory and same-account-excluded threat boundary; it no longer claims OS-handle identity proof. |

## New contradictions

No new blocking contradiction found in the requested areas. The catalog oracle's generation 0 represents the no-record/absent-catalog sentinel; valid persisted catalog records still start at generation 1 per `docs/41`. Keep that distinction explicit in implementation assertions so generation 0 is never serialized as a catalog frame.

This PASS applies only to the design's testability and acceptance contract at the recorded SHA. The specified Linux/Windows implementation runs, Go tests, coverage thresholds, and Playwright/loopback E2E remain future acceptance work; this review does not mark them executed.
