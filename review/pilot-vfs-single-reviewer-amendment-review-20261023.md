# Single-reviewer Golden Promotion Amendment Review

Date: 2026-10-23  
Reviewed design: `docs/56-pinned-sqlite-golden-harness-delta.md`  
Reviewed design SHA-256: `2C6314265E93D8A494FFC7E80310686F1A64107C3CC0E9E1551C63513F550092`

## Decision

Architecture, backend, security, and test-design reviewers returned PASS on the exact hash above with no remaining P1/P2/P3 findings. The project owner’s decision is explicit: `l17728` is the sole human signer; one valid signature is sufficient, and no second reviewer or signature is required.

The amendment defines a closed v2 approval payload and a service-signed promotion record. The record binds the approval envelope, all reviewed artifacts, reviewer identity, sequence, trusted promotion time, and outcome. The promotion service performs one atomic anti-rollback commit; mTLS-authenticated `POST` performs promotion and read-only `GET` verifies an existing record without consuming sequence state. The test matrix covers legacy and multi-signature rejection, HTTP identity failures, idempotency, record tampering/rollback, time-window endpoints, and clock-offset boundaries.

Earlier review findings were closed in the reviewed hash: exact payload fields and constants; refusal of v1 and multi-signature encodings; durable promotion-record format and authoritative lookup; POST/GET status semantics; and explicit positive/negative time boundaries. Review conclusions apply only to the design snapshot and do not claim that the service or ledger is implemented.

## Acceptance boundary

The VFS candidate runner remains fail-closed until an independently operated signed run-authorization verifier, one-use receipt service, pinned Linux toolchain, and valid authorization are available. No candidate was generated or promoted by this amendment. The promotion service’s reviewer and service keys are not present in the repository; secrets must remain outside source control and logs.

The design change was committed as `ea0cab00d2060f69afcc14574f25948937844e7d` on `pilot/linux-final-acceptance-20261022` and pushed to `origin`.
