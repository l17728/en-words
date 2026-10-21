# Pilot test discovery unit test — 20261020-094227

Command:
````
python -m unittest discover -s review -p test_pilot_test_discovery.py -v
````

Exit code: `1`

Output:
````text
test_audit_archives_failure_and_partial_phase_evidence (test_pilot_test_discovery.ManifestTests.test_audit_archives_failure_and_partial_phase_evidence) ... FAIL
test_empty_children_require_explicit_parent_only_declaration (test_pilot_test_discovery.ManifestTests.test_empty_children_require_explicit_parent_only_declaration) ... ok
test_native_audit_refuses_cross_compilation_before_running_tests (test_pilot_test_discovery.ManifestTests.test_native_audit_refuses_cross_compilation_before_running_tests) ... FAIL
test_parent_only_suite_must_be_explicit (test_pilot_test_discovery.ManifestTests.test_parent_only_suite_must_be_explicit) ... ok
test_rejects_duplicate_json_keys (test_pilot_test_discovery.ManifestTests.test_rejects_duplicate_json_keys) ... ok
test_rejects_empty_required_suite (test_pilot_test_discovery.ManifestTests.test_rejects_empty_required_suite) ... ok
test_rejects_stale_source_sdd_hash (test_pilot_test_discovery.ManifestTests.test_rejects_stale_source_sdd_hash) ... ok
test_timeout_terminates_child_and_returns_partial_evidence (test_pilot_test_discovery.ProcessRunnerTests.test_timeout_terminates_child_and_returns_partial_evidence) ... ok
test_malformed_json_fails_closed (test_pilot_test_discovery.ValidateEventsTests.test_malformed_json_fails_closed) ... ok
test_missing_subtest_fails_even_when_parent_passes (test_pilot_test_discovery.ValidateEventsTests.test_missing_subtest_fails_even_when_parent_passes) ... ok
test_nonzero_exit_fails_even_if_json_claims_pass (test_pilot_test_discovery.ValidateEventsTests.test_nonzero_exit_fails_even_if_json_claims_pass) ... ok
test_out_of_order_parent_or_package_events_fail (test_pilot_test_discovery.ValidateEventsTests.test_out_of_order_parent_or_package_events_fail) ... ok
test_package_failure_plus_pass_fails (test_pilot_test_discovery.ValidateEventsTests.test_package_failure_plus_pass_fails) ... ok
test_package_mismatch_fails (test_pilot_test_discovery.ValidateEventsTests.test_package_mismatch_fails) ... ok
test_parent_fail_plus_pass_conflict_fails (test_pilot_test_discovery.ValidateEventsTests.test_parent_fail_plus_pass_conflict_fails) ... ok
test_parent_skip_fails (test_pilot_test_discovery.ValidateEventsTests.test_parent_skip_fails) ... ok
test_requires_exact_parent_and_every_subtest_to_pass (test_pilot_test_discovery.ValidateEventsTests.test_requires_exact_parent_and_every_subtest_to_pass) ... ok
test_skipped_subtest_fails (test_pilot_test_discovery.ValidateEventsTests.test_skipped_subtest_fails) ... ok
test_subtest_without_terminal_fails (test_pilot_test_discovery.ValidateEventsTests.test_subtest_without_terminal_fails) ... ok

======================================================================
FAIL: test_audit_archives_failure_and_partial_phase_evidence (test_pilot_test_discovery.ManifestTests.test_audit_archives_failure_and_partial_phase_evidence)
----------------------------------------------------------------------
pilot_test_discovery.AuditError: pilot_native_test_manifest_failed: C:\Users\Administrator\AppData\Local\Temp\tmp2gf8c2xx\artifacts\20261020T014239.232824Z: unexpected_test_events: ['TestLedger/different-case']

During handling of the above exception, another exception occurred:

Traceback (most recent call last):
  File "D:\en-words\review\test_pilot_test_discovery.py", line 167, in test_audit_archives_failure_and_partial_phase_evidence
    with self.assertRaisesRegex(discovery.AuditError, "required_subtest_not_run_exactly_once"):
         ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
AssertionError: "required_subtest_not_run_exactly_once" does not match "pilot_native_test_manifest_failed: C:\Users\Administrator\AppData\Local\Temp\tmp2gf8c2xx\artifacts\20261020T014239.232824Z: unexpected_test_events: ['TestLedger/different-case']"

======================================================================
FAIL: test_native_audit_refuses_cross_compilation_before_running_tests (test_pilot_test_discovery.ManifestTests.test_native_audit_refuses_cross_compilation_before_running_tests)
----------------------------------------------------------------------
pilot_test_discovery.AuditError: target_not_in_manifest: linux/amd64

During handling of the above exception, another exception occurred:

Traceback (most recent call last):
  File "D:\en-words\review\test_pilot_test_discovery.py", line 188, in test_native_audit_refuses_cross_compilation_before_running_tests
    with self.assertRaisesRegex(discovery.AuditError, "cross_target_execution_forbidden"):
         ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
AssertionError: "cross_target_execution_forbidden" does not match "target_not_in_manifest: linux/amd64"

----------------------------------------------------------------------
Ran 19 tests in 0.746s

FAILED (failures=2)
````

