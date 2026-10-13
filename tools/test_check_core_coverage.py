"""핵심 커버리지 검사의 경계값과 손상된 입력 거부를 검증한다."""
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import check_core_coverage as coverage


class CoreCoverageTests(unittest.TestCase):
    def profile(self, covered=9, uncovered=1, mode="set"):
        lines = [f"mode: {mode}"]
        for target in coverage.TARGETS:
            filename = "github.com/FelixBaek/arkism/" + target
            lines.extend((f"{filename}:1.1,2.2 {covered} 1",
                          f"{filename}:3.1,4.2 {uncovered} 0"))
        return "\n".join(lines) + "\n"

    def run_checker(self, text):
        with tempfile.TemporaryDirectory() as directory:
            profile = Path(directory) / "coverage.out"
            profile.write_text(text, encoding="utf-8")
            return subprocess.run(
                [sys.executable, coverage.__file__, str(profile)],
                capture_output=True, text=True, check=False,
            )

    def test_exact_threshold_in_all_modes(self):
        # Go의 세 가지 mode 모두 정확히 90%인 파일을 통과시킨다.
        for mode in ("set", "count", "atomic"):
            with self.subTest(mode=mode):
                result = self.run_checker(self.profile(mode=mode))
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout.count("9/10 (90.00%) — 통과"), 3)

    def test_single_file_just_below_threshold_fails(self):
        # 다른 파일이 90%여도 한 파일이 바로 미달하면 전체 검사가 실패한다.
        profile = self.profile().replace("tei.go:1.1,2.2 9 1", "tei.go:1.1,2.2 899 1")
        profile = profile.replace("tei.go:3.1,4.2 1 0", "tei.go:3.1,4.2 101 0")
        result = self.run_checker(profile)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("899/1000 (89.90%) — 90% 미달", result.stdout)
        self.assertEqual(result.stdout.count("— 통과"), 2)

    def test_missing_target_fails(self):
        # 검사 대상 파일이 하나라도 빠지면 나머지 수치와 무관하게 거부한다.
        profile = "\n".join(line for line in self.profile().splitlines() if "tei.go:" not in line)
        with self.assertRaisesRegex(ValueError, "tei.go: coverage가 누락"):
            coverage.parse_profile(profile)

    def test_empty_and_zero_statement_profiles_fail(self):
        # 빈 파일·헤더만 있는 파일·분모가 0인 파일을 성공으로 처리하지 않는다.
        for profile in ("", "mode: set\n", self.profile(covered=0, uncovered=0)):
            with self.subTest(profile=profile):
                self.assertNotEqual(self.run_checker(profile).returncode, 0)

    def test_malformed_lines_fail(self):
        # 잘못된 mode·행·수치·좌표는 검사 대상 밖의 파일이라도 거부한다.
        for bad in ("mode: unknown\n", "unparseable\n", "other.go:1.1,2.2 -1 0\n",
                    "other.go:0.1,2.2 1 0\n", "other.go:3.1,2.2 1 0\n",
                    "other.go:1.1,2.2 1 x\n", "\n"):
            with self.subTest(bad=bad):
                with self.assertRaises(ValueError):
                    coverage.parse_profile(self.profile() + bad)
        with self.assertRaises(ValueError):
            coverage.parse_profile(self.profile(mode="unknown"))

    def test_duplicate_blocks_fail(self):
        # 동일 블록은 count·문장 수가 같거나 달라도 중복 합산하지 않고 거부한다.
        block = self.profile().splitlines()[1]
        for duplicate in (block, block.replace(" 9 1", " 9 0"), block.replace(" 9 1", " 10 1")):
            with self.subTest(duplicate=duplicate):
                with self.assertRaisesRegex(ValueError, "중복 coverage 블록"):
                    coverage.parse_profile(self.profile() + duplicate + "\n")

    def test_execution_count_is_not_statement_weight(self):
        # 블록 실행 횟수가 커져도 문장 수와 커버리지 비율은 변하지 않는다.
        profile = self.profile(mode="count").replace(" 9 1\n", " 9 999999999999999999999\n")
        totals = coverage.parse_profile(profile + "other.go:1.1,2.2 1000 0\n")
        self.assertEqual(totals, {target: [9, 10] for target in coverage.TARGETS})

    def test_cli_usage_and_missing_file_fail(self):
        # CLI 인자 누락·과다·존재하지 않는 파일은 읽기 쉬운 오류와 실패 코드를 낸다.
        with tempfile.TemporaryDirectory() as directory:
            missing = str(Path(directory) / "missing.out")
            for args in ([], [missing, missing], [missing]):
                with self.subTest(args=args):
                    result = subprocess.run([sys.executable, coverage.__file__, *args],
                                            capture_output=True, text=True, check=False)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertTrue(result.stderr)
                    self.assertNotIn("Traceback", result.stderr)


if __name__ == "__main__":
    unittest.main()
