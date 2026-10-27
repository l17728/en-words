# First-audio startup policy implementation review — 2026-10-27

## Scope and evidence

Reviewed only the isolated matrix startup/SQLite policy slice at commit `5dbd47772b0c96b5171c4a78a12a94c0e112db6f`. CI run [40438989866](https://github.com/l17728/en-words/actions/runs/40438989866) passed the targeted Go tests on Linux and Windows. Windows explicitly ran with `CGO_ENABLED=1` and MinGW GCC; Linux also passed strict `gofmt`, `go vet`, and production-binary symbol exclusion.

The architecture reviewer approved the narrowed authorizer policy and current docs. The security reviewer approved the same startup-policy scope. The backend test review confirmed the fixture and startup guard tests are included by the CI filter; its earlier concern about Windows CGO coverage was closed by the explicit compiler setup and successful Windows test step.

## Findings and resolution

Earlier review found overly broad SQLite function/action exceptions, including an unnecessary `substr` permission. The implementation removed `substr` and constrained the remaining function/action permissions by phase, database object, and trigger source. User and config writes are additionally rejected by fixed `BEFORE` row triggers; the regression asserts blocked config writes do not emit a revision. These findings are closed for startup policy only.

## Limits

This review does not approve the full isolated backend or matrix. Checked-in schema digest/object inventory, immutable lesson/runtime/audit policy, complete snapshots, reset fencing and drain, stale-operation races, dedicated two-journey Playwright, warm-reset workload, and full acceptance review remain open. Warm-reset matrix status: **NOT RUN**.
