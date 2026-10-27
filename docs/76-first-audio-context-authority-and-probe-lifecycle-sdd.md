# First-Audio Context Authority and Probe Lifecycle SDD

Date: 2026-10-27  
Status: design reviewed; the isolated no-SQL controller slice may proceed. SQL/backend integration remains separately blocked on an operation inventory and field-level trusted builders.

## 1. Scope

Define trusted construction and lifecycle rules around the test-only context in `docs/75`. The next implementation slice remains isolated from SQL: it may implement a controller, context transition validator, and one-use challenge ledger, but must not install SQLite callbacks, triggers, UDFs, or run application DML. Integration with `api.App` is a later gate in `docs/74`.

The context is a policy input, not an authority by itself. Only a controller created by the dedicated matrix harness may construct or publish contexts. A structurally valid caller-created context must not be enough to cross a phase boundary or approve a write.

## 2. Trust and secret boundaries

The current `ProbeContract` stores and returns raw token bytes. Before any connection callback consumes a token, replace that representation:

- The controller obtains 32 random bytes per live physical connection from `crypto/rand`, binding them to run ID, lesson ID, generation, context version, and connection ID.
- Contexts store only SHA-256 token digests and non-secret binding metadata. The secret bytes are held only by the controller until the probe is issued; accessors never return them.
- After issuing a challenge, discard controller references to the raw token and best-effort zero mutable buffers and temporary copies. Go/runtime and driver copies cannot be guaranteed erased, so tokens must never be serialized or retained for diagnostics and their live interval must end at probe completion.
- The probe callback hashes the submitted token and compares the fixed digest with `crypto/subtle.ConstantTimeCompare`. Raw values never enter logs, JSONL, errors, snapshots, database rows, CI artifacts, or test failure formatting.
- Each physical connection owns an immutable challenge record and an `atomic.Bool` consumed flag. A valid challenge consumes exactly once by compare-and-swap. Wrong binding, wrong digest, reuse, missing context, or wrong phase denies and increments a monotonic denial counter.
- Challenge records are installed before publishing the matching `PREFLIGHT` context. Each connection owns a probe receipt bound to run, lesson, generation, context version, connection ID, and probe ID. Its monotonic CAS state is `issued -> token_consumed -> row_guard_observed -> rollback_verified`; any error moves it to terminal failure and no state can be skipped or repeated. Only the probe executor receives the opaque receipt: the UDF may advance `token_consumed` and `row_guard_observed`, and only the executor may record `rollback_verified` after its transaction's `Rollback` returns nil. Wrong-connection, wrong-probe, duplicate, or out-of-order receipts fail closed. Token consumption alone is not proof that the lesson trigger ran or the transaction rolled back. Only after all expected connections reach `rollback_verified`, the complete fixture snapshot remains unchanged, and probe activity is drained may the controller publish `JOURNEY`; a late probe then fails by phase even if an old connection retains its record.

The CSPRNG source, token length, digest comparison, no-logging rule, single use, and reuse rejection are testable requirements. No raw token is retained by the immutable context or exposed by an accessor. The current test-only raw-token shape in `docs/75` is transitional and must be changed before any callback/SQL integration.

## 3. Trusted transition rules

All candidate contexts are created from controller-owned verified facts, never accepted as caller-provided replacement objects. Context version strictly increases on every publication. Run ID never changes. The controller rejects unknown transitions without changing the active pointer.

| Active phase | Allowed next context | Conditions |
|---|---|---|
| none | `STARTUP_PROBE` | Harness capability and isolated fixture setup validated; initial identity fixed. Admission remains closed. |
| `STARTUP_PROBE` | `PREFLIGHT` | `NewApp` completed; startup full-table snapshot equals baseline; no unexpected denials/guard calls; all live connections are registered. Admission remains closed. |
| `PREFLIGHT` | `JOURNEY` | Every pinned live connection consumed its one-use challenge, invoked the exact OLD==NEW lesson row guard, and had its probe transaction rollback verified; full fixture snapshot is unchanged; registry and denial counters reconcile. Publish and verify JOURNEY context, then atomically reopen ordinary admission. |
| `JOURNEY` | `JOURNEY` | Same run, lesson, and generation; strictly newer context version; expected row contracts came from an operation-specific trusted builder and canonical serializer, not caller-supplied OLD/NEW bytes. Close admission and drain every lease that could still execute SQL under the prior context before publishing the replacement. Replacement is complete, never merged. Reopen only after commit/readback verification and transition-lease release. |
| `JOURNEY` | `RESET_APPLY` | Close request/write admission; wait for every old-generation lease and in-flight side effect to drain; verify canonical old lesson/snapshot; reserve one unique next lesson ID and derive the exact reset rows. Admission stays closed while reset runs. |
| `RESET_APPLY` | next-generation `JOURNEY` | Reset transaction committed, same-pool readback and complete expected snapshot diff passed, and old-generation leases remain zero. Increment generation exactly once; publish/verify the new context, release the exclusive transition lease, then atomically reopen admission. |
| any nonterminal phase | `TERMINAL` | Any denied/uncertain write, commit ambiguity, counter mismatch, probe failure, snapshot mismatch, timeout, hook loss, or cleanup uncertainty. If old leases are drained, publish/verify terminal context. If a drain timed out, close admission and mark controller terminal out-of-band without replacing the pointer; preserve only sanitized failure evidence and terminate/reap the child. |
| `TERMINAL` | none | No recovery to an earlier phase and no new generation. Only metadata/shutdown protocol remains available. |

