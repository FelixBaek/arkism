#!/usr/bin/env python3
"""Offline evidence recall for the answer step of a long-conversation evaluation.

For every question it runs the same search the answer step runs (exact+fts+vector,
purpose answer, include sources) and reports which of the question's evidence
turns are covered by the returned memories and conversation turns. No model is called.

A gold turn (has_answer) counts as covered when a returned conversation chunk
contains its opening text, or when a returned memory from the same session is
mostly made of that turn's words (weighted by rarity) and shares at least one
word that is rare in the haystack. The rare-word condition keeps a generic
memory ("ran a 5K") from covering the turn that carries the specific fact.

Input: a JSON list of questions with question_id, question_type, question,
haystack_session_ids and answer_session_ids, ingested by the bench tool, whose
capture event ids and turn source refs end in "-s<N>" (N = 1-based session index).

Output: one JSON row per question and a per-category summary on stdout.
"""
import argparse
import concurrent.futures
import json
import math
import re
import sys
import urllib.parse
import urllib.request

SESSION = re.compile(r"-s(\d+)(?:-\d+)?$")
WORD = re.compile(r"[a-z0-9]+")
STOP = set("""a an the and or but of to in on at for with by from as is are was were be been being i me my we our you your
he she it its they them their this that these those have has had do does did not no so if then than too very can could
would should will just also about into over some any all more most other such only own same up down out off again
speaker user unnamed person someone""".split())
RARE_SESSIONS = 3
COVER = 0.6


def words(text):
    return [w for w in WORD.findall(text.lower()) if w not in STOP]


def call(args, method, path, body=None):
    req = urllib.request.Request(args.url.rstrip("/") + path, method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Authorization": "Bearer " + args.token, "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=120) as resp:
        return json.load(resp)


def session_of(item, args, scope):
    """1-based session index an item came from, or None."""
    for tag in item.get("tags") or []:
        if tag.startswith("event_id:"):
            m = SESSION.search(tag)
            if m:
                return int(m.group(1))
    ref = item.get("source_ref")
    if ref is None and item.get("collection") == "sources":
        q = urllib.parse.urlencode(scope)
        ref = call(args, "GET", "/v1/sources/" + item["id"] + "?" + q).get("source_ref", "")
    m = SESSION.search(ref or "")
    return int(m.group(1)) if m else None


def one(args, q):
    sample = q["question_id"]
    scope = {"workspace_id": args.workspace, "project_id": args.project_template.replace("{sample}", sample), "user_id": args.user}
    ids = q["haystack_session_ids"]
    df = {}
    for sess in q["haystack_sessions"]:
        for w in set(w for t in sess for w in words(t["content"])):
            df[w] = df.get(w, 0) + 1
    idf = lambda w: math.log((1 + len(ids)) / (1 + df.get(w, 0))) + 1
    gold = [(ids.index(s) + 1, t["content"]) for s in q.get("answer_session_ids") or [] if s in ids
            for t in q["haystack_sessions"][ids.index(s)] if t.get("has_answer")]
    body = {"scope": scope, "query": q["question"][:1000], "modes": ["exact", "fts", "vector"], "purpose": "answer",
            "destination": "internal", "limit": args.limit, "max_chars": 6000, "include": ["sources"]}
    out = call(args, "POST", "/v1/search", body)
    mem = [(session_of(h, args, scope), h.get("text", "")) for h in out.get("items") or [] if h.get("collection") in ("", None, "memories")]
    conv = [(session_of(h, args, scope), h.get("text", "")) for h in out.get("conversation") or []]

    def covered(sess, turn):
        tw = set(words(turn))
        head = " ".join(turn.split()[:12]).lower()
        if any(s == sess and head in " ".join(t.split()).lower() for s, t in conv):
            return True
        for s, t in mem:
            mw = set(words(t))
            if s != sess or not mw:
                continue
            share = sum(idf(w) for w in mw & tw) / sum(idf(w) for w in mw)
            if share >= COVER and any(df.get(w, 0) <= RARE_SESSIONS for w in mw & tw):
                return True
        return False

    hits = [covered(s, t) for s, t in gold]
    vector = any("vector" in (h.get("lanes") or []) for h in out.get("items") or [])
    return {"question_id": sample, "degraded": out.get("degraded") or ([] if vector else ["no_vector_hit"]), "category": q["question_type"], "gold_sessions": sorted({s for s, _ in gold}),
            "gold_turns": len(gold), "covered": sum(hits), "hit_all": bool(gold) and all(hits),
            "memory_sessions": [s for s, _ in mem], "conversation_sessions": [s for s, _ in conv],
            "distinct_memory_sessions": len({s for s, _ in mem if s}), "texts": [t[:160] for _, t in mem]}


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--url", required=True)
    ap.add_argument("--token-file", required=True)
    ap.add_argument("--dataset", required=True)
    ap.add_argument("--project-template", required=True, help="project id per question, {sample} is the question id")
    ap.add_argument("--workspace", default="default")
    ap.add_argument("--user", default="management:owner")
    ap.add_argument("--limit", type=int, default=10)
    ap.add_argument("--concurrency", type=int, default=4)
    ap.add_argument("--out", required=True, help="rows JSON path")
    args = ap.parse_args()
    with open(args.token_file) as f:
        args.token = f.read().strip()
    with open(args.dataset) as f:
        questions = [q for q in json.load(f) if q.get("answer_session_ids")]
    with concurrent.futures.ThreadPoolExecutor(args.concurrency) as pool:
        rows = list(pool.map(lambda q: one(args, q), questions))
    with open(args.out, "w") as f:
        json.dump(rows, f, ensure_ascii=False, indent=1)
    cats = {}
    for r in rows:
        c = cats.setdefault(r["category"], [0, 0, 0, 0])
        c[0] += 1
        c[1] += r["hit_all"]
        c[2] += r["covered"]
        c[3] += r["gold_turns"]
    total = [sum(c[i] for c in cats.values()) for i in range(4)]
    degraded = [r["question_id"] for r in rows if r["degraded"]]
    if degraded:
        print("degraded responses (rerun before comparing):", " ".join(degraded))
    for name, c in sorted(cats.items()) + [("all", total)]:
        print(f"{name:28s} questions {c[0]:3d}  all-evidence {c[1]:3d}  turns {c[2]}/{c[3]} ({100 * c[2] / max(c[3], 1):.1f}%)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
