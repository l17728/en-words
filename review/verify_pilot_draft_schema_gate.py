"""Verify explicit skips for the unapproved Pilot v3 schema draft tests."""

from __future__ import annotations

import json
from pathlib import Path
import sys


EXPECTED_SKIPS = {
    "TestOpenRejectsUnexpectedPersistentView",
    "TestOpenRejectsUnexpectedSQLiteUserVersionWithoutMutation",
    "TestOpenAcceptsExactPinnedSchemaFixture",
    "TestProtectedConnectionRevalidatesSchemaAfterReadOnlyPreflight",
}
SKIP_REASON = (
    "draft schema fixture NOT RUN: 103-object candidate is not reconciled with the approved 95-object inventory; "
    "pending docs/65 canonical input approval"
)
LEGACY_REJECTION_TEST = "TestOpenRejectsUnrecognizedSchemaWithoutMutatingDatabase"
EXPECTED_PACKAGE = "enwords/internal/pilotcontrol"
ALLOWED_ACTIONS = {"start", "run", "pause", "cont", "pass", "bench", "fail", "output", "skip"}


def verify(events: list[dict[str, object]]) -> list[str]:
    errors: list[str] = []
    start_events = [event for event in events if event.get("Action") == "start"]
    if len(start_events) != 1 or not events or events[0].get("Action") != "start":
        errors.append("event stream must begin with exactly one Go test start event")
    for index, event in enumerate(events):
        if event.get("Action") not in ALLOWED_ACTIONS:
            errors.append(f"event {index} has unknown Go test action {event.get('Action')!r}")
        if index == 0 and event.get("Action") == "start" and event.get("Package") is None:
            # test2json's stream-level start event may omit Package; all package/test events are bound below.
            continue
        if event.get("Package") != EXPECTED_PACKAGE:
            errors.append(f"event {index} is missing exact Package {EXPECTED_PACKAGE!r}")
    test_events: dict[str, list[dict[str, object]]] = {}
    for event in events:
        package = event.get("Package")
        test_name = event.get("Test")
        if package is not None and package != EXPECTED_PACKAGE:
            errors.append(f"event belongs to unexpected package: {package!r}")
        if test_name is not None:
            if not isinstance(test_name, str) or not test_name:
                errors.append("test event has invalid test ID")
                continue
            if package != EXPECTED_PACKAGE:
                errors.append(f"test event {test_name} is missing exact Package {EXPECTED_PACKAGE!r}")
            test_events.setdefault(test_name, []).append(event)

    for name, test_actions in test_events.items():
        actions = [event.get("Action") for event in test_actions]
        if not actions or actions[0] != "run" or actions.count("run") != 1:
            errors.append(f"test {name} must have exactly one initial run event")
        terminals = [action for action in actions if action in {"pass", "skip", "fail"}]
        if len(terminals) != 1:
            errors.append(f"test {name} must have exactly one terminal event; observed {terminals!r}")
        elif actions[-1] != terminals[0]:
            errors.append(f"test {name} has events after its terminal {terminals[0]!r}")

    all_skip_events = [event for event in events if event.get("Action") == "skip"]
    skip_events = [event for event in all_skip_events if event.get("Test")]
    skip_names = [str(event["Test"]) for event in skip_events]
    if len(all_skip_events) != len(skip_events):
        errors.append("a skip event is missing its test ID")
    if len(skip_names) != len(EXPECTED_SKIPS) or set(skip_names) != EXPECTED_SKIPS:
        errors.append(f"skip test IDs/count mismatch: got={skip_names!r}, want exactly={sorted(EXPECTED_SKIPS)!r}")

    outputs: dict[str, str] = {}
    for event in events:
        if event.get("Action") == "output" and event.get("Test"):
            test_name = str(event["Test"])
            outputs[test_name] = outputs.get(test_name, "") + str(event.get("Output", ""))
    for name in EXPECTED_SKIPS:
        if name in skip_names and SKIP_REASON not in outputs.get(name, ""):
            errors.append(f"skip {name} is missing the approved reason")

    test_failures = [str(event.get("Test", "<package>")) for event in events if event.get("Action") == "fail"]
    if test_failures:
        errors.append(f"test failures present: {test_failures!r}")

    legacy_passes = [event for event in events if event.get("Action") == "pass" and event.get("Test") == LEGACY_REJECTION_TEST]
    if len(legacy_passes) != 1:
        errors.append(f"legacy rejection test must PASS exactly once; observed {len(legacy_passes)} pass events")

    package_passes = [
        event for event in events
        if event.get("Action") == "pass" and not event.get("Test") and event.get("Package") == EXPECTED_PACKAGE
    ]
    if len(package_passes) != 1:
        errors.append(f"pilotcontrol package must PASS exactly once; observed {len(package_passes)} pass events")
    elif not events or events[-1] is not package_passes[0]:
        errors.append("pilotcontrol package PASS must be the final event in the stream")
    return errors


def load_events(path: Path) -> list[dict[str, object]]:
    events: list[dict[str, object]] = []
    with path.open("r", encoding="utf-8") as stream:
        for line_number, line in enumerate(stream, start=1):
            try:
                event = json.loads(line, object_pairs_hook=_reject_duplicate_keys)
            except (json.JSONDecodeError, ValueError) as error:
                raise ValueError(f"invalid go test JSON at line {line_number}: {error}") from error
            if not isinstance(event, dict) or not isinstance(event.get("Action"), str):
                raise ValueError(f"invalid go test event at line {line_number}")
            events.append(event)
    if not events:
        raise ValueError("go test JSON event stream is empty")
    return events


def _reject_duplicate_keys(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key {key!r}")
        result[key] = value
    return result


def main(argv: list[str] | None = None) -> int:
    args = sys.argv[1:] if argv is None else argv
    if len(args) != 1:
        print("usage: verify_pilot_draft_schema_gate.py <go-test-jsonl>", file=sys.stderr)
        return 2
    try:
        errors = verify(load_events(Path(args[0])))
    except (OSError, ValueError) as error:
        print(f"PILOT_SCHEMA_DRAFT_GATE_INVALID: {error}", file=sys.stderr)
        return 1
    if errors:
        for error in errors:
            print(f"PILOT_SCHEMA_DRAFT_GATE_FAIL: {error}", file=sys.stderr)
        return 1
    print("PILOT_SCHEMA_DRAFT_GATE_PASS: exactly four documented draft tests skipped; legacy rejection and package passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
