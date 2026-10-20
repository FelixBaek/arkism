#!/usr/bin/env python3
"""Write bilingual (Korean / English) release notes for a commit range, grouped by kind of change.

Research changes come first (a commit whose Research: is not none), then the area that most of a commit's files
belong to. Each entry shows the Korean subject (제목:) and the English subject; commits from before the bilingual
record show only the English one.
"""
import argparse
import re
import sys
from collections import Counter

from check_work_record import ROOT, git, safe_range, sections

GROUPS = [  # (key, Korean title, English title, path pattern)
    ("research", "연구 변경", "Research changes", None),
    ("app", "앱 화면", "App", re.compile(r"^desktop/(ui|src-tauri)/")),
    ("engine", "엔진·서버", "Engine & server", re.compile(r"^(internal|cmd|api|config)/")),
    ("build", "빌드·배포·도구", "Build, release & tools", re.compile(r"^(?:(?:\.github|tools|desktop/installer)/.*|desktop/[^/]+\.sh)$")),
    ("docs", "문서", "Docs", re.compile(r"^(?:guide/.*|.*\.md)$")),
    ("other", "기타", "Other", None),
]
NONE = re.compile(r"^\s*(none|해당 ?없음|없음)\b", re.I)


def group_of(found, files):
    research = found.get("research", "")
    if research and not NONE.match(research):
        return "research"
    areas = Counter(next((k for k, _, _, rx in GROUPS if rx and rx.search(f)), "other") for f in files)
    return areas.most_common(1)[0][0] if areas else "other"


def entry(sha, subject, found):
    ko = found.get("title_ko", "").splitlines()[0] if found.get("title_ko") else ""
    tags = ", ".join(x for x in (found.get("task", "").splitlines() or [""])[:1] if x and not NONE.match(x))
    head = f"- **{ko}** · {subject}" if ko else f"- {subject}"
    return head + (f" ({tags})" if tags else "") + f" `{sha[:7]}`"


def notes(commits, title):
    """commits: [(sha, message, files)] -> markdown."""
    grouped = {k: [] for k, *_ in GROUPS}
    for sha, message, files in commits:
        found = sections(message)
        grouped[group_of(found, files)].append(entry(sha, message.splitlines()[0].strip(), found))
    out = [f"# {title}", ""]
    for key, ko, en, _ in GROUPS:
        if grouped[key]:
            out += [f"## {ko} · {en}", "", *grouped[key], ""]
    if len(out) == 2:
        out += ["바뀐 점 없음 · No changes", ""]
    return "\n".join(out)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--range", required=True, help="e.g. v0.1.2..v0.1.3")
    ap.add_argument("--title", default="Arkism")
    args = ap.parse_args(argv)
    log = git("log", "--no-merges", "--reverse", "--format=%H%x00%B%x00", "--end-of-options", safe_range(args.range)).split("\0")
    commits = []
    for sha, message in zip(log[0::2], log[1::2]):
        sha = sha.strip()
        if sha:
            commits.append((sha, message.strip(), git("diff-tree", "--no-commit-id", "--name-only", "-r", "--end-of-options", sha).split()))
    sys.stdout.write(notes(commits, args.title))
    return 0


if __name__ == "__main__":
    sys.exit(main())
