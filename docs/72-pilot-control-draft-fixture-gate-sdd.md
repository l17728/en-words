# Pilot Control Draft Fixture Gate SDD

Date: 2026-10-23
Status: Design amendment for review; no schema promotion is authorized.

## Evidence

The embedded production schema inventory in `server/internal/pilotcontrol/schema_inventory_v3.json` pins 95 explicit objects and DDL digest `54558381…`. The available literal DDL and generated draft inventory are a separate 103-object candidate (including `intent_binding` and initialization-state objects); docs/65 and the review closeout explicitly say that candidate is not an accepted production baseline. However, four tests in `sqlite_test.go` call `createReviewedSchemaFixture`, which loads that draft and then exercise the production `Open` validator as though it were the approved schema. The Linux race log reports `TestOpenAcceptsExactPinnedSchemaFixture` rejects `table/intent_binding`; three other fixture users fail during setup because the helper's insert omits the draft's required `control_generation` column. The exact affected test IDs are `TestOpenRejectsUnexpectedPersistentView`, `TestOpenRejectsUnexpectedSQLiteUserVersionWithoutMutation`, `TestOpenAcceptsExactPinnedSchemaFixture`, and `TestProtectedConnectionRevalidatesSchemaAfterReadOnlyPreflight`.

## Requirements

1. Do not weaken or change production schema validation, approved inventory bytes, or pinned digests to make the draft pass.
2. Do not represent a design draft or a skip as successful schema acceptance.
3. Until [docs/65-pilot-control-ledger-schema-unification-and-test-harness-sdd.md](65-pilot-control-ledger-schema-unification-and-test-harness-sdd.md)'s canonical-input review and inventory reconciliation gates are complete, only the four named draft-fixture-dependent tests must report an explicit `SKIP` reason that names the unapproved 103-object draft and the required approval gate. Put the skip in each named test, not in the shared fixture helper, so a future caller cannot silently inherit the skip.
4. Keep ordinary negative tests for genuinely malformed/unrecognized databases active. Preserve their assertions that rejection does not mutate the database.
5. The package CI summary must include `V3_DRAFT_SCHEMA_FIXTURE=NOT_RUN`; CI success remains package-level and does not imply schema, private VFS, or product RunRoot acceptance.
6. Once the canonical DDL/inventory is approved and reconciled, remove the skip and run the positive Open, unexpected-object, user-version, and connection-revalidation cases against the exact pinned fixture with normal and race tests.

## Design

Each of the four named test functions will call `t.Skip` before creating the draft fixture, with one identical stable reason identifying the 103-vs-95 mismatch and docs/65 approval gate. The two-column fixture in `TestOpenRejectsUnrecognizedSchemaWithoutMutatingDatabase` remains active because it intentionally constructs a malformed legacy database. No runtime code, route, schema manifest, or hash changes are part of this amendment.

The workflow package-only summary adds `V3_DRAFT_SCHEMA_FIXTURE=NOT_RUN`. Contract tests bind this field to the same always-run summary step. Add `review/verify_pilot_draft_schema_gate.py` to parse `go test -json` events and require the exact four-test skip set, the stable reason on each skip, a PASS event for `TestOpenRejectsUnrecognizedSchemaWithoutMutatingDatabase`, no other skip/fail event, and package PASS. CI runs the parser on the actual Linux `go test -json -race` output. Review must verify the exact skip set and that no production file or schema pin changes.

## Verification and limits

On native Linux with CGO, run `go test -json -race ./internal/pilotcontrol -count=1` and pass its output to the skip-event verifier; it must verify exactly the four documented skips while all other tests pass. Run `go vet`, the full backend regression, and Linux tagged manifest tests. A Windows `[no tests to run]` result is not evidence for these Linux-only cases. These checks do not approve the 103-object draft or test its SQLite contract. That requires the separate docs/65 input-pin, schema reconciliation, harness, and expert-review gates.
