#!/usr/bin/env python3
"""Label which skill a question should get, record what route_skills picks, and score it.

`label` shows each unlabeled question with the skills POST /v1/skills/route offers and records the
labeler's answer as one line of labels.jsonl. `run` asks the route for every labeled question and writes
what it picked to runs-<date>.jsonl. `score` turns labels and runs into aggregate numbers only (no
question text), the file kept in results/. The route call is the one agents make: it has no side effect
beyond the ROUTER decision log and the judge's metered call.
"""
import argparse
import datetime
import json
import os
import sys
import time
import urllib.parse
import urllib.request
from pathlib import Path

PRIVATE_DIR = Path.home() / ".local/state/arkism/skill-route-v1"
# The owner's real questions, already extracted and masked by recall-quality-v2.
QUERIES = Path.home() / ".local/state/arkism/recall-quality-v2/queries.jsonl"


def read_jsonl(path):
    if not path.exists():
        return []
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def write_private(path, text, mode="w"):
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with open(path, mode, encoding="utf-8") as f:
        f.write(text)
    os.chmod(path, 0o600)


class Router:
    def __init__(self, url, token_file, scope, runtime=""):
        self.url, self.scope, self.runtime = url.rstrip("/"), scope, runtime
        self.token = Path(token_file).read_text(encoding="utf-8").strip()
        self.skill_of = {}

    def _call(self, path, payload=None):
        data = None if payload is None else json.dumps(payload).encode()
        request = urllib.request.Request(self.url + path, data=data, headers={"content-type": "application/json", "authorization": "Bearer " + self.token})
        with urllib.request.urlopen(request, timeout=60) as response:
            return json.load(response)

    def skill_id(self, version_id):
        if version_id not in self.skill_of:
            record = self._call(f"/v1/skill_versions/{urllib.parse.quote(version_id, safe='')}?{urllib.parse.urlencode(self.scope)}")
            self.skill_of[version_id] = record.get("skill_id", "")
        return self.skill_of[version_id]

    def route(self, query):
        """What an agent's route_skills sees: the pick (skill id or ''), every judged skill by suitability, and the cost."""
        payload = {"scope": self.scope, "query": query, "limit": 8, "max_chars": 64000, "modes": ["exact", "fts", "vector"], "purpose": "skills"}
        if self.runtime:
            payload["runtime"] = self.runtime
        started = time.monotonic()
        out = self._call("/v1/skills/route", payload)
        latency = round((time.monotonic() - started) * 1000)
        judged = sorted(out.get("judgments") or [], key=lambda j: -float(j.get("suitability") or 0))
        offered = [self.skill_id(j["id"]) for j in judged if j.get("id")]
        selected = self.skill_id(out["selected_id"]) if out.get("selected_id") else ""
        return {"selected": selected, "offered": offered, "provider": out.get("provider", ""), "degraded": out.get("degraded") or [], "latency_ms": latency}


def parse_answer(answer, shown):
    """Rank numbers or skill ids typed by the labeler; None for skip/quit, [] for 'no skill fits'."""
    answer = answer.strip()
    if answer in ("s", "q"):
        return None
    if answer == "-":
        return []
    ids = []
    for token in answer.replace(",", " ").split():
        ids.append(shown[int(token) - 1] if token.isdigit() and 1 <= int(token) <= len(shown) else token)
    return sorted(set(ids), key=ids.index)


def label(args, router):
    done = {row["id"] for row in read_jsonl(args.labels)}
    pending = [q for q in read_jsonl(args.queries) if q["id"] not in done][:args.limit]
    print(f"{len(pending)} to label ({len(done)} done). Answer with rank numbers or skill ids, '-' = no skill fits, 's' = skip, 'q' = quit.")
    for n, query in enumerate(pending, 1):
        routed = router.route(query["query"])
        shown = routed["offered"]
        print(f"\n[{n}/{len(pending)}] {query['id']}: {query['query']}")
        for rank, skill in enumerate(shown, 1):
            print(f"  {rank:2d}. {skill}{'  <- picked' if skill == routed['selected'] else ''}")
        answer = input("expected> ")
        if answer.strip() == "q":
            break
        expected = parse_answer(answer, shown)
        if expected is None:
            continue
        note = input("note (enter for none)> ").strip()
        row = {"id": query["id"], "query": query["query"], "expected_skill_ids": expected, "labeler": args.labeler,
               "labeled_at": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"), "note": note, "shown": shown}
        write_private(args.labels, json.dumps(row, ensure_ascii=False) + "\n", "a")
    print(f"{len(read_jsonl(args.labels))} labeled.")


def run(args, router):
    out = args.labels.parent / f"runs-{datetime.date.today().isoformat()}.jsonl"
    labeled = read_jsonl(args.labels)
    write_private(out, "".join(json.dumps({"id": row["id"], **router.route(row["query"])}) + "\n" for row in labeled))
    print(f"{len(labeled)} questions -> {out}")


def percentile(values, p):
    if not values:
        return None
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, int(round(p / 100 * (len(ordered) - 1))))]


