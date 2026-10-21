# Pilot Start Kernel SDD — Test Review 13

**Date:** 2026-10-16  
**Scope:** Read-only test-design review of the latest `docs/41-试点课堂Start持久迁移内核SDD.md`; no tests/code run.  
**Verdict: PASS.** Review11's two P2 findings and review12's loopback-security coverage gap are closed with deterministic checks.

## Finding status

- **Receipt ACL/path validation: CLOSED.** §42 requires `OpenVerified` before every Coordinator start, receipt handling, or DB/sidecar/key open; failed path, owner, ACL/mode, link, or identity validation prevents receipt lookup and returns `case_integrity_error`. §93 explicitly includes a valid-receipt case with weakened ACL or replaced file and asserts no disclosure or planning/tool work.
- **Cleanup failure quarantine: CLOSED.** §§42/93 specify persistent `cleanup_quarantined`, restart-time refusal to open/reuse, and catalog release only after all DB/WAL/SHM/sidecar/key artifacts are confirmed removed. §96 includes an E2E restart/reuse check.
- **Loopback request boundary: CLOSED.** §96 now has independent negative cases for cross-origin Origin, cross-site Fetch Metadata despite otherwise valid headers, fake Host, missing Origin/CSRF, cookie-only or missing/invalid Bearer, wrong content type, GET, and OPTIONS. Each asserts rejection before Coordinator with zero Prepare/ExecuteTool/Registry calls; OPTIONS must emit no CORS allow headers. It also checks the actual socket binds only to `127.0.0.1`, scans token sentinels in URL/history/storage/IndexedDB/cookies/logs/files, and retains a valid same-origin Bearer+CSRF positive control.
- **Earlier test contracts: PASS.** The degraded receipt recovery, permit single-use/binding, filesystem/key negative cases, crash-state matrix, event enums, DB cardinality/log assertions, and retention/quarantine cases remain explicit.

No open P1/P2 test-design issue was identified in the requested scope. This is approval of the SDD test contract only; no implementation or execution result is implied.
