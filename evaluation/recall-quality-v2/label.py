#!/usr/bin/env python3
"""Label the private query set by hand, and dump what the live search returns for scoring.

`label` shows each unlabeled query with the top hits of POST /v1/search (read-only, purpose "answer")
and records the labeler's answer as one line of labels.jsonl. `run` writes the ranked memory ids the
search returns for every labeled query to runs-<date>.jsonl, the input of
tools/evaluate_semantic_recall.py --labels. Both talk only to the local Arkism API named by ARKISM_URL
and ARKISM_TOKEN_FILE; neither writes to the server.
"""
import argparse
import datetime
import json
import os
import sys
import urllib.request
from pathlib import Path

PRIVATE_DIR = Path.home() / ".local/state/arkism/recall-quality-v2"


def read_jsonl(path):
    if not path.exists():
        return []
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def append_jsonl(path, row):
    with open(path, "a", encoding="utf-8") as f:
        f.write(json.dumps(row, ensure_ascii=False) + "\n")
    os.chmod(path, 0o600)


class Search:
    def __init__(self, url, token_file, scope, limit):
        self.url, self.scope, self.limit = url.rstrip("/"), scope, limit
        self.token = Path(token_file).read_text(encoding="utf-8").strip()

    def hits(self, query):
        body = json.dumps({"query": query, "scope": self.scope, "limit": self.limit, "purpose": "answer"}).encode()
        request = urllib.request.Request(self.url + "/v1/search", data=body, headers={"content-type": "application/json", "authorization": "Bearer " + self.token})
        with urllib.request.urlopen(request, timeout=60) as response:
            return [hit for hit in json.load(response).get("items", []) if hit.get("collection", "memories") == "memories"]


def parse_answer(answer, shown):
    """Rank numbers or memory ids typed by the labeler; None for skip/quit, [] for 'nothing should come back'."""
    answer = answer.strip()
    if answer in ("s", "q"):
        return None
    if answer == "-":
        return []
    ids = []
    for token in answer.replace(",", " ").split():
        if token.isdigit() and 1 <= int(token) <= len(shown):
            ids.append(shown[int(token) - 1])
        else:
            ids.append(token)
    return sorted(set(ids), key=ids.index)


def label(args, search):
    done = {row["id"] for row in read_jsonl(args.labels)}
    pending = [q for q in read_jsonl(args.queries) if q["id"] not in done][:args.limit]
    print(f"{len(pending)} to label ({len(done)} done). Answer with rank numbers or memory ids, '-' = nothing should come back, 's' = skip, 'q' = quit.")
    for n, query in enumerate(pending, 1):
        hits = search.hits(query["query"])
        shown = [hit["id"] for hit in hits]
        print(f"\n[{n}/{len(pending)}] {query['id']}: {query['query']}")
        for rank, hit in enumerate(hits, 1):
            print(f"  {rank:2d}. {hit['id']}  ({hit.get('kind', '?')})  {' '.join(str(hit.get('text', '')).split())[:140]}")
        answer = input("expected> ")
        if answer.strip() == "q":
            break
        expected = parse_answer(answer, shown)
        if expected is None:
            continue
        note = input("note (enter for none)> ").strip()
        append_jsonl(args.labels, {"id": query["id"], "query": query["query"], "expected_memory_ids": expected, "labeler": args.labeler,
                                   "labeled_at": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"), "note": note, "shown": shown})
    print(f"{len(read_jsonl(args.labels))} labeled.")


def run(args, search):
    out = args.labels.parent / f"runs-{datetime.date.today().isoformat()}.jsonl"
    labeled = read_jsonl(args.labels)
    with open(out, "w", encoding="utf-8") as f:
        for row in labeled:
            f.write(json.dumps({"id": row["id"], "returned": [hit["id"] for hit in search.hits(row["query"])]}) + "\n")
    os.chmod(out, 0o600)
    print(f"{len(labeled)} queries -> {out}")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("command", choices=("label", "run"))
    parser.add_argument("--queries", type=Path, default=PRIVATE_DIR / "queries.jsonl")
    parser.add_argument("--labels", type=Path, default=PRIVATE_DIR / "labels.jsonl")
    parser.add_argument("--labeler", default=os.environ.get("USER", "owner"))
    parser.add_argument("--limit", type=int, default=20, help="queries to label in this sitting")
    parser.add_argument("--k", type=int, default=10, help="hits to show and to record per query")
    parser.add_argument("--url", default=os.environ.get("ARKISM_URL", "http://127.0.0.1:8091"))
    parser.add_argument("--token-file", default=os.environ.get("ARKISM_TOKEN_FILE", "/srv/arkism/config/owner.token"))
    parser.add_argument("--workspace", default=os.environ.get("ARKISM_WORKSPACE", "default"))
    parser.add_argument("--project", default=os.environ.get("ARKISM_PROJECT", "default"))
    parser.add_argument("--user", default=os.environ.get("ARKISM_USER", "management:owner"))
    args = parser.parse_args(argv)
    search = Search(args.url, args.token_file, {"workspace_id": args.workspace, "project_id": args.project, "user_id": args.user}, args.k)
    (label if args.command == "label" else run)(args, search)
    return 0


if __name__ == "__main__":
    sys.exit(main())
