#!/usr/bin/env python3
"""Executable acceptance probes for the isolated Pilot v3 DDL draft only."""
from __future__ import annotations

import json
import hashlib
import re
import sqlite3
from contextlib import contextmanager
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DDL = ROOT / "review/pilot-control-ledger-v3-literal-ddl-draft-20261021.sql"
SEED = ROOT / "review/pilot-control-ledger-v3-bootstrap-seed-draft-20261021.sql"
INIT_SEEDS = ROOT / "review/pilot-control-ledger-v3-initialization-seeds-draft-20261022.json"
SCHEMA_INVENTORY = ROOT / "review/pilot-control-ledger-v3-schema-inventory-draft-20261022.json"
INV = ROOT / "review/pilot-control-ledger-v3-invariant-queries-draft-20261021.sql"
CAP_MANIFEST = ROOT / "review/pilot-control-ledger-v3-write-capabilities-20261021.json"
DDL_MANIFEST = ROOT / "review/pilot-control-ledger-v3-ddl-manifest-draft-20261021.json"

CAPABILITIES: dict[str, dict[str, set[str]]] = {}
PROTECTED_TABLES = {
    item["name"] for item in json.loads(SCHEMA_INVENTORY.read_text(encoding="utf-8"))["objects"]
    if item["type"] == "table"
}
_CAPABILITY_SCOPE_TOKEN = object()
_ACTIVE_CAPABILITIES: dict[int, str] = {}
for method, resources in json.loads(CAP_MANIFEST.read_text(encoding="utf-8"))["capabilities"].items():
    for resource in resources:
        table, actions = resource.split(":", 1)
        CAPABILITIES.setdefault(table, {})[method] = set(actions.split(","))


class AuthorizedConnection(sqlite3.Connection):
    @property
    def current_capability(self) -> str | None:
        return _ACTIVE_CAPABILITIES.get(id(self))


def authorize_protected_dml(table: str, action_name: str, source: str | None,
                            capability: str | None) -> int:
    if source is not None:
        return sqlite3.SQLITE_DENY
    actions = CAPABILITIES.get(table, {}).get(capability or "", set())
    return sqlite3.SQLITE_OK if action_name in actions else sqlite3.SQLITE_DENY


def _set_capability(db: AuthorizedConnection, name: str | None, token: object) -> None:
    if token is not _CAPABILITY_SCOPE_TOKEN:
        raise PermissionError("capability scope is internal to the typed ledger runner")
    if name is None:
        _ACTIVE_CAPABILITIES.pop(id(db), None)
    else:
        _ACTIVE_CAPABILITIES[id(db)] = name


def checked_next_control_generation(raw: bytes) -> bytes:
    if len(raw) != 8 or raw == bytes(8):
        raise ValueError("control_generation must be a positive 8-byte big-endian value")
    value = int.from_bytes(raw, "big")
    if value == (1 << 64) - 1:
        raise OverflowError("control_generation exhausted")
    return (value + 1).to_bytes(8, "big")


def rendered_seed() -> str:
    seed = SEED.read_text(encoding="utf-8")
    schema_digest = hashlib.sha256(SCHEMA_INVENTORY.read_bytes()).hexdigest()
    seed_digest = hashlib.sha256(INIT_SEEDS.read_bytes()).hexdigest()
    return (seed.replace(":global_limit", "7").replace(":tenant_limit", "5")
            .replace(":principal_limit", "3").replace(":journal_id", "X'01010101010101010101010101010101'")
            .replace(":restore_generation", "X'0000000000000001'")
            .replace(":schema_manifest_sha256", f"X'{schema_digest}'")
            .replace(":seed_manifest_sha256", f"X'{seed_digest}'"))


def db_new(trigger_source_probe: bool = False) -> sqlite3.Connection:
    db = sqlite3.connect(":memory:", factory=AuthorizedConnection, cached_statements=0)
    db.execute("PRAGMA foreign_keys=ON")
    db.execute("PRAGMA recursive_triggers=ON")
    db.execute("PRAGMA trusted_schema=OFF")
    seed = rendered_seed()
    db.execute("BEGIN IMMEDIATE")
    execute_script(db, DDL.read_text(encoding="utf-8"))
    execute_script(db, seed)
    db.commit()
    if trigger_source_probe:
        db.execute("CREATE TABLE user_trigger_source(value INTEGER)")
        db.execute("""CREATE TRIGGER user_trigger_probe AFTER INSERT ON user_trigger_source
                     BEGIN INSERT INTO pilot_session_fences
                     (tenant_id,principal_ref,session_key_hash,session_revision,session_state,expires_at,updated_at)
                     VALUES(X'01',zeroblob(32),zeroblob(32),X'0000000000000001','active',1,1); END""")
    def authorizer(action: int, arg1: str | None, arg2: str | None,
                   database: str | None, source: str | None) -> int:
        # The host exposes this same immutable policy on every pooled connection.
        if action == sqlite3.SQLITE_PRAGMA:
            return sqlite3.SQLITE_OK if arg2 is None and arg1 in {"user_version", "foreign_keys", "recursive_triggers", "trusted_schema", "journal_mode", "synchronous", "foreign_key_check", "integrity_check", "table_list"} else sqlite3.SQLITE_DENY
        if database == "temp" or action in (sqlite3.SQLITE_ATTACH, sqlite3.SQLITE_DETACH,
                sqlite3.SQLITE_ALTER_TABLE, sqlite3.SQLITE_CREATE_INDEX, sqlite3.SQLITE_CREATE_TABLE,
                sqlite3.SQLITE_CREATE_TEMP_INDEX, sqlite3.SQLITE_CREATE_TEMP_TABLE,
                sqlite3.SQLITE_CREATE_TEMP_TRIGGER, sqlite3.SQLITE_CREATE_TEMP_VIEW,
                sqlite3.SQLITE_CREATE_TRIGGER, sqlite3.SQLITE_CREATE_VIEW, sqlite3.SQLITE_CREATE_VTABLE,
                sqlite3.SQLITE_DROP_INDEX, sqlite3.SQLITE_DROP_TABLE, sqlite3.SQLITE_DROP_TEMP_INDEX,
                sqlite3.SQLITE_DROP_TEMP_TABLE, sqlite3.SQLITE_DROP_TEMP_TRIGGER, sqlite3.SQLITE_DROP_TEMP_VIEW,
                sqlite3.SQLITE_DROP_TRIGGER, sqlite3.SQLITE_DROP_VIEW, sqlite3.SQLITE_DROP_VTABLE,
                sqlite3.SQLITE_REINDEX, sqlite3.SQLITE_ANALYZE):
            return sqlite3.SQLITE_DENY
        if action in (sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE, sqlite3.SQLITE_DELETE):
            action_name = {sqlite3.SQLITE_INSERT: "INSERT", sqlite3.SQLITE_UPDATE: "UPDATE", sqlite3.SQLITE_DELETE: "DELETE"}[action]
            if arg1 == "pilot_business_capacity_counts":
                if action in (sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE) and source == "pilot_allocation_capacity_after_insert":
                    return sqlite3.SQLITE_OK
                return sqlite3.SQLITE_DENY
            if arg1 == "pilot_capacity_alert_events":
                return sqlite3.SQLITE_OK if action == sqlite3.SQLITE_INSERT and source == "pilot_allocation_capacity_after_insert" else sqlite3.SQLITE_DENY
            if arg1 in PROTECTED_TABLES:
                return authorize_protected_dml(arg1, action_name, source, db.current_capability)
        return sqlite3.SQLITE_OK
    db.set_authorizer(authorizer)
    return db


@contextmanager
def with_capability(db: AuthorizedConnection, name: str):
    assert db.current_capability is None, "writer capability cannot be nested"
    _set_capability(db, name, _CAPABILITY_SCOPE_TOKEN)
    try:
        yield
    finally:
        _set_capability(db, None, _CAPABILITY_SCOPE_TOKEN)


