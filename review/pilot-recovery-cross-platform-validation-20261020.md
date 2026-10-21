# Pilot Recovery Cross-Platform Validation

Date: 2026-10-20

## Source binding

This report covers the recovery bootstrap/transport slice under `server/internal/pilotsupervisor`, build tag `pilot_e2e`. The four reviewed files were checked against the current source hashes:

- `recovery_bootstrap.go`: `EBA13B7E972A7B19EF80A0ECD33B86AA832A0CBE7294296783CC65F251481165`
- `recovery_bootstrap_test.go`: `5AD49D67F9057E80812F9DA200A518F9C780B1E045CA48F57470DAFE048E6B9D`
- `recovery_transport_linux.go`: `C6429C835CCA33EF7B8DBD2FAF444D96E85B2770CA6CF9DFF1722D645A3E0CB4`
- `recovery_transport_linux_test.go`: `3E19884F0CE8CE882D27FE1CC9A3925C29C78559C61A8C5493C1405B423B1AB5`

## Results

- `go test ./... -count=1`: PASS, 23/23 packages; API 60.648s and store 23.019s. Full package output: `%TEMP%\en-words-go-regression-20261020-234952\default-test.log`.
- `go vet ./...`: PASS.
- `go test -tags pilot_e2e ./internal/pilotsupervisor -count=1` on Windows: PASS.
- `go vet -tags pilot_e2e ./internal/pilotsupervisor` on Windows: PASS.
- Linux amd64 and arm64 pilot test binaries: cross-compilation PASS only. SHA-256: amd64 `D10CF43DEEECAA01D97DB0C4FCE5FD8D0C2AEBD0E956EAFF63BE51727E6152B7`; arm64 `472FF909B6EC7FF3306F7E1AEC44BF517D8176CD8719E1EBA5803DABDE3C7CAC`.
- Python review regression: PASS, 134 tests.
- `npm run build`: PASS; TypeScript and Vite. Main JS bundle warning at approximately 1,170 kB.
- `npm run lint`: exit 0; 8 existing warnings in `src/components/LadderSettings.tsx`.

The Go toolchain was Go 1.25.10, downloaded as a portable archive, SHA-256 verified against the official Go downloads page, and run without changing system PATH.

## Not evaluated / release gates

No Linux binary was run. Linux runtime, `-race`, native Linux vet, and privileged acceptance remain NOT RUN. Race was unavailable on this Windows host (`CGO_ENABLED=0`, no gcc/clang). The docs/47 privileged matrix still requires a disposable Linux amd64/arm64 VM with cgroup v2, systemd, delegated cgroups, separate service UIDs, pidfd/clone3 support, and the real launcher/Reaper topology. Product API/Agent/classroom integration and browser E2E are also not covered by this infrastructure slice.
## Reproduction commands (Windows PowerShell)

Commands below used the verified executable at `%TEMP%\en-words-go1.25.10\go\bin\go.exe`, working directory `server/`; every listed command exited 0:

```powershell
& "$env:TEMP\en-words-go1.25.10\go\bin\go.exe" test ./... -count=1
& "$env:TEMP\en-words-go1.25.10\go\bin\go.exe" vet ./...
& "$env:TEMP\en-words-go1.25.10\go\bin\go.exe" test -tags pilot_e2e ./internal/pilotsupervisor -count=1
& "$env:TEMP\en-words-go1.25.10\go\bin\go.exe" vet -tags pilot_e2e ./internal/pilotsupervisor
$env:GOOS='linux'; $env:GOARCH='amd64'; & "$env:TEMP\en-words-go1.25.10\go\bin\go.exe" test -tags pilot_e2e -c ./internal/pilotsupervisor -o "$env:TEMP\en-words-go-regression-20261020-234952\pilot-linux-amd64.test"
$env:GOOS='linux'; $env:GOARCH='arm64'; & "$env:TEMP\en-words-go1.25.10\go\bin\go.exe" test -tags pilot_e2e -c ./internal/pilotsupervisor -o "$env:TEMP\en-words-go-regression-20261020-234952\pilot-linux-arm64.test"
```

Cross-compile commands succeeded (exit 0) and produced test executables; they were not executed. Go command logs and binaries are in `%TEMP%\en-words-go-regression-20261020-234952` for this host session.