`RESET_APPLY` cannot be entered from `PREFLIGHT`; a reset cannot be replayed as a new transition. Idempotent reset-request replay returns the stored protocol response before transition validation and never republishes a context or increments generation. A failed/uncertain reset goes directly to terminal.

An atomic pointer swap alone cannot revoke a context already loaded by an in-flight SQLite callback. Therefore every transition that changes row contracts (`JOURNEY` to `JOURNEY` included) requires a write-admission close and a drain of all prior-context SQL leases before publication. No old-context callback may overlap the publication boundary. If draining cannot prove zero active leases, fail terminal without publishing. This is a mandatory invariant, not an optimization.

A drain timeout is not a normal context transition: enter controller-terminal state out of band, retain the old pointer for any still-running callback, keep admission closed, and delegate bounded force-termination/reaping to the parent. Do not publish a new pointer while an untracked old callback may still use the old one.
A drain timeout moves the controller to out-of-band terminal status immediately and closes admission; it does not pretend that pointer replacement cancels an old callback. While an old lease/callback is stuck, do not replace the pointer, reopen, take acceptance snapshots, or close a pool still in use. The parent applies the bounded process-tree termination/reap rule in `docs/73`; only after child exit may it record terminal cleanup evidence. The context phase becomes `TERMINAL` only after a successful drain makes replacement safe.
Closing admission also requires a specified reopen point. Successful transitions reopen only after the new context is published and reloaded/verified; transaction-bearing transitions additionally require commit/readback verification and release of the exclusive transition lease. Reopen failure or a context/version mismatch goes terminal while admission remains closed. Terminal admission is never reopened.

The per-operation OLD→NEW policy is a separate unresolved design input: before SQL integration, inventory every application write callsite and define an operation-specific builder/field allowlist for each allowed mutation. A generic builder accepting caller-provided canonical rows is forbidden. Each builder must read the old row inside the same transaction, validate the typed domain command, compute the exact expected new row, and serialize it canonically before the guarded DML. Uninventoried write operations stay denied. This document does not claim that the current table-level operation list in `docs/74` is enough to implement those semantic transitions.

## 4. Write admission fence

The controller owns admission state and lease counts keyed by context version (and generation). Any row-contract replacement and reset atomically close admission before waiting for old-version application leases to reach zero. New requests during drain are rejected without entering application code. Once all ordinary leases have drained, only the controller may acquire one exclusive transition lease. It opens a guarded transaction, reads OLD inside that transaction, validates a typed command, computes the canonical contract, publishes the context while ordinary admission remains closed, executes only the matching guarded DML, commits, and verifies same-pool readback. No other SQL operation may cross this publication. It then releases the exclusive lease and atomically reopens ordinary admission only after rechecking the active context/version. Thus OLD remains stable across read→publish→write without carrying an ordinary old-version lease over publication. A successful replacement without reopen is a liveness defect; reopening before publication/readback is a safety defect. Timeout, double release, underflow, generation mismatch, or uncertain cancellation is terminal. No SQLite authorizer/UDF callback may acquire the admission mutex or wait for a drain; callbacks consume one already-loaded immutable context and atomic counters only.

Normal-operation acquisition order is strict: lock the admission gate; reject if closed; load the active context pointer exactly once while still holding the gate; require its version to match the gate's open version; increment that version's lease count; then unlock and return a lease carrying that exact context pointer. No caller may load/retain context before acquiring the lease. On a nil/mismatched context, reject and do not increment a lease. All preparation, authorizer/UDF callbacks, execution, transaction completion, and result-row close associated with the operation remain inside that lease; release happens exactly once afterward. This lock ordering prevents a caller from pairing a stale pointer with a lease acquired after admission reopens.

The controller must not hold its lifecycle mutex while waiting for handlers or database work. Shutdown closes admission, drains or force-terminates the owned child per `docs/73`, closes the listener before the pool, and treats any unjoined effect or close error as failure. This slice tests the in-memory gate only; it does not claim that all application side effects are yet leased.

## 5. Implementation interface constraints

Keep types and methods unexported in `_test.go` files with `//go:build cgo`. Constructors and publish APIs are controller-only within the package. The publisher validates both phase shape and the transition table; it no longer accepts an arbitrary structurally valid phase from any caller. Publish uses monotonic CAS and rereads active phase/version on contention. A candidate is deep-copied before publication. Terminal publication is one-way.

The test-only context replaces raw probe tokens with fixed-size digests. The per-connection challenge record is immutable except for its atomic consumed bit and is not readable through general diagnostic APIs. Diagnostics expose counts, phase/version, and connection IDs only. Every callback-level denial includes an internal reason code and phase/version counter, never SQL arguments or secret bytes.

