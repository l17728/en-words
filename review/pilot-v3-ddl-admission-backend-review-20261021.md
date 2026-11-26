# Pilot Control Ledger v3 DDL Backend Review

Review date: 2026-10-21. Scope: read-only review of the exact DDL/design candidate and implementation-acceptance cases; no production schema or source code was changed.

## Snapshot and integrity

The requested hashes matched: docs/51 `13e3e2d6859623f8c6f424d87e139fe684bde5cd48d4506dd3e8b9ca276b2a26`; docs/52 `4a33744b224cbf5ea3c94149c451218f38598fe64bc2a42ffd10054e1f86bef5`; schema-fault manifest `eda02616ea5472600106033535f4c35f92b0793dd34053bd67d5d0732db55106`; product manifest `bfc7bc302e7e9953793387f101221975571e57f4ae4535ed5edec5cceade8cdb`; DDL manifest `54558381ea66be5305c4ba037e93ad7be53d23f6b8c28785b8e3ca0222ac2324`. The schema suite has 36 unique case IDs/barriers; product suite has 101 unique case IDs/barriers. Every source hash recorded in the DDL manifest matched the current file. The manifest records 95 statements and 95 explicit objects; SQLite's parsed inventory matched the manifest exactly, and `foreign_key_check` returned no rows.

## Verification

`python review/test_pilot_v3_ddl_draft.py` passed. It exercised atomic DDL/seed rollback, FK constraints, INV-01..11 on no-root, normal close, prepared-cancel, and EEXIST paths; targeted negative evidence/receipt/lease/readiness faults; trigger-source authorizer denial; exact object-name filtering; capacity triggers; schema/PRAGMA and REPLACE denial; and monotonic auth/session guards. The 11 authz vectors were independently checked: valid signatures verify, tampered signatures fail, and the declared domain-separated update digest matches.

SCHEMA-34..36 now specify production-driver acceptance beyond the in-memory DDL probe: failed per-connection ConnectHook must prevent readiness and close that handle; eight concurrently pinned physical connections must each enforce the authorizer; and typed capability scope must clear on success, error, and panic before connection reuse. These are precise acceptance oracles, including trigger-only exceptions and denial of equivalent direct SQL.

## Decision

Backend/physical-schema design review: **PASS for implementation slicing**. This is not production runtime acceptance. The artifacts remain explicitly design-only; implementation must pin and verify the real Go SQLite driver, execute SCHEMA-34..36 against real pooled physical connections, and run the full schema fault suite before any service or product route is enabled. No production DDL should be installed from this draft before the required domain design approvals.
