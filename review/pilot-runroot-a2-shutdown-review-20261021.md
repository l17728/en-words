# Pilot RunRoot A2 Shutdown/Restart Design Review

Date: 2026-10-21

## Reviewed snapshot

- `docs/50-pilot-runroot-product-orchestration-sdd.md`: `855c717c3b1a2bc7db64ba2b19acb2518a281cd2ec165e6e6a4be2f54d24b61a`
- `docs/54-pilot-runroot-stage-a-implementation-delta.md`: `c4c0dd80d56df92f265da2a0b9b18bb754267595faf6510c1899398aa2841070`
- `review/pilot-runroot-product-orchestration-manifest-20261021.json`: `c9bf3dee04b8d80c8a708316125d19332ac05dbc4a611edb18fe560d499cffdb`

## Findings and resolution

The review caught an ownership conflict: the resident A0 process cannot directly persist a launcher-owned journal quarantine frame. The revised contract assigns journal append plus `fdatasync` to the launcher and the sequence/hash anchor commit to A0 through `AdvanceLauncherJournalAnchor`.

Graceful stop and hard launcher crash are separate acceptance cases. The original live launcher reaps its exact children with retained pidfds on graceful stop. On hard crash, systemd `KillMode=control-group` and PID 1 terminate/reap descendants; a replacement must prove the old cgroup empty before journal access and must not `waitid` old nonchildren. Both cases remain startup-only, create no lifecycle/terminal evidence, admit no replacement generation, and remain blocked on uncertain child/cgroup/journal/anchor state.

## Review result

- Architecture review: PASS on the exact hashes above; no remaining finding in the revised A2 shutdown/restart contract or updated case range.
- Independent acceptance-design review: PASS on the A2 clauses; the reviewer confirmed the 105-case manifest and source bindings before the final docs/50 case-count wording correction. The separate architecture review checked the final exact hashes above. Both A2 cases have distinct actors, preconditions, barriers, actions, and oracles.
- Product manifest: 105 cases; unique case IDs and named barriers; all cases remain `BLOCKED_NOT_IMPLEMENTED`; all source hashes match.
- `python review/generate_pilot_v3_ddl_manifest.py`: PASS.
- `python review/test_pilot_v3_ddl_draft.py`: PASS (DDL draft invariants/fault harness only; this is not evidence that A2 is implemented).

This is a design review only. A2 shutdown/restart implementation and native Linux acceptance remain outstanding; no production readiness or classroom pilot is approved by this record.
