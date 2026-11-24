# Pilot Supervisor Idempotency and Grant-Boundary Test Review

Review date: 2026-10-19. Read-only test-design review; no implementation tests were run and no SDD was modified.

Reviewed `docs/42-Pilot隔离运行监督器SDD.md`, SHA-256 `79436BF0CF92CD7ED04B6D0C0EE2ECD9AE44369733655287C1F73E1E468B0876`.

## Verdict

**FAIL — test coverage is not yet sufficient for the requested idempotency and expiry boundaries.** The protocol behavior is mostly specified, and the worker-ready/IPC-response-loss failure path is covered. However, the acceptance matrix does not prove the HTTP Idempotency-Key mapping, concurrent retry behavior, exact grant-expiry boundaries, or the distinct new-root identity with executable assertions.

## Findings

### P1 — HTTP Idempotency-Key contract is not fully exercised

§3 specifies `(session_key_hash, idempotency_key_hash) → (request_id,digest)`, same-key/same-payload replay, concurrent requests joining one in-flight operation, and same-key payload/scope conflicts. FI-IPC-01 only says a bound request retry returns the same selector and conflicts for a reused `request_id`; E2E-PILOT-08 likewise changes `request_id` payload/scope. Neither proves that the HTTP Idempotency-Key is hashed/scoped correctly or that parallel same-key requests cause exactly one worker IPC/CreateCase call.

**Required tests/oracles:** (1) completed retry in the same authenticated session with same Idempotency-Key and canonical payload returns byte-identical selector and creates one case; (2) two barrier-synchronized concurrent retries with that key/payload share one in-flight result and issue exactly one IPC/CreateCase; (3) same session/key with changed payload or scope returns `request_conflict` before IPC; (4) same key in a different session does not resolve to the first session's request/selector and cannot read its case. Assert request-map cardinality, IPC/CreateCase count, catalog generation/case inventory, and zero unauthorized member opens.

### P1 — Grant expiry is specified but boundary behavior has no executable oracle

§3 defines `now < expires_at_ms <= now+60s`, and FI-AUTH-01 mentions an expired grant. The test plan does not cover equality at either bound, one-millisecond outside values, or expiration while a signed grant waits at an IPC barrier. E2E mappings have no grant-expiry journey.

**Required tests/oracles:** using an injected/frozen worker UTC clock, assert `expires_at_ms == now` rejects; `now+1ms` and exactly `now+60s` accept; `now+60s+1ms` rejects. Add a delay/barrier case where a valid-at-signing grant expires before worker validation and is rejected. Rejection must occur before capability/member/DB/sidecar/key opens and before CreateCase/Planner/Registry effects; keep signature, request digest, scope and run epoch fixed. If expiry is intentionally only a Go/process FI, add a browser E2E that observes the stable failure/retry outcome and zero effects at the Gateway boundary.

### P2 — New-run root separation is a journey assertion, not a complete FI oracle

§7/E2E-PILOT-08 require a fresh epoch/root after uncertain create, but FI-IPC-01 only says the old root is abandoned/never reopened and there is no duplicate in an active root. It does not explicitly assert the replacement root is a different canonical directory derived from a distinct 128-bit epoch, has an independent initial catalog/inventory, and never resolves the abandoned selector.

**Required test/oracle:** record old/new run_epoch, verified root identity/path hash, catalog/member inventory and process IDs; assert epoch and root identity differ, old root is never opened by the new worker, old selector remains inaccessible, new catalog begins from its own initial state, and exactly one case exists only in the new active root after a deliberate new create.

## Passing coverage

The durable-`ready` then lost IPC selector response branch is explicit: kill/reap worker, abandon the whole old run/root, do not resend the grant or reuse the root, and retain process/catalog evidence. The fully owner-bound HTTP response-loss case is also named in E2E-PILOT-08 and should be retained while adding the missing exact assertions above.
