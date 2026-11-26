# Security Review 4: `lesson.start` Persistence Kernel SDD

Read-only review of current `docs/41-试点课堂Start持久迁移内核SDD.md`, following round 3. No implementation was changed and no tests or runtime checks were run.

## Decision

**The three round-3 conditions are closed in the design text. Approve the narrow test-only Start design with three remaining non-blocking implementation conditions.** No new P1 blocker is apparent. This is not runtime validation or approval to open a pilot.

## Round-3 condition closure

- **Capability expiry — closed.** §3 now requires every commit and receipt-read invocation to revalidate that the lesson-bound capability has not expired, as well as current owner, account status and reservation.
- **Frozen show-word binding — closed.** §§3–4 require the planned `word_id` and displayed word to match the exact lesson's `frozen_words`, with the store repeating the check before writing the receipt/outbox. It also forbids filling from the live word list.
- **Panic boundary — closed as a design contract.** §3 requires rollback on executor panic/error, then a fixed redacted HTTP failure and no panic value/stack in errors or logs. The executor cannot continue or publish success.

## No regression check

- Owner/task authorization and receipt replay remain bound to `VerifiedPilotStartActor`; current status, expiry, owner, task scope and reservation are rechecked before commit/read. Receipt replay compares the full fingerprint and does not execute tools/effects.
- Harness remains a separate `pilot_e2e` artifact absent from the default server dependency graph, with temporary DB, loopback ephemeral port, random in-process token and production-route absence assertion.
- COMMIT uncertainty still produces `pending_unknown` on absent/unavailable receipt, never infers rollback, and only permits same-ID/fingerprint/version/generation re-entry; `not_committed` requires proven rollback.
- Task-local audit and fixed `say`/`show_word` allowlists remain in force. `audit_logs.Args` now contains only byte count plus fixed schema/scope/effect-index metadata; no arbitrary tool args are persisted. The executor remains typed, test-only, bounded, without DB access, and constrained against external side effects.

## Remaining non-blocking conditions

1. **P2 — Windows reparse-point validation remains unspecified.** The harness path contract still rejects symlinks but does not explicitly reject junctions or other reparse points. Canonicalize the private temp directory and DB path and reject all reparse-point components before opening SQLite.
2. **P2 — Persisted SHA-256 fingerprint exposure remains unspecified.** The database receipt allowlist still stores the candidate-derived fingerprint, whose inputs include low-entropy `say` text and a word. The document excludes it from ordinary logs but does not define whether `StartReceipt` or `lookupPilotStartReceipt` returns it. Prefer a public receipt without the digest; otherwise use keyed protection across trust boundaries and explicitly treat candidate content as non-secret. A hash is not redaction.
3. **P2 — Add an explicit executor-panic fault-injection case.** The contract now says panic rolls back and the HTTP boundary redacts output, but the listed Go cases inject write/COMMIT failures and the E2E cases interrupt pre/post commit; neither explicitly panics from each tool handler. Add a deterministic panic injection that proves no checkpoint/audit/receipt/outbox row commits, the task-local auditor buffer is discarded, the pinned connection is safely rolled back or discarded, and HTTP/log output contains neither panic value nor stack. Do not recover inside the transaction and continue.

## Limitations

This review checks design text only. No tests, path/reparse-point checks, build-artifact inspection, panic injection, or runtime authorization checks were performed. The approval covers only the isolated test-only Start slice; it does not establish implementation correctness or classroom readiness.
