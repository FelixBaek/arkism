#!/usr/bin/env python3
"""Compare fixed retrieval rows without changing questions, gold evidence or budgets.

This evaluates evidence coverage, not answer accuracy. The baseline and candidate
must come from the same frozen harness; provenance mapping remains its limitation.
"""
import argparse
import hashlib
import json
import math
from pathlib import Path

CATEGORIES = ("multi-hop", "temporal", "open-domain", "single-hop")


def load(path):
    result = {}
    for line in Path(path).read_text().splitlines():
        if not line.strip():
            continue
        row = json.loads(line)
        key = row["key"]
        if not isinstance(key, str) or key in result:
            raise ValueError("missing or duplicate question identity")
        if row["category"] not in CATEGORIES or not isinstance(row["question"], str):
            raise ValueError("unknown category or invalid question")
        if not isinstance(row["evidence"], list) or any(not isinstance(x, str) or not x for x in row["evidence"]):
            raise ValueError("invalid gold evidence")
        result[key] = row
    if not result:
        raise ValueError("empty result set")
    return result


def recall(row):
    run = row["@10"]
    if not isinstance(run["items"], list) or not isinstance(run["conversation"], list):
        raise ValueError("invalid results")
    if len(run["items"]) > 10 or len(run["conversation"]) > 5:
        raise ValueError("fixed retrieval count budget exceeded")
    covered = set()
    for item in run["items"] + run["conversation"]:
        if not isinstance(item.get("dia"), list) or any(not isinstance(x, str) for x in item["dia"]):
            raise ValueError("invalid mapped evidence")
        covered.update(item["dia"])
    evidence = set(row["evidence"])
    return len(evidence & covered) / len(evidence) if evidence else None


def compare(baseline, candidate, threshold=0.9):
    if not math.isfinite(threshold) or not 0.9 <= threshold <= 1:
        raise ValueError("threshold must be between 0.9 and 1")
    if baseline.keys() != candidate.keys():
        raise ValueError("question identities changed")
    values = {cat: [[], []] for cat in CATEGORIES}
    frozen = []
    degraded = 0
    unscored = 0
    for key in sorted(baseline):
        a, b = baseline[key], candidate[key]
        for field in ("category", "question", "evidence"):
            if a[field] != b[field]:
                raise ValueError("fixed question/category/evidence changed")
        frozen.append([key, a["category"], a["question"], a["evidence"]])
        x, y = recall(a), recall(b)
        if not isinstance(b["@10"].get("degraded"), list):
            raise ValueError("missing degradation evidence")
        degraded += bool(b["@10"]["degraded"])
        if x is None:
            unscored += 1
            continue
        values[a["category"]][0].append(x)
        values[a["category"]][1].append(y)
    per = {}
    for cat, (a, b) in values.items():
        if not a:
            raise ValueError("missing scored category")
        old, new = sum(a) / len(a), sum(b) / len(b)
        per[cat] = {"questions": len(a), "baseline": old, "candidate": new,
                    "delta": new-old, "passed": new+1e-12 >= max(threshold, old)}
    return {"metric": "macro evidence recall@10 plus bounded conversation lane",
            "frozen_questions_sha256": hashlib.sha256(json.dumps(frozen, ensure_ascii=False, separators=(",", ":")).encode()).hexdigest(),
            "threshold": threshold, "unscored": unscored, "degraded": degraded,
            "categories": per, "passed": degraded == 0 and all(x["passed"] for x in per.values())}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", required=True)
    parser.add_argument("--candidate", required=True)
    parser.add_argument("--output")
    args = parser.parse_args()
    try:
        report = compare(load(args.baseline), load(args.candidate))
    except (ValueError, KeyError, TypeError, OSError) as exc:
        parser.exit(2, f"Recall gate invalid: {exc}\n")
    rendered = json.dumps(report, indent=2) + "\n"
    if args.output:
        Path(args.output).write_text(rendered)
    print(rendered, end="")
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
