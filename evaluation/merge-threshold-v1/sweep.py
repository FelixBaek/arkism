"""Sweep the capture merge threshold (CaptureMergeSimilarity, default 0.92) over the frozen recall set.

The capture gate embeds the new claim in query mode and compares it by cosine with approved memories'
document vectors (internal/memory/capture_merge_gate.go, merge_proposals.go mergeQueryVector). The frozen
recall-quality-v1 vectors were embedded the same way (query prefix for questions, none for memories), so
the cosines here are the ones the gate would see. No model is called.

The frozen set has no claim-to-claim rewordings. pairs.json labels each (question, memory) pair `same`
when the memory is in the question's reviewed `relevant` list; ambiguous questions are left out. This is a
proxy: a question a memory answers is looser than a rewording of that memory.

Two views are reported per threshold:
- pair: every pair at or above the threshold counts as a "same" prediction.
- gate: per question, only the highest pair at or above the threshold is proposed, as the gate does.

`python3 sweep.py --write-pairs` regenerates pairs.json; `python3 sweep.py` writes results.json and prints
the table.
"""
import argparse
import hashlib
import json
import math
from pathlib import Path

ROOT = Path(__file__).resolve().parent
FROZEN = ROOT.parent / "recall-quality-v1"
DEFAULT = 0.92
THRESHOLDS = [round(0.80 + 0.01 * i, 2) for i in range(19)]
# Below the requested range, only to show where the proxy pairs separate.
REFERENCE = [0.50, 0.55, 0.60, 0.65, 0.70, 0.75]


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def build_pairs(dataset):
    docs = [d["id"] for d in dataset["documents"]]
    pairs = []
    for q in dataset["queries"]:
        if q["label_status"] == "ambiguous":
            continue
        for d in docs:
            pairs.append({"query_id": q["id"], "document_id": d, "split": q["split"],
                          "category": q["category"], "same": d in q["relevant"]})
    return pairs


def cosine(a, b):
    return sum(x * y for x, y in zip(a, b)) / math.sqrt(sum(x * x for x in a) * sum(y * y for y in b))


def score(pairs, vectors):
    docs = {v["id"]: v["vector"] for v in vectors["documents"]}
    queries = {v["id"]: v["vector"] for v in vectors["queries"]}
    return [dict(p, cosine=cosine(queries[p["query_id"]], docs[p["document_id"]])) for p in pairs]


def prf(tp, predicted, positives):
    p = tp / predicted if predicted else 0.0
    r = tp / positives if positives else 0.0
    return {"tp": tp, "predicted": predicted, "positives": positives, "precision": round(p, 4),
            "recall": round(r, 4), "f1": round(2 * p * r / (p + r), 4) if p + r else 0.0}


def sweep(scored, thresholds):
    best = {}
    for p in scored:
        if p["query_id"] not in best or p["cosine"] > best[p["query_id"]]["cosine"]:
            best[p["query_id"]] = p
    has_same = {p["query_id"] for p in scored if p["same"]}
    rows = []
    for t in thresholds:
        hits = [p for p in scored if p["cosine"] >= t]
        proposed = [p for p in best.values() if p["cosine"] >= t]
        rows.append({"threshold": t,
                     "pair": prf(sum(p["same"] for p in hits), len(hits), sum(p["same"] for p in scored)),
                     "gate": prf(sum(p["same"] for p in proposed), len(proposed), len(has_same))})
    return rows


def summarize(scored):
    same = sorted(p["cosine"] for p in scored if p["same"])
    other = sorted(p["cosine"] for p in scored if not p["same"])
    return {"same_pairs": len(same), "other_pairs": len(other),
            "same_cosine_min_median_max": [round(same[0], 4), round(same[len(same) // 2], 4), round(same[-1], 4)],
            "other_cosine_max": round(other[-1], 4)}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--write-pairs", action="store_true")
    args = ap.parse_args()
    dataset = json.loads((FROZEN / "dataset.json").read_text())
    if args.write_pairs:
        rows = ",\n".join(json.dumps(p, ensure_ascii=False) for p in build_pairs(dataset))
        head = json.dumps(sha(FROZEN / "dataset.json"))
        (ROOT / "pairs.json").write_text(f'{{"source_dataset_sha256": {head}, "pairs": [\n{rows}\n]}}\n')
    pairs = json.loads((ROOT / "pairs.json").read_text())
    assert pairs["source_dataset_sha256"] == sha(FROZEN / "dataset.json"), "pairs.json is stale"
    scored = score(pairs["pairs"], json.loads((FROZEN / "model_vectors.json").read_text()))
    results = {"default": DEFAULT, "vectors_sha256": sha(FROZEN / "model_vectors.json"),
               "distribution": summarize(scored), "sweep": sweep(scored, THRESHOLDS),
               "reference_below_range": sweep(scored, REFERENCE)}
    (ROOT / "results.json").write_text(json.dumps(results, indent=1) + "\n")
    print("| threshold | pair P | pair R | pair F1 | gate P | gate R | gate F1 | proposals |")
    print("|---:|---:|---:|---:|---:|---:|---:|---:|")
    for row in results["reference_below_range"] + results["sweep"]:
        t, a, g = row["threshold"], row["pair"], row["gate"]
        mark = f"**{t:.2f}** (default)" if t == DEFAULT else f"{t:.2f}"
        print(f"| {mark} | {a['precision']:.3f} | {a['recall']:.3f} | {a['f1']:.3f} | "
              f"{g['precision']:.3f} | {g['recall']:.3f} | {g['f1']:.3f} | {g['predicted']} |")
    print(json.dumps(results["distribution"]))


if __name__ == "__main__":
    main()
