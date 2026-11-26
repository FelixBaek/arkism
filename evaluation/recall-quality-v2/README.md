# 사람 라벨 기억 회상 평가셋 v2

#357 · #366. v1(`evaluation/recall-quality-v1`, 로컬 main에만 있는 합성 36건)은 에이전트가 쓴 라벨이었다.
v2는 소유자가 실제로 던진 질문에 소유자가 직접 "어떤 기억이 나와야 했는지"를 라벨한다.
질문과 라벨은 held-out 평가 자료이므로 **저장소 밖**에 둔다([평가 가이드 §2](../../guide/memory-evaluation.md#2-freeze-the-experiment-before-collecting-results)).
이 폴더에는 도구, 양식, 그리고 집계 결과만 둔다.

## 파일과 위치

| 무엇 | 어디 | 공개 |
| --- | --- | --- |
| 질문 추출기 `extract_queries.py` | 이 폴더 | 공개 |
| 라벨 도구 `label.py` | 이 폴더 | 공개 |
| 러너 `tools/evaluate_semantic_recall.py --labels` | `tools/` | 공개 |
| 질문 `queries.jsonl`, 라벨 `labels.jsonl`, 검색 결과 `runs-*.jsonl` | `~/.local/state/arkism/recall-quality-v2/` (모드 0700) | 비공개 |
| 집계 결과 `results/<날짜>.json` | 이 폴더의 `results/` | 공개(집계만, 질문 본문 없음) |

## 양식

`queries.jsonl` 한 줄: `{"id", "query", "session", "day", "project"}`. `session`과 `project`는 해시 앞자리이고 `day`는 날짜만 남긴다.
본문은 `extract_queries.py`가 이메일·전화번호·URL·IP·홈 경로·토큰 모양 문자열을 가린 뒤의 문장이다. 사람 이름과 제품 이름은 자동으로 가리지 않는다. 그래서 비공개다.

`labels.jsonl` 한 줄(라벨 양식):

```json
{"id": "q-0001", "query": "…", "expected_memory_ids": ["mem-id-1"], "labeler": "felix", "labeled_at": "2026-10-27T12:00:00Z", "note": "", "shown": ["mem-id-1", "mem-id-7"]}
```

- `expected_memory_ids`: 이 질문에 기억이 돌려줬어야 하는 기억 ID. 빈 배열은 "아무것도 돌려주면 안 된다"는 뜻이다.
- `shown`: 라벨할 때 검색이 보여 준 후보 ID(순위순). 라벨 당시의 검색 결과 기록이며 정답이 아니다.
- 특정 기억이 꼭 필요하지 않았으면 `필수 기억 없음`을 입력한다. 도구는 해당 질문을 `dispositions.jsonl`에 기록하고 다음 실행에서 건너뛴다. 검색 결과가 비어야 한다고 판단한 경우에만 `-`를 입력해 음성 문항으로 채점한다.
- `label_mode: "blind"`: 검색과 자격 증명 읽기 없이 라벨했을 때만 기록한다. 이때 `shown`은 빈 배열이다.
- 라벨은 사람이 쓴다. 도구는 보여 주고 받아 적기만 한다.

`runs-<날짜>.jsonl` 한 줄: `{"id", "returned": ["mem-id", …]}` — 러너가 채점하는 검색 결과(순위순). `label.py run`이 만든다.
라벨과 검색 결과 각각에서 질문 ID는 고유해야 한다. 중복 ID가 있으면 채점을 중단한다.

## 절차

1. 추출: `python3 evaluation/recall-quality-v2/extract_queries.py` → 약 200건. `--limit`, `--seed`로 바꿀 수 있다.
2. 라벨: `python3 evaluation/recall-quality-v2/label.py label --limit 20` — 질문과 현재 검색의 상위 후보를 보여 주고, 순위 번호나 ID를 받는다. `-`는 "정답 없음", `s`는 건너뛰기, `q`는 종료.
   검색은 `ARKISM_URL`·`ARKISM_TOKEN_FILE`(기본값은 [서버 가이드](../../guide/server.md#configuration))로 읽기 전용 호출만 한다.
   독립 라벨을 만들 때는 후보 순위를 보기 전에 `label --blind --labels <새 비공개 파일>`을 실행한다. 사전에 준비한 자료에서 기억 ID를 확인해 입력한다. 증거가 부족한 질문은 건너뛰고, 관련 기억이 없다고 판단한 경우에만 `-`를 쓴다.
   나중에 판단할 질문은 `s`로 넘긴다. 직접 경험이 없으면 `해보지 않음` 또는 `해보지않음`을 입력한다. 이 응답은 비공개 `dispositions.jsonl`에만 남고, 해당 문항은 다음 실행에서 다시 묻지 않으며 채점 집계에서 제외한다.
3. 검색 결과 저장: `python3 evaluation/recall-quality-v2/label.py run` → `runs-<날짜>.jsonl`.
   같은 질문·라벨 파일, 연결 설정과 `--k`로 `run --fts-only`를 실행하면 전문 검색만 요청하고 모델 질의 확장과 재정렬을 끈 비교 결과가 `runs-<날짜>-fts.jsonl`에 저장된다. 두 결과 파일을 같은 라벨로 각각 채점할 때는 집계 파일이 같은 날짜를 사용하므로 `--out`에 서로 다른 출력 폴더를 지정한다.
   사용자 지정 라벨 파일(예: `team.jsonl`)은 판단 기록, 기대 답변, 검색 결과 파일 이름 앞에 `team.jsonl.`이 붙는다. 기존 공용 파일에 여러 평가자의 기록이 섞여 있다면 출처를 확인한 기록만 해당 평가자의 파일에 옮긴다.
4. 채점: `python3 tools/evaluate_semantic_recall.py --labels ~/.local/state/arkism/recall-quality-v2/labels.jsonl --runs …/runs-<날짜>.jsonl` → `results/<날짜>.json`.

라벨을 다 본 뒤 질문이나 라벨을 고치지 않는다. 고칠 것이 생기면 다음 버전 파일로 만든다.

## 수치

| 지표 | 뜻 |
| --- | --- |
| `recall_at_k` | 정답이 있는 질문 중 상위 k 안에 정답이 하나라도 든 비율 |
| `item_recall_at_k` | 정답 기억 전체 중 상위 k 안에 든 비율 |
| `mrr` | 첫 정답 순위의 역수 평균(정답 있는 질문만) |
| `precision_at_k` | 상위 k 반환 중 정답 비율(반환이 있는 질문만) |
| `negative_returned_at_k` | 정답이 없는 질문 중 상위 k에 무언가 돌려준 비율 |

**현재 수치: 없음.** 라벨이 아직 0건이다. 첫 결과 파일이 `results/`에 생기면 그 파일 이름과 숫자를 여기에 적는다. 문서에 적힌 숫자는 결과 파일에 있는 것만 옮긴다.

## 한계

- 질문은 소유자 한 사람의 코딩 세션에서 나왔다. 다른 사용자나 다른 언어의 분포를 대표하지 않는다.
- 라벨러가 한 사람이라 라벨 일치도를 잴 수 없다. 모호한 질문은 `s`로 건너뛰고, 기대한 주제만 알면 블라인드 모드에서 `topic: <주제>`로 남긴다.
- 라벨은 라벨 당시 활성 기억을 기준으로 한다. 삭제·대체된 기억을 제외할 때는 채점 전에 해당 질문을 제외한 별도 라벨 파일을 만든다.
