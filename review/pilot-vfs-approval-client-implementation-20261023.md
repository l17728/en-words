# Pilot VFS Approval Client Implementation Review

Date: 2026-10-23

## Scope

Added `review/pilot_vfs_approval.py`, a local verifier for the reviewed v2 single-reviewer approval and service-signed promotion record, plus an HTTPS/mTLS client for the reviewed `POST /v1/promotions` and read-only `GET /v1/promotions/{promotion_id}` contract. It does not create signatures, contain production keys, implement the durable promotion service, or authorize a candidate run.

The verifier enforces canonical JSON for payloads and envelopes, duplicate-key rejection, closed schemas, strict unpadded base64url, Ed25519 signature and trust-role checks, distinct reviewer/service keys, exact artifact and sequence bindings, and the signed promotion-time window. The client checks HTTPS hostname/CA validation, TLS 1.2+, client identity, bounded timeouts and responses, and verifies returned records locally. It rejects weak TLS contexts again before each request. A POST whose result may be ambiguous raises `PromotionServiceError(outcome_unknown=True)` and is never retried automatically.

## Verification

- Unit/integration protocol suite: 23/23 passed; `py_compile` passed. Evidence: `.test-artifacts/pilot-vfs-approval-final4-20261023/result.json` and `unit.stderr`.
- `py_compile` passed for the implementation in that run.
- Static backend/security and protocol/test-design reviews passed for the final implementation hash; see the conversation review outputs and the source hashes below.
- `git diff --check` passed for both Python files.

Implementation SHA-256: `51527329582C9E9C1B5CCEDA19D4BC032ACAB79ADE3E5A11615DAA6E95CE6047`.

Test SHA-256: `9E39C37118B781B1C2E10CE7607438EC911137BE42BD7E02FC9B323B51E7E8F6`.

## Remaining External Gate

No production promotion service, anti-rollback ledger, registered reviewer/service keys, or mTLS credentials are present in this repository. The TLS test server is an ephemeral in-process fixture; its tests do not prove durable atomic consumption, crash recovery, backup-restore resistance, or deployed identity configuration. No candidate or `PASS_GOLDEN` record was produced.

If a POST response is lost, the client reports an unknown outcome and requires authoritative-ledger reconciliation before any new approval. The reviewed API offers lookup by `promotion_id`, which may itself be lost with the POST response; it has no lookup by approval-envelope digest/sequence. Operational reconciliation must therefore be provided by the independently operated service until a separately reviewed recovery-query contract exists. Do not retry the same approval or infer success from a local artifact.
