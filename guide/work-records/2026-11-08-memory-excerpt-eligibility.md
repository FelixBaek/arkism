# Eligible conversation excerpts / 관련성 검사를 통과하는 대화 발췌

Task: memory-quality follow-up, conversation excerpt eligibility.
Research: none
Why: a rare single-concept turn could win excerpt scoring, then fail the lexical gate and hide an entire source despite an eligible complete turn fitting the same budget.
Verified: the synthetic regression failed before the fix; focused post-fix PostgreSQL and conversation checks passed in 5.287s.
Limits: this corrects excerpt selection only. It does not establish a recall percentage, change temporal policy, expand budgets, or evaluate live models.

제목: 관련성 검사와 대화 발췌 선택 기준 일치
이유: 드문 단어 하나가 있는 구간을 선택한 뒤 전체 대화가 관련성 검사에서 탈락하는 누락을 막는다.
검증: 수정 전 합성 회귀 검사에서 누락을 재현했다. 수정 후 실제 DB·대화 전달 선택 검사가 5.287초에 통과했다.
한계: 발췌 선택만 수정하며 검색 품질 백분율 달성이나 운영 모델 성능을 주장하지 않는다.
연구: 해당 없음

## Change / 변경

The existing sliding window now prefers complete, contiguous turns with enough
distinct query concepts to pass the downstream lexical gate, then uses its
existing inverse-frequency score and tie-break. Alias spellings count as one
concept. If no qualifying window exists, the existing best-score fallback stays.
The scan remains O(lines × query terms); the caller's 600-rune excerpt budget and
shared conversation budget are unchanged. Private redaction still precedes
scoring. Neighbor stitching still refuses to cross an omitted part of a batch.

관련성 검사에 필요한 서로 다른 개념 수를 만족하는 연속 구간을 먼저 선택하고,
그 안에서는 기존 점수와 동률 규칙을 적용한다. 사본·백업 같은 별칭을 중복
개념으로 세지 않는다. 적합한 구간이 없으면 기존 대체 선택을 유지한다.
글자 예산, 비공개 내용 제거 순서, 생략 구간을 가로지르는 이웃 대화 연결 금지는
바꾸지 않았다.

## Verification / 검증

- Before the fix, `TestConversationExcerptPrefersEligibleWindow` failed as
  expected: the one-concept rare turn displaced a valid two-concept turn.
  Log: `/tmp/arkism-memory-quality-20261108/excerpt-eligibility-before.log`.
- After the fix, scoped `go test ./internal/memory` passed in 5.287s with
  the selection `^Test(Conversation(QueryExcerpt|Excerpt|MiddleExcerpt|Neighbor|LaneReturns)|PostgresConversation(Excerpt|Neighbors)|ContextDeliveryConversationHTTPAndBudget)`.
  It covered the new failure case through an actual isolated PostgreSQL source
  search, alias concepts, unchanged fallback and ranking among eligible windows,
  private redaction, late/middle excerpts, neighbor boundaries and shared budgets,
  scoped PostgreSQL neighbor access, and HTTP context delivery/token accounting.
  It ran under a 6 GiB systemd scope and 900-second deadline using the disposable
  test database. Log:
  `/tmp/arkism-memory-quality-20261108/excerpt-eligibility-after.log`.

No benchmark question/answer rules, source data, evaluation snapshots, release
builds, external model calls, publication, or production activation were changed
or performed. The fixtures use generic synthetic conversation text only.

벤치마크 문항·정답별 규칙, 원본 자료, 평가 스냅샷을 바꾸지 않았다. 제품 빌드,
외부 모델 호출, 공개 게시와 운영 적용도 수행하지 않았다.
