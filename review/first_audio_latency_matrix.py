"""Private 30-cold/100-warm synthetic first-audio matrix runner."""
from __future__ import annotations

import argparse
import asyncio
import json
import math
import os
import re
import sys
import uuid
from pathlib import Path
from typing import Any

from first_audio_matrix_control import (
    MatrixProtocolError,
    _create_private_directory,
    _safe_failure_code,
    _safe_failure_stage,
    build_first_audio_web,
    run_first_audio_reset_acceptance,
)

METRICS = ("show_word_ms", "first_frame_ms", "audio_start_ms", "receipt_to_onstart_ms")
JOURNEYS = 30
WARM_SAMPLES = 100
TIMEOUT_CODES = {
    "playwright_journey_deadline", "test_binary_build_deadline", "control_response_deadline_or_limit",
}
INCOMPLETE_CODES = {
    "child_did_not_exit_after_shutdown", "failed_shutdown_child_alive",
    "playwright_process_tree_alive", "playwright_child_processes_alive",
    "playwright_child_process_groups_alive", "posix_process_groups_alive",
    "web_production_build_deadline",
}


def nearest_rank(values: list[int | float], percentile: int) -> int | float | None:
    if percentile not in (50, 95, 99) or not values:
        return None
    ordered = sorted(values)
    return ordered[max(0, math.ceil(percentile * len(ordered) / 100) - 1)]


def summarize_attempts(attempts: list[dict[str, Any]]) -> dict[str, Any]:
    counts = {status: sum(item.get("status") == status for item in attempts)
              for status in ("success", "failed", "timeout", "incomplete", "not_attempted")}
    successful = [item for item in attempts if item.get("status") == "success"]
    distributions: dict[str, Any] = {}
    for metric in METRICS:
        values = [item[metric] for item in successful
                  if isinstance(item.get(metric), (int, float)) and not isinstance(item.get(metric), bool)]
        distributions[metric] = {
            "n": len(values), "p50": nearest_rank(values, 50), "p95": nearest_rank(values, 95),
            "p99": nearest_rank(values, 99), "max": max(values) if values else None,
        }
    frame_gate = all(
        distributions[metric]["n"] == counts["success"]
        and distributions[metric]["p95"] is not None
        and distributions[metric]["p95"] <= 2000
        and distributions[metric]["p99"] <= 5000
        and distributions[metric]["max"] < 10_000
        for metric in ("show_word_ms", "first_frame_ms")
    )
    return {"counts": counts, "distributions": distributions,
            "frame_gate_passed": frame_gate,
            "gate_passed": frame_gate and counts["success"] == len(attempts)
            and counts["failed"] == counts["timeout"] == counts["incomplete"] == counts["not_attempted"] == 0}


def _load_storage_profile(run_root: Path) -> dict[str, Any]:
    path = run_root / "storage-profile.json"
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise MatrixProtocolError("private storage profile artifact could not be read") from exc
    size_schema = {"database_bytes", "wal_bytes"}
    size_names = ("before_timed_work", "after_timed_work_before_close", "after_close")
    if (not isinstance(value, dict) or set(value) != {
            "schema_version", "effective_profile", "guarded_connections", "runtime_pragma_attempts",
            "before_timed_work", "after_timed_work_before_close", "after_close"}
            or value["schema_version"] != 1 or not isinstance(value["effective_profile"], dict)
            or set(value["effective_profile"]) != {"journal_mode", "synchronous", "busy_timeout_ms"}
            or not isinstance(value["effective_profile"]["journal_mode"], str)
            or not value["effective_profile"]["journal_mode"]
            or type(value["guarded_connections"]) is not int or value["guarded_connections"] < 3
            or type(value["runtime_pragma_attempts"]) is not int or value["runtime_pragma_attempts"] != 0
            or any(not isinstance(value[name], dict) or set(value[name]) != size_schema
                   or type(value[name]["database_bytes"]) is not int or value[name]["database_bytes"] <= 0
                   or type(value[name]["wal_bytes"]) is not int or value[name]["wal_bytes"] < 0
                   for name in size_names)
            or type(value["effective_profile"]["synchronous"]) is not int
            or not 0 <= value["effective_profile"]["synchronous"] <= 3
            or type(value["effective_profile"]["busy_timeout_ms"]) is not int
            or not 0 <= value["effective_profile"]["busy_timeout_ms"] <= 120_000
            or any(ord(char) < 0x20 for char in value["effective_profile"]["journal_mode"])):
        raise MatrixProtocolError("private storage profile artifact violates schema")
    return value