def execute_script(db: sqlite3.Connection, script: str) -> None:
    pending = ""
    for line in script.splitlines(keepends=True):
        pending += line
        if sqlite3.complete_statement(pending):
            statement = pending.strip()
            if statement and not all(part.lstrip().startswith("--") for part in statement.splitlines() if part.strip()):
                # execute() does not issue the implicit COMMIT used by executescript().
                sql = "\n".join(line for line in statement.splitlines() if not line.lstrip().startswith("--"))
                if sql.strip():
                    db.execute(sql)
            pending = ""
    assert not pending.strip(), f"incomplete SQL tail: {pending[-100:]}"


def expect_abort(db: sqlite3.Connection, sql: str, args: tuple = ()) -> None:
    try:
        db.execute(sql, args)
    except sqlite3.IntegrityError:
        return
    raise AssertionError(f"expected constraint/trigger rejection: {sql}")


def expect_denied(db: sqlite3.Connection, sql: str, args: tuple = ()) -> None:
    try:
        db.execute(sql, args)
    except sqlite3.DatabaseError as error:
        code = getattr(error, "sqlite_errorcode", None)
        assert code == sqlite3.SQLITE_AUTH, f"expected SQLITE_AUTH, got {code}: {error}"
        return
    raise AssertionError(f"expected connection-authorizer denial: {sql}")


def check_invariants(db: sqlite3.Connection) -> None:
    text = INV.read_text(encoding="utf-8")
    pieces = []
    pending = ""
    current = None
    for line in text.splitlines():
        if line.startswith("-- INV-"):
            current = line
            pending = ""
        elif current and not line.lstrip().startswith("--"):
            pending += line + "\n"
            if sqlite3.complete_statement(pending):
                pieces.append((current, pending))
                current, pending = None, ""
    assert len(pieces) == 11, len(pieces)
    for name, query in pieces:
        try:
            rows = db.execute(query).fetchall()
        except sqlite3.Error as exc:
            raise AssertionError(f"{name} failed to execute: {exc}") from exc
        assert rows == [(0,)], f"{name} returned {rows!r}"


def invariant_count(db: sqlite3.Connection, invariant_id: str) -> int:
    text = INV.read_text(encoding="utf-8")
    current = None
    pending = ""
    for line in text.splitlines():
        if line.startswith("-- INV-"):
            if current and sqlite3.complete_statement(pending):
                if current.startswith(f"-- {invariant_id} "):
                    return db.execute(pending).fetchone()[0]
            current, pending = line, ""
        elif current and not line.lstrip().startswith("--"):
            pending += line + "\n"
            if sqlite3.complete_statement(pending):
                if current.startswith(f"-- {invariant_id} "):
                    return db.execute(pending).fetchone()[0]
                current, pending = None, ""
    raise AssertionError(f"invariant not found: {invariant_id}")


def check_missing_evidence_faults(db: sqlite3.Connection) -> None:
    missing_receipt = sqlite3.connect(":memory:")
    missing_receipt.close()
    missing_receipt = db_new()
    capacity_trigger_fixture(missing_receipt)
    missing_receipt.set_authorizer(None)
    missing_receipt.execute("DROP TRIGGER pilot_operation_receipts_no_delete")
    missing_receipt.execute("DELETE FROM pilot_operation_evidence_receipts WHERE operation_id=(SELECT operation_id FROM run_root_operations WHERE response_code='created')")
    assert invariant_count(missing_receipt, "INV-05") > 0

    missing_finish = sqlite3.connect(":memory:")
    missing_finish.close()
    missing_finish = db_new()
    capacity_trigger_fixture(missing_finish)
    missing_finish.set_authorizer(None)
    missing_finish.execute("DROP TRIGGER pilot_outbox_no_delete")
    missing_finish.execute("DELETE FROM run_root_event_outbox WHERE event_name='bootstrap.finish'")
    assert invariant_count(missing_finish, "INV-05") > 0

    missing_close_receipt = db_new()
    capacity_trigger_fixture(missing_close_receipt)
    missing_close_receipt.set_authorizer(None)
    missing_close_receipt.execute("DROP TRIGGER pilot_operation_receipts_no_delete")
    missing_close_receipt.execute("DELETE FROM pilot_operation_evidence_receipts WHERE operation_id IN (SELECT operation_id FROM run_root_operations WHERE operation_kind='pilot_run_close')")
    assert invariant_count(missing_close_receipt, "INV-07") > 0

    missing_prepared_close_receipt = db_new()
    capacity_trigger_fixture(missing_prepared_close_receipt)
    missing_prepared_close_receipt.set_authorizer(None)
    missing_prepared_close_receipt.execute("DROP TRIGGER pilot_operation_receipts_no_delete")
    missing_prepared_close_receipt.execute("DELETE FROM pilot_operation_evidence_receipts WHERE operation_id=?", (b"C"*16,))
    assert missing_prepared_close_receipt.execute("SELECT count(*) FROM pilot_prepared_cancel_receipts WHERE close_operation_id=?", (b"C"*16,)).fetchone() == (1,)
    assert invariant_count(missing_prepared_close_receipt, "INV-07") > 0

    orphan_terminal = sqlite3.connect(":memory:")
    orphan_terminal.close()
    orphan_terminal = db_new()
    capacity_trigger_fixture(orphan_terminal)
    orphan_terminal.set_authorizer(None)
    orphan_terminal.execute("DROP TRIGGER pilot_lifecycle_update_guard")
    orphan_terminal.execute("UPDATE pilot_run_lifecycle SET lifecycle_state='terminal',terminal_result='abandoned',run_root_operation_id=NULL WHERE run_epoch=(SELECT run_epoch FROM pilot_root_run_state LIMIT 1)")
    assert invariant_count(orphan_terminal, "INV-07") > 0

    no_root_missing_close_receipt = db_new()
    no_root_abort_fixture(no_root_missing_close_receipt)
    no_root_missing_close_receipt.set_authorizer(None)
    no_root_missing_close_receipt.execute("DROP TRIGGER pilot_operation_receipts_no_delete")
    no_root_missing_close_receipt.execute("DELETE FROM pilot_operation_evidence_receipts WHERE operation_id=(SELECT operation_id FROM run_root_operations WHERE operation_kind='pilot_no_root_abort')")
    assert invariant_count(no_root_missing_close_receipt, "INV-07") > 0

    eexist_lost_lease = db_new()
    capacity_trigger_fixture(eexist_lost_lease)
    eexist_lost_lease.set_authorizer(None)
    eexist_lost_lease.execute("DROP TRIGGER pilot_lease_update_guard")
    eexist_lost_lease.execute("UPDATE pilot_run_lease SET run_epoch=NULL,generation=NULL,operation_id=NULL,updated_at=53 WHERE singleton_id=1")
    assert invariant_count(eexist_lost_lease, "INV-06") > 0

    eexist_promoted = db_new()
    capacity_trigger_fixture(eexist_promoted)
    eexist_promoted.set_authorizer(None)
    eexist_promoted.execute("DROP TRIGGER pilot_lifecycle_update_guard")
    eexist_promoted.execute("UPDATE pilot_run_lifecycle SET lifecycle_state='active',updated_at=54 WHERE run_epoch=(SELECT run_epoch FROM run_epoch_allocations WHERE allocation_sequence=3)")
    assert invariant_count(eexist_promoted, "INV-06") > 0


