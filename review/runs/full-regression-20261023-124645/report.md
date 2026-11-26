# Regression Attempt Report — 2026-10-23

Source tree at start and final recheck matched: SHA-256 `257b9464991a737484a9d31d02034d25fa094f876a8d28cd5132d1779aaf806e` over 512 tracked/untracked files under `server/` and `web/`; HEAD `5d66c5c20a6077cef6694205685733de1a3b4dc7`.

## Go

Toolchain: `D:\en-words\.test-tools\go1.27.3-extract2-20261022\go\bin\go.exe`, verified as `go version go1.27.3 windows/amd64`; GOROOT contains `src`, `pkg`, `api`, `bin`.

Attempt 1: `go test -json ./...` with per-run isolated GOCACHE, GOMODCACHE, GOPATH. The module cache was downloading dependencies for over five minutes; no package test events were produced. Interrupted. Log: `go-test.jsonl`.

Attempt 2: `go test -mod=readonly -json ./...` with `GOPROXY=off`, `GOSUMDB=off`, fresh GOCACHE/GOPATH and existing `D:\en-words\.test-tools\gomodcache`. Seven key dependency module directories were confirmed present. The Go process remained alive but made negligible CPU progress for over five minutes; log stayed 0 bytes, no test events or exit code were produced. Stopped.

Result: 0 reported tests, 0 passes, 0 failures; this is an incomplete/blocked run, not a pass. `go vet ./...` was not run because the Go test command did not progress to package results.

## Web and E2E

Command: `npm run build` (`tsc -b && vite build`). The `tsc -b` child remained active beyond observation, with low CPU and no diagnostic/log growth beyond the npm script banner. Interrupted with exit code 1. Log: `web-build.log`.

Playwright was not started because the required web build did not complete. No E2E tests ran.

All artifacts are under `review/runs/full-regression-20261023-124645/`; no application source was edited.