def _write_private_results(path: Path, payload: dict[str, Any]) -> None:
    encoded = json.dumps(payload, sort_keys=True, separators=(",", ":"), allow_nan=False).encode("utf-8")
    temporary = path.with_name(path.name + ".tmp-" + uuid.uuid4().hex)
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(encoded + b"\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass


def _failure_status(code: str) -> str:
    if code in TIMEOUT_CODES:
        return "timeout"
    if code in INCOMPLETE_CODES or code in {"controller_failure", "matrix_protocol_error"}:
        return "incomplete"
    return "failed"


def _journey_values(journey: dict[str, Any]) -> dict[str, Any]:
    fields = (*METRICS, "lesson_id", "generation", "source_build_identity",
              "web_build_identity", "websocket_closed")
    if (any(field not in journey for field in fields)
            or journey["websocket_closed"] is not True
            or not isinstance(journey["source_build_identity"], str)
            or not re.fullmatch(r"[0-9a-f]{64}", journey["source_build_identity"], re.ASCII)
            or not isinstance(journey["web_build_identity"], str)
            or not re.fullmatch(r"[0-9a-f]{64}", journey["web_build_identity"], re.ASCII)):
        raise MatrixProtocolError("first-audio evidence is invalid")
    return {field: journey[field] for field in fields}


async def run_matrix(repository_root: Path, go_binary: Path, node_binary: Path,
                     run_id: str | None = None) -> tuple[dict[str, Any], Path]:
    repository_root = repository_root.resolve(strict=True)
    artifact_root = repository_root / ".test-artifacts"
    if not artifact_root.is_dir():
        raise MatrixProtocolError("private artifact root is unavailable")
    matrix_id = run_id or ("fa" + uuid.uuid4().hex[:30])
    if not re.fullmatch(r"[A-Za-z0-9_-]{8,64}", matrix_id, re.ASCII):
        raise MatrixProtocolError("invalid run id")
    matrix_root = artifact_root / ("first-audio-latency-" + matrix_id)
    _create_private_directory(matrix_root)
    results_path = matrix_root / "matrix-results.json"
    cold: list[dict[str, Any]] = [{"sample": index, "status": "not_attempted"}
                                  for index in range(1, JOURNEYS + 1)]
    warm: list[dict[str, Any]] = [{"sample": index, "status": "not_attempted"}
                                  for index in range(1, WARM_SAMPLES + 1)]
    profile_artifacts: list[dict[str, Any]] = []
    warmup_status = "not_attempted"
    cold_run_failures: list[dict[str, Any]] = []
    warm_run_status = "not_attempted"
    warm_run_failure_code: str | None = None
    web_build_status = "not_started"
    web_build_failure_code: str | None = None
    web_build_identity: str | None = None
    source_build_identity: str | None = None
    result: dict[str, Any] = {"schema_version": 1, "run_id": matrix_id, "status": "running",
                              "cold": cold, "warm": warm, "warmup_status": warmup_status,
                              "cold_run_failures": cold_run_failures, "warm_run_status": warm_run_status,
                              "warm_run_failure_code": warm_run_failure_code,
                              "web_build_status": web_build_status,
                              "web_build_failure_code": web_build_failure_code,
                              "web_build_identity": web_build_identity,
                              "source_build_identity": source_build_identity,
                              "storage_profiles": profile_artifacts,
                              "percentile_method": "nearest_rank",
                              "confidence_note": {
                                  "cold_p99": "low_confidence_order_statistic_n_30",
                                  "warm_p99": "near_maximum_low_confidence_n_100"}}

    def persist() -> None:
        result.update({"warmup_status": warmup_status,
                       "cold_run_failures": cold_run_failures,
                       "warm_run_status": warm_run_status,
                       "warm_run_failure_code": warm_run_failure_code,
                       "web_build_status": web_build_status,
                       "web_build_failure_code": web_build_failure_code,
                       "web_build_identity": web_build_identity})
        result["source_build_identity"] = source_build_identity
        result["cold_summary"] = summarize_attempts(cold)
        result["warm_summary"] = summarize_attempts(warm)
        _write_private_results(results_path, result)

    persist()
    web_build_status = "running"
    persist()
    try:
        web_build_identity, web_build_dir = await build_first_audio_web(
            repository_root, node_binary, matrix_root)
        web_build_status = "success"
    except Exception as exc:
        web_build_status = _failure_status(_safe_failure_code(exc))
        web_build_failure_code = _safe_failure_code(exc)
        result.update({"status": "failed"})
        persist()
        cold_summary = summarize_attempts(cold)
        warm_summary = summarize_attempts(warm)
        return {"status": "failed", "cold_counts": cold_summary["counts"],
                "warm_counts": warm_summary["counts"], "warmup_status": warmup_status,
                "web_build_failure_code": web_build_failure_code}, results_path
    persist()
    cold_stop = False
    for index in range(1, JOURNEYS + 1):
        if cold_stop:
            continue
        cold[index - 1] = {"sample": index, "status": "incomplete"}
        persist()
        subrun_id = f"{matrix_id}_c{index:02d}"

        def cold_progress(event: dict[str, Any]) -> None:
            nonlocal source_build_identity
            if event.get("event") == "attempt_started":
                cold[index - 1] = {"sample": index, "status": "incomplete"}
            elif event.get("event") == "journey_completed" and event.get("journey") == 1:
                values = _journey_values(event)
                if (values["web_build_identity"] != web_build_identity
                        or (source_build_identity is not None
                            and values["source_build_identity"] != source_build_identity)):
                    raise MatrixProtocolError("first-audio evidence is invalid")
                source_build_identity = values["source_build_identity"]
                cold[index - 1] = {"sample": index, "status": "success", **values}
            persist()

        try:
            outcome = await run_first_audio_reset_acceptance(
                repository_root, subrun_id, go_binary, node_binary, journey_count=1,
                progress_callback=cold_progress, web_build_ready=True,
                web_build_identity=web_build_identity, web_build_dir=web_build_dir)
            if (outcome.get("reset_count") != 1 or cold[index - 1].get("status") != "success"
                    or len(outcome.get("journeys", [])) != 1):
                raise MatrixProtocolError("first-audio evidence is invalid")
            profile = _load_storage_profile(artifact_root / ("first-audio-matrix-" + subrun_id))
            profile_artifacts.append({"cohort": "cold", "sample": index, **profile})
        except Exception as exc:
            code = _safe_failure_code(exc)
            if cold[index - 1].get("status") != "success":
                cold[index - 1].update({"status": _failure_status(code), "failure_code": code,
                                        "failure_stage": _safe_failure_stage(exc)})
            cold_run_failures.append({"sample": index, "failure_code": code,
                                      "failure_stage": _safe_failure_stage(exc)})
            cold_stop = True
        persist()
    if any(item["status"] != "success" for item in cold) or cold_run_failures:
        warmup_status = "not_attempted"
    else:
        subrun_id = f"{matrix_id}_warm"
        warm_run_status = "running"
        persist()

        def warm_progress(event: dict[str, Any]) -> None:
            nonlocal warmup_status, source_build_identity
            journey = event.get("journey")
            if not isinstance(journey, int) or not 1 <= journey <= WARM_SAMPLES + 1:
                raise MatrixProtocolError("first-audio evidence is invalid")
            if event.get("event") == "attempt_started":
                if event.get("warmup") is True:
                    warmup_status = "incomplete"
                else:
                    warm[journey - 2] = {"sample": journey - 1, "status": "incomplete"}
            elif event.get("event") == "journey_completed":
                values = _journey_values(event)
                if (values["web_build_identity"] != web_build_identity
                        or (source_build_identity is not None
                            and values["source_build_identity"] != source_build_identity)):
                    raise MatrixProtocolError("first-audio evidence is invalid")
                source_build_identity = values["source_build_identity"]
                if event.get("warmup") is True:
                    warmup_status = "success"
                else:
                    warm[journey - 2] = {"sample": journey - 1, "status": "success", **values}
            persist()

        try:
            outcome = await run_first_audio_reset_acceptance(
                repository_root, subrun_id, go_binary, node_binary,
                journey_count=WARM_SAMPLES + 1, warmup_first=True,
                progress_callback=warm_progress, web_build_ready=True,
                web_build_identity=web_build_identity, web_build_dir=web_build_dir)
            if (outcome.get("reset_count") != WARM_SAMPLES
                    or warmup_status != "success"
                    or any(item["status"] != "success" for item in warm)):
                raise MatrixProtocolError("reset count does not match completed journeys")
            profile_artifacts.append({"cohort": "warm", "sample": WARM_SAMPLES, **_load_storage_profile(
                artifact_root / ("first-audio-matrix-" + subrun_id))})
            warm_run_status = "success"
        except Exception as exc:
            code = _safe_failure_code(exc)
            warm_run_status = "failed"
            warm_run_failure_code = code
            result["warm_run_failure_stage"] = _safe_failure_stage(exc)
            if warmup_status == "incomplete":
                warmup_status = _failure_status(code)
            for item in warm:
                if item["status"] == "incomplete":
                    item["status"] = _failure_status(code)
                    item["failure_code"] = code
        persist()
    cold_summary = summarize_attempts(cold)
    warm_summary = summarize_attempts(warm)
    passed = (cold_summary["gate_passed"] and warm_summary["gate_passed"]
              and warmup_status == "success" and warm_run_status == "success"
              and web_build_status == "success"
              and not cold_run_failures and len(profile_artifacts) == JOURNEYS + 1)
    result.update({"status": "passed" if passed else "failed", "warmup_status": warmup_status,
                   "warm_run_status": warm_run_status, "warm_run_failure_code": warm_run_failure_code,
                   "cold_summary": cold_summary, "warm_summary": warm_summary})
    persist()
    return {"status": result["status"], "cold_counts": cold_summary["counts"],
            "warm_counts": warm_summary["counts"], "warmup_status": warmup_status,
            "web_build_status": web_build_status, "warm_run_status": warm_run_status,
            "cold_run_failures": cold_run_failures,
            "warm_run_failure_code": warm_run_failure_code}, results_path


def main() -> int:
    parser = argparse.ArgumentParser(description="Run the private synthetic 30-cold/100-warm first-audio matrix")
    parser.add_argument("--go", required=True, type=Path)
    parser.add_argument("--node", required=True, type=Path)
    parser.add_argument("--run-id")
    args = parser.parse_args()
    repository_root = Path(__file__).resolve(strict=True).parent.parent
    try:
        summary, _private_result_path = asyncio.run(run_matrix(repository_root, args.go, args.node, args.run_id))
    except Exception as exc:
        print(json.dumps({"status": "failed", "reason": _safe_failure_code(exc)}, separators=(",", ":")))
        return 1
    print(json.dumps(summary, sort_keys=True, separators=(",", ":")))
    return 0 if summary["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