def no_root_abort_fixture(db: sqlite3.Connection) -> None:
    generation, epoch, opid = b"G" * 8, b"E" * 16, b"O" * 16
    nonce = "0123456789abcdef0123456789abcdef"
    tenant, principal, business, request, payload = b"T1", b"P" * 32, b"B" * 16, b"Q" * 16, b"D" * 32
    with with_capability(db, "ConsumeBootstrap"):
        db.execute("INSERT INTO pilot_control_generations VALUES(?,?,?,?,?,?,?,?)",
                   (generation, nonce, "normal", epoch, None, None, None, 1))
        db.execute("""INSERT INTO pilot_bootstrap_handoffs
      (generation,launch_nonce,run_epoch,control_mode,gateway_pid,gateway_uid,gateway_start_ticks,
       gateway_pidns_device,gateway_pidns_inode,gateway_cgroup_mount_id,gateway_cgroup_device,gateway_cgroup_inode,handed_off_at)
      VALUES(?,?,?,'normal',1,1,?,?,?,?,?,?,1)""",
          (generation, nonce, epoch, b"1"*8, b"2"*8, b"3"*8, b"4"*8, b"5"*8, b"6"*8))
        db.execute("INSERT INTO pilot_run_lifecycle(generation,run_epoch,lifecycle_state,updated_at) VALUES(?,?,'bootstrapped',1)", (generation, epoch))
    with with_capability(db, "CommitLifecycleBoundary"):
        for state, at in (("logger_registered",2), ("logger_ready",3), ("reaper_ready",4), ("registered",5)):
            db.execute("UPDATE pilot_run_lifecycle SET lifecycle_state=?,updated_at=? WHERE generation=?", (state, at, generation))
    with with_capability(db, "BeginRunTermination"):
        # CHECK must reject NULL for root-create even though SQLite accepts NULL CHECK results.
        expect_abort(db, """INSERT INTO run_root_operations
          (operation_id,tenant_id,principal_ref,business_operation_id,operation_kind,request_id,run_epoch,
           allocated_run_epoch,generation,payload_digest,operation_state,owner_generation,created_at,updated_at)
          VALUES(X'78787878787878787878787878787878',?,?,?,'run_root_create',?,?,NULL,?,?, 'prepared',zeroblob(8),6,6)""",
          (tenant, principal, b"x"*16, request, epoch, generation, payload))
        db.execute("""INSERT INTO run_root_operations
      (operation_id,tenant_id,principal_ref,business_operation_id,operation_kind,request_id,run_epoch,
       allocated_run_epoch,generation,payload_digest,operation_state,owner_generation,created_at,updated_at)
      VALUES(?,?,?,?,'pilot_no_root_abort',?,?,NULL,?,?, 'prepared',zeroblob(8),6,6)""",
          (opid, tenant, principal, business, request, epoch, generation, payload))
        close_key, close_payload = b"K"*32, payload
        db.execute("""UPDATE pilot_run_lifecycle SET lifecycle_state='terminating',close_operation_id=?,
      close_key_hash=?,close_payload_digest=?,updated_at=7 WHERE generation=?""", (opid, close_key, close_payload, generation))
        expect_abort(db, "UPDATE pilot_run_lifecycle SET close_key_hash=NULL,updated_at=8 WHERE generation=?", (generation,))
        expect_abort(db, "UPDATE run_root_operations SET operation_state='executing',updated_at=8 WHERE operation_id=?", (opid,))
        db.execute("UPDATE run_root_operations SET operation_state='executing',owner_generation=?,updated_at=8 WHERE operation_id=?", (b"W"*8, opid))
    with with_capability(db, "CommitUnregister"):
        db.execute("UPDATE pilot_run_lifecycle SET lifecycle_state='unregistered',updated_at=9 WHERE generation=?", (generation,))
    with with_capability(db, "RecordContainmentEvidence"):
        db.execute("""INSERT INTO pilot_run_containment_evidence
          (run_epoch,gateway_generation,launch_nonce,gateway_pid,gateway_uid,gateway_start_ticks,gateway_pidns_device,
           gateway_pidns_inode,gateway_cgroup_mount_id,gateway_cgroup_device,gateway_cgroup_inode,
           endpoint_parent_device,endpoint_parent_inode,endpoint_device,endpoint_inode,endpoint_removed,leader_reaped,
           populated,evidence_version,launcher_evidence_digest,evidence_digest,canonical_receipt,recorded_at)
          VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
          (epoch,generation,nonce,1,1,b"1"*8,b"2"*8,b"3"*8,b"4"*8,b"5"*8,b"6"*8,
           b"7"*8,b"8"*8,b"9"*8,b"A"*8,1,1,0,1,b"B"*32,b"C"*32,b"{}",10))
        db.execute("UPDATE pilot_run_lifecycle SET lifecycle_state='containment_empty',updated_at=11 WHERE generation=?", (generation,))
    with with_capability(db, "FinalizeNoRootAbort"):
        close_receipt = b'{"domain":"pilot-run-close-v1","terminal_result":"closed_without_root"}'
        close_receipt_digest = hashlib.sha256(close_receipt).digest()
        db.execute("""INSERT INTO pilot_operation_evidence_receipts
          (receipt_digest,operation_id,run_epoch,principal_ref,payload_digest,canonical_receipt,created_at)
          VALUES(?,?,?,?,?,?,12)""", (close_receipt_digest,opid,epoch,principal,payload,close_receipt))
        db.execute("""UPDATE run_root_operations SET operation_state='completed',response_code='closed_without_root',
          canonical_response=?,receipt_digest=?,updated_at=13 WHERE operation_id=?""", (close_receipt,close_receipt_digest,opid))
        expect_abort(db, "UPDATE run_root_operations SET canonical_response=NULL,updated_at=14 WHERE operation_id=?", (opid,))
        db.execute("UPDATE pilot_run_lifecycle SET lifecycle_state='terminal',terminal_result='closed_without_root',updated_at=14 WHERE generation=?", (generation,))


def capacity_trigger_fixture(db: sqlite3.Connection) -> None:
    tenant, principal = b"T1", b"R" * 32
    for n in range(1, 4):
        generation = n.to_bytes(8, "big")
        epoch, opid = bytes([n]) * 16, bytes([n + 10]) * 16
        request, business, payload = bytes([30+n]) * 16, bytes([40+n]) * 16, bytes([20+n]) * 32
        nonce = f"{n:032x}"
        with with_capability(db, "ConsumeBootstrap"):
            db.execute("INSERT INTO pilot_control_generations VALUES(?,?, 'normal', ?,NULL,NULL,NULL,?)",
                       (generation, nonce, epoch, n))
            db.execute("""INSERT INTO pilot_bootstrap_handoffs
          (generation,launch_nonce,run_epoch,control_mode,gateway_pid,gateway_uid,gateway_start_ticks,
           gateway_pidns_device,gateway_pidns_inode,gateway_cgroup_mount_id,gateway_cgroup_device,gateway_cgroup_inode,handed_off_at)
          VALUES(?,?,?,'normal',1,1,?,?,?,?,?,?,?)""",
              (generation, nonce, epoch, b"1"*8, b"2"*8, b"3"*8, b"4"*8, b"5"*8, b"6"*8, n))
            db.execute("INSERT INTO pilot_run_lifecycle(generation,run_epoch,lifecycle_state,updated_at) VALUES(?,?,'bootstrapped',?)",
                       (generation, epoch, n))
        if n in (1, 2, 3):
            with with_capability(db, "CommitLifecycleBoundary"):
                for state, at in (("logger_registered",11), ("logger_ready",12), ("reaper_ready",13), ("registered",14)):
                    db.execute("UPDATE pilot_run_lifecycle SET lifecycle_state=?,updated_at=? WHERE generation=?", (state, at, generation))
        with with_capability(db, "PrepareWithHandoff"):
            db.execute("""INSERT INTO run_epoch_allocations
          (run_epoch,allocation_sequence,generation,tenant_id,principal_ref,session_key_hash,gateway_pid,gateway_uid,
           gateway_pidns_device,gateway_pidns_inode,gateway_cgroup_mount_id,gateway_cgroup_device,gateway_cgroup_inode,
           gateway_start_ticks,schema_digest,payload_digest,request_id,business_operation_id,operation_id,
           allocation_state,run_terminal_state,terminal_evidence_digest,allocated_at)
          VALUES(?,?,?,?,?,?,1,1,?,?,?,?,?,?,?,?,?,?,?,'prepared','none',NULL,?)""",
              (epoch, n, generation, tenant, principal, b"S"*32, b"2"*8, b"3"*8, b"4"*8, b"5"*8, b"6"*8,
               b"1"*8, b"D"*32, bytes([20+n])*32, bytes([30+n])*16, bytes([40+n])*16, opid, n))
            if n in (1, 2, 3):
                db.execute("""INSERT INTO run_root_operations(operation_id,tenant_id,principal_ref,business_operation_id,operation_kind,request_id,
                  run_epoch,allocated_run_epoch,generation,payload_digest,operation_state,owner_generation,created_at,updated_at)
                  VALUES(?,?,?,?,'run_root_create',?,?,?,?,?,'prepared',zeroblob(8),20,20)""",
                  (opid,tenant,principal,business,request,epoch,epoch,generation,payload))
                # Operation identity is bound in the same capability/transaction as allocation preparation.
                db.execute("UPDATE pilot_run_lifecycle SET run_root_operation_id=?,updated_at=20 WHERE generation=?", (opid,generation))
                if n == 1:
                    db.execute("UPDATE pilot_run_lease SET run_epoch=?,generation=?,operation_id=?,updated_at=20 WHERE singleton_id=1", (epoch,generation,opid))
    assert db.execute("SELECT count FROM pilot_business_capacity_counts WHERE scope_kind='global'").fetchone() == (3,)
    assert db.execute("SELECT count FROM pilot_business_capacity_counts WHERE scope_kind='tenant'").fetchone() == (3,)
    assert db.execute("SELECT count FROM pilot_business_capacity_counts WHERE scope_kind='principal'").fetchone() == (3,)
    assert db.execute("SELECT threshold_percent,first_crossing_count FROM pilot_capacity_alert_events WHERE scope_kind='principal' ORDER BY threshold_percent").fetchall() == [(80, 3), (90, 3)]
    first = db.execute("SELECT run_epoch,generation,tenant_id,principal_ref,business_operation_id,request_id,operation_id,payload_digest FROM run_epoch_allocations WHERE allocation_sequence=1").fetchone()
    epoch,generation,tenant,principal,business,request,opid,payload = first
    with with_capability(db, "Claim"):
        db.execute("UPDATE run_root_operations SET operation_state='executing',owner_generation=?,updated_at=21 WHERE operation_id=?", (b"W"*8,opid))
        db.execute("UPDATE run_epoch_allocations SET allocation_state='executing' WHERE run_epoch=?", (epoch,))
    with with_capability(db, "FinishWithOutbox"):
        db.execute("UPDATE run_root_operations SET operation_state='completed',response_code='created',canonical_response=?,receipt_digest=?,updated_at=22 WHERE operation_id=?", (b"{}",b"H"*32,opid))
        db.execute("UPDATE run_epoch_allocations SET allocation_state='completed',run_terminal_state='active' WHERE run_epoch=?", (epoch,))
        db.execute("INSERT INTO pilot_operation_evidence_receipts(receipt_digest,operation_id,run_epoch,principal_ref,payload_digest,canonical_receipt,created_at) VALUES(?,?,?,?,?,?,22)", (b"H"*32,opid,epoch,principal,payload,b"{}"))
        db.execute("INSERT INTO run_root_event_outbox(outbox_id,operation_id,event_name,process_id,canonical_event,event_digest,delivery_state,created_at) VALUES(?,?,'bootstrap.finish',1,?,?,'ready',23)", (b"J"*16,opid,b"{}",b"D"*32))
        db.execute("INSERT INTO pilot_root_run_state(run_epoch,root_mount_id,root_device,root_inode,run_state,cleanup_state,updated_at) VALUES(?,?,?,?,'active','not_required',24)", (epoch,b"1"*8,b"2"*8,b"3"*8))
    with with_capability(db, "AuthorizeOutboxSend"):
        db.execute("UPDATE run_root_event_outbox SET delivery_state='send_authorized' WHERE outbox_id=?", (b"J"*16,))
    with with_capability(db, "AcknowledgeOutbox"):
        db.execute("UPDATE run_root_event_outbox SET delivery_state='acknowledged',acknowledged_digest=event_digest,acknowledged_correlation=? WHERE outbox_id=?", (b"N"*16,b"J"*16))
        expect_abort(db, "UPDATE run_root_event_outbox SET acknowledged_digest=NULL WHERE outbox_id=?", (b"J"*16,))
    finish_prepared_cancellation(db)
    finish_normal_root_close(db)
    finish_eexist_collision(db)
    check_invariants(db)


def finish_eexist_collision(db: sqlite3.Connection) -> None:
    row = db.execute("""SELECT a.run_epoch,a.generation,a.tenant_id,a.principal_ref,a.business_operation_id,
      a.request_id,a.operation_id,a.payload_digest FROM run_epoch_allocations a
      WHERE a.allocation_sequence=3""").fetchone()
    epoch,generation,tenant,principal,business,request,opid,payload = row
    canonical = b'{"result":"exists"}'
    with with_capability(db, "PrepareWithHandoff"):
        db.execute("UPDATE pilot_run_lease SET run_epoch=?,generation=?,operation_id=?,updated_at=50 WHERE singleton_id=1", (epoch,generation,opid))
    with with_capability(db, "Claim"):
        db.execute("UPDATE run_root_operations SET operation_state='executing',owner_generation=?,updated_at=50 WHERE operation_id=?", (b"X"*8,opid))
        db.execute("UPDATE run_epoch_allocations SET allocation_state='executing' WHERE run_epoch=?", (epoch,))
    with with_capability(db, "FinishWithOutbox"):
        db.execute("UPDATE run_root_operations SET operation_state='abandoned',response_code='exists',canonical_response=?,receipt_digest=?,updated_at=51 WHERE operation_id=?", (canonical,b"X"*32,opid))
        db.execute("UPDATE run_epoch_allocations SET allocation_state='indeterminate',run_terminal_state='indeterminate' WHERE run_epoch=?", (epoch,))
        db.execute("INSERT INTO pilot_operation_evidence_receipts(receipt_digest,operation_id,run_epoch,principal_ref,payload_digest,canonical_receipt,created_at) VALUES(?,?,?,?,?,?,51)", (b"X"*32,opid,epoch,principal,payload,canonical))
        db.execute("INSERT INTO run_root_event_outbox(outbox_id,operation_id,event_name,process_id,canonical_event,event_digest,delivery_state,created_at) VALUES(?,?,'bootstrap.finish',1,?,?,'ready',52)", (b"Y"*16,opid,canonical,b"Y"*32))
    with with_capability(db, "AuthorizeOutboxSend"):
        db.execute("UPDATE run_root_event_outbox SET delivery_state='send_authorized' WHERE outbox_id=?", (b"Y"*16,))
    with with_capability(db, "AcknowledgeOutbox"):
        db.execute("UPDATE run_root_event_outbox SET delivery_state='acknowledged',acknowledged_digest=event_digest,acknowledged_correlation=? WHERE outbox_id=?", (b"Z"*16,b"Y"*16))
    assert db.execute("SELECT lifecycle_state FROM pilot_run_lifecycle WHERE generation=?", (generation,)).fetchone() == ("registered",)
    assert db.execute("SELECT count(*) FROM pilot_root_run_state WHERE run_epoch=?", (epoch,)).fetchone() == (0,)


def finish_normal_root_close(db: sqlite3.Connection, audit_boundaries: bool = False) -> None:
    epoch,generation,tenant,principal = db.execute("SELECT run_epoch,generation,tenant_id,principal_ref FROM run_epoch_allocations WHERE allocation_sequence=1").fetchone()
    nonce = db.execute("SELECT launch_nonce FROM pilot_bootstrap_handoffs WHERE generation=?", (generation,)).fetchone()[0]
    close_id, business, request, payload, close_key = b"D"*16, b"M"*16, b"N"*16, b"P"*32, b"Q"*32
    with with_capability(db, "BeginRunTermination"):
        db.execute("""INSERT INTO run_root_operations(operation_id,tenant_id,principal_ref,business_operation_id,operation_kind,request_id,
          run_epoch,allocated_run_epoch,generation,payload_digest,operation_state,owner_generation,created_at,updated_at)
          VALUES(?,?,?,?,'pilot_run_close',?,?,NULL,?,?, 'prepared',zeroblob(8),40,40)""",
          (close_id,tenant,principal,business,request,epoch,generation,payload))
        db.execute("""UPDATE pilot_run_lifecycle SET lifecycle_state='terminating',close_operation_id=?,close_key_hash=?,
          close_payload_digest=?,updated_at=41 WHERE generation=?""", (close_id,close_key,payload,generation))
        db.execute("UPDATE run_root_operations SET operation_state='executing',owner_generation=?,updated_at=42 WHERE operation_id=?",
                   (b"U"*8,close_id))
        db.execute("UPDATE pilot_root_run_state SET run_state='terminating',cleanup_state='pending',updated_at=42 WHERE run_epoch=?", (epoch,))
    if audit_boundaries:
        check_invariants(db)
    with with_capability(db, "RecordContainmentEvidence"):
        db.execute("UPDATE pilot_run_lifecycle SET lifecycle_state='unregistered',updated_at=44 WHERE generation=?", (generation,))
        db.execute("""INSERT INTO pilot_run_containment_evidence
          (run_epoch,gateway_generation,launch_nonce,gateway_pid,gateway_uid,gateway_start_ticks,gateway_pidns_device,
           gateway_pidns_inode,gateway_cgroup_mount_id,gateway_cgroup_device,gateway_cgroup_inode,
           endpoint_parent_device,endpoint_parent_inode,endpoint_device,endpoint_inode,endpoint_removed,leader_reaped,
           populated,evidence_version,launcher_evidence_digest,evidence_digest,canonical_receipt,recorded_at)
          VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
          (epoch,generation,nonce,1,1,b"1"*8,b"2"*8,b"3"*8,b"4"*8,b"5"*8,b"6"*8,
           b"7"*8,b"8"*8,b"9"*8,b"A"*8,1,1,0,1,b"B"*32,b"E"*32,b"{}",44))
        db.execute("UPDATE pilot_root_run_state SET run_state='containment_empty',containment_evidence_digest=?,updated_at=44 WHERE run_epoch=?", (b"E"*32,epoch))
        db.execute("UPDATE pilot_run_lifecycle SET lifecycle_state='containment_empty',updated_at=45 WHERE generation=?", (generation,))
        expect_abort(db, "UPDATE pilot_root_run_state SET containment_evidence_digest=NULL,updated_at=46 WHERE run_epoch=?", (epoch,))
    if audit_boundaries:
        check_invariants(db)
    with with_capability(db, "CommitRunCleanupDisposition"):
        db.execute("INSERT INTO pilot_run_cleanup_dispositions VALUES(?, 'complete', ?,?,?,?,?,46)",
                   (epoch,b"1"*8,b"2"*8,b"3"*8,b"Z"*32,b"{}"))
        db.execute("UPDATE pilot_root_run_state SET cleanup_state='complete',updated_at=47 WHERE run_epoch=?", (epoch,))
    if audit_boundaries:
        check_invariants(db)
    with with_capability(db, "FinalizeRunTerminal"):
        close_receipt = b'{"domain":"pilot-run-close-v1","terminal_result":"abandoned","containment":"verified"}'
        close_receipt_digest = hashlib.sha256(close_receipt).digest()
        db.execute("INSERT INTO pilot_operation_evidence_receipts(receipt_digest,operation_id,run_epoch,principal_ref,payload_digest,canonical_receipt,created_at) VALUES(?,?,?,?,?,?,48)",
                   (close_receipt_digest,close_id,epoch,principal,payload,close_receipt))
        db.execute("UPDATE run_root_operations SET operation_state='completed',response_code='abandoned',canonical_response=?,receipt_digest=?,updated_at=48 WHERE operation_id=?",
                   (close_receipt,close_receipt_digest,close_id))
        db.execute("UPDATE run_epoch_allocations SET run_terminal_state='abandoned',terminal_evidence_digest=? WHERE run_epoch=?", (b"T"*32,epoch))
        db.execute("UPDATE pilot_run_lease SET run_epoch=NULL,generation=NULL,operation_id=NULL,updated_at=48 WHERE singleton_id=1")
        expect_abort(db, "UPDATE run_epoch_allocations SET terminal_evidence_digest=NULL WHERE run_epoch=?", (epoch,))
        db.execute("UPDATE pilot_root_run_state SET run_state='abandoned',updated_at=48 WHERE run_epoch=?", (epoch,))
        db.execute("UPDATE pilot_run_lifecycle SET lifecycle_state='terminal',terminal_result='abandoned',updated_at=48 WHERE generation=?", (generation,))
    if audit_boundaries:
        check_invariants(db)


