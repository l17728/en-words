# Full tagged Go validation r14 - 2026-10-17

Go: `go1.25.14 windows/amd64`. Working directory: `server/`.

- `go test -tags pilot_e2e -count=1 -coverprofile=../review/pilot-start-kernel-validation-20261017-r14-tagged-cover.out ./...` — PASS, 23 packages, 0 skipped, 0 failed. `-count=1` disables test-result caching.
- `go vet -tags pilot_e2e ./...` — PASS, exit 0, no diagnostics; copylocks findings are cleared.
- Default production-tag tests and vet were already PASS in r13 and were not rerun here.

Full per-package coverage is included in the tagged test log. The combined coverage profile is archived separately.

Logs: `pilot-start-kernel-validation-20261017-r14-tagged-test.log`, `pilot-start-kernel-validation-20261017-r14-tagged-vet.log`.
