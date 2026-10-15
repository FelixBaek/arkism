"""Reject incomplete evidence and exclude account telemetry from public logs."""
import json
from pathlib import Path
import sys
import unittest

ROOT = Path(__file__).resolve().parent / 'reuse-20261015'
sys.path.insert(0, str(ROOT))
from live_agents import public_event
from summarize_checks import summarize


class EvidencePublicationTests(unittest.TestCase):
    def raw(self, *events):
        return '\n'.join(json.dumps(event) for event in events).encode()

    def test_account_telemetry_is_not_public_evidence(self):
        self.assertIsNone(public_event({'type': 'rate_limit_event', 'rate_limit_info': {'utilization': 0.5}}))
        event = {'type': 'turn.completed', 'usage': {'input_tokens': 7}}
        self.assertEqual(public_event(event), event)

    def test_malformed_or_unfinished_log_is_rejected(self):
        start = {'Action': 'start', 'Package': 'fixture'}
        completed = {'Action': 'pass', 'Package': 'fixture'}
        malformed = self.raw(start, completed) + b'\n{"Action":'
        incomplete = self.raw(start)
        running = self.raw(start, {'Action': 'run', 'Package': 'fixture', 'Test': 'TestOne'}, completed)
        for raw in (malformed, incomplete, running, b''):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                summarize(raw)

    def test_package_panic_remains_a_failure(self):
        raw = self.raw({'Action': 'run', 'Package': 'fixture', 'Test': 'TestOne'},
                       {'Action': 'fail', 'Package': 'fixture'})
        result = summarize(raw)
        self.assertEqual(len(result[3]), 1)
        self.assertEqual(result[4][0]['result'], 'fail')

    def test_finished_failure_is_preserved(self):
        raw = self.raw({'Action': 'start', 'Package': 'fixture'},
                       {'Action': 'run', 'Package': 'fixture', 'Test': 'TestOne'},
                       {'Action': 'fail', 'Package': 'fixture', 'Test': 'TestOne'},
                       {'Action': 'fail', 'Package': 'fixture'})
        counts, top, skips, failures, packages = summarize(raw)
        self.assertEqual(counts['fail'], 1)
        self.assertEqual(top['fail'], 1)
        self.assertEqual(skips, [])
        self.assertEqual(len(failures), 2)
        self.assertEqual(packages[0]['result'], 'fail')


if __name__ == '__main__':
    unittest.main()
