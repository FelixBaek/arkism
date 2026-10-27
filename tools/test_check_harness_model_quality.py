"""하네스 품질 근거 파일의 일관성 검사와 판정 함수를 오프라인으로 검증한다."""
import copy
import json
import unittest

import check_harness_model_quality as quality


def result(model, passed=True, **flags):
    assertions = {name: flags.get(name, passed) for name in quality.ASSERTIONS}
    return {"model": model, "passed": all(assertions.values()), "assertions": assertions, "prompt_version": "p1"}


def gate(initial, heldout):
    """{category: [results]}를 두 suite로 묶은 근거 파일. 요약 칸은 행에서 계산한다."""
    g = {"schema_version": "s1", "prompt_version": "p1", "model_discovery": {"a": True, "b": True},
         "cases": [{"category": c, "results": r} for c, r in initial.items()],
         "heldout": {"cases": [{"category": c, "results": r} for c, r in heldout.items()]}}
    g["initial_passed_categories"] = quality.passed_cases(g)
    g["heldout"]["passed_categories"] = quality.passed_cases(g["heldout"])
    g["passed_categories"] = quality.derive_passed(g)
    return g


class DeriveTests(unittest.TestCase):
    def test_category_needs_initial_and_both_heldout_variants(self):
        g = gate({"x": [result("a"), result("b")], "y": [result("a"), result("b")], "z": [result("a", False), result("b")]},
                 {"x_variant2": [result("a")], "x_variant3": [result("a")], "y_variant2": [result("a", False)], "y_variant3": [result("a")],
                  "z_variant2": [result("a")], "z_variant3": [result("a")]})
        self.assertEqual(g["initial_passed_categories"], ["x", "y"])
        self.assertEqual(g["passed_categories"], ["x"])
        self.assertEqual(quality.problems(g), [])

    def test_summary_that_disagrees_with_rows_is_reported(self):
        g = gate({"x": [result("a")]}, {"x_variant2": [result("a")], "x_variant3": [result("a")]})
        g["passed_categories"] = ["x", "ghost"]
        g["cases"][0]["results"][0]["passed"] = False  # row says failed, assertions say passed
        found = quality.problems(g)
        self.assertEqual(len(found), 3, found)  # row mismatch, initial summary, passed_categories
        self.assertTrue(any("passed=False" in p for p in found))

    def test_unknown_model_and_foreign_prompt_version_are_reported(self):
        g = gate({"x": [result("c")]}, {})
        g["cases"][0]["results"][0]["prompt_version"] = "p0"
        found = quality.problems(g)
        self.assertTrue(any("model_discovery" in p for p in found))
        self.assertTrue(any("prompt_version" in p for p in found))

    def test_status_names_the_version_that_blocks_the_gate(self):
        g = gate({"x": [result("a")]}, {"x_variant2": [result("a")], "x_variant3": [result("a")]})
        self.assertEqual(quality.gate_status(g, "s1", "p1"), "active for ['x']")
        self.assertIn("prompt_version 'p1' != host 'p2'", quality.gate_status(g, "s1", "p2"))
        self.assertIn("schema_version", quality.gate_status(g, "s0", "p1"))


class AssessTests(unittest.TestCase):
    fixture = {"expect": ["한국어"], "in_progress": ["테스트"], "unverified": ["테스트"]}

    def test_correct_output_passes_every_assertion(self):
        output = {"session_state": {"completed": ["보고 언어 결정"], "remaining": ["테스트 실행"]},
                  "memory_candidates": [{"statement": "보고는 한국어로 쓴다", "source_refs": [{"source_id": "s1"}]}]}
        self.assertEqual(quality.assess(output, self.fixture), dict.fromkeys(quality.ASSERTIONS, True))

    def test_unobserved_completion_and_test_pass_fail(self):
        output = {"session_state": {"completed": ["테스트 실행"], "checks": ["테스트 통과"]},
                  "memory_candidates": [{"statement": "보고는 영어로 쓴다"}]}
        self.assertEqual(quality.assess(output, self.fixture), dict.fromkeys(quality.ASSERTIONS, False))

    def test_unparsable_output_fails_semantic_case_only(self):
        self.assertEqual(quality.assess({}, self.fixture), {"semantic_case": False, "no_unobserved_completion": True, "no_unobserved_test_pass": True})


class RepositoryFileTests(unittest.TestCase):
    def test_committed_evidence_is_consistent(self):
        g = json.loads(quality.QUALITY.read_text(encoding="utf-8"))
        self.assertEqual(quality.problems(g), [])
        self.assertEqual(sorted(g["passed_categories"]), quality.derive_passed(copy.deepcopy(g)))

    def test_host_versions_are_read_from_source(self):
        schema, prompt = quality.host_versions()
        self.assertTrue(schema.startswith("session-state-"))
        self.assertTrue(prompt.startswith("harness-session-"))

    def test_fixtures_cover_initial_and_heldout_variants(self):
        for category, fixtures in quality.FIXTURES.items():
            self.assertEqual(len(fixtures), 1 + len(quality.HELDOUT_VARIANTS), category)
            for f in fixtures:
                self.assertTrue(f["expect"] and f["in_progress"] and f["unverified"], category)

    def test_check_exits_zero_on_committed_file(self):
        self.assertEqual(quality.main([]), 0)


if __name__ == "__main__":
    unittest.main()
