import json
import pathlib
import re
import subprocess
import unittest
from unittest.mock import patch

import run as benchmark
import summarize


class JudgeHarnessTests(unittest.TestCase):
    def test_zero_exit_without_completion_is_not_success(self):
        calls = []
        def execute(command, **kwargs):
            calls.append(command)
            return subprocess.CompletedProcess(command, 0, '', '')
        with patch.object(benchmark.subprocess, 'run', side_effect=execute):
            success, diagnostic = benchmark.evaluate('import sys; sys.exit(0)', 'assert False')
        self.assertFalse(success)
        self.assertIn('before completing', diagnostic)
        name = calls[0][calls[0].index('--name')+1]
        self.assertEqual(calls[1], ['docker', 'rm', '--force', name])

    def test_completion_and_zero_exit_are_both_required(self):
        for status, expected in ((0, True), (1, False)):
            def execute(command, status=status, **kwargs):
                if command[1] == 'rm':
                    return subprocess.CompletedProcess(command, 1, '', 'No such container')
                marker = re.search(r'ARKISM_JUDGE_COMPLETED_[a-f0-9]+', command[-1]).group()
                return subprocess.CompletedProcess(command, status, marker+'\n', '')
            with patch.object(benchmark.subprocess, 'run', side_effect=execute):
                success, _ = benchmark.evaluate('value=1', 'assert value == 1')
            self.assertEqual(success, expected)

    def test_timeout_removes_only_its_unique_container(self):
        names = []
        def execute(command, **kwargs):
            if command[1] == 'run':
                name = command[command.index('--name')+1]
                names.append(name)
                raise subprocess.TimeoutExpired(command, kwargs['timeout'])
            self.assertEqual(command, ['docker', 'rm', '--force', names[-1]])
            return subprocess.CompletedProcess(command, 0, '', '')
        with patch.object(benchmark.subprocess, 'run', side_effect=execute):
            for _ in range(2):
                with self.assertRaises(subprocess.TimeoutExpired):
                    benchmark.evaluate('while True: pass', 'assert True', timeout=0.1)
        self.assertEqual(len(set(names)), 2)

    def test_cleanup_failure_is_visible(self):
        def execute(command, **kwargs):
            return subprocess.CompletedProcess(command, 1, '', 'daemon unavailable')
        with patch.object(benchmark.subprocess, 'run', side_effect=execute), self.assertRaises(RuntimeError):
            benchmark.evaluate('pass', 'assert True')

    def test_summary_retains_unknown_tokens_and_coverage(self):
        rows = [{'input_tokens': 100}, {'input_tokens': None}]
        self.assertEqual(summarize.measured_total(rows, 'input_tokens'), '미측정 (1/2 관측)')
        self.assertEqual(summarize.measured_total([{'input_tokens': 0}], 'input_tokens'), '0 (1/1 관측)')
        self.assertEqual(summarize.measured_total([], 'input_tokens'), '미측정 (0/0 관측)')
        data = json.loads((pathlib.Path(__file__).parent / 'results/results.json').read_text())
        data[0]['input_tokens'] = None
        report = summarize.render_report(data)
        self.assertIn('미측정 (14/15 관측)', report)
        self.assertIn('미측정', report)


if __name__ == '__main__':
    unittest.main()
