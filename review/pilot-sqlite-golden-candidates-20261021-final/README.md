# Pinned SQLite runtime trace candidates — final snapshot

**Candidate evidence only; these files are not canonical goldens and do not approve or implement the private VFS.** The test-only wrapper records callbacks from the pinned SQLite 3.45.1 build through the host Linux `unix` VFS. No production VFS registration or wiring was added.

The frozen source archive is `server/.test-artifacts/pilot-linux-current-20261021/pinned-sqlite-harness/frozen-source-final4.tar.gz` (SHA-256 `18D15DE277F2E16E438FE6DDD65FD2144C6F097B97F70876B6678D5460E8A853`). Collected remote outputs are in `server/.test-artifacts/pilot-linux-current-20261021/pinned-sqlite-harness/remote-artifacts-final4/`; the bundle SHA-256 is `694E229147719B1114BC993AE22CB5B323178A7F1A7FDD52CDB00D3C36EEAE52`.

The focused run used Go 1.25.14, Linux/amd64, cgo enabled, UID/GID 65534, ext4. It verified SQLite version `3.45.1`, number `3045001`, source ID `2024-01-30 16:01:20 e876e51a0ed5c5b3126f52e532044363a014bc594cfefa87ffb5b82257cc467a`, parent VFS `unix`, a successful `SELECT 1`, and explicit xClose callbacks in main-open, WAL-create, existing-sidecar, and rollback-journal traces. Event context is copied under the same mutex used for context updates. Truncation or event-buffer overflow invalidates candidate generation; candidate records expose `trace_overflowed: false`. Artifacts require an explicitly configured persistent output directory and use a deterministic run ID.

Focused, tagged normal, tagged race, tagged vet, and tagged coverage commands all exited 0; statement coverage was 83.9%. A Windows `GOOS=windows CGO_ENABLED=0 go list` check excluded the Linux tracer. The raw traces and runner logs are included below. `SIDECAR-EEXIST-REOPEN`, all Unix lock cases, all SHM cases, all pager/crash cases, and all 61 callback behavior cases remain `NOT RUN`; no collision injector or barrier harness was available. A fresh exact-snapshot implementation review is pending.

## Frozen key hashes

- C tracer: `077CF164C8119F99BD2710DF9117020699470ACAA466BCE1EDC163E94C80C6D1`
- Go tracer: `5FBFCAE17F9C5D2F93DE6F106D872CF0CA9B84D8314A51496B3E3C155B324208`
- Candidate test: `1D266448B28EC81F68B153D3A8B432CAB59A1DD840EFF9145650D978CDDB274F`
- Manifest test: `A8488F9ACBCA2CFE9BEC79DAC5B7391771FC1275C85E7ADE19E507E68B8698B1`
- Callback manifest: `BF5214C06F709C6FBD92C4D09E09B6DC3513327EA96F1CFE18EE3587762EF152`
- `docs/55`: `4386D1F6F3E272A1D0DB213CA81708767E89FD471FACB12E91BAF3D58B883451`
- `docs/56`: `32A3FE78D5307BE99778D3C221263DD53A27C9D19BD857805154A884AAC74BD7`
- Literal DDL fixture: `A32C7EAD5544FEF50576989C7655DD4A9D70B9AE21A2358F91824054BF45403D`
