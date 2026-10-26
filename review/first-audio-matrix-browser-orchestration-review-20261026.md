# First-Audio Browser Orchestration Review — 2026-10-26

## Scope and reviewed source

This review covers the dedicated synthetic Playwright journey, its parent runner, process containment, and the corresponding protocol SDD delta. It does not approve the isolated SQLite backend or mark the acceptance matrix as run.

| Artifact | SHA-256 |
| --- | --- |
| `docs/73-first-audio-matrix-control-protocol-sdd.md` | `9E1639863558D6B19A12BF46E4860B6BCFC72C2B998DC09758F44CF01543E3B9` |
| `review/first_audio_matrix_control.py` | `E019A9CB12117300C2DCA24A78E3AE9B2FB1219A1471DB1BE6E358BD463E718F` |
| `review/test_first_audio_matrix_control.py` | `B2EBFCC6E175C8CB6986A980E515C4F1B8F08C424743BAFA950C8D25DED06C2B` |
| `web/e2e/first-audio-contained-spawn.cjs` | `1C6C2FF88826B738027C8A28BFE9A529EC9A64238EEFBB1BB76DEC3DE981E139` |
| `web/e2e/first-audio-matrix.spec.ts` | `477602D86F896467769B1B6DA83490337776B4FC619487C42C8003885C24F57A` |
| `web/playwright.first-audio-matrix.config.ts` | `E1E55EFC515988207C694F246F833A01F835FAB93163ACC8D2FB56A5CDF23D5F` |
| `.github/workflows/first-audio-matrix-protocol.yml` | `AE15DB03E3AB500832158667E07C994EFE906C5B19CC68BE53BF4E95CBC6880F` |

## Domain review

| Domain | Result | Decision |
| --- | --- | --- |
| Architecture | PASS | Windows/Linux process containment, timeout and normal-exit cleanup ordering, and the parent-controlled two-journey/reset sequence are coherent. Other POSIX platforms fail before the Go harness starts. |
| Security | PASS | The child environment is allowlisted; web server credentials are masked. Linux detached process groups are synchronously recorded with PID/start time in a private, exclusive manifest, read with a 16 KiB bound and 128-record limit, identity-checked, and admitted once to avoid PID reuse. `/proc` and manifest failures fail closed after cleanup attempts. |
| CI | PASS | Ubuntu and Windows jobs install Node 22 and the web lockfile dependencies before the Python boundary suite, so Linux process-group tests cannot silently skip for missing Node. |

Reviewers examined the current runner hash above. The architecture reviewer rechecked the exact runner hash after the final bounded-read change. No P0/P1/P2 findings remain in this browser-orchestration scope.

## Verification and limits

- Python parent-runner suite: passed; 4 Linux/POSIX-specific checks skipped on the Windows host.
- Node preload syntax/load check: passed.
- Playwright discovery: one dedicated test found; browser journey not executed.
- Web production build: passed (3,134 modules).
- Focused Go synthetic-login test: passed on Windows/amd64 with `CGO_ENABLED=0`; this does not exercise SQLite CGO hooks.
- Linux process-group behavior still requires the Linux CI job. The actual two-journey acceptance remains **NOT RUN** because the guarded SQLite backend, reset-race cases, and canonical snapshot gate are not implemented.

This review approves only the parent/browser orchestration delta. It is not evidence of acoustic onset, classroom latency, reset correctness, or pilot readiness.
