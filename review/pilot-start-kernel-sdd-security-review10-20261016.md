# Security Review 10: `lesson.start` Persistence Kernel SDD

Read-only review of the latest `docs/41-试点课堂Start持久迁移内核SDD.md`, covering the new ACL/key negative-case matrix, same-account threat boundary, degraded-sidecar restart recovery, and protocol/log privacy. No code or design was changed and no tests or runtime checks were run.

## Decision

**BLOCK pending two security contract clarifications.** The requested ACL/key controls and recovery lifecycle are now detailed and coherent within the explicit trusted-local-account boundary. However, the loopback harness has a mutating HTTP surface and a “random fixture token,” but the SDD does not say how the token is authenticated or specify CSRF/Origin/Host/CORS protections. It also lists `trace_id` in ordinary logs while prohibiting raw decision IDs, without defining that the log value is distinct from `decision_id`. Both ambiguities can be resolved in the design without changing the test-only scope.

## Reviewed controls

- **ACL/key negative cases — specified.** §3 covers owner/DACL and POSIX mode/umask checks, replacement and link attacks, fresh per-case 256-bit OS-CSPRNG keys, fixed test-vector exclusion, atomic exclusive creation, protected-handle transfer, and secret-leak scans. §6 includes Windows/POSIX permission failures, key replacement/repeated-entropy cases, pre-Prepare rejection, and case non-reuse.
- **Threat boundary — explicit.** The design excludes compromise by the trusted supervisor account, administrator, kernel, or host. It states receipt-first provides structural consistency only inside that boundary, not protection from same-account/admin DB tampering.
- **Degraded receipt recovery — well specified.** Tests require a committed receipt to be verified before tail-marker damage; after restart the same authorized owner receives the identical receipt and the case remains `sidecar_degraded` across repeated restarts. New commands are rejected with zero Prepare/ExecuteTool/Registry calls. If receipt lookup is unavailable with a corrupt sidecar, the harness returns `recovery_integrity_error` without writes.
- **Lifecycle and retention — specified.** Successful cases are removed after evidence persistence; terminal failures expire within 24 hours; pending/degraded artifacts are supervisor-only and capped at 7 days. DB/WAL/SHM/journal/sidecar/key are removed together and failed cleanup isolates the case.
- **Tool capability — improved.** A one-use opaque permit is created only after durable `tool_started`, binds command/plan/attempt/effect/tool, and is consumed atomically before one Registry execution. Invalid/reused permits are rejected before handler entry.

## Findings

### PSK-SEC10-01 — Loopback harness CSRF/authentication contract is missing (P1)

§5/§6 expose a browser-driven loopback harness with state-changing Start and receipt routes and mention a random fixture token, but do not define whether it is a bearer header, cookie, or another credential; whether the page embeds it; or how the handler rejects cross-origin requests. Binding only to `127.0.0.1` does not by itself prevent a hostile website from attempting requests to a local service. The design needs an explicit browser threat boundary, particularly because this harness persists a classroom transition and invokes Registry tools.

**Required revision:** specify token transport as a non-cookie custom authorization header unavailable to ambient credential submission; validate exact Host and permitted Origin (or an equally strong per-case CSRF nonce/custom-header scheme), reject cross-origin/opaque origins and unsupported content types, disable CORS sharing, and ensure mutating actions are never reachable via GET or simple form POST. Keep the random token out of logs and prevent arbitrary caller-selected tokens. Add negative E2E cases for a hostile origin, missing/invalid token, altered Host, form content type, and credential leakage.

### PSK-SEC10-02 — Ordinary `trace_id` logging is ambiguous against decision-ID redaction (P2)

§3/§4 identify `decision_id` as the Registry audit trace ID and say ordinary logs must not contain raw decision IDs; §7's ordinary log schema nevertheless includes `trace_id`. If this is the same identifier, the log allowlist conflicts with its redaction rule. If it is a distinct request correlation ID, that distinction and generation rule are unstated.

**Required revision:** define log `trace_id` as a fresh opaque request-level identifier that is not the decision/task/lesson/user ID and is not derived from them, or log a keyed digest instead. Explicitly forbid mapping it back to `decision_id` outside the authorized DB receipt/audit path and include it in sentinel/redaction assertions.

## Limits

No tests, browser-origin checks, ACL/mode checks, key-generation checks, build-artifact inspection, database access, or recovery execution were performed. This is a design review only and does not verify runtime behavior or classroom readiness.
