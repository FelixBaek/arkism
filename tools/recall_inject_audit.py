#!/usr/bin/env python3
"""Summarize injected context sizes from capture metrics (ST-810); numbers only."""
import argparse
from collections import defaultdict
import json
from pathlib import Path

from capture_metrics_audit import timestamp

HOOKS = frozenset(("SessionStart", "UserPromptSubmit"))


def spread(values):
    """Five-number summary with nearest-rank quantiles."""
    if not values:
        return {"n": 0}
    v = sorted(values)
    pick = lambda p: v[round(p * (len(v) - 1))]
    return {"n": len(v), "min": v[0], "p10": pick(0.1), "p50": pick(0.5),
            "p90": pick(0.9), "max": v[-1]}


def selected(line, cutoff):
    """Return an inject/recall row at or after the cutoff, None to ignore, or raise."""
    row = json.loads(line)
    if row.get("event") not in ("inject", "recall"):
        return None
    if cutoff and timestamp(row.get("ts")) < cutoff:
        return None
    if not isinstance(row.get("session"), str):
        row["session"] = None
    return row


def summarize(lines, since=None, session_max=4000):
    """inject chars per hook and recall item counts; session ids only counted."""
    cutoff = timestamp(since) if since is not None else None
    chars, injected = defaultdict(list), defaultdict(list)
    sessions = defaultdict(set)
    skipped = 0
    for line in lines:
        try:
            row = selected(line, cutoff)
        except (ValueError, TypeError, AttributeError):
            skipped += 1
            continue
        if row is None:
            continue
        hook = row.get("hook") if row.get("hook") in HOOKS else "other"
        if row["event"] == "inject" and type(row.get("chars")) is int:
            chars[hook].append(row["chars"])
            sessions[hook].add(row["session"])
        elif row["event"] == "recall" and type(row.get("injected")) is int:
            injected[hook].append(row["injected"])
    return {
        "since": cutoff.isoformat() if cutoff else None,
        "session_max_chars": session_max,
        "inject_chars": {h: {**spread(v), "sessions": len(sessions[h] - {None}),
                             "over_session_max": sum(c > session_max for c in v)}
                         for h, v in sorted(chars.items())},
        "recall_items": {h: {**spread(v), "empty": v.count(0)}
                         for h, v in sorted(injected.items())},
        "skipped_lines": skipped,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("metrics", type=Path, nargs="?",
                        default=Path.home() / ".local/state/arkism-capture/metrics.jsonl")
    parser.add_argument("--since", help="Inclusive timezone-aware ISO timestamp")
    parser.add_argument("--session-max", type=int, default=4000,
                        help="ARKISM_SESSION_MAX_CHARS of the measured host")
    args = parser.parse_args()
    try:
        with args.metrics.open(encoding="utf-8") as stream:
            result = summarize(stream, args.since, args.session_max)
    except (OSError, ValueError):
        parser.exit(2, "Cannot read metrics or parse the since timestamp.\n")
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
