# Memory feedback and consumer follow-ups

Task: MEM-FOLLOWUPS-F4
Research: none

## F4: preserve composed request types

Why: Conditional allOf branches caused the SDK generator to discard sibling object properties, leaving real search and context request types as unknown. The generator now intersects known sibling fields with representable composition branches; unsupported conditions remain conservative.

Verified: Three focused generator/type tests passed. A temporary generated client compiled with valid scoped search/context inputs and required compiler errors for missing scope, string token budget, and non-boolean option. The same acceptance test against the original generator failed on unused expected-error directives, reproducing missing type checks. No emitted product artifacts or remote calls were used.

Limits: Conditional constraints that cannot be expressed by the generator remain server-validated. Generated committed API/SDK artifacts are synchronized during integration. Type checking does not execute the service.

제목: 조합 스키마의 검색·문맥 요청 타입 보존
이유: allOf 처리 중 형제 객체 필드가 사라지는 문제를 수정했다.
검증: 생성기 시험과 임시 생성 SDK의 유효·무효 요청 컴파일 검증 통과. 기존 생성기로 타입검사 누락을 재현했다.
한계: 지원하지 않는 조건은 서버 검증을 유지하며 생성물은 통합 단계에서 동기화한다.
연구: 해당 없음

## F1/F2: first evaluation per trace and visible batched signals

Task: MEM-FOLLOWUPS-F1-F2
Research: none

Why: Repeated evaluation event IDs on one trace must not satisfy the opt-in policy's evidence threshold, and public signal reads should not add one derived-row query per visible memory. The opt-in policy now picks the earliest server-recorded completed evaluation, with ID as a deterministic timestamp tie-break, for each content identity/dimension/trace ID. It selects before applying the time window, so a later duplicate cannot refresh expired evidence. Unknown content or trace attribution does not become a guessed independent sample. Public history, legacy weights, and all stored events remain unchanged.

The public signal API checks each memory's visibility first, then batches only visible signal IDs. Request order, missing signals, unavailable memories, strict storage errors and scope/malformed-row rejection are preserved. Stores without the optional batch reader keep their previous lookup behavior.

Verified: Scoped independent-outcome, public-signal, signal-policy and existing signal tests passed with a disposable database (0.199s). A real outcome→Search sequence confirms four events from one trace retain all four historical records but one failed eligible result and do not satisfy the minimum threshold. Separate completed traces still reach Search weighting. Database/reference parity covers dimension/content separation and first-result selection before the time window. Public batch tests cover unavailable memory, visible memory without a signal, malformed scope and storage failure.

Limits: Trace identity is an explicit deduplication boundary, not proof of causal independence across distinct traces. Legacy default contracts remain event-based. Initial new test assertions confused an explicit neutral factor with an absent factor and serialized equivalent timestamps in different zones; the corrected assertions compare factor 1 and timestamp instants.

제목: 반복 평가 표본 제외와 공개 신호 일괄 조회
이유: 신규 회복 정책에서 같은 작업의 재평가가 최소 표본을 채우지 않도록 하고 가시성 확인 뒤 조회를 묶는다.
검증: 실제 평가 기록·검색 연결, 임시 DB 동등성, 기간 전 중복 제거, 범위·오류·누락 신호 검증 통과.
한계: 서로 다른 trace의 인과 독립성은 보장하지 않는다. 기존 기본 정책과 전체 감사 이력은 유지한다.
연구: 해당 없음

## F5: classify metadata invalidation without weakening reference fences

Task: MEM-FOLLOWUPS-F5
Research: none

Why: Exact dependency revisions remain authoritative, but operators need to distinguish content/safety changes from review-schedule or observation-time changes before studying continuity. Memory replacements now record `dependency_metadata_revision_changed` only when a conservative comparison differs solely in revision/update time, review schedule, reference bookkeeping, or observed-at metadata. All other changes retain the existing reason. Both paths install the same exact-reference fence and resumable purge intent.

Verified: Nine synthetic mutation classifications (three metadata, six content/safety) and a real disposable-database replacement passed. The metadata purge reason was persisted, the old exact ref still returned conflict, and old dynamic evidence was not reused. Existing immediate-fence/resume regression passed (targeted package run 0.224s).

Limits: This implements classification and a durable measurement basis, not evidence rebinding. Operational mutation frequencies, memory quality effects, and safe continuity/rebind policy remain deferred because no representative operational material was supplied. Synthetic counts are not operational measurements.

제목: 동적 상태의 메타데이터 변경 원인 분류
이유: 정확 참조 방어를 유지하면서 향후 연속성 판단에 필요한 변경 유형을 구분한다.
검증: 9개 합성 변경과 실제 DB의 원인 저장·기존 참조 거부·이전 상태 미사용 검증 통과.
한계: 운영 빈도와 안전한 근거 재연결 효과 자료는 부족하여 보류한다. 정확 참조 fence와 purge는 그대로 유지한다.
연구: 해당 없음
