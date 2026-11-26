#!/usr/bin/env python3
"""Generate and validate the isolated Pilot v3 DDL inventory and case map.

This tool is intentionally read-only unless --write is supplied. It never edits
its source manifests. Output files are replaced atomically one at a time, with
the DDL manifest written last as the bundle commit marker.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sqlite3
import tempfile
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
DDL = ROOT / "review/pilot-control-ledger-v3-literal-ddl-draft-20261021.sql"
INVARIANTS = ROOT / "review/pilot-control-ledger-v3-invariant-queries-draft-20261021.sql"
SEEDS = ROOT / "review/pilot-control-ledger-v3-bootstrap-seed-draft-20261021.sql"
INIT_SEEDS = ROOT / "review/pilot-control-ledger-v3-initialization-seeds-draft-20261022.json"
SCHEMA_CASES = ROOT / "review/pilot-control-ledger-v3-schema-fault-manifest-20261021.json"
PRODUCT_CASES = ROOT / "review/pilot-runroot-product-orchestration-manifest-20261021.json"
VECTORS = ROOT / "review/pilot-control-ledger-v3-authz-vectors-20261021.json"
CAPABILITIES = ROOT / "review/pilot-control-ledger-v3-write-capabilities-20261021.json"
POLICY = ROOT / "review/pilot-control-ledger-v3-schema-execution-policy-draft-20261022.json"
OUTPUT = ROOT / "review/pilot-control-ledger-v3-ddl-manifest-draft-20261021.json"
INVENTORY_OUTPUT = ROOT / "review/pilot-control-ledger-v3-schema-inventory-draft-20261022.json"
MAP_OUTPUT = ROOT / "review/pilot-control-ledger-v3-schema-execution-map-draft-20261022.json"
GENERATOR = Path(__file__).resolve()

SOURCES = [
    "docs/50-pilot-runroot-product-orchestration-sdd.md",
    "docs/51-pilot-control-ledger-v3-sdd.md",
    "docs/52-pilot-control-ledger-v3-physical-model-decisions.md",
    "docs/53-pilot-control-ledger-v3-physical-schema-matrix.md",
    "docs/54-pilot-runroot-stage-a-implementation-delta.md",
]
INIT_SOURCES = [
    "docs/62-pilot-vfs-initialization-and-recovery-amendment.md",
    "docs/63-pilot-intent-quota-journal-sdd.md",
]
SEED_REVIEW = "review/pilot-control-ledger-v3-initialization-seeds-review-20261022.md"
CANONICAL_INPUT_PATHS = sorted(set(
    SOURCES + INIT_SOURCES + [
        "docs/65-pilot-control-ledger-schema-unification-and-test-harness-sdd.md",
        SEED_REVIEW,
        DDL.relative_to(ROOT).as_posix(), INVARIANTS.relative_to(ROOT).as_posix(),
        SEEDS.relative_to(ROOT).as_posix(), INIT_SEEDS.relative_to(ROOT).as_posix(),
        SCHEMA_CASES.relative_to(ROOT).as_posix(), PRODUCT_CASES.relative_to(ROOT).as_posix(),
        VECTORS.relative_to(ROOT).as_posix(), CAPABILITIES.relative_to(ROOT).as_posix(),
        POLICY.relative_to(ROOT).as_posix(),
    ]
))
OUTPUT_SOURCE_PATHS = sorted(set(CANONICAL_INPUT_PATHS + [
    "review/test_pilot_v3_ddl_draft.py", "review/test_pilot_v3_schema_generation.py",
    GENERATOR.relative_to(ROOT).as_posix(),
]))


def sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def file_sha(path: Path) -> str:
    return sha(path.read_bytes())


def capture_input_snapshot() -> dict[str, bytes]:
    """Read every output dependency once so the approved pin covers built bytes."""
    return {name: (ROOT / name).read_bytes() for name in OUTPUT_SOURCE_PATHS}


def canonical_input_digest(snapshot: dict[str, bytes] | None = None) -> str:
    captured = snapshot if snapshot is not None else capture_input_snapshot()
    entries = [{"path": name, "sha256": sha(captured[name])} for name in CANONICAL_INPUT_PATHS]
    canonical = json.dumps(entries, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return sha(canonical)


def clean_sql(raw: str) -> str:
    output: list[str] = []
    quote: str | None = None
    bracket = False
    pending_space = False
    i = 0
    while i < len(raw):
        char = raw[i]
        nxt = raw[i + 1] if i + 1 < len(raw) else ""
        if quote is not None:
            output.append(char)
            if char == quote:
                if nxt == quote:
                    output.append(nxt)
                    i += 2
                    continue
                quote = None
            i += 1
            continue
        if bracket:
            output.append(char)
            if char == "]":
                if nxt == "]":
                    output.append(nxt)
                    i += 2
                    continue
                bracket = False
            i += 1
            continue
        if char.isspace():
            pending_space = True
            i += 1
            continue
        if pending_space and output:
            output.append(" ")
        pending_space = False
        output.append(char)
        if char in ("'", '"', "`"):
            quote = char
        elif char == "[":
            bracket = True
        i += 1
    return "".join(output).strip().rstrip(";").strip()


def _scan_sql(source: str) -> tuple[list[str], str, bool]:
    """Lex SQL statement boundaries without executing or versioning SQLite."""
    result: list[str] = []
    current: list[str] = []
    quote: str | None = None
    bracket = False
    line_comment = False
    block_comment = False
    i = 0
    while i < len(source):
        char = source[i]
        nxt = source[i + 1] if i + 1 < len(source) else ""
        if line_comment:
            if char in "\r\n":
                line_comment = False
                current.append(" ")
            i += 1
            continue
        if block_comment:
            if char == "*" and nxt == "/":
                block_comment = False
                current.append(" ")
                i += 2
            else:
                i += 1
            continue
        if quote is not None:
            current.append(char)
            if char == quote:
                if nxt == quote:
                    current.append(nxt)
                    i += 2
                    continue
                quote = None
            i += 1
            continue
        if bracket:
            current.append(char)
            if char == "]":
                if nxt == "]":
                    current.append(nxt)
                    i += 2
                    continue
                bracket = False
            i += 1
            continue
        if char == "-" and nxt == "-":
            line_comment = True
            i += 2
            continue
        if char == "/" and nxt == "*":
            block_comment = True
            i += 2
            continue
        if char in ("'", '"', "`"):
            quote = char
            current.append(char)
            i += 1
            continue
        if char == "[":
            bracket = True
            current.append(char)
            i += 1
            continue
        if char == ";":
            candidate = "".join(current) + ";"
            # SQLite's documented completeness probe is lexical only. It is
            # used to keep trigger bodies intact; no SQL is executed and no
            # host catalog or runtime schema behavior is consulted.
            if sqlite3.complete_statement(candidate):
                statement = clean_sql("".join(current))
                if statement:
                    result.append(statement)
                current.clear()
            else:
                current.append(char)
            i += 1
            continue
        current.append(char)
        i += 1
    return result, "".join(current), quote is not None or bracket or block_comment


def sql_statements(source: str) -> list[str]:
    """Split a SQL script lexically; never execute or introspect SQLite."""
    result, remainder, unterminated = _scan_sql(source)
    if unterminated or clean_sql(remainder):
        raise ValueError("incomplete SQL statement at end of canonical input")
    return result


def derive_explicit_objects(statements: list[str]) -> list[dict[str, str]]:
    """Derive explicit inventory rows from CREATE statements, without SQLite execution."""
    objects: list[dict[str, str]] = []
    seen: set[tuple[str, str]] = set()
    table_names: set[str] = set()
    for statement in statements:
        match = re.match(
            r"CREATE\s+(?:UNIQUE\s+)?(TABLE|INDEX|TRIGGER)\s+([A-Za-z0-9_]+)\b",
            statement,
            re.I,
        )
        if not match:
            raise ValueError("canonical DDL contains a non-literal or unsupported CREATE statement")
        kind, name = match.group(1).lower(), match.group(2)
        key = (kind, name.casefold())
        if key in seen or name.casefold().startswith("sqlite_"):
            raise ValueError(f"duplicate or reserved explicit object: {kind}/{name}")
        seen.add(key)
        if kind == "table":
            table_name = name
            table_names.add(name.casefold())
        else:
            target = re.search(r"\bON\s+([A-Za-z0-9_]+)\b", statement, re.I)
            if not target:
                raise ValueError(f"cannot identify target table for {kind}/{name}")
            table_name = target.group(1)
        objects.append({
            "type": kind,
            "name": name,
            "table_name": table_name,
            "normalized_sql": statement,
        })
    for item in objects:
        if item["type"] in {"index", "trigger"} and item["table_name"].casefold() not in table_names:
            raise ValueError(f"{item['type']} targets an undeclared table: {item['name']}")
    if len(objects) != len(statements):
        raise ValueError("explicit object count differs from canonical CREATE statement count")
    return sorted(objects, key=lambda item: (item["type"], item["name"]))


def named_invariants(source: str) -> list[dict[str, str]]:
    output: list[dict[str, str]] = []
    current_id = current_name = None
    pending = ""
    for line in source.splitlines(keepends=True):
        marker = re.match(r"\s*--\s*(INV-\d+)\s+(.+?)\s*$", line)
        if marker:
            if current_id or clean_sql(pending):
                raise ValueError("previous named invariant is incomplete")
            current_id, current_name = marker.groups()
            pending = ""
            continue
        if line.lstrip().startswith("--"):
            continue
        pending += line
        complete, remainder, unterminated = _scan_sql(pending)
        if unterminated:
            raise ValueError("unterminated quoted or commented invariant query")
        if complete:
            if len(complete) != 1 or clean_sql(remainder):
                raise ValueError("a named invariant must contain exactly one SQL statement")
            query = complete[0]
            output.append({
                "invariant_id": current_id,
                "name": current_name or "",
                "sha256": sha(query.encode("utf-8")),
                "query": query,
                "execution_scope": "startup_or_offline_reconciliation; not per-transaction hot path",
                "oracle": "zero rows or a single violation_count=0 row",
            })
            current_id = current_name = None
            pending = ""
    if current_id or clean_sql(pending):
        raise ValueError("incomplete named invariant query")
    if len({item["invariant_id"] for item in output}) != len(output):
        raise ValueError("duplicate invariant id")
    return output


def read_json_bytes(path: Path, data: bytes) -> dict[str, Any]:
    value = json.loads(data)
    if not isinstance(value, dict):
        raise ValueError(f"expected JSON object: {path.relative_to(ROOT).as_posix()}")
    return value


def read_json(path: Path) -> dict[str, Any]:
    """Convenience reader for tests; production output generation uses snapshots."""
    return read_json_bytes(path, path.read_bytes())


def verify_legacy_source_pins(snapshot: dict[str, bytes]) -> tuple[dict[str, Any], dict[str, Any]]:
    """Fail closed on stale source pins; never repair input manifests in place."""
    schema = read_json_bytes(SCHEMA_CASES, snapshot[SCHEMA_CASES.relative_to(ROOT).as_posix()])
    product = read_json_bytes(PRODUCT_CASES, snapshot[PRODUCT_CASES.relative_to(ROOT).as_posix()])
    expected_sets = ((schema, SOURCES + INIT_SOURCES), (product, SOURCES))
    for manifest, paths in expected_sets:
        pins = manifest.get("source_hashes", {})
        for relative in paths:
            expected = pins.get(relative)
            actual = sha(snapshot[relative])
            if not expected or expected != actual:
                raise ValueError(f"stale or missing source hash in case manifest: {relative}")
    return schema, product


def build_execution_map(fault_bytes: bytes, policy_bytes: bytes) -> dict[str, Any]:
    fault = json.loads(fault_bytes)
    policy = json.loads(policy_bytes)
    fault_hash = sha(fault_bytes)
    policy_hash = sha(policy_bytes)
    if not isinstance(fault, dict) or not isinstance(policy, dict):
        raise ValueError("fault manifest and execution policy must be JSON objects")
    if policy.get("manifest_version") != 1 or policy.get("status") != "DRAFT_CANONICAL_EXECUTION_POLICY_NOT_EVIDENCE":
        raise ValueError("execution policy header is invalid")
    if policy.get("source_fault_manifest") != SCHEMA_CASES.relative_to(ROOT).as_posix():
        raise ValueError("execution policy points to an unexpected fault manifest")
    if policy.get("source_fault_manifest_sha256") != fault_hash:
        raise ValueError("execution policy is not bound to the exact fault manifest")
    source_cases = fault.get("cases")
    policy_cases = policy.get("cases")
    if not isinstance(source_cases, list) or not isinstance(policy_cases, list):
        raise ValueError("fault manifest and execution policy must contain case arrays")
    if any(not isinstance(case, dict) for case in source_cases):
        raise ValueError("fault manifest case rows must be JSON objects")
    if any(not isinstance(case, dict) for case in policy_cases):
        raise ValueError("execution policy case rows must be JSON objects")
    source_ids = [case.get("case_id") for case in source_cases]
    policy_ids = [case.get("case_id") for case in policy_cases]
    if len(source_ids) != 48 or len(set(source_ids)) != 48:
        raise ValueError("fault manifest must contain 48 unique case ids")
    if policy_ids != source_ids or len(set(policy_ids)) != 48:
        raise ValueError("execution policy must preserve the 48 fault ids in source order")
    result: list[dict[str, Any]] = []
    test_names: set[str] = set()
    result_paths: set[str] = set()
    for source, rule in zip(source_cases, policy_cases):
        required = {
            "case_id", "status", "implementation_status", "test_name", "runner_command",
            "result_path", "execution_class", "prerequisites", "wrapper_invocation",
            "aggregate_invocation",
        }
        optional = {"harness_layer", "platform_requirement"}
        if not required.issubset(rule) or set(rule) - required - optional:
            raise ValueError(f"unexpected or missing policy fields for {rule['case_id']}")
        if rule["status"] != "BLOCKED_NOT_IMPLEMENTED":
            raise ValueError(f"policy attempts to mark an unexecuted case accepted: {rule['case_id']}")
        if rule["implementation_status"] != "BLOCKED_NOT_IMPLEMENTED":
            raise ValueError(f"unimplemented case has invalid implementation status: {rule['case_id']}")
        if not isinstance(rule["prerequisites"], list) or not rule["prerequisites"]:
            raise ValueError(f"missing explicit prerequisites: {rule['case_id']}")
        string_fields = ("test_name", "runner_command", "result_path", "execution_class",
                         "wrapper_invocation", "aggregate_invocation")
        if any(not isinstance(rule[name], str) or not rule[name].strip() for name in string_fields):
            raise ValueError(f"empty or invalid execution field for {rule['case_id']}")
        if any(not isinstance(item, str) or not item.strip() for item in rule["prerequisites"]):
            raise ValueError(f"invalid prerequisite for {rule['case_id']}")
        if rule["test_name"] in test_names or rule["result_path"] in result_paths:
            raise ValueError("duplicate Go test name or result path")
        test_names.add(rule["test_name"])
        result_paths.add(rule["result_path"])
        expected_test = ("TestPilotSchemaPerf/PERF-01" if rule["case_id"] == "PERF-01"
                         else f"TestPilotSchemaFaultManifest/{rule['case_id']}")
        if rule["test_name"] != expected_test:
            raise ValueError(f"unexpected Go test binding: {rule['case_id']}")
        if rule["result_path"] != f"review/artifacts/pilot-schema-gate/<gate_id>/<run_id>/{rule['case_id']}.json":
            raise ValueError(f"invalid result path for {rule['case_id']}")
        if rule["case_id"] == "PERF-01":
            if rule["execution_class"] != "DEFERRED_REFERENCE_HOST":
                raise ValueError("PERF-01 must remain deferred on the ordinary matrix")
            if not {"harness_layer", "platform_requirement"}.issubset(rule):
                raise ValueError("PERF-01 must declare its reference-host layer and platform requirement")
            if any(not isinstance(rule[name], str) or not rule[name].strip()
                   for name in ("harness_layer", "platform_requirement")):
                raise ValueError("PERF-01 target and platform requirement must be nonempty")
        elif "harness_layer" in rule or "platform_requirement" in rule:
            raise ValueError(f"reference-host fields are reserved for PERF-01: {rule['case_id']}")
        elif rule["execution_class"] == "DEFERRED_REFERENCE_HOST":
            raise ValueError(f"only PERF-01 may be deferred from the ordinary matrix: {rule['case_id']}")
        merged = dict(source)
        merged.update(rule)
        result.append(merged)
    return {
        "manifest_version": 1,
        "status": "DRAFT_TRACEABILITY_ONLY_NOT_EXECUTABLE",
        "source_manifest": SCHEMA_CASES.relative_to(ROOT).as_posix(),
        "source_manifest_sha256": fault_hash,
        "execution_policy": POLICY.relative_to(ROOT).as_posix(),
        "execution_policy_sha256": policy_hash,
        "case_count": 48,
        "cases": result,
    }


def build_outputs(approved_input_sha256: str, snapshot: dict[str, bytes] | None = None) -> tuple[bytes, bytes, bytes]:
    if not re.fullmatch(r"[0-9a-f]{64}", approved_input_sha256):
        raise ValueError("approved input digest must be 64 lowercase hexadecimal characters")
    captured = snapshot if snapshot is not None else capture_input_snapshot()
    missing = set(OUTPUT_SOURCE_PATHS) - set(captured)
    if missing:
        raise ValueError("input snapshot missing paths: " + ", ".join(sorted(missing)))
    actual_input_digest = canonical_input_digest(captured)
    if actual_input_digest != approved_input_sha256:
        raise ValueError("canonical input digest does not match the externally approved pin")
    schema_cases, product_cases = verify_legacy_source_pins(captured)
    ddl_text = captured[DDL.relative_to(ROOT).as_posix()].decode("utf-8")
    statements = sql_statements(ddl_text)
    objects = derive_explicit_objects(statements)
    statement_records = []
    for ordinal, statement in enumerate(statements, start=1):
        match = re.match(r"CREATE\s+(?:UNIQUE\s+)?(TABLE|INDEX|TRIGGER)\s+([A-Za-z0-9_]+)", statement, re.I)
        assert match is not None
        statement_records.append({
            "ordinal": ordinal,
            "object_type": match.group(1).lower(),
            "object_name": match.group(2),
            "normalized_sql": statement,
            "sha256": sha(statement.encode("utf-8")),
        })
    ddl_digest = sha("\n".join(item["normalized_sql"] for item in statement_records).encode("utf-8"))
    inventory = {
        "schema_version": 3,
        "ddl_sql_sha256": ddl_digest,
        "explicit_object_count": len(objects),
        "objects": [{
            "type": item["type"], "name": item["name"], "table_name": item["table_name"],
            "sql_sha256": sha(item["normalized_sql"].encode("utf-8")),
        } for item in objects],
    }
    inventory_bytes = (json.dumps(inventory, ensure_ascii=False, indent=2) + "\n").encode("utf-8")
    inventory_hash = sha(inventory_bytes)
    init_seed = read_json_bytes(INIT_SEEDS, captured[INIT_SEEDS.relative_to(ROOT).as_posix()])
    capabilities = read_json_bytes(CAPABILITIES, captured[CAPABILITIES.relative_to(ROOT).as_posix()])
    source_hashes = {p: sha(captured[p]) for p in OUTPUT_SOURCE_PATHS}
    manifest = {
        "manifest_version": 1,
        "feature": "Pilot Control Ledger v3 literal SQLite DDL draft",
        "status": "DRAFT_FOR_DOMAIN_REVIEW_NOT_IMPLEMENTATION_AUTHORITY",
        "sqlite_minimum": "3.37.0",
        "source_hashes": source_hashes,
        "ddl_statement_count": len(statement_records),
        "ddl_sql_sha256": ddl_digest,
        "ddl_statements": statement_records,
        "bootstrap_seed_sql": {
            "path": SEEDS.relative_to(ROOT).as_posix(), "sha256": sha(captured[SEEDS.relative_to(ROOT).as_posix()]),
            "policy_parameters": {"production": [1000000, 100000, 1000], "fixture": [7, 5, 3]},
            "atomic_with_ddl": True,
            "initialization_marker": {
                "table": "pilot_initialization_state", "must_be_last_dml": True,
                "inventory_bytes_sha256": inventory_hash,
                "seed_manifest": {
                    "path": INIT_SEEDS.relative_to(ROOT).as_posix(),
                    "sha256": sha(captured[INIT_SEEDS.relative_to(ROOT).as_posix()]), "seed_count": len(init_seed["seeds"]),
                },
            },
        },
        "schema_inventory_draft": {
            "path": INVENTORY_OUTPUT.relative_to(ROOT).as_posix(),
            "inventory_bytes_sha256": inventory_hash, "ddl_sql_sha256": ddl_digest,
            "explicit_object_count": len(objects),
        },
        "accepted_explicit_objects": objects,
        "accepted_implicit_objects": [
            "SQLite autoindexes derived from exact DDL; verified through index_list/index_xinfo by pinned Go/CGO harness"
        ],
        "connection_authorizer_policy": {
            "driver_binding": capabilities["sqlite_binding_candidate"],
            "required_on": "every resident and pooled SQLite connection before exposure to RPC handlers",
            "deny": ["ATTACH", "DETACH", "TEMP schema objects", "schema DDL after initialization", "ANALYZE/REINDEX after initialization", "mutating PRAGMAs", "all unscoped DML on protected tables"],
            "method_capability_manifest": {
                "path": CAPABILITIES.relative_to(ROOT).as_posix(), "sha256": sha(captured[CAPABILITIES.relative_to(ROOT).as_posix()]),
            },
            "principle": "unexported method-scoped capabilities are acquired only after typed authentication and signature verification; capability always clears; no raw-SQL API or cross-capability statement cache exists",
        },
        "rejected_objects": ["all TEMP objects", "views", "virtual tables", "unlisted tables", "unlisted indexes", "unlisted triggers", "all extra sqlite_master objects except SQLite-owned internal objects"],
        "named_invariants": named_invariants(captured[INVARIANTS.relative_to(ROOT).as_posix()].decode("utf-8")),
        "schema_fault_case_ids": [case["case_id"] for case in schema_cases["cases"]],
        "product_orchestration_case_ids": [case["case_id"] for case in product_cases["cases"]],
        "known_review_gates": [
            "Resolve the 103-object candidate against docs/51-54 before updating compiled artifacts.",
            "Verify named invariants against positive and corrupt fixtures.",
            "Validate production authorizer behavior on every pooled connection before admission.",
            "Do not treat this draft generator or its output as an accepted production schema.",
        ],
    }
    fault_bytes = captured[SCHEMA_CASES.relative_to(ROOT).as_posix()]
    policy_bytes = captured[POLICY.relative_to(ROOT).as_posix()]
    map_value = build_execution_map(fault_bytes, policy_bytes)
    encode = lambda value: (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode("utf-8")
    return encode(manifest), inventory_bytes, encode(map_value)


def atomic_replace(path: Path, data: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", suffix=".tmp", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--check", action="store_true", help="validate current generated outputs without writing")
    action.add_argument("--write", action="store_true", help="atomically replace generated outputs")
    parser.add_argument("--approved-inputs-sha256", required=True,
                        help="out-of-worktree approved canonical-input digest; required for both actions")
    args = parser.parse_args()
    try:
        manifest_bytes, inventory_bytes, map_bytes = build_outputs(args.approved_inputs_sha256)
        outputs = ((INVENTORY_OUTPUT, inventory_bytes), (MAP_OUTPUT, map_bytes), (OUTPUT, manifest_bytes))
        if args.check:
            drift = [path.relative_to(ROOT).as_posix() for path, expected in outputs
                     if not path.exists() or path.read_bytes() != expected]
            if drift:
                raise ValueError("generated output drift: " + ", ".join(drift))
            print("PASS: generated outputs match canonical inputs")
            return 0
        for path, data in outputs:
            atomic_replace(path, data)
        print(f"PASS: atomically replaced {len(outputs)} outputs; manifest written last as commit marker")
        return 0
    except (OSError, ValueError, KeyError, TypeError, json.JSONDecodeError) as exc:
        print(f"FAIL: {type(exc).__name__}: {exc}")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
