# PREFLIGHT_PREPARE Design Review

Date: 2026-10-29
Scope: `docs/92-first-audio-preflight-prepare-phase-sdd.md`, reconciled with docs/74 and docs/91.

## Review outcomes

- **Architecture — PASS.** `NewApp` remains within STARTUP_PROBE; the strict owner transitions through an exclusive PREFLIGHT_PREPARE before connection expansion, then publishes final PREFLIGHT. No listener or JOURNEY is part of this slice.
- **Security — PASS.** PREPARE grants no SQL/UDF/transaction/cleanup/user authority. Registration phase is provenance only. Three ISSUED, unarmed receipts and exact live ID/raw identity are validated under one registry lock across context publication and arming; no callback or connection user may run in that window.
- **Test quality — PASS.** Required cases cover phase/version/lease bypass, identity cardinality/reuse, PREPARE authorization denial, candidate mutation, legacy direct-path compatibility, incomplete/foreign/stale receipts, and Linux normal/race plus Windows CI selectors.

## Review iterations resolved

1. Clarified that receipts are ISSUED-but-unarmed at publication input; arming follows successful immutable-context publication.
2. Required registry synchronization to cover exact identity-set validation, context swap, and receipt arming while all pins remain held.
3. Added strict `requirePreflightPrepare` controller policy for the guarded owner and retained direct transition only for legacy fixtures.
4. Defined ConnectHook phase as registration provenance; runtime authorization always checks the currently published immutable context.
5. Synchronized docs/74 and docs/91, including the PREPARE no-SQL exception to general metadata-read policy.
6. Replaced caller-controlled startup verification flags with a controller-owned, one-use receipt bound to the STARTUP context and matching baseline/readback digests.

This review approves the design only. Code review and CI remain required before accepting the implementation. Parent kill/reap for uncertain cleanup is not yet verified by this slice.
