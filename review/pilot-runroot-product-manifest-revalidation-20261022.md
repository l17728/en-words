# Pilot RunRoot Product Manifest Revalidation

Date: 2026-10-22  
Scope: exact-hash integrity and case-inventory revalidation only. This record does not approve implementation or product readiness.

## Finding and correction

Earlier records describe different snapshots: the product review has 101 cases (`6f9855da?`), and the A2 shutdown review has 105 (`c9bf3dee?`). The 106-case draft was corrected with API-17..20 for route denial and closed request schemas, deterministic API-15 contention, and the public/internal separation in PRODUCT-10. Security review of the 110-case snapshot identified missing Origin/CSRF rules and server-side mint resource bounds. The SDD now specifies bearer-only session transport, exact-Origin checks, no cookie fallback or credentialed CORS, and concrete request-size/rate/concurrency/outstanding-handle limits. Architecture and test-design review identified quota interactions; API-15 uses 32 independently authorized principals, and API-22..26 isolate limits and quota-journal failure/restart cases. Rolling-hour usage and unbound-handle count survive restart; token bucket restarts empty. The exact quota-journal design is now in docs/63 with a 19-case manifest; both remain separate review gates before implementation. A bound handle releases capacity only from exact-launcher durable evidence keyed by principal, handle hash, and run epoch; expiry release instead requires durable issuance/expiry timestamps plus durable unbound state and has no run epoch. API-24's test fixture validates quota semantics only, not production provenance. Cross-store ambiguity retains capacity. API-15 expects 32 durable quota permits and zero business effects. No route or product behavior was enabled.

## Verified snapshot

| Artifact | SHA-256 |
|---|---|
| `docs/43-Pilot授权幂等与父进程身份绑定SDD.md` | `b586621fc749abd8e609fef211cf83e00b3614c25ea85711998bffec426dc251` |
| `docs/45-pilot-run-root-bootstrap-sdd.md` | `c09416a9c6f0070655f7e03192b04630b98e25798f079c94bdee67dc831d2837` |
| `docs/46-pilot-host-logger-queue-sdd.md` | `8c821317d515b49596f621ee2a6dbef1e9816fe55b41b24b86c1690d21bd6486` |
| `docs/47-pilot-linux-launcher-identity-handoff-sdd.md` | `10674f2b05bb688e0664bbe527bbcf541e5f10a93aa0b18d5dc8dbb9e2e58e99` |
| `docs/50-pilot-runroot-product-orchestration-sdd.md` | `7e9b3078097855cf22e58d2018e9d5b719ab0243ab929f580de147951f8dd164` |
| `docs/51-pilot-control-ledger-v3-sdd.md` | `13e3e2d6859623f8c6f424d87e139fe684bde5cd48d4506dd3e8b9ca276b2a26` |
| `docs/52-pilot-control-ledger-v3-physical-model-decisions.md` | `4a33744b224cbf5ea3c94149c451218f38598fe64bc2a42ffd10054e1f86bef5` |
| `docs/53-pilot-control-ledger-v3-physical-schema-matrix.md` | `9709f5e61705e7e2089c0191021e86de4914ff621a182099734d0fa4be53cb74` |
| `docs/54-pilot-runroot-stage-a-implementation-delta.md` | `c4c0dd80d56df92f265da2a0b9b18bb754267595faf6510c1899398aa2841070` |
| `review/pilot-control-ledger-v3-schema-fault-manifest-20261021.json` (41 cases) | `ba1d588978d0df2d88eb830e0c0de315b868cc2963ef43c172c4deeda4b430a7` |
| Manifest `review/pilot-runroot-product-orchestration-manifest-20261021.json` | `815884c1dfc8c59642567d3403398c8e890654c5ae6c1bf754cccb8431f234e4` |

The manifest has 116 unique case IDs and 116 unique named barriers; all 116 cases remain `BLOCKED_NOT_IMPLEMENTED`. Group counts are API 27 (API-00..26), IDRES 8, LAUNCH 13, ROOT 16, RECOVERY 11, PRODUCT 19, UI 8, LOG 8, A0 1, A2 3, and DR 2. Every listed source path exists and every source hash matches the current worktree.

## Decision

The inventory and source binding are internally consistent for this snapshot. The 101-, 105-, 106-, 110-, 112-, and earlier 116-case reviews are historical and do not constitute review of this exact manifest hash. Obtain exact-hash architecture, backend, security, product/UI/E2E, and test-design review before using the manifest to authorize implementation. All RunRoot, logger orchestration, API/UI, and classroom acceptance cases remain blocked; the route remains disabled. The quota-journal design/provenance/integration prerequisite and all private-VFS/native Linux gates remain open.

## 2026-10-22 follow-up: intent-binding digest integrity

This follow-up supersedes the stale schema-fault inventory and hash table above for the current design snapshot. The trusted typed store recomputes `intent_binding.row_digest` before commit and on startup, replay, and receipt reads. A mismatch quarantines the ledger before RPC readiness and retains quota capacity. SQLite checks storage type and length only. SCHEMA-47 covers incorrect supplied digests and persisted digest tampering. This remains design-only; the compiled schema inventory and runtime implementation have not been updated.

