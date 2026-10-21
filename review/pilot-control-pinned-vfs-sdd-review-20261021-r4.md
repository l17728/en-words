# Pilot Control Pinned SQLite VFS SDD Review — Exact Draft r4

- Verdict: **P2 — add xattr-query failure vectors before implementation approval**.
- docs/55 SHA-256: `63B09CED9F0A6E982A410528948C802DE4246CC80180BD6E0BE7861F1EEDC538`
- docs/56 SHA-256: `2C3D042A5DF00A7843DC519FFDC2983D274FF62B8ABA2B4619468C0D74D8F59D`
- Callback manifest SHA-256: `AB360AA90B5585BDF44F7C7EEFE360F7E15B450C1AB3C76D41B93CAE76E850AB`
- Manifest integrity: 74 cases, 74 unique IDs, source hash matches docs/55, status `DESIGN_CASES_NOT_EXECUTED`.

## Finding

**P2 — documented fail-closed xattr-query errors have no explicit test vector.** Docs/55 requires an inability to query ACL xattrs (including permission/unsupported/I/O errors) to fail closed before application dispatch. The six `VFS-ACL-*` rows only inject `xattr_result=present`; they prove rejection of a discovered ACL but do not lock the distinct error branch. Add at least one deterministic xattr-read failure case for root preflight and one for an opened main/sidecar object, with expected `ipc_registry_corrupt`/`SQLITE_CANTOPEN`, zero application dispatch, no mutation, and readiness not published (or store fenced for a post-ready check). Without these vectors, an implementation that treats an xattr-query error as “ACL absent” can pass the listed ACL cases.

## Exact correction checks

PASS on the prior result-layer issue: all eight URI negative vectors (`VFS-FULLPATH-02..09`) and four root/ancestor ACL vectors use `N/A_PRE_SQLITE_PREFLIGHT` with explicit `error_layer`; the main and sidecar `xOpen` ACL cases retain `SQLITE_CANTOPEN`. Each URI negative asserts zero filesystem callbacks and zero SQL dispatch. The manifest parses with all required fields and unique IDs.

PASS on cross-document direction: docs/55 carries docs/43 §2.8 ACL-xattr rejection across the control-root tree; the root traversal pins ancestors with anchored `openat2` and `RESOLVE_NO_XDEV`, with same-mount identity checks. docs/56 consistently labels itself a review draft, states the 74-case manifest hash binding, keeps all behavior NOT RUN, and explains the loader still targets the old 61-row snapshot until this delta is reviewed. It limits pre-VFS goldens to transparent host-VFS characterization and keeps private-VFS/native acceptance as separate gates. No contradiction found in this staged implementation sequence.

After the ACL-query-error coverage is added, the design is ready to enter the separately gated test harness/runtime-golden phase. The missing runtime goldens and native filesystem matrix are prerequisites, not evidence of pass or a finding against the draft. This review approves no production VFS implementation or A0/product wiring.

