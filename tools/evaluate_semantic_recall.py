#!/usr/bin/env python3
"""Replay public synthetic recall labels against a local TEI, never production records.

The 0.38 cutoff was fixed from the deployed configuration before this dataset
was embedded. Labels are agent-authored development checks, not human quality
approval. Frozen vectors let the Go PostgreSQL test replay this experiment
without network access, model calls, or private data.

With --labels and --runs it instead scores a human-labeled set (recall@k, MRR,
precision; evaluation/recall-quality-v2) and writes an aggregate-only result file.
"""
import argparse
import datetime
import hashlib
import json
import math
from pathlib import Path
from urllib.request import Request, build_opener, HTTPRedirectHandler

MODEL = "microsoft/harrier-oss-v1-0.6b"
REVISION = "f9b9dc8d367d443f2479d27aa5d8d2850c0774ee"
QUERY_PREFIX = "Instruct: Given a user request, retrieve the stored memories, rules and skills that help answer it\nQuery: "
CUTOFF = 0.38
DOCUMENTS = [
    ("regression", "오류를 고친 뒤에는 같은 문제가 다시 생기지 않는지 자동 검사로 확인한다."),
    ("rollout", "출시할 때는 새 기능을 우선 소수 이용자에게만 제공하고 결과를 확인한 뒤 전체에 공개한다."),
    ("backup", "백업은 서버 밖에 별도 보관하고, 복원 시험으로 읽히는지 주기적으로 확인한다."),
    ("privacy", "개인정보가 포함된 기록은 외부 모델에 보내기 전에 식별 정보를 제거한다."),
    ("report", "작업 보고는 한국어로 짧게 쓰고, 결론과 남은 일부터 알려 준다."),
    ("approval", "인공지능이 추출한 기억은 검토 대기 상태로 저장하고, 사람이 승인한 뒤에만 활성화한다."),
]
# Phrasing and synonym cases were fixed before the first model run. All are
# held out from cutoff selection: the cutoff is an existing server setting.
QUERIES = [
    ("paraphrase", "regression", "결함 수정을 끝냈을 때 재발 방지는 어떻게 검증하지?"),
    ("synonym", "regression", "버그를 패치한 다음 회귀 테스트를 수행해야 한다는 규칙이 있나?"),
    ("paraphrase", "regression", "문제를 해결했다고 끝내지 말고 무엇을 더 점검해야 하지?"),
    ("paraphrase", "rollout", "이번 변경을 모든 고객에게 한꺼번에 배포해도 될까?"),
    ("synonym", "rollout", "카나리아 배포 후 점진적으로 적용 범위를 넓히는 절차를 찾아줘."),
    ("paraphrase", "rollout", "개선된 서비스를 처음 내놓을 때 위험을 줄이는 공개 순서를 알려줘."),
    ("paraphrase", "backup", "운영 장비가 사라져도 데이터를 되살릴 수 있게 하려면?"),
    ("synonym", "backup", "오프사이트 사본을 두고 복구 훈련을 실시하라는 지침을 찾아줘."),
    ("paraphrase", "backup", "보관한 사본이 정작 필요할 때 못 쓰는 상태인지 어떻게 예방하지?"),
    ("paraphrase", "privacy", "고객 대화를 클라우드 AI에게 전달하기 전 처리해야 할 일이 뭐야?"),
    ("synonym", "privacy", "민감한 인적 사항을 비식별화한 다음 외부 인공지능으로 전송하는 원칙이 있나?"),
    ("paraphrase", "privacy", "이름과 연락처가 섞인 로그를 원격 추론 서비스에 넣으려는데 준비 절차를 알려줘."),
    ("paraphrase", "report", "진행 상황을 나에게 설명할 때 선호하는 언어와 분량은?"),
    ("synonym", "report", "업무 경과는 우리말로 간결하게, 핵심 결과를 앞세워 전달해 줘."),
    ("paraphrase", "report", "완료한 내용과 앞으로 해야 할 것을 어떤 방식으로 알려주면 되지?"),
    ("paraphrase", "approval", "모델이 대화에서 뽑아낸 정보를 곧바로 이후 응답에 적용해도 돼?"),
    ("synonym", "approval", "자동 생성된 메모는 인간의 검수와 허가를 거쳐 사용하도록 한다."),
    ("paraphrase", "approval", "기계가 발견한 사용자 선호를 실제 규칙으로 쓰기까지 어떤 단계를 거쳐야 해?"),
    ("near_negative", "", "선형 회귀와 로지스틱 회귀의 수학적 차이를 설명해줘."),
    ("near_negative", "", "카나리아 새에게 주는 먹이 종류가 궁금해."),
    ("near_negative", "", "스마트폰 사진 복구 업체의 평균 요금은 얼마야?"),
    ("near_negative", "", "개인정보 보호법의 벌금 금액은 얼마인가?"),
    ("near_negative", "", "한국어 문법에서 관형사와 형용사는 어떻게 달라?"),
    ("near_negative", "", "인간의 장기 기억이 뇌에 저장되는 생물학적 원리를 알려줘."),
    ("negative", "", "토마토 파스타 소스를 만드는 요리법을 알려줘."),
    ("negative", "", "오스트레일리아의 수도는 어디야?"),
    ("negative", "", "고양이 털이 많이 빠지는 계절은 언제야?"),
    ("negative", "", "화산은 어떤 과정으로 형성되나?"),
    ("negative", "", "기타에서 C 코드를 잡는 손가락 위치는?"),
    ("negative", "", "축구에서 오프사이드가 되는 조건을 설명해줘."),
]


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise ValueError("redirects are disabled")


