import json
import os
from datetime import datetime, timedelta, timezone
from pathlib import Path
import tempfile
import unittest

from measurement_audit import audit
from review_timer import record


class MeasurementTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)

    def run_fixture(self, condition):
        directory = self.root / f'task-{condition}-1'
        directory.mkdir()
        usage = {'input_tokens': 100, 'cached_input_tokens': 80, 'output_tokens': 5}
        row = dict(usage, task_id='task', condition=condition, replicate=1,
                   success=True, task_seconds=2, review_seconds=None)
        (directory / 'result.json').write_text(json.dumps(row))
        (directory / 'events.json').write_text(json.dumps([{'type': 'turn.completed', 'usage': usage}]))
        (directory / 'prompt.txt').write_text('same prompt')
        return directory

    def test_missing_and_partial_cost_is_not_zero_or_savings(self):
        self.run_fixture('static')
        self.run_fixture('arkism')
        report = audit(self.root)
        self.assertIsNone(report['conditions']['arkism']['billed_cost_usd'])
        self.assertEqual(report['conditions']['arkism']['uncached_input_tokens'], 20)
        charge = {'run_id': 'task-static-1', 'amount_usd': '0.25',
                  'basis': 'provider_billed', 'evidence_ref': 'synthetic-test-receipt'}
        report = audit(self.root, [charge])
        self.assertEqual(report['conditions']['static']['billed_cost_usd'], '0.25')
        self.assertIsNone(report['paired_static_arkism'][0]['static_minus_arkism_billed_usd'])
        with self.assertRaises(ValueError):
            audit(self.root, [charge, charge])
        with self.assertRaises(ValueError):
            audit(self.root, [dict(charge, basis='estimated_price')])

    def test_raw_usage_mismatch_is_rejected(self):
        directory = self.run_fixture('arkism')
        path = directory / 'result.json'
        row = json.loads(path.read_text())
        row['input_tokens'] = 99
        path.write_text(json.dumps(row))
        with self.assertRaises(ValueError):
            audit(self.root)

    def test_review_ledger_rejects_links_shared_files_and_nonregular_paths(self):
        target = self.root / 'target'
        target.write_text('preserve me')
        link = self.root / 'link'
        link.symlink_to(target)
        def start(path):
            return record(path, 'start', 'r', condition='arkism', candidate_id='c', reviewer='tester')
        with self.assertRaises(OSError):
            start(link)
        target.chmod(0o644)
        with self.assertRaises(ValueError):
            start(target)
        self.assertEqual(target.read_text(), 'preserve me')
        target.chmod(0o600)
        os.link(target, self.root / 'hardlink')
        with self.assertRaises(ValueError):
            start(target)
        fifo = self.root / 'fifo'
        os.mkfifo(fifo, 0o600)
        with self.assertRaises(ValueError):
            start(fifo)

    def test_review_interval_requires_operator_start_and_decision(self):
        path = self.root / 'private-reviews.jsonl'
        now = datetime(2026, 1, 1, tzinfo=timezone.utc)
        with self.assertRaises(ValueError):
            record(path, 'stop', 'r', decision='approved', evidence_ref='test', now=now)
        record(path, 'start', 'r', condition='arkism', candidate_id='candidate', reviewer='tester', now=now)
        with self.assertRaises(ValueError):
            record(path, 'start', 'r', condition='arkism', candidate_id='candidate', reviewer='tester', now=now)
        with self.assertRaises(ValueError):
            record(path, 'stop', 'r', decision='approved', evidence_ref='test', now=now-timedelta(seconds=1))
        with self.assertRaises(ValueError):
            record(path, 'stop', 'r', decision='approved', now=now)
        event = record(path, 'stop', 'r', decision='rejected', evidence_ref='test', now=now+timedelta(seconds=12))
        self.assertEqual(event['review_interval_seconds'], 12)
        self.assertIsNone(event['active_attention_seconds'])
        self.assertEqual(len(path.read_text().splitlines()), 2)
        with self.assertRaises(ValueError):
            record(path, 'stop', 'r', decision='approved', evidence_ref='test', now=now+timedelta(seconds=15))


if __name__ == '__main__':
    unittest.main()
