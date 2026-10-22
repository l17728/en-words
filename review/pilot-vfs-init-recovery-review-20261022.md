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

## Remaining implementation gates

1. Synchronize docs/51–58 with this amendment, including the initialization marker in the v3 DDL, current pager oracle, seed manifest, schema-fault vectors, callback/recovery manifests, and loader hash bindings.
2. Review the synchronized exact-hash bundle across architecture, backend, security, and test design before implementing the test-only identity/lock bridge.
3. Complete human approval of the four host callback proposals and generate/review the per-handle Unix and SHM lock-range oracle. The proposal approval cannot stand in for the lock-range oracle.
4. Implement and execute all callback/startup/recovery cases, then pass the Linux amd64/arm64 × ext4/XFS normal/race/coverage/vet matrix and required privilege preflight.
5. Only after the private VFS gate, proceed sequentially through A0, A1, A2, Stage B, product API/UI, multi-user E2E, and the supervised classroom pilot.

Current inventories remain non-passing: the callback manifest has 81 `DESIGN_CASES_NOT_EXECUTED` rows; the product orchestration manifest has 106 `BLOCKED_NOT_IMPLEMENTED` rows. The 2026-10-22 Linux r4 package acceptance covers only its named `pilotcontrol`/`pilotsupervisor` package checks and does not satisfy the private VFS or product gates.

## Cross-document synchronization progress (2026-10-22)

- Updated docs/55 to use docs/62's initialization/recovery order and the frozen no-journal-callback observation for `PAGER-EMPTY-OBSOLETE-RESERVED`; the synthetic xDelete behavior remains a separate test.
- Updated docs/53 to require the initialization marker and fixed seed manifest in the literal v3 schema/seed inventory.
- Updated docs/56 and rebound the callback manifest and loader to docs/55 SHA-256 `01787314f59f65dd350f748c8f6e0fa0f4c394d327d5fb55fe6c8527a6a60829`. Manifest SHA-256 and loader binding are `e9f152a4d8ccf769b910603a4981559f88130f8c3371167a65da38fbc294bea5`.
- A local Python integrity check confirms the two source-hash bindings and manifest hash match; the manifest still has 81 `DESIGN_CASES_NOT_EXECUTED` cases. Go is not available in this shell, so the Go loader test was not run here.
- The complete docs/51–58 + schema inventory/fault vectors/seed manifest/loader bundle has not yet been synchronized or reviewed. The exact-hash cross-domain review of the updated bundle is pending, as are human golden approval and the per-handle lock-range oracle. No VFS implementation or product wiring is authorized by these documentation changes.

## Final synchronized snapshot review (2026-10-22)

After the earlier review, the recovery manifest grew to 46 uniquely identified cases and the callback manifest/loader bindings were refreshed. Static review against the exact snapshot below returned PASS from architecture, backend, and security reviewers. Two loader text-oracle mismatches (`VFS-ADOPT-WAL-01` and `VFS-ADOPT-WAL-SHM-01`) were found and corrected; a direct map-to-manifest check now reports 10/10 adoption assertions equal. JSON integrity checks report 81 unique callback IDs and 46 unique recovery IDs, with source and manifest SHA-256 bindings matching the loader. The generic legacy `readiness` placeholder was removed; `expected_readiness` is the sole machine-readable field.

| Artifact | SHA-256 |
| --- | --- |
| `docs/53-pilot-control-ledger-v3-physical-schema-matrix.md` | `9709f5e61705e7e2089c0191021e86de4914ff621a182099734d0fa4be53cb74` |
| `docs/55-pilot-control-pinned-sqlite-vfs-sdd.md` | `fa957bfc9b2b039abfa7b7d72d831fc55e616f72074dd8dc8f8e9b4367014dea` |
| `docs/56-pinned-sqlite-golden-harness-delta.md` | `b3eb9da4371c4b4fe1cbebe8a32180ae22b02467f57cc609ecc379e16059fea8` |
| `docs/58-pilot-vfs-eexist-evidence-layer-sdd.md` | `1b8538da1c5dd342e8c078db42c81dda02ee87c3a4b2246da1fa560c2c96cfe6` |
| `docs/62-pilot-vfs-initialization-and-recovery-amendment.md` | `41f96963ed11840d0826b50966fc171d246e83a1f91485671278a655a415ee16` |
| `server/internal/pilotcontrol/testdata/vfs-callback-cases.json` | `f8926da338dea3cd03e1b334caab710e30fdd89486f3aebfc6934eb33d495797` |
| `server/internal/pilotcontrol/testdata/pilot-vfs-recovery-cases.json` | `9a269450c2993a8e05540134a9ba2be2eb476c115f7ef5f031d98b0a01137c92` |
| `server/internal/pilotcontrol/pilot_golden_manifest_test.go` | `5c24ca3cb784e696713c4d6d1bab41a759efc137e21f00d6a20a73f228779373` |
| `review/pilot-control-ledger-v3-schema-fault-manifest-20261021.json` | `ba1d588978d0df2d88eb830e0c0de315b868cc2963ef43c172c4deeda4b430a7` |

Architecture, backend, security, and test-design static reviews now pass for the synchronized design/loader snapshot. Four missing pre-SQLite startup failures were added: unproven prior-process drain, SHM unlink followed by directory-fsync EIO, lifecycle-lock acquire failure, and lifecycle-lock release failure. The loader locks quarantine, zero SQLite entry/dispatch, FD closure balance, namespace mutation count, unlink durability, and no false rollback for these cases. The CI workflow also selects the recovery-manifest test, runs Python from the repository root, resolves Go/strace to absolute paths, and triggers on docs/6* changes. `git diff --check`, workflow YAML parsing, and Python hash/ID/oracle consistency checks pass. The delegated local Python suite reports 11 tests: 1 passed, 10 skipped (Linux/POSIX or real Go/strace prerequisites), exit 0. Go/gofmt are unavailable locally, so no Go compile or loader test ran; remote GitHub Actions remains unverified. Both manifests remain `DESIGN_CASES_NOT_EXECUTED`; these reviews do not approve private VFS implementation or runtime acceptance. Private-VFS callback/recovery execution, the Linux filesystem/architecture matrix, human-reviewed lock-range goldens, and sequential A0-to-classroom gates remain open.

CI workflow hash after fixing tool path resolution: `7ea81f68a55777b4c5ab8a4da28092c5b4edc4cf15061f8f8eb88e0a492d8457`. Final callback/recovery/loader hashes remain `3933ca6822d6566b8d87c0224abc6419688ff4b3006472857dd9cc0892ebe6a3`, `9e4899bac00f448a790d7d34e2fb5d720a74b2d7c839496a52ade948346ed3ea`, and `108170834ebfc133003cb8826ea1d32f9dba5e8dea43c6b69a01e098d74b0c8a`.
