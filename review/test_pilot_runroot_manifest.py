from __future__ import annotations

import hashlib
import json
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
QUOTA = ROOT / "review/pilot-intent-quota-journal-manifest-20261022.json"
QUOTA_SPEC = ROOT / "docs/63-pilot-intent-quota-journal-sdd.md"
PRODUCT = ROOT / "review/pilot-runroot-product-orchestration-manifest-20261021.json"
SCHEMA = ROOT / "review/pilot-control-ledger-v3-schema-fault-manifest-20261021.json"
VECTORS = ROOT / "review/pilot-intent-quota-ipc-v1-vectors.json"


class ManifestTests(unittest.TestCase):
    def check_hashes(self, manifest: dict) -> None:
        for name, digest in manifest["source_hashes"].items():
            path = ROOT / name
            self.assertTrue(path.is_file(), name)
            self.assertEqual(digest, hashlib.sha256(path.read_bytes()).hexdigest(), name)

    def test_product_manifest_inventory_hashes_and_design_state(self) -> None:
        manifest = json.loads(PRODUCT.read_text(encoding="utf-8"))
        cases = {case["case_id"]: case for case in manifest["cases"]}
        self.assertEqual(117, len(cases))
        ids = [case["case_id"] for case in manifest["cases"]]
        self.assertEqual(len(ids), len(set(ids)))
        self.assertEqual({"BLOCKED_NOT_IMPLEMENTED"}, {c["status"] for c in cases.values()})
        self.check_hashes(manifest)

    def test_api26_separates_permit_and_issued_failure_side_effects(self) -> None:
        manifest = json.loads(PRODUCT.read_text(encoding="utf-8"))
        case = next(case for case in manifest["cases"] if case["case_id"] == "API-26")
        oracle = case["oracle"]
        self.assertEqual("api26.quota_journal_commit_boundary", case["named_barrier"])
        self.assertIn("Before an anchored PERMIT and one-use ticket", oracle)
        self.assertIn("no nonce reservation, encryption, or handle", oracle)
        self.assertIn("After ticket delivery", oracle)
        self.assertIn("never re-reserves or re-encrypts", oracle)
        self.assertIn("exact recovered anchored ISSUED may return only its stored ciphertext", oracle)
        self.assertIn("PERMIT-only is burned", oracle)
        self.assertIn("ambiguous boundary stays unavailable", oracle)
        self.assertIn("PERMIT journal bytes and its external anchor", oracle)
        self.assertEqual("BLOCKED_NOT_IMPLEMENTED", case["status"])

    def test_ui09_issued_uncertainty_never_leaks_or_retries_a_handle(self) -> None:
        manifest = json.loads(PRODUCT.read_text(encoding="utf-8"))
        case = next(case for case in manifest["cases"] if case["case_id"] == "UI-09")
        self.assertEqual("ui09.issued_anchor_uncertain_before_http_reply", case["named_barrier"])
        self.assertIn("one consumed nonce and at most one encryption", case["oracle"])
        self.assertIn("no handle response", case["oracle"])
        self.assertIn("no run POST, polling retry, or automatic second activation", case["oracle"])
        self.assertIn("one new explicit Start", case["oracle"])
        self.assertIn("does not substitute for the API-26 coordinator fault oracle", case["oracle"])
        self.assertIn("request ID and consumes an AEAD nonce both distinct", case["oracle"])
        self.assertIn("IndexedDB, or CacheStorage", case["oracle"])
        self.assertIn("console/error events, response bodies, and captured browser/network diagnostics", case["oracle"])
        self.assertIn("reports only match counts and never prints matched values", case["oracle"])
        self.assertEqual("BLOCKED_NOT_IMPLEMENTED", case["status"])

    def test_schema_manifest_hashes_and_design_state(self) -> None:
        manifest = json.loads(SCHEMA.read_text(encoding="utf-8"))
        case_ids = [case["case_id"] for case in manifest["cases"]]
        self.assertEqual(48, len(case_ids))
        self.assertEqual(len(case_ids), len(set(case_ids)))
        self.assertEqual({"BLOCKED_NOT_IMPLEMENTED"}, {c["status"] for c in manifest["cases"]})
        self.check_hashes(manifest)