def distance(a, b):
    return 1 - sum(x * y for x, y in zip(a, b)) / math.sqrt(sum(x*x for x in a) * sum(y*y for y in b))


# Local TEI only: the fixture must never be built from a remote model.
ENDPOINT = "http://127.0.0.1:8093/embed"
# The frozen fixture always lives beside the Go test that replays it.
OUTPUT = Path(__file__).resolve().parent.parent / "internal/memory/testdata/semantic_recall_harrier.json"


def embed(opener, endpoint, texts, prefix):
    result = []
    for offset in range(0, len(texts), 4):
        body = json.dumps({"inputs": [prefix + text for text in texts[offset:offset+4]]}).encode()
        req = Request(endpoint, data=body, headers={"Content-Type": "application/json"})
        with opener.open(req, timeout=60) as response:
            rows = json.load(response)
        if len(rows) != min(4, len(texts)-offset) or any(len(v) != 1024 or not all(isinstance(x, (int, float)) and math.isfinite(x) for x in v) or sum(x*x for x in v) == 0 for v in rows):
            raise ValueError("invalid local embedding response")
        result.extend(rows)
    return result


def cached_queries():
    previous = json.loads(OUTPUT.read_text())
    if previous["model"] != MODEL or previous["revision"] != REVISION or previous["query_prefix"] != QUERY_PREFIX or [(q["category"], q["relevant"][0] if q["relevant"] else "", q["text"]) for q in previous["queries"]] != QUERIES:
        raise ValueError("cached query contract changed")
    return previous["queries"]


def observe(documents, queries):
    stats = {}
    for query in queries:
        row = stats.setdefault(query["category"], {"queries": 0, "target_found": 0, "returned": 0, "wrong_returned": 0})
        selected = [doc["id"] for doc in documents if distance(query["vector"], doc["vector"]) <= CUTOFF]
        row["queries"] += 1
        row["target_found"] += bool(set(selected) & set(query["relevant"]))
        row["returned"] += len(selected)
        row["wrong_returned"] += len(set(selected) - set(query["relevant"]))
    return stats


def read_jsonl(path):
    return [json.loads(line) for line in Path(path).read_text(encoding="utf-8").splitlines() if line.strip()]


