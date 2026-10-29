# Direct Row-Guard UDF Negative Review

## Scope

Review of the isolated real-SQL test for direct top-level invocation of
`first_audio_row_guard` under the token-consumption permit, plus the tracked
test driver's physical-close accounting. This is a component-level negative
case only; it does not approve guarded `api.App`, browser journey, performance,
or classroom acceptance.

## Findings and disposition

- Design review: **PASS**. `docs/85-first-audio-direct-row-guard-call-negative-sdd.md`
  specifies the SQLite authorizer denial, bounded child-process isolation,
  transaction terminalization, and physical connection eviction.
- Security review: **PASS**. The registered variadic UDF is called only by a
  real SQLite statement; the actual authorizer returns `SQLITE_AUTH` before the
  UDF callback. The manifest is unchanged, callback count remains zero, and no
  token material is logged.
- Testability review: **PASS**. Cleanup callbacks use LIFO order so terminal
  transaction cleanup precedes pinned-connection and pool closure. The direct
  denial count is recorded separately from the expected rollback-denial delta.
  Parent process enforces a deadline, waits/reaps its child, and caps output.
- Reviewers performed static review; they did not execute the Go test.

## Validation status

Local Windows validation is unavailable because `go`/`gofmt` are not installed.
Linux and Windows CGO protocol tests, formatting, vet, and production-symbol
checks are **PENDING GitHub Actions**. No test result is claimed until CI passes.

## Acceptance boundary

Passing this case closes only the direct top-level row-guard UDF call. Other
docs/78 negative cases, isolated backend and guarded-App integration, two-process
Playwright journey/reset races, performance matrix, and human classroom pilot
remain open.
