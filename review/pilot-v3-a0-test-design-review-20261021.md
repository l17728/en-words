# Pilot v3 A0 Test-Design Review

Date: 2026-10-21  
Decision: **PASS — test-design layer only**

## Reviewed source hashes

| Artifact | SHA-256 |
|---|---|
| `docs/51-pilot-control-ledger-v3-sdd.md` | `13e3e2d6859623f8c6f424d87e139fe684bde5cd48d4506dd3e8b9ca276b2a26` |
| `docs/52-pilot-control-ledger-v3-physical-model-decisions.md` | `4a33744b224cbf5ea3c94149c451218f38598fe64bc2a42ffd10054e1f86bef5` |
| `docs/53-pilot-control-ledger-v3-physical-schema-matrix.md` | `cb29920d48e3c484836cb3544a26d884811ed06f5cf1078a3bd6ea2d43596d3c` |
| `docs/54-pilot-runroot-stage-a-implementation-delta.md` | `d75aa50b9e97515c930101b49111bc24a5bd07a6f368b5039a92f72558134b7d` |
| `review/pilot-control-ledger-v3-schema-fault-manifest-20261021.json` | `eda02616ea5472600106033535f4c35f92b0793dd34053bd67d5d0732db55106` |
| `review/pilot-runroot-product-orchestration-manifest-20261021.json` | `bfc7bc302e7e9953793387f101221975571e57f4ae4535ed5edec5cceade8cdb` |
| `review/pilot-control-ledger-v3-ddl-manifest-draft-20261021.json` | `54558381ea66be5305c4ba037e93ad7be53d23f6b8c28785b8e3ca0222ac2324` |

The DDL manifest's remaining referenced SQL, capability, vector, generator, and harness hashes also match the current files. The fault manifest has 36 unique cases; all remain `BLOCKED_NOT_IMPLEMENTED`.

## Findings

SCHEMA-34..36 close the prior production-authorizer test-design gap with actionable barriers and observable outcomes:

- SCHEMA-34 injects `ConnectHook` failure on first and later physical connections before pool/service publication, then asserts readiness/RPC are denied, the failed connection is never reused, and a fresh healthy pool works.
- SCHEMA-35 pins eight distinct live `database/sql` connections and checks denied DML, DDL, TEMP objects, ATTACH/DETACH, and mutating PRAGMAs on each, while distinguishing approved trigger-originated writes from direct SQL imitations.
- SCHEMA-36 exercises typed capability success, returned error, and panic, then reuses the same and another pooled connection to prove rollback, capability clearing, and no cross-method or cross-connection leakage.

These are implementable as internal Go tests against the exact pinned CGO SQLite driver. The Python DDL harness remains supplementary: it passed (`python review/test_pilot_v3_ddl_draft.py`) and validates SQL policy, but does not validate the production Go driver's `ConnectHook`, connection pool, or panic cleanup. Those runtime checks must be implemented and run before A0 acceptance. The DDL manifest correctly keeps driver version/license, pool behavior, capability cleanup, and performance as implementation release gates; it is an artifact inventory, not the executable fault-case list.

## Gate boundary

Test-design review passes for A0. This does not record implementation acceptance: the 36 schema-fault cases are blocked, the DDL manifest remains `DRAFT_FOR_DOMAIN_REVIEW_NOT_IMPLEMENTATION_AUTHORITY`, and native CGO/Linux, race, coverage, multi-connection, and full-domain review evidence must still pass before production exposure.