def finish_prepared_cancellation(db: sqlite3.Connection) -> None:
    row = db.execute("""SELECT a.run_epoch,a.generation,a.tenant_id,a.principal_ref,a.business_operation_id,a.request_id,a.operation_id,
      a.payload_digest,l.launch_nonce FROM run_epoch_allocations a JOIN pilot_bootstrap_handoffs l
      ON l.generation=a.generation AND l.run_epoch=a.run_epoch WHERE a.allocation_sequence=2""").fetchone()
    epoch,generation,tenant,principal,business,request,opid,payload,nonce = row
    close_id, close_business, close_request, close_payload = b"C"*16, b"X"*16, b"Y"*16, b"Z"*32
    close_key, cancel_digest, intent_digest, evidence_digest = b"K"*32, b"R"*32, b"I"*32, b"E"*32
    with with_capability(db, "BeginRunTermination"):
        db.execute("""INSERT INTO run_root_operations(operation_id,tenant_id,principal_ref,business_operation_id,operation_kind,request_id,
          run_epoch,allocated_run_epoch,generation,payload_digest,operation_state,owner_generation,created_at,updated_at)
          VALUES(?,?,?,?,'pilot_run_close',?,?,NULL,?,?, 'prepared',zeroblob(8),30,30)""",
          (close_id,tenant,principal,close_business,close_request,epoch,generation,close_payload))
        db.execute("""UPDATE pilot_run_lifecycle SET lifecycle_state='terminating',close_operation_id=?,close_key_hash=?,
          close_payload_digest=?,updated_at=31 WHERE generation=?""", (close_id,close_key,close_payload,generation))
        db.execute("UPDATE run_root_operations SET operation_state='executing',owner_generation=?,updated_at=32 WHERE operation_id=?",
                   (b"V"*8,close_id))
    with with_capability(db, "CancelPreparedRunRoot"):
        db.execute("UPDATE run_root_operations SET operation_state='abandoned',response_code='abandoned',canonical_response=?,receipt_digest=?,updated_at=34 WHERE operation_id=?",
                   (b"{}",cancel_digest,opid))
        db.execute("UPDATE run_epoch_allocations SET allocation_state='abandoned' WHERE run_epoch=?", (epoch,))
        db.execute("""INSERT INTO pilot_prepared_cancel_receipts
          (receipt_digest,operation_id,generation,run_epoch,close_operation_id,intent_delivery_disposition,intent_digest,
           intent_correlation,absence_witness_digest,canonical_receipt,created_at)
          VALUES(?,?,?,?,?,'never_authorized',?,NULL,?,?,35)""",
          (cancel_digest,opid,generation,epoch,close_id,intent_digest,b"A"*32,b"{}"))
    with with_capability(db, "RecordContainmentEvidence"):
        db.execute("UPDATE pilot_run_lifecycle SET lifecycle_state='unregistered',updated_at=36 WHERE generation=?", (generation,))
        db.execute("""INSERT INTO pilot_run_containment_evidence
          (run_epoch,gateway_generation,launch_nonce,gateway_pid,gateway_uid,gateway_start_ticks,gateway_pidns_device,
           gateway_pidns_inode,gateway_cgroup_mount_id,gateway_cgroup_device,gateway_cgroup_inode,
           endpoint_parent_device,endpoint_parent_inode,endpoint_device,endpoint_inode,endpoint_removed,leader_reaped,
           populated,evidence_version,launcher_evidence_digest,evidence_digest,canonical_receipt,recorded_at)
          VALUES(?,?,?,1,1,?,?,?,?,?,?,?,?,?,?,1,1,0,1,?,?,?,37)""",
          (epoch,generation,nonce,b"1"*8,b"2"*8,b"3"*8,b"4"*8,b"5"*8,b"6"*8,
           b"7"*8,b"8"*8,b"9"*8,b"B"*8,b"C"*32,evidence_digest,b"{}"))
        db.execute("UPDATE pilot_run_lifecycle SET lifecycle_state='containment_empty',updated_at=38 WHERE generation=?", (generation,))
    with with_capability(db, "FinalizePreparedCancellation"):
        close_receipt = b'{"domain":"pilot-run-close-v1","terminal_result":"abandoned","prepared_cancel":true}'
        close_receipt_digest = hashlib.sha256(close_receipt).digest()
        db.execute("INSERT INTO pilot_operation_evidence_receipts(receipt_digest,operation_id,run_epoch,principal_ref,payload_digest,canonical_receipt,created_at) VALUES(?,?,?,?,?,?,39)",
                   (close_receipt_digest,close_id,epoch,principal,close_payload,close_receipt))
        db.execute("UPDATE run_root_operations SET operation_state='completed',response_code='abandoned',canonical_response=?,receipt_digest=?,updated_at=39 WHERE operation_id=?",
                   (close_receipt,close_receipt_digest,close_id))
        db.execute("UPDATE run_epoch_allocations SET allocation_state='abandoned',run_terminal_state='abandoned',terminal_evidence_digest=? WHERE run_epoch=?",
                   (evidence_digest,epoch))
        db.execute("UPDATE pilot_run_lifecycle SET lifecycle_state='terminal',terminal_result='abandoned',updated_at=39 WHERE generation=?",
                   (generation,))


