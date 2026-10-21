# Security Review: Pilot Catalog Journal SDD (§109)

Read-only review of `docs/41-试点课堂Start持久迁移内核SDD.md` §109 against §§42 and 108. Scope: catalog authorization, wire-format replay, transition legality, cross-process behavior, and crash consistency. No design/code was changed and no tests were run.

## Decision

**BLOCKED pending resolution of one crash-consistency contract contradiction.** The journal's trust boundary, strict framing, monotonic global generations, per-case transition table, lock ordering, and pre-open authorization are otherwise coherent with §§42/108. However, §109.3 requires startup to accept only complete valid prefixes ending at acknowledged append boundaries and reject any unacknowledged frame. The file format stores no durable acknowledgment bit, and a complete CRC-valid frame can remain visible after `Sync` returned an error. A restarted supervisor cannot distinguish that frame from one whose `Sync` succeeded. This makes the stated acceptance rule unimplementable as written.

## Finding

- **PSK-CAT-109-01 — P1, blocking: restart cannot identify an unacknowledged complete frame.** §109.1 appends a complete frame, calls `Flush+Sync`, and poisons the current supervisor on any Sync error ([SDD §109.1, line 128](/D:/en-words/docs/41-试点课堂Start持久迁移内核SDD.md:128)). §109.3 then says only complete prefixes produced at acknowledged append boundaries may load and any unacknowledged frame fails globally ([SDD §109.3, line 153](/D:/en-words/docs/41-试点课堂Start持久迁移内核SDD.md:153)). The wire format contains only payload, lengths, CRC and trailer ([line 126]); it does not encode whether the writer observed successful Sync. If a full frame reaches the page cache/storage but Sync returns an error and the process exits, replay sees the same bytes as a fully acknowledged frame. CRC and contiguous generation detect torn/corrupt data, not the Sync return outcome. Therefore startup cannot implement the “unacknowledged frame” distinction from this catalog alone.

  **Required resolution:** define restart semantics using observable evidence. Either (a) remove the unobservable acknowledged/unacknowledged distinction and specify conservative per-state recovery by rechecking underlying durable facts, while keeping partial/invalid frames fail-closed; or (b) introduce a separately justified durable acknowledgment mechanism and specify its own crash/error recovery. Do not claim the current frame format can detect a past Sync error.

## Controls that are coherent

- **Trust and integrity boundary.** §109.0 explicitly says the catalog is not HMAC-authenticated and relies on the verified owner-only handle, OS permissions and trusted-supervisor model from §42; CRC is correctly limited to torn/corrupt-frame detection. This is consistent with the documented exclusion of trusted-account compromise.
- **Wire format and replay checks.** Fixed-order canonical ASCII JSON, exact allowlist, no null/unknown/duplicate fields, length agreement, repeated length, trailer, CRC-32C, contiguous global generations and exact frame concatenation provide deterministic parsing. Empty existing catalogs, partial frames, invalid transitions, overflow and bounded-capacity exhaustion fail closed. Tombstone retention and no silent compaction prevent case-ID reuse/replay.
- **State machine.** `provisioning` precedes filesystem effects; `ready` requires verified durable initial files; `open_recovery_required` precedes sidecar/DB effects; only committed or proven terminal failure resolves that state; quarantine and cleanup states deny fresh opens; deleted IDs remain tombstoned. This aligns with §108's durable open marker and pending-unknown semantics. The `quarantined` receipt-first path is explicitly separate from fresh command authorization.
- **Authorization and lock order.** §109.3 reuses the two-phase catalog check → case lifecycle lock → catalog recheck → OpenVerified sequence from §42. Appends occur under the verified-handle global lock, while case operations hold the per-case lock; catalog holders never wait on case locks. No lock inversion is apparent from the stated protocol.
- **Resource exhaustion.** The 16 MiB/65,536-frame caps deliberately sacrifice availability once exhausted and fail globally; this is operationally bounded and explicit. Compaction is excluded pending a separately reviewed protocol, avoiding silent tombstone loss.

## Implementation gates and limitations

The document says this format is frozen before supervisor implementation; the catalog/supervisor is not claimed to exist. Implementation must still provide atomic first initialization, the verified-handle cross-process lock, full-chain validation before all DB/sidecar/key opens, state transitions under lock, sync ordering, and platform ACL/durability checks. The separate 16 MiB and frame-count limits also need to be enforced before append, not only after replay. This review does not verify any of those behaviors.

No tests, fault injection, lock contention, Sync failures, restarts, filesystem operations, or implementation checks were run.
