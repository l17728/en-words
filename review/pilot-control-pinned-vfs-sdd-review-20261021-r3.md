# Pilot Control Pinned SQLite VFS SDD Review — Exact Draft r3

- Verdict: **P2 — correct test-result schema before implementation approval**.
- docs/55 SHA-256: `736B50EC5470C63607F816989ECEDF9927FC7C6A5FF5E654873BFAE47604B4EC`
- docs/56 SHA-256: `D572BDFABAEEB9B00410525BB3225BDBCD77CC1996139BBC5400EAD008B46E5C`
- 74-case manifest SHA-256: `CA737511D9424D5F0A2181EB2E487ED1BD02D8B49873C9EF5F73DCCE3C1FD51C`
- Cross-document snapshots: docs/43 `B586621FC749ABD8E609FEF211CF83E00B3614C25EA85711998BFFEC426DC251`; docs/50 `FA699830239AA0463C574CFF4AE2FF7AA6F85B6A163D57B6ABDB3907BCBAA52F`; docs/54 `C4C0DD80D56DF92F265DA2A0B9B18BB754267595FAF6510C1899398AA2841070`.

## Finding

**P2 — pre-SQLite failures are represented as SQLite return codes.** The new ACL root/ancestor rows (for example `VFS-ACL-ROOT-ACCESS-01`) require `error=ipc_registry_corrupt`, zero SQLite/application dispatch, and `readiness=not_ready`, but also set `expected_sqlite_rc=SQLITE_CANTOPEN`. Docs/55 says these ACL checks happen before SQLite registration and application SQL; consequently there is no SQLite callback return code to assert. Likewise, the URI rows labeled `invalid_uri_before_fullpath` require zero filesystem callbacks, so their oracle must distinguish a wrapper/preflight rejection from an actual `sqlite3_open_v2`/VFS return. Change the manifest schema to a tagged result such as `expected_result={layer, code}` or an explicit `N/A_PRE_SQLITE_PREFLIGHT`, and make the test plan distinguish process/API error, SQLite rc, and callback rc. Otherwise generated evidence can falsely claim SQLite returned `CANTOPEN` when SQLite was never called.

## Checks that pass

The docs/55 root traversal contract uses anchored `openat2` with `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_XDEV`, pins ancestors without mount crossing, and validates mount identity. The docs/43 §2.8 ACL-xattr policy is now stated for the whole control-root tree and root/main/sidecars; the six ACL cases cover root and ancestor access/default xattrs plus main/sidecar access ACLs. The URI allowlist is byte-exact, and the seven negative vectors cover reordered query, encoded path, authority, fragment, extra parameter, read-only mode, and wrong VFS; their oracles require no filesystem callback and no application dispatch. The manifest contains 74 unique, schema-complete rows bound to the exact docs/55 hash. docs/56 transparently says the Go loader still binds the old 61-row snapshot and must be changed only after review; it does not claim behavior execution.

## Readiness boundary

The design remains separate from runtime evidence: all 74 cases are `DESIGN_CASES_NOT_EXECUTED`; the four runtime-derived SQLite goldens and native filesystem matrix remain required gates. Those missing artifacts are not findings against this draft's claims. After correcting the pre-SQLite result schema, the architecture is ready to enter the separately gated harness/golden implementation phase. This review does not approve a production VFS, A0, or product wiring.
