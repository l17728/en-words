#!/usr/bin/env python3
"""Regression tests for deterministic, non-mutating Pilot schema generation."""
from __future__ import annotations

import hashlib
import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
GENERATOR_PATH = ROOT / "review/generate_pilot_v3_ddl_manifest.py"
SPEC = importlib.util.spec_from_file_location("pilot_schema_generator", GENERATOR_PATH)
assert SPEC and SPEC.loader
generator = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = generator
SPEC.loader.exec_module(generator)


class SchemaGenerationTests(unittest.TestCase):
    def build(self):
        snapshot = generator.capture_input_snapshot()
        return generator.build_outputs(generator.canonical_input_digest(snapshot), snapshot)

    def test_static_ddl_parse_derives_103_unique_explicit_objects(self) -> None:
        statements = generator.sql_statements(generator.DDL.read_text(encoding="utf-8"))
        objects = generator.derive_explicit_objects(statements)
        self.assertEqual(103, len(statements))
        self.assertEqual(103, len(objects))
        self.assertEqual(103, len({(item["type"], item["name"]) for item in objects}))
        self.assertEqual(objects, sorted(objects, key=lambda item: (item["type"], item["name"])))

    def test_statement_split_handles_multiple_statements_literals_comments_and_trigger_bodies(self) -> None:
        source = """CREATE TABLE t (v TEXT CHECK(v <> ';')); CREATE INDEX i ON t(v);
-- delimiter ; ignored
CREATE TRIGGER tr AFTER INSERT ON t BEGIN
  INSERT INTO t(v) VALUES('semi;colon');
  UPDATE t SET v='x' WHERE v='y';
END;
"""
        statements = generator.sql_statements(source)
        self.assertEqual(3, len(statements))
        self.assertEqual(["index", "table", "trigger"],
                         [item["type"] for item in generator.derive_explicit_objects(statements)])

    def test_normalization_preserves_literal_whitespace_and_escaped_quotes(self) -> None:
        one_space = generator.clean_sql("CREATE TABLE t(v TEXT CHECK(v='a b'))")
        two_spaces = generator.clean_sql("CREATE TABLE t(v TEXT CHECK(v='a  b'))")
        escaped = generator.clean_sql("CREATE TABLE t(v TEXT CHECK(v='a ''b'' c'))")
        self.assertNotEqual(hashlib.sha256(one_space.encode()).digest(), hashlib.sha256(two_spaces.encode()).digest())
        self.assertIn("'a ''b'' c'", escaped)
        self.assertEqual(
            generator.sql_statements("CREATE TABLE t(v TEXT /* comment */ CHECK(v='-- not comment'));")[0],
            generator.sql_statements("CREATE TABLE t(v TEXT CHECK(v='-- not comment'));")[0],
        )

    def test_static_inventory_does_not_execute_host_sqlite(self) -> None:
        with patch.object(generator.sqlite3, "connect", side_effect=AssertionError("must not execute SQLite")):
            manifest, inventory, execution_map = self.build()
        self.assertEqual(103, json.loads(inventory)["explicit_object_count"])
        self.assertEqual(48, json.loads(execution_map)["case_count"])
        self.assertIn(b"ddl_sql_sha256", manifest)
        self.assertNotIn(b"ddl_manifest_sha256", inventory)

    def test_digest_domains_are_distinct_and_inventory_binding_is_exact(self) -> None:
        manifest_bytes, inventory_bytes, _ = self.build()
        manifest = json.loads(manifest_bytes)
        inventory = json.loads(inventory_bytes)
        self.assertEqual(inventory["ddl_sql_sha256"], manifest["ddl_sql_sha256"])
        self.assertEqual(
            hashlib.sha256(inventory_bytes).hexdigest(),
            manifest["schema_inventory_draft"]["inventory_bytes_sha256"],
        )
        self.assertNotEqual(inventory["ddl_sql_sha256"], manifest["schema_inventory_draft"]["inventory_bytes_sha256"])
        self.assertNotIn("ddl_manifest_sha256", inventory)
        self.assertNotIn("review/pilot-control-ledger-v3-ddl-manifest-draft-20261021.json", manifest["source_hashes"])

    def test_duplicate_reserved_or_non_create_objects_fail(self) -> None:
        for statements in (
            ["CREATE TABLE safe_name (id INTEGER)", "CREATE TABLE safe_name (id INTEGER)"],
            ["CREATE TABLE sqlite_reserved (id INTEGER)"],
            ["PRAGMA user_version=3"],
        ):
            with self.subTest(statements=statements), self.assertRaises(ValueError):
                generator.derive_explicit_objects(statements)

    def test_execution_policy_binds_exact_fault_manifest_and_perf_target(self) -> None:
        fault_bytes = generator.SCHEMA_CASES.read_bytes()
        policy_bytes = generator.POLICY.read_bytes()
        result = generator.build_execution_map(fault_bytes, policy_bytes)
        self.assertEqual(48, result["case_count"])
        self.assertEqual(generator.file_sha(generator.SCHEMA_CASES), result["source_manifest_sha256"])
        self.assertEqual(generator.file_sha(generator.POLICY), result["execution_policy_sha256"])
        perf = next(row for row in result["cases"] if row["case_id"] == "PERF-01")
        self.assertEqual("DEFERRED_REFERENCE_HOST", perf["execution_class"])
        self.assertEqual("PERFORMANCE_REFERENCE_HOST", perf["harness_layer"])
        self.assertEqual("BLOCKED_NOT_IMPLEMENTED", perf["implementation_status"])
        policy = json.loads(policy_bytes)
        policy["source_fault_manifest_sha256"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "not bound"):
            generator.build_execution_map(fault_bytes, json.dumps(policy).encode())

    def test_stale_source_manifest_pin_fails_closed(self) -> None:
        stale_source = generator.ROOT / generator.SOURCES[0]
        snapshot = generator.capture_input_snapshot()
        relative = stale_source.relative_to(generator.ROOT).as_posix()
        snapshot[relative] += b" stale"
        with self.assertRaisesRegex(ValueError, "stale or missing source hash"):
            generator.verify_legacy_source_pins(snapshot)

    def test_duplicate_case_id_and_attempted_acceptance_are_rejected(self) -> None:
        fault_bytes = generator.SCHEMA_CASES.read_bytes()
        policy = generator.read_json(generator.POLICY)
        policy["cases"][1]["case_id"] = policy["cases"][0]["case_id"]
        with self.assertRaisesRegex(ValueError, "preserve the 48 fault ids"):
            generator.build_execution_map(fault_bytes, json.dumps(policy).encode())
        policy = generator.read_json(generator.POLICY)
        policy["cases"][0]["status"] = "PASS"
        with self.assertRaisesRegex(ValueError, "mark an unexecuted case accepted"):
            generator.build_execution_map(fault_bytes, json.dumps(policy).encode())

    def test_non_object_case_rows_fail_with_clear_validation_errors(self) -> None:
        fault = json.loads(generator.SCHEMA_CASES.read_text(encoding="utf-8"))
        fault["cases"][0] = None
        policy = generator.read_json(generator.POLICY)
        raw_fault = json.dumps(fault).encode()
        policy["source_fault_manifest_sha256"] = hashlib.sha256(raw_fault).hexdigest()
        with self.assertRaisesRegex(ValueError, "fault manifest case rows must be JSON objects"):
            generator.build_execution_map(raw_fault, json.dumps(policy).encode())

        policy["cases"][0] = None
        raw_fault = generator.SCHEMA_CASES.read_bytes()
        policy["source_fault_manifest_sha256"] = hashlib.sha256(raw_fault).hexdigest()
        with self.assertRaisesRegex(ValueError, "execution policy case rows must be JSON objects"):
            generator.build_execution_map(raw_fault, json.dumps(policy).encode())

    def test_empty_fields_and_non_perf_deferred_status_are_rejected(self) -> None:
        fault = generator.SCHEMA_CASES.read_bytes()
        original_policy = generator.read_json(generator.POLICY)
        for field, value in (("runner_command", ""), ("wrapper_invocation", ""),
                             ("execution_class", ""), ("aggregate_invocation", "")):
            policy = json.loads(json.dumps(original_policy))
            policy["cases"][0][field] = value
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, "empty or invalid execution field"):
                generator.build_execution_map(fault, json.dumps(policy).encode())
        policy = json.loads(json.dumps(original_policy))
        policy["cases"][0]["status"] = "DEFERRED_REFERENCE_HOST"
        with self.assertRaisesRegex(ValueError, "mark an unexecuted case accepted"):
            generator.build_execution_map(fault, json.dumps(policy).encode())
        policy = json.loads(json.dumps(original_policy))
        perf = next(case for case in policy["cases"] if case["case_id"] == "PERF-01")
        perf["harness_layer"] = ""
        with self.assertRaisesRegex(ValueError, "target and platform"):
            generator.build_execution_map(fault, json.dumps(policy).encode())

    def test_policy_digest_is_calculated_from_the_exact_bytes_parsed(self) -> None:
        fault = json.loads(generator.SCHEMA_CASES.read_text(encoding="utf-8"))
        fault["cases"][0]["action"] = "tampered action"
        raw_fault = json.dumps(fault, separators=(",", ":")).encode()
        policy = json.loads(generator.POLICY.read_text(encoding="utf-8"))
        policy["source_fault_manifest_sha256"] = hashlib.sha256(raw_fault).hexdigest()
        raw_policy = json.dumps(policy, separators=(",", ":")).encode()
        mapped = generator.build_execution_map(raw_fault, raw_policy)
        self.assertEqual(hashlib.sha256(raw_fault).hexdigest(), mapped["source_manifest_sha256"])
        self.assertEqual("tampered action", mapped["cases"][0]["action"])

    def test_outputs_are_computed_without_mutating_source_manifests(self) -> None:
        paths = (generator.SCHEMA_CASES, generator.PRODUCT_CASES, generator.POLICY)
        before = {path: hashlib.sha256(path.read_bytes()).hexdigest() for path in paths}
        self.build()
        after = {path: hashlib.sha256(path.read_bytes()).hexdigest() for path in paths}
        self.assertEqual(before, after)

    def test_input_pin_blocks_changed_canonical_source(self) -> None:
        snapshot = generator.capture_input_snapshot()
        approved = generator.canonical_input_digest(snapshot)
        changed_snapshot = dict(snapshot)
        changed_snapshot[generator.CANONICAL_INPUT_PATHS[0]] += b" changed after approval"
        with self.assertRaisesRegex(ValueError, "does not match the externally approved pin"):
            generator.build_outputs(approved, changed_snapshot)

    def test_output_generation_reads_each_input_once_into_the_approved_snapshot(self) -> None:
        snapshot = generator.capture_input_snapshot()
        approved = generator.canonical_input_digest(snapshot)
        with patch.object(Path, "read_bytes", side_effect=AssertionError("input reread after snapshot")):
            built = generator.build_outputs(approved, snapshot)
        self.assertEqual(3, len(built))

    def test_help_is_side_effect_free_and_requires_explicit_action(self) -> None:
        paths = (generator.SCHEMA_CASES, generator.PRODUCT_CASES, generator.POLICY,
                 generator.OUTPUT, generator.INVENTORY_OUTPUT, generator.MAP_OUTPUT)
        before = {path: hashlib.sha256(path.read_bytes()).hexdigest() for path in paths}
        help_result = subprocess.run(
            [sys.executable, str(GENERATOR_PATH), "--help"], cwd=ROOT,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        no_action = subprocess.run(
            [sys.executable, str(GENERATOR_PATH)], cwd=ROOT,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        check_result = subprocess.run(
            [sys.executable, str(GENERATOR_PATH), "--check", "--approved-inputs-sha256",
             generator.canonical_input_digest()], cwd=ROOT,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        wrong_pin = subprocess.run(
            [sys.executable, str(GENERATOR_PATH), "--check", "--approved-inputs-sha256", "0" * 64], cwd=ROOT,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        after = {path: hashlib.sha256(path.read_bytes()).hexdigest() for path in paths}
        self.assertEqual(0, help_result.returncode)
        self.assertNotEqual(0, no_action.returncode)
        self.assertEqual(0, check_result.returncode, check_result.stderr.decode("utf-8", errors="replace"))
        self.assertNotEqual(0, wrong_pin.returncode)
        self.assertEqual(before, after)

    def test_atomic_replace_replaces_complete_file(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "nested" / "output.json"
            target.parent.mkdir()
            target.write_bytes(b"old")
            generator.atomic_replace(target, b"new-complete-content")
            self.assertEqual(b"new-complete-content", target.read_bytes())
            self.assertEqual([target], list(target.parent.glob("output.json")))


if __name__ == "__main__":
    unittest.main()
