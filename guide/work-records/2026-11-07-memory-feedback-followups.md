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

## F3 — Show configured policy and applied samples / 평가 정책과 적용 표본 표시

Task: memory-followups-F3
Research: none
연구: 해당 없음

Why: People could not inspect the opt-in policy or distinguish retained events from independent evidence. Explicit disable is required to override an inherited policy.
이유: 설정 화면에 최근 평가 정책을 연결하고, 전체 이력과 현재 내용·기간·평가 종류에 맞는 독립 표본을 구분합니다. 상속 정책도 명시적으로 끌 수 있습니다.

Changes: Scoped settings now edit dimension, window and minimum samples. A scoped, authorized signal-policy endpoint reports the baseline factor, work factor, server timestamp, current-window events, eligible first-trace samples and excluded events. The memory detail panel has an explicit read/refresh/retry path. It does not label missing attribution as a duplicate or delivery as causal success.
변경: 설정에서 평가 종류·기간·최소 표본을 바꾸며, 기억 평가 패널에서 실제 서버 계산 기준과 표본을 조회·재시도합니다. `disabled:true`는 기존 전체 이력 정책으로 돌아갑니다.

Verified: Targeted Go service/HTTP tests pass, including actual outcome ingestion through Search factor parity, anonymous rejection and inherited disable. A helper supplies real 200/401 responses for the integrated strict contract corpus. Focused node tests and local Chromium component tests pass: keyboard toggle/save, dimension/window changes, sibling-setting preservation, explicit disable, failed explanation request/retry and scope query preservation. Screenshots inspected for policy settings and evaluation details.
검증: 실제 서비스 계산과 검색 가중치 동일성, 인증 거부, 상속 해제, 키보드 조작과 재시도를 확인했습니다. 기존 PG 필터 검증은 F1/F2의 임시 DB 시험에서 실행했으며 이번 F3 Go 묶음의 PG 검사는 환경 미지정으로 건너뛰었습니다.

Limits: Browser tests render production components with synthetic scoped HTTP responses, not the complete authenticated application. Better Design was invoked locally but does not support these JS files; no supported JSX/TSX/CSS files were found, so this is not a design-check pass. API artifact regeneration and strict corpus inclusion belong to integration. Reported baseline factors can differ for a different query time or separate dynamic ranking.
한계: 화면 검증은 합성 응답을 사용하는 실제 컴포넌트 범위입니다. Better Design은 JS를 지원하지 않아 통과로 간주하지 않습니다. API 생성물과 종합 계약 검사는 통합 작업에서 연결합니다.
