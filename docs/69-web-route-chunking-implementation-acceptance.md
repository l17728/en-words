# Web Route Chunking Implementation and Acceptance

## Design and implementation

Implementation follows the reviewed design in [docs/68](68-web-route-chunking-sdd.md). `App.tsx` loads all nine route pages on demand and preserves the existing auth, route identity key, and modal cleanup. A loading fallback exposes a status message and navigation controls. A rejected route chunk shows a recoverable error; a session marker allows one document reload per hashed build ID and remains set after a successful retry.

The Vite dev and preview middleware revalidates HTML and marks content-hashed JavaScript and CSS immutable. Dashboard notifications use a small semantic list and native progress control. Feedback results load as a page-level dynamic import. Because existing records can make that import load automatically on first Dashboard render, the size checker includes its static dependency closure in the Dashboard cold-payload total.

## Acceptance evidence

- `npm run build`: the exact source snapshot used by the latest recovery E2E passed on Node 25.2.1/Vite 8.2.2; Vite transformed 3,134 modules and completed in 1m23s after TypeScript. The build log is in `.test-artifacts/recovery-final-rerun-20261023/npm-build.log`; source hashes were identical before and after. Earlier source snapshots and the aborted build are retained in the [machine-readable report](../review/web-route-chunking-acceptance-20261023.json) for provenance.
- `npm run test:route-chunks`: 3/3 passed. Tests cover manifest closure, cycles, missing route reachability, invalid baseline provenance, gzip budget, and raw chunk limits.
- Route-chunk Playwright: 16/16 passed against a build created by its own test configuration. It covers bounded recovery and repeated failure, accessible loading controls, preview cache headers, guest/admin redirects, browser back, account replacement closing an owned modal, and direct loads/content for all nine routes.
- Current-code browser regression: bugs/smoke 9/9 and fulljourney 1/1 passed. The recovery suite passed 43/43 on the latest run. Before that, it exposed two test-window races: JD28's 15-second summary assertion expired about 0.46 seconds before the correctly ordered `lesson_summary` frame, and REC04's 15-second reconnect assertion expired before the existing exponential retry. JD28 now waits for the 12 observed mock audio segments, and REC04 waits 30 seconds for one retry window. The two focused cases passed and the complete recovery suite then passed 43/43. See the latency investigation SDD for the separate 31.05-second historical storage tail.
- `npm run lint`: exit 0. It still reports eight existing warnings in `src/components/LadderSettings.tsx`; the route-chunk files add no warnings.

The frozen-route payload report uses Node `zlib.gzipSync(level: 9)`, deduplicates shared files, and verifies baseline schema, gzip method, lockfile hash, Node/Vite versions, and positive baseline values. `/login` is 246,217 bytes gzip, 33.87% below baseline. Authenticated `/dashboard`, including automatically loaded feedback results, is 241,865 bytes, 35.04% below baseline. The largest emitted JavaScript file is 399,792 bytes. Every route payload and file hash is recorded in the JSON report.

Stats (316,504 bytes) and Admin (287,599 bytes) remain larger than the two critical-route 250 KB target, though both are below the former monolithic baseline. The reviewed SDD sets the strict size and reduction gates for Login and Dashboard; other routes are recorded for later optimization.

## Reviews and deployment scope

Architecture/performance review found no code P1/P2 after the baseline and Dashboard dependency fixes. Frontend/E2E review confirmed the route, retry, auth, and lifecycle checks; no code P1 remains.

The repository has no identified production static host or reverse-proxy configuration. Header behavior is verified for Vite dev/preview only. Before deploying elsewhere, verify that the host revalidates the HTML document and serves content-hashed assets with a one-year immutable cache policy. This deployment-specific check remains open until the target host is known.

## Remaining acceptance gates

- Exact-source `npm run build` passed after the latest route and recovery changes, and the full recovery suite passed 43/43. Route payload verification and its 3/3 unit checks also passed; evidence is recorded above.
- Production cache headers remain open because the deployment host/reverse proxy is not identified. Vite dev/preview headers do not establish production behavior.
- The 30-cold/100-warm synthetic first-audio matrix and isolated real-classroom latency pilot remain open. See the separate first-audio SDD; no browser stub result is evidence of audible onset or multi-student performance.
