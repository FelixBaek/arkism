#!/usr/bin/env python3
"""Check the record a change leaves: why, what was verified, and the research link where one is needed.

Commits (default origin/main..HEAD) carry the record twice, in English and in Korean: "Why:" and "Verified:", then
"제목:" (Korean subject), "이유:" and "검증:". Commits that touch what a memory study shows people (memory display,
explanation text, trust display, feedback and correction) also need "Task:" and "Research:" ("Research: none" is
fine). A worker's completion report (--report) needs its task ID, checks, failures
and commit state, and the commit it names must exist.

This proves the record has its form, not that its content is true or that a change works.
"""
import argparse
from datetime import datetime
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
# shortcut: commits authored before the rule began are exempt; replace with a baseline commit once main has one.
RULE_START = "2026-10-21T03:40:00+09:00"
RESEARCH_PATHS = re.compile(r"^desktop/ui/(memory-[\w-]+|inject|overview[\w-]*|reviews|intelligence)\.js$")
SECTIONS = {"Why": "why", "Task": "task", "Verified": "verified", "Limits": "limits", "Research": "research",
            "제목": "title_ko", "이유": "why_ko", "작업": "task", "검증": "verified_ko", "한계": "limits_ko",
            "연구": "research"}
REQUIRED = ("Why", "Verified", "제목", "이유", "검증")
NOT_RUN = re.compile(r"not[ _]run|미실행|skipped|못 ?[했함돌]", re.I)
PASSED = re.compile(r"\bpass(ed)?\b|통과|\bok\b", re.I)
SHA = re.compile(r"\b[0-9a-f]{7,40}\b")
PATH = re.compile(r"[\w.-]+(?:/[\w.-]+)+")  # checked per token, so no backtracking across a line
REPORT_FIELDS = ("작업 ID", "실행한 검증", "미실행", "커밋 상태")


GIT_FLAGS = {"-r", "--no-merges", "--reverse", "--no-commit-id", "--name-only", "--end-of-options",
             "--format=%H%x00%aI%x00%B%x00", "--format=%H%x00%B%x00"}
SAFE_ARGUMENT = re.compile(r"\w[\w./^~@-]*", re.ASCII)


def git(*args):
    """Run Git with arguments that cannot smuggle in an option or a shell word."""
    if any(a not in GIT_FLAGS and not SAFE_ARGUMENT.fullmatch(a) for a in args):
        raise ValueError("invalid Git argument")
    return subprocess.run(["git", "-C", str(ROOT), *args], check=True, capture_output=True, text=True).stdout


def safe_range(text):
    base, sep, head = text.partition("..")
    if not (sep and SAFE_ARGUMENT.fullmatch(base) and SAFE_ARGUMENT.fullmatch(head) and ".." not in head):
        raise SystemExit(f"--range must look like base..head: {text!r}")
    return text


def evidence_paths(line):
    for word in line.split():
        word = word.strip("`'\"()[],;")
        if "://" not in word and PATH.fullmatch(word) and "." in word.rsplit("/", 1)[1].strip("."):
            yield word


def sections(message):
    """Split "Key: text" sections (English or Korean key) out of a commit message body."""
    out, key = {}, None
    for line in message.splitlines()[1:]:
        hit = next(((k, line.split(":", 1)[1]) for name, k in SECTIONS.items()
                    if re.match(rf"\s*{name}\s*:", line)), None)
        if hit:
            key = hit[0]
            out[key] = hit[1].strip()
        elif key:
            out[key] = (out[key] + "\n" + line.strip()).strip()
    return out


def verified_problems(text, exists):
    problems = []
    for line in text.splitlines():
        if NOT_RUN.search(line) and PASSED.search(line):
            problems.append(f"a check marked not run is also called passed: {line.strip()[:80]}")
        for path in evidence_paths(line):
            if not exists(path):
                problems.append(f"evidence path does not exist: {path}")
    return problems


def check_commit(sha, message, files, exists):
    found, problems = sections(message), []
    for name in REQUIRED:
        if not found.get(SECTIONS[name]):
            problems.append(f"missing {name}:")
    if any(RESEARCH_PATHS.match(f) for f in files):
        for name in ("Task", "Research"):
            if not found.get(SECTIONS[name]):
                problems.append(f"touches a study-facing screen; missing {name}: (none is fine)")
    for key in ("verified", "verified_ko"):
        problems += verified_problems(found.get(key, ""), exists)
    return [f"{sha[:9]} {p}" for p in problems]


def check_report(text, commit_exists, exists):
    problems = [f"report is missing {f}" for f in REPORT_FIELDS if f not in text]
    for line in text.splitlines():
        if "커밋 상태" in line:
            for sha in SHA.findall(line):
                if not commit_exists(sha):
                    problems.append(f"report names a commit this repository does not have: {sha}")
    return problems + verified_problems(text, exists)


def commit_has(sha):
    return lambda path: subprocess.run(["git", "-C", str(ROOT), "cat-file", "-e", f"{sha}:{path}"],
                                       capture_output=True).returncode == 0


def commit_exists(sha):
    return subprocess.run(["git", "-C", str(ROOT), "cat-file", "-e", f"{sha}^{{commit}}"],
                          capture_output=True).returncode == 0


def check_range(rng):
    """Problems in the commits of rng authored since RULE_START, or None when the base is not here."""
    base = rng.split("..")[0]
    if subprocess.run(["git", "-C", str(ROOT), "rev-parse", "--verify", "-q", "--end-of-options", base],
                      capture_output=True).returncode:
        print(f"Work record check skipped: {base} is not in this checkout.")
        return None
    problems = []
    start = datetime.fromisoformat(RULE_START)
    log = git("log", "--no-merges", "--format=%H%x00%aI%x00%B%x00", "--end-of-options", rng).split("\0")
    for sha, authored, message in zip(log[0::3], log[1::3], log[2::3]):
        sha = sha.strip()
        if sha and datetime.fromisoformat(authored) >= start:  # author date survives a rebase
            files = git("diff-tree", "--no-commit-id", "--name-only", "-r", "--end-of-options", sha).split()
            problems += check_commit(sha, message, files, commit_has(sha))
    return problems


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--range", default="origin/main..HEAD")
    ap.add_argument("--report", type=pathlib.Path, help="a worker completion report to check instead of commits")
    args = ap.parse_args(argv)
    if args.report:
        report = args.report.resolve()
        if not report.is_file() or not report.is_relative_to(pathlib.Path.cwd().resolve()):
            raise SystemExit("--report must be a file under the current directory")
        problems = check_report(report.read_text(encoding="utf-8"), commit_exists, commit_has("HEAD"))
    else:
        problems = check_range(safe_range(args.range))
        if problems is None:
            return 0
    for p in problems:
        print(p)
    print("Work record check: " + (f"{len(problems)} problem(s)." if problems else "passed."))
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
