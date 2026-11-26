# Runtime Artifact Directory Review — 2026-10-21

**Verdict: PASS for the no-overwrite guard.** Exact docs/56 SHA256: `AA9DF6020775E3D790D5DFE5B4135ED8D847FB4BC8E8DE414CF3E4D642141040`. Current generator source SHA256: `D5C50BD56033BE18B7FD10731C5DCAF526590094B87CB6C4C5EFDBFDFA5AD51F`.

The generator requires an absolute `PILOT_VFS_ARTIFACT_DIR`, fails before VFS registration when that setting is unset, and rejects an existing nonempty directory before writing any trace or candidate artifact. This preserves reviewed evidence against overwrite and is consistent with requiring a fresh persistent directory. The directory check occurs before tracer registration or scenario execution.

**Wording note:** the generator uses `MkdirAll`, so a path whose directory does not yet exist is created; it does not fail for a missing directory path. If “fails closed if it is missing” means the environment setting is absent, the behavior matches. To avoid ambiguity, docs may say “fails if the artifact-directory setting is absent or the selected directory is nonempty; creates the selected directory if absent.” This does not create an overwrite risk.

No other issue found in this change. This review covers the test-only evidence directory guard, not runtime candidate correctness or canonical promotion.
