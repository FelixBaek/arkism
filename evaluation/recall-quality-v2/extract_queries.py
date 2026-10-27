#!/usr/bin/env python3
"""Pull the owner's own questions out of local coding-session transcripts into a private queries file.

Reads the user turns of Claude Code session logs (~/.claude/projects/*/*.jsonl by default), keeps short
typed prompts, masks addresses, URLs, home paths and token-like strings, removes duplicates and samples
up to --limit of them deterministically, questions first. Output goes to a private directory (mode 0700)
outside the repository; nothing here is meant to be committed.
"""
import argparse
import datetime
import hashlib
import json
import os
import random
import re
import sys
from pathlib import Path

PRIVATE_DIR = Path.home() / ".local/state/arkism/recall-quality-v2"
TRANSCRIPTS = Path.home() / ".claude/projects"
MASKS = [
    (re.compile(r"[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}", re.I), "[email]"),
    (re.compile(r"https?://\S+"), "[url]"),
    (re.compile(r"\b01[016789][- ]?\d{3,4}[- ]?\d{4}\b"), "[phone]"),
    (re.compile(r"\b\d{1,3}(?:\.\d{1,3}){3}\b"), "[ip]"),
    (re.compile(re.escape(str(Path.home()))), "~"),
    (re.compile(r"\b[A-Za-z0-9_-]{32,}\b"), "[token]"),
]
QUESTION = re.compile(r"(?:\?)|(?:(?:지|까|어|야|해|나|냐|요|줘|해줘|알려|보여|찾아|확인)\s*[.!]?$)")


def anonymize(text):
    for pattern, replacement in MASKS:
        text = pattern.sub(replacement, text)
    return " ".join(text.split())


def usable(entry):
    """A typed user prompt that is short, prose, and not a slash command or a pasted block."""
    message = entry.get("message") or {}
    text = message.get("content")
    if entry.get("type") != "user" or entry.get("isSidechain") or entry.get("promptSource") == "system":
        return None
    if not isinstance(text, str):
        return None
    text = text.strip()
    if not (8 <= len(text) <= 400) or text[0] in "<[/{" or "```" in text or text.count("\n") > 3:
        return None
    return text


def short_hash(value):
    return hashlib.sha256(value.encode("utf-8")).hexdigest()[:12]


def entries(path):
    """Parsed user-turn lines of one transcript; unreadable files and lines are skipped."""
    try:
        lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError:
        return
    for line in lines:
        if '"user"' not in line:  # cheap pre-filter; the JSON check below decides
            continue
        try:
            yield json.loads(line)
        except json.JSONDecodeError:
            continue


def collect(transcripts):
    seen, rows = set(), []
    for path in sorted(transcripts.glob("*/*.jsonl")):
        for entry in entries(path):
            text = usable(entry)
            if text is None:
                continue
            query = anonymize(text)
            key = re.sub(r"\W+", "", query.lower())
            if not key or key in seen:
                continue
            seen.add(key)
            rows.append({"query": query, "session": short_hash(str(entry.get("sessionId", path.stem))),
                         "day": str(entry.get("timestamp", ""))[:10], "project": short_hash(str(entry.get("cwd", path.parent.name)))[:8]})
    return rows


def sample(rows, limit, seed):
    """Deterministic: questions first, each project no more than its share, then fill from the rest."""
    rng = random.Random(seed)
    questions = [r for r in rows if QUESTION.search(r["query"])]
    others = [r for r in rows if not QUESTION.search(r["query"])]
    rng.shuffle(questions)
    rng.shuffle(others)
    chosen, per_project = [], {}
    cap = max(1, limit // 10)  # shortcut: a flat per-project cap stands in for stratification; revisit once >10 projects matter
    for pool in (questions, others):
        for row in pool:
            if len(chosen) >= limit:
                break
            if per_project.get(row["project"], 0) >= cap:
                continue
            per_project[row["project"]] = per_project.get(row["project"], 0) + 1
            chosen.append(row)
    for i, row in enumerate(chosen, 1):
        row["id"] = f"q-{i:04d}"
    return chosen


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--transcripts", type=Path, default=TRANSCRIPTS)
    parser.add_argument("--out", type=Path, default=PRIVATE_DIR / "queries.jsonl")
    parser.add_argument("--limit", type=int, default=200)
    parser.add_argument("--seed", type=int, default=20261027)
    args = parser.parse_args(argv)
    rows = collect(args.transcripts)
    chosen = sample(rows, args.limit, args.seed)
    args.out.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(args.out.parent, 0o700)
    with open(args.out, "w", encoding="utf-8") as f:
        for row in chosen:
            f.write(json.dumps({k: row[k] for k in ("id", "query", "session", "day", "project")}, ensure_ascii=False) + "\n")
    os.chmod(args.out, 0o600)
    print(json.dumps({"candidates": len(rows), "written": len(chosen), "questions": sum(bool(QUESTION.search(r["query"])) for r in chosen),
                      "projects": len({r["project"] for r in chosen}), "out": str(args.out), "at": datetime.datetime.now(datetime.timezone.utc).isoformat()}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
