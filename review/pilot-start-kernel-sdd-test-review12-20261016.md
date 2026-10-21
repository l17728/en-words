# Pilot Start Kernel SDD — Test Review 12

**Date:** 2026-10-16  
**Scope:** Read-only review of the latest `docs/41-试点课堂Start持久迁移内核SDD.md`; no code/tests run.  
**Verdict: BLOCK** — both review11 P2 findings are closed. The new loopback security policy is clear, but its executable negative matrix omits several explicitly forbidden request forms and listener assertions.

## Review 11 finding status

- **ACL/canonical identity before receipt lookup: CLOSED.** §42 requires `OpenVerified` before any receipt handling or DB/sidecar/key open; §93 includes receipt-present cases with weakened ACL or replaced file, requiring pre-Prepare refusal and no receipt disclosure.
- **Cleanup failure quarantine: CLOSED.** §§42/93 now specify durable `cleanup_quarantined`, restart persistence, no open/reuse, and release only after whole-case removal.

## New finding

### P2 — Loopback request security negatives do not cover all declared rejection gates

§96 specifies exact Host and same-origin Origin, `Sec-Fetch-Site=same-origin`, POST JSON, per-case CSRF and Bearer auth, no cookie auth, no cross-origin CORS, rejection of OPTIONS write preflight, and token exclusion from URL/storage/log. The explicit E2E negative list tests cross-origin, fake Host, absent Origin/CSRF, content type, and GET. It does not independently test:

- `Sec-Fetch-Site: cross-site` with otherwise-valid Host/Origin/CSRF/Bearer;
- cookie-only authentication (and invalid/missing Bearer) to prove cookies cannot substitute for Bearer;
- OPTIONS preflight and CORS response headers;
- a listener-bound-address assertion proving the server actually binds only `127.0.0.1`, not wildcard/LAN;
- CSRF token absence from URL and browser storage.

The generic “缺任一项” requirement does not define concrete cases or expected responses/counters for these vectors. In particular, the `Sec-Fetch-Site` check is a separate predicate from Origin validation, and a same-origin page can be tested with an overridden fetch-metadata header.

**Required test additions:** one isolated negative case per omitted gate, all proving rejection before Coordinator/Planner/Registry; assert no permissive CORS headers for OPTIONS, inspect the bound listener address, and inspect URL/storage for the CSRF token. Keep one valid same-origin Bearer+CSRF control case.

## Other areas

Receipt-first degraded recovery, `PilotToolPermit` binding/consumption, filesystem ACL/key negatives, crash-state transitions, and nominal retention/quarantine are adequately specified. No P1 finding found.
