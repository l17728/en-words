# Pilot catalog journal SDD test-design review r2

- **Verdict: BLOCKED**
- **Scope:** Read-only follow-up on docs/41 ??109.3?109.4. No tests were run; no code or SDD changes were made.

The revision closes all four r1 findings. ?109.4 now specifies frame-prefix crash outcomes, distinct zero/partial/full write and Sync failures, typed errors and poison scope, transition/size/frame/generation vectors, and named-barrier process tests without sleeps. The remaining blocking gap is a concurrent same-case Start race.

## Open findings

1. **P2 ? Same-case Start winner/loser is not tested across processes.** ?109.3 says one Start per case and serializes same-case HTTP operations under the lifecycle lock. ?109.4 tests concurrent appends to different case IDs and second commands, but not two Start requests that both read `ready` before racing for the same case lock. Add a barrier-controlled race; assert exactly one `ready ? open_recovery_required` append, the loser sees the new state and is rejected before DB/sidecar/key opens or Planner calls, and both lock acquisition orders preserve one generation chain.

2. **P3 ? Parent-directory Sync success before a lost response lacks its own row.** The initial catalog creation needs both file Sync and parent-directory Sync. Add a crash after both succeed but before response; recovery should accept the chain and callers should converge by reading state.

3. **P3 ? Fixed IDs for golden vectors are implicit.** Golden bytes require deterministic case IDs, but production IDs are CSPRNG-generated. State that golden tests inject fixed IDs/generations through a test-only source and test production ID freshness separately.

## Closed r1 findings

- Crash points now distinguish strict frame prefixes, complete bytes before/during Sync, after successful Sync before response, and cleanup deletion boundaries, with restart and open-count oracles.
- Fault tests distinguish zero-byte, partial, and complete-frame writes followed by Sync failure; they assert no success, no retry, poison scope, typed result, and expected DB/sidecar/key opens.
- Transition and capacity vectors now include invalid transitions without writes, payload edges, exactly 16 MiB/65,536 frames and the next rejected append, plus generation overflow.
- Cross-case concurrency now uses named barriers, reader visibility checks, process death while holding the lock, bounded completion, and Windows/POSIX execution.

The JSON artifact includes evidence, risks, recommendations, and closure status for each r1 finding.
