import unittest

import check_work_record as w

GOOD = """Automation: steps in plain words

Why: users wrote variables by hand and got lost.
Verified: node --test desktop/ui/schedule-words.test.js passed; screenshot shots/auto.png
Limits: channel select not screenshotted (not run).

제목: 자동화 단계를 평소 말로 적기
이유: 사용자가 변수를 직접 적다가 헤맸다.
검증: node --test desktop/ui/schedule-words.test.js 통과
"""


def exists(*paths):
    return lambda p: p in paths


class CommitRecordTest(unittest.TestCase):
    def test_a_full_record_passes(self):
        self.assertEqual(w.check_commit("a" * 40, GOOD, ["desktop/ui/crew.js"],
                                        exists("desktop/ui/schedule-words.test.js", "shots/auto.png")), [])

    def test_missing_why_and_verified_are_named(self):
        problems = w.check_commit("b" * 40, "Fix popup\n\nMoved the button.\n", ["desktop/ui/crew.js"], exists())
        self.assertEqual([p.split(" ", 1)[1] for p in problems],
                         ["missing Why:", "missing Verified:", "missing 제목:", "missing 이유:", "missing 검증:"])

    def test_english_only_is_not_enough(self):
        english = GOOD.split("\n\n제목:")[0]
        problems = w.check_commit("f" * 40, english, [], exists("desktop/ui/schedule-words.test.js", "shots/auto.png"))
        self.assertEqual([p.split(" ", 1)[1] for p in problems], ["missing 제목:", "missing 이유:", "missing 검증:"])

    def test_study_facing_screens_need_task_and_research(self):
        problems = w.check_commit("c" * 40, GOOD, ["desktop/ui/memory-time.js"],
                                  exists("desktop/ui/schedule-words.test.js", "shots/auto.png"))
        self.assertEqual(len(problems), 2)
        ok = GOOD + "Task: PSY-02\nResearch: none (layout only)\n"
        self.assertEqual(w.check_commit("c" * 40, ok, ["desktop/ui/memory-time.js"],
                                        exists("desktop/ui/schedule-words.test.js", "shots/auto.png")), [])

    def test_not_run_called_passed_and_missing_evidence_are_caught(self):
        msg = "X\n\nWhy: y\nVerified: see evaluations/run-9/result.json\n\n제목: x\n이유: y\n검증: go test 미실행, 통과\n"
        problems = w.check_commit("d" * 40, msg, [], exists())
        self.assertTrue(any("not run is also called passed" in p for p in problems))
        self.assertTrue(any("evaluations/run-9/result.json" in p for p in problems))

    def test_one_line_may_hold_a_passed_check_and_a_not_run_one(self):
        self.assertEqual(w.verified_problems("go vet passed; node tests not run (no node)", exists()), [])
        self.assertEqual(w.verified_problems("go test 통과, 스크린샷 미실행", exists()), [])
        self.assertEqual(len(w.verified_problems("node tests not run · passed", exists())), 1)

    def test_korean_task_and_research_keys_count(self):
        ok = GOOD + "작업: PSY-02\n연구: 해당 없음\n"
        self.assertEqual(w.check_commit("e" * 40, ok, ["desktop/ui/memory-time.js"],
                                        exists("desktop/ui/schedule-words.test.js", "shots/auto.png")), [])


class ReportTest(unittest.TestCase):
    REPORT = "작업 ID / 단계: R4\n실행한 검증 명령 / 결과: go test ./internal/memory 통과\n" \
             "실패 / unknown / 미실행과 이유: 없음\n커밋 상태: abc1234\n"

    def test_a_report_naming_a_real_commit_passes(self):
        self.assertEqual(w.check_report(self.REPORT, lambda sha: sha == "abc1234", exists()), [])

    def test_missing_fields_and_foreign_commit(self):
        problems = w.check_report("작업 ID: R4\n커밋 상태: deadbeef\n", lambda sha: False, exists())
        self.assertIn("report is missing 실행한 검증", problems)
        self.assertIn("report names a commit this repository does not have: deadbeef", problems)


class InputTest(unittest.TestCase):
    def test_range_and_git_arguments(self):
        self.assertEqual(w.safe_range("origin/main..HEAD"), "origin/main..HEAD")
        for bad in ("-x..y", "a..b..c", "HEAD", "a..--output=x"):
            with self.assertRaises(SystemExit):
                w.safe_range(bad)
        with self.assertRaises(ValueError):
            w.git("log", "--output=/tmp/x")

    def test_evidence_paths(self):
        self.assertEqual(list(w.evidence_paths("ran `tools/a.py`, see https://x.io/a/b.md and v1.2/3")),
                         ["tools/a.py"])
        self.assertEqual(list(w.evidence_paths("Ran python3 tools/check_local.py. See desktop/ui/app.js:120.")),
                         ["tools/check_local.py", "desktop/ui/app.js"])
        self.assertEqual(list(w.evidence_paths("release notes for origin/main..HEAD; ran it")), [])

    def test_an_unknown_range_head_fails_instead_of_skipping(self):
        with self.assertRaises(SystemExit):
            w.check_range("HEAD..no-such-ref-here")


if __name__ == "__main__":
    unittest.main()
