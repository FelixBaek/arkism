"""Replay the frozen recall set (recall-quality-v1) through the outcome weight of memory_signal ranking.

Two exports are replayed: the frozen 2026-10-18 export of recall-quality-v1 and search_candidates_ec057c4.json,
the same experiment exported on origin/main ec057c4 (TestPostgresRecallQualityExperimentExport, frozen vectors,
no model call). Each export holds the server's fused candidates with their scores. This script rescales those scores
by the same outcome factor the server applies (+5% per passed sample, three at most; /10 once any sample
failed; 1 without a judgement), re-sorts, then applies the host floor and kind caps the export recorded.
The frozen set carries no judged outcomes, so the real "after" equals "before"; the other scenarios
assign synthetic per-document signals to bound what the weight can gain or lose. None of them is evidence
of production gain.
"""
import hashlib
import json
import random
import statistics
from pathlib import Path

ROOT = Path(__file__).resolve().parent
FROZEN = ROOT.parent / "recall-quality-v1"
MIN_SCORE, RANDOM_DRAWS, SEED = 0.025, 200, 20261027
KIND_CAPS = {"user": (2, 1500), "procedural": (3, 1500), "lesson": (2, 1500), "decision": (2, 1500)}
SHIPPED_DIVISOR, COMPARED_DIVISORS = 10, (10, 2)


def outcome_factor(samples, passed, fail_divisor=SHIPPED_DIVISOR):
    if samples < 1:
        return 1.0
    f = 1 + 0.05 * min(passed, 3)
    return f / fail_divisor if passed < samples else f


def pack(ids, documents, limit=6, max_chars=6000):
    """The exported host budget: search max_chars first, then the kind caps, in rune counts."""
    rows, remaining = [], max_chars
    for doc_id in ids[:limit]:
        if remaining <= 0:
            break
        document = documents[doc_id]
        length = min(len(document["text"]), remaining)
        rows.append((doc_id, document["kind"], length))
        remaining -= length
        remaining -= min(len(document.get("summary", "")), remaining)
    used, kept = {}, []
    for doc_id, kind, length in rows:
        count, chars = used.get(kind, (0, 0))
        count, chars = count + 1, chars + length
        cap = KIND_CAPS.get(kind)
        if cap and (count > cap[0] or (count > 1 and chars > cap[1])):
            continue
        used[kind] = (count, chars)
        kept.append(doc_id)
    return kept


def floor(candidate):
    """The host floor: a low fused score survives only from a text lane or a verified vector match."""
    lanes = candidate.get("lanes", [])
    match = candidate.get("vector_match") or {}
    verified = "vector" in lanes and 0 <= match.get("distance", -1) <= match.get("max_distance", 0)
    return candidate["score"] >= MIN_SCORE or "exact" in lanes or "keyword" in lanes or verified


def final_ids(row, documents, signals, fail_divisor):
    ranked = sorted(row["baseline_candidates"], key=lambda c: -c["score"] * outcome_factor(*signals.get(c["id"], (0, 0)), fail_divisor))
    return pack([c["id"] for c in ranked if floor(c)], documents)


def metrics(queries, returned):
    out = {"relevant_queries": 0, "recall@1": 0, "recall@3": 0, "recall@6": 0, "mrr": 0.0, "wrong_documents": 0,
           "negative_queries": 0, "negative_false_positives": 0}
    for q in queries:
        if q["label_status"] == "ambiguous":
            continue
        ids, relevant = returned[q["id"]], set(q["relevant"])
        if not relevant:
            out["negative_queries"] += 1
            out["negative_false_positives"] += bool(ids)
            out["wrong_documents"] += len(ids)
            continue
        out["relevant_queries"] += 1
        out["wrong_documents"] += len([i for i in ids if i not in relevant])
        rank = next((i + 1 for i, d in enumerate(ids) if d in relevant), None)
        for k in (1, 3, 6):
            out[f"recall@{k}"] += bool(rank and rank <= k)
        out["mrr"] += 1 / rank if rank else 0
    n = out["relevant_queries"]
    for k in (1, 3, 6):
        out[f"recall@{k}"] = round(out[f"recall@{k}"] / n, 4)
    out["mrr"] = round(out["mrr"] / n, 4)
    return out


