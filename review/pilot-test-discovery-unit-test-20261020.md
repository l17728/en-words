# Pilot test discovery unit test — 2026-10-20

Command:
````
python -m unittest discover -s review -p test_pilot_test_discovery.py -v
````

Exit code: `0`

Output:
````text
test_native_audit_refuses_cross_compilation_before_running_tests (test_pilot_test_discovery.ManifestTests.test_native_audit_refuses_cross_compilation_before_running_tests) ... ok
test_native_audit_rejects_registered_parent_with_missing_subtest_and_archives_failure (test_pilot_test_discovery.ManifestTests.test_native_audit_rejects_registered_parent_with_missing_subtest_and_archives_failure) ... ok
test_rejects_empty_required_suite (test_pilot_test_discovery.ManifestTests.test_rejects_empty_required_suite) ... ok
test_failure_exit_code_fails_even_if_json_claims_pass (test_pilot_test_discovery.ValidateEventsTests.test_failure_exit_code_fails_even_if_json_claims_pass) ... ok
test_malformed_json_fails_closed (test_pilot_test_discovery.ValidateEventsTests.test_malformed_json_fails_closed) ... ok
test_missing_subtest_fails_even_when_parent_passes (test_pilot_test_discovery.ValidateEventsTests.test_missing_subtest_fails_even_when_parent_passes) ... ok
test_parent_must_pass (test_pilot_test_discovery.ValidateEventsTests.test_parent_must_pass) ... ok
test_requires_parent_and_every_required_subtest_to_pass (test_pilot_test_discovery.ValidateEventsTests.test_requires_parent_and_every_required_subtest_to_pass) ... ok
test_run_without_terminal_pass_fails (test_pilot_test_discovery.ValidateEventsTests.test_run_without_terminal_pass_fails) ... ok
test_skip_fails (test_pilot_test_discovery.ValidateEventsTests.test_skip_fails) ... ok

----------------------------------------------------------------------
Ran 10 tests in 0.062s

OK
````

