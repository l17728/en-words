# Pilot `lesson.start` Kernel SDD — Second Test Contract Review

**Disposition:** Not approved for implementation yet. This read-only review checks the revisions against all six findings from the first test-contract review. No tests were run and no code or design files were changed.

## Finding-by-finding status

| First review finding | Status | Assessment |
|---|---|---|
| PILOT-START-TEST-01: audit persistence/linkage | **BLOCK** | Partially addressed by specifying one `audit_logs` row and a `TraceID=decision_id` lookup, but the accepted audit schema still omits schema version, scope, effect index, and queued status required by the SDD's own audit allowlist. Existing `AuditLog.Args` is reduced to `{"bytes":N}`, so these required values are not stored. No FK/unique constraint is added, and “禁止其它同trace记录” does not specify whether such rows are rejected before or after commit or how a trace collision is handled. Specify one internally consistent persisted audit representation and exact uniqueness/cardinality contract. |
| PILOT-START-TEST-02: codec and fingerprint vectors | **BLOCK** | The fingerprint algorithm is substantially improved and the provided SHA-256 vector is reproducible: the specified compact UTF-8 JSON hashes to `06e513f5380a577706fe27f69e73d02dcdbfbda56b1e9e3f7545d0fecb4a5ba9`. However, the active codec remains a field-name list without per-field types, array element types, or a concrete golden codec JSON fixture. “所有int/bool/string/数组/map...” does not say which fields have which types. Thus strict decoding and codec golden tests are still underspecified. |
| PILOT-START-TEST-03: commit-before-response-drop E2E | **APPROVE** | The revised harness hook is ordered after COMMIT and before writing the HTTP response, and it independently verifies receipt, checkpoint, audit, and both outbox rows before dropping the response. This proves the intended committed-then-lost-response scenario, assuming the implementation preserves this ordering and the harness cannot be configured by a request. |
| PILOT-START-TEST-04: `pending_unknown` recovery | **BLOCK** | The prose contract remains sound, but the test matrix still lacks deterministic held-writer/lookup timing, unavailable receipt lookup, and same-ID re-entry after the original writer settles. The added E2E covers a known committed receipt, not an unresolved/unknown commit. The “COMMIT前中断” case is useful but does not prove rollback evidence or distinguish `not_committed` from unknown. |
| PILOT-START-TEST-05: deterministic dual-pool idempotency | **BLOCK** | The test remains stated as “双独立 pool 并发”; there is no barrier/hold protocol ensuring both pools overlap in the same command before the first commit. The cardinality phrase “一条...序列” is not numerically explicit for Go (especially audit row and two outbox rows), and no identical receipt equality assertion is stated. A sequential test could still satisfy the written contract. |
| PILOT-START-TEST-06: log event/reason assertions | **BLOCK** | The required log fields and three event names remain, but accepted/replayed/rejected/unknown event sequences and allowed `outcome`/`reason` values are not enumerated. A no-leak assertion is required generally, but no concrete forbidden-field/value matrix is specified for serialized events. |

## Additional acceptance observations

- The canonical fingerprint vector covers nested field ordering, timestamp integer encoding, and basic UTF-8 input. It does not itself test escaped control characters, backslashes/quotes, non-ASCII text (which is explicitly not normalized), overflow/float rejection, or map key ordering; include representative canonicalization vectors if those encoder branches are supported.
- The E2E response-drop hook should be one-shot, inaccessible to ordinary request parameters, and fail the test if its independent post-COMMIT reads cannot verify all four persisted record classes before it drops the response.
- The SDD still correctly states that this isolated Start slice does not activate the classroom, outbox consumer, real audio/model, or human pilot.

## Required next revision

Before another implementation approval review, add a typed active codec fixture and explicit audit schema/cardinality; add deterministic two-pool barriers and the unresolved-COMMIT recovery matrix; and define event/reason enums plus executable log privacy assertions. The post-COMMIT response-drop E2E change is accepted.
