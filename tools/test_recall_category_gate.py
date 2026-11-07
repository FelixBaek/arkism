import copy
import unittest
from check_recall_category_gate import CATEGORIES, compare


def fixture():
    return {c: {"key": c, "category": c, "question": "fixed question " + c,
                "evidence": ["a", "b"], "@10": {"items": [{"dia": ["a", "b"]}],
                "conversation": [], "degraded": []}} for c in CATEGORIES}


class RecallGateTest(unittest.TestCase):
    def test_all_categories_required(self):
        base = fixture()
        self.assertTrue(compare(base, copy.deepcopy(base))["passed"])
        bad = copy.deepcopy(base)
        bad["multi-hop"]["@10"]["items"] = [{"dia": ["a"]}]
        report = compare(base, bad)
        self.assertFalse(report["passed"])
        self.assertEqual(report["categories"]["multi-hop"]["candidate"], .5)

    def test_labels_questions_and_categories_cannot_change(self):
        for field, value in (("evidence", ["a"]), ("question", "easier"), ("category", "temporal")):
            base, changed = fixture(), fixture()
            changed["multi-hop"][field] = value
            with self.assertRaises(ValueError): compare(base, changed)

    def test_no_dropped_rows_or_missing_category(self):
        base, changed = fixture(), fixture()
        del changed["open-domain"]
        with self.assertRaises(ValueError): compare(base, changed)
        with self.assertRaises(ValueError): compare(changed, changed)

    def test_degradation_and_budget_fail_closed(self):
        base, changed = fixture(), fixture()
        changed["temporal"]["@10"]["degraded"] = ["vector"]
        self.assertFalse(compare(base, changed)["passed"])
        changed["temporal"]["@10"]["items"] *= 11
        with self.assertRaises(ValueError): compare(base, changed)

    def test_stronger_baseline_cannot_regress_or_lower_target(self):
        base = fixture()
        for threshold in (.89, float("nan"), float("inf")):
            with self.assertRaises(ValueError): compare(base, base, threshold)
        self.assertEqual(compare(base, base)["unscored"], 0)
        base = fixture()
        for row in base.values():
            row["evidence"] = [str(i) for i in range(10)]
            row["@10"]["items"] = [{"dia": row["evidence"][:]}]
        changed = copy.deepcopy(base)
        changed["single-hop"]["@10"]["items"][0]["dia"].pop()
        self.assertFalse(compare(base, changed)["passed"])


if __name__ == "__main__": unittest.main()
