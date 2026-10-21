# Pilot v3 / A0 Security Admission Review r2

Date: 2026-10-21  
Decision: **PASS for design and artifact consistency only**. This is not runtime or implementation acceptance.

## Reviewed snapshot

| Artifact | SHA-256 |
|---|---|
| `docs/50-pilot-runroot-product-orchestration-sdd.md` | `36f3ada6dbeab349987c4d11568ce94d3c6a74748ada10d70dba7bae1ad56725` |
| `docs/51-pilot-control-ledger-v3-sdd.md` | `13e3e2d6859623f8c6f424d87e139fe684bde5cd48d4506dd3e8b9ca276b2a26` |
| `docs/52-pilot-control-ledger-v3-physical-model-decisions.md` | `4a33744b224cbf5ea3c94149c451218f38598fe64bc2a42ffd10054e1f86bef5` |
| `docs/53-pilot-control-ledger-v3-physical-schema-matrix.md` | `cb29920d48e3c484836cb3544a26d884811ed06f5cf1078a3bd6ea2d43596d3c` |
| `docs/54-pilot-runroot-stage-a-implementation-delta.md` | `d75aa50b9e97515c930101b49111bc24a5bd07a6f368b5039a92f72558134b7d` |
| `review/pilot-control-ledger-v3-schema-fault-manifest-20261021.json` | `eda02616ea5472600106033535f4c35f92b0793dd34053bd67d5d0732db55106` |
| `review/pilot-runroot-product-orchestration-manifest-20261021.json` | `bfc7bc302e7e9953793387f101221975571e57f4ae4535ed5edec5cceade8cdb` |
| `review/pilot-control-ledger-v3-ddl-manifest-draft-20261021.json` | `54558381ea66be5305c4ba037e93ad7be53d23f6b8c28785b8e3ca0222ac2324` |

The DDL manifest's 14 source hashes, schema-fault manifest's 5 source hashes, and product manifest's 9 source hashes matched their current files. The literal DDL manifest contains 95 statements. The schema-fault and product manifests contain respectively 36 and 101 unique cases; all remain `BLOCKED_NOT_IMPLEMENTED`. The prior docs/51 case-count drift is corrected in this snapshot.

## Security findings

No blocking security-design finding remains in the reviewed scope. The resident `PilotControlReaper` is the serialized sole writer; launcher, authorization authority, normal Reaper, and recovery-only Reaper have distinct caller classes and method allowlists. The per-connection `ConnectHook` authorizer is required before pool exposure; scoped capabilities are private to typed methods, clear on every exit, and cannot share prepared statements across scopes. The only trigger-origin write exception is the named capacity trigger. New SCHEMA-34/35/36 cases cover first/later hook failure, eight concurrently pinned physical connections, and capability cleanup after success/error/panic with rollback and reuse checks.

Identity storage retains versioned HMAC aliases without raw immutable IDs; key/alias loss or mismatch fails closed. Authorization updates require a dedicated Ed25519 key, fixed canonical-CBOR envelope, monotonic revisions, idempotent receipts, and a committed resident fence before restrictive changes become effective. Journal anchor CAS plus the externally monotonic signed restore marker fences paired-snapshot rollback; bootstrap and recovery RPCs bind caller identity to exact generation/nonce/process/target tuples.

The 11 frozen authorization vectors were independently checked with Ed25519: 9 signatures verify, the two signature-tamper vectors fail verification as expected, and the included update digest matches the specified domain-separated hash. `python review/test_pilot_v3_ddl_draft.py` passed; `git diff --check` on the reviewed docs/artifacts was clean.

## Acceptance boundary

The Python harness validates SQL/state invariants, not the production Go/CGO SQLite driver. The production-driver hook/capability tests, Linux/race/native filesystem gates, A0 service behavior, product API/E2E, and classroom pilot are not accepted here. In particular, the new SCHEMA-34/35/36 cases are specifications, not evidence of executed Go tests.
