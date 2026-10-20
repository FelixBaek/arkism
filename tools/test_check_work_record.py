import unittest

import check_work_record as w

GOOD = """Automation: steps in plain words

Why: users wrote variables by hand and got lost.
Verified: node --test desktop/ui/schedule-words.test.js passed; screenshot shots/auto.png
Limits: channel select not screenshotted (not run).
"""


def exists(*paths):
    return lambda p: p in paths


class CommitRecordTest(unittest.TestCase):
    def test_a_full_record_passes(self):
        self.assertEqual(w.check_commit("a" * 40, GOOD, ["desktop/ui/crew.js"],
                                        exists("desktop/ui/schedule-words.test.js", "shots/auto.png")), [])

    def test_missing_why_and_verified_are_named(self):
        problems = w.check_commit("b" * 40, "Fix popup\n\nMoved the button.\n", ["desktop/ui/crew.js"], exists())
        self.assertEqual([p.split(" ", 1)[1] for p in problems], ["missing Why:", "missing Verified:"])

    def test_study_facing_screens_need_task_and_research(self):
        problems = w.check_commit("c" * 40, GOOD, ["desktop/ui/memory-time.js"],
                                  exists("desktop/ui/schedule-words.test.js", "shots/auto.png"))
        self.assertEqual(len(problems), 2)
        ok = GOOD + "Task: PSY-02\nResearch: none (layout only)\n"
        self.assertEqual(w.check_commit("c" * 40, ok, ["desktop/ui/memory-time.js"],
                                        exists("desktop/ui/schedule-words.test.js", "shots/auto.png")), [])

    def test_not_run_called_passed_and_missing_evidence_are_caught(self):
        msg = "X\n\nWhy: y\nVerified: go test ./... not run, passed\nsee evaluations/run-9/result.json\n"
        problems = w.check_commit("d" * 40, msg, [], exists())
        self.assertTrue(any("not run is also called passed" in p for p in problems))
        self.assertTrue(any("evaluations/run-9/result.json" in p for p in problems))

    def test_korean_keys_count(self):
        msg = "X\n\n이유: 버튼이 안 눌림\n검증: 화면 확인 통과\n"
        self.assertEqual(w.check_commit("e" * 40, msg, [], exists()), [])


class ReportTest(unittest.TestCase):
    REPORT = "작업 ID / 단계: R4\n실행한 검증 명령 / 결과: go test ./internal/memory 통과\n" \
             "실패 / unknown / 미실행과 이유: 없음\n커밋 상태: abc1234\n"

    def test_a_report_naming_a_real_commit_passes(self):
        self.assertEqual(w.check_report(self.REPORT, lambda sha: sha == "abc1234", exists()), [])

    def test_missing_fields_and_foreign_commit(self):
        problems = w.check_report("작업 ID: R4\n커밋 상태: deadbeef\n", lambda sha: False, exists())
        self.assertIn("report is missing 실행한 검증", problems)
        self.assertIn("report names a commit this repository does not have: deadbeef", problems)


if __name__ == "__main__":
    unittest.main()
