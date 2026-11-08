# Candidate coverage experiment / 기억 검색 후보 폭 실험

Task: MQ-R1; MQ-R2 feasibility diagnostic.
Why: separate candidate coverage from query expansion and final evidence selection.
Status: implementation and focused checks complete; full HTTP quality acceptance pending.

## Design / 설계

`recall_quality.candidate_pool` is an independent, opt-in comparison setting:
0 preserves the existing behavior; 50 and 100 bound each ordinary memory search
lane and the fused memory pool, plus conversation source retrieval and fusion.
The final item and character budgets do not grow. The option does not enable
query expansion, a model, new scoring or a different excerpt selector.

Historical, relationship, frozen-context and non-memory searches retain their
existing limits. Provisional candidates retain their separate small pool. Partial
cross-encoder/model/decision reranking retains the previous fused-head bound, so
unscored candidates cannot displace scored rows. Consequently an experiment that
claims a complete 100-row memory pool must disable those partial evaluators.

이유: 후보 폭의 효과를 질문 확장·선택 점수 변경과 분리해 측정한다. 기본값은
0이며 기존 동작을 유지한다. 50·100은 최종 출력 수가 아니라 내부 후보 한도다.
권한·원본 생존·scope 재검사와 최종 출력 예산은 기존 경로를 사용한다.

## Research relation and deviation / 연구 관계와 차이

This is a candidate-coverage ablation motivated by a local retrieval architecture
review, not a reproduction or adoption of an external model. A separate private,
gold-aware oracle found feasible evidence sets inside frozen vector candidate pools.
No labels, oracle choices or private source text enter the product implementation.
The live hybrid search pool is not assumed identical to that offline vector pool.

The oracle scored451 questions out of453. Under the existing approximate evidence
mapping and exported full texts, feasible 10-memory/5-conversation selections reached
the candidate-union bound for every scored question. At100+100 candidates the category
bounds were98.7455% multi-hop,100% temporal,95.3125% open-domain and99.5816% single-hop.
At50+50, open-domain's bound was88.28125%. These are **gold-aware diagnostic bounds,
not measured service quality**. Missing original final response bodies and some
referenced sources prevent a complete exact-span audit of the prior results.

연구 차이: 정답을 아는 검산은 구현에 사용하지 않는다. 100+100 후보 안에서
출력 예산을 지키며 근거를 고를 수 있음을 확인했지만, 정답을 모르는 실제
검색기가 같은 선택을 할 수 있다는 뜻은 아니다. MQ-R2 집합 선택은 별도 작업이다.

## Verification / 검증

- Focused Go race checks:19 tests passed,0 skipped,1.271s. Covers settings
  validation and reset, independent width, excluded contexts, a useful fact
  beyond rank50, unchanged provisional limits and partial-reranker protection,
  plus existing local-expansion and conversation-selection regression tests.
- API/SDK generator checks:11 tests passed, including TypeScript acceptance of
  0/50/100 and rejection of unsupported values. DTO/API matching covered102
  operations; OpenAPI structure covered435 operations. These are structural
  checks, not435 executed HTTP requests. Generated SDK check and diff check passed.
- Private oracle:5 constructed checks and120 randomized exhaustive comparisons
  passed;90,600 candidate text hashes checked; the full diagnostic took0.60s.
  No new embedding/model inference, database or external provider was used.

Initial checks exposed two test-authoring errors (SpaceView configuration nesting
and zero-value block normalization); both were corrected before the passing run.
The contract generator first encountered the shell's older Go version; rerunning
with the already installed matching toolchain succeeded without downloading tools.

검증 한계: 위 기능 검사는 분야별90% 달성의 증거가 아니다. 실제 HTTP 품질
비교와 원래 기준의 회귀 gate는 아직 실행 중/준비 단계이며 통과로 표시하지 않는다.
Product/release builds, deployment and publication were not performed.

Executed commands (the matching Go toolchain was already installed):

```sh
GOTOOLCHAIN=local GOPROXY=off GOMAXPROCS=2 go test -race ./internal/memory -run '^(TestRecallCandidateWidth.*|TestLocalCandidatePool.*|TestLocalQuality.*|TestRecallQualitySettingsValidate|TestRecallQualitySpaceDefaults|TestConversationSelection.*)$' -count=1 -timeout=180s -json
GOTOOLCHAIN=local GOPROXY=off GOMAXPROCS=2 go vet ./internal/memory
git diff --check
```

Contract generation and verification used the same cached Go toolchain on PATH:

```sh
systemd-run --user --scope -q -p MemoryMax=6G timeout 900 env GOPROXY=off python3 tools/update_api_contracts.py
python3 tools/generate_sdk.py
systemd-run --user --scope -q -p MemoryMax=6G timeout 900 env PYTHONDONTWRITEBYTECODE=1 TSC_BIN="$CACHED_TSC_BIN" python3 -m unittest tools/test_update_api_contracts.py tools/test_generate_sdk.py
systemd-run --user --scope -q -p MemoryMax=6G timeout 900 env GOPROXY=off python3 tools/update_api_contracts.py --check
python3 tools/check_openapi.py
python3 tools/generate_sdk.py --check
```

`CACHED_TSC_BIN` denotes the already installed TypeScript5.9.3 executable; no
package install or download occurred. The existing SDK loopback test emitted
one ResourceWarning without a test failure. Repeated generator/test checks are
not added to the unique11-test count.

Scoped vet passed. The private oracle was run with `python3 oracle.py` from its
private diagnostic directory; its source, input hashes, tests and witnesses are
retained locally and are not public benchmark fixtures.

Review: standards review requested the explicit commands above. Specification
review found the engine behavior consistent with MQ-R1, but stage-level candidate
ID/hash/drop diagnostics are still pending. Existing explain output truncates lane
details and cannot certify all100 candidates. The prepared HTTP replay must retain
raw responses and distinguish observed candidate loss from unobserved stages.