def labeled_metrics(labels, runs, ks=(1, 3, 5, 10)):
    """recall@k, MRR and precision over a human-labeled set (evaluation/recall-quality-v2/README.md).

    labels: rows with id and expected_memory_ids; runs: rows with id and returned (ranked memory ids).
    A label whose run is missing is counted under "unscored", never as a miss. Aggregates only: no query text.
    """
    returned = {row["id"]: list(dict.fromkeys(row["returned"])) for row in runs}
    scored = [row for row in labels if row["id"] in returned]
    positives = [row for row in scored if row["expected_memory_ids"]]
    negatives = [row for row in scored if not row["expected_memory_ids"]]
    ranks = []
    for row in positives:
        hit = [i for i, m in enumerate(returned[row["id"]], 1) if m in row["expected_memory_ids"]]
        ranks.append(hit[0] if hit else None)
    out = {"labeled": len(labels), "scored": len(scored), "unscored": len(labels) - len(scored), "positives": len(positives), "negatives": len(negatives),
           "mrr": sum(1 / r for r in ranks if r) / len(ranks) if ranks else None, "at_k": {}}
    for k in ks:
        top = {row["id"]: returned[row["id"]][:k] for row in scored}
        found = [any(m in row["expected_memory_ids"] for m in top[row["id"]]) for row in positives]
        items = [(sum(m in top[row["id"]] for m in row["expected_memory_ids"]), len(row["expected_memory_ids"])) for row in positives]
        with_return = [row for row in scored if top[row["id"]]]
        out["at_k"][str(k)] = {
            "recall": sum(found) / len(found) if found else None,
            "item_recall": sum(f for f, _ in items) / sum(n for _, n in items) if items else None,
            "precision": sum(sum(m in row["expected_memory_ids"] for m in top[row["id"]]) / len(top[row["id"]]) for row in with_return) / len(with_return) if with_return else None,
            "negative_returned": sum(bool(top[row["id"]]) for row in negatives) / len(negatives) if negatives else None,
        }
    return out


def labeled_main(args):
    result = labeled_metrics(read_jsonl(args.labels), read_jsonl(args.runs))
    result.update(labels_sha256=hashlib.sha256(Path(args.labels).read_bytes()).hexdigest(), runs_sha256=hashlib.sha256(Path(args.runs).read_bytes()).hexdigest(),
                  created=datetime.date.today().isoformat())
    args.out.mkdir(parents=True, exist_ok=True)
    target = args.out / f"{result['created']}.json"
    target.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"out": str(target), "scored": result["scored"], "mrr": result["mrr"], "at_k": result["at_k"]}, ensure_ascii=False))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reuse-query-vectors", action="store_true", help="reuse unchanged public query vectors while re-embedding documents")
    parser.add_argument("--labels", help="human-labeled jsonl (private); scores it with --runs instead of embedding the synthetic set")
    parser.add_argument("--runs", help="ranked search results jsonl from evaluation/recall-quality-v2/label.py run")
    parser.add_argument("--out", type=Path, default=Path(__file__).resolve().parent.parent / "evaluation/recall-quality-v2/results", help="directory for the aggregate result file")
    args = parser.parse_args()
    if args.labels or args.runs:
        if not (args.labels and args.runs):
            parser.error("--labels and --runs go together")
        labeled_main(args)
        return
    opener = build_opener(NoRedirect)
    # recordContent for a memory with an empty summary is text + newline.
    documents = [{"id": name, "text": text, "vector": vector} for (name, text), vector in zip(DOCUMENTS, embed(opener, ENDPOINT, [text + "\n" for _, text in DOCUMENTS], ""))]
    if args.reuse_query_vectors:
        queries = cached_queries()
    else:
        queries = [{"category": category, "relevant": [relevant] if relevant else [], "text": text, "vector": vector} for (category, relevant, text), vector in zip(QUERIES, embed(opener, ENDPOINT, [text for _, _, text in QUERIES], QUERY_PREFIX))]
    prefix_hash = hashlib.sha256(json.dumps([QUERY_PREFIX, ""], ensure_ascii=False, separators=(",", ":")).encode()).hexdigest()
    fixture = {"model": MODEL, "revision": REVISION, "query_prefix": QUERY_PREFIX, "document_prefix": "", "document_input_format": "recordContent: text + newline + empty summary", "space": f"tei:{MODEL}:{REVISION}:1024d:{prefix_hash}", "max_distance": CUTOFF, "labels": "agent-authored public synthetic; held out from fixed deployed-cutoff selection; not human approval", "documents": documents, "queries": queries}
    fixture["observed"] = observe(documents, queries)
    OUTPUT.write_text(json.dumps(fixture, ensure_ascii=False, separators=(",", ":")) + "\n")
    print(json.dumps({"fixed_max_distance": CUTOFF, "stats": fixture["observed"], "fixture_sha256": hashlib.sha256(OUTPUT.read_bytes()).hexdigest()}, ensure_ascii=False))

if __name__ == "__main__":
    main()
