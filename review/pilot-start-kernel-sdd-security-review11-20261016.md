# Security Review 11: `lesson.start` Persistence Kernel SDD

Read-only review of the latest `docs/41-试点课堂Start持久迁移内核SDD.md`, checking closure of PSK-SEC10-01/02 and the added ACL-before-open requirements. No code or design was changed and no tests or runtime checks were run.

## Decision

**PASS for the narrow test-only security design.** The browser-facing loopback contract now uses same-origin-only POST JSON requests with exact Host/Origin checks, Fetch Metadata, an in-memory per-case CSRF token and Bearer authentication, no cookies, and no CORS. Negative E2E cases must reject cross-origin, forged Host, absent Origin/token, wrong content type, and GET attempts before Coordinator/Planner/Registry. Logs now use a request-specific random `request_trace_id`, explicitly independent from decision ID and Registry trace ID. ACL/path verification is required before receipt handling or database access, including replay paths.

## Round-10 finding dispositions

- **PSK-SEC10-01 — closed in design.** §6 binds only to `127.0.0.1` on a random port and requires exact Host/Origin, `Sec-Fetch-Site: same-origin`, POST, JSON, in-memory per-case `X-Pilot-CSRF`, and Bearer authorization. Cookie authentication, GET/form requests, absent Origin, and cross-origin CORS/OPTIONS are rejected. The token is excluded from URL, storage, and logs. The new browser negative cases assert rejection before Coordinator with zero Planner/Registry calls.
- **PSK-SEC10-02 — closed.** §7 renames the field `request_trace_id`, defines it as a fresh per-request random diagnostic ID, and states it is independent, non-derivable, and non-reusable relative to `decision_id` and Registry `TraceID`. Persistent correlation remains through authorized DB receipt/audit only.

## Additional controls checked

- **ACL/key handling — consistent.** `OpenVerified` is required at Coordinator startup, every receipt path (including hits), and before DB/sidecar/key opens. Rejected owner/ACL/mode/path/file identity returns a fixed integrity error before receipt query or Planner. The SDD retains owner-only Windows DACL and POSIX `0700`/`0600`/`umask 077`, atomic key creation, fresh independent 256-bit OS-CSPRNG key per case, same-case key recovery only via the supervisor, protected-handle delivery, and negative tests for link/replacement/race, permissive permissions, and duplicate entropy.
- **Same-account boundary and receipt-first recovery — explicit.** Admin/same-account compromise is excluded; DB receipt-first is limited to structural consistency within that boundary. A damaged sidecar with a valid receipt replays the same receipt to the authorized owner and quarantines the case across restart; absent/unavailable receipt plus corruption fails closed.
- **Data lifecycle and privacy — consistent.** Synthetic-only data, 24-hour terminal-failure and 7-day pending/degraded retention, supervisor-only access, grouped artifact cleanup and persistent quarantine after cleanup failure remain specified. Raw IDs, fingerprints, tokens, credentials, prompts, and student inputs remain excluded from ordinary logs; HMACs in sidecar are case-scoped.

## Limits

No tests, browser request checks, ACL/key checks, build-artifact inspection, database access, or recovery execution were performed. This is approval of the written isolated test-only security design; it does not verify implementation, production deployment, real student data, or classroom readiness.
