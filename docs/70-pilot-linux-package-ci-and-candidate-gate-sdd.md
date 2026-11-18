# Pilot Linux Package CI and Candidate Gate SDD

Date: 2026-10-23

## Problem and evidence

The resumed GitHub Actions run `39892011289` started on Linux and passed setup, `strace` installation, and candidate-runner unit checks. It stopped at manifest integrity because the remote commit's docs/55 hash was stale, so the later Linux package tests did not run. Separately, the current local `review/run_pilot_vfs_candidate.py` has no authorization-envelope, receipt, trust-root, or verifier handling, despite docs/56 requiring those checks before evidence-directory creation or child launch. The current workflow invokes this runner directly after manifest checks.

## Requirements

1. Run safe Linux package acceptance independently from manifest and runner-contract checks. A manifest failure must not suppress Go package, race, vet, or build jobs.
2. Keep candidate generation out of the enabled workflow until the docs/56 authorization verifier and one-use receipt client are implemented and independently reviewed. Do not replace cryptographic verification with an environment-variable flag, command-line digest, self-signed local file, or human approval note.
3. Make the local runner fail closed before creating the evidence directory or starting Go whenever verified authorization and a signed consume receipt are unavailable. This applies to all current modes: `candidate`, `golden`, and `propose-goldens`; promotion remains separately gated.
4. Preserve tests for candidate-runner path/provenance safety. Safe CI jobs may test the manifest's `DESIGN_CASES_NOT_EXECUTED` inventory, but must not invoke the real candidate command or report VFS behavior as accepted.
5. Keep workflow display names and summaries explicit that successful package checks do not mean candidate, private VFS, or product RunRoot acceptance; emit the package summary even when a package step fails.

## Design

The workflow uses independent Linux jobs with no `needs` dependencies: (a) job id `linux-package-regression`, display name `Linux package checks only (not VFS or RunRoot acceptance)`, for full backend tests, pilot packages, race, vet, and production builds; (b) `linux-manifest-contract` with display name `Linux manifest contract only`; and (c) `linux-runner-preflight` with display name `Linux runner preflight only (candidate remains NOT RUN)`. Manifest failure therefore leaves its job red but cannot skip the package job. No aggregation may use `continue-on-error` to make the overall workflow green. The package job has an `if: always()` summary step that writes `PACKAGE_CHECKS_ONLY; VFS_CANDIDATE=NOT_RUN; PRIVATE_VFS=NOT_ACCEPTED; PRODUCT_RUNROOT=NOT_ACCEPTED` even when a prior package step fails.

The candidate invocation is removed from all enabled jobs and is not replaced with an optional boolean bypass. A future separately reviewed workflow may restore it only after the independent authorization-service location, pinned trust root, signed envelope/receipt verifier, replay tests, and native-toolchain binding are available.

In the runner, authorization preflight precedes `prepare_artifact_directory`, temporary wrapper creation, and every subprocess. Until a real verifier and consume-receipt integration exist, invoking `candidate`, `golden`, or `propose-goldens` returns a stable `BLOCKED_PREREQUISITE` result and starts no child. With a fresh artifact path, tests assert it remains absent; with a pre-existing directory, tests assert its contents and permissions remain unchanged. A process-launcher seam verifies zero child starts, and a wrapper-creation seam verifies no temporary wrapper is made. A mode cannot self-authorize from a supplied digest or local key.

## Verification

After exact-hash review, run Linux Actions on the same committed snapshot: the package job must run even when the manifest job is deliberately made to fail; both outcomes remain independently visible. Workflow contract tests assert all three display names, the `if: always()` package summary and its exact status text, plus absence of candidate-runner invocation in enabled jobs. Runner tests exercise all three modes with both absent and pre-existing artifact paths and prove the stable blocked status, unchanged permissions/content, no wrapper, no subprocess, and no candidate/proposal files. Manifest tests assert `DESIGN_CASES_NOT_EXECUTED` and never call the candidate runner. Candidate execution remains `NOT RUN`. Test malformed, missing, stale, replayed, wrong-key, wrong-subject, wrong-purpose, source-mismatch, and service-unavailable authorization inputs once the verifier interface is supplied. Until then those cryptographic cases remain unimplemented and cannot be represented as passing.

## Explicit limits

This change does not create the independent authorization service, trust root, candidate evidence, canonical goldens, private SQLite VFS, or real-classroom approval. It enables independent Linux package checks while preserving those gates.
