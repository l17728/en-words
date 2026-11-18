# Pilot Intent Quota Journal: Implementation Delta 1

## Status

This is the first, deliberately limited implementation slice against the reviewed design in `docs/63-pilot-intent-quota-journal-sdd.md`. It does not authorize production minting. The feature must remain unavailable until all SDD gates pass and a separately reviewed, installer-pinned monotonic anchor is deployed.

## Implemented slice

`server/internal/pilotquota` provides a bounded frame codec and read-only replay validator. It checks the 16-byte header, 160-byte authenticated prefix, version/type/flags/length, HMAC, ciphertext hash and size, checked seconds-to-nanoseconds conversion, signed-range nanosecond values, sequential records, previous-frame digests, unique operation/request identifiers, and PERMIT → ISSUED → one BOUND/EXPIRED settlement. BOUND and EXPIRED persist signed `event_time_ns` separately from nondecreasing journal `record_time_ns`, so valid pre-expiry receipts can be appended late. Settlement lookup uses issued sequence indexing. Replay returns exact relative offsets/frame lengths, counts PERMIT-only requests as burned, discards ciphertext suffixes from retained metadata, and never repairs or writes the journal.

Four complete codec vectors are loaded directly from `review/pilot-intent-quota-frame-v1-vectors.json` and checked byte-for-byte, including JSON metadata. A separate two-handle legal mixed-chain fixture in `server/internal/pilotquota/testdata/mixed-chain-v1.json` verifies all four record types and exact offsets/lengths. Timestamp conversion boundaries and re-signed settlement tuple mismatch cases are covered.

## Validation and review

Go 1.25.0, Windows amd64: `go test -count=1 ./internal/pilotquota` PASS; `go test -count=1 -cover ./internal/pilotquota` PASS at 98.3%; `go vet ./internal/pilotquota` PASS. `go test -race` remains unavailable because this environment has `CGO_ENABLED=0`. The design (docs/63, case manifest, and frame vectors) passed architecture, backend, security, and test-design review at the exact hashes recorded in the review evidence. Implementation reviews passed architecture, backend, test design, and security for the exact code hashes in that evidence. Security review is scoped to codec/replay and does not cover deployment. Hashes and command output are in [the regression evidence](../review/pilot-intent-quota-codec-regression-20261022.md).

## Not implemented

There is no production journal opener, inode/permission pinning, append/fdatasync path, external anchor adapter, trusted clock, cross-process singleton, authenticated IPC, reservation/ticket lifecycle, settlement worker, capacity escrow, service telemetry, HTTP integration, or browser/E2E flow. These are separately specified in the QJ case manifest; all QJ cases remain `BLOCKED_NOT_IMPLEMENTED` until their full oracles pass. No E2E is applicable to this unintegrated codec slice; default-build minting and every production route remain disabled.

## Next gates

The codec/replay security review is complete for the frozen hashes above; every future production slice needs its own design review, code review, and exact-hash regression. The injected append coordinator is specified and reviewed in [docs/65](65-pilot-quota-durable-append-coordinator-sdd.md), with its isolated acceptance recorded in [docs/66](66-pilot-quota-durable-append-coordinator-review.md). Neither slice authorizes production minting.

1. Implement and review a pinned filesystem journal, external monotonic anchor, and trusted-time adapter; test crash/restart, rollback, and filesystem failures on native Linux.
2. Add authenticated IPC, reservation/ticket lifecycle, and settlement orchestration with bounded resources and forbidden-field log scans.
3. Integrate behind an explicit default-off gate; prove unavailable dependencies fail before nonce reservation or encryption.
4. Add multi-user classroom E2E only after an authenticated end-to-end route exists; keep real audio and model trials isolated from production data.
