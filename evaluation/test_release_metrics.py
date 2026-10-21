import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import release_metrics as rm

RAW = {
    'compression': [{'transcript_bytes': 1000, 'transcript_tokens': 250, 'memory_bytes': 50, 'memory_tokens': 10},
                    {'transcript_bytes': 1000, 'transcript_tokens': 250, 'memory_bytes': 150, 'memory_tokens': 40}],
    'injection': [{'recall_tokens': 60, 'static_tokens': 100, 'relevant_hit': True, 'distractor_hits': 0},
                  {'recall_tokens': 90, 'static_tokens': 100, 'relevant_hit': False, 'distractor_hits': 1}],
    'latency_ms': [float(i) for i in range(1, 101)],
}


class ReleaseMetricsTest(unittest.TestCase):
    def test_summary_pools_tasks_and_uses_nearest_rank(self):
        s = rm.summarize(RAW)
        self.assertEqual(s['compression']['ratio_bytes'], 10.0)
        self.assertEqual(s['compression']['ratio_tokens'], 10.0)
        self.assertEqual(s['hook_latency_ms'], {'n': 100, 'p50': 51.0, 'p95': 95.0, 'max': 100.0})
        tok = s['prompt_tokens']
        self.assertEqual((tok['recall_mean'], tok['static_mean'], tok['savings_vs_static']), (75.0, 100.0, 0.25))
        self.assertEqual((tok['relevant_hit_rate'], tok['distractor_hits']), (0.5, 1))

    def test_empty_raw_is_rejected(self):
        with self.assertRaises(ValueError):
            rm.summarize({**RAW, 'latency_ms': []})

    def test_readme_table_is_replaced_in_date_order(self):
        s = rm.summarize(RAW)
        results = [{'version': 'v0.1.5', 'date': '2026-12-01', 'commit': 'b' * 40, 'summary': s},
                   {'version': 'v0.1.4', 'date': '2026-11-01', 'commit': 'a' * 40, 'summary': s}]
        with tempfile.TemporaryDirectory() as tmp:
            readme = Path(tmp) / 'README.md'
            readme.write_text(f'intro\n{rm.START}\nold\n{rm.END}\nnotes\n')
            rm.update_readme(readme, results)
            text = readme.read_text()
        self.assertNotIn('old', text)
        self.assertTrue(text.startswith('intro\n') and text.endswith('\nnotes\n'))
        self.assertLess(text.index('v0.1.4'), text.index('v0.1.5'))
        self.assertIn('| 10.0x | 51.0 / 95.0 / 100.0 | 75.0 (100.0) | 25.0% |', text)


if __name__ == '__main__':
    unittest.main()