def single_created_root_fixture(db: sqlite3.Connection) -> None:
    generation, epoch, opid = (1).to_bytes(8,"big"), b"1"*16, b"\x0b"*16
    request, business, payload = b"\x1f"*16, b"\x29"*16, b"\x15"*32
    tenant, principal, nonce = b"T1", b"R"*32, f"{1:032x}"
    with with_capability(db, "ConsumeBootstrap"):
        db.execute("INSERT INTO pilot_control_generations VALUES(?,?, 'normal', ?,NULL,NULL,NULL,?)", (generation,nonce,epoch,1))
        db.execute("""INSERT INTO pilot_bootstrap_handoffs
          (generation,launch_nonce,run_epoch,control_mode,gateway_pid,gateway_uid,gateway_start_ticks,gateway_pidns_device,
           gateway_pidns_inode,gateway_cgroup_mount_id,gateway_cgroup_device,gateway_cgroup_inode,handed_off_at)
          VALUES(?,?,?,'normal',1,1,?,?,?,?,?,?,1)""", (generation,nonce,epoch,b"1"*8,b"2"*8,b"3"*8,b"4"*8,b"5"*8,b"6"*8))
        db.execute("INSERT INTO pilot_run_lifecycle(generation,run_epoch,lifecycle_state,updated_at) VALUES(?,?,'bootstrapped',1)", (generation,epoch))
    with with_capability(db, "CommitLifecycleBoundary"):
        for state, at in (("logger_registered",2),("logger_ready",3),("reaper_ready",4),("registered",5)):
            db.execute("UPDATE pilot_run_lifecycle SET lifecycle_state=?,updated_at=? WHERE generation=?", (state,at,generation))
    with with_capability(db, "PrepareWithHandoff"):
        db.execute("""INSERT INTO run_epoch_allocations
          (run_epoch,allocation_sequence,generation,tenant_id,principal_ref,session_key_hash,gateway_pid,gateway_uid,
           gateway_pidns_device,gateway_pidns_inode,gateway_cgroup_mount_id,gateway_cgroup_device,gateway_cgroup_inode,
           gateway_start_ticks,schema_digest,payload_digest,request_id,business_operation_id,operation_id,allocation_state,
           run_terminal_state,terminal_evidence_digest,allocated_at)
          VALUES(?,1,?,?,?,?,1,1,?,?,?,?,?,?,?,?,?,?,?,'prepared','none',NULL,1)""",
          (epoch,generation,tenant,principal,b"S"*32,b"2"*8,b"3"*8,b"4"*8,b"5"*8,b"6"*8,b"1"*8,b"D"*32,payload,request,business,opid))
        db.execute("""INSERT INTO run_root_operations(operation_id,tenant_id,principal_ref,business_operation_id,operation_kind,request_id,
          run_epoch,allocated_run_epoch,generation,payload_digest,operation_state,owner_generation,created_at,updated_at)
          VALUES(?,?,?,?,'run_root_create',?,?,?,?,?,'prepared',zeroblob(8),6,6)""", (opid,tenant,principal,business,request,epoch,epoch,generation,payload))
        db.execute("UPDATE pilot_run_lifecycle SET run_root_operation_id=?,updated_at=6 WHERE generation=?", (opid,generation))
        db.execute("UPDATE pilot_run_lease SET run_epoch=?,generation=?,operation_id=?,updated_at=6 WHERE singleton_id=1", (epoch,generation,opid))
    with with_capability(db, "Claim"):
        db.execute("UPDATE run_root_operations SET operation_state='executing',owner_generation=?,updated_at=7 WHERE operation_id=?", (b"W"*8,opid))
        db.execute("UPDATE run_epoch_allocations SET allocation_state='executing' WHERE run_epoch=?", (epoch,))
    with with_capability(db, "FinishWithOutbox"):
        db.execute("UPDATE run_root_operations SET operation_state='completed',response_code='created',canonical_response=?,receipt_digest=?,updated_at=8 WHERE operation_id=?", (b"{}",b"H"*32,opid))
        db.execute("UPDATE run_epoch_allocations SET allocation_state='completed',run_terminal_state='active' WHERE run_epoch=?", (epoch,))
        db.execute("INSERT INTO pilot_operation_evidence_receipts VALUES(?,?,?,?,?,?,8)", (b"H"*32,opid,epoch,principal,payload,b"{}"))
        db.execute("INSERT INTO run_root_event_outbox(outbox_id,operation_id,event_name,process_id,canonical_event,event_digest,delivery_state,created_at) VALUES(?,?,'bootstrap.finish',1,?,?,'ready',9)", (b"J"*16,opid,b"{}",b"D"*32))
        db.execute("INSERT INTO pilot_root_run_state(run_epoch,root_mount_id,root_device,root_inode,run_state,cleanup_state,updated_at) VALUES(?,?,?,?,'active','not_required',9)", (epoch,b"1"*8,b"2"*8,b"3"*8))
    with with_capability(db, "AuthorizeOutboxSend"):
        db.execute("UPDATE run_root_event_outbox SET delivery_state='send_authorized' WHERE outbox_id=?", (b"J"*16,))
    with with_capability(db, "AcknowledgeOutbox"):
        db.execute("UPDATE run_root_event_outbox SET delivery_state='acknowledged',acknowledged_digest=event_digest,acknowledged_correlation=? WHERE outbox_id=?", (b"N"*16,b"J"*16))
        db.execute("UPDATE pilot_run_lifecycle SET lifecycle_state='active',updated_at=10 WHERE generation=?", (generation,))
    check_invariants(db)


