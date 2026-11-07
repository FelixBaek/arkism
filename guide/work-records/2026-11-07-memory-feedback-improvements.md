# Memory feedback reads, recovery policy, and evaluation history

Task: memory-audit-M04-M05-M07; related M01 directive status UI
Research: none

## Why

Candidate signal reads were sequential, a single historical failed evaluation kept a permanent penalty, and the detail panel did not expose the actual outcome history. This change keeps the existing default ranking contract and adds an explicit recovery option and navigable evidence.

- The primary store reads candidate signals in batches of at most 100. Stores without the optional reader retain their existing lookup path. Missing or failed reads remain neutral.
- `recall_quality.signal_policy` opts into one dimension, a recent window, and a minimum number of evaluation records. Example: `{"dimension":"work_task","window_days":30,"min_samples":3}`. Valid dimensions are `work_task` and `personal_compliance`; windows are 1–3650 days and minimum records are 3–1000. Omit the policy to retain legacy weighting.
- New independently checked outcomes record a hash of text, summary, kind, rule:always/rule:never tags, and memory context. Revision and observed-at metadata do not reset that content identity. The opt-in factor is `0.5 + (passed + 1) / (total + 2)` once the threshold is met; host work weighting still multiplies it. All historical records remain readable. One failure followed by 100 successes can recover under the opt-in policy; legacy weighting remains 0.115.
- The evaluation panel lazily reads 20 outcomes at a time, displays dimension, result, time and record version, opens the referenced task explanation, and links to the existing correction action. Record versions can also change for metadata. Errors have explicit retry buttons. Text is rendered as text and long task descriptions are marked as partial.

- Related M01: rule list/detail distinguish `needs_review`, `inactive`, and `unavailable` source states. The existing save endpoint performs explicit human approval as an independent manual rule; both direct approval and edit/save explain that future source changes will no longer suspend that rule. Agents have no approval button.

## Verified

- With a disposable local database: `systemd-run --user --scope -q -p MemoryMax=6G timeout 900 go test ./internal/memory -run 'TestSignal|TestMemoryOutcome|TestMemorySignal|TestOutcomeWeights|Test.*Recency' -v` passed (0.553s). The database tests ran rather than skipped.
- Actual `RecordMemoryOutcome` → stored content hash → `Search` reached the opt-in factor. Content change, metadata-only revision, observed-at changes, dimension, window, minimum configuration and host work preservation were checked. Existing outcome HTTP, scope and signal behavior tests also passed.
- The new batch regression test was also run against the unmodified sequential implementation through a source overlay: it failed as expected with zero batch calls and 201 individual reads. The worktree was not overwritten.
- The default search comparison fixed the clock and compared all 12 returned items, scores and explanations between sequential and batched readers. Results were identical, while signal lookups fell from 12 to 1. An initial comparison used a moving clock and correctly differed in recency scores; the fixture now holds time constant.
- A local synthetic 201-row database comparison returned identical weights: 201 sequential reads took 66.25ms and three batched reads took 9.15ms in one run. These measurements are not production latency estimates. A separate scope fixture refused a foreign-scope row; content/window filters retained one eligible record while unfiltered historical counts retained all three.
- `node --test desktop/ui/memory-insight.test.js desktop/ui/memory-views.test.js desktop/ui/memory-detail.test.js` passed all three files. The actual evaluation component and product styles were also rendered in a local browser using synthetic API replies: history failure/retry, pagination, old/current versions, opening an earlier page's trace, safe text rendering, scoped requests, keyboard correction callback, and narrow-screen overflow checks passed. All three suspended rule states were rendered in the browser, then approved by keyboard with the existing scoped revision-checked save payload. No external network was used.
- The local design checker was invoked on the changed UI files but supports only JSX/TSX/CSS; it reported no supported files for these JS/MJS changes. This is not a checker pass. The rendered panel and keyboard behavior were checked separately.

## Limits

This is an opt-in mechanical policy, not evidence of improved answer quality. Historical outcomes without a content hash are retained in history but excluded from content-specific weighting; they are not guessed or backfilled. The sample threshold counts evaluation events, not independent tasks, and host work observations keep their existing all-history semantics. The policy has no dedicated settings control or current-weight explanation panel yet. A complete model-free relevance benchmark and policy calibration remain separate work.

Browser verification covered the mounted evaluation component and correction callback with synthetic replies, not a full application/server correction submission. The backend route tests and browser tests are distinct boundaries. API/SDK contract generation is handled during integration. Product builds, model calls, production access and remote publication were not performed.

## 제목

기억 피드백 조회 일괄 처리, 선택형 회복 정책과 실제 평가 이력

## 이유

후보별 조회 비용을 줄이고, 실패 이력을 지우지 않으면서 내용·기간·차원에 맞게 평가를 적용할 선택지를 추가했다. 평가 화면에서는 실제 결과와 작업 근거를 확인하고 기존 내용 수정 동작으로 이동할 수 있게 했다. 기본 정책과 기존 업무 관측 가중치는 유지한다.

## 검증

제한된 메모리 기능·회귀 시험과 임시 DB 시험, 기본 검색 결과의 순위·점수·설명 동등성, 실제 평가 기록에서 검색으로의 연결을 확인했다. UI 세 시험 파일 및 로컬 브라우저의 재시도·페이지 추가·작업 근거·키보드 교정 연결·좁은 화면 검증이 통과했다. 상세 범위와 단회 측정값은 위에 기록했다. 코드 검사기는 변경 파일 형식을 지원하지 않아 통과로 표시하지 않았다.

## 한계

신규 정책은 명시적으로 설정해야 한다. 내용 해시가 없는 과거 평가는 이력에는 남지만 신규 순위 표본에는 포함하지 않는다. 평가 이벤트 수는 독립된 업무 수와 같지 않다. 전체 앱의 교정 제출이나 운영 성능·답변 품질 향상을 입증한 것은 아니다. API/SDK 계약은 통합 단계에서 갱신한다.

연구: 해당 없음

## M01 integration follow-up: active source links

An active linked rule also becomes independent when a human edits or disables it. The detail view now states this before the action, including that later source changes or expiry no longer control the rule. The existing backend and save payload are unchanged. The focused rule UI test and local browser checks passed, including keyboard disable, the existing revision/scoped request, and a 390px screen without horizontal overflow. The rendered active-link explanation was inspected separately. The local code checker still reports no supported files for JS; it is not recorded as a pass.

Read-only integration audit used a temporary source overlay rather than changing the integrated worktree: HTTP policy PATCH, GET, effective settings, invalid dimension/window/minimum/unknown field rejection and clearing to legacy all passed. Existing context-delivery HTTP and linked-rule lifecycle tests passed alongside it (0.146s). The audit found no additional confirmed implementation regression in those paths.

제목: 활성 연결 규칙의 수동 전환 의미 표시
이유: 원본이 유효한 규칙도 수정·끄기 때 연결에서 독립된다는 실제 동작을 사용자에게 알린다.
검증: 규칙 UI 시험과 로컬 브라우저 키보드 끄기·요청값·좁은 화면 검증 통과. 통합본 정책 설정 HTTP 경계는 임시 overlay로 검증했다.
한계: 동작 안내만 바꿨고 백엔드 정책은 유지했다. 브라우저는 합성 API 응답이며 운영 사용을 입증하지 않는다.
