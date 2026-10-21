# Pilot catalog journal SDD test-design review

- **Verdict: BLOCKED**
- **Scope:** Read-only review of docs/41 ?109.3, cross-checked with ??109.1?109.2. No tests were run and no source or design files were changed.

?109.3 identifies the right acceptance themes and correctly avoids guessing whether an HTTP response was delivered after a complete Sync. It is not yet a deterministic, executable matrix. Crash points, failure seams and their state oracles, transition vectors, and concurrency schedules remain too general to audit consistently.

## Findings

1. **P2 ? Crash boundaries are ambiguous.** ?Every frame boundary? can mean between records or inside each DWC1 frame. The specification defines magic, length, payload, CRC, repeated length, trailer, file Sync, parent Sync, and response, but does not map crash injection points to expected on-disk bytes, restart verdict, recovered generation, or whether case files may be opened. Enumerate child-process crash hooks at each boundary with fixed payloads/IDs and an exact recovery oracle.

2. **P2 ? Fault injection is not mapped to outcomes.** Short write, partial frame, file Sync, and first-create parent Sync failures are named, but the required test seam and assertions are not. Distinguish zero-byte, partial, and full-frame writes followed by uncertain Sync; cover lock, seek/read, exclusive-create, and replay failures too. For every row assert return class, poison scope, no retry, restart behavior, and zero DB/sidecar/key opens where required.

3. **P2 ? Transition and capacity vectors are not traceable.** ?Every allowed and forbidden transition? and ?limits? lack an explicit matrix. Add one positive/negative vector per state edge, proving rejected appends do not change generation or acknowledge. Pin payload sizes 1/256/257, catalog bytes at max/max+1, frame count at max/max+1, and generation overflow.

4. **P2 ? Multi-process concurrency is timing-dependent as written.** Requiring two processes to yield contiguous generations does not specify how to force contention or check readers, same-case conflicts, and process exit while holding the lock. Use barriers rather than sleeps; assert a single contiguous global chain, legal per-case chains, no duplicate generations, no partial snapshots, and deterministic recovery on both Windows and POSIX.

## Areas reviewed

- Golden frame/CRC, canonical JSON, global generations, limits, Sync failure, restart, and multi-process append are all correctly identified as necessary categories.
- Windows/POSIX verified-handle, ACL/mode, file identity, and parent-directory durability are explicitly reserved as separate platform gates.
- No catalog/supervisor implementation is claimed by the SDD, so this verdict concerns completeness of the future acceptance contract only.

The JSON artifact contains the full evidence, risk, recommendation, reviewed-area matrix, and exit criteria.
