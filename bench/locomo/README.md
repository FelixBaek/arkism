# LoCoMo 벤치마크 하네스

누구나 같은 숫자를 다시 낼 수 있게 만든 Arkism용 [LoCoMo](https://github.com/snap-research/locomo) 측정 도구다.
경쟁 제품들이 내는 LoCoMo 점수는 대개 다시 돌려 볼 방법이 없다. 여기서는 커밋, 데이터셋 해시, 모델, 설정,
문항별 답과 채점을 모두 결과 파일에 남긴다.

## 무엇을 재나

1. **수집** — 대화마다 세션을 하나씩 Arkism의 실제 수집 경로에 넣는다. drain과 같은 추출 프롬프트·스키마·모델·주장 검사
   (`internal/host/capture.ExtractClaims`, `bench_export.go`)로 주장을 뽑고, `POST /v1/capture`로 보낸다.
   - 화자 이름은 본문에 남긴다(`Caroline: ...`). 사진은 캡션으로 `[shares a photo: ...]`.
   - 이벤트 `occurred_at`과 추출 프롬프트의 TODAY는 세션 날짜다.
   - EXISTING MEMORIES는 drain처럼 같은 범위를 검색해 넣는다(앞 세션에서 생긴 기억이 보인다).
   - 기본은 세션 전체를 보낸다. `--cue-filter`를 주면 훅처럼 cue 정규식에 걸린 줄만 보낸다.
2. **검토 변형** — `--variant approve-all`은 후보를 전부 검토 API(`POST /v1/memory_candidates/{id}/review`, `approve`)로
   승인한다(상한). `review`는 서버가 정한 대로 둔다.
3. **질문** — 범주 1~4만 쓴다(5번 adversarial 제외, mem0 방식). 질문마다 `POST /v1/search`(limit 10, 수집 클라이언트와
   같은 모드)로 기억을 찾고, **찾은 기억 텍스트와 질문만** 답변 모델에 준다. 기억 앞에는 그 기억의 근거 이벤트 날짜
   (source의 `collected_at`)를 붙인다. 채점은 mem0식 LLM 판정(CORRECT/WRONG, 표현 차이에 관대) → 0/1.
4. **기준선** — 같은 실행에서 `no-memory`(아무것도 안 줌)를 기본으로 함께 잰다. `--full-context-baseline`이면
   `full-context`(수집한 세션 전체)도 잰다.

모델은 모두 로컬 Claude Code CLI로 돈다(`ARKISM_CAPTURE_CLAUDE` 또는 `claude`, `-p --model M --output-format json`).
API 키가 필요 없다. 추출 모델은 수집과 같은 `ARKISM_CAPTURE_MODEL`(기본 haiku)이다.

## 데이터셋과 라이선스

LoCoMo는 **CC BY-NC 4.0**(비상업)이다. 저장소에 넣지 않는다. 실행할 때 고정 커밋에서 받는다.

- URL: `https://raw.githubusercontent.com/snap-research/locomo/cbfbc1dba6bc53d00625212a0f22d55ffee7c1fc/data/locomo10.json`
- sha256: `79fa87e90f04081343b8c8debecb80a9a6842b76a7aa537dc9fdf651ea698ff4` (맞지 않으면 실행하지 않는다)
- 캐시: `<사용자 캐시>/arkism-bench/locomo/` (`--cache-dir`로 바꿈), 이미 받은 파일은 `--dataset`으로 지정 가능

## 범위(scope)

대화끼리 기억이 섞이면 안 되므로 대화마다 프로젝트를 따로 쓴다: workspace `default`, project `locomo-<sample_id>`
(`--project-template`, `{sample}` 치환). 내장 런타임의 owner 토큰은 `default/default`에만 등록돼 있으므로 이 프로젝트들을
owner·worker 토큰에 따로 부여해야 한다. `run-runtime.sh`가 `memory-principal`로 이 일을 하고, 서버와 worker를 띄운 뒤
하네스를 실행하고, 끝나면 모두 내린다. 측정은 **새 데이터 디렉터리**에서 하는 것을 원칙으로 한다(기존 기억이 섞이지 않게).

## 실행

```sh
# 1) 새 런타임 데이터 디렉터리 (Postgres는 root로 못 돈다: 데이터 소유자로 실행)
arkism-desktop --runtime-check /path/to/data
# 2) 하네스 빌드 후 런타임과 함께 실행
go build -o /path/to/data/locomo-bench ./bench/locomo
ARKISM_CAPTURE_CLAUDE=$(command -v claude) bench/locomo/run-runtime.sh STAGE /path/to/data "conv-26" -- \
  bash -c '/path/to/data/locomo-bench --url "$BENCH_URL" --token-file "$BENCH_TOKEN_FILE" \
    --conversations 1 --sessions 3 --questions-per-conv 15 --answer-model haiku --out /path/to/out'
```

이미 떠 있는 서버라면 직접:

```sh
go run ./bench/locomo --url http://127.0.0.1:PORT --token-file F [--conversations N] [--sessions N] \
  [--questions-per-conv N] [--variant approve-all|review] [--answer-model haiku|sonnet] [--judge-model M] \
  [--full-context-baseline] [--concurrency 2] [--out DIR]
```

- `--out`에 `run.json`과 `checkpoint.jsonl`이 남는다. 같은 `--out`으로 다시 실행하면 끝난 세션·문항은 건너뛴다
  (설정이 다르면 거부). CLI 호출은 시간 제한(`--timeout`, 기본 180초)과 재시도(`--retries`, 기본 2)를 한다.
- 결과: `results.json`(문항별 행, 범주별·전체 정확도, 호출 수, 시간)과 `report.md`.
- 토큰은 어디에도 출력하지 않는다.

## 알려진 한계

- 추출 프롬프트는 "코딩 에이전트에게 사용자가 한 말"용이다. 일상 대화인 LoCoMo에서는 세션마다 주장이 적게 나온다
  (스모크: 세션 3개에서 7개). 점수는 제품 그대로의 수치이고, 대화 기억 특화 시스템과 같은 조건이 아니다.
- 기본 런타임은 임베더 없이 키워드 검색이다(`MEMORY_EMBEDDER=none`). 의미 검색을 재려면 Ollama 임베더를 켜고 돌린다.
- 문맥 토큰은 글자 수 ÷ 4 추정치다. CLI가 알려 주는 답변·채점 비용은 남기지만 추출 비용은 집계하지 않는다.
- LLM 채점은 같은 계열 모델이라 관대할 수 있다. 문항별 답과 판정 이유를 남겨 사람이 다시 볼 수 있게 했다.

## English

A reproducible LoCoMo harness for Arkism. Each session goes through Arkism's real capture path (the drain's extraction
prompt, schema, model and claim checks, then `POST /v1/capture` with the session date as `occurred_at`); the
`approve-all` variant approves every candidate through the review API, `review` leaves the server's decision. Each
category 1–4 question (adversarial category 5 excluded, as in mem0) is answered from only the `POST /v1/search` hits
(limit 10) and graded CORRECT/WRONG by an LLM judge; a `no-memory` baseline (and optionally `full-context`) runs
alongside. All models run through the local Claude Code CLI, so no API key is needed. The CC BY-NC 4.0 dataset is
never committed: it is downloaded at run time from a pinned commit and verified against a pinned sha256. Runs resume
from `checkpoint.jsonl`, and `results.json` / `report.md` record commit, models, settings, dataset hash, counts,
per-category accuracy, retrieved-context size and wall time.
