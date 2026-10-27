# First-Audio Write Inventory Design Review

Date: 2026-10-27  
Scope: first-audio-only write boundary; this review does not certify implementation or classroom acceptance.

## Reviewed baseline

- `docs/74-first-audio-matrix-isolated-backend-sdd.md` SHA-256: `AEB2CAF254B545CB7C9852DB54494A35B5D96C1FBA9362E9CBF70F1A74D43121`
- `docs/77-first-audio-write-callsite-inventory-sdd.md` SHA-256: `CD6FAC372B2E872F1F452468A898C3BAAF3EEF3888C078D3BD7A46BD2804AF99`

Architecture, security, backend, and testability reviewers approved this exact baseline. Reviewers did not execute tests.

## Findings closed

1. **Reset restored the interrupted first lesson.** Reset now updates only the synthetic lesson primary key and deletes only `snapshot/<oldID>`. It leaves both old/new keys absent; the second ordinary `Hub.Enter` creates a new Room, runs `Machine.Start`, emits `lesson_started`, and inserts a fresh snapshot. Reset itself creates no audit rows.
2. **GORM snapshot upsert lacked an OLD/NEW callback contract.** Initial insert requires same-transaction absence and exactly `[INSERT(expected NEW)]`. Update requires exact OLD bytes before DML and `[INSERT(expected NEW), UPDATE(expected OLD, expected NEW)]`; missing, extra, or reordered callbacks roll back before commit.
3. **Final database state could not prove cross-table chronology.** A child-private, non-persisted, closed-schema commit ledger now locks the five-event order for each journey. Only successful commits are recorded with `outcome=committed`; no user content or error text is captured.
4. **Reset snapshot diff and ledger fields were inconsistent in docs74.** Both design documents now agree: reset delta is only lesson-ID change plus old-snapshot deletion; new snapshot insertion belongs to the second journey.
5. **Immutable-context reset shape still granted a new-snapshot insert during reset.** Follow-up comparison against the approved reset lifecycle found that `docs/75` and its test-only guard context still represented three reset row contracts. The shape now permits only lesson UPDATE and old-snapshot DELETE. The reset-to-journey transition requires the next-generation snapshot UPSERT to have OLD absent, and regression cases reject the former reset INSERT and a resumed OLD-present snapshot.

## Implementation status

`first_audio_matrix_upsert_cgo_test.go` adds a private SQLite/GORM regression for the production `OnConflict(...).Create(RuntimeRecord)` expression, callback order/value checks, missing-OLD rejection, and rollback when the UPDATE guard is absent. Architecture and security static review passed. `gofmt` and the Python matrix-control suite (20 tests, 4 platform skips) passed locally. Local Go CGO execution did not reach tests: the Go 1.25 build stalled without compiler children after GCC was configured and was stopped at the diagnostic limit.

GitHub [run 40500366122](https://github.com/l17728/en-words/actions/runs/40500366122) at `420fbb0` passed on Linux and Windows. The Linux/Windows first-audio Go selector includes the renamed upsert cases; Linux CGO, immutable-context race checks, API vet, production-symbol exclusion, and Windows CGO boundary checks also passed. The Playwright step only discovers the spec with `--list`; it is not a browser journey run. This CI does not run the full backend suite or certify the guarded App matrix.

The guarded `api.App` integration, per-connection permit coverage, real two-process Playwright journeys, cross-reset races, performance run, and human classroom pilot remain **NOT RUN**. No reset, E2E, or real-classroom acceptance is claimed.

## Reset-context follow-up review

Architecture and security reviewers approved the follow-up change at these exact file hashes: `docs/75` `F48ED2180BDA3D2399E1E27D9022E982A265851426E772344E4F9FF87EBED75F`; context shape/transition `B589021CC97A130994E829472B6847C26E35791696F7BC611517E6590820A5C0`; transition regression `6C0F5F6A3F87F2FA13E8D6D995904D5FB56759A461301165967DB253237C8F90`; lifecycle regression `67A0D978B1ED75C3ECE0B991D4BB8C26A3B61FFCB47B06A2DFBD0174037F6622`. Backend/testability static review also passed. The reviewers confirmed this remains an in-memory test guard: it does not prove real same-pool absence readback, bind NEW bytes to a real Room, or implement guarded DML. Those integration criteria remain open.

The follow-up Python boundary suite passed locally (20 tests, 4 platform skips); `git diff --check` and `gofmt -d` were clean. Go/CGO execution for this follow-up is pending the branch CI run; the previous CI run at `3d7c815` does not include these uncommitted changes.
