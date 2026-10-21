# Pilot Supervisor SDD Test Review — Final

Review date: 2026-10-19. Read-only test-design review; no implementation tests were run and no design document was modified.

Reviewed revisions:

- `docs/41-试点课堂Start持久迁移内核SDD.md` — SHA-256 `EDCCD7B52ACE3300912796B69730B7B91D6C47542D959B9471798D173E10BE34`
- `docs/42-Pilot隔离运行监督器SDD.md` — SHA-256 `AA33AC4A45B15E84281D81A38A6CAE97FF52D538BFF26B16E5BBE21A5D223A56`

## Verdict

**BLOCKED by two recovery/E2E oracle conflicts.** Capability scope and owner checks, logical-time construction and injected timing faults are substantially specified. The R-* profiles and browser journey map are also present, but degraded-sidecar diagnostics do not currently have one coherent counter/result oracle.

## Findings

### B5 — FI-REC-01 requires successful R-AUDIT validation for corrupt-prefix cases

FI-REC-01 groups suffix-only damage and earlier sidecar event corruption, then says `R-AUDIT验证仍成功` for both. The earlier-corruption branch includes malformed/invalid tool events; R-AUDIT requires a valid sidecar event chain and matching `tool_started/tool_result` for the DB audit timestamps and HMACs. It cannot pass in that branch while also detecting the corrupted prefix and rejecting the receipt as §9.1/E2E-PILOT-06 requires.

**Required closure:** split FI-REC-01 by profile. Suffix-only corruption after the intact, HMAC-valid prefix: perform all R-* DB checks and assert R-AUDIT success before returning the original receipt as degraded. Prefix corruption: assert no receipt and no R-AUDIT success claim; specify whether schema validation runs, whether data recovery queries are skipped, and exact counters/error. Preserve the catalog `committed` state and zero writes in both cases.

### B6 — E2E-PILOT-06 says all counters are zero despite required recovery reads

The suffix-only E2E branch explicitly displays the receipt “after all R-* reads,” which necessarily increments `db_schema_validation_sql` and `db_recovery_data_sql`. The same row then says counters remain zero for all cases. That is not executable as written unless “counters” means only business side-effect counters.

**Required closure:** name the counter classes separately: expect schema/data recovery query counts for the suffix profile, and zero writes, Planner, Registry, new command, and outbox delivery counters. Give the committed-prefix-corruption branch its own expected query counts consistent with B5.

### P2 — Stale reference to docs/41 §70 remains

R-AUDIT now includes a complete local timing formula and fault test, but still says the contract satisfies `docs/41 §70`; the reviewed docs/41 has no §70. Remove or replace that citation with the relevant existing sections, or state that the timing contract is local to docs/42.

## Areas that pass review

- **Capability authorization:** principal is sourced from authenticated gateway state; owner/run/scope binding is unforgeable and process-local; start, recovery, receipt-read and administrator cleanup scopes are separated. FI-AUTH-01 covers forged, stale, cross-run and wrong-scope capabilities with zero member-open and side-effect counts.
- **Logical time:** wall/monotonic attempt anchor, permit-bound TS, monotonic handler-only DurationMs, logical `tool_result` time, and wall-clock/Sync-delay injections are specified. FI-AUDIT-01 requires exact audit/sidecar equality.
- **Recovery data model:** R-* projections, profiles, values, bounds and counters are explicit. The general no-receipt path and pending-unknown outcome are constrained; inconsistent evidence is quarantined.
- **E2E and operations:** journeys exercise real browser→loopback HTTP→SQLite flows, multiple students, restart, lost response, cleanup race, degraded receipt and unavailable DB/network. Linux/Windows evidence gates, coverage and diagnostic artifacts are specified.

This is a document design review only; implementation, platform CI, and real classroom acceptance remain untested.