class QuotaJournalManifestTests(unittest.TestCase):
    def check_hashes(self, manifest: dict) -> None:
        for name, digest in manifest["source_hashes"].items():
            path = ROOT / name
            self.assertTrue(path.is_file(), name)
            self.assertEqual(digest, hashlib.sha256(path.read_bytes()).hexdigest(), name)

    @classmethod
    def setUpClass(cls) -> None:
        cls.manifest = json.loads(QUOTA.read_text(encoding="utf-8"))
        cls.spec = QUOTA_SPEC.read_text(encoding="utf-8")
        cls.cases = {case["case_id"]: case for case in cls.manifest["cases"]}

    def test_sources_hash_and_case_inventory(self) -> None:
        self.assertEqual({f"QJ-{i:02d}" for i in range(26)}, set(self.cases))
        self.assertEqual("DESIGN_ONLY_NOT_IMPLEMENTED", self.manifest["acceptance_state"])
        self.assertEqual({"BLOCKED_NOT_IMPLEMENTED"}, {c["status"] for c in self.cases.values()})
        barriers = [c["named_barrier"] for c in self.manifest["cases"]]
        self.assertEqual(len(barriers), len(set(barriers)))
        for case in self.manifest["cases"]:
            for field in ("actor", "initial_state", "named_barrier", "action", "oracle"):
                self.assertTrue(case.get(field), f"{case['case_id']} missing {field}")
        self.check_hashes(self.manifest)

    def test_ipc_v1_golden_frames_are_complete_and_exact(self) -> None:
        vectors = json.loads(VECTORS.read_text(encoding="utf-8"))["vectors"]
        expected = {"reserve_request": 1, "reserve_response": 2, "commit_request": 3, "commit_response": 4, "settlement_lookup_request": 5, "settlement_lookup_response": 6, "launcher_turn": 7}
        self.assertEqual(set(expected), set(vectors))
        for name, message_type in expected.items():
            with self.subTest(vector=name):
                vector = vectors[name]
                frame = bytes.fromhex(vector["frame_hex"])
                payload = bytes.fromhex(vector["payload_hex"])
                self.assertEqual(message_type, vector["message_type"])
                self.assertEqual(vector["payload_length"], len(payload))
                self.assertEqual(vector["frame_length"], len(frame))
                self.assertEqual(b"ENWQIPC1", frame[:8])
                self.assertEqual(message_type, frame[10])
                self.assertEqual(len(payload), int.from_bytes(frame[12:16], "big"))
                self.assertEqual(payload, frame[16:])

    def test_ipc_peer_identity_resource_caps_and_recovery_are_locked(self) -> None:
        for term in ("dedicated launcher uid", "systemd", "scm_credentials", "same-uid helper", "127 accepted sockets", "64 queued packets", "16 dispatch workers", "operation id is persisted", "durable_uncertain", "settlementlookup", "launcherturn", "no principal or append lock", "type 7", "turn state"):
            self.assertIn(term, self.spec.lower())
        qj22_action = self.cases["QJ-22"]["action"].lower()
        self.assertIn("unregistered", qj22_action)
        self.assertIn("same-uid", qj22_action)
        self.assertIn("ALREADY_ACKNOWLEDGED", self.cases["QJ-25"]["oracle"])
        self.assertIn("qj20.issued_transaction_arbitration", self.cases["QJ-20"]["named_subbarriers"])

    def test_every_case_has_explicit_sensitive_log_oracle(self) -> None:
        for case in self.manifest["cases"]:
            with self.subTest(case=case["case_id"]):
                self.assertIn("log", case["oracle"].lower())
                self.assertIn("forbidden-field", case["oracle"].lower())

    def test_watchdog_and_boundaries_are_explicit(self) -> None:
        for phrase in ("fixed 10-second `CLOCK_BOOTTIME` durability deadline", "PERMIT, ISSUE, BOUND, and EXPIRED", "DURABLE_PENDING", "DURABLE_ACKABLE", "DURABLE_UNCERTAIN", "No other transitions are legal"):
            self.assertIn(phrase, self.spec)
        self.assertIn("qj20.exact_deadline_uncertain", self.cases["QJ-20"]["named_subbarriers"])
        self.assertIn("qj25.restart_anchored_issue", self.cases["QJ-25"]["named_subbarriers"])

    def test_qj10_fault_matrix_covers_all_record_kinds(self) -> None:
        names = self.cases["QJ-10"]["named_subbarriers"]
        self.assertEqual(40, len(names))
        for kind in ("permit", "issued", "bound", "expired"):
            self.assertTrue(any(n.startswith(f"qj10.{kind}.") for n in names))

    def test_denial_and_recovery_cases_scan_sensitive_logs(self) -> None:
        for case_id in ("QJ-00", "QJ-01", "QJ-04", "QJ-06", "QJ-15", "QJ-17", "QJ-18", "QJ-19", "QJ-20", "QJ-21", "QJ-23", "QJ-24", "QJ-25"):
            self.assertIn("common forbidden-field", self.cases[case_id]["oracle"], case_id)

    def test_qj03_barrier_and_qj06_fake_clock_contract(self) -> None:
        self.assertEqual("qj03.before_second_process_lock_attempt", self.cases["QJ-03"]["named_barrier"])
        self.assertIn("fake CLOCK_BOOTTIME", self.cases["QJ-06"]["action"])
        self.assertIn("native CLOCK_BOOTTIME", self.cases["QJ-06"]["action"])

    def test_receipt_canonical_vector_and_anchor_contract(self) -> None:
        for phrase in ("complete canonical receipt CBOR byte vector", "exactly nine definite-length elements", "CompareAndAdvance(expected,next)", "ADVANCED", "AMBIGUOUS"):
            self.assertIn(phrase, self.spec)
        self.assertIn("qj24.canonical_vector", self.cases["QJ-24"]["named_subbarriers"])
        self.assertIn("qj23.cas_after_sync_before_reply", self.cases["QJ-23"]["named_subbarriers"])


if __name__ == "__main__":
    unittest.main()
