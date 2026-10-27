"""사람 라벨 회상 세트 채점(recall@k·MRR·정밀도)을 고정 입력으로 검증한다."""
import unittest

import evaluate_semantic_recall as recall


LABELS = [
    {"id": "a", "expected_memory_ids": ["m1"]},          # rank 1
    {"id": "b", "expected_memory_ids": ["m2", "m3"]},    # m3 at rank 3, m2 never
    {"id": "c", "expected_memory_ids": ["m9"]},          # never returned
    {"id": "d", "expected_memory_ids": []},              # negative, something returned
    {"id": "e", "expected_memory_ids": []},              # negative, nothing returned
    {"id": "f", "expected_memory_ids": ["m1"]},          # no run row: unscored
]
RUNS = [
    {"id": "a", "returned": ["m1", "m4", "m1"]},
    {"id": "b", "returned": ["m4", "m5", "m3"]},
    {"id": "c", "returned": ["m4"]},
    {"id": "d", "returned": ["m4"]},
    {"id": "e", "returned": []},
]


class LabeledMetricsTests(unittest.TestCase):
    def test_counts_and_mrr(self):
        out = recall.labeled_metrics(LABELS, RUNS, ks=(1, 3))
        self.assertEqual((out["labeled"], out["scored"], out["unscored"], out["positives"], out["negatives"]), (6, 5, 1, 3, 2))
        self.assertAlmostEqual(out["mrr"], (1 + 1 / 3 + 0) / 3)

    def test_at_k(self):
        out = recall.labeled_metrics(LABELS, RUNS, ks=(1, 3))["at_k"]
        self.assertAlmostEqual(out["1"]["recall"], 1 / 3)
        self.assertAlmostEqual(out["3"]["recall"], 2 / 3)
        self.assertAlmostEqual(out["3"]["item_recall"], 2 / 4)
        # precision@3 over queries with a return: a 1/2 (duplicate m1 counted once), b 1/3, c 0, d 0
        self.assertAlmostEqual(out["3"]["precision"], (1 / 2 + 1 / 3 + 0 + 0) / 4)
        self.assertAlmostEqual(out["3"]["negative_returned"], 1 / 2)

    def test_empty_sets_give_none_not_division_errors(self):
        out = recall.labeled_metrics([], [], ks=(1,))
        self.assertIsNone(out["mrr"])
        self.assertEqual(out["at_k"]["1"], {"recall": None, "item_recall": None, "precision": None, "negative_returned": None})


if __name__ == "__main__":
    unittest.main()
