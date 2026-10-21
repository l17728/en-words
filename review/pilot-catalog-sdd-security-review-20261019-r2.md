# Pilot Catalog SDD Security Review — Round 2

**Decision: PASS (design review; no remaining §109 security blocker identified).**

Scope: read-only review of the revised §109 in `docs/41-试点课堂Start持久迁移内核SDD.md`, cross-checked against §§42 and 108. This is a design review only. No code was changed and no tests or runtime checks were performed.

## Prior finding closure

The prior P1 is closed. The revised protocol no longer asks restart to infer whether a complete frame was acknowledged by a previous HTTP caller. A complete, valid frame is authoritative; a partial or invalid frame fails closed. The caller converges by reading catalog state. This is observable and implementable without an acknowledgment bit. A write or required Sync error still poisons the current supervisor instance globally, preventing further success responses or opens in that process. These rules are consistent: restart replays durable bytes and independently enforces the state-specific recovery restrictions.

Crash cases remain conservative. If the `open_recovery_required` frame survives, fresh commands remain denied pending independently verified disposition. If a final disposition frame survives, catalog replay may authorize only the operations that disposition permits; receipt reads/recovery still require the independent DB/sidecar checks in §§42/109.2. If that frame does not survive, the preceding open marker keeps the case unresolved. Partial frames fail globally. No path infers commit from an in-memory result.

## State and authorization review

The `ready → cleanup_quarantined` addition closes the previously missing never-started cleanup path. Its stated preconditions—holding the per-case lifecycle lock and proving no start attempt or lesson-state write—avoid using the transition to erase an active or unresolved lesson. Provisioning failures still pass through quarantine; cleanup then proceeds through a durable cleanup claim and permanent deleted tombstone. This follows §108’s prohibition on fresh work after uncertainty and §42’s case-lock-before-catalog-lock ordering.

The fixed-schema catalog remains a bounded authorization journal under the verified owner-only handle and trusted local supervisor boundary. CRC is correctly described as corruption detection rather than authenticity. Global contiguous generations, exact canonical framing, no compaction, and permanent tombstones make replay deterministic and prevent silent case-ID reuse. Two-phase authorization (catalog check, case lock, catalog re-read, then verified open) is compatible with the cross-process lock contract. No authorization bypass, replay ambiguity, or lock-order deadlock is apparent in the revised text.

## Implementation conditions (not design blockers)

- Enforce file-size and frame-count limits before append as well as during replay, and poison globally on exhaustion or uncertain durability.
- Keep catalog validation and the second authorization read ahead of every DB, sidecar, or key open; preserve the case lock through handle closure and cleanup/tombstone durability as required by §§42/56.
- On `ready` cleanup, make the “no start attempt or sidecar/DB lesson write” proof explicit in implementation tests so initial empty-file provisioning is distinguished from lesson-state mutation.
- Complete the separately stated POSIX/Windows ACL, stable identity, file/parent Sync, and cross-process lock acceptance gates. The SDD explicitly says the supervisor/catalog implementation is not present; this review does not establish those controls in code.

No §109 design blocker remains. The listed items are implementation and acceptance gates, not reasons to hold the design.
