# Pilot Supervisor SDD — R6 Cross-Document Architecture Review

- Review date: 2026-10-19
- `docs/41-试点课堂Start持久迁移内核SDD.md` SHA-256: `7890FB542818C77F131CD06D09845120406EB7C1567AF1BDFA83C92FA51D3867`
- `docs/42-Pilot隔离运行监督器SDD.md` SHA-256: `AEF772E4D71633690E4E3F4C7498837552088D5BD385AF1875DE820E1B9AA447`
- Scope: read-only architecture review of §112 corrections, §42 lifecycle/close contracts and FI-CLOSE rows, and filesystem allowlists/state mapping.

## Conclusion: PASS

No blocking, P1, or P2 architecture issue was found in the requested scope.

## Findings

| ID | Severity | Result | Evidence |
|---|---|---|---|
| R6-01 | — | PASS | `docs/41` §112 explicitly supersedes the old SQLite OS-file-ID and child-process HMAC-key requirements. `docs/42` §§2, 6, 9 now consistently limit SQLite verification to canonical path and keep the case key in the trusted Supervisor; the E2E/fault sections do not transfer the key. The acknowledged trusted-directory/same-account threat boundary is stated in both documents. |
| R6-02 | — | PASS | `docs/42` §§7–9 enumerate every root object, stable case-lock object, case directory/member, and log directory/member. The exact allowlist covers `catalog.bootstrap.lock`, `catalog.log`, `case-locks/<32hex>.lock`, `cases/<32hex>/{db.sqlite, db.sqlite-wal, db.sqlite-shm, db.sqlite-journal, attempts.jsonl, case.key}`, and `logs/{events.jsonl, log.pseudonym.key}`. No named object used elsewhere in the SDD is outside this inventory. |
| R6-03 | — | PASS | `docs/42` §8 state/capability matrix agrees with `docs/41` §109.2–§109.3: only ready permits first Start; recovery/quarantine/cleanup/deleted states have restricted capabilities; cleanup uses the permanent external case lock and tolerates the specified absent-directory crash point. The state-dependent directory presence rules in §7 match those recovery cases. |
| R6-04 | — | PASS | `docs/42` §§3, 9 and FI-CLOSE-01a–g agree on `{closed,nil}`, `{still_open,error}`, and `{unknown,error}`; invalid combinations become unknown. `still_open` is retried only by the original owner on the same handle and maps to `close_pending`; unknown/invalid maps to `restart_required`, forbids another OS close/unlock, poisons the supervisor, and requires bounded child termination/reap before restart. The FI rows preserve the case/global lock until safe release. |

## Non-blocking clarification

For implementation clarity, the platform ACL text in `docs/42` §2 item 3 could state explicitly that restricted child SIDs receive no read access to `case.key` or `logs/log.pseudonym.key`; §2 item 7 and the FI key rows already require the keys to remain Supervisor-owned, so this is a defense-in-depth wording suggestion rather than a finding.