## 6. Required regression cases for the no-SQL controller slice

Tests in this slice use in-memory fake callbacks and an injected probe/transaction executor. They prove only controller state and receipt protocol behavior. A fake `row_guard_observed`, fake unchanged-snapshot result, and fake successful rollback are test inputs; none demonstrates that SQLite ran a trigger, left a database unchanged, or rolled back an actual transaction.

- Positive transition table and every forbidden phase skip/back-edge; rejected transitions leave the active pointer unchanged. A drain timeout must not publish a terminal pointer while an old callback is live.
- Admission stays closed through startup/preflight, opens only after JOURNEY publication, closes and drains before each contract replacement, and opens after verified readback and transition-lease release. Terminal never reopens.
- Run ID, lesson/generation identity, reset old/new IDs, and strictly increasing version binding; terminal is irreversible.
- Deterministic injected random source for tests plus production `crypto/rand` construction; reject entropy-source failure and short output. No fixed test token reaches logs/errors/artifacts.
- Correct per-connection challenge accepted exactly once; wrong token, wrong connection, wrong run/lesson/generation/version, reuse, missing challenge, and late token after `JOURNEY` all deny.
- Probe milestones are independent: simulated consumed token without a row-guard observation, simulated row-guard observation without verified rollback, or rollback without both prior milestones cannot complete preflight. These tests exercise receipt state transitions only.
- Eight or more concurrent connections: challenges cannot cross-accept, each connection consumes at most once, and all expected probes reconcile before phase publication.
- Deep-copy/context immutability and race tests for readers during legal publication; readers observe one complete version, and old callbacks fail closed after phase change.
- Lease tests: close-vs-acquire race, same-generation admission, reject new leases during reset, drain waits for outstanding leases, cancellation/release exactly once, underflow and timeout terminal behavior, no lifecycle lock held during drain.
- Lease/context binding race: a caller paused before gate acquisition cannot retain an old pointer, acquire a later lease, and proceed; the gate returns the pointer/version it bound during acquisition, and a mismatched version is rejected.
- Contracted-write ordering through a fake transaction executor: ordinary leases drain before the exclusive transition lease; OLD read, typed validation, context publication, guarded DML, and COMMIT are requested in that order. The fake readback occurs after simulated COMMIT, followed by lease release and reopen. Injected failure at each boundary leaves admission closed or terminal; this is not SQLite transaction evidence.
- Replay does not create a context version, consume a challenge twice, increment generation, or re-enter reset.
- Sanitized diagnostics/counters preserve trace and reason code while excluding tokens, digests, SQL, and user payloads.

## 6.1 Deferred SQL/backend integration acceptance

The following require the later reviewed SQL integration and cannot pass in this controller-only slice: actual per-connection SQLite trigger/UDF callbacks; a real exact OLD==NEW lesson update; actual transaction rollback verification; unchanged full fixture snapshots; guarded DML through the custom pool; commit/readback behavior; lease coverage of every real application SQL callsite; reset-fence races; and both sequential browser journeys. The test-only fake executor must never be reported as proof of these conditions.

## 7. Review and acceptance gates

Review this specification before coding with architecture, security, backend, and test reviewers. Before any SQL integration, add an operation inventory and field-level OLD→NEW contract table to this SDD or a reviewed companion SDD; do not invent defaults for unexamined callsites. After implementation, require deterministic unit tests, Linux `go test -race`, Windows CGO tests, `gofmt`, `go vet`, production-symbol exclusion, and full backend regression. Add first-audio E2E cases only when SQL/backend integration exists: stale operation racing context replacement/reset, timeout while draining, duplicate reset replay, terminal commit uncertainty, and both sequential first-entry journeys against one private database.

This controller slice is not the isolated backend. Only after its transition, token, and lease tests pass may a separate implementation review wire it into SQLite. Until the guarded `api.App` backend, real reset diff, two-journey Playwright E2E, cold/warm workload, and authorized human classroom pilot pass, matrix and classroom acceptance remain open.

## Design review record — 2026-10-27

- Architecture/lifecycle review: PASS at SHA-256 `3CBE3407C1194619C867858D15E65DAB6F88AD38E2E9B79675419D2CB109375C`; admission reopening, pointer/lease binding, transition transaction ordering, and timeout behavior were clarified after review findings.
- Security review: PASS at the same SHA; raw-token lifetime, digest-only contexts, receipt identity/single-use, and out-of-band terminal behavior were accepted.
- Backend feasibility and testability review: PASS for the no-SQL controller-only slice. Fake callbacks/executors validate controller protocol only; they do not provide SQLite/backend evidence.
- System gap review: PASS for proceeding with this slice. The remaining design integration blocker is inventory of real application write callsites plus field-level trusted typed builders and their app wiring.
- Reviewers did not execute tests. The later implementation must pass the CI and regression gates above; actual SQL, reset, browser, performance, and classroom acceptance remain NOT RUN/open.
