import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from first_audio_matrix_control import MatrixProtocolError
from first_audio_latency_matrix import nearest_rank, run_matrix, summarize_attempts, _load_storage_profile


class FirstAudioLatencyMatrixTests(unittest.TestCase):
    def test_nearest_rank_uses_ceil_rank_and_rejects_empty(self):
        values = list(range(1, 31))
        self.assertEqual(15, nearest_rank(values, 50))
        self.assertEqual(29, nearest_rank(values, 95))
        self.assertEqual(30, nearest_rank(values, 99))
        self.assertIsNone(nearest_rank([], 95))
        self.assertIsNone(nearest_rank(values, 90))

    def test_gate_requires_full_denominator_and_both_frame_endpoints(self):
        passing = [{"status": "success", "show_word_ms": 1000.0, "first_frame_ms": 1500.0,
                    "audio_start_ms": 1510.0, "receipt_to_onstart_ms": 10.0} for _ in range(100)]
        summary = summarize_attempts(passing)
        self.assertTrue(summary["gate_passed"])
        self.assertEqual(100, summary["counts"]["success"])
        self.assertEqual(1000.0, summary["distributions"]["show_word_ms"]["p99"])

        failed_denominator = passing[:-1] + [{"status": "not_attempted"}]
        summary = summarize_attempts(failed_denominator)
        self.assertFalse(summary["gate_passed"])
        self.assertEqual(1, summary["counts"]["not_attempted"])

        slow_subtitle = [{**item, "show_word_ms": 2100.0} for item in passing]
        self.assertFalse(summarize_attempts(slow_subtitle)["gate_passed"])
        slow_frame = [{**item, "first_frame_ms": 5001.0} for item in passing]
        self.assertFalse(summarize_attempts(slow_frame)["gate_passed"])

    def test_timing_distributions_use_only_successes_but_keep_failure_counts(self):
        attempts = [
            {"status": "success", "show_word_ms": 100.0, "first_frame_ms": 200.0,
             "audio_start_ms": 210.0, "receipt_to_onstart_ms": 10.0},
            {"status": "timeout"}, {"status": "failed"}, {"status": "incomplete"},
            {"status": "not_attempted"},
        ]
        summary = summarize_attempts(attempts)
        self.assertEqual({"success": 1, "failed": 1, "timeout": 1, "incomplete": 1, "not_attempted": 1},
                         summary["counts"])
        self.assertEqual(1, summary["distributions"]["first_frame_ms"]["n"])
        self.assertFalse(summary["gate_passed"])

    def test_storage_profile_schema_rejects_nonfinite_or_path_data(self):
        valid = {"schema_version": 1,
                 "effective_profile": {"journal_mode": "delete", "synchronous": 2, "busy_timeout_ms": 5000},
                 "guarded_connections": 3, "runtime_pragma_attempts": 0,
                 "before_timed_work": {"database_bytes": 4096, "wal_bytes": 0},
                 "after_timed_work_before_close": {"database_bytes": 8192, "wal_bytes": 4096},
                 "after_close": {"database_bytes": 12288, "wal_bytes": 0}}
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "storage-profile.json"
            path.write_text(json.dumps(valid), encoding="utf-8")
            self.assertEqual(valid, _load_storage_profile(Path(tmp)))
            for invalid in (
                {**valid, "before_timed_work": {"database_bytes": True, "wal_bytes": 0}},
                {**valid, "runtime_pragma_attempts": 1},
                {**valid, "effective_profile": {**valid["effective_profile"], "synchronous": 9}},
                {**valid, "effective_profile": {**valid["effective_profile"], "journal_mode": "wal\nsecret"}},
                {**valid, "absolute_path": "C:/private/database.db"},
            ):
                path.write_text(json.dumps(invalid), encoding="utf-8")
                with self.assertRaises(MatrixProtocolError):
                    _load_storage_profile(Path(tmp))

    def test_cold_failure_keeps_denominator_and_does_not_launch_remaining_work(self):
        async def no_web_build(repository_root, node_binary, build_root):
            return "c" * 64, build_root

        async def fail_first(repository_root, run_id, go_binary, node_binary, **kwargs):
            self.assertTrue(run_id.endswith("_c01"))
            raise MatrixProtocolError("test-binary build deadline")

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / ".test-artifacts").mkdir()
            with patch("first_audio_latency_matrix.build_first_audio_web", no_web_build), \
                    patch("first_audio_latency_matrix.run_first_audio_reset_acceptance", fail_first):
                summary, result_path = __import__("asyncio").run(
                    run_matrix(root, Path("go"), Path("node"), "ColdFailure_20261031"))
            payload = json.loads(result_path.read_text(encoding="utf-8"))
        self.assertEqual("failed", summary["status"])
        self.assertEqual(30, len(payload["cold"]))
        self.assertEqual("timeout", payload["cold"][0]["status"])
        self.assertEqual(29, payload["cold_summary"]["counts"]["not_attempted"])
        self.assertEqual(100, len(payload["warm"]))
        self.assertEqual(100, payload["warm_summary"]["counts"]["not_attempted"])

    def test_matrix_rejects_journey_from_different_frozen_web_bundle(self):
        async def fake_web_build(repository_root, node_binary, build_root):
            return "c" * 64, build_root

        async def stale_bundle_journey(repository_root, run_id, go_binary, node_binary, **kwargs):
            kwargs["progress_callback"]({"event": "attempt_started", "journey": 1, "warmup": False})
            kwargs["progress_callback"]({"event": "journey_completed", "journey": 1, "warmup": False,
                "show_word_ms": 100.0, "first_frame_ms": 200.0, "audio_start_ms": 210.0,
                "receipt_to_onstart_ms": 10.0, "lesson_id": 1, "generation": 1,
                "source_build_identity": "a" * 64, "web_build_identity": "d" * 64,
                "websocket_closed": True})
            raise AssertionError("identity mismatch must abort the callback")

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / ".test-artifacts").mkdir()
            with patch("first_audio_latency_matrix.build_first_audio_web", fake_web_build), \
                    patch("first_audio_latency_matrix.run_first_audio_reset_acceptance", stale_bundle_journey):
                summary, result_path = __import__("asyncio").run(
                    run_matrix(root, Path("go"), Path("node"), "StaleBundle_20261031"))
            payload = json.loads(result_path.read_text(encoding="utf-8"))
        self.assertEqual("failed", summary["status"])
        self.assertEqual("failed", payload["cold"][0]["status"])
        self.assertEqual("first_audio_evidence_invalid", payload["cold"][0]["failure_code"])
        self.assertEqual(29, payload["cold_summary"]["counts"]["not_attempted"])

    def test_warm_shutdown_error_preserves_completed_samples_and_partial_incomplete(self):
        async def no_web_build(repository_root, node_binary, build_root):
            return "c" * 64, build_root

        async def fake_acceptance(repository_root, run_id, go_binary, node_binary, **kwargs):
            artifact_root = repository_root / ".test-artifacts"
            subrun_root = artifact_root / ("first-audio-matrix-" + run_id)
            subrun_root.mkdir(mode=0o700)

            def write_profile():
                profile = {"schema_version": 1,
                           "effective_profile": {"journal_mode": "delete", "synchronous": 2,
                                                  "busy_timeout_ms": 5000},
                           "guarded_connections": 3, "runtime_pragma_attempts": 0,
                           "before_timed_work": {"database_bytes": 1, "wal_bytes": 0},
                           "after_timed_work_before_close": {"database_bytes": 1, "wal_bytes": 0},
                           "after_close": {"database_bytes": 1, "wal_bytes": 0}}
                (subrun_root / "storage-profile.json").write_text(json.dumps(profile), encoding="utf-8")

            journey_count = kwargs["journey_count"]
            callback = kwargs["progress_callback"]
            completed = 0
            for journey in range(1, journey_count + 1):
                warmup = bool(kwargs.get("warmup_first") and journey == 1)
                callback({"event": "attempt_started", "journey": journey, "warmup": warmup})
                if run_id.endswith("_warm") and journey == 5:
                    raise MatrixProtocolError("child did not exit after shutdown")
                event = {"event": "journey_completed", "journey": journey, "warmup": warmup,
                         "show_word_ms": 100.0, "first_frame_ms": 200.0, "audio_start_ms": 210.0,
                         "receipt_to_onstart_ms": 10.0, "lesson_id": journey,
                         "generation": journey, "source_build_identity": "a" * 64,
                         "web_build_identity": "c" * 64,
                         "websocket_closed": True}
                callback(event)
                completed += 1
            write_profile()
            return {"reset_count": max(1, journey_count - 1), "journeys": [None] * completed}

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / ".test-artifacts").mkdir()
            with patch("first_audio_latency_matrix.build_first_audio_web", no_web_build), \
                    patch("first_audio_latency_matrix.run_first_audio_reset_acceptance", fake_acceptance):
                summary, result_path = __import__("asyncio").run(
                    run_matrix(root, Path("go"), Path("node"), "WarmFailure_20261031"))
            payload = json.loads(result_path.read_text(encoding="utf-8"))
        self.assertEqual("failed", summary["status"])
        self.assertEqual("failed", payload["warm_run_status"])
        self.assertEqual("success", payload["warmup_status"])
        self.assertEqual(["success"] * 3 + ["incomplete"] + ["not_attempted"] * 96,
                         [item["status"] for item in payload["warm"]])
        self.assertEqual(3, payload["warm_summary"]["distributions"]["first_frame_ms"]["n"])

    def test_final_cold_shutdown_failure_does_not_start_warm_run(self):
        async def no_web_build(repository_root, node_binary, build_root):
            return "c" * 64, build_root

        calls = []
        async def fake_acceptance(repository_root, run_id, go_binary, node_binary, **kwargs):
            calls.append(run_id)
            progress = kwargs["progress_callback"]
            progress({"event": "attempt_started", "journey": 1, "warmup": False})
            progress({"event": "journey_completed", "journey": 1, "warmup": False,
                      "show_word_ms": 100.0, "first_frame_ms": 200.0, "audio_start_ms": 210.0,
                      "receipt_to_onstart_ms": 10.0, "lesson_id": 1, "generation": 1,
                      "source_build_identity": "b" * 64, "web_build_identity": "c" * 64,
                      "websocket_closed": True})
            if run_id.endswith("_c30"):
                raise MatrixProtocolError("child did not exit after shutdown")
            subrun_root = repository_root / ".test-artifacts" / ("first-audio-matrix-" + run_id)
            subrun_root.mkdir(mode=0o700)
            profile = {"schema_version": 1,
                       "effective_profile": {"journal_mode": "delete", "synchronous": 2,
                                              "busy_timeout_ms": 5000},
                       "guarded_connections": 3, "runtime_pragma_attempts": 0,
                       "before_timed_work": {"database_bytes": 1, "wal_bytes": 0},
                       "after_timed_work_before_close": {"database_bytes": 1, "wal_bytes": 0},
                       "after_close": {"database_bytes": 1, "wal_bytes": 0}}
            (subrun_root / "storage-profile.json").write_text(json.dumps(profile), encoding="utf-8")
            return {"reset_count": 1, "journeys": [{}]}

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / ".test-artifacts").mkdir()
            with patch("first_audio_latency_matrix.build_first_audio_web", no_web_build), \
                    patch("first_audio_latency_matrix.run_first_audio_reset_acceptance", fake_acceptance):
                summary, result_path = __import__("asyncio").run(
                    run_matrix(root, Path("go"), Path("node"), "ColdCleanup_20261031"))
            payload = json.loads(result_path.read_text(encoding="utf-8"))
        self.assertEqual(30, len(calls))
        self.assertFalse(any(call.endswith("_warm") for call in calls))
        self.assertEqual("success", payload["cold"][-1]["status"])
        self.assertEqual(1, len(payload["cold_run_failures"]))
        self.assertEqual("not_attempted", payload["warm_run_status"])
        self.assertEqual("failed", summary["status"])


if __name__ == "__main__":
    unittest.main()