The schema-fault manifest now contains 48 cases, the RunRoot product manifest contains 116 cases, and the quota journal manifest contains 20 cases. All source hashes in the three manifests match this worktree. The receipt trust boundary is specified as a resident-ledger Ed25519 signature, with a deployment-pinned public-key fingerprint; quota settlement also authenticates the registered launcher IPC peer. Architecture, security, backend, and test-design reviews passed the current design package; test-design requested the final SCHEMA-47 split into independent corrupted database fixtures, which architecture, security, and test-design then confirmed. The targeted integrity suite passed 18 tests. No product code or compiled `schema_inventory_v3.json` was changed. Native Linux VFS/SQLite gates, product/API/Playwright E2E implementation gates, and isolated supervised classroom acceptance remain open.

Current source hashes for the revised design package:

| Artifact | SHA-256 |
|---|---|
| `docs/50-pilot-runroot-product-orchestration-sdd.md` | `a4cbf44e7d326639e102a0ccdba18cf117038324059c668dbd6fe3ba39eef186` |
| `docs/51-pilot-control-ledger-v3-sdd.md` | `5e4984e57d119e2157688f37559b7b7e6c51cefc4b9f71a9fa542a6225b6e1a0` |
| `docs/52-pilot-control-ledger-v3-physical-model-decisions.md` | `eac7115e9fb30c0a2eb2b470dadd24bbf2ba0b2b5b69978298bc64f9b5107cd7` |
| `docs/53-pilot-control-ledger-v3-physical-schema-matrix.md` | `0a2dfce7fa0bedbf8ea7bad813beca30a01dee275fc50ef394542302300afcdf` |
| `docs/54-pilot-runroot-stage-a-implementation-delta.md` | `63db174044bb8fd779f0cc9950355d5f8cbc1123eee7817558889ecc47a86609` |
| `docs/63-pilot-intent-quota-journal-sdd.md` | `37e07693044d529e1cd1b101d239969a26163bbec4e2c38c31d5b6dd490332b2` |
| Schema-fault manifest (48 cases) | `ff1820eb56a190a77fed418adee8eafc66466a8c78e959c171d18b7c85824d98` |
| Quota-journal manifest (20 cases) | `0dbe22d63ba754d07fccdeb3fa284022f3c1c8813028cfdf5dde8ef9a5c6174b` |
| RunRoot product manifest (116 cases) | `b9a45b002b6f6555c43a5ed4a3522523da36d94bfbce1909fdbd329cfa79420a` |


## 2026-10-22 follow-up: intent-binding digest integrity

This follow-up supersedes the stale schema-fault inventory and hash table above for the current design snapshot. The typed store recomputes `intent_binding.row_digest` before commit and on startup/replay/receipt reads; mismatch quarantines before RPC readiness and retains quota capacity. SQLite checks type and length only. SCHEMA-47 covers incorrect supplied digests and persisted digest tampering. This is design-only; the compiled schema inventory and runtime implementation have not been updated.

| Artifact | SHA-256 |
|---|---|
| `docs/50-pilot-runroot-product-orchestration-sdd.md` | `0744f3f33e53602c9dfea3f2cc3afe98ae6f766102132cb08f737baa36764b38` |
| `docs/51-pilot-control-ledger-v3-sdd.md` | `159ff7bff895929660fdc04d8a5b11c3eb60589535acb7d800747197183589dd` |
| `docs/52-pilot-control-ledger-v3-physical-model-decisions.md` | `eac7115e9fb30c0a2eb2b470dadd24bbf2ba0b2b5b69978298bc64f9b5107cd7` |
| `docs/53-pilot-control-ledger-v3-physical-schema-matrix.md` | `d40e4aab0be5c66ae1e796c789b2afe7b00bfe6153eb233f51d0f0c53c56db16` |
| `docs/63-pilot-intent-quota-journal-sdd.md` | `f40f3c7f1be7403ce642ac14e31e4c8237ad23698d734a0baff64f7157ee04da` |
| `review/pilot-intent-quota-journal-manifest-20261022.json` | `c1ace3848e6d88abe70b8d59ae26a8fa967c0dbc19112d17fcfe1f244b3ab5aa` |
| `review/pilot-runroot-product-orchestration-manifest-20261021.json` | `1e649c20f6a687c15bf1f1b0d850a0b2b9477a5aa219b4c6bd1222561efff4f4` |
| `review/pilot-control-ledger-v3-schema-fault-manifest-20261021.json` | `9444b24659c418ba845c460c74a2b30e189148d4712dc22ac0e535a9d4a01cd9` |
| `review/test_pilot_runroot_manifest.py` | `765fa516ddb3c52d81ec0ada72d4bb943f441a76edb99d89a3264686211f918e` |

The schema-fault manifest now contains 47 unique cases; the RunRoot manifest contains 116 cases; the quota journal manifest contains 20 cases. All manifest source hashes match this worktree. `python -m unittest review.test_pilot_runroot_manifest` passed 18 tests (0 failures). Exact-hash architecture, backend, and security follow-up reviews are pending. No product code or compiled `schema_inventory_v3.json` was changed. Native Linux VFS/SQLite gates, product/API/Playwright E2E implementation gates, and isolated supervised classroom acceptance remain open.
