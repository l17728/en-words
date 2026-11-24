# Runtime Evidence Schema Re-review — 2026-10-21

**Verdict: P2 remains for event-count semantics.** Exact docs/56 SHA256: `332A5912D97DBDEF2B33BF26A67288BB17CC2320D40DD26555F5E70BC4C0ADB5`.

The new child `started` flag resolves the prior ambiguity between a child that never launched and one that launched but failed before reaching its barrier. The never-started child now has a consistent `NOT RUN` status, `NOT_APPLICABLE` cleanup, and null exit/terminal/output artifact fields. `details` provides a place for scenario-specific observations without overloading the common expected/observed fields. These changes do not conflict with the 15 scenario rows.

**P2 — `event_count` lacks a defined counting basis.** State whether it counts normalized callback events, raw JSONL lines, or both; cross-process traces make this material, and a partially executed `NOT RUN` scenario can still have callbacks. Recommended rule: `event_count` equals the number of normalized callback events in that scenario's `events` array, including retained partial events for NOT RUN; it excludes protocol/control messages. A scenario that produces no callback events records zero. The candidate loader should assert equality with the array length.

No other schema issue found in the reviewed additions. The old runtime candidate evidence is not upgraded or approved by this schema review.
