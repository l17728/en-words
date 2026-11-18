"""Contracts for the Go JSON event verifier used by Linux package CI."""

from __future__ import annotations

import importlib.util
from pathlib import Path
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "review" / "verify_pilot_draft_schema_gate.py"
SPEC = importlib.util.spec_from_file_location("pilot_draft_schema_gate", SCRIPT)
assert SPEC and SPEC.loader
GATE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GATE)


def passing_events() -> list[dict[str, object]]:
    events: list[dict[str, object]] = [{"Action": "start"}]
    for name in sorted(GATE.EXPECTED_SKIPS):
        events.extend(
            [
                {"Action": "run", "Package": GATE.EXPECTED_PACKAGE, "Test": name},
                {"Action": "output", "Package": GATE.EXPECTED_PACKAGE, "Test": name, "Output": GATE.SKIP_REASON + "\n"},
                {"Action": "skip", "Package": GATE.EXPECTED_PACKAGE, "Test": name},
            ]
        )
    events.extend(
        [
            {"Action": "run", "Package": GATE.EXPECTED_PACKAGE, "Test": GATE.LEGACY_REJECTION_TEST},
            {"Action": "pass", "Package": GATE.EXPECTED_PACKAGE, "Test": GATE.LEGACY_REJECTION_TEST},
            {"Action": "pass", "Package": GATE.EXPECTED_PACKAGE},
        ]
    )
    return events


class DraftSchemaGateVerifierTests(unittest.TestCase):
    def test_accepts_only_the_four_named_skips_and_legacy_package_pass(self) -> None:
        self.assertEqual(GATE.verify(passing_events()), [])

    def test_rejects_missing_duplicate_and_unexpected_skip_events(self) -> None:
        for mutate in (
            lambda events: events.pop(next(i for i, item in enumerate(events) if item.get("Action") == "skip")),
            lambda events: events.__setitem__(
                next(i for i, item in enumerate(events) if item.get("Action") == "skip"),
                {"Action": "skip", "Test": "UnexpectedSkip"},
            ),
            lambda events: events.append({"Action": "skip", "Test": "TestUnexpectedSkip"}),
        ):
            with self.subTest(mutate=mutate):
                events = passing_events()
                mutate(events)
                self.assertTrue(any("skip test IDs/count mismatch" in error for error in GATE.verify(events)))

    def test_rejects_wrong_reason_missing_legacy_pass_and_test_failure(self) -> None:
        events = passing_events()
        output = next(item for item in events if item.get("Action") == "output")
        output["Output"] = "different skip reason\n"
        events = [item for item in events if item.get("Test") != GATE.LEGACY_REJECTION_TEST]
        events.append({"Action": "fail", "Test": "TestUnexpectedFailure"})
        errors = GATE.verify(events)
        self.assertTrue(any("missing the approved reason" in error for error in errors))
        self.assertTrue(any("legacy rejection test must PASS" in error for error in errors))
        self.assertTrue(any("test failures present" in error for error in errors))

    def test_rejects_malformed_json_lines(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "events.jsonl"
            path.write_text('{"Action":"pass"}\nnot-json\n', encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "invalid go test JSON at line 2"):
                GATE.load_events(path)

    def test_rejects_duplicate_json_keys(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "events.jsonl"
            path.write_text('{"Action":"pass","Action":"skip"}\n', encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "duplicate JSON key 'Action'"):
                GATE.load_events(path)

    def test_rejects_cross_package_or_missing_package_test_evidence(self) -> None:
        for replacement in ("enwords/internal/other", None):
            with self.subTest(package=replacement):
                events = passing_events()
                events[1]["Package"] = replacement
                self.assertTrue(any("unexpected package" in error or "missing exact Package" in error for error in GATE.verify(events)))

    def test_rejects_missing_duplicate_or_out_of_order_test_terminals(self) -> None:
        first_skip = sorted(GATE.EXPECTED_SKIPS)[0]
        cases = (
            lambda events: events.__setitem__(next(i for i, e in enumerate(events) if e.get("Test") == first_skip and e.get("Action") == "skip"), {"Action": "output", "Package": GATE.EXPECTED_PACKAGE, "Test": first_skip, "Output": "no terminal"}),
            lambda events: events.insert(next(i for i, e in enumerate(events) if e.get("Test") == first_skip and e.get("Action") == "skip") + 1, {"Action": "pass", "Package": GATE.EXPECTED_PACKAGE, "Test": first_skip}),
            lambda events: events.pop(next(i for i, e in enumerate(events) if e.get("Test") == first_skip and e.get("Action") == "run")),
        )
        for mutate in cases:
            with self.subTest(mutate=mutate):
                events = passing_events()
                mutate(events)
                self.assertTrue(any("terminal" in error or "initial run" in error or "after its terminal" in error for error in GATE.verify(events)))

    def test_rejects_missing_or_duplicate_package_pass(self) -> None:
        for mutate in (
            lambda events: events.pop(),
            lambda events: events.append({"Action": "pass", "Package": GATE.EXPECTED_PACKAGE}),
        ):
            with self.subTest(mutate=mutate):
                events = passing_events()
                mutate(events)
                self.assertTrue(any("package must PASS exactly once" in error for error in GATE.verify(events)))

    def test_rejects_package_pass_before_tests_or_trailing_events(self) -> None:
        for mutate in (
            lambda events: events.insert(0, events.pop()),
            lambda events: events.append({"Action": "output", "Package": GATE.EXPECTED_PACKAGE, "Output": "late"}),
        ):
            with self.subTest(mutate=mutate):
                events = passing_events()
                mutate(events)
                self.assertTrue(any("package PASS must be the final event" in error for error in GATE.verify(events)))

    def test_requires_stream_start_and_package_identity_on_package_events(self) -> None:
        cases = (
            lambda events: events.pop(0),
            lambda events: events.__setitem__(0, {"Action": "start", "Package": "enwords/internal/other"}),
            lambda events: events.__setitem__(1, {"Action": "output", "Output": "missing package"}),
        )
        for mutate in cases:
            with self.subTest(mutate=mutate):
                events = passing_events()
                mutate(events)
                self.assertTrue(any("start event" in error or "exact Package" in error for error in GATE.verify(events)))

    def test_rejects_unknown_event_actions(self) -> None:
        events = passing_events()
        events.insert(1, {"Action": "authorized", "Package": GATE.EXPECTED_PACKAGE})
        self.assertTrue(any("unknown Go test action" in error for error in GATE.verify(events)))


if __name__ == "__main__":
    unittest.main()
