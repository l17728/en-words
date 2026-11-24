# Pilot v3 fixed initialization seeds — design note

## Current decision

The reviewed v3 ledger has no immutable reference or content rows that are invariant across deployments. Its persisted rows are runtime state, append-only audit/receipt data, or deployment-specific bootstrap values. In particular, capacity policy values and the launcher journal ID/restore generation are supplied by trusted deployment configuration and are deliberately not part of the fixed-seed digest.

Therefore the candidate `initialization_seeds_v1.json` is the canonical empty ordered set: `{"format_version":1,"seeds":[]}` followed by one LF. Its SHA-256 is stored in the immutable initialization marker alongside the exact schema-inventory digest. It is embedded in the production binary only after the DDL/schema review; the current file under `review/` is a draft artifact, not production configuration.

## Change rule

If a later schema adds immutable reference/content rows, add their exact table/key/value tuples in canonical order, regenerate the seed digest and initialization marker vectors, and obtain schema/security/test review before implementation. Mutable lease, capacity counters, journal anchor, control generation, policy settings, and initialization marker itself must never enter this list.
