"""Go 핵심 파일 세 개의 문장 커버리지가 각각 90% 이상인지 확인한다."""
import re
import sys
from pathlib import Path


TARGETS = (
    "internal/memory/tei.go",
    "internal/memory/candidate_quota.go",
    "internal/memory/context_budget.go",
)
BLOCK = re.compile(r"(.+):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)")


def parse_profile(text):
    lines = text.splitlines()
    if not lines or lines[0] not in ("mode: set", "mode: count", "mode: atomic"):
        raise ValueError("Go coverage mode가 없거나 올바르지 않습니다")
    totals = {target: [0, 0] for target in TARGETS}
    seen = set()
    for number, line in enumerate(lines[1:], 2):
        match = BLOCK.fullmatch(line)
        if not match:
            raise ValueError(f"{number}행: 잘못된 coverage 블록")
        filename = match[1]
        start_line, start_col, end_line, end_col, statements, count = map(int, match.groups()[1:])
        if min(start_line, start_col, end_line, end_col) < 1 or (end_line, end_col) < (start_line, start_col):
            raise ValueError(f"{number}행: 잘못된 소스 위치")
        key = (filename, start_line, start_col, end_line, end_col)
        if key in seen:
            raise ValueError(f"{number}행: 중복 coverage 블록")
        seen.add(key)
        for target in TARGETS:
            if filename == target or filename.endswith("/" + target):
                totals[target][0] += statements if count else 0
                totals[target][1] += statements
    for target, (_, total) in totals.items():
        if not total:
            raise ValueError(f"{target}: coverage가 누락되었거나 문장이 없습니다")
    return totals


def main():
    if len(sys.argv) != 2:
        print("사용법: python3 tools/check_core_coverage.py COVERPROFILE", file=sys.stderr)
        return 1
    try:
        totals = parse_profile(Path(sys.argv[1]).read_text(encoding="utf-8"))
    except (OSError, ValueError) as error:  # UnicodeError is a ValueError
        print(f"실패: {error}", file=sys.stderr)
        return 1
    passed = True
    for target, (covered, total) in totals.items():
        meets_threshold = covered * 100 >= total * 90
        print(f"{target}: {covered}/{total} ({covered / total:.2%}) — "
              f"{'통과' if meets_threshold else '90% 미달'}")
        passed = passed and meets_threshold
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
