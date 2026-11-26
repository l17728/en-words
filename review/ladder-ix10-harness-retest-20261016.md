# Ladder IX10 Harness Retest

Date: 2026-10-16. This was a single headless Chromium interaction-suite acceptance run against the fixed ladder source snapshot, using an isolated server and API test harness. No source or tests were changed. No other suite was started.

## Result

- Playwright interaction suite: **30 passed, 0 skipped, 0 failed, 0 flaky**; native duration 338.49 s; runner exit 0. The suite contained 30 distinct test IDs and all 30 appeared once in the native report.
- IX10 `finished timeout sweep performs zero writes and completed report remains readable`: **passed once, retry 0**, 14.5 s (test 6 of 30). This resolves the prior harness setup failure: both `E2E_SERVER_BINARY` and `E2E_HARNESS_BINARY` were supplied by the approved runner.
- Runner prechecks: Node unit tests exit 0; Oxlint exit 0.
- Runner/source fingerprint: `b8439f15cfd90ed435b17d7d23fd5a4ae0e6b78790a07f0923f09e444dac93d1` before and after; unchanged. Runner SHA-256: `720fca497351381be5e8c3c25ef4cfe874f371e0bc4bd9122f0ddc573353051d`.

## Inputs and isolation

**Retention note (2026-11-24):** the reproducible `source/` and `source-verified/` repository copies and build caches were removed to avoid checking in duplicate source trees and dependencies. Their verified-input manifests, fingerprints, runner scripts, reports, and raw test evidence remain. Recreate a source snapshot from this repository with the adjacent `make_snapshot.py` before rerunning; the historical run result itself is unchanged.

- Historical source: the 2026-10-16 run copied `review/ladder-integrated-browser-acceptance-20261016/source` to `source-verified`; both physical copies have since been pruned as noted above.
- Dependencies: formal `web/node_modules` were copied as regular files, excluding `.vite` and `.tmp`; 19,986 files / 184,027,331 bytes. Formal-to-clone and clone-to-frozen-manifest SHA comparisons had zero differences. `package.json` and lockfile hashes matched the prior snapshot record.
- Server binary SHA-256: `e75d2ad4100fd79704d6160e8a93a0672434e366dfa1454b80c69ba716076d6a`. API harness SHA-256: `ba469b056d3c5912f909c394eb7c7edf2ba55fa1102c79f2673664112e6eab5a`; compiled from snapshot `server` with isolated Go caches/TEMP. Compile exit 0 (238.53 s).
- Ports 18081 and 15174 were free before start and released afterward. All runner-owned processes exited. Tests used mock providers; no 8090 platform or real/human audio was used. The runner generated TypeScript `.tmp` incremental cache only inside the isolated clone.

## Evidence

- Native Playwright report: `review/ladder-ix10-harness-retest-20261016/evidence/playwright-interaction-r1/interaction-results.json`.
- Runner manifest and stdout logs: same directory (`manifest.json`, `interaction.log`, `node-unit.log`, `lint.log`).
- Build output and dependency verification: `review/ladder-ix10-harness-retest-20261016/logs/` and `evidence/verified-dependencies-manifest.json`.
- Machine-readable summary: `review/ladder-ix10-harness-retest-20261016/evidence/acceptance-summary.json`.
