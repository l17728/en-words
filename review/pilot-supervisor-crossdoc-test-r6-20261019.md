# Pilot supervisor cross-document test-spec review, revision 6

- **Requested docs/41 SHA-256:** `7890fb542818c77f131cd06d09845120406eb7c1567af1bdfa83c92fa51d3867` — matched.
- **docs/42 SHA-256:** `aef772e4d71633690e4e3f4c7498837552088d5bd385af1875de820e1b9aa447` — matched the final review target.
- **Verdict:** PASS, no open P1/P2.
- **Scope:** Read-only cross-document review of docs/41 §112 and current docs/42 close adapter, DB counters, FI rows, and inherited R4 gates. No tests were run and no design/code was changed.

## R5 and cross-document checks

| Area | Status | Evidence |
|---|---|---|
| Close adapter outcome tuple | **PASS** | §9 defines exactly three valid tuples: `{closed,nil}`, `{still_open,non-nil}`, `{unknown,non-nil}`. Every other pair, including `closed+err`, maps to unknown/`ErrInvalidCloseOutcome`; §3 maps unknown/invalid outcomes to `restart_required`. This closes R5-N1 and gives a deterministic table-driven adapter test oracle. |
| FI close rows | **PASS** | FI-CLOSE-01a..g distinguish each owned resource and state-specific behavior: closed advances, still_open retries only through its owner, unknown stops same-process close, holds the lock, and requires bounded child exit/restart. DB/driver uncertainty maps to unknown. |
| Database counters | **PASS** | §10 defines `db_open_rw`, `db_open_ro_recovery`, and `db_application_sql`, including when PRAGMA/path mismatch increments each. FI rows use these to separate application access from allowed read-only recovery. |
| State matrix and file operations | **PASS** | §7/§8/§9 define the initial bootstrap exception, case-lock inventory, per-state capabilities, exact fixed-member allowlist, verified relative-handle operations, and close/delete ordering. `docs/41` §109.4/§152 covers recovery lock convergence and state transitions. |
| Provisioning, cleanup and boundaries | **PASS** | DB create/write/Sync/identity stages are split; sidecar/key and each cleanup member have explicit outcomes. Cleanup transition distinguishes zero-byte, complete, and partial frames. Catalog generation, frame/payload/file/count limits, malformed boundaries and golden vectors are specified in `docs/41` §109.4/§178 and mirrored by docs/42 FI cases. Generation zero is only an absent-chain sentinel; persisted records begin at one. |
| docs/41 §112 alignment | **PASS** | §112 limits SQLite verification to canonical-path comparison under a protected-directory/same-account-excluded threat boundary and keeps the HMAC key within the trusted supervisor. docs/42 §2/§6 mirrors both corrections and does not claim handle binding or child key delivery. |
| Multi-process and E2E | **PASS** | C1–C5 provide named child-process schedules and lock/revalidation/termination assertions. `docs/41` §109.4/§180–182 adds writer/reader, process-kill, same-case winner and provisioning convergence tests. docs/42 E2E-PILOT-01..07 maps those invariants to the later loopback/Playwright journeys. |
| Inherited R4 gates | **PASS** | Linux/Windows native evidence, coverage, structured logs and negative secret scans remain in the reviewed text. |

The requested docs/41 and docs/42 hashes both matched. This PASS concerns the design/test specification only; it does not claim that implementation, platform, Go, coverage, or Playwright tests have been executed.
