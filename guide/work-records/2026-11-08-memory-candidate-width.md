# Candidate coverage experiment / 기억 검색 후보 폭 실험

Task: MQ-R1; MQ-R2 feasibility diagnostic.
Why: separate candidate coverage from query expansion and final evidence selection.
Status: implementation and focused checks complete; full paired HTTP comparison completed and rejected for quality regression. The default remains0.

## Completed actual HTTP comparison / 실제 검색 비교 완료

Each condition completed453 actual HTTP searches (451 scored questions), for906
successful searches total. The real search handler, isolated PostgreSQL18 and
restricted-role authorization/RLS were used. The embedder served only pinned
query vectors and refused cache misses; no new model inference or external
provider call occurred. The selected output remains10 memories/6000 text-plus-
summary characters and up to5 conversations within their existing shared budget.
The measured engine revision is `0f4dc305`; the later MQ-R2 model-selection
integration has separate functional verification and no completed quality score.

| Category / 분야 | Fresh pool0 baseline | Pool100 candidate | Acceptance |
| --- | ---: | ---: | --- |
| Multi-hop / 여러 기억 연결 |59.7312%|60.0358%|Below90%|
| Temporal / 날짜·시간 |91.0920%|89.5594%|Regression and below90%|
| Open-domain / 개방형 |61.4583%|61.7188%|Below90%|
| Single-hop / 단일 기억 |97.0711%|97.4895%|Pass|

The unchanged category gate rejected the candidate against both the fresh paired
baseline and the original frozen baseline (exit1). Across questions, the paired
comparison gained12 and lost12; temporal gained0 and lost3. Degraded responses0.
This option is retained for controlled experiments, **not adopted as a default
quality improvement**. Candidate widening alone did not solve evidence selection.

The fresh pool0 run is not an exact reproduction of every historic result:
multi-hop changed from59.5520% to59.7312%, with one question gaining recall and none
losing; the other three category scores match. Hit sets changed for39 questions,
order for192 and mapped evidence sets for38. The historic per-request clock was
not preserved, and the cause is not established. Both the original benchmark
and the new paired baseline remain in the comparison; neither is overwritten.
A subsequent read-fence timestamp investigation checked the original dump:
all1,157 memory rows and1,784 source rows already had millisecond SQL update
timestamps matching their document timestamps. Consequently the newly discovered
sub-millisecond fence precision risk is not established as a cause of this
comparison difference; it must not be used to explain away the failed gate.

Search p50/p95 were0.600/0.915s for pool0 and1.099/1.620s for pool100. These are
sequential cached-embedding HTTP measurements on a shared host, not production
latency or comparisons to earlier live-embedding timings. Successful-search wall
time totaled806.61s. An earlier one-connection test-server attempt timed out with
503 after about45s; a fresh bounded eight-connection server completed the two
conditions. Pool starvation is a plausible explanation, not a measured wait-state
diagnosis. The earlier restore-target error and failed search remain recorded.

All1781 stored documents matched the original export,1157 referenced originals
were live, and1781 SQL embedding columns matched the stored float32 vector casts.
The906 request, response, query and cache hashes were checked; code and frozen
inputs remained unchanged. The two space settings differed only in candidate_pool.
The successful server recorded906 cache calls/0 misses; including the failed
server gives907 cache calls, still0 new inference. The first completed eligible
result was used; the first three successful probe rows were reused in the full run.
Diagnostic limit50 requests and a separate pool50 full run were not executed.
Raw responses are retained privately; no missing run is filled with invented data.

실제 검색 결과: 후보를 넓힌 것만으로는90%에 도달하지 못했고 시간 분야가
하락했다. 기본값은0으로 유지하며, 정답을 모르는 근거 집합 선택은 별도로
검증해야 한다. 새 근거 회수율은 답변 정확도나 미사용 자료 성적이 아니다.

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

검증 한계: 위 기능 검사는 분야별90% 달성의 증거가 아니다. 후속 실제 HTTP
비교와 원래 기준의 회귀 gate 결과는 문서 첫 표에 갱신했으며 미채택이다.
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

The HTTP replay used a private Go test overlay with the unchanged product handler,
not a newly built product executable. Its command was `go test -p 2 -v -overlay
<private-overlay> -run '^TestPrivateMQHTTPReplayServer$' -count=1 -timeout=60m
./internal/memory`. The private client ran453 questions at output limit10 for
pool0 and pool100; the scorer loaded gold only after responses were persisted.
Exact local commands, overlay versions and hashes are retained with those private
artifacts. Public gate commands, with private row paths parameterized, were:

```sh
python3 tools/check_recall_category_gate.py --baseline "$MQ_PAIRED_BASELINE_ROWS" --candidate "$MQ_POOL100_ROWS" --output "$MQ_PAIRED_GATE_OUTPUT"
python3 tools/check_recall_category_gate.py --baseline "$MQ_ORIGINAL_BASELINE_ROWS" --candidate "$MQ_POOL100_ROWS" --output "$MQ_ORIGINAL_GATE_OUTPUT"
```

Both returned1 for the expected quality rejection, with the unchanged frozen
question fingerprint. Full intermediate100-candidate stage/drop tracing remains
unimplemented; retained final bodies and original corpus do not establish it.

Oracle follow-up: the first diagnostic counted memory text only. A corrected
version includes text plus summary; the category bounds are unchanged and the
largest100-pool witness uses1014 memory characters. A separate validator checked
906 witnesses and1301 selected entries against468 unique raw records, including
lane, project, candidate membership, text hash, independently summed character
cost and count limits. It passed in0.38s with unchanged inputs and no inference,
database or network use. This validates the diagnostic, not service quality.
