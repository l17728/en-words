# Pilot VFS Candidate Acceptance — 2026-10-22

## Scope and decision

Linux candidate generation and fail-closed behavior are verified against docs/55, docs/57, docs/58, and docs/61. Status is **PASS_CANDIDATE**, not golden approval. No proposal was copied into checked-in fixtures. Golden promotion still requires a human to inspect raw traces, candidate summaries, and structured diffs.

Pinned content hashes: docs/55 `A19CB22A25E2312FC37CC5094886AD421CD284D022F5DA6AB39E97B12EB2BDEF`; docs/57 `C1BA9572F4621831FB1F172D3356545FDF9546A14B9E2B47A3435E30C13AA2B4`; docs/58 `1B8538DA1C5DD342E8C078DB42C81DDA02EE87C3A4B2246DA1FA560C2C96CFE6`; docs/61 `46C8D78E6536ACF4BB892C12C10A64BEA2D7496F41BCF1056C77517977720C14`. Callback manifest SHA-256: `5F11776EABB9A139672B35DD2EE92A71A52CA8687DB2C6F4607A01342C191F2D`.

## Verification

On the authorized Linux host, the tagged Pilot VFS package tests, `-race`, `go vet`, and all 11 Python runner tests passed; statement coverage was 81.0%. The runner suite includes all 17 control/publication fault injections, expected failure markers, process-specific bounded reap assertions, artifact path/symlink/hardlink checks, and fail-closed peer-close/zero-record ambiguity. LOCK cross-process contention now treats every missing barrier, including timeout, as fatal; an injected timeout E2E verifies failure, no candidate/proposal, and bounded child cleanup. EEXIST success and initial UNAVAILABLE both require a validated terminal `DONE` frame, clean child exit/reap, and durable trace publication.

Two independent runs returned `PASS_CANDIDATE`. All proposal hashes matched:

| Proposal | SHA-256 |
| --- | --- |
| Pager-delete trace | `00d26c4ea30f5a66633f16178b39d33436d8b7ba79f94b009135f3ba0269d24d` |
| SHM-lock trace | `e79ba6401a96bd392d7057e02447ca83c76cd2a681a01aebc639883ef8942755` |
| Unix-lock trace | `fd4c2cedcd59beafff4e230d0f8d64dc08678908c21fb40ad5edabe999c91ec3` |
| VFS flags | `ca93a68137acdfe001ea49b1a5a6a0d20311f6d73c04f6723aacfc1ebba8543e` |

Evidence archives: `final7-run-artifacts.tar.gz` SHA-256 `289BC26E9DC22FA07E5078247A41CF17E44CCE69D59E1172758B1D72EB8A97B3`; `final8-run-artifacts.tar.gz` SHA-256 `1FC1F7871F1BFB0128A37F6F93C7970EB0B8FCF6841F3C3AA2C4399332942842`.

After the LOCK timeout correction, a fresh run `9ce1ac87ebce853e9a00484d899eb476` also returned `PASS_CANDIDATE`; all four proposal hashes matched the two prior runs. Its complete raw evidence is `final9-timeout-fix-run-artifacts.tar.gz`, SHA-256 `D4CE7D0865F2948FF1F97EF89E4E9D8F2036A621B11A5E8CE86E064FE3DF50DD`.

## Review and remaining gates

Latest implementation hashes: trace generator `012ECF95FDF2901B7C987D54155AAC3E65742A55246BE38586D2434FE1EA17DC`; missing-scenario tests `C9B2DE44122992228658107F0B8ECF62B1EAE74A12F41762C324AB8B534660E0`; Python runner tests `36FAF3AA87D170899BD0397F80E753B0103AC813F1E8925A3D3F90CBAFEE751D`.

Architecture, backend, and test reviewers all returned PASS on the exact final code/spec snapshot, including the LOCK timeout correction. Golden approval remains a human review step. The separate protected-root/private-VFS implementation and production Pilot readiness are outside this acceptance.
