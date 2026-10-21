# Pinned SQLite VFS SDD — Security Review 2

Reviewed artifact: `docs/55-pilot-control-pinned-sqlite-vfs-sdd.md`  
SHA-256: `d875770aba4bf651cb129be9397b895b18439d404106761a95b2a1fdc9a0d72b`  
Review date: 2026-10-21. Design review only; no source files changed.

## Decision: BLOCK on two contract contradictions

The additions close the main detail gaps from the prior review: docs/55 now specifies pinned root and file identity checks, exact DB/sidecar names, no-follow/exclusive sidecar creation, `fstatat`/`unlinkat`, hardlink limits, post-open sidecar identity retention, runtime failure fencing, and a deterministic failure matrix. The pinned VFS is explicitly required before A0 readiness.

1. **VFS URI selection conflicts with URI rejection.** The filesystem contract says URI names/flags fail closed, while initialization requires opening with an explicit `vfs=` URI argument. SQLite normally needs URI interpretation enabled to select a named VFS through `vfs=...`. Define one narrow accepted URI form and flag combination (exact VFS name plus fixed canonical database token, no other query parameters), or select the registered VFS through a mechanism that does not require URI parsing. Until reconciled, an implementation cannot satisfy both rules safely.
2. **Root ancestor traversal omits `RESOLVE_NO_XDEV`.** The trusted-anchor `openat2` call includes `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS`, and only compares the final root mount identity. docs/43 §2.1 requires rejecting cross-mount traversal. An intermediate mount crossed and then exited can leave the final root on the approved mount while violating that rule. Add `RESOLVE_NO_XDEV` for root acquisition (and keep the existing identity checks); add a barrier-driven intermediate-mount test.

## docs/54 prototype isolation: PASS

docs/54 explicitly states the stock path-based adapter is an isolated experiment, must not be wired to A0/product routes, and cannot publish readiness until the pinned VFS passes its native gates. This is clear and appropriately fail-closed.

Implementation and native Linux matrix remain unverified; approval here is blocked on the two SDD inconsistencies above.
