# Pilot Control Trigger Source Authorizer Review

Date: 2026-10-21  
Decision: **PASS for this authorizer boundary and exact source snapshot**

## Reviewed hashes

| File | SHA-256 |
|---|---|
| `server/internal/pilotcontrol/sqlite.go` | `44E3DC85AF4F27611F855042A0C2382611BB0881F0DDB21B75921E7044955737` |
| `server/internal/pilotcontrol/sqlite_test.go` | `45429272C079190DF74AE9402B8D600B8251991E522122364D46E8B3261BBB0E` |
| `server/third_party/go-sqlite3/callback.go` | `3C45B7A2C48F011908511EE07531D4876236A2EB813C9B4B387EC9F5D968DA98` |
| `server/third_party/go-sqlite3/sqlite3.go` | `3804415577231DF5836E1C13E7B8D800C196FC0E58885EBBFC36DEB4BD995C24` |

The local v1.14.22 driver fork adds a backward-compatible `RegisterAuthorizerWithTriggerName` callback so the authorizer can see SQLite's actual fifth argument. The legacy callback remains available to other callers.

## Decision and boundary

Architecture review initially questioned INSERT permission on the capacity-count table. The approved allocation trigger in `review/pilot-control-ledger-v3-literal-ddl-draft-20261021.sql` conditionally inserts tenant and principal count rows before incrementing them. That INSERT permission is therefore required. It is limited to a validated schema, active `capPrepareRunAllocation`, the exact `pilot_allocation_capacity_after_insert` trigger name, and fixed table/action pairs. The companion regression denies direct count/alert INSERT, UPDATE, and DELETE under the capability and denies a different trigger name while allowing the reviewed allocation trigger's effects.

Architecture re-review passed these exact hashes after the DDL clarification and negative-test additions. This approves only the authorizer boundary; it does not approve the path-based VFS, production service integration, full coverage gate, or classroom pilot.
