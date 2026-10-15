import json
import os
from datetime import datetime, timedelta, timezone
from pathlib import Path
import tempfile
import unittest

from measurement_audit import audit
from review_timer import record
from private_storage import private_file


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
        estimated = [dict(charge, basis='estimated_price')]
        with self.assertRaises(ValueError):
            audit(self.root, estimated)

    def test_prompt_changes_are_visible_even_when_pair_remains_equal(self):
        directories = [self.run_fixture('static'), self.run_fixture('arkism')]
        before = audit(self.root)
        for directory in directories:
            (directory / 'prompt.txt').write_text('changed together')
        after = audit(self.root)
        self.assertTrue(after['paired_static_arkism'][0]['prompt_bytes_equal'])
        self.assertEqual(before['conditions'], after['conditions'])
        self.assertEqual(len(after['evidence']), 6)
        old_hashes = {item['path']: item['sha256'] for item in before['evidence']}
        changed = [item['path'] for item in after['evidence']
                   if old_hashes[item['path']] != item['sha256']]
        self.assertEqual(sorted(changed), ['task-arkism-1/prompt.txt', 'task-static-1/prompt.txt'])

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
        ledger = self.root / 'reviews.jsonl'
        ledger.symlink_to(target)
        def start():
            return record('start', 'r', condition='arkism', candidate_id='c',
                          reviewer='tester', storage_root=self.root)
        with self.assertRaises(OSError):
            start()
        ledger.unlink()
        ledger.write_text('preserve me')
        ledger.chmod(0o644)
        with self.assertRaises(ValueError):
            start()
        self.assertEqual(ledger.read_text(), 'preserve me')
        ledger.chmod(0o600)
        os.link(ledger, self.root / 'hardlink')
        with self.assertRaises(ValueError):
            start()
        ledger.unlink()
        os.mkfifo(ledger, 0o600)
        with self.assertRaises(ValueError):
            start()

    def test_private_root_rejects_symlink_and_shared_directory(self):
        link = self.root / 'linked-root'
        link.symlink_to(self.root, target_is_directory=True)
        def open_private(root):
            with private_file('reviews', root=root, create=True):
                pass
        with self.assertRaises(OSError):
            open_private(link)
        shared = self.root / 'shared'
        shared.mkdir(mode=0o755)
        with self.assertRaises(ValueError):
            open_private(shared)
        self.assertFalse((shared / 'reviews.jsonl').exists())

    def test_review_interval_requires_operator_start_and_decision(self):
        path = self.root / 'reviews.jsonl'
        now = datetime(2026, 1, 1, tzinfo=timezone.utc)
        with self.assertRaises(ValueError):
            record('stop', 'r', decision='approved', evidence_ref='test', now=now, storage_root=self.root)
        record('start', 'r', condition='arkism', candidate_id='candidate', reviewer='tester', now=now, storage_root=self.root)
        with self.assertRaises(ValueError):
            record('start', 'r', condition='arkism', candidate_id='candidate', reviewer='tester', now=now, storage_root=self.root)
        backwards = now-timedelta(seconds=1)
        with self.assertRaises(ValueError):
            record('stop', 'r', decision='approved', evidence_ref='test', now=backwards, storage_root=self.root)
        with self.assertRaises(ValueError):
            record('stop', 'r', decision='approved', now=now, storage_root=self.root)
        event = record('stop', 'r', decision='rejected', evidence_ref='test', now=now+timedelta(seconds=12), storage_root=self.root)
        self.assertEqual(event['review_interval_seconds'], 12)
        self.assertIsNone(event['active_attention_seconds'])
        self.assertEqual(len(path.read_text().splitlines()), 2)
        later = now+timedelta(seconds=15)
        with self.assertRaises(ValueError):
            record('stop', 'r', decision='approved', evidence_ref='test', now=later, storage_root=self.root)


if __name__ == '__main__':
    unittest.main()
