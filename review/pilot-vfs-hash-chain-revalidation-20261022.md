# Pilot VFS Evidence Hash-Chain Revalidation

Date: 2026-10-22  
Scope: documentation and draft-manifest integrity only. This record does not approve the production VFS, A0, product routes, or classroom use.

## Findings and corrections

The schema-fault manifest referenced an old `docs/63` hash, which caused `review.test_pilot_runroot_manifest` to fail. The manifest was refreshed to the current `docs/63` hash. A second stale value in `docs/56` referenced an old `docs/62` hash; that reference was corrected. The DDL manifest and schema inventory were regenerated so their source bindings point to the corrected files. The manifest test now also checks schema-case ID uniqueness and the 48-case inventory.

## Exact snapshot

| Artifact | SHA-256 |
|---|---|
| `docs/55-pilot-control-pinned-sqlite-vfs-sdd.md` | `fa957bfc9b2b039abfa7b7d72d831fc55e616f72074dd8dc8f8e9b4367014dea` |
| `docs/56-pinned-sqlite-golden-harness-delta.md` | `32405ada1e7da628f30e29d36480c6e0e7a046cd3e47247b8ed08b85bdae2817` |
| `docs/57-runtime-golden-missing-scenarios-sdd.md` | `c1ba9572f4621831fb1f172d3356545fdf9546a14b9e2b47a3435e30c13aa2b4` |
| `docs/58-pilot-vfs-eexist-evidence-layer-sdd.md` | `1b8538da1c5dd342e8c078db42c81dda02ee87c3a4b2246da1fa560c2c96cfe6` |
| `docs/62-pilot-vfs-initialization-and-recovery-amendment.md` | `44333074581018d3a98d0b0cf8aab8edaf53d0c195ea3429bb71a33221e04ed8` |
| `docs/63-pilot-intent-quota-journal-sdd.md` | `366e726cce3d7715864e48013fc9145047a87338a8f69459915e0a3e909ab6b6` |
| Schema-fault manifest | `9e188e6ad3ce059e637be4c697661ead7bc09f37167a164921e8615be0671a16` |
| DDL draft manifest | `869755f9273b617ada42ebd2f44365deab446b7817294b760027edc548f016d5` |
| DDL schema inventory | `e537af51414816adacb038bb6b401d6f495c6021bb867f41e750f6703c671911` |
| Manifest regression test | `4362f5c1d81011e5c89fc6556a7317e315173e8c5381574808e3039cc0578197` |

## Review and validation

Architecture, backend/test-design, and security reviews passed this exact metadata snapshot. Reviewers verified every source hash in the schema-fault and DDL manifests, the DDL inventory pointer and digest, the 48 unique schema cases remaining `BLOCKED_NOT_IMPLEMENTED`, and the non-authorizing draft statuses. They confirmed candidate traces remain proposals rather than canonical goldens and that docs/55–58/62 continue to prohibit connecting the path-based prototype to A0 or product routes.

- `python -m unittest -v review.test_pilot_runroot_manifest`: 11 passed.
- `python review/test_pilot_v3_ddl_draft.py`: passed; 103 DDL statements and schema objects, with invariant and negative-fault checks.
- `PYTHONPATH=review python -m unittest -v review.test_pilot_vfs_candidate_runner review.test_pilot_test_discovery`: 34 tests, 24 passed and 10 skipped for POSIX/Linux/Go/strace-only conditions; 0 failures.
- Source-hash audit: all bindings in product (116 cases), schema-fault (48 cases), quota-journal (26 cases), and DDL draft manifests matched. All product/schema/quota cases remain blocked/not implemented.
- `git diff --check`: passed.

Go and Linux `strace` are unavailable in this Windows environment. The skipped cases do not count as runtime VFS, native Linux, race, filesystem, or product acceptance. The private VFS, canonical runtime goldens, native Linux matrix, A0/A1/A2, RunRoot/logger product orchestration, Playwright flows, and supervised classroom pilot remain open.

## Follow-up: stale candidate and evidence authorization gate (2026-10-22)

The archived final9 candidate remains historical and is not applicable to current docs/55. Its archived candidate/proposal hashes are internally consistent, but the archived source binding is stale; no golden or VFS status is promoted.

The evidence-binding design was tightened after security review. A bare SHA-256 CLI value is explicitly non-authorizing. The revised draft specifies a canonical RFC 8785 payload, Ed25519 signature, out-of-band trust root, repository/purpose/subject/source bindings, validity and revocation checks, and online one-use nonce/sequence consumption before any evidence write or child launch. It also pins target OS/architecture, toolchain/module bundle digests, an empty snapshot-local GOCACHE, and the build environment allowlist. BIND-10..12 cover authorization failures and build-input/environment tampering.

An explicit 74-entry source inventory now exists at `review/pilot-vfs-source-snapshot-manifest.json`, SHA-256 `dfd2f1e69738ed58bc7b69f40d6f0dcfc9e5aac5b788c7262d4edef38b5a7fb6`; docs/56 SHA-256 is `8b100cac9a2df993b885aaff67fa81673310167250e2d6da41f80f38531b90d2`. The manifest remains `DRAFT_NOT_AUTHORIZED`: Linux toolchain, compiler/linker, module-cache, module-graph and OS-image digests are null because no native Linux capture is available. Independent exact-hash expert review is pending. The design review does not authorize runner implementation, fresh candidate execution, golden promotion, private VFS, A0, product wiring, or classroom use.

