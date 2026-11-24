# Pinned SQLite runtime trace candidates — final5

**Candidate evidence only; these files are not canonical goldens and do not approve or implement the private VFS.** The test-only wrapper records SQLite callbacks through the host Linux `unix` VFS. No production VFS registration or wiring was added.

The frozen source archive is `server/.test-artifacts/pilot-linux-current-20261021/pinned-sqlite-harness/frozen-source-final5.tar.gz` (SHA-256 `9F5BD546D1B55BA29AD6E18E2AB3498EC2DC9669CA7A9262B134692D5669988E`). The collected remote logs, coverage profile, and raw candidate data are in `server/.test-artifacts/pilot-linux-current-20261021/pinned-sqlite-harness/remote-artifacts-final5/`; the artifact archive SHA-256 is `F687E336B4A6E1B0F5EBE662458C502B271072A0B2CCF050350E15DEAADAA26A`.

The exact snapshot passed focused tests, tagged normal, tagged race, tagged vet, and tagged coverage; each command exited 0 and package statement coverage was 83.9%. The Windows `GOOS=windows CGO_ENABLED=0 go list` check exited 0 and excluded the Linux tracer. Runs used Go 1.25.14, Linux/amd64, cgo, UID/GID 65534 and ext4. The captured SQLite identity was version `3.45.1`, number `3045001`, source ID `2024-01-30 16:01:20 e876e51a0ed5c5b3126f52e532044363a014bc594cfefa87ffb5b82257cc467a`; its parent VFS `unix` is asserted and recorded.

The main-open, WAL-create, existing-sidecar, and rollback-journal candidate traces include xClose events; each raw JSONL hash matches the candidate record. Scenario/run/case/source IDs are attached, and the deterministic run ID is `pilot-vfs-runtime-candidate-v1`. Overflow or truncation invalidates capture; candidate JSON records `trace_overflowed: false`. A persistent absolute `PILOT_VFS_ARTIFACT_DIR` is required before any tracer registration or SQLite operation.

`SIDECAR-EEXIST-REOPEN`, all Unix lock cases, all SHM cases, all pager/crash cases, and all 61 callback behavior cases remain **NOT RUN**. No EEXIST injection or process barrier harness was available. The final5 exact-hash implementation review passed with no P1/P2 findings.

## Key source hashes

- C tracer: `077CF164C8119F99BD2710DF9117020699470ACAA466BCE1EDC163E94C80C6D1`
- Go tracer: `5FBFCAE17F9C5D2F93DE6F106D872CF0CA9B84D8314A51496B3E3C155B324208`
- Candidate test: `7FF2F2EAB38E0B557E618D1A70DC5812E35E99572651665D65947E9CEE71EA66`
- Manifest test: `A8488F9ACBCA2CFE9BEC79DAC5B7391771FC1275C85E7ADE19E507E68B8698B1`
- Callback manifest: `BF5214C06F709C6FBD92C4D09E09B6DC3513327EA96F1CFE18EE3587762EF152`
- `docs/55`: `4386D1F6F3E272A1D0DB213CA81708767E89FD471FACB12E91BAF3D58B883451`
- `docs/56`: `32A3FE78D5307BE99778D3C221263DD53A27C9D19BD857805154A884AAC74BD7`
- Literal DDL fixture: `A32C7EAD5544FEF50576989C7655DD4A9D70B9AE21A2358F91824054BF45403D`
