# Pilot start kernel validation — 2026-10-17

Go toolchain: `go1.25.14 windows/amd64` at `D:\en-words\.test-tools\go\bin\go.exe`.

Final validation, all run from `server/` against the final working-tree files:

- `go test -tags pilot_e2e ./internal/agent ./internal/scripts ./internal/store -count=1` — PASS (exit 0). See [tagged test log](pilot-start-kernel-validation-20261017-final-pilot-tags.log).
- `go test ./...` — PASS (exit 0), all 20 listed packages passed. See [production test log](pilot-start-kernel-validation-20261017-final-production-test.log).
- `go vet ./...` — PASS (exit 0). See [vet log](pilot-start-kernel-validation-20261017-final-production-vet.log).
- Coverage was not collected; `-race` was not run.

During the first run, tagged store tests failed with `pilot start sidecar integrity error`; the workspace was concurrently being corrected for Windows DACL semantics and SDD AttemptNo/golden alignment. The final uncached tagged run passed. The first production run had a single transient `TestCov_WSClientBackpressure` failure; an isolated `-count=1 -v` rerun passed, and the final full production run passed. Its diagnostic log shows the expected socket-close path while the test succeeds.

No source or documentation files were edited by this validation task. Detailed initial and repeat diagnostics are also in the `pilot-start-kernel-validation-20261017-*` logs.
