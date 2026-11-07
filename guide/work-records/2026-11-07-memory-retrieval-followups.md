# Retrieval follow-ups R1–R6 / 검색 추가 개선 R1–R6

Task: MEM-IMPROVE-20261107-R1-R6
Research: none
제목: 날짜 누락·그래프 불안정·문자 정규화·복수 기간·비유한 가중치 개선과 벡터 규모 측정

Why: Candidate limits applied before event-time precedence lost valid answers. Graph fan-out limits selected different neighbors under different physical row orders. Canonically equivalent text had different lexical tokens, and NaN bypassed range comparisons. Explicit disjoint time periods also needed a bounded API. Vector scaling needed measured evidence before any indexing decision.
이유: 실제 날짜가 맞는 기억 누락, 저장 순서에 따른 그래프 후보 변화, 같은 글자의 조합 방식 차이, NaN 허용을 재현해 수정했다. 복수 기간은 모호한 자연어 추정 없이 명시 요청으로 제공하고 벡터 규모별 지연은 합성 자료로 측정했다.

## Implemented contracts / 구현 계약

| ID | Change and expected benefit / 변경 및 기대 효과 | Boundary / 경계 |
| --- | --- | --- |
| R1 | Stream the SQL time-window superset and retain the best eligible K after the existing event-time precedence/liveness checks. 창 밖 사건을 가진 최근 수정 행 300개 뒤의 실제 정답도 찾는다. | Retained hits are bounded by K; scanned rows and decoding work remain linear in the SQL superset. Request cancellation stops the query. This is a correctness fix, not a constant-time latency claim. |
| R2 | Order all four graph LATERAL branches before fan-out LIMIT. 같은 관계를 역순으로 저장해도 같은 후보를 선택한다. | The existing fan-out cap remains; this does not return every neighbor. Lexicographic deterministic selection can exclude a relevant high-ID neighbor. |
| R3 | NFC-normalize both document/query tokens and retain attached combining marks. Derived token version 2 and resumable backfill refresh ordinary memory, source, observation and knowledge projections. | Source payload and revision stay unchanged. Old version 1 rows may miss canonical variants until backfill completes. This does not remove accents, use NFKC compatibility folding, or alter exact keyword metadata. |
| R4 | `temporal_windows` adds 1–4 explicit `[from,to)` windows to SearchInput and ContextBuildInput. Union/dedup produces one temporal lane; overlap cannot multiply RRF support. Explain reports `time_windows`. | Mutually exclusive with `temporal_window`; empty/reversed lists, history and relationship searches are rejected. Other lanes can still retrieve out-of-window facts. The space temporal-off policy remains authoritative. No new natural-language date guessing. |
| R5 | Reject NaN and infinities in lane weights, including environment parsing. | Finite boundary values and default weights remain unchanged. JSON already rejects nonfinite numbers. |
| R6 | Add an opt-in isolated synthetic exact-vector scale measurement with known top-three answers and a foreign-scope distractor. | No ANN index or production strategy change. Production data and workload evidence remain unavailable; the operational ANN decision remains deferred. |

R4 explicitly requested base modes may also use the explicit windows; graph expansion is not implicitly enabled. Agent-pinned context rejects these unsupported temporal options. Native serving falls back to baseline instead of ignoring either single or multiple explicit windows. Existing best-effort policy may skip an unavailable window and reports the degraded temporal lane; authorization, cancellation and source validation still fail closed. A caller requesting comparisons must supply actual time boundaries; returning facts is not a generated comparative answer.

R3 uses the coordinated migration `0008_search_tokens_nfc.sql`: existing rows default to version 1, future inserts default to version 2, and replacements mark current tokens version 2. The backfill's 500-row batches use the complete scope key and expected revision, report pending skipped revisions for a subsequent call, and do not rewrite canonical content. The migration and manifest are integrated separately by the coordinator. No automatic production backfill was executed.

## Verified / 검증

