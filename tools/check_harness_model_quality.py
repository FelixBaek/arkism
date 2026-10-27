#!/usr/bin/env python3
"""Check the Haiku quality evidence behind config/harness-quality.json, or probe the models to renew it.

The host (internal/host/capture/session_processor.go, sessionGateOK) lets Haiku run the session
processor for a category only when the evidence file carries the host's schema_version and
prompt_version and lists that category in passed_categories. A category is listed when every probed
model passed every assertion on the initial case and on both held-out variants.

`--check` (default, offline, free) re-derives passed_categories from the recorded results, rejects an
evidence file whose numbers disagree with its own rows, and says whether the host can use it at all.
`--probe` sends the synthetic fixtures below to the models named in config/harness-models.json through
the Messages API. It costs money, needs ANTHROPIC_API_KEY, writes a new evidence file and never runs in CI.
"""
import argparse
import datetime
import json
import os
import re
import sys
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
QUALITY = ROOT / "config/harness-quality.json"
MODELS = ROOT / "config/harness-models.json"
PROCESSOR = ROOT / "internal/host/capture/session_processor.go"
HELDOUT_VARIANTS = (2, 3)
API = "https://api.anthropic.com/v1/messages"

# Synthetic fixtures: a transcript slice and what a correct session_state / memory_candidates output must and
# must not say. Nothing here comes from a real session. The assertion names are the ones the evidence records.
#   semantic_case            every term in `expect` appears in some memory candidate statement
#   no_unobserved_completion no `in_progress` term appears under session_state.completed
#   no_unobserved_test_pass  no `unverified` term appears next to 통과/pass in session_state
FIXTURES = {
    "korean_correction": [
        {"transcript": "사용자: 보고서는 영어로 써 줘.\n에이전트: 알겠습니다.\n사용자: 아니, 영어 말고 한국어로. 앞으로 보고는 전부 한국어야.\n에이전트: 한국어로 쓰겠습니다. 테스트는 아직 안 돌렸습니다.",
         "expect": ["한국어"], "in_progress": ["테스트"], "unverified": ["테스트"]},
        {"transcript": "사용자: 커밋 제목은 대문자로 시작해.\n에이전트: 네.\n사용자: 정정할게. 소문자 type(scope) 형식으로 써. 대문자는 틀렸어.\n에이전트: 소문자 형식으로 바꾸겠습니다. 린트는 아직 확인 전입니다.",
         "expect": ["소문자"], "in_progress": ["린트"], "unverified": ["린트"]},
        {"transcript": "사용자: 배포는 금요일에 해.\n에이전트: 금요일로 잡겠습니다.\n사용자: 잘못 말했어. 금요일 배포는 금지야. 화요일에만 해.\n에이전트: 화요일로 바꾸겠습니다. 배포 스크립트는 아직 돌려 보지 않았습니다.",
         "expect": ["화요일"], "in_progress": ["배포 스크립트"], "unverified": ["배포 스크립트"]},
    ],
    "conditional_preference": [
        {"transcript": "사용자: 운영 서버를 만질 때만 변경 전에 나한테 확인받아. 테스트 서버는 그냥 해도 돼.\n에이전트: 알겠습니다. 운영 변경 전 확인, 테스트 서버는 바로 진행하겠습니다. 마이그레이션은 아직 실행 전입니다.",
         "expect": ["운영"], "in_progress": ["마이그레이션"], "unverified": ["마이그레이션"]},
        {"transcript": "사용자: 긴 글은 피곤하니까 보고는 세 줄로. 다만 장애 보고서는 자세히 써도 돼.\n에이전트: 평소 세 줄, 장애 보고서는 자세히 쓰겠습니다. 점검 스크립트는 아직 안 돌렸습니다.",
         "expect": ["장애"], "in_progress": ["점검 스크립트"], "unverified": ["점검 스크립트"]},
        {"transcript": "사용자: 외부 공개 저장소에는 영어 주석만. 사내 저장소는 한국어 주석이 좋아.\n에이전트: 공개는 영어, 사내는 한국어로 하겠습니다. 빌드는 아직 확인 전입니다.",
         "expect": ["공개"], "in_progress": ["빌드"], "unverified": ["빌드"]},
    ],
    "mixed_personal_work": [
        {"transcript": "사용자: 다음 주 수요일은 병원 가는 날이라 오후에 없어. 그리고 결제 모듈 리팩터링은 내가 돌아온 뒤 리뷰할게.\n에이전트: 수요일 오후 부재를 기억하고, 리팩터링 리뷰는 그 뒤로 두겠습니다. 리팩터링 테스트는 아직 미실행입니다.",
         "expect": ["수요일"], "in_progress": ["리팩터링"], "unverified": ["리팩터링"]},
        {"transcript": "사용자: 나는 커피를 안 마셔서 회의는 오전 10시 이후가 좋아. 로그인 버그는 오늘 안에 고쳐야 해.\n에이전트: 회의는 10시 이후, 로그인 버그는 오늘 처리하겠습니다. 수정은 아직 시작 전입니다.",
         "expect": ["10시"], "in_progress": ["로그인"], "unverified": ["로그인"]},
        {"transcript": "사용자: 주말엔 알림 보내지 마. 그리고 검색 색인 작업은 월요일 새벽에 돌려.\n에이전트: 주말 알림을 끄고 색인은 월요일 새벽으로 잡겠습니다. 색인 작업은 아직 돌리지 않았습니다.",
         "expect": ["주말"], "in_progress": ["색인"], "unverified": ["색인"]},
    ],
}
ASSERTIONS = ("semantic_case", "no_unobserved_completion", "no_unobserved_test_pass")
PASS_WORDS = re.compile(r"통과|\bpass(ed)?\b|성공", re.I)


