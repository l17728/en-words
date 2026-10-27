# First-audio audit collision trigger and read-tuple review — 2026-10-27

## Scope

This slice adds a fixed test-schema audit ID collision trigger and exact SQLite authorizer source tuples. It does not permit audit INSERTs through the guarded app pool and does not implement full row validation or reset.

## Implementation

The pinned SQLite schema contains 63 objects, with canonical schema digest `797ffcb2b01d472f29fc73ee42c9ba8874e86aa38e6102cd2f9a3fb4329ddb78`. `fa_audit_no_replace` aborts duplicate IDs in a `BEFORE INSERT` trigger, including attempts using `INSERT OR REPLACE`.

The pinned SQLite runtime emits `SELECT`, `main.audit_logs.id`, and an empty-database/empty-column table-existence read from `fa_audit_no_replace`; policy allows only these observed tuples in `STARTUP_APP` or `PREFLIGHT`. It emits no `SQLITE_FUNCTION` callback for special `RAISE()` syntax, so the policy grants no synthetic `raise` tuple. The config revision trigger may read only `main.config_globals.key` and `main.config_revisions.student_id/key/revision` from `rev_config_globals_UPDATE` in those same phases. Other trigger reads and all trigger exceptions in `DRIVER_PROBE` are denied. No audit DML exception was added.

## Verification

- CI [40461676896](https://github.com/l17728/en-words/actions/runs/40461676896), commit `614e9681b8f807d59d629b1a02f51a144d1928e7`: Linux and Windows CGO matrix checks passed; Linux `gofmt`, `go vet`, and production-symbol exclusion passed.
- Regression cases cover phase/source/table/column/database restrictions, exact config revision reads, and audit duplicate/`OR REPLACE` preserving the original row.
- Architecture and security review: PASS.
- Startup CGO test SHA-256: `D11492133C9AF4A023CA934B193ABC8E6A9F951845FD33463A96FACDBD5D6ED5`.
- Schema manifest file SHA-256: `60680FA43D27AE77011A64ED2AB95022DB60A922806AA02BFE6A5BFB9F03CC43`.

## Limits

The duplicate/replace data-integrity test runs against the setup SQLite pool. Direct authorizer tuple tests pin the callback policy, while the full workflow exercises guarded startup/config reservation; a guarded-pool audit INSERT is intentionally still denied until row-validation UDFs bind each row to the current lesson and generation. The real isolated backend, reset transaction, and two-journey Playwright acceptance remain **NOT ACCEPTED**.
