# Pilot Control Pinned SQLite VFS SDD Review — Follow-up

- Verdict: **P2 — resolve before implementation approval**.
- Exact docs/55 SHA-256: `4386D1F6F3E272A1D0DB213CA81708767E89FD471FACB12E91BAF3D58B883451`.
- Cross-doc snapshots: docs/43 `B586621FC749ABD8E609FEF211CF83E00B3614C25EA85711998BFFEC426DC251`; docs/50 `FA699830239AA0463C574CFF4AE2FF7AA6F85B6A163D57B6ABDB3907BCBAA52F`; docs/54 `C4C0DD80D56DF92F265DA2A0B9B18BB754267595FAF6510C1899398AA2841070`.
- Manifest: 61 unique cases, bound to this docs/55 hash; manifest status is `DESIGN_CASES_NOT_EXECUTED`.

## Finding

**P2 — docs/55 omits a mandatory filesystem invariant from docs/43 §2.8.** Docs/43 requires rejecting `system.posix_acl_access` and `system.posix_acl_default` xattrs on every control-root descendant, even when the effective mode is restrictive. Docs/55 specifies owner/group/mode/link-count/mount checks for the root, main DB, and sidecars, but neither states this ACL-xattr check nor includes it in the 61-case VFS inventory. Add explicit rejection for root/ancestors/main/sidecars and negative cases proving no SQLite dispatch/readiness. Also make the existing-sidecar `xOpen` metadata predicate explicit (regular type, expected owner/group, exact mode, `nlink=1`, pinned mount, no ACL xattrs); “verify metadata” is too open-ended for the security contract.

## Resolved issue and readiness

The prior mount-crossing concern is addressed: docs/55 requires anchored `openat2` with `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_XDEV`, pins every ancestor without crossing mounts, and checks same-mount identity. URI use is constrained to one byte-exact allowlisted URI and invalid traversal is rejected before filesystem resolution. The 61-case manifest has one invalid-URI vector (`../`); expand it with noncanonical/extra query parameters, duplicate/reordered parameters, percent encoding, authority/fragment, and alternate mode/VFS values to lock the stated byte-exact rule.

With the P2 filesystem-policy omission resolved, the architecture can proceed to its distinct runtime-golden implementation gate. Missing runtime goldens are not counted as a design finding here; docs/50 and docs/54 correctly require them before implementation, and neither the 61-case integrity result nor this review is runtime acceptance. This remains a design-only review; no implementation or A0/product wiring is approved.
