# Linux Supervisor Race Follow-up — 2026-10-23

## Failure observed

GitHub Actions run [39947889582](https://github.com/l17728/en-words/actions/runs/39947889582) passed manifest and runner preflight plus quota journal coverage, then timed out after 600 seconds in the Linux `pilotsupervisor` race package. The blocked test was `TestConnectHostLoggerClientRejectsWrongServiceCredentials/uid`, with the stack in `serveLoggerReadinessOnce` cleanup at its blocking `unix.Connect` wake-up.

## Root cause and correction

The readiness fixture accepted only one connection. Wrong-credential validation retries, which could fill the listener backlog. Cleanup then issued a blocking local `Connect` to wake `accept`; when the backlog was full, that cleanup could block indefinitely. This was a test fixture deadlock, not a production logger-client hang.

The fixture now uses nonblocking listener and accepted sockets, gives one goroutine exclusive listener ownership, and stops/joins that goroutine with a one-second cleanup bound. A Linux-only regression fills the actual AF_UNIX backlog until `EAGAIN`, runs cleanup, and verifies the endpoint refuses a new connection. Production authentication and retry code are unchanged.

## Verification status

- `gofmt` and `git diff --check`: passed.
- Static implementation review by the author: no P1/P2 found; independent review remains pending.
- Linux rerun: not run. The available remote ext4 runner had 785,113,088 bytes free, below its 1 GiB preflight requirement; no files were uploaded or removed.
- The fix still requires a GitHub Actions Linux rerun before the race gate can be marked green.
