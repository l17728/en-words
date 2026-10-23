# Pilot VFS Approval and Recovery Client Review

Date: 2026-10-23

## Scope

`review/pilot_vfs_approval.py` verifies the reviewed v2 single-reviewer approval, service-signed promotion record, and service-signed permanent recovery fence. Its HTTPS/mTLS client supports `POST /v1/promotions`, read-only promotion lookup, and `POST /v1/promotions/reconcile/{approval_envelope_sha256}`. It does not create signatures, contain production keys, implement the durable service or witness, or authorize a candidate run.

Verification enforces canonical JSON, closed schemas, duplicate-key rejection, Ed25519 signatures, historical reviewer role/key validity, artifact and sequence bindings, and distinct reviewer/service keys. The client validates HTTPS hostname/CA, TLS 1.2+, mTLS identity, bounded timeouts and response sizes. An ambiguous promotion outcome remains unknown; recovery returns a terminal result only after verifying the signed promotion record or signed fence. It never retries the original promotion request.

## Verification

- Recovery protocol suite: 31 tests passed with `ResourceWarning` treated as an error; `py_compile` passed. This includes injected response-cleanup failure preserving `outcome_unknown` and attempting both closes. The TLS warning seen in the earlier run no longer occurs.
- Full output and result record: `.test-artifacts/pilot-vfs-recovery-final3-20261023/result.json`, `unit.stdout`, and `unit.stderr`.
- Test coverage includes successful promotion recovery, signed permanent fencing, historical reviewer validation, mTLS identity, and malformed, oversized, truncated, unavailable, or mismatched responses.
- Linux CI installs the pinned test dependency from `review/requirements-pilot-vfs.txt` and runs this suite in the runner preflight job.
- Static backend/security and test-design reviews passed with no P1/P2/P3 findings for the exact implementation and test hashes below.
- `git diff --check` passed for the reviewed changes.

Implementation SHA-256: `383A9F2850A6A48F9E0872A2916D9F9FD61E6AEF3B98920210D6260B4DD4AA19`
Test SHA-256: `A480CDBB06F68A461C2EEF8DDE0B76D458C8B25E5837ED2E75B1E2EE3DD8B046`

## Design and External Acceptance Gate

The recovery contract and crash/reconciliation cases are specified in [docs/67](../docs/67-pilot-promotion-ambiguous-outcome-recovery-sdd.md), with the amendment linked from [docs/56](../docs/56-pinned-sqlite-golden-harness-delta.md). Architecture, backend/security, and test-design reviews approved the local client SDD. The final lifecycle patch and cleanup-failure test also passed backend/security and test-design review at the hashes above. These approvals do not establish that a remote service implements the contract.

No production promotion service, durable anti-rollback ledger, independent signed checkpoint witness, registered production reviewer/service keys, mTLS credentials, or authorized candidate are available here. The ephemeral HTTPS fixture does not prove atomic durable consumption, crash recovery, restore resistance, or deployed identity controls. Candidate promotion and `PASS_GOLDEN` therefore remain NOT RUN. Until the reconcile endpoint and durable witness are deployed and independently accepted, a lost POST response must be reconciled against the authoritative service ledger; do not retry the same approval or infer success from local files.
