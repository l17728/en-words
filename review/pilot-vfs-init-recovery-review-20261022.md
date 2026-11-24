# Pilot VFS Initialization and Recovery Design Review

Date: 2026-10-22  
Reviewed artifact: `docs/62-pilot-vfs-initialization-and-recovery-amendment.md`  
SHA-256: `B98509890C9BE79CCF4EBF6A522F1AB2744374C89FE20BFA48278D2CDD5C70C1`

## Decision

Architecture, backend, security, and test-design reviewers returned **PASS** on the exact document hash above. This approval is limited to the initialization/restart-recovery amendment. It does not authorize private VFS implementation, canonical-golden promotion, A0 readiness, product routes, or classroom use. No code or tests were changed/run for this review.

## Review findings closed

- WAL mode setup is separated from the atomic schema/seed/initialization-marker transaction; existing databases cannot re-enter initialization.
- Restart adoption distinguishes O_PATH identity anchors from each connection's independent OFD file descriptions, fixes descriptor ownership, releases internal locks before SQLite recovery, and defines stale-SHM deletion and crash re-entry.
- Startup permissions, ACL checks, `openat2` resolution flags, lifecycle lock range, build-time digest trust, embedded manifests, and initialization marker constraints are explicit.
- The recovery matrix includes valid WAL plus stale SHM, invalid sidecar identities, pager recovery, unknown commit outcomes, marker validation, and zero application dispatch before readiness.
- Host pager observations are not invented: the zero-page/zero-byte journal scenario records no journal `xOpen`, `xClose`, or `xDelete`; a separate synthetic `xDelete` case covers callback behavior.
- Golden lock-range evidence separates volatile per-run inode/FD validation from deterministic cross-run fixtures and models SHM's own shared-node FD.

## Current implementation gates

1. **Closed, design-only scope:** the reviewed design synchronization for docs/53, 55, 56, 58, and 62, the callback/recovery case manifests, and loader/hash bindings listed in the final snapshot below. This does not mean that the literal v3 DDL/seed inventory or a separate initialization seed manifest has been implemented.
2. **Closed, reviewed-artifact scope:** exact-hash static review of the listed design, manifest, loader, fault-inventory, and workflow artifacts. It does not cover all of docs/51–58, any uncreated seed manifest, the identity/lock bridge implementation, runtime acceptance, or golden promotion.
3. **Open:** human approval of the four host callback proposals and generation/review of the per-handle Unix and SHM lock-range oracle. Proposal approval cannot stand in for that oracle.
4. **Open:** add the initialization marker and fixed seed manifest to the literal v3 DDL/seed inventory, implement and execute the identity/lock bridge and callback/startup/recovery cases, then pass the Linux amd64/arm64 × ext4/XFS normal/race/coverage/vet matrix and required privilege preflight. GitHub Actions is currently blocked before job startup by account billing; see the run link below.
5. **Open, sequentially gated:** after the private VFS gate, proceed through A0, A1, A2, Stage B, product API/UI, multi-user E2E, and the supervised classroom pilot.

Current inventories remain non-passing: the callback manifest has 81 `DESIGN_CASES_NOT_EXECUTED` rows; the product orchestration manifest has 106 `BLOCKED_NOT_IMPLEMENTED` rows. The 2026-10-22 Linux r4 package acceptance covers only its named `pilotcontrol`/`pilotsupervisor` package checks and does not satisfy the private VFS or product gates.

## Cross-document synchronization progress (2026-10-22)

This is an earlier checkpoint; its pending-work statements are superseded by the final reviewed snapshot and current gates above.

- Updated docs/55 to use docs/62's initialization/recovery order and the frozen no-journal-callback observation for `PAGER-EMPTY-OBSOLETE-RESERVED`; the synthetic xDelete behavior remains a separate test.
- Updated docs/53 to require the initialization marker and fixed seed manifest in the literal v3 schema/seed inventory.
- Updated docs/56 and rebound the callback manifest and loader to docs/55 SHA-256 `01787314f59f65dd350f748c8f6e0fa0f4c394d327d5fb55fe6c8527a6a60829`. Manifest SHA-256 and loader binding are `e9f152a4d8ccf769b910603a4981559f88130f8c3371167a65da38fbc294bea5`.
- A local Python integrity check confirms the two source-hash bindings and manifest hash match; the manifest still has 81 `DESIGN_CASES_NOT_EXECUTED` cases. Go is not available in this shell, so the Go loader test was not run here.
- The complete docs/51–58 + schema inventory/fault vectors/seed manifest/loader bundle has not yet been synchronized or reviewed. The exact-hash cross-domain review of the updated bundle is pending, as are human golden approval and the per-handle lock-range oracle. No VFS implementation or product wiring is authorized by these documentation changes.

## Final synchronized snapshot review (2026-10-22)

At an intermediate checkpoint, the recovery manifest had 46 uniquely identified cases and the callback manifest/loader bindings were refreshed. Static review against that snapshot returned PASS from architecture, backend, and security reviewers. Two loader text-oracle mismatches (`VFS-ADOPT-WAL-01` and `VFS-ADOPT-WAL-SHM-01`) were found and corrected; a direct map-to-manifest check then reported 10/10 adoption assertions equal. This 46-case inventory is historical and is superseded by the final 50-case snapshot recorded below. The generic legacy `readiness` placeholder was removed; `expected_readiness` is the sole machine-readable field.

