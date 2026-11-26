# Linux Launcher Identity Handoff SDD Review

Date: 2026-10-20

Reviewed specifications:

- `docs/43` (Pilot authorization/idempotency SDD) ? SHA-256 `5F112C797919BE4FEBCE2DECD2C890DF00AD46F5782DB3FE14E07FBB60068A1E`
- `docs/47-pilot-linux-launcher-identity-handoff-sdd.md` ? SHA-256 `277DFF65EFC85F0D83648B6F6480F669C53CC761B9673FAFB7D0C62E984132C3`

## Domain decisions

| Domain | Result | Review focus |
|---|---|---|
| Architecture | PASS | Parent/reaper topology, recovery-only capability boundary, generation ordering, durable evidence transition |
| Backend | PASS | Atomic evidence/state commit, terminal transaction, lease invariant, ACK and replay outcomes |
| Security | PASS | Peer identity, least privilege, control-generation replay protection, no business capability in recovery mode |
| Testing | PASS | Deterministic crash cuts, recovery result fixtures, identity negatives, Linux host preflight, log privacy |

## Closed review findings

The review cycle resolved ambiguities in `recovery_not_ready` predicates and retry policy, control-generation consumption on error ACKs, the evidence-only crash boundary, `recovery_proof_missing` target-state preservation, Linux privileged-runner preflight, and `restart_required` fault injection. The immutable evidence row and `run_state=containment_empty` transition commit atomically while the lease remains held; a separate final transaction verifies receipts and releases it.

## Gate

The design is approved for the first isolated Linux implementation slice behind the existing pilot build tags. This review does not claim Linux runtime acceptance: amd64/arm64 normal, race, vet, privileged cgroup/systemd fixtures, and the specified artifacts remain implementation gates. No production API or real-user audio is authorized by this SDD.
