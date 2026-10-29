# 사람 라벨 스킬 추천 평가셋 v1

#429. `POST /v1/skills/route`가 고른 스킬이 맞았는지 소유자가 직접 라벨해서 잰다.
질문은 [회상 평가 v2](../recall-quality-v2/README.md)가 이미 뽑아 가린 `queries.jsonl`을 그대로 쓴다.
질문과 라벨은 held-out 평가 자료이므로 **저장소 밖**에 두고, 이 폴더에는 도구와 집계 결과만 둔다.

## 파일과 위치

| 무엇 | 어디 | 공개 |
| --- | --- | --- |
| 라벨·실행·채점 도구 `route_eval.py` | 이 폴더 | 공개 |
| 질문 `queries.jsonl` | `~/.local/state/arkism/recall-quality-v2/` | 비공개 |
| 라벨 `labels.jsonl`, 실행 결과 `runs-*.jsonl` | `~/.local/state/arkism/skill-route-v1/` (모드 0700) | 비공개 |
| 집계 결과 `results/<날짜>.json` | 이 폴더의 `results/` | 공개(집계만, 질문 본문 없음) |

## 양식

`labels.jsonl` 한 줄:

```json
{"id": "q-0001", "query": "…", "expected_skill_ids": ["agent/pdf"], "labeler": "felix", "labeled_at": "2026-10-29T12:00:00Z", "note": "", "shown": ["agent/pdf", "agent/docx"]}
```

- `expected_skill_ids`: 이 질문에 불러왔어야 하는 스킬 ID(버전이 아니라 스킬). 빈 배열은 "스킬이 필요 없다"는 뜻이다.
- `shown`: 라벨할 때 판정 모델이 본 후보 스킬(적합도순). 정답이 아니다.

`runs-<날짜>.jsonl` 한 줄: `{"id", "selected", "offered", "provider", "degraded", "latency_ms"}`. `selected`가 빈 문자열이면 라우트가 고르지 않은 것이다.

## 절차

1. 라벨: `python3 evaluation/skill-route-v1/route_eval.py label --limit 20` — 질문과 후보를 보여 주고 순위 번호나 스킬 ID를 받는다. `-`는 "필요 없음", `s`는 건너뛰기, `q`는 종료.
2. 실행: `python3 evaluation/skill-route-v1/route_eval.py run` → `runs-<날짜>.jsonl`.
3. 채점: `python3 evaluation/skill-route-v1/route_eval.py score` → `results/<날짜>.json`.

라우트 호출은 에이전트의 `route_skills`와 같은 요청이다. 판정 모델을 한 번 부르고(사용량에 잡힌다) ROUTER 판정 기록에 한 줄을 남긴다. 그 밖의 쓰기는 없다.
`ARKISM_URL`·`ARKISM_TOKEN_FILE`·`ARKISM_WORKSPACE`·`ARKISM_PROJECT`·`ARKISM_USER`는 회상 평가 v2와 같고, `ARKISM_RUNTIME`을 주면 그 런타임에 맞는 스킬만 후보가 된다.

## 수치

| 지표 | 뜻 |
| --- | --- |
| `pick_accuracy` | 정답이 있는 질문 중 고른 스킬이 정답인 비율 |
| `wrong_pick_rate` | 정답이 있는 질문 중 다른 스킬을 고른 비율 |
| `withheld_rate` | 정답이 있는 질문 중 아무것도 안 고른 비율 |
| `candidate_recall` | 정답이 있는 질문 중 판정 후보 안에 정답이 든 비율(검색 단계의 한계) |
| `abstain_accuracy` / `unneeded_pick_rate` | 스킬이 필요 없는 질문에서 안 고른 / 괜히 고른 비율 |
| `latency_ms_p50` / `_p95`, `providers`, `degraded` | 응답 시간, 판정을 누가 했는지(`rules`는 판정 모델 대신 검색 1위), 낮아진 이유 |

`candidate_recall`이 낮으면 검색을, 그보다 `pick_accuracy`가 많이 낮으면 판정 모델을 고친다.

**현재 수치: 없음.** 라벨이 아직 0건이다. 첫 결과 파일이 생기면 그 파일 이름과 숫자를 여기에 적는다.

## 한계

- 질문은 소유자의 코딩 세션 질문이라 대부분 스킬이 필요 없다. 정답 있는 질문이 적으면 비율이 크게 흔들린다.
- 라벨러가 한 사람이라 라벨 일치도를 잴 수 없다.
- 스킬 목록·분석이 바뀌면 같은 라벨로도 결과가 달라진다. 결과 파일 날짜로 비교한다.
