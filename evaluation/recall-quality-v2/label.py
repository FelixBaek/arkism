#!/usr/bin/env python3
"""Label the private query set by hand, and dump what the live search returns for scoring.

`label` shows each unlabeled query with the top hits of POST /v1/search (read-only, purpose "answer")
and records the labeler's answer as one line of labels.jsonl. `run` writes the ranked memory ids the
search returns for every labeled query to runs-<date>.jsonl, the input of
tools/evaluate_semantic_recall.py --labels. `label --blind` records independently chosen IDs
without calling search or reading an API token. The other modes use the configured Arkism API.
"""
import argparse
import datetime
import json
import os
import sys
import tempfile
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from eval_support import parse_answer, restrict_private_file


PRIVATE_DIR = Path.home() / ".local/state/arkism/recall-quality-v2"


def read_jsonl(path):
    if not path.exists():
        return []
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def append_jsonl(path, row):
    def open_private(name, flags):
        return os.open(name, flags, 0o600)

    with open(path, "a", encoding="utf-8", opener=open_private) as f:
        restrict_private_file(f)
        f.write(json.dumps(row, ensure_ascii=False) + "\n")


class Search:
    def __init__(self, url, token_file, scope, limit, fts_only=False):
        self.url, self.scope, self.limit = url.rstrip("/"), scope, limit
        self.fts_only = fts_only
        self.token = Path(token_file).read_text(encoding="utf-8").strip()

    def hits(self, query):
        criteria = {"query": query, "scope": self.scope, "limit": self.limit, "purpose": "answer"}
        if self.fts_only:
            criteria.update(modes=["fts"], rerank=False, expand=False)
        body = json.dumps(criteria).encode()
        request = urllib.request.Request(self.url + "/v1/search", data=body, headers={"content-type": "application/json", "authorization": "Bearer " + self.token})
        with urllib.request.urlopen(request, timeout=60) as response:
            return [hit for hit in json.load(response).get("items", []) if hit.get("collection", "memories") == "memories"]


def artifact_path(labels, name):
    prefix = "" if labels.name == "labels.jsonl" else labels.name + "."
    return labels.with_name(prefix + name)


