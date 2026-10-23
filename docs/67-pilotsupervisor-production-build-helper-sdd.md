# Pilot Supervisor Production Build Helper SDD

Date: 2026-10-22

## Problem and evidence

The `pilot_e2e` production build of `internal/pilotsupervisor` fails for Linux amd64 and arm64: `runroot_evidence.go` calls `runRootBasenameDigest`, but the only definition is in the `_test.go` file `runroot_evidence_test.go`. `go test` and `go test -c` mask the defect because test sources are compiled alongside the package. The failed build and source hashes are recorded under `server/.test-artifacts/pilot-crosscompile-linux-20261022T190541Z`.

## Intended change

Move the pure `runRootBasenameDigest` helper into production source in `runroot_evidence.go` and remove its duplicate test-only definition and now-unused `fmt` import. Preserve its exact contract: return lowercase hexadecimal SHA-256 from `hashRunRootDomain("en-words-basename-v1\x00", []byte(name))`. Do not change evidence fields, CBOR encoding, domain separation, or golden bytes.

## Verification

Keep the existing `pilot_e2e` evidence-vector regression tests as the behavior lock, including `TestRunRootEvidenceVectors`, `TestRunRootEvidenceDigestDomainAndSchemaAreBound`, and `TestRunRootEvidenceRejectsSchemaDrift`. Add an explicit production package build to the Linux Actions workflow; test binary compilation is not a substitute. From `server`, verify `gofmt`, targeted `go test -tags=pilot_e2e ./internal/pilotsupervisor -run '^(TestRunRootEvidenceVectors|TestRunRootEvidenceDigestDomainAndSchemaAreBound|TestRunRootEvidenceRejectsSchemaDrift)$' -count=1`, and `go vet -tags=pilot_e2e ./internal/pilotsupervisor`. Separately run both `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags=pilot_e2e ./internal/pilotsupervisor` and `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags=pilot_e2e ./internal/pilotsupervisor`; failures block acceptance. Native Linux CGO, SQLite VFS, `strace`, and service acceptance remain separate gates.

## Scope and observability

This is a compile-time visibility defect in a deterministic helper. It adds no runtime branch, I/O, state transition, or new log event; existing evidence vectors and build output provide the relevant verification. No browser behavior or product API changes are implied.

## Acceptance

Architecture, backend, and test reviewers approve this SDD before implementation. The fix is accepted only when production package builds pass for both Linux architectures and the unchanged evidence vectors pass.