def main() -> None:
    ddl_manifest = json.loads(DDL_MANIFEST.read_text(encoding="utf-8"))
    inventory_bytes = SCHEMA_INVENTORY.read_bytes()
    inventory = json.loads(inventory_bytes)
    inventory_hash = hashlib.sha256(inventory_bytes).hexdigest()
    assert ddl_manifest["schema_inventory_draft"]["inventory_bytes_sha256"] == inventory_hash
    assert ddl_manifest["schema_inventory_draft"]["ddl_sql_sha256"] == inventory["ddl_sql_sha256"]
    assert ddl_manifest["ddl_statement_count"] == len(ddl_manifest["ddl_statements"])
    assert inventory["explicit_object_count"] == len(ddl_manifest["accepted_explicit_objects"])
    for relative_path, expected_hash in ddl_manifest["source_hashes"].items():
        assert hashlib.sha256((ROOT / relative_path).read_bytes()).hexdigest() == expected_hash, relative_path
    for statement in ddl_manifest["ddl_statements"]:
        assert hashlib.sha256(statement["normalized_sql"].encode("utf-8")).hexdigest() == statement["sha256"]
    object_db = sqlite3.connect(":memory:")
    execute_script(object_db, DDL.read_text(encoding="utf-8"))
    normalize = lambda sql: re.sub(r"\s+", " ", re.sub(r"/\*.*?\*/", "", re.sub(r"(?m)^\s*--.*$", "", sql or ""), flags=re.S)).strip().rstrip(";").strip()
    actual_objects = [
        {"type": kind, "name": name, "table_name": table_name, "normalized_sql": normalize(sql)}
        for kind, name, table_name, sql in object_db.execute(
            "SELECT type,name,tbl_name,sql FROM sqlite_master "
            "WHERE substr(name,1,7) <> 'sqlite_' ORDER BY type,name")
    ]
    assert actual_objects == ddl_manifest["accepted_explicit_objects"], "manifest inventory differs from executed literal DDL"

    inventory_probe = sqlite3.connect(":memory:")
    inventory_probe.execute("CREATE TABLE sqliteXfoo(value INTEGER)")
    visible_names = {row[0] for row in inventory_probe.execute(
        "SELECT name FROM sqlite_master WHERE substr(name,1,7) <> 'sqlite_'"
    )}
    assert "sqliteXfoo" in visible_names, "literal sqlite_ filtering must retain user objects such as sqliteXfoo"

    # A mid-seed failure must roll back schema, seeds and user_version together.
    seed_payload = INIT_SEEDS.read_bytes()
    seed_data = json.loads(seed_payload)
    canonical_seed = (json.dumps(seed_data, ensure_ascii=False, sort_keys=True, separators=(",", ":")) + "\n").encode("utf-8")
    assert seed_payload == canonical_seed, "fixed seed manifest must be canonical JSON plus one LF"
    assert seed_data == {"format_version": 1, "seeds": []}
    seed_sql = SEED.read_text(encoding="utf-8")
    assert seed_sql.count("INSERT INTO pilot_initialization_state(") == 1, "there must be exactly one marker statement"
    user_version_at = seed_sql.lower().rfind("pragma user_version=3;")
    marker_at = seed_sql.lower().rfind("insert into pilot_initialization_state(")
    assert 0 <= user_version_at < marker_at, "user_version must precede the final marker"
    after_marker = seed_sql[marker_at:].lower()
    assert not re.search(r"\b(insert|update|delete|replace)\s+", after_marker[after_marker.find(";") + 1:]), "initialization marker must be final DML"
    failed = sqlite3.connect(":memory:")
    failed.execute("BEGIN IMMEDIATE")
    execute_script(failed, DDL.read_text(encoding="utf-8"))
    bad_seed = (SEED.read_text(encoding="utf-8").replace(":global_limit", "7").replace(":tenant_limit", "5")
                .replace(":principal_limit", "9").replace(":journal_id", "X'01010101010101010101010101010101'")
                .replace(":restore_generation", "X'0000000000000001'")
                .replace(":schema_manifest_sha256", f"X'{hashlib.sha256(SCHEMA_INVENTORY.read_bytes()).hexdigest()}'")
                .replace(":seed_manifest_sha256", f"X'{hashlib.sha256(INIT_SEEDS.read_bytes()).hexdigest()}'"))
    try:
        execute_script(failed, bad_seed)
    except sqlite3.IntegrityError:
        failed.rollback()
    else:
        raise AssertionError("invalid policy seed unexpectedly committed")
    assert failed.execute("SELECT count(*) FROM sqlite_master WHERE substr(name,1,7) <> 'sqlite_'").fetchone() == (0,)
    assert failed.execute("PRAGMA user_version").fetchone() == (0,)

    # A failure at the final marker DML must also roll back schema and user_version.
    marker_failure = sqlite3.connect(":memory:")
    marker_failure.execute("BEGIN IMMEDIATE")
    execute_script(marker_failure, DDL.read_text(encoding="utf-8"))
    marker_sql = ("INSERT INTO pilot_initialization_state(singleton_id,format_version,schema_manifest_sha256,seed_manifest_sha256,complete) "
                  f"VALUES(1,1,X'{inventory_hash}',X'{hashlib.sha256(INIT_SEEDS.read_bytes()).hexdigest()}',1);\n")
    duplicate_marker_seed = rendered_seed().replace("PRAGMA user_version=3;", marker_sql + "PRAGMA user_version=3;")
    try:
        execute_script(marker_failure, duplicate_marker_seed)
    except sqlite3.IntegrityError:
        marker_failure.rollback()
    else:
        raise AssertionError("duplicate marker unexpectedly committed")
    assert marker_failure.execute("SELECT count(*) FROM sqlite_master WHERE substr(name,1,7) <> 'sqlite_'").fetchone() == (0,)
    assert marker_failure.execute("PRAGMA user_version").fetchone() == (0,)

    db = db_new()
    assert db.execute("PRAGMA user_version").fetchone() == (3,)
    assert db.execute("PRAGMA foreign_key_check").fetchall() == []
    assert db.execute("SELECT control_generation FROM run_root_ledger_meta").fetchone() == (bytes.fromhex("0000000000000001"),)
    for protected_table in sorted(PROTECTED_TABLES):
        expect_denied(db, f'DELETE FROM "{protected_table}" WHERE 0')
        for action_name in ("INSERT", "UPDATE", "DELETE"):
            assert authorize_protected_dml(protected_table, action_name, None, None) == sqlite3.SQLITE_DENY
    assert authorize_protected_dml("pilot_initialization_state", "INSERT", None, None) == sqlite3.SQLITE_DENY
    assert authorize_protected_dml("intent_binding", "INSERT", None, "UnknownCapability") == sqlite3.SQLITE_DENY
    assert authorize_protected_dml("intent_binding", "INSERT", "untrusted_trigger", "FirstBindIfUnbound") == sqlite3.SQLITE_DENY
    try:
        checked_next_control_generation(bytes.fromhex("ffffffffffffffff"))
    except OverflowError:
        pass
    else:
        raise AssertionError("uint64 maximum control_generation must fail closed")
    assert db.execute("SELECT count(*),min(format_version),min(length(schema_manifest_sha256)),min(length(seed_manifest_sha256)),min(complete) FROM pilot_initialization_state").fetchone() == (1, 1, 32, 32, 1)
    assert db.execute("SELECT schema_manifest_sha256,seed_manifest_sha256 FROM pilot_initialization_state").fetchone() == (bytes.fromhex(hashlib.sha256(SCHEMA_INVENTORY.read_bytes()).hexdigest()), bytes.fromhex(hashlib.sha256(INIT_SEEDS.read_bytes()).hexdigest()))
    expect_denied(db, "UPDATE pilot_initialization_state SET complete=0 WHERE singleton_id=1")
    expect_denied(db, "DELETE FROM pilot_initialization_state WHERE singleton_id=1")
    expect_denied(db, "INSERT OR REPLACE INTO pilot_initialization_state VALUES(1,1,zeroblob(32),zeroblob(32),1)")
    intent_insert = "INSERT INTO intent_binding(handle_hash,principal_ref,decision,event_time_ns,run_epoch,control_generation,row_digest) VALUES(?,?,'BOUND',1,?,?,?)"
    intent_args = (b"H"*32,b"P"*32,b"E"*16,b"G"*8,b"D"*32)
    expect_denied(db, intent_insert, intent_args)
    with with_capability(db, "IncrementControlGeneration"):
        expect_denied(db, intent_insert, intent_args)
    assert db.current_capability is None, "failed operation must clear capability scope"
    try:
        with with_capability(db, "FirstBindIfUnbound"):
            raise RuntimeError("injected typed-method failure")
    except RuntimeError:
        pass
    else:
        raise AssertionError("injected capability-scope failure was not raised")
    assert db.current_capability is None, "exception path must clear capability scope"
    with with_capability(db, "FirstBindIfUnbound"):
        db.execute(intent_insert, intent_args)
    with with_capability(db, "ExpireIfUnbound"):
        db.execute("INSERT INTO intent_binding(handle_hash,principal_ref,decision,event_time_ns,run_epoch,control_generation,row_digest) VALUES(?,?,'EXPIRED',2,NULL,?,?)",
                   (b"X"*32,b"P"*32,b"G"*8,b"D"*32))
    expect_denied(db, "UPDATE intent_binding SET decision='EXPIRED' WHERE handle_hash=?", (b"H"*32,))
    expect_denied(db, "DELETE FROM intent_binding WHERE handle_hash=?", (b"H"*32,))
    expect_denied(db, "INSERT OR REPLACE INTO intent_binding(handle_hash,principal_ref,decision,event_time_ns,run_epoch,control_generation,row_digest) VALUES(?,?,'BOUND',2,?,?,?)",
                 (b"H"*32,b"P"*32,b"F"*16,b"G"*8,b"D"*32))
    triggerdb = db_new(trigger_source_probe=True)
    with with_capability(triggerdb, "PublishAuthorizationFence"):
        expect_denied(triggerdb, "INSERT INTO user_trigger_source(value) VALUES(1)")
    check_invariants(db)

    # Key rotation may preserve one principal across distinct alias key versions.
    with with_capability(db, "ResolvePrincipal"):
        db.execute("INSERT INTO pilot_principal_aliases VALUES(?,?,?,?,?,?)", (b"T1", 1, b"A"*32, b"P"*32, 1, 1))
        db.execute("INSERT INTO pilot_principal_aliases VALUES(?,?,?,?,?,?)", (b"T1", 2, b"B"*32, b"P"*32, 2, 2))
        expect_abort(db, "INSERT INTO pilot_principal_aliases VALUES(?,?,?,?,?,?)", (b"T1", 2, b"C"*32, b"P"*32, 2, 3))

    # Seeded counters cannot be forged, replaced, or decremented.
    expect_denied(db, "INSERT INTO pilot_business_capacity_counts VALUES('tenant',X'5431',zeroblob(32),0)")
    expect_denied(db, "UPDATE pilot_business_capacity_counts SET count=1 WHERE scope_kind='global'")
    expect_denied(db, "UPDATE pilot_business_capacity_counts SET count=-1 WHERE scope_kind='global'")
    expect_denied(db, "INSERT OR REPLACE INTO pilot_business_capacity_counts VALUES('global',X'',zeroblob(32),0)")
    expect_denied(db, "UPDATE run_root_ledger_meta SET control_generation=X'0000000000000002' WHERE singleton=1")
    with with_capability(db, "FirstBindIfUnbound"):
        expect_denied(db, "UPDATE run_root_ledger_meta SET control_generation=X'0000000000000002' WHERE singleton=1")
    with with_capability(db, "IncrementControlGeneration"):
        current = db.execute("SELECT control_generation FROM run_root_ledger_meta WHERE singleton=1").fetchone()[0]
        db.execute("UPDATE run_root_ledger_meta SET control_generation=? WHERE singleton=1", (checked_next_control_generation(current),))
    assert db.execute("SELECT control_generation FROM run_root_ledger_meta").fetchone() == (bytes.fromhex("0000000000000002"),)
    assert db.current_capability is None
    expect_denied(db, "UPDATE run_root_ledger_meta SET control_generation=X'0000000000000003' WHERE singleton=1")
    for sql in ("CREATE VIEW forbidden_view AS SELECT 1", "CREATE VIRTUAL TABLE forbidden_vtab USING fts5(x)",
                "CREATE TABLE forbidden_table(x)", "CREATE TEMP TABLE forbidden_temp(x)",
                "CREATE INDEX forbidden_index ON pilot_control_generations(launch_nonce)",
                "CREATE TRIGGER forbidden_trigger AFTER INSERT ON pilot_run_lease BEGIN SELECT 1; END",
                "DROP TABLE run_root_ledger_meta", "ALTER TABLE run_root_ledger_meta ADD COLUMN forbidden INTEGER",
                "REINDEX pilot_control_generations", "ANALYZE pilot_control_generations", "PRAGMA user_version=99"):
        expect_denied(db, sql)
    expect_denied(db, "UPDATE pilot_run_lease SET run_epoch=?,generation=?,operation_id=?,updated_at=1 WHERE singleton_id=1",
                  (b"E"*16,b"G"*8,b"O"*16))
    expect_denied(db, "INSERT INTO run_root_event_outbox(outbox_id,operation_id,event_name,process_id,canonical_event,event_digest,delivery_state,created_at) VALUES(?,?,'bootstrap.finish',1,?,?,'ready',1)",
                  (b"I"*16,b"O"*16,b"{}",b"D"*32))

    no_root_abort_fixture(db)
    check_invariants(db)

    boundarydb = db_new()
    single_created_root_fixture(boundarydb)
    finish_normal_root_close(boundarydb, audit_boundaries=True)

    capacitydb = db_new()
    capacity_trigger_fixture(capacitydb)
    check_missing_evidence_faults(capacitydb)

    # Session revocation is monotonic: revision can advance, but revoked cannot revive.
    authdb = db_new()

    journaldb = db_new()
    with with_capability(journaldb, "AdvanceLauncherJournalAnchor"):
        journaldb.execute("UPDATE pilot_launcher_journal_anchor SET last_sequence=1,last_record_hash=?,updated_at=1 WHERE singleton_id=1 AND last_sequence=0",
                          (bytes.fromhex("11" * 32),))
    assert journaldb.execute("SELECT last_sequence,last_record_hash FROM pilot_launcher_journal_anchor").fetchone() == (1, bytes.fromhex("11" * 32))
    expect_denied(journaldb, "UPDATE pilot_launcher_journal_anchor SET last_sequence=2,last_record_hash=? WHERE singleton_id=1", (bytes.fromhex("22" * 32),))
    with with_capability(journaldb, "AdvanceLauncherJournalAnchor"):
        expect_abort(journaldb, "UPDATE pilot_launcher_journal_anchor SET last_sequence=3,last_record_hash=?,updated_at=2 WHERE singleton_id=1", (bytes.fromhex("33" * 32),))
    tenant, principal, session = b"T1", b"P"*32, b"S"*32
    with with_capability(authdb, "PublishAuthorizationFence"):
        authdb.execute("INSERT INTO pilot_authorization_state VALUES(?,?,?, ?, 'student','active',1)",
                       (tenant, principal, b"1"*8, b"D"*32))
        authdb.execute("INSERT INTO pilot_session_fences VALUES(?,?,?,?,'active',100,1)",
                       (tenant, principal, session, b"1"*8))
    try:
        authdb.current_capability = "PublishAuthorizationFence"
    except AttributeError:
        pass
    else:
        raise AssertionError("test callers must not be able to forge an active capability by assigning connection state")
    expect_denied(authdb, "UPDATE pilot_authorization_state SET role='admin',authz_revision=?,updated_at=2 WHERE tenant_id=? AND principal_ref=?",
                  (b"2"*8, tenant, principal))
    expect_denied(authdb, "INSERT INTO pilot_authz_update_receipts(request_id,tenant_id,principal_ref,target_kind,target_key,revision,update_digest,committed_at) VALUES(?,?,?,'account',zeroblob(32),?,?,2)",
                  (b"X"*16,tenant,principal,b"2"*8,b"Y"*32))
    session_update = "UPDATE pilot_session_fences SET session_revision=?,session_state='revoked',updated_at=2 WHERE tenant_id=? AND principal_ref=? AND session_key_hash=?"
    with with_capability(authdb, "PublishAuthorizationFence"):
        authdb.execute(session_update, (b"2"*8, tenant, principal, session))
    expect_denied(authdb, session_update, (b"3"*8, tenant, principal, session))
    with with_capability(authdb, "PublishAuthorizationFence"):
        expect_abort(authdb, "UPDATE pilot_session_fences SET session_revision=?,session_state='active',updated_at=3 WHERE tenant_id=? AND principal_ref=? AND session_key_hash=?",
                     (b"3"*8, tenant, principal, session))
        expect_abort(authdb, "UPDATE pilot_authorization_state SET authz_revision=?,updated_at=3 WHERE tenant_id=? AND principal_ref=?",
                     (b"1"*8, tenant, principal))

    print("PASS: atomic init rollback/FK; canonical fixed-seed payload and final immutable marker ordering; INV-01..11 on no-root, normal-root-close commit boundaries, prepared-cancel, and acknowledged EEXIST paths; negative missing root/close receipt or finish, orphan-terminal, EEXIST readiness promotion, and lost-EEXIST-lease faults; alias rotation, monotonic launcher journal anchor, trigger-source authorizer denial, literal sqlite_ inventory filtering, capacity triggers, DDL/PRAGMA denial, REPLACE guards, and monotonic auth/session guards")


if __name__ == "__main__":
    main()