The current environment still has no Go installation, Linux runner, SSH configuration, or configured remote runner credentials. Therefore native Go/strace/race/filesystem verification remains NOT RUN; previously passing Python and DDL checks are documentation/manifest checks only and do not close those gates.

## Security-protocol revision (2026-10-22)

The next exact draft closes reviewer findings on path-scope ambiguity, one-use consumption receipts, and golden approval records. The manifest now defines `entries` as the complete allowlist and `closed_roots` as subtrees that must be exhaustively enumerated; explicitly listed singleton files are permitted outside those roots. The docs specify canonical receipt fields and signature verification, service-side authorization validation, tuple binding, replay rejection, and exactly-one-winner concurrent consumption. Golden promotion now requires two distinct authorized reviewers signing the same payload under a separate promotion trust root, binding archive, snapshot, candidate report, reviewed diff, outcome, reviewers, time, and sequence. BIND-13/14 exercise forged/replayed/mismatched approval and valid two-reviewer acceptance.

This supersedes the earlier follow-up draft hashes: current docs/56 SHA-256 is `84c9bcadbe920140472e303f75386dbc733aeee2922325d56b44153ef73b81d2`; current 74-entry manifest SHA-256 is `38d177767be8d5867bb54f8b83204e074eadcf5c5cb66dbb08c41d113956fec0`. The authorization, receipt and promotion schemas now state exact field types, lengths, character sets, numeric ranges, time format and canonical base64url/hash forms; signed envelopes reject duplicate keys, and decoded payload bytes must exactly equal their RFC 8785 reserialization. BIND-10/13 include duplicate-key, key-order, whitespace, escape and nonzero-pad-bit vectors. The report and process record bind authorization payload digest, complete signed receipt envelope digest and receipt ID, nonce, sequence, run ID and source snapshot, so evidence from separate runs cannot be mixed. The authorization service must durably consume the tuple before signing a receipt and must not reissue success after crash, restart, restore or uncertain commit. BIND-10 covers durability/failover; BIND-15 covers cross-run report/process/receipt swaps. These hashes await exact-hash architecture, backend/test, and security re-review. Manifest status remains `DRAFT_NOT_AUTHORIZED` and native toolchain values remain unavailable; no runner implementation, candidate execution, or golden promotion is authorized.

### Final design review and current validation

Architecture, backend/test-design, and security reviewers independently returned PASS on the exact `docs/56` hash `84c9bcadbe920140472e303f75386dbc733aeee2922325d56b44153ef73b81d2` and manifest hash `38d177767be8d5867bb54f8b83204e074eadcf5c5cb66dbb08c41d113956fec0`. They verified all 74 entries, explicit closed-root semantics, strict signature/receipt/promotion encodings, durable single-use authorization, report/process cross-binding, and BIND-10..15. This closes the evidence-binding design review only; no reviewer authorized implementation or golden promotion.

- Source inventory audit: 74/74 paths exist and all recorded sizes and SHA-256 values match.
- `PYTHONPATH=review python -m unittest -v review.test_pilot_runroot_manifest`: 11 passed.
- `python review/test_pilot_v3_ddl_draft.py`: passed (103 DDL statements/schema objects and invariant/fault probes).
- `PYTHONPATH=review python -m unittest -v review.test_pilot_vfs_candidate_runner review.test_pilot_test_discovery`: 34 tests, 24 passed, 10 skipped for Linux/POSIX/Go/strace-only conditions; 0 failures.
- Targeted `git diff --check` for docs/56 and this report passed. A repository-wide diff check remains noisy from pre-existing unrelated modified files; they were left untouched.

Implementation and fresh Linux candidate execution remain `BLOCKED_PREREQUISITE`: the source inventory is `DRAFT_NOT_AUTHORIZED`, authorization is `NOT_AUTHORIZED`, toolchain hashes are null, Go/Linux/strace and SSH runner credentials are unavailable. Historical final9 candidate evidence remains stale to current docs/55 and cannot be promoted. No runner code, VFS, A0, product wiring, or classroom state was changed.
## Environment-status correction (2026-10-22)

The earlier statement that the workspace has no Go installation or native Linux evidence is superseded for current status. Go 1.27.3 is available locally at `.test-tools/go1.27.3-extract2-20261022/go/bin/go.exe`, and `review/pilot-supervisor-linux-package-acceptance-20261022.md` now records native Linux amd64/ext4 package acceptance. This later run validates `pilotsupervisor` only; it is not a fresh VFS candidate run and does not close any VFS callback, filesystem, race, golden-promotion, or A0 gate.

The final9 VFS candidate remains bound to stale docs/55 and cannot be promoted. A current VFS candidate still requires the exact source snapshot and valid signed one-use run authorization in docs/56; the checked-in source inventory remains `DRAFT_NOT_AUTHORIZED`. This correction does not authorize runner execution or implementation. Linux arm64/XFS and privileged service acceptance remain unverified.
