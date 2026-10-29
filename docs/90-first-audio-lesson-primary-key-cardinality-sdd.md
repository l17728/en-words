# First-Audio Lesson Primary-Key Cardinality SDD

Date: 2026-10-29  
Status: implemented; reviewed; protocol CI passed on Linux and Windows (run 40826787583). Full backend regression is tracked separately.

## Goal and boundary

Establish against the real GORM/SQLite migrated schema that `lessons.id` is the unique row key used by the fixed first-audio statement, so `UPDATE ... WHERE id=?` cannot affect multiple rows. The dynamic-typing 18-column SQL harness fixture is not production schema and cannot serve as this evidence. This is a schema/cardinality component check, not guarded-App or classroom acceptance.

## Test: `TestFirstAudioLessonPrimaryKeyCardinalityRealSchema`

Open a fresh temporary database through `store.Open`. Before installing any guard authorizer, query `PRAGMA table_info(lessons)` and assert every schema row: exactly one column has `pk=1`, its name is `id`, its declared type is `INTEGER`, and all other columns have `pk=0`. This rules out a composite key and establishes the migrated schema's INTEGER primary-key shape. The duplicate-insert constraint check independently proves uniqueness; this test does not claim rowid-alias semantics.

Insert one synthetic `store.Lesson` with a unique ID, then attempt another insert with that ID. Require `errors.As` to the active driver fork type `*github.com/glebarez/go-sqlite.Error` (do not import `modernc.org/sqlite` separately, which would register a second SQLite driver instance) and `Code()==1555` (`SQLITE_CONSTRAINT_PRIMARYKEY`); query and assert the table still contains exactly one row with that ID. Do not accept a generic insert failure or a different constraint code. Execute the fixed no-op statement `UPDATE main.lessons SET id=id WHERE id=?` and require exactly one affected row. The separate docs/88 zero-row negative remains the evidence for a missing target; do not conflate these outcomes.

Emit only trace ID, module/event, PK name/type/count, duplicate-insert rejection and SQLite code, fixed statement identity, and affected-row count. Never log row contents or credentials.

## Review and validation gate

Obtain security and test-quality SDD review before coding. Add this SDD, its review artifact (`review/first-audio-lesson-primary-key-cardinality-review-20261029.md`), and the test file to Linux/Windows workflow path filters. Explicitly include this test in the focused Linux/Windows selector and Linux race selector. After code review, run Linux/Windows protocol CI, Linux race/vet/format, and full backend `go test ./...`. This closes only primary-key cardinality evidence.
