# Pilot Intent Quota Codec Regression Evidence

## Current reviewed slice

Go 1.25.0, Windows amd64; commands run from `D:\en-words\server` using `D:\en-words\.test-tools\go1.25.0\bin\go.exe`:

```text
go test -count=1 ./internal/pilotquota                 PASS
go test -count=1 -cover ./internal/pilotquota          PASS, 98.3% statements
go vet ./internal/pilotquota                           PASS
go test -race -count=1 ./internal/pilotquota           NOT RUN: CGO_ENABLED=0; Go reports that -race requires cgo
```

The tests load all four literal frame vectors from JSON, verify vector metadata, create a distinct legal two-handle chain with exact offsets and lengths, cover late BOUND event/append timing and EXPIRED boundaries, exercise seconds/nanoseconds maximums, and reject re-signed settlement identity/hash/time mismatches. Replay metadata retains no ciphertext suffix. `gofmt -d` is clean.

## Frozen source hashes

| File | SHA-256 |
|---|---|
| `server/internal/pilotquota/frame.go` | `e1dd4a132bc944f28b6494ec2d768fd1c16de7ed6cf64a10693c07a374b9044a` |
| `server/internal/pilotquota/replay.go` | `9c7056c8a45cda97cea6ca18e68b2dfbd933f5f0da4cfebbff4b6b9743d7c7` |
| `server/internal/pilotquota/frame_test.go` | `a79a8065a6e85fb9eae1f1de6f7a91aa2ef67dd8dbfe148922b5d5c73c57da89` |
| `server/internal/pilotquota/vectors_test.go` | `60633f7fd96e50b82bcee8d875491ac1a84277d55828459cfc63124752f20d5c` |
| `server/internal/pilotquota/chain_test.go` | `3e579e87bfea485fe292f0377fd6aff97e8061ab42809d8f3687dca25144e5a2` |
| `server/internal/pilotquota/testdata/mixed-chain-v1.json` | `58bf30840a14fe19035bf721f4440d75dfe51fec14117f7f82aba78d56b28fa5` |

The reviewed design hashes are docs/63 `366e726cce3d7715864e48013fc9145047a87338a8f69459915e0a3e909ab6b6`, manifest `8c433cb8e5d44d2b67bf87ac4b0c31f479b49ad917acb3f627b24b6be0599fd1`, and vectors `cc8956d6735dc38728cc5b0cd5a14c034d681c3c4fd6a12b005d6248f46c7d49`. All manifest source hashes were independently checked. Design reviews passed across architecture, backend, security, and test design. Implementation reviews also passed architecture, backend, security, and test design. Security review is scoped to codec/replay only.

## Scope limit

This evidence applies only to bounded in-memory framing and read-only replay. It does not validate durable writes, external anchoring, file identity/permissions, trusted time, IPC, Linux recovery, API integration, or browser behavior. Production minting remains disabled; every QJ manifest case remains `BLOCKED_NOT_IMPLEMENTED` until its complete acceptance oracle passes.

## Earlier baseline

An earlier implementation snapshot reported 99.4% coverage against the pre-event-time SDD. That evidence was superseded by the current design and source hashes above and is not relied on for this acceptance.