def run(signals, dataset, export, fail_divisor=SHIPPED_DIVISOR):
    documents = {d["id"]: d for d in dataset["documents"]}
    returned = {q["id"]: final_ids(q, documents, signals, fail_divisor) for q in export["queries"]}
    return {split: metrics([q for q in dataset["queries"] if q["split"] == split], returned) for split in ("calibration", "holdout")}, returned


EXPORTS = {"frozen_2026-10-18": FROZEN / "search_candidates.json", "base_ec057c4": ROOT / "search_candidates_ec057c4.json"}


def evaluate(dataset, export):
    before, returned = run({}, dataset, export)
    for q in export["queries"]:
        if returned[q["id"]] != q["baseline"]:
            raise SystemExit(f"replay without signals must reproduce the exported baseline: {q['id']}")
    # Oracle bounds from the labels: how often each document came back rightly or wrongly in the baseline.
    right, wrong = {}, {}
    for q in dataset["queries"]:
        if q["label_status"] == "ambiguous":
            continue
        for doc in q["relevant"] and returned[q["id"]] or []:
            (right if doc in q["relevant"] else wrong).__setitem__(doc, (right if doc in q["relevant"] else wrong).get(doc, 0) + 1)
        for doc in returned[q["id"]] if not q["relevant"] else []:
            wrong[doc] = wrong.get(doc, 0) + 1
    docs = [d["id"] for d in dataset["documents"]]
    noise = {d: (1, 0) for d in docs if wrong.get(d, 0) > right.get(d, 0)}
    useful = {d: (1, 0) for d in docs if d not in noise}
    scenarios = {"after_no_judged_outcomes": {}, "oracle_noise_documents_fail": noise, "worst_useful_documents_fail": useful}
    results = {"before": before, "shipped_fail_divisor": SHIPPED_DIVISOR, "scenario_signal_counts": {name: len(sig) for name, sig in scenarios.items()},
               "random_signals_model": f"{RANDOM_DRAWS} draws, seed {SEED}: each document unjudged with p=0.5, else 1..5 samples each passing with p=0.8"}
    rng, draws = random.Random(SEED), []
    for _ in range(RANDOM_DRAWS):
        sig = {}
        for d in docs:
            if rng.random() < 0.5:
                samples = rng.randint(1, 5)
                sig[d] = (samples, sum(rng.random() < 0.8 for _ in range(samples)))
        draws.append(sig)
    for divisor in COMPARED_DIVISORS:
        block = {name: run(sig, dataset, export, divisor)[0] for name, sig in scenarios.items()}
        drawn = [run(sig, dataset, export, divisor)[0] for sig in draws]
        block["random_signals"] = {}
        for split in ("calibration", "holdout"):
            for key in ("recall@1", "recall@6", "mrr", "wrong_documents"):
                values = [d[split][key] for d in drawn]
                block["random_signals"][f"{split}.{key}"] = {"mean": round(statistics.mean(values), 4), "min": min(values), "max": max(values)}
        results[f"fail_divisor_{divisor}"] = block
    return results


def main():
    dataset_bytes = (FROZEN / "dataset.json").read_bytes()
    dataset, results = json.loads(dataset_bytes), {}
    for name, path in EXPORTS.items():
        export = json.loads(path.read_text())
        if export["dataset_sha256"] != hashlib.sha256(dataset_bytes).hexdigest():
            raise SystemExit(f"{path.name} does not match dataset.json")
        results[name] = evaluate(dataset, export)
    (ROOT / "results.json").write_text(json.dumps(results, ensure_ascii=False, indent=2, sort_keys=True) + "\n")
    print(json.dumps(results, ensure_ascii=False, indent=1))


if __name__ == "__main__":
    main()
