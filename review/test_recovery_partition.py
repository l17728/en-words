import copy
import re
import unittest
from pathlib import Path
from run_recovery_partition import entries, partition, pattern, bind_retained, unaffected_source


def report(count):
    return {'stats': {'skipped': 0, 'unexpected': 0, 'flaky': 0}, 'suites': [{'title': 'file.spec.ts', 'specs': [{'title': f'case [{i}]?', 'id': str(i), 'tests': [{'projectId': '', 'status': 'expected', 'results': [{'status': 'passed', 'retry': 0}]}]} for i in range(count)]}]}


class FixedRemainingGroups(unittest.TestCase):
    def test_three_sevens_have_exact_disjoint_complement(self):
        groups = partition(report(42), report(21))
        self.assertEqual([len(x) for x in groups], [7, 7, 7])
        self.assertEqual([x['id'] for g in groups for x in g], [f'{i}:' for i in range(21, 42)])

    def test_duplicate_missing_foreign_ids_fail(self):
        for data, kept in [(report(41), report(21)), (report(42), report(20)), (report(42), report(22))]:
            with self.assertRaises(RuntimeError): partition(data, kept)
        data = report(42); data['suites'][0]['specs'][1]['id'] = '0'
        with self.assertRaises(RuntimeError): partition(data, report(21))

    def test_failed_or_retried_retained_case_rejected(self):
        for field, value in [('status', 'failed'), ('retry', 1)]:
            kept = copy.deepcopy(report(21)); kept['suites'][0]['specs'][0]['tests'][0]['results'][0][field] = value
            with self.assertRaises(RuntimeError): partition(report(42), kept)

    def test_complete_title_regex_escapes_and_anchors(self):
        selected = entries(report(1)); regex = re.compile(pattern(selected))
        self.assertIsNotNone(regex.search('project ' + selected[0]['title']))
        self.assertIsNone(regex.search(selected[0]['title'] + ' extra'))
        self.assertIsNone(regex.search('fileXspecYts case 0'))

    def test_retained_must_be_the_recorded_report_and_discovery(self):
        data, kept = report(42), report(21)
        prior = {'suites': [{'suite': 'recovery', 'expected_ids': [r['id'] for r in entries(data)], 'reports': [{'path': 'retained.json', 'stats': kept['stats'], 'test_ids': [r['id'] for r in entries(kept)]}]}]}
        bind_retained(prior, Path('retained.json'), kept, data)
        with self.assertRaises(RuntimeError): bind_retained(prior, Path('foreign.json'), kept, data)
        altered = copy.deepcopy(kept); altered['stats']['unexpected'] = 1
        with self.assertRaises(RuntimeError): bind_retained(prior, Path('retained.json'), altered, data)

    def test_local_body_proof_preserves_everything_outside_approved_regions(self):
        before = "shared\ntest('old recognizer delayed onend old body test('G09 canceled delayed synthesis unchanged\nfor (const recovery of ['phase', 'pause']) old body test('SUB04 disconnect tail"
        after = before.replace('old body', 'new body')
        self.assertEqual(unaffected_source(before), unaffected_source(after))
        self.assertNotEqual(unaffected_source(before), unaffected_source(after.replace('shared', 'changed')))
        with self.assertRaises(RuntimeError): unaffected_source('missing markers')


if __name__ == '__main__': unittest.main()
