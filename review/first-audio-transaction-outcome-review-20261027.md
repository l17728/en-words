# First-Audio Transaction Outcome Review

## Scope

This review covers the failed-COMMIT cleanup fix in the local fork of
`github.com/glebarez/go-sqlite` v1.21.2, its `go.mod` replacement, and the
deferred foreign-key API regression. It does not approve or claim the complete
immutable guarded-pool implementation or classroom matrix acceptance.

## Findings and disposition

- Architecture review: **PASS**. The replacement targets the driver actually
  used by the GORM dialector. A failed COMMIT rolls back if SQLite still reports
  an active transaction; failed cleanup or a still-active transaction wraps
  `driver.ErrBadConn`, while preserving the original COMMIT error. The
  single-connection regression uses a TEMP table to verify same-connection
  reuse after successful cleanup.
- Security review: **PASS**. The fork source, upstream version, local patch,
  licenses, and checksums are recorded. The API/room error path exposes only
  fixed uncertainty status, not driver or rollback details.
- Regression review: local Go 1.25 targeted API tests and driver cleanup tests
  pass; `go vet ./internal/api` and `gofmt` pass.

## CI evidence

- [First-audio protocol checks, Linux and Windows](https://github.com/l17728/en-words/actions/runs/40493069515): **PASS** at `93b108d`.
- [Linux backend package regression](https://github.com/l17728/en-words/actions/runs/40492667934): **PASS** at `6a6e96a`; that commit contains the same runtime patch and API regression.
- The earlier run [40492667920](https://github.com/l17728/en-words/actions/runs/40492667920) failed because the nested driver's upstream test suite embeds `embed.db` and `embed2.db`, which the repository's `*.db` ignore rule omitted. Both 8 KiB upstream test fixtures are now tracked, and the later Linux/Windows run passed.

## Remaining acceptance gates

This is not guarded-pool fault injection. The immutable-authorizer App journey,
two-process Playwright reset journey, stale-operation/reset races, frozen
performance run, and human classroom pilot remain **NOT RUN** as recorded in
`docs/74-first-audio-matrix-isolated-backend-sdd.md`.

## Follow-up boundary correction — 2026-10-27

- Startup authorizer review found that `config_globals` reservation writes and
  `rev_config_globals_UPDATE` trigger reads/writes were broader than the
  documented matrix boundary. Those exceptions are removed. Regression tests
  now deny config/revision DML and trigger reads during app/preflight, and
  confirm a blocked config update does not append a revision.
- Reset identity now freezes an independent copy of the ordered `word_ids`
  vector at bind and after reset. Bind/reset responses and metadata expose
  copies. Reset requires exact ordered equality plus stable student/book IDs;
  contract drift reports `reset_outcome_uncertain` and terminates the run.
  A regression backend mutates the same backing slice returned by bind to lock
  the former aliasing failure.
- Architecture, security, and backend/testability reviews: **PASS**, no
  remaining findings in this diff. Reviewers did not execute tests.
- Go 1.25 CGO API regression (first-audio matrix, transaction outcomes, and
  deferred-commit regression): **PASS**. Python matrix-control suite: 20 tests,
  4 skipped, **PASS**. `go vet ./internal/api`, `gofmt`, and `git diff --check`:
  **PASS**.
- These checks do not implement or accept the guarded SQLite backend, isolated
  two-process Playwright journeys, performance run, or human classroom pilot;
  those gates remain **NOT RUN/open**.
