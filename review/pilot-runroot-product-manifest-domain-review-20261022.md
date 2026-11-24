# Pilot RunRoot Product Manifest Domain Review

Date: 2026-10-22  
Scope: design, manifest integrity, test-design, product/E2E review, and Windows Python regression only. This record is not implementation or Linux/product acceptance.

## Exact design snapshot

| Artifact | SHA-256 |
|---|---|
| `docs/50-pilot-runroot-product-orchestration-sdd.md` | `23e5c61b3061b3a15053399dbdb6269d34273e4c6f302d6c37c16a97e92a2989` |
| `review/pilot-runroot-product-orchestration-manifest-20261021.json` | `e8456558503014ecadcb1e5498cd4fc5be6b8098e4dfc6ee6cb2645dee5cfb81` |
| `review/pilot-runroot-product-manifest-revalidation-20261022.md` | `f7297bc965dc3f0e9a4dba3284fd1c436117c0ed3a71d5d3c34e27ca33e41a50` |

All 116 acceptance cases remain `BLOCKED_NOT_IMPLEMENTED`; the route and test tenant remain disabled.

## Review results

- Architecture: PASS. Confirmed 116 unique cases/barriers, source bindings, nonce quota ordering, separate expiry/binding evidence, and preserved VFS/A0/A1/A2 gates.
- Backend: PASS. Confirmed API-22..26 isolation, API-15 durable permits with zero business side effects, API-24 evidence fields/restart semantics, and that the quota journal is a future reviewed prerequisite rather than an existing component.
- Security: PASS at design level. Confirmed bearer-only requests, exact Origin checks, no cookie fallback/credentialed CORS, server-side quotas, durable quota-journal failure closed behavior, and provenance rules. Production integration/provenance still requires its own acceptance.
- Test design: PASS. Confirmed each API quota dimension has an independent setup/oracle and every case remains explicitly unimplemented.
- Product/UI/E2E: PASS at design level. UI-01..08 cover public lifecycle states, explicit ordinary-text route, two students plus admin isolation, and expired-handle UX. The final API-24 expiry clarification does not alter those journeys.

## Regression evidence

Command: `python -m unittest -v review.test_pilot_runroot_manifest review.test_pilot_vfs_candidate_runner review.test_pilot_test_discovery`  
Environment: Windows 10 amd64, Python 3.12.10.  
Result: 44 tests, exit code 0; 10 POSIX/Linux-specific tests skipped because this host lacks those facilities. The manifest integrity tests and current API-15/API-24 assertions passed.

Evidence: `review/pilot-runroot-current-python-20261022-rerun6-final.stderr.log` (SHA-256 `8d649f1dd192d59e0e379bd098757f1f221e118b0c313f517c9a570d949b90e6`), `review/pilot-runroot-current-python-20261022-rerun6-final.meta.json` (SHA-256 `b213e389726ad5c0837b90a66f304ec2e7725196f53a0ceb7700a5d2ce7a7f3e`), test source SHA-256 `176195062fdbb26dfa360600c126d7ec947e2d3503ba25ec7d2f26bcc75a9b65`.

## Remaining gates

These reviews authorize no implementation by themselves. The protected quota-journal format, crash/reopen protocol, launcher binding/expiry provenance, replay handling, and cross-store recovery require their own reviewed design and integration acceptance; mint stays disabled until then. Private pinned SQLite VFS, literal schema fault tests, native Linux filesystem/systemd/cgroup runs, implementation-stage gates, browser E2E execution, and the supervised real-classroom pilot are still outstanding. Windows skipped tests do not count as Linux acceptance.
