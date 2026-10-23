# Pilot Supervisor Production Build Helper Implementation Delta

Date: 2026-10-22

## Change

Moved `runRootBasenameDigest` from `runroot_evidence_test.go` into `runroot_evidence.go`, retaining the `pilot_e2e` build tag and the exact domain-separated SHA-256 contract. The test-only duplicate and its `fmt` import were removed. The Linux Actions workflow now performs fail-fast production builds for Linux amd64 and arm64 with CGO disabled before candidate testing.

## Verification evidence

The implementation was reviewed against SDD `docs/67-pilotsupervisor-production-build-helper-sdd.md` (SHA-256 `3ECDC4F7A57F56425DE1837415D3649F685DB3BC8E75E12CD677FA99AA2B4B6B`). Architecture and backend source reviews passed with no findings. Source hashes are recorded in `server/.test-artifacts/pilot-supervisor-production-fix-20261022T191500Z/hashes-before.txt` and `hashes-after.txt`; all 51 target hashes match.

Reviewed implementation SHA-256: `runroot_evidence.go` `B39F4ED740080A387BC6B1FFFE7ECFE2966215361FCCDA4AB8565280135887DA`; `runroot_evidence_test.go` `C31A726D32FC5A07724EDA673B27C16284DBA1EC9B7AB28E9D900C8133D93B43`; `.github/workflows/pilot-control-cgo.yml` `7E2D24BDEE870D00811933E4E624E895FC1B574D9545AFB0F54751A77EF88F63`.

All recorded commands exited 0: gofmt, Windows `go test -tags=pilot_e2e ./internal/pilotsupervisor -count=1`, Windows race, tagged vet, Linux amd64 and arm64 `CGO_ENABLED=0 go build -tags=pilot_e2e ./internal/pilotsupervisor`, and actionlint on the workflow. Logs and exit files are in that artifact directory. Cross-compiled binaries were not executed.

After the focused fix, the default full Go suite and offline Pilot manifest/runner Python regression were rerun in `server/.test-artifacts/pilot-build-fix-regression-20261022T192645Z`: all commands exited 0, the Go suite reported 24 tested packages passing (26 total; the two pilot packages have no default-tag test files), and Python reported 24 tests with 14 passed and 10 environment-specific skips. No candidate was run; all 51 tracked validation-target hashes were unchanged across the run.

## Remaining boundaries

The original GitHub dispatch did not start a runner because account billing blocks Actions; the new workflow step therefore has local cross-build evidence but no hosted-run evidence yet. This fix has no product API or browser-visible behavior, so the direct production package build is the defect-specific regression gate. The product Playwright RunRoot journey remains unimplemented and is still part of the broader Pilot acceptance gap. Native Linux CGO/SQLite VFS, `strace`, privileged service, and real-classroom acceptance remain `NOT RUN`.
