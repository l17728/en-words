# Linux Runtime Candidate Evidence Review

**Review date:** 2026-10-21  
**Verdict:** PASS for test-only candidate evidence; not canonical promotion.

## Reviewed artifacts and hashes

- Evidence bundle `runtime-candidate-evidence.tar.gz`: `D01111EB4429C82EE2DD101B38063A267454230192CBE125DEB4F24C41A268FB`
- Acceptance report `acceptance-report.txt`: `A0D82EC008C130FE2E06A2F0C006C4D84D7C7C4BBDA9AC8661C101324A246B9A`
- VFS flags candidate: `1FAFF171E8B563C3634C02946B798EA4369802C9DEAA08A4D08486D710D81F3B`
- Unix lock candidate: `0E01789A13A5DD2A12C2232536665EA586E746BAAEDC78860877C3DA4B4C896C`
- SHM lock candidate: `63DE6ADFB42C9A95EC3AB5C80FF535CB798DE38B6356A3479723A9C6EF31E382`
- Pager/delete candidate: `8833CEBEB3A2F675E3FEFA9B6E10F7BB25AA6A019FC7BAE0829762C049F50FE3`

Artifacts reviewed from `.test-artifacts/pilot-linux-current-20261021/manifest-80-final-gofmt-20261021T123921/runtime-candidate-20261021/`. The manifest and SDD source hashes recorded by the run match the reviewed docs/55, docs/56, manifest, and loader snapshots.

## Checks and results

- Confirmed candidate groups label themselves `CANDIDATE`, state parent VFS `unix`, pin SQLite 3.45.1/source ID, and limit scope to host-VFS behavior characterization.
- Confirmed all scenario raw-trace paths exist and their SHA-256 values match the candidate JSON references.
- Confirmed `test.exit` is `0`, the tagged candidate-generation test log passes, trace overflow is false, and the archive contains report, candidates, raw traces, and logs.
- Scenario totals: VFS flags 4 candidate / 1 not run; Unix locks 4 / 0; SHM 3 / 0; pager/delete 2 / 1.
- EEXIST sidecar reopen is explicitly `NOT RUN` because the syscall interposer was unavailable and no collision was injected. Hot rollback is `NOT RUN` because the crash-barrier generator is not implemented.

## Scope and limitations

Evidence is from Go 1.25.14 on Linux amd64 with CGO enabled, UID `nobody`, and ext4 (`/dev/vda2`). These artifacts characterize the host Unix VFS only. They are not canonical goldens, do not execute the private Pilot Control VFS, and do not change the 80 manifest rows from design-only / not-run status. No candidate was promoted by this review.