def ratio(n, d):
    return round(n / d, 4) if d else None


def score(labels, runs):
    """Aggregate numbers only. Questions with an expected skill and questions where none fits are scored apart."""
    by_id = {row["id"]: row for row in runs}
    fit, none, latencies, providers, degraded = [], [], [], {}, {}
    for row in labels:
        got = by_id.get(row["id"])
        if got is None:
            continue
        (fit if row["expected_skill_ids"] else none).append((set(row["expected_skill_ids"]), got))
        latencies.append(got["latency_ms"])
        providers[got["provider"]] = providers.get(got["provider"], 0) + 1
        for d in got["degraded"]:
            degraded[d] = degraded.get(d, 0) + 1
    return {
        "labeled": len(labels), "scored": len(fit) + len(none), "with_fit": len(fit), "without_fit": len(none),
        # A skill fits: was the pick right, wrong, or withheld, and was a right skill among the judged at all?
        "pick_accuracy": ratio(sum(got["selected"] in exp for exp, got in fit), len(fit)),
        "wrong_pick_rate": ratio(sum(bool(got["selected"]) and got["selected"] not in exp for exp, got in fit), len(fit)),
        "withheld_rate": ratio(sum(not got["selected"] for exp, got in fit), len(fit)),
        "candidate_recall": ratio(sum(bool(exp & set(got["offered"])) for exp, got in fit), len(fit)),
        # No skill fits: the route should pick nothing; a pick here is an unneeded skill load.
        "abstain_accuracy": ratio(sum(not got["selected"] for exp, got in none), len(none)),
        "unneeded_pick_rate": ratio(sum(bool(got["selected"]) for exp, got in none), len(none)),
        "latency_ms_p50": percentile(latencies, 50), "latency_ms_p95": percentile(latencies, 95),
        "providers": providers, "degraded": degraded,
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("command", choices=("label", "run", "score"))
    parser.add_argument("--queries", type=Path, default=QUERIES)
    parser.add_argument("--labels", type=Path, default=PRIVATE_DIR / "labels.jsonl")
    parser.add_argument("--runs", type=Path, help="score: the runs file (default: newest runs-*.jsonl beside the labels)")
    parser.add_argument("--out", type=Path, help="score: where to write the aggregate (default: results/<date>.json here)")
    parser.add_argument("--labeler", default=os.environ.get("USER", "owner"))
    parser.add_argument("--limit", type=int, default=20, help="questions to label in this sitting")
    parser.add_argument("--runtime", default=os.environ.get("ARKISM_RUNTIME", ""))
    parser.add_argument("--url", default=os.environ.get("ARKISM_URL", "http://127.0.0.1:8091"))
    parser.add_argument("--token-file", default=os.environ.get("ARKISM_TOKEN_FILE", "/srv/arkism/config/owner.token"))
    parser.add_argument("--workspace", default=os.environ.get("ARKISM_WORKSPACE", "default"))
    parser.add_argument("--project", default=os.environ.get("ARKISM_PROJECT", "default"))
    parser.add_argument("--user", default=os.environ.get("ARKISM_USER", "management:owner"))
    args = parser.parse_args(argv)
    if args.command == "score":
        runs = args.runs or max(args.labels.parent.glob("runs-*.jsonl"), default=None)
        if runs is None:
            parser.error("no runs file: run `route_eval.py run` first or pass --runs")
        result = {"labels": args.labels.name, "runs": runs.name, **score(read_jsonl(args.labels), read_jsonl(runs))}
        out = args.out or Path(__file__).resolve().parent / "results" / f"{datetime.date.today().isoformat()}.json"
        out.parent.mkdir(exist_ok=True)
        out.write_text(json.dumps(result, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
        print(json.dumps(result, ensure_ascii=False, indent=1))
        return 0
    router = Router(args.url, args.token_file, {"workspace_id": args.workspace, "project_id": args.project, "user_id": args.user}, args.runtime)
    (label if args.command == "label" else run)(args, router)
    return 0


if __name__ == "__main__":
    sys.exit(main())
