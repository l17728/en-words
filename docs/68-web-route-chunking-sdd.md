# Web Route Chunking SDD

## Status and scope

Implemented and accepted for the web application. Architecture/performance and frontend/test-design reviewers approved the exact design hash recorded in `review/web-route-chunking-sdd-review-20261023.md` before implementation. Acceptance evidence and deployment-specific open checks are recorded in `docs/69-web-route-chunking-implementation-acceptance.md`. The pre-change 2026-10-23 build transformed 3,134 modules into one 1,169.93 kB JavaScript entry chunk (376.27 kB gzip), above Vite's 500 kB warning threshold.

This change reduces cold-route download and parse work by loading route pages on demand. It does not change APIs, authentication rules, classroom state, voice capture/playback, or teaching behavior. Use `React.lazy` for Login, Books, Dashboard, Placement, Classroom, VoicePreflight, Stats, Admin, and Settings. Use an error boundary as well as Suspense; Suspense alone does not catch rejected module imports. The current App has no persistent navigation shell outside the route tree, so loading/error fallback UI must include its own back control and authorized dashboard link.

## User-visible behavior

- Direct navigation to every existing route continues to render the same page and preserve current auth redirects.
- While a route chunk loads, expose a small accessible `role="status"` indicator.
- A chunk load failure shows a recoverable error with Retry; it never leaves a blank screen or mutates session state. Since `React.lazy` caches a rejected promise, Retry reloads the current document once, preserving local session state. Track one retry per build ID in `sessionStorage`; if storage is unavailable, use the current history entry state. A second failure for that build shows a stable update-required error and does not reload again.
- Revalidate the HTML entry document while keeping content-hashed JS assets immutable. This allows the bounded retry to receive the current chunk manifest after deployment.
- Preserve `Modal.destroyAll()` cleanup and route-key session reset. If navigation or account replacement occurs while a chunk is pending, existing page cleanup closes old classroom WS, microphone capture, playback queue, and TTS; a late module resolution cannot mount or resume the prior account's classroom.

## Performance contract

Before changing code, archive the current clean build and measure its `/login` and authenticated `/dashboard` baselines with the same Node `zlib.gzipSync(level: 9)` function used after implementation. In baseline mode, the current statically imported application has no route-specific manifest dynamic entry, so both route baselines are the HTML entry plus its complete transitive static `imports` closure. Record source, lockfile, raw-byte, and gzip-byte hashes. Keep the older 376.27 kB Vite reporter value as historical context only; it is not the acceptance denominator.

Compare clean production builds before and after from the same lockfile and toolchain. Measure two cold-route payloads: `/login`, then authenticated `/dashboard`. In post-change mode, the checker uses a frozen explicit URL-to-manifest-key table (`/login` → `src/pages/Login.tsx`, `/dashboard` → `src/pages/Dashboard.tsx`, and likewise for each tested route). Resolve exactly one matching route entry from the HTML entry's reachable `dynamicImports`; fail if a key is absent, ambiguous, or unreachable. Classify every dynamic import reachable from each route's static closure as either initial (included in its cold payload) or deferred (loaded only after interaction); fail closed when a dynamic import is unclassified or classified more than once. Exclude `dynamicImports` on the HTML entry during this page-level classification: some Vite/Rolldown manifests include the HTML entry in each route's static-import closure, and its dynamic imports describe the route registry already checked above. For each route, count the deduplicated manifest closure containing the HTML JS entry, its transitive static `imports`, and that route's dynamic entry plus its static imports and initial dynamic dependencies. Exclude other route-only dynamic imports. Enable the Vite build manifest and calculate each JS file's gzip size with Node `zlib.gzipSync(level: 9)`, then sum compressed file sizes.

Each post-change cold-route payload must be at most 250,000 bytes and at least 25% smaller than its corresponding same-algorithm pre-change baseline. No emitted route/shared JS chunk may exceed 500,000 minified bytes. The checker fails on missing manifest entries, cycles/unresolved references, or an empty route closure. Record build command, source and lockfile hashes, each closure's raw/gzip sizes, and elapsed build time. Record cold-cache `/login` and authenticated `/dashboard` browser navigation timings on the same local browser/machine before and after; these timings are diagnostic, not a production latency claim. If route-level splitting cannot satisfy the size contract without risky manual vendor grouping, stop and return to design review instead of changing the threshold.

## Validation

1. Build production assets and assert both route-closure size contracts from the generated manifest/output, not from a hand-maintained estimate.
2. Playwright headless Chromium: direct-load and navigate through login, books, dashboard, placement, classroom, voice preflight, stats, admin, and settings; assert each page's existing primary heading/action.
3. Inject a first-request failure for a route chunk, restore the chunk response, click accessible Retry, and prove one reload restores the page without logout or duplicate business submission. A second failure for that build must show stable update-required UI without another automatic reload, including when browser session storage is blocked.
4. Cover browser back/forward; assert visitors redirect to `/login`, non-admins visiting `/admin` redirect to `/dashboard`, account replacement closes the previous account's modal, and classroom navigation/account replacement during a delayed chunk load starts no old classroom requests, WS, microphone, or TTS.
5. Run the existing 145-case browser regression and affected Go regression unchanged; route loading must not alter API/WS frame counts.

## Rollout and stop conditions

No feature flag is required because the change preserves route behavior. Do not claim success from build-only evidence or compressed size alone. Stop if initial-page interaction regresses, direct routes fail, retry loops, a chunk error leaves a blank screen, classroom resources survive route/account replacement, or measured size thresholds are missed. This proposal is independent of the outstanding 21.678 s durable classroom commit tail; it does not claim to fix that latency.
