"""The outcome-weight replay must reproduce the frozen baseline before it weighs anything."""
import json
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent
sys.path.insert(0, str(ROOT / "recall-rank-signal-v1"))
import replay  # noqa: E402


class OutcomeWeightReplay(unittest.TestCase):
    def test_outcome_factor_matches_server_rule(self):
        self.assertEqual(replay.outcome_factor(0, 0), 1.0)
        self.assertAlmostEqual(replay.outcome_factor(7, 7), 1.15)
        self.assertAlmostEqual(replay.outcome_factor(2, 1), 0.105)
        self.assertAlmostEqual(replay.outcome_factor(1, 0, 2), 0.5)

    def test_replay_without_signals_reproduces_each_exported_baseline(self):
        dataset = json.loads((ROOT / "recall-quality-v1" / "dataset.json").read_text())
        results = json.loads((ROOT / "recall-rank-signal-v1" / "results.json").read_text())
        for name, path in replay.EXPORTS.items():
            export = json.loads(path.read_text())
            metrics, returned = replay.run({}, dataset, export)
            for q in export["queries"]:
                self.assertEqual(returned[q["id"]], q["baseline"], f"{name} {q['id']}")
            self.assertEqual(results[name]["before"], metrics, name)
        self.assertEqual(results["frozen_2026-10-18"]["before"]["holdout"]["wrong_documents"], 87)


if __name__ == "__main__":
    unittest.main()
