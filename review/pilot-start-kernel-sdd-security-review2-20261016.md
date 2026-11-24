# Security Review 2: `lesson.start` Persistence Kernel SDD

Target: revised `docs/41-试点课堂Start持久迁移内核SDD.md`. This review compares the revised text against all five findings in the first security review and additionally checks the `PilotStartExecutor` capability boundary. It is read-only; no code or documentation was changed and no tests/runtime checks were run.

## Decision

**Security review approves the revised SDD for the narrow test-only Start slice, with two non-blocking implementation conditions below.** All three first-round P1 blockers are resolved in the design text. The first-round P2 issues are materially addressed; one residual fingerprint confidentiality point and executor wiring condition should be carried into implementation review. This is not implementation approval by other domains and does not establish runtime safety or classroom readiness.

## First-round findings

| Finding | Disposition | Review |
|---|---|---|
| PSK-SEC-01 — Principal and receipt-read authorization | **Approve** | §3 now separates the active owner student from a server-created, lesson-bound teacher task capability, rejects client-supplied identity facts, and requires commit and receipt lookup to take the verified actor and recheck owner, account status, task scope, and pilot reservation. Receipt lookup is bound to lesson ID and verified actor, preventing owner-string authentication and cross-owner existence disclosure. Current account deactivation is an explicit rejection. Keep expiry validation and actor/task binding inside the trusted middleware/store boundary when implementing; the production task-principal design remains correctly deferred. |
| PSK-SEC-02 — Test harness artifact isolation | **Approve** | §5 names a separate `cmd/pilot-e2e-harness` package, `pilot_e2e` build tag, a default-server dependency graph that does not import it, private temporary directory/database, loopback ephemeral port, in-process random token, no path/port/credential override, and production-route negative checks. This is a meaningful artifact-level boundary rather than a default-off runtime flag. Implementation must retain the no-tag production build check and reject Windows reparse points as well as ordinary symlinks when validating the private directory/database path. |
| PSK-SEC-03 — Commit-unknown and receipt not-found | **Approve** | §3 defines a fresh bounded recovery context, reacquisition of the writer, `pending_unknown` for both unreachable and absent receipts, no rollback inference, no new command ID, same-ID/fingerprint/version/generation re-entry, and `not_committed` only where the atomic layer proves callback non-commit plus successful rollback. §6 adds a post-COMMIT/pre-response-loss browser hook. This closes the prior ambiguity at design level. |
| PSK-SEC-04 — Replay fingerprint and authorization generation | **Approve** | Canonical fingerprint now covers `kind`, expected checkpoint version, expected auth generation, owner/lesson/create fingerprint, plan version and exact candidate; golden vector and per-field mutation requirements make the canonicalization reviewable. Transaction order authenticates the current actor before receipt lookup, compares full fingerprint/kind/owner/lesson, and never executes effects on replay. Current active-owner authorization permits reading a committed historical receipt; it is not treated as authority to replay media or mutate state. |
| PSK-SEC-05 — Log/persisted data allowlists | **Approve with follow-up** | §6 removes stable student/lesson identifiers and enumerable scope hashes from ordinary logs, prohibits prompts/model output/student input/credentials/arbitrary tool arguments, fixes audit/receipt/outbox fields and size ceilings, and requires inspection of serialized rows/logs. However, the persisted receipt still includes a SHA-256 fingerprint over a command containing low-entropy candidate text/word data, and the SDD does not say whether that digest is returned to the test client. A plain digest is integrity metadata, not confidentiality protection. Keep it out of public receipts/logs where possible; if returned or used across trust boundaries, use a keyed MAC or document that only the authorized owner can read it and that candidate content is non-secret. |

## Execution capability boundary

**Approve with a non-blocking implementation condition.** The revised `PilotStartExecutor` is narrower than a SQL callback: it receives typed plan/actor, does not expose `*gorm.DB`, is confined to the test-only API adapter, and can only run the `classroom.say` schema/handler through the existing scope/schema/budget pipeline. The handler is explicitly constrained to a pure result; task-local audit is buffered; `Machine.Start` is a distinct state-machine action; store-side allowlist/schema validation and transaction writes remain authoritative. The reviewed current `classroom.say` handler validates trusted classroom task, bounded text and category and returns a UI action without mutating Room state.

Because Go interfaces cannot enforce purity, implementation must wire exactly one fixed test-only executor (never a request/config-supplied implementation), reject unsupported tool names/categories, keep it free of repository/global-auditor/network/cache dependencies, and test that executor error or panic yields no committed rows and no externally visible effect. Its work under the SQLite writer must remain bounded. Do not expose this capability from the production server artifact.

## Residual conditions and limits

- Exact expiry checking for the lesson-bound task capability should be explicit in the verifier and covered by implementation tests; expired capabilities must fail on commit and receipt lookup.
- Verify the harness directory using canonical paths and Windows reparse-point checks, not only a symbolic-link check.
- Keep the SHA-256 command fingerprint out of ordinary logs. Treat any returned fingerprint as a potentially guessable digest, not a redacted secret.
- This approval is only for the SDD's isolated, test-only `created → active` transaction slice. It does not approve a production principal design, ordinary routes, old Room integration, outbox consumption, remote models, real audio, deployment, or a classroom pilot.

No tests were run. No runtime security property is inferred from the document review.
