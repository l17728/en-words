# Quiz Voice Confirmation SDD

Status: implemented; design and code review approved; targeted and full Go acceptance passed. Scope is the unanswered-word branch of the classroom quiz.

## Problem

The voice command parser recognizes “不会” as `skip` during `wait_answer`, but `Room.applyCommand` only handles that command during `repeating`. The quiz transcript is therefore consumed without submitting an answer. Typed “不会” falls through to semantic L2 because it is missing from the deterministic give-up phrases. This contradicts the product decision that knowledge confirmation is a simple yes/no decision.

## Contract

- The quiz accepts either a recalled meaning or a self-report. The UI will say “说出中文意思，或确认认识/不会”; an exact positive self-report is intentionally sufficient for this quiz item.
- Confirmation matching uses the existing `normalizeZh`: Unicode lowercase, remove whitespace and punctuation, then strip one leading/trailing filler token. Only the listed whole normalized phrases match; embedded words in longer answers do not.
- Positive phrases: “会”, “会了”, “知道”, “认识”, “记得”. Negative phrases: “不会”, “不知道”, “不认识”, “不记得”, “想不起来”, “忘了”, “不清楚”, “没学过”. Negative is checked first. “不是不会” is not a negative confirmation and continues through ordinary meaning judgment; it must not be rejected by substring give-up matching.
- These exact positive/negative confirmations return deterministic correct/wrong and never call L2. Longer sentences continue through ordinary answer judgment.
- In `wait_answer`, “不会” submits the deterministic negative answer through `EvAnswerSubmitted`, preserving the two-attempt policy: first wrong answer reteaches; second wrong answer moves on and records the word as not mastered. Explicit “跳过”/“跳过这个词”/“过” from either voice or the answer box advances immediately through a new `EvQuizSkipped` transition. The server performs the conversion after the current event envelope is accepted, preserving event-id and turn idempotency.
- An explicit quiz skip is a wrong first attempt: set the current item judgment to wrong, set `FirstWrong` and reset streak/consecutive mastery, add to `FinalWrong`, then advance without RETEACH. Do not increment `ReteachN`: it is the count used for reteach encouragement and must not claim a reteach occurred. The word remains unsettled because it is absent from `Mastered` and present in `FinalWrong`. A duplicate/stale skip is a no-op.
- Append one zero-millisecond sample to `AnswerMs`, matching the current immediate submission path (which also appends a zero); this makes the existing average metric denominator consistent, without inventing a duration not supplied by the event.
- During `judging`, additional confirmations or skip commands cannot start another judgment or advance the item. Repeat-phase `skip` retains its current behavior.
- Richer semantic answers retain current L1/L2 behavior. This change does not alter ASR, prompts, providers, or classroom phase rules.

## Tests and observability

Go unit tests cover exact phrases, normalization, negation precedence, longer text, no LLM calls, and decision source/reason. State-machine/API tests cover skip accounting (including `ReteachN`, `AnswerMs`, report, review list and unsettled state), “不会” first-attempt reteach and second-attempt advance, repeated event IDs, judging-phase duplicate rejection, and no duplicate progression. Playwright covers signed-in classroom transcripts for positive and negative confirmation, immediate typed skip, and a duplicate/late submission; it asserts the prompt text and exactly-once progression. `judge_decision` records bounded deterministic judgments; `quiz_skipped` records only lesson/item IDs and fixed outcome, never transcript text.

## Review and acceptance record (2026-10-30)

- Architecture and test design reviews approved the contract. Code review caught that typed “跳过” still reached ordinary judgment; the event ingress now converts exact skip aliases only after turn/event validation. Follow-up review confirmed stale-turn protection and bounded logging; no transcript is logged.
- Three-word Playwright classroom journey passed (`1 passed`, 2.1-minute test / 2.6-minute run): first “不会” triggers RETEACH, second “不会” advances, “认识” passes, typed “跳过” records one wrong item and advances, and a replay using the old turn is rejected. The same journey checks the prompt, review playback, summary and lesson report.
- Full backend `go test ./...` passed (about 80 seconds); `go vet ./...` passed (14 seconds). Targeted judge/classroom/voicecmd and skip API regressions passed. `npm run build` passed (3134 modules).
- Acceptance proves the server behavior after a transcript is received. It does not measure microphone recognition accuracy or qualify a real classroom acoustic pilot.

## Acceptance boundary

This proves deterministic handling after a transcript reaches the server. It does not claim microphone recognition quality or real-classroom acoustic acceptance.
