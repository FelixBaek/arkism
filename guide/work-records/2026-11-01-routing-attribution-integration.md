# Preserve observation leaves when enabling routing attribution

Task: PR527, RTE-06/08 integration
Research: Engineering integration of existing routing observations; no paid pilot or effect claim.
Design: Replay only the two new product commits over the corrected integration branch. Pass the existing injected-run flag through the attribution wrapper to the original routed-attempt observer. Keep both context-view schemas and the new routing-attribution response schemas.
Relation: adapted
Deviation: The wrapper signature follows the integrated observer rather than the older product-branch signature. Attribution remains off by default and the pilot arm still requires it.
Why: Direct replay conflicts with the integrated observation wrapper and the appended context-view schemas. Choosing either side would lose an existing feature.
Verified: Targeted host routing/observation race tests passed. A new off/on integration test confirms exactly two observed leaves and two reported attempts for a failed pick and successful fallback, one attribution body only when enabled, and no copied outer reservation identifiers. Independent actual HTTP/PostgreSQL normal and race tests passed using genuine restricted LOGIN and real authorization: idempotency, changed-record conflict, prompt rejection, pagination, round trip, cross-scope and read/write permission denial. The temporary database and role were removed and the private cluster stopped. Pinned changed-code Go lint passed. Offline OpenAPI validation passed for 309 operations and references; that invocation contained zero response cases and is not claimed as HTTP-response validation.
Limits: Fresh complete remote CI remains required; private functional tests are evidence about these paths, not every product integration. No provider call, paid pilot, product build, deployment or operating-data access. Observation and attribution records do not prove outcome effectiveness.

제목: 라우팅 기록을 켜도 기존 시도별 관측 유지
이유: 통합된 관측 래퍼와 API 문서에 제품 연결을 합칠 때 생기는 충돌을 양쪽 기능을 유지하도록 해결했다.
검증: 기록 off·on에서 실패한 첫 선택과 성공한 대체 선택의 관측2개·보고2개를 확인했다. on에서만 기록1개를 보낸다. 대상 race·Go lint와 API 구조·참조309개 검사가 통과했다. 이 API 구조 검사는 실제 HTTP 응답 검사를 포함하지 않는다.
한계: 실제 HTTP·PostgreSQL의 재전송·페이지·권한 경계도 별도 임시 환경에서 일반·race 통과했다. 최신 전체 원격 CI는 필요하다. 기본 off와 파일럿 조건을 유지하며 제품 빌드·배포·외부 모델 호출은 하지 않았다.
