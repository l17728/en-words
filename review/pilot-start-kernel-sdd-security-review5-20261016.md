# Security Review 5: `lesson.start` Persistence Kernel SDD

Read-only review of the current `docs/41-试点课堂Start持久迁移内核SDD.md`, including the new T-01 nickname/meaning data and pilot audit mapping. No code or design was changed and no tests or runtime checks were run.

## Decision

**All three round-4 conditions are closed in the SDD. Security approves the narrow isolated test-only Start design with no remaining design blocker.** This does not approve implementation or establish runtime behavior; the document still requires cross-domain review and later test acceptance.

## Round-4 closure

- **Reparse points — closed.** §5 now canonicalizes the harness directory and database path before opening SQLite, checks every path component for Windows `FILE_ATTRIBUTE_REPARSE_POINT`, rejects symlinks/junctions/mount points/other reparse points, and verifies the final DB remains under the newly created private directory.
- **Receipt fingerprint exposure — closed for API/log surfaces.** §4 limits public `StartReceipt` to fixed result and command ID, with no request fingerprint or candidate summary; §6 says the lookup UI must not receive a fingerprint. Ordinary logs exclude the fingerprint and candidate. The database still stores SHA-256 for integrity/idempotency. This digest is not anonymization or encryption: anyone with direct database access can test low-entropy candidates, so DB access must remain protected and the covered content must not be treated as secret because it is hashed.
- **Panic injection — closed in the test plan.** §6 now injects a panic sentinel in each handler and requires no checkpoint/audit/receipt/outbox rows, discard of task-local audit buffer, rollback or safe connection isolation, and a fixed HTTP error with no panic value/stack in response or logs. It forbids recovery inside the transaction followed by continued execution.

## New T-01 and audit-mapping review

- The greeting uses a verified active owner's nickname, normalized by trimming, removing control characters/braces, limiting to 12 runes, and using a fixed fallback. Only the fixed OP-1 template is accepted; non-first lessons are rejected. This avoids exposing prior performance or accepting client-selected text as an internal greeting.
- `classroom.show_word` is tied to the lesson's first frozen word. The command carries the word ID, exact word, and ordered meanings; the store rechecks the exact values against this lesson's `frozen_words` before writes. It does not live-query content or accept arbitrary tool names/fields. Meaning count/size and non-empty constraints are specified.
- Registry audit stays task-local. Raw arguments are reduced to a byte count; the explicit `pilot-audit-map/v1` mapping adds only schema version, fixed `teacher_agent` scope, and effect index. The outbox stores two fixed typed effects in order, with bounded payloads. No raw model output, prompt, student input, credentials or arbitrary tool arguments are persisted.
- `VerifiedPilotStartActor` continues to bind current active student, lesson, task capability and expiry. Commit/replay/receipt lookup revalidate capability expiry, owner, account, nickname and reservation. The test-only task context has a two-call budget, fixed task/trigger, and the tool handlers cannot be selected by HTTP input.

## Remaining boundary

There is **no remaining security design blocker** for this isolated test-only slice. The SHA-256 receipt fingerprint remains a database integrity value, not a secrecy control; implementation must preserve the response/log exclusions and protect the temporary database as stated. No conclusion is made about production principal design, production route availability, old Room integration, outbox consumption, real audio or classroom readiness.

No tests, build-artifact inspection, path manipulation, panic injection or database access tests were performed as part of this review.