| Artifact | SHA-256 |
| --- | --- |
| `docs/53-pilot-control-ledger-v3-physical-schema-matrix.md` | `9709f5e61705e7e2089c0191021e86de4914ff621a182099734d0fa4be53cb74` |
| `docs/55-pilot-control-pinned-sqlite-vfs-sdd.md` | `fa957bfc9b2b039abfa7b7d72d831fc55e616f72074dd8dc8f8e9b4367014dea` |
| `docs/56-pinned-sqlite-golden-harness-delta.md` | `78b9b83df2261f41e325f1a7911895dafec28606321669c395c70c69ca46ae1b` |
| `docs/58-pilot-vfs-eexist-evidence-layer-sdd.md` | `1b8538da1c5dd342e8c078db42c81dda02ee87c3a4b2246da1fa560c2c96cfe6` |
| `docs/62-pilot-vfs-initialization-and-recovery-amendment.md` | `44333074581018d3a98d0b0cf8aab8edaf53d0c195ea3429bb71a33221e04ed8` |
| `server/internal/pilotcontrol/testdata/vfs-callback-cases.json` | `3933ca6822d6566b8d87c0224abc6419688ff4b3006472857dd9cc0892ebe6a3` |
| `server/internal/pilotcontrol/testdata/pilot-vfs-recovery-cases.json` | `9e4899bac00f448a790d7d34e2fb5d720a74b2d7c839496a52ade948346ed3ea` |
| `server/internal/pilotcontrol/pilot_golden_manifest_test.go` | `108170834ebfc133003cb8826ea1d32f9dba5e8dea43c6b69a01e098d74b0c8a` |
| `review/pilot-control-ledger-v3-schema-fault-manifest-20261021.json` | `ba1d588978d0df2d88eb830e0c0de315b868cc2963ef43c172c4deeda4b430a7` |

Architecture, backend, security, and test-design static reviews pass for the listed synchronized design/loader snapshot. Four missing pre-SQLite startup failures were added: unproven prior-process drain, SHM unlink followed by directory-fsync EIO, lifecycle-lock acquire failure, and lifecycle-lock release failure. The loader locks quarantine, zero SQLite entry/dispatch, FD closure balance, namespace mutation count, unlink durability, and no false rollback for these cases. The CI workflow also selects the recovery-manifest test, runs Python from the repository root, resolves Go/strace to absolute paths, and triggers on docs/6* changes. `git diff --check`, workflow YAML parsing, and Python hash/ID/oracle consistency checks pass. An earlier delegated Python subset reported 11 tests (1 passed, 10 skipped); the broader rerun is recorded below and supersedes that count. Go/gofmt are unavailable locally, so no Go compile or loader test ran. GitHub Actions run 39705639785 was blocked before any test step because account payments failed or the spending limit must be increased. The first run was rejected because `runner.temp` was referenced at job-level `env`; this was moved to the manifest-test step. The corrected workflow is on the temporary branch, but Linux checks remain blocked by account billing. Both manifests remain `DESIGN_CASES_NOT_EXECUTED`; these reviews do not approve private VFS implementation or runtime acceptance. Private-VFS callback/recovery execution, the Linux filesystem/architecture matrix, human-reviewed lock-range goldens, and sequential A0-to-classroom gates remain open.

CI workflow SHA-256 after moving the runner context to step scope: `b4bbb43b08ed5024fc85abf6eab0bbb077a04943327da0948da9cebae2e5d4a9`. Final callback/recovery/loader hashes remain `3933ca6822d6566b8d87c0224abc6419688ff4b3006472857dd9cc0892ebe6a3`, `9e4899bac00f448a790d7d34e2fb5d720a74b2d7c839496a52ade948346ed3ea`, and `108170834ebfc133003cb8826ea1d32f9dba5e8dea43c6b69a01e098d74b0c8a`.

GitHub evidence: [run 39705639785](https://github.com/l17728/en-words/actions/runs/39705639785) was blocked before any test step by the repository account's billing/spending-limit state. The workflow correction is on the temporary acceptance branch and awaits a runnable GitHub runner.

## Local regression rerun (2026-10-22)

The Windows-compatible runner/discovery suite was rerun by the background regression agent with `PYTHONPATH=review`: `python -m unittest -v review.test_pilot_vfs_candidate_runner review.test_pilot_test_discovery`. Result: 34 tests, 24 passed, 10 skipped, 0 failed (exit 0). Skips require POSIX permissions or Linux/Go/strace/server runtime; no skipped case is counted as Linux acceptance. The first invocation without `PYTHONPATH` failed during discovery import and was corrected before the recorded run. Clean raw log: `review/pilot-vfs-init-recovery-python-rerun-raw-20261022.log`, SHA-256 `b48dd65743e23deea00b4b98caf9b06aa18c733bdb654f122e283ae79b9df87e`; it records the working directory, argv, captured stdout/stderr, and exit code.
