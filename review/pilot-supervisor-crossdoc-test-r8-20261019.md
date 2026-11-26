# Pilot Supervisor SDD Test Review R8

Review date: 2026-10-19. Read-only test-design review; no implementation tests were run and no design document was modified.

Reviewed `docs/42-Pilot隔离运行监督器SDD.md`, SHA-256 `C1EAE6935BA3581AFFB47D1A8D9EF4CD6F5E8092E08A27BBD543EB1CA8A3905E`. Cross-document checks used `docs/41-试点课堂Start持久迁移内核SDD.md`; its §112 correction and §3/§4 contracts are present.

## Verdict

**PASS for test-design readiness; no blocking or P1 findings.** The new request-digest binding closes the remaining grant/body substitution gap. Earlier FI-REC/E2E counter conflicts and recovery-order ambiguity are resolved in this revision. This is a specification review only; it does not mean the implementation or pilot has passed.

## Checks

- **Grant authorization and request binding — PASS.** The signed grant binds `scope` and SHA-256 of the fixed typed operation's canonical payload; the worker must recompute it before side effects. FI-AUTH-01 covers forged, stale, cross-run, wrong-scope, and payload-mismatched grants, and asserts zero case/member/DB/sidecar/key opens and zero receipt/Planner/Registry effects.
- **Recovery order and stop counters — PASS.** Schema/marker statements run first; the first schema failure prevents sidecar/data reads. Sidecar key/HMAC and event-prefix validation precede R-*; first invalid tool-event prefix prevents all R-* queries. R-* order and stop-on-first-fact-mismatch are fixed, each statement has its own counter, and the counter oracle follows the last executed statement. Recovery uses `database_list` path validation, then `query_only=ON`, before any other recovery SQL.
- **FI-REC-01 and E2E-PILOT-06 — PASS.** Intact valid tool-event prefix plus suffix-only damage runs every R-* query, validates R-AUDIT, and may return the original receipt as degraded. Prefix damage stops before R-*, with no receipt; committed state remains unchanged. Schema/fact corruption has its own stop position and exact counters. Business side effects remain zero in all negative branches.
- **Time and diagnostics — PASS.** Permit-bound logical timestamp, monotonic handler-only duration, and sync-delay exclusions are deterministic and have explicit wall-clock-jump/delay injection. FI/E2E evidence includes statement counters, stable error/state, restart outcome, process logs and business-effect counts.
- **Cross-process, crash, platform and E2E gates — PASS for design.** Named IPC barriers and separate syscall-error/short-write versus hard-kill variants are specified. Linux/Windows adapter evidence, coverage thresholds, JSON log validation, and browser journey/FI traceability are retained. Platform execution and browser E2E remain acceptance work, not reviewed results.

No remaining test-design finding was identified for the requested areas. Production integration, platform CI, Playwright runs and isolated classroom acceptance remain unverified and are explicit later gates.
