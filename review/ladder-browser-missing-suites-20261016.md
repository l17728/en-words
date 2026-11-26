# Browser Acceptance: Missing Suite Batch

**Result:** PASS for the four previously missing runner suites in an isolated batch. A later isolated `repeat` run passed 3/3. These are separate runs; neither report claims a single-run 12-suite matrix or a single binary.

## Execution

The approved `review/run_browser_acceptance.py` runner ran `overdue`, `journey`, `playback`, and `recovery` sequentially with retries disabled, exact expected test counts, fail-stop behavior, isolated candidate server/API harness, and headless Playwright. Build caches, temp files, browser profiles, and outputs stayed under `review/ladder-browser-missing-suites-20261016/`. Only public synthetic audio fixtures were available; no model gateway or human audio was used.

| Suite | Native expected | Skipped | Unexpected | Flaky | Duration |
|---|---:|---:|---:|---:|---:|
| overdue | 1 | 0 | 0 | 0 | 34.4 s |
| journey | 1 | 0 | 0 | 0 | 87.9 s |
| playback | 1 | 0 | 0 | 0 | 80.7 s |
| recovery shard 1 | 21 | 0 | 0 | 0 | 301.4 s |
| recovery shard 2 | 21 | 0 | 0 | 0 | 341.7 s |

The recovery shard test-ID union exactly matches all 42 discovered IDs. All five native JSON reports have runner exit 0 and zero skipped, unexpected, or flaky tests. Earlier old-suite evidence remains separate; the older 145-test matrix is not treated as current because its source manifest differs from the current tree.

## Integrity and cleanup

**Retention note (2026-11-24):** the generated source snapshot, copied `node_modules`, and build caches were removed after the run. Runner scripts, hashes/manifests, native Playwright reports, logs, and this summary remain; reproduce the snapshot before rerunning rather than treating deleted dependencies as repository input.

- Runner-source fingerprint: `b8439f15cfd90ed435b17d7d23fd5a4ae0e6b78790a07f0923f09e444dac93d1`.
- Candidate server SHA-256: `5164ad7d612d10afbf0e267058080050b53df30d4c84bd5e28fddd430497814a`.
- API harness SHA-256: `9b1ba542b2a6b53a005e97e507bfccd504e98ef76bd534bd808063e1365be5af`.
- Source before/after manifests are equal; runner reports `unchanged=true`.
- Formal and snapshot `package.json` and lockfile hashes match. The copied `node_modules` trees have 19,986 files / 184,027,331 bytes and zero per-file differences; `.vite` and `.tmp` caches were excluded.
- Owned runner, browser, candidate server processes have exited. Ports 18080, 18081, 15173, and 15174 have no listeners.
- Vite logs contain transient proxy `ECONNREFUSED` messages during candidate-server lifecycle transitions; these did not correspond to Playwright failures. All five native reports in this batch are green.

## Raw evidence

See `review/ladder-browser-missing-suites-20261016/evidence/missing-suites-r1/` for native Playwright JSON, stdout logs, exact IDs, and runner manifest. Build logs and binary inventory are under the same report directory. `verified-inputs.json` and `evidence/missing-suites-r1/manifest.json` record source/dependency hashes and unchanged-source verification.

The follow-on `repeat` run is recorded separately in `review/ladder-browser-missing-suites-20261016/evidence/repeat-only-r1/`; its native JSON reports 3 expected, 0 skipped, 0 unexpected, and 0 flaky cases. The cross-batch coverage index is [ladder-browser-cross-batch-index-20261016.md](ladder-browser-cross-batch-index-20261016.md) with machine-readable companion JSON. It records suite batches, source fingerprint, per-batch binary hashes, and per-suite case IDs while explicitly distinguishing old stdout-only evidence from native Playwright JSON.

The six earlier direct-config suites have since been rerun through the formal runner in the separate `evidence/direct-six-r2/` batch. Its six native reports total 53/53, each runner command exited 0 with no skipped, unexpected, or flaky tests. The original `direct-config-r1` output remains untouched and is excluded because its wrapper recorded null exit codes and failed status despite passing stdout lines.