- Before/after failure evidence: restoring the prior temporal/graph/weight implementations and prior normalization made the new regression tests fail: temporal result empty, graph forward insertion `n001..n020` versus reverse `n041..n060`, NFC/NFD mismatch for accented text, and `graph=NaN` retained. Restoring the fixes passed these tests.
- Actual isolated PostgreSQL and service tests: temporal answer recovery and malformed textual date fallback; cancellation; graph physical-order independence; 511 old-version memory/knowledge rows across a batch boundary; zero-budget resumability, replay, lexical retrieval and unchanged canonical record.
- Final focused run after expanded coverage passed in 0.918s: six top-level tests, all four graph branches (outgoing, incoming, shared-topic memory fan-out and topic fan-out), plus actual native baseline fallback and pinned-context rejection. The optional scale test was skipped in this final focused run; its separate measurements remain below.
- Actual HTTP context request returned facts from two separate periods in both selected items and final ContextData, with two explained windows. A fixed query clock proved duplicate-window scores unchanged. Empty, conflicting and over-limit inputs were rejected.
- Related regression selection passed: 93 top-level tests, 11.578s. Two explicit opt-ins were skipped: the unrelated recall-quality export and the scale measurement (the scale measurement was run separately below). Selection: `TestRetrieval`, `TestRecall`, `TestSearchTokens`, `TestPostgresRecall`, `TestPostgresSearchKorean`, `TestKnowledgeFTS`, `TestContextDelivery`, `TestSchemaMigrationFiles`, `TestNativeSearch`.
- The opt-in scale run plus all then-current retrieval follow-up tests passed in 15.619s. Each scale also passed an authorized full-service vector search with the expected top three. The 192 measured repository searches all preserved the expected top three, and a foreign-scope perfect-distance row did not displace them.

검증: 위의 기존 실패 재현과 수정 후 실제 DB·서비스·HTTP 검사 결과를 구분했다. 511개 인덱스 재생성은 원문과 revision을 유지했고, 복수 기간의 최종 문맥 전달과 중복 점수 불변을 확인했다. 미실행 검사를 통과로 기록하지 않았다.

### Synthetic vector observations / 합성 벡터 관측

1024 dimensions, known angular neighbors, exact scan, 32 queries per cell, runtime parallelism 14. Times are milliseconds from the local temporary PostgreSQL run; no service-level latency assertion is attached.

| Rows | Concurrency | p50 ms | p95 ms | Correct top 3 |
| ---: | ---: | ---: | ---: | ---: |
| 128 | 1 | 5.173 | 6.361 | 32/32 |
| 128 | 4 | 6.949 | 12.793 | 32/32 |
| 1024 | 1 | 39.077 | 48.207 | 32/32 |
| 1024 | 4 | 46.329 | 51.480 | 32/32 |
| 4096 | 1 | 158.617 | 167.538 | 32/32 |
| 4096 | 4 | 173.030 | 176.550 | 32/32 |

Reproduce against an isolated test database with `POSTGRES_TEST_DSN` configured:

```sh
ARKISM_VECTOR_SCALE_AUDIT=1 systemd-run --user --scope -q -p MemoryMax=6G timeout 900 go test ./internal/memory -run '^TestRetrievalVectorScaleSynthetic$' -count=1 -v
```

Limits: Synthetic vectors occupy a two-dimensional subspace of 1024 dimensions, run on a shared local machine, and do not model realistic tenant distribution, embedding semantics, cache behavior or production concurrency. The observed growth is evidence of work scaling for this fixture only. Model answer quality, operational latency, ANN recall and deployment were not tested. Builds, model calls, pushes and production access were not performed.
한계: 합성 규모 측정은 운영 품질·지연·ANN 효과의 증거가 아니다. 실제 자료가 필요한 운영 인덱스 결정은 계속 보류한다. 시간 검색은 정확성을 위해 후보 전체를 검사하므로 큰 범위의 비용이 남는다. 제품 빌드·실제 모델·운영 접근·게시를 실행하지 않았다.
