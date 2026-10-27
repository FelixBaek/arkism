"""The merge-threshold sweep must match the frozen set it was derived from and the gate's default."""
import json
import re
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent
sys.path.insert(0, str(ROOT / "merge-threshold-v1"))
import sweep  # noqa: E402


class MergeThresholdSweep(unittest.TestCase):
    def test_pairs_are_rebuilt_from_the_frozen_dataset(self):
        dataset = json.loads((sweep.FROZEN / "dataset.json").read_text())
        pairs = json.loads((ROOT / "merge-threshold-v1" / "pairs.json").read_text())
        self.assertEqual(pairs["source_dataset_sha256"], sweep.sha(sweep.FROZEN / "dataset.json"))
        self.assertEqual(pairs["pairs"], sweep.build_pairs(dataset))
        self.assertEqual(sum(p["same"] for p in pairs["pairs"]), 72)

    def test_default_matches_server_constant(self):
        go = (ROOT.parent / "internal" / "memory" / "capture_merge_gate.go").read_text()
        self.assertEqual(float(re.search(r"defaultCaptureMergeSimilarity = ([\d.]+)", go).group(1)), sweep.DEFAULT)

    def test_metrics_on_a_tiny_case(self):
        scored = [{"query_id": "a", "same": True, "cosine": 0.95}, {"query_id": "a", "same": False, "cosine": 0.93},
                  {"query_id": "b", "same": False, "cosine": 0.94}, {"query_id": "c", "same": True, "cosine": 0.5}]
        row = sweep.sweep(scored, [0.92])[0]
        self.assertEqual((row["pair"]["tp"], row["pair"]["predicted"], row["pair"]["positives"]), (1, 3, 2))
        self.assertEqual((row["gate"]["tp"], row["gate"]["predicted"], row["gate"]["positives"]), (1, 2, 2))
        self.assertEqual(row["gate"]["f1"], 0.5)

    def test_recorded_results_reproduce(self):
        results = json.loads((ROOT / "merge-threshold-v1" / "results.json").read_text())
        pairs = json.loads((ROOT / "merge-threshold-v1" / "pairs.json").read_text())["pairs"]
        scored = sweep.score(pairs, json.loads((sweep.FROZEN / "model_vectors.json").read_text()))
        self.assertEqual(json.loads(json.dumps(sweep.sweep(scored, sweep.THRESHOLDS))), results["sweep"])


if __name__ == "__main__":
    unittest.main()
