#!/usr/bin/env python3
"""One-shot public synthetic calibration; never edits server configuration.

Rule fixed before embedding: maximize labeled-pair recall with zero false
positives on this separate calibration set; use the midpoint between the last
retained positive and closest negative. Apply that one candidate to the frozen
holdout without tuning. Agent-authored labels are not human policy approval.
"""
import argparse
import json
import math
from pathlib import Path
from urllib.request import Request, build_opener
from evaluate_semantic_recall import MODEL, REVISION, QUERY_PREFIX, NoRedirect, distance

DOCS = [
    ("retry", "연결 시간이 초과된 요청은 같은 처리 식별자를 유지하고 대기 간격을 점차 늘려 다시 시도한다."),
    ("deletion", "계정을 지우면 그 사용자에게 속한 저장 데이터와 백업 사본을 모두 삭제한다."),
    ("credentials", "서비스 접속 비밀키는 환경 변수로 제공하며 소스 코드 저장소에는 올리지 않는다."),
    ("migration", "데이터베이스 구조를 바꾸기 전에 시험 환경에서 이관과 되돌리기를 검증한다."),
]
QUESTIONS = [
    ("retry", "네트워크 타임아웃이 나면 지수 백오프로 동일한 작업 키를 써서 재요청한다는 지침을 찾아줘."),
    ("retry", "통신이 끊겼을 때 같은 동작이 중복 실행되지 않게 복구하는 방법은?"),
    ("deletion", "회원 탈퇴 이후 개인정보의 복제본까지 남김없이 없애야 하나?"),
    ("deletion", "사용자가 서비스를 떠나기로 했을 때 남아 있는 기록을 어떻게 처리하지?"),
    ("credentials", "API 토큰을 깃에 커밋하는 대신 프로세스 설정으로 넘기라는 규칙이 있나?"),
    ("credentials", "인증용 시크릿을 프로그램에 안전하게 넣는 방식과 금지된 보관 장소를 알려줘."),
    ("migration", "스키마 마이그레이션과 롤백을 실서비스에 적용하기 전 어디서 확인해?"),
    ("migration", "테이블 설계를 변경하기 전에 원상복구가 가능한지 연습해야 하나?"),
    ("", "HTTP 상태 코드 418이 생긴 역사적인 이유가 궁금해."),
    ("", "축구 경기의 연장전과 추가 시간은 어떻게 달라?"),
    ("", "회원 가입 전환율을 높이기 위한 광고 문구가 필요해."),
    ("", "박물관의 역사 기록을 디지털 복제하는 기술을 설명해줘."),
    ("", "암호학의 공개키와 대칭키가 수학적으로 어떻게 다른가?"),
    ("", "피아노에서 장조와 단조의 조성을 구별하는 방법은?"),
    ("", "철새가 먼 거리를 이동하는 생태학적 원리는 무엇인가?"),
    ("", "SQL에서 INNER JOIN과 LEFT JOIN의 차이를 가르쳐줘."),
]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--holdout", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--reuse-calibration-vectors", action="store_true", help="replay the already frozen calibration against an updated holdout input contract")
    args = parser.parse_args()
    holdout = json.loads(args.holdout.read_text())
    if holdout["model"] != MODEL or holdout["revision"] != REVISION or holdout["query_prefix"] != QUERY_PREFIX:
        raise ValueError("holdout embedding identity mismatch")
    opener = build_opener(NoRedirect)
    def embed(texts, prefix):
        out = []
        for offset in range(0, len(texts), 4):
            batch = texts[offset:offset+4]
            req = Request("http://127.0.0.1:8093/embed", data=json.dumps({"inputs": [prefix+t for t in batch]}).encode(), headers={"Content-Type": "application/json"})
            with opener.open(req, timeout=60) as response:
                vectors = json.load(response)
            if len(vectors) != len(batch) or any(len(v) != 1024 or not all(isinstance(x, (int, float)) and math.isfinite(x) for x in v) for v in vectors):
                raise ValueError("invalid local embedding response")
            out.extend(vectors)
        return out
    if args.reuse_calibration_vectors:
        previous = json.loads(args.output.read_text())
        if previous.get("document_input_format") != "recordContent: text + newline + empty summary" or previous["space"] != holdout["space"] or [(d["id"], d["text"]) for d in previous["documents"]] != DOCS or [(q["relevant"][0] if q["relevant"] else "", q["text"]) for q in previous["queries"]] != QUESTIONS:
            raise ValueError("cached calibration contract changed")
        documents, queries = previous["documents"], previous["queries"]
    else:
        documents = [{"id": name, "text": text, "vector": vec} for (name, text), vec in zip(DOCS, embed([text+"\n" for _, text in DOCS], ""))]
        queries = [{"relevant": [name] if name else [], "text": text, "vector": vec} for (name, text), vec in zip(QUESTIONS, embed([text for _, text in QUESTIONS], QUERY_PREFIX))]
    positive, negative = [], []
    for q in queries:
        for d in documents:
            (positive if d["id"] in q["relevant"] else negative).append(distance(q["vector"], d["vector"]))
    closest_negative = min(negative)
    retained = [value for value in positive if value < closest_negative]
    if not retained:
        raise ValueError("calibration cannot separate any positive at zero false positives")
    cutoff = (max(retained) + closest_negative) / 2
    def metrics(docs, questions, limit):
        counts = {"positive_queries": 0, "target_found": 0, "negative_queries": 0, "negative_queries_with_result": 0, "returned": 0, "wrong_returned": 0}
        for q in questions:
            found = [d["id"] for d in docs if distance(q["vector"], d["vector"]) <= limit]
            counts["positive_queries"] += bool(q["relevant"])
            counts["negative_queries"] += not q["relevant"]
            counts["target_found"] += bool(set(found) & set(q["relevant"]))
            counts["negative_queries_with_result"] += not q["relevant"] and bool(found)
            counts["returned"] += len(found)
            counts["wrong_returned"] += len(set(found) - set(q["relevant"]))
        return counts
    result = {"model": MODEL, "revision": REVISION, "space": holdout["space"], "document_input_format": "recordContent: text + newline + empty summary", "labels": "agent-authored public synthetic; development only", "selection_rule": "maximum calibration pair recall with zero false positives; midpoint between retained-positive maximum and negative minimum; selected once before holdout evaluation", "candidate_max_distance": cutoff, "calibration_positive_distance_min": min(positive), "calibration_positive_distance_max": max(positive), "calibration_negative_distance_min": closest_negative,
              "calibration": metrics(documents, queries, cutoff), "holdout_current": metrics(holdout["documents"], holdout["queries"], .38), "holdout_candidate": metrics(holdout["documents"], holdout["queries"], cutoff), "documents": documents, "queries": queries}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, ensure_ascii=False, separators=(",", ":"))+"\n")
    print(json.dumps({key: value for key, value in result.items() if key not in {"documents", "queries"}}, ensure_ascii=False))


if __name__ == "__main__":
    main()
