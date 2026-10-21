# Pinned SQLite VFS SDD — Security Review

Reviewed artifact: `docs/55-pilot-control-pinned-sqlite-vfs-sdd.md`  
SHA-256: `44e1072425efb5e19c30969818e1046847ed0ea68c4417bb11c4981212d42ca4`  
Review date: 2026-10-21. Design-only review; no files were modified by this review.

## Decision: BLOCK pending a precise filesystem-operation contract

The security direction is correct: a private VFS must use a pinned directory handle, no-follow opens, actual-handle identity, same-filesystem sidecars, no pathname fallback, and fail-closed readiness. Native Linux tests cover symlink, hardlink, rename/replace, mount crossing, sidecar replacement, short write, locking, sync, and delete failures.

The SDD still leaves implementation-critical details open:

1. **Directory pin and expected identity source are underspecified.** “Pinned trusted-directory handle” does not state how the trusted anchor and every ancestor are opened/verified, or where the expected main-file identity comes from. Carry forward docs/43 §2.1 explicitly: anchor-relative no-symlink/no-mount-crossing traversal, owner/mode checks on each component, and exact main DB owner/group/mode/nlink/device/inode/mount policy before SQLite sees the handle.
2. **Sidecar policy is not concrete enough to prevent hardlink/replacement races.** WAL, SHM, rollback journal, and lock objects are dynamic, so there is no explanation of how their expected identities are established. Require fixed sibling basenames only; no-follow/exclusive creation; `fstat` of opened handles; same device/mount and approved owner/group/mode; `nlink == 1`; and identity revalidation for pre-existing entries.
3. **Namespace operations need explicit safe semantics.** SQLite VFS deletion/access operations are name-based; “delete and rename callbacks operate on retained handles” is not precise enough. Specify directory-FD-relative operations, reject traversal/absolute names, verify the entry still names the expected inode immediately before unlink/replace, never remove a substituted object, and fence the store on any identity drift or namespace error. State that no SQL dispatch/readiness can proceed after such a failure.

These details should be acceptance requirements, not left solely to implementer judgment, because they define whether sidecar replacement and hardlink attacks are actually closed.

## Prototype isolation: PASS

`docs/54` clearly labels the stock path-based `sqlite.go` as an isolated authorizer/schema experiment, forbids wiring it into A0 or product routes, and blocks readiness until docs/55 VFS implementation and native gates pass. This correctly prevents the prototype from being mistaken for an accepted storage boundary.
