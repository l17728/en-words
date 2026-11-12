import unittest
from run_browser_acceptance import validate_shards, validate_global_scenarios, wait_owned, SUITES, EXPECTED
import subprocess
from unittest.mock import Mock


def report(ids, status="passed", retry=0):
    return {"stats": {"skipped": 0, "unexpected": 0, "flaky": 0}, "suites": [{"specs": [{"id": key, "tests": [{"projectId": "", "status": "expected", "results": [{"status": status, "retry": retry}]}]} for key in ids]}]}


class CompleteShards(unittest.TestCase):
    def test_discovered_suite_counts_match_the_current_contract(self):
        self.assertEqual(SUITES.count('continuation'), 1)
        self.assertEqual(EXPECTED['continuation'], 6)
        self.assertEqual(EXPECTED['recovery'], 43)
        self.assertEqual(sum(EXPECTED[suite] for suite in SUITES), 148)

    def test_complete_union(self):
        self.assertEqual(validate_shards(["a:", "b:"], [report(["a"]), report(["b"])]), ["a:", "b:"])

    def test_single_suite_requires_exact_ids_and_one_pass_without_retry(self):
        self.assertEqual(validate_shards(["a:", "b:"], [report(["a", "b"])]), ["a:", "b:"])
        for data in [report(["a"]), report(["a", "a"]), report(["a", "b"], retry=1)]:
            with self.subTest(data=data), self.assertRaises(RuntimeError):
                validate_shards(["a:", "b:"], [data])

    def test_missing_duplicate_and_unexpected_ids_rejected(self):
        for groups in [[report(["a"])], [report(["a"]), report(["a"])], [report(["a"]), report(["c"])]]:
            with self.subTest(groups=groups), self.assertRaises(RuntimeError): validate_shards(["a:", "b:"], groups)

    def test_nonpass_or_retry_rejected(self):
        for data in [report(["a"], "failed"), report(["a"], retry=1)]:
            with self.assertRaises(RuntimeError): validate_shards(["a:"], [data])

    def test_skip_flaky_and_duplicate_discovery_rejected(self):
        for field in ["skipped", "unexpected", "flaky"]:
            data = report(["a"]); data["stats"][field] = 1
            with self.assertRaises(RuntimeError): validate_shards(["a:"], [data])
        with self.assertRaises(RuntimeError): validate_shards(["a:", "a:"], [report(["a"])])

    def test_global_scenario_ids_allow_only_firstuse_playback_overlap(self):
        entries = [
            {"suite": "firstuse", "expected_ids": ["same:"], "verified_ids": ["same:"]},
            {"suite": "playback", "expected_ids": ["same:"], "verified_ids": ["same:"]},
        ]
        result = validate_global_scenarios(entries)
        self.assertEqual((result["scenario_executions"], result["unique_spec_ids"]), (2, 1))
        self.assertEqual(result["intentional_duplicate"], [{"spec_id": "same:", "suites": ["firstuse", "playback"]}])
        with self.assertRaises(RuntimeError): validate_global_scenarios(entries, require_complete=True)
        bad = [{"suite": "overdue", "expected_ids": ["same:"], "verified_ids": ["same:"]}]
        with self.assertRaises(RuntimeError): validate_global_scenarios(entries + bad)
        with self.assertRaises(RuntimeError): validate_global_scenarios(entries + entries[:1])
        with self.assertRaises(RuntimeError): validate_global_scenarios([{"suite": "firstuse", "expected_ids": ["same:"], "verified_ids": []}])


class OwnedTimeout(unittest.TestCase):
    def test_timeout_terminates_entire_owned_tree_before_waiting(self):
        process = Mock(pid=321)
        process.wait.side_effect = [subprocess.TimeoutExpired("owned", 900), 1]
        stop = Mock()
        with self.assertRaises(subprocess.TimeoutExpired): wait_owned(process, 900, stop)
        stop.assert_called_once_with(321)
        self.assertEqual(process.wait.call_args_list[0].kwargs, {"timeout": 900})
        self.assertEqual(process.wait.call_args_list[1].kwargs, {"timeout": 15})

    def test_normal_completion_does_not_kill_tree(self):
        process = Mock(pid=321); process.wait.return_value = 0
        stop = Mock()
        self.assertEqual(wait_owned(process, 900, stop), 0)
        stop.assert_not_called()


if __name__ == "__main__": unittest.main()
