# First Audio Matrix Warm Reset Design Review — 2026-10-23

Reviewed document: `docs/70-classroom-first-audio-latency-sdd.md`

Final reviewed SHA-256: `C94398EA366B8479DEB4ED5C1697721194316EFBDFEE46A0E109B2F5154D9ACB`

## Review outcome

- Architecture: PASS, no P1/P2.
- Backend: PASS, no P1/P2.
- Security: PASS, no P1/P2.

The final revision closes earlier P2 findings by requiring a dedicated Go test binary with no product reset route, a same-lock generation check and durable-side-effect admission barrier, draining of already admitted writes, fail-closed reconciliation for `outcome_uncertain`, a persistent scheduler/learning/report baseline diff, and explicit stale callback/cleanup fault cases. The control capability is treated as a secret and excluded from logs and artifacts.

The 30-cold/100-warm matrix is still NOT RUN. Implementation may start against this reviewed contract; the matrix cannot pass unless reset lifecycle and baseline tests pass first.

## Prototype implementation review

The first test-binary prototype was rejected before measurement. Backend and security reviewers found that `Room.Drop` followed by a best-effort `agentWriteMu` drain is not the shared generation/admission fence required by §7b; an old callback can still be waiting to start a durable write. Other findings: the log parser watched `outcome` while production emits `transaction_outcome`; the harness accepted an existing database under `.test-artifacts`; reset waits were unbounded; close errors were ignored; and lexical path checks did not resolve symlinks. The targeted Go command also failed to compile because `scanner` escaped its declaration scope and `itoa64` duplicated an existing package helper.

Disposition: the unsafe prototype was removed, no matrix was started, and no performance claim is made. A future implementation needs an ingress/lifecycle barrier shared with durable mutation admission, bounded drain and cleanup, a newly created private synthetic database, resolved-path validation, and regression cases before it can proceed to E2E or 30/100 measurement.

After prototype removal, `go test ./internal/api -run '^$' -count=1` passed as a compile check (5.450s). The complete API package regression then passed with `-timeout=10m`: 396 pass events, 0 failures, 2 skips, 68.571s package time. An initial diagnostic run with a 90s total timeout expired during the suite's intentional 10.1s delayed-cleanup case; the longer run completed that test successfully. Raw JSONL is retained only under ignored `.test-artifacts/`.

## 2026-10-26 design and inventory follow-up

The classroom first-audio SDD and production write-path inventory were revised after independent backend/security review found missing post-rollback audit writes, unclassified `ws_tickets`/pilot/duplex tables, inconsistent completion-key allowlists, and snapshot gaps during the user journey. The final SDD SHA-256 is `E4F163208407D80191476E9B10B61B1868F5BCE5931DCBC99528DCCF5AE8EF81`; the final inventory SHA-256 is `25A5A8FBB004D27C915B337A3F43BD43E5C092E5203004CABEA5E10F5AFE1D5E`.

Architecture, backend, and security reviews all passed these exact hashes with no remaining P1/P2 findings. The inventory reconciles production `tools.Tool` registrations/schemas and GORM/SQL mutation paths; unknown SQLite schema objects are fail-closed. The first-audio matrix allowlist is restricted to the synthetic lesson, its snapshot runtime key, and exact-trace append-only audit rows. Completion keys, classroom events, feedback/overdue/config/memory changes, and all memory-card rows are denied. Journey-boundary snapshots plus immutable prepare-time authorization and row-aware execution-time trigger/UDF checks cover transient writes, cached statements, rollback, and `INSERT OR REPLACE` collisions.

This is **design and inventory acceptance only**. The warm matrix remains **NOT RUN**. Implementation has not yet proven the generation/write permit, authorizer and trigger/UDF coverage on every physical pool connection, violation counters across rollback, or the required negative tests. Do not report a latency result until those implementation tests pass.
