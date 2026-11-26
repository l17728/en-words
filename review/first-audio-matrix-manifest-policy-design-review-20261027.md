# First-audio manifest and audit-trigger policy design review — 2026-10-27

## Reviewed specification

`docs/74-first-audio-matrix-isolated-backend-sdd.md`, SHA-256 `200D757FF1E50A2D3C14115DD035D9D614AF57A038CB487D8FC1C20F434C6A45`.

## Changes reviewed

1. The schema-to-read manifest check runs through the setup pool after pinned schema validation. Physical columns from `PRAGMA table_xinfo` must exactly match the GORM-derived authorizer read columns for every manifested table, including `sqlite_sequence` and explicit `sqlite_master` metadata. The empty-column `COUNT(*)` sentinel is validated separately and excluded from physical-column equality. The guarded app pool continues to deny runtime PRAGMA actions.
2. The fixed `fa_audit_no_replace` trigger receives only its required `SQLITE_SELECT` source exception and `SQLITE_READ` on `main.audit_logs.id` from that exact source. Unique-ID inserts should pass; collisions and `INSERT OR REPLACE` must abort without changing rows; wrong source/table/column tuples remain denied.

## Review outcome

- Architecture: PASS; no P0/P1/P2 findings.
- Security: PASS; no P0/P1/P2 findings. Reviewers emphasized preserving hidden/generated columns from `table_xinfo` and exact trigger/source/table/column matching.
- Implementation and regression evidence remain pending. This review approves the design amendment only; it does not claim the manifest assertion or audit trigger behavior is implemented.
