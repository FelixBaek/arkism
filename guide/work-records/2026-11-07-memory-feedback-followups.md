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
