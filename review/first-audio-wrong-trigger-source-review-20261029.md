# Wrong Trigger-Source Negative Design Review

## Scope

Design review for the isolated real-SQL negative that attempts to call the
row-guard UDF from an unapproved trigger source. This does not accept the full
docs/78 matrix, guarded `api.App`, listener-factory behavior, browser journey,
performance, or classroom pilot.

## Findings and disposition

- Security review: **PASS**. The existing finite authorizer manifest remains
  unchanged. A real `BEFORE UPDATE` trigger named `attacker_source_guard` uses
  the row-guard UDF in its `WHEN` expression; the active `PROBE_SQL` permit must
  reject the exact nonempty-source `SQLITE_FUNCTION` tuple before UDF entry.
- Testability review: **PASS**. The fixture uses a seeded lesson ID and exact
  no-op UPDATE, same connection/challenge/receipt/session binding, transaction
  epoch, isolated child process, and LIFO cleanup ordering: terminalize/retire,
  pinned handle close, then pool close. It asserts zero UDF callbacks, terminal
  receipt/physical-close evidence, and rejection of a valid later JOURNEY.
- The child creates no App/listener. Listener-factory count and full docs/78
  acceptance remain open for guarded-App integration.

## Validation status

The design, security, and test-quality reviews are approved. Reviewers confirmed
that the test uses the tracked raw connection, exact seeded lesson and no-op
UPDATE, asserts the exact trigger-source denial tuple and zero UDF callbacks,
and safely reaps the isolated child. The event log records the tuple and SQLite
result; the raw challenge token is zeroed after setup. Linux/Windows CGO protocol
CI is pending. Do not treat the earlier Python SQLite shape probe as acceptance
evidence.
