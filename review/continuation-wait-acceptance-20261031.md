# WAIT39 continuation-wait acceptance review

## Scope

Reviewed the local playback-completion wait UI and `PlaybackContinuationWait` helper, including business-frame clearing, stale callbacks, reconnect behavior, account changes, REST fallback, and privacy-safe observations. The design contract is in `docs/39-课堂朗读后等待提示最小设计.md`; case definitions are in `docs/03-测试用例.md`.

## Review findings and closure

- P2: prior disconnect/account E2E cases replayed an already-fired callback and did not cover a first late `onend`. Added `disconnect_late` and `account_late`, which invoke the first callback after lifecycle invalidation.
- P2: the REST fallback case simulated `readyState=CLOSED` before the close callback while the app still considered the session online, but the design did not define this interval. The SDD now states that this short race may display waiting until REST failure/success resolves; after the lifecycle reports offline, connection/reconnect UI takes precedence.
- P1: token storage could change before React effect cleanup, allowing a stale `onend` to submit `tts_finished` even though it did not create a wait. Completion now rechecks the session token and current `TurnContext` before either creating the wait or sending the ACK. The account race test triggers the first old `onend` within the storage event and asserts zero WS and REST ACKs.

Independent follow-up review: **0 P0, 0 P1, 0 P2**.

## Verification

- Node helper tests: 5/5 passed.
- Playwright WAIT39 journeys: 6/6 passed, including late disconnect/account callbacks and REST fallback failure.
- `npm run build`: passed (`tsc -b` and Vite production build).

The browser harness uses controlled media and an isolated backend. It verifies UI/protocol behavior, not acoustic quality or the separate cold/warm first-audio performance matrix.
