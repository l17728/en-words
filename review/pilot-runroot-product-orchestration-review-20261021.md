# Pilot RunRoot Product Orchestration Review (2026-10-21)

## Reviewed snapshot

- `docs/50-pilot-runroot-product-orchestration-sdd.md`: `36f3ada6dbeab349987c4d11568ce94d3c6a74748ada10d70dba7bae1ad56725`
- `docs/54-pilot-runroot-stage-a-implementation-delta.md`: `9f8b6c8dd5ca8f455f595a27090de2cf43ab523e1f0502de833cf2608c789c64`
- `review/pilot-runroot-product-orchestration-manifest-20261021.json`: `6f9855da2e9177b121164fff5044eb2d57aafc13447cdf78c2f795661983856a`

The manifest binds docs/43, 45, 46, 47, and 50-54. It contains 101 cases: API-00..16 (17), IDRES-01..08 (8), LAUNCH-01..13 (13), ROOT-01..16 (16), RECOVERY-01..10 (10), PRODUCT-01..19 (19), UI-01..08 (8), LOG-01..08 (8), and DR-01..02 (2). IDs and named barriers are unique, all required source hashes match, and all cases remain `BLOCKED_NOT_IMPLEMENTED`.

## Final design review

Architecture, backend, security, product/UI/E2E, and test-design reviewers approved the latest protocol and cross-principal correction with no remaining design blockers. The accepted design specifies the signed mint claim, opaque AES-256-GCM handle, canonical token/CBOR encoding, purpose and expiry semantics, serialized durable nonce allocation, rollback behavior, malformed/concurrency/crash/rotation coverage, and principal-bound handles. A student reusing another principal?s handle is generically rejected with no effects; independent runs require independent authenticated handle issuance. The UI requires an explicit Start action after an expired unbound handle and never silently remints.

The review is design-only. Earlier architecture/test reviews cover prior revisions; they are not represented as approval of runtime product integration. No user-facing pilot route is enabled by this review.

## Validation and remaining gates

`python review/generate_pilot_v3_ddl_manifest.py` regenerated the hash-bound DDL inventory. `python review/test_pilot_v3_ddl_draft.py` passed atomic-init/FK, INV-01..11, corruption/fault cases, alias rotation, journal anchor, authorizer and capacity checks. The source-hash/unique-ID/barrier/status audit passed for all 101 product cases.

Current Windows/amd64 Go validation is recorded in [pilot-runroot-windows-validation-20261021.md](pilot-runroot-windows-validation-20261021.md): default and `pilot_e2e` test/vet all pass. It does not execute Linux-only code. The trusted launcher, integrated API/UI lifecycle and recovery/close path remain unimplemented; pilot package coverage, browser E2E against product routes, native Linux amd64/arm64 ext4/XFS privileged acceptance, and supervised classroom acceptance remain required. The pilot tenant must remain disabled until those gates pass.
