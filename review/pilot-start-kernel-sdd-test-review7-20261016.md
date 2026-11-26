# Pilot `lesson.start` Kernel SDD — Seventh Test-Domain Review

**Result: BLOCK.** This is a read-only review of the current SDD. No code or design files were changed and no tests were run.

## Prior review findings

- **Crash-state coverage — substantially closed.** The new recovery table covers intent sync, planner/tool stages, bundle/cache, writer start, commit, sidecar terminal sync, malformed/truncated sidecar, and process restart. It specifies conservative `pending_unknown`, receipt-first recovery, and `not_committed` only from durable `terminal_failed`/`rollback_proven` evidence.
- **Planner/Bank failures — closed at the test-matrix level.** Agent API construction failure, missing OP-1 fallback, failed Registry calls, budget denial, panic, sidecar sync failure, and restart behavior are now called out.
- **Cross-lesson verifier — closed at the stated contract level.** The SDD separates global integrity from per-lesson lifecycle checks, allows mixed created/active lessons, and explicitly calls for cross-lesson/student/decision/effect-index corruption injection and orphan/duplicate checks.
- **Playwright surface — closed.** A test-only HTML/JS harness page is now specified, with separate production-route absence checks, sidecar-preserving child restart, pending-unknown UI, post-COMMIT response loss, and T-01/fallback assertions.

## Remaining blockers

1. **P1 — `attempt_started` cannot yet carry the specified semantic `command_hmac`.** Sidecar `command_hmac` is defined over the canonical fingerprint, which includes rendered say text and frozen show-word data. The strict sequence requires `attempt_started` to be flushed before the Planner/Bank API runs, and that event schema requires a command HMAC field. The SDD does not define whether that field is null/omitted for `attempt_started`, how it becomes bound later without mutating append-only history, or how recovery compares a retry before rerunning Planner. Define a two-stage binding protocol (for example, key-only intent followed by a signed semantic-bind event) or compute/freeze all fingerprint inputs before intent without violating the “intent before Planner” rule; add crash and changed-fingerprint tests at both stages.

2. **P1 — The sidecar chain has no cross-command append serialization contract.** The chain uses `event_seq` and `prev_event_hmac` over JSONL order, but only per-lesson single-flight is specified. Two different lessons can append concurrently through the shared case-level sidecar, racing sequence allocation, HMAC chaining, writes, and `Flush+Sync`. Specify a sidecar-wide mutex/file lock and global sequence/chain ownership, then test concurrent A/B lessons with interleaved tool events and restart verification. Per-lesson locking does not protect the shared append-only chain.

3. **P2 — Sidecar HMAC canonicalization and result-class enums are still not fully executable.** `event_hmac` covers “canonical JSON fields,” but the sidecar’s exact byte encoding/key order/escaping/null representation is not defined by the command fingerprint rules. Also, `result_class` is described as a stable category but its allowed values are not enumerated. Freeze the sidecar serialization and result-class enum, and provide a byte/hash golden vector plus a vector for each terminal/failure class.

## Disposition

The latest design now has a useful crash matrix and closes the earlier lifecycle, Agent/Bank failure, cross-lesson, and browser-surface gaps. Do not implement until the attempt-intent HMAC binding and sidecar-wide serialization issues are resolved; both affect whether recovery/audit remains trustworthy under the exact concurrency and crash conditions this slice is meant to validate.