def label(args, search):
    done = {row["id"] for row in read_jsonl(args.labels)}
    dispositions_path = artifact_path(args.labels, "dispositions.jsonl")
    topics_path = artifact_path(args.labels, "expectations.jsonl")
    if len({p.resolve() for p in (args.labels, args.queries, dispositions_path, topics_path)}) != 4:
        raise ValueError("queries, labels, expectations and dispositions must be separate files")
    dispositions = {row["id"]: row["disposition"] for row in read_jsonl(dispositions_path)
                    if row["disposition"] in ("no_experience", "no_required_memory")}
    closed = done | dispositions.keys()
    pending = [q for q in read_jsonl(args.queries) if q["id"] not in closed][:args.limit]
    answer_hint = "independently selected memory IDs" if args.blind else "rank numbers or memory IDs"
    print(f"{len(pending)} to label ({len(done)} labeled, {len(dispositions)} closed without scoring). Enter {answer_hint}, '-' = nothing should be returned, '필수 기억 없음' = no particular memory required, '해보지 않음' = no experience, 's' = skip for now, 'q' = quit.")
    if args.blind:
        topics = {row["id"]: row["expected_answer_topic"] for row in read_jsonl(topics_path)}
        print("If the memory ID is unknown, enter 'topic: <expected answer>' to keep the question open for scoring.")
    for n, query in enumerate(pending, 1):
        if query["id"] in closed:
            continue
        hits = [] if args.blind else search.hits(query["query"])
        shown = [hit["id"] for hit in hits]
        print(f"\n[{n}/{len(pending)}] {query['id']}: {query['query']}")
        if args.blind and query["id"] in topics:
            print(f"  Previously recorded expectation: {topics[query['id']]}")
        for rank, hit in enumerate(hits, 1):
            print(f"  {rank:2d}. {hit['id']}  ({hit.get('kind', '?')})  {' '.join(str(hit.get('text', '')).split())[:140]}")
        answer = input("expected> ")
        if answer.strip() == "q":
            break
        disposition = {"해보지않음": "no_experience", "필수기억없음": "no_required_memory"}
        disposition = disposition.get("".join(answer.split()))
        if disposition:
            append_jsonl(dispositions_path, {"id": query["id"], "disposition": disposition,
                                             "recorded_at": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds")})
            closed.add(query["id"])
            continue
        if args.blind and answer.strip().startswith("topic:"):
            topic = answer.strip()[len("topic:"):].strip()
            if topic and topics.get(query["id"]) != topic:
                append_jsonl(topics_path, {"id": query["id"], "expected_answer_topic": topic,
                                           "provided_before_ranking": True})
                topics[query["id"]] = topic
            continue
        expected = parse_answer(answer, shown)
        if expected is None:
            continue
        note = input("note (enter for none)> ").strip()
        row = {"id": query["id"], "query": query["query"], "expected_memory_ids": expected, "labeler": args.labeler,
               "labeled_at": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"), "note": note, "shown": shown}
        if args.blind:
            row["label_mode"] = "blind"
        append_jsonl(args.labels, row)
        closed.add(query["id"])
    print(f"{len(read_jsonl(args.labels))} labeled.")


def run(args, search, *, fts_only=False, requested_k=None):
    suffix = "-fts" if fts_only else ""
    out = artifact_path(args.labels, f"runs-{datetime.date.today().isoformat()}{suffix}.jsonl")
    if out.resolve() == args.labels.resolve():
        raise ValueError("run output and labels must be different files")
    labeled = read_jsonl(args.labels)
    if not labeled:
        raise ValueError("no labeled queries to run")
    pending = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=out.parent, delete=False) as f:
            pending = Path(f.name)
            restrict_private_file(f)
            for row in labeled:
                result = {"id": row["id"], "returned": [hit["id"] for hit in search.hits(row["query"])]}
                if requested_k is not None:
                    result["requested_k"] = requested_k
                f.write(json.dumps(result) + "\n")
        os.replace(pending, out)
    finally:
        if pending is not None:
            pending.unlink(missing_ok=True)
    print(f"{len(labeled)} queries -> {out}")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("command", choices=("label", "run"))
    parser.add_argument("--queries", type=Path, default=PRIVATE_DIR / "queries.jsonl")
    parser.add_argument("--labels", type=Path, default=PRIVATE_DIR / "labels.jsonl")
    parser.add_argument("--labeler", default=os.environ.get("USER", "owner"))
    parser.add_argument("--limit", type=int, default=20, help="queries to label in this sitting")
    parser.add_argument("--blind", action="store_true", help="label using independent memory IDs without showing ranked search results")
    parser.add_argument("--fts-only", action="store_true", help="run a full-text baseline with expansion and reranking disabled, saving a separate -fts result")
    parser.add_argument("--k", type=int, default=10, help="hits to show and to record per query")
    parser.add_argument("--url", default=os.environ.get("ARKISM_URL", "http://127.0.0.1:8091"))
    parser.add_argument("--token-file", default=os.environ.get("ARKISM_TOKEN_FILE", "/srv/arkism/config/owner.token"))
    parser.add_argument("--workspace", default=os.environ.get("ARKISM_WORKSPACE", "default"))
    parser.add_argument("--project", default=os.environ.get("ARKISM_PROJECT", "default"))
    parser.add_argument("--user", default=os.environ.get("ARKISM_USER", "management:owner"))
    args = parser.parse_args(argv)
    if not 1 <= args.k <= 50:
        parser.error("--k must be between 1 and 50")
    if args.fts_only and args.command != "run":
        parser.error("--fts-only requires the run command")
    if args.blind:
        if args.command != "label":
            parser.error("--blind requires the label command")
        label(args, None)
        return 0
    search = Search(args.url, args.token_file, {"workspace_id": args.workspace, "project_id": args.project, "user_id": args.user}, args.k, args.fts_only)
    if args.command == "label":
        label(args, search)
    else:
        run(args, search, fts_only=args.fts_only, requested_k=args.k)
    return 0


if __name__ == "__main__":
    sys.exit(main())
