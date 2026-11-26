# Pinned SQLite runtime trace candidates — 2026-10-21

**Evidence class: candidate only. These files are not canonical goldens and must not be used as approval of the private VFS.** They record host `unix` VFS behavior from pinned SQLite 3.45.1 on Linux/amd64/ext4. No production VFS registration or wiring was added.

The frozen source archive is `server/.test-artifacts/pilot-linux-current-20261021/pinned-sqlite-harness/frozen-source-final.tar.gz` (SHA-256 `350B8E35169985D7D357B9C6B58131FEF8F49A38D3F528D4DC529A70214159F9`). Runtime outputs and complete command logs are in the ignored sibling `server/.test-artifacts/pilot-linux-current-20261021/pinned-sqlite-harness/remote-artifacts-final/`; its collected artifact archive SHA-256 is `6D5DD414591A672C7A1983395DED8C6E6266494B2D50B3ADC247A5747BE5E90C`.

The focused candidate run used Go 1.25.14, Linux/amd64, cgo enabled, UID/GID 65534 on ext4. It verified SQLite version 3.45.1, number 3045001, source ID `2024-01-30 16:01:20 e876e51a0ed5c5b3126f52e532044363a014bc594cfefa87ffb5b82257cc467a`, and parent VFS `unix`. The main-open, WAL-create, existing-sidecar, and rollback-journal scenarios produced correlated raw traces. `SIDECAR-EEXIST-REOPEN`, all Unix lock cases, all SHM cases, and all pager/crash cases remain `NOT RUN`; no syscall collision injection or barrier harness was available. The 61-case callback manifest is valid and unique, but every callback behavior remains `NOT RUN`.

Tagged `pilotcontrol` normal, race, vet, and coverage commands all exited 0; statement coverage was 83.9%. A Windows `GOOS=windows CGO_ENABLED=0 go list` check excluded the Linux-only tracer. Raw command outputs, coverage profile, exact source hashes, per-scenario JSONL, and candidate records are included here. The detailed design review remains `docs/56-pinned-sqlite-golden-harness-delta.md` plus `review/pilot-sqlite-golden-harness-design-review-20261021.md`; a fresh implementation review of this final snapshot is pending.

## Frozen key hashes

- C tracer: `AEEAD1475B510F20EADC2FC790F519AD2311A8094DC25C2AB7872052CB7A16D1`
- Go tracer: `5FBFCAE17F9C5D2F93DE6F106D872CF0CA9B84D8314A51496B3E3C155B324208`
- Candidate test: `C562397AA92B500DCD572FA4C71E95DEBC0E20B048DCD9005401564C8218AEAD`
- Manifest: `BF5214C06F709C6FBD92C4D09E09B6DC3513327EA96F1CFE18EE3587762EF152`
- `docs/55`: `4386D1F6F3E272A1D0DB213CA81708767E89FD471FACB12E91BAF3D58B883451`
- `docs/56`: `32A3FE78D5307BE99778D3C221263DD53A27C9D19BD857805154A884AAC74BD7`
- Literal DDL fixture: `A32C7EAD5544FEF50576989C7655DD4A9D70B9AE21A2358F91824054BF45403D`
