# Linux Supervisor Race Follow-up — 2026-10-23

## Failure observed

GitHub Actions run [39947889582](https://github.com/l17728/en-words/actions/runs/39947889582) passed manifest and runner preflight plus quota journal coverage, then timed out after 600 seconds in the Linux `pilotsupervisor` race package. The blocked test was `TestConnectHostLoggerClientRejectsWrongServiceCredentials/uid`, with the stack in `serveLoggerReadinessOnce` cleanup at its blocking `unix.Connect` wake-up.

## Root cause and correction

The readiness fixture accepted only one connection. Wrong-credential validation retries, which could fill the listener backlog. Cleanup then issued a blocking local `Connect` to wake `accept`; when the backlog was full, that cleanup could block indefinitely. This was a test fixture deadlock, not a production logger-client hang.

The fixture now uses nonblocking listener and accepted sockets, gives one goroutine exclusive listener ownership, and stops/joins that goroutine with a one-second cleanup bound. A Linux-only regression fills the actual AF_UNIX backlog until `EAGAIN`, runs cleanup, and verifies the endpoint refuses a new connection. Production authentication and retry code are unchanged.

## Verification status

- `gofmt` and `git diff --check`: passed.
- Independent static review: passed with no P1/P2 findings. The reviewer verified listener ownership, bounded idempotent cleanup, and the deterministic backlog-full assertion.
- The first remote Linux runner was below its 1 GiB preflight disk requirement (785,113,088 bytes free), so it performed no upload or cleanup. The code was instead validated by the authorized GitHub Actions Linux runner.
- GitHub Actions run [39953367533](https://github.com/l17728/en-words/actions/runs/39953367533) succeeded on commit `f9420e7855a954ac5a02dc8f62d8edafe9491aca`. Linux supervisor race passed in 46.640 seconds with no data race; quota race, three vet checks, dual-architecture builds, full 25-package backend regression, and ext4 cleanup passed.
- Follow-up GitHub Actions run [39966201882](https://github.com/l17728/en-words/actions/runs/39966201882) also succeeded on `e85c7ea5148c318b66bcc8a7ca532b6d7c8b60bd`. It reran the manifest, runner preflight, and Linux package regression jobs after the acceptance records were updated; all three passed. The source code was unchanged from the successful `f9420e7` run.

This remains package-level verification. VFS candidate and product RunRoot acceptance were explicitly outside this workflow and remain open.
