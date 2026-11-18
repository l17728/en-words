# SQLite startup race hardening acceptance

## Scope

Strengthen the independent-pool startup race test and make unpublished-pool cleanup observable. This does not claim acceptance of the guarded first-audio reset matrix.

## Review and corrections

Independent review found and the implementation closed these test-contract gaps:

- Reject any startup error result that also returns a database handle.
- Emit a `pool_cleanup` stage on startup failure and propagate `sql.DB.Close` errors instead of discarding them. The race test requires observed cleanup completion before deleting its diagnostic directory.
- Bound preflight probes, worker schema reads, reference schema generation, and recovery with contexts. Recovery errors retain the diagnostic directory and fail the test.

Final independent review: **0 P0, 0 P1, 0 P2**.

## Verification

- Busy classification, two-pool startup race, and partial-open cleanup repeated five times: passed.
- Full `TestStartup` group: passed; the Playwright-only harness case skips by design.
- Final recovery-path recheck after the last cleanup guard: two-pool race and partial-open cleanup repeated three times, passed.

Logs are retained under `.test-artifacts/startup-matrix-validation-20261031-r2/` and `r3/`.