def host_versions():
    """schema_version and prompt_version the host compares the evidence against."""
    source = PROCESSOR.read_text(encoding="utf-8")
    found = {name: re.search(rf'\b{name}\s*=\s*"([^"]+)"', source) for name in ("sessionSchemaVersion", "sessionPromptVersion")}
    if not all(found.values()):
        raise SystemExit(f"{PROCESSOR}: version constants not found")
    return found["sessionSchemaVersion"].group(1), found["sessionPromptVersion"].group(1)


def category_name(category, variant):
    return category if variant == 1 else f"{category}_variant{variant}"


def passed_cases(suite):
    """Categories whose every model result passed in one suite."""
    return sorted(c["category"] for c in suite.get("cases", []) if c["results"] and all(r["passed"] for r in c["results"]))


def derive_passed(gate):
    """passed_categories the host rule allows: passed initially and on every held-out variant."""
    heldout = set(passed_cases(gate.get("heldout", {})))
    return [c for c in passed_cases(gate) if all(category_name(c, v) in heldout for v in HELDOUT_VARIANTS)]


def problems(gate):
    """Rows that disagree with the file's own summaries. Empty means the file is consistent."""
    out = []
    for name, suite in (("initial", gate), ("heldout", gate.get("heldout", {}))):
        for case in suite.get("cases", []):
            for r in case["results"]:
                if set(r["assertions"]) != set(ASSERTIONS):
                    out.append(f"{name} {case['category']} {r['model']}: assertions are not {ASSERTIONS}")
                if r["passed"] != all(r["assertions"].values()):
                    out.append(f"{name} {case['category']} {r['model']}: passed={r['passed']} but assertions say {all(r['assertions'].values())}")
                if r.get("prompt_version") != gate.get("prompt_version"):
                    out.append(f"{name} {case['category']} {r['model']}: prompt_version differs from the file's")
                if not gate.get("model_discovery", {}).get(r["model"]):
                    out.append(f"{name} {case['category']} {r['model']}: model not in model_discovery")
    # Summaries are compared as sets: the file lists them in probe order, the host only tests membership.
    heldout = gate.get("heldout", {})
    for name, summary, rows in (("initial_passed_categories", gate.get("initial_passed_categories"), passed_cases(gate)),
                                ("heldout.passed_categories", heldout.get("passed_categories"), passed_cases(heldout)),
                                ("passed_categories", gate.get("passed_categories"), derive_passed(gate))):
        if sorted(summary or []) != rows:
            out.append(f"{name} {summary} != rows {rows}")
    return out


def gate_status(gate, schema, prompt):
    """What the host does with this file: the categories Haiku may take, or why none."""
    if gate.get("schema_version") != schema:
        return f"inactive: schema_version {gate.get('schema_version')!r} != host {schema!r}; every session call uses Sonnet"
    if gate.get("prompt_version") != prompt:
        return f"inactive: prompt_version {gate.get('prompt_version')!r} != host {prompt!r}; every session call uses Sonnet"
    return f"active for {gate.get('passed_categories')}"


def texts(value):
    """Every string inside a JSON value, joined, so a term check does not depend on the output's shape."""
    if isinstance(value, str):
        return value
    if isinstance(value, dict):
        return " ".join(texts(v) for v in value.values())
    if isinstance(value, list):
        return " ".join(texts(v) for v in value)
    return ""


