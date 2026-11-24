# Security Review 7: `lesson.start` Persistence Kernel SDD

Read-only review of the latest `docs/41-试点课堂Start持久迁移内核SDD.md`, against findings PSK-SEC6-01/02 and the newly added plan/task/trace HMAC fields. No code or design was changed and no tests or runtime checks were run.

## Decision

**BLOCK pending one specification consistency fix.** The new §3 threat boundary, filesystem controls, key lifecycle, and artifact-retention rules close PSK-SEC6-01/02 in substance. Receipt-first recovery is explicitly limited to structural recovery within the trusted local-account boundary, rather than claiming protection from that account or administrators. However, §3 requires key delivery only by protected inherited handle/anonymous pipe and forbids argv/environment, while the later Playwright harness contract still says the child is started “通过私有运行参数传递” (through private runtime parameters). That wording can be read as passing the HMAC key in process arguments and leaves two conflicting implementation contracts.

## Round-6 finding dispositions

- **PSK-SEC6-01 — closed except for the §6 harness wording conflict.** §3 now specifies owner-only Windows DACL/POSIX `0700`/`0600` plus `umask 077`, checks permissions before each open, rejects wrong owners, extra ACEs, reparse/symlink/hard-link and directory replacement, uses directory-handle-relative opens, and requires atomic `CREATE_NEW`/`O_EXCL` key creation. It restricts delivery to a protected inherited handle/anonymous pipe and excludes argv, environment, logs, and dumps. It expressly limits the threat model to one trusted supervisor OS account and excludes compromise of that account/admin/kernel/host. This is a coherent boundary for a test-only harness. §3 further limits receipt-first to structural consistency in that boundary and disclaims resistance to same-account/admin DB tampering. **Remaining:** §6 must replace “private runtime parameters” with the same inherited-handle/pipe contract and explicitly say the key itself is never a runtime argument.
- **PSK-SEC6-02 — closed in the SDD.** Only synthetic student data is allowed. Successful cases are removed after receipt verification and browser evidence persistence; `terminal_failed` cases are retained at most 24 hours after review evidence is persisted; `pending_unknown`/`sidecar_degraded` cases are supervisor-only and retained at most 7 days before explicit destruction/recreation. Cleanup closes connections and removes DB, WAL/SHM/journal, sidecar, and key together; failure isolates the case and prevents reuse. These rules address access, retention, and cleanup, assuming implementation enforces them.

## New HMAC and privacy review

- `planning_fingerprint` is not written to sidecar/logs; `command_hmac` binds it. `plan_hmac` binds the final command fingerprint, template ID/source, and fixed say/show-word sequence without storing the greeting or word payload in the sidecar. `bundle_hmac` binds the plan, final fingerprint, and bundle digest. Domain tags separate attempt key, attempt, plan, bundle, task, and trace uses.
- `task_hmac` and `trace_hmac` are deterministic under the per-case key and therefore intentionally link repeated events for the same task/trace within that case. They do not reveal the source IDs without the key, and the per-case artifact/key lifetime is bounded. Avoid reusing a key across cases; the SDD's case-scoped key setup implies this but should be stated as an explicit invariant.
- **PSK-SEC7-01 (P2, consistency):** §4 says every event carries task/trace HMACs derived from both the verified capability and Registry `AuditEntry`, but `tool_started` must be durably written before invoking Registry, when that result entry does not yet exist. Clarify that `tool_started` derives task HMAC from the verified capability and trace HMAC from the server-created decision ID, then `tool_result` verifies the same values against the returned `AuditEntry`. This avoids an impossible ordering or accidentally executing Registry before its durable intent record.
- The updated §4 golden event now includes the added required fields, resolving the prior allowlist/golden mismatch. The log contract still keeps raw task/trace/decision IDs and fingerprints out of ordinary logs. The DB fingerprint and persisted outbox remain sensitive to direct DB readers; the SDD correctly treats the fingerprint as structural integrity metadata, not confidentiality.

## Other boundary checks

Receipt replay requires the active authenticated owner and validates the complete DB lifecycle; skipping capability expiry/current nickname is limited to returning the already committed receipt without Planner/Registry work. A damaged sidecar with a valid receipt quarantines the entire case and blocks new commands; without a receipt, corruption fails closed. The production route/build separation remains specified but was not inspected in a built artifact.

## Required revisions and limits

1. Replace §6 “private runtime parameters” with protected inherited-handle/anonymous-pipe delivery and explicitly forbid passing the key in argv/environment.
2. Define `tool_started` task/trace HMAC derivation before Registry, and verify those HMACs against the resulting audit entry in `tool_result`.
3. State explicitly that each isolated case gets a fresh independent HMAC key.

No tests, filesystem permission checks, key-delivery checks, build-artifact inspection, database access, or recovery execution were performed. The review assesses the written design only; it does not validate implementation or classroom readiness.
