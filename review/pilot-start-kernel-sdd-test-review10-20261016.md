# Pilot Start Kernel SDD — Test Review 10

**Date:** 2026-10-16  
**Scope:** Read-only test-design review of the latest `docs/41-试点课堂Start持久迁移内核SDD.md`; no code/tests run.  
**Verdict: BLOCK** — all three review9 findings are closed in the test matrix. Two new P2 gaps remain around receipt replay under bad ACLs and failed cleanup.

## Review 9 finding status

- **Receipt-present damaged sidecar recovery: CLOSED.** §93 now requires a committed DB proof, damaged marker, same-case restart, exact receipt replay, `sidecar_degraded`, repeated-restart stability, zero calls for a fresh command, and `recovery_integrity_error` when receipt lookup is unavailable. §96 has the matching Playwright path.
- **ACL/path/key negative cases: CLOSED for pre-Prepare requests.** §93 enumerates wrong owner, extra/inherited ACE, POSIX mode/umask, reparse/hard-link/path swap, existing/replaced key, repeated entropy, and key exposure checks, all failing before Prepare and preventing reuse.
- **Permit replay/binding: CLOSED.** §93 covers one-time use and wrong command/plan/attempt/effect/tool bindings, asserting no Registry call or result/bundle, plus the durable-start barrier.

## Findings

### P2 — Receipt replay bypasses the ACL check required before opening case files

§42 requires owner-only ACL validation “before each open” for SQLite, sidecar, and key. But §52 checks receipt before ACL/key validation (which is explicitly deferred to receipt-miss), and §47 also performs receipt lookup before its sidecar/permission validation branch. The new §93 ACL cases all assert failure “before Prepare”; receipt replay never calls Prepare. Consequently, a case whose directory/DB ACL becomes permissive after commit may still return a receipt without applying §42's open-time ACL invariant, and the test matrix will not detect it.

**Required resolution/test:** retain receipt-first precedence over sidecar integrity, but specify that directory/DB ACL and canonical file identity are verified before opening the DB for the receipt query. Add a valid-receipt fixture, loosen the ACL or replace/swap the path, restart, and assert refusal without exposing the receipt or invoking any planner/registry. Alternatively, explicitly justify and scope an exception to §42 and test the chosen behavior.

### P2 — Cleanup failure behavior is specified but not tested

§42 says a cleanup failure isolates the case and forbids reuse. §93 tests retention windows and grouped deletion, but does not inject a failed deletion (for example, locked DB/WAL/key or denied unlink) and prove the supervisor retains an isolated tombstone/marker and cannot reopen the partially deleted case under the same case ID.

**Required test:** make one member of the DB/WAL/sidecar/key group undeletable; assert cleanup reports failure, leaves the case non-reusable and inaccessible to harness requests, retries or quarantines according to a fixed rule, and never silently creates a new key over residual files.

## Other reviewed areas

- **Plan/tool crash lifecycle:** PASS. §§43–46, 51, and 93 specify `plan_ready`, `tool_started`, `tool_result`, pre/post-Sync crash points, `pending_unknown`, and no tool replay.
- **Event state/result enums:** PASS. §44 enumerates result classes and §46 fixes ordered transitions and terminal outcomes.
- **Retention:** PASS for the nominal deadlines in §§42/93. Cleanup-failure handling remains the P2 finding above.

No P1 finding was identified. Close the two P2 findings before test-domain approval.
