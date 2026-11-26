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

## Callback-capture follow-up (2026-10-27)

A CGO-only capture test registers a capture-only mattn `ConnectHook`, compiles `EXPLAIN INSERT`, and records actual trigger-source callbacks without executing bytecode. It checks that each observed tuple from `fa_audit_no_replace` passes the production policy, requires `SELECT`, `main.audit_logs.id`, and the empty-database/empty-column `audit_logs` table-existence read, rejects unexpected nonempty trigger sources, denies top-level audit INSERT, and confirms the fixture remains empty. SQLite special `RAISE()` syntax emits no function callback; the policy denies the synthetic `SQLITE_FUNCTION raise` tuple.

- Linux/Windows CGO capture fix passed at [40467106839](https://github.com/l17728/en-words/actions/runs/40467106839), commit `d55647b`.
- Removing the unobserved RAISE function allowance passed Linux/Windows, formatting, vet, and production-symbol checks at [40467507837](https://github.com/l17728/en-words/actions/runs/40467507837), commit `092c528`.
- Architecture and security review: PASS. Regression review confirmed gofmt and no stale RAISE-grant claims.
- Current CGO test hashes: startup `6B87D8D92C2A02B31E24F155F519F1E4C2A62A5349D5DC6F7333CC11D067DC29`; callback capture `9D4BB505414437598BC796841C27FF034AA94A1DAC36AD9D75321D19B87692B1`.

This remains callback and setup-pool evidence only. It does not implement guarded journey audit writes, immutable row context/UDFs, full backend lifecycle/reset, or two-journey browser acceptance.