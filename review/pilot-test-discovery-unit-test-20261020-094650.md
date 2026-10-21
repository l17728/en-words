# Pilot test discovery unit test — 20261020-094650

Command:
````
python -m unittest discover -s review -p test_pilot_test_discovery.py -v
````

Exit code: `0`

Output:
````text
test_audit_archives_failure_and_partial_phase_evidence (test_pilot_test_discovery.ManifestTests.test_audit_archives_failure_and_partial_phase_evidence) ... ok
test_empty_children_require_explicit_parent_only_declaration (test_pilot_test_discovery.ManifestTests.test_empty_children_require_explicit_parent_only_declaration) ... ok
test_interrupted_probe_stops_audit_before_next_command (test_pilot_test_discovery.ManifestTests.test_interrupted_probe_stops_audit_before_next_command) ... ok
test_native_audit_refuses_cross_compilation_before_running_tests (test_pilot_test_discovery.ManifestTests.test_native_audit_refuses_cross_compilation_before_running_tests) ... ok
test_parent_only_suite_must_be_explicit (test_pilot_test_discovery.ManifestTests.test_parent_only_suite_must_be_explicit) ... ok
test_rejects_duplicate_json_keys (test_pilot_test_discovery.ManifestTests.test_rejects_duplicate_json_keys) ... ok
test_rejects_empty_required_suite (test_pilot_test_discovery.ManifestTests.test_rejects_empty_required_suite) ... ok
test_rejects_stale_source_sdd_hash (test_pilot_test_discovery.ManifestTests.test_rejects_stale_source_sdd_hash) ... ok
test_timeout_terminates_child_and_returns_partial_evidence (test_pilot_test_discovery.ProcessRunnerTests.test_timeout_terminates_child_and_returns_partial_evidence) ... ok
test_windows_tree_cleanup_failure_is_bounded_and_reported (test_pilot_test_discovery.ProcessRunnerTests.test_windows_tree_cleanup_failure_is_bounded_and_reported) ... ok
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

----------------------------------------------------------------------
Ran 21 tests in 1.747s

OK
````