def assess(output, fixture):
    """The three recorded assertions over one model output (parsed JSON)."""
    state = output.get("session_state", {}) if isinstance(output, dict) else {}
    candidates = texts(output.get("memory_candidates", [])) if isinstance(output, dict) else ""
    completed = texts(state.get("completed", []))
    claims = [line for line in re.split(r"[.\n]", texts(state)) if PASS_WORDS.search(line)]
    return {
        "semantic_case": all(term in candidates for term in fixture["expect"]),
        "no_unobserved_completion": not any(term in completed for term in fixture["in_progress"]),
        "no_unobserved_test_pass": not any(term in line for term in fixture["unverified"] for line in claims),
    }


def probe_one(model, system, fixture, key):
    body = json.dumps({"model": model, "max_tokens": 4096, "system": system, "messages": [{"role": "user", "content":
        "다음 원문 구간을 읽고 session_state와 memory_candidates를 JSON 한 덩어리로만 반환하라. 원문의 source_id는 s1, revision은 1이다.\n\n" + fixture["transcript"]}]}).encode()
    request = urllib.request.Request(API, data=body, headers={"content-type": "application/json", "x-api-key": key, "anthropic-version": "2023-06-01"})
    with urllib.request.urlopen(request, timeout=120) as response:
        reply = json.load(response)
    text = "".join(block.get("text", "") for block in reply.get("content", []))
    match = re.search(r"\{.*\}", text, re.S)
    try:
        output = json.loads(match.group(0)) if match else {}
    except json.JSONDecodeError:
        output = {}
    assertions = assess(output, fixture)
    return {"model": model, "passed": all(assertions.values()), "assertions": assertions, "model_id": reply.get("model", model),
            "token_usage": reply.get("usage", {}), "stop_reason": reply.get("stop_reason")}


def cost_of(result, price):
    """USD for one result from a [per-M-input, per-M-output] price, or None when the price is unknown."""
    if price is None:
        return None
    usage = result["token_usage"]
    return (usage.get("input_tokens", 0) * price[0] + usage.get("output_tokens", 0) * price[1]) / 1e6


def probe_case(name, fixture, models, system, key, prompt, prices):
    results = []
    for model in models:
        r = probe_one(model, system, fixture, key)
        r.update(prompt_version=prompt, operation_id=f"quality-{name}-{model}", cost_usd=cost_of(r, prices.get(model)))
        results.append(r)
    return {"category": name, "results": results}


def probe(models, prices):
    key = os.environ.get("ANTHROPIC_API_KEY")
    if not key:
        raise SystemExit("--probe needs ANTHROPIC_API_KEY")
    schema, prompt = host_versions()
    presets = json.loads(MODELS.read_text(encoding="utf-8"))["prompts"]
    system = presets["common"] + "\n" + presets["session_processor"]
    suites = {1: [], 2: [], 3: []}
    for category, fixtures in FIXTURES.items():
        for variant, fixture in zip((1, *HELDOUT_VARIANTS), fixtures):
            suites[variant].append(probe_case(category_name(category, variant), fixture, models, system, key, prompt, prices))
    cost = sum(r["cost_usd"] or 0 for cases in suites.values() for case in cases for r in case["results"])
    gate = {"schema_version": schema, "prompt_version": prompt, "cases": suites[1],
            "model_discovery": dict.fromkeys(models, True), "actual_cost_usd": cost,
            "limitation": "Three synthetic cases per category (one initial + two held-out). Passing them does not establish general Korean quality.",
            "evaluated_at": datetime.date.today().isoformat(), "input_data": "synthetic fixtures in tools/check_harness_model_quality.py",
            "heldout": {"schema_version": schema, "prompt_version": prompt, "cases": suites[2] + suites[3], "model_discovery": dict.fromkeys(models, True), "suite": "heldout"}}
    gate["initial_passed_categories"] = passed_cases(gate)
    gate["heldout"]["passed_categories"] = passed_cases(gate["heldout"])
    gate["passed_categories"] = derive_passed(gate)
    return gate


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--probe", action="store_true", help="call the models (paid) and write a new evidence file")
    parser.add_argument("--models", nargs="+", default=["claude-haiku-5-5", "claude-sonnet-5-5"])
    parser.add_argument("--prices", default="{}", help='JSON {model: [usd per M input tokens, usd per M output tokens]}; unknown models record cost null')
    parser.add_argument("--out", type=Path, default=QUALITY)
    args = parser.parse_args(argv)
    if args.probe:
        gate = probe(args.models, json.loads(args.prices))
        args.out.write_text(json.dumps(gate, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        print(f"wrote {args.out}: passed_categories={gate['passed_categories']} cost_usd={gate['actual_cost_usd']}")
        return 0
    gate = json.loads(args.out.read_text(encoding="utf-8"))
    found = problems(gate)
    for line in found:
        print(f"{args.out.name}: {line}", file=sys.stderr)
    print(f"{args.out.name}: {gate_status(gate, *host_versions())}")
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main())
