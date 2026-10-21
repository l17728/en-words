# Pilot VFS Golden Promotion — Human Review Packet (2026-10-22)

## Decision requested

Review the raw Linux candidate traces and structured golden proposals below. Approve promotion only if the callback contract is represented faithfully and no proposal makes a filesystem-syscall claim from a host-VFS callback trace. Until approval and fixture promotion, these files remain proposals; the private pinned VFS, A0 ledger readiness, and Pilot route must remain disabled.

## Frozen candidate

- Run: `9ce1ac87ebce853e9a00484d899eb476` (`PASS_CANDIDATE`), Linux amd64, Go 1.27.3, SQLite 3.45.1 source ID `2024-01-30 16:01:20 e876e51a0ed5c5b3126f52e532044363a014bc594cfefa87ffb5b82257cc467a`, ext4.
- Raw archive: `review/pilot-vfs-golden-proposals-20261022/final9-timeout-fix-run-artifacts.tar.gz`, SHA-256 `D4CE7D0865F2948FF1F97EF89E4E9D8F2036A621B11A5E8CE86E064FE3DF50DD`.
- Run report and standalone raw traces: `review/pilot-vfs-golden-proposals-20261022/final9-frozen/en-words-final-20261022-timeout-fix/`.
- Independent implementation review: `review/pilot-vfs-candidate-acceptance-20261022.md` (architecture, backend and test PASS).

## Four promotion proposals

| Proposal | SHA-256 | Scenarios |
|---|---|---:|
| `sqlite3451-pager-delete-trace.golden.proposal.json` | `00D26C4EA30F5A66633F16178B39D33436D8B7BA79F94B009135F3BA0269D24D` | 3 |
| `sqlite3451-shm-lock-trace.golden.proposal.json` | `E79BA6401A96BD392D7057E02447CA83C76CD2A681A01AEBC639883EF8942755` | 3 |
| `sqlite3451-unix-lock-trace.golden.proposal.json` | `FD4C2CEDCD59BEAFFF4E230D0F8D64DC08678908C21FB40AD5EDABE999C91EC3` | 4 |
| `sqlite3451-vfs-flags.golden.proposal.json` | `CA93A68137ACDFE001EA49B1A5A6A0D20311F6D73C04F6723AACFC1EBBA8543E` | 6 |

## Review checklist

1. Confirm each proposal hash above against the file in `final9-frozen/en-words-final-20261022-timeout-fix/` and compare its scenario observations with the corresponding `*.raw.jsonl` trace.
2. Check the SQLite version/source ID, callback names, callback order, open flags, roles, result codes and explicit absent-callback outcomes. An absent `xDelete`/journal callback must remain “not observed”; it must not be inferred as a tested delete behavior.
3. Confirm the proposals are host `unix` VFS callback characterizations only. The separate `lock-syscalls.raw.log` is process-level supplemental evidence and is not correlated to SQLite file handles/inodes; neither source proves private adapter `openat2`, pinned-handle or namespace behavior.
4. Confirm run-to-run candidate hashes converge for the final corrected run family and that no trace overflow, missing barrier or child cleanup failure was waived.
5. Record `APPROVE` or `REJECT` with any corrections. Approval permits copying these exact bytes to canonical fixtures and running exact-hash comparisons; it does not approve private VFS or Pilot product readiness.

No canonical golden fixture has been modified by preparing this packet.


