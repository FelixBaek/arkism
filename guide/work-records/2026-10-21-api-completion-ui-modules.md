# 2026-10-21 API 누락 해소와 UI 구조 분리

작업 ID: Task: none (사용자 요청: API 규격·실제 응답·UI 구조 개선과 미문서화 항목 최신화)
연구 작업: Research: none
기준: `8a4c610`에서 시작한 후속 변경. 이전 ‘UI는 설명만’ 요청은 최신 구현 요청으로 변경됐다.

## 변경 이유와 구현

이전 검사는 기억 목록의 실제 응답만 검증했고, 60개 관리 API를 미문서화 예외로
남겼다. 이 예외를 삭제했다. 현재 실제 등록 271개와 OpenAPI를 양방향으로
대조하며, 누락·유령 문서 경로를 모두 거부한다. JSON DTO 필드·태그·nullable·
omitempty를 로컬 Go AST에서 추출하고, 60개 작업의 요청·응답을 구체화했다.
알 수 없는 타입을 빈 스키마로 대체하지 않는다. 요청 필수값·쿼리·동작 설명은
별도의 검토 가능한 `api/management-contracts.json`으로 관리한다.

원본과 대조한 주요 차이: 코드 리뷰·provider API는 중첩 payload만이 아닌
Record envelope를 반환한다. 지식 문서 조회는 `source` 쿼리가 필요하다.
메신저 전송 실패는 HTTP 200의 ok=false/error일 수 있다. expires_at=null은
만료 해제이며, wiki는 초안과 사용자 검토 내용을 구분한다. 스키마를 검증하기
위해 실제 핸들러를 실행했으며, 사후 예시를 계약에 맞게 꾸며내지 않았다.

60개 작업 모두 정상 요청과 동일 요청의 익명 거부, 모든 POST의 미정의 필드
거부를 수집한다. 기존 기억 페이지의 검색·커서·오류 10개와 합쳐 HTTP 응답
170개다(200: 64, 400: 45, 403: 61). 응답뿐 아니라 정상 요청 본문·쿼리도
최종 OpenAPI로 검증한다. 성공·익명 사례가 빠진 관리 API가 있으면 검사에
실패한다. 기존 다른 API의 모든 성공·오류 분기를 검증했다고 주장하지 않는다.

UI는 디자인을 보존하고 구조를 나눴다. app.js는 5,034→2,215줄,
crew.js는 4,379→1,294줄이다. 연결/API, 기억 목록/상세/페이지 상태, 스킬·
이미지·디자인·도구 화면, 그래프 모델/캔버스/노드/실행/AI 검토/전문가/
페르소나/일정을 명시적 모듈 경계로 구분한다. 연결과 화면별 필터는 단일
소유 객체로 연결한다. 다른 모듈에 실제 쓰기가 필요한 값만 setter를 노출한다.
이전 연결 응답과 같은 실행 ID로 재진입한 이전 조회도 현재 화면을 덮지 못한다.

HTML의 스크립트 60개를 모양 적용용 look.js와 ES module 진입점으로 정리했다.
기존 주변 화면의 window 팩토리는 진입점 안의 호환 어댑터로 남는다. 새 상태를
전역에 추가하지 않는다. esbuild 0.28.2와 브라우저 검사 도구를 잠금 파일로
고정했고, Windows 패키징은 조립한 ui-dist를 사용하도록 연결했다. 출력 입력
해시·HTML 자산·영어 청크·개발 의존성 미포함을 검사한다.

한국어 시작에 영어 사전을 읽지 않고, 영어 선택 시에만 동적 로드한다.
셸 탭은 고정 메시지 ID로 연결한다. 기존 문구의 DOM 번역 어댑터도 유지하며
사용자 원문·코드는 제외한다. 모듈의 번역 누락 검사와 미리보기 기억 페이지
응답도 최신 구조로 맞췄다. 개발 안내·API 안내·UI 구조·Changelog를 갱신했다.

## 실행한 검증과 결과

- Windows 네이티브 Go: memory list / HTTP route / OpenAPI 관련 최상위 검사
  9개 통과. 60개 관리 API의 정상·익명·POST 미정의 필드 사례 포함. 생략 없음.
  최종 패키지 실행 3.159초. 실제 제품 설치·DB·계정·레지스트리·에이전트 파일은
  사용하지 않았고, 소유 표식이 있는 테스트 소스 복사본과 임시 fixture만 사용했다.
- Windows에서 출력한 실제 응답 170개를 최종 문서로 검증: 통과.
  공식 OpenAPI 3.1 구조 스키마·참조·경로 변수 검사 및 DTO drift 검사 통과.
- Python 계약 검사의 정상/깨진 참조/누락 path/변형 응답/빈 실제 증거/
  잘못된 요청 본문 제어 검사 6개 통과.
- Node UI 검사 파일 15개 통과. 별도 모듈 상태/지연 응답 8개, 실제 기억
  캡처 모듈의 추가·되돌림 8개 사례 통과. 문자열 추출을 VM으로 실행하던
  기억 테스트는 실제 ES module factory를 불러오는 검사로 변경했다.
- 번역: 정적 문구 4,170개, 패턴 912개에서 누락 0개. 기존 문구 유지.
- 실제 Chromium: 기억 첫 50개, 다음 요청, 실패·재시도 보존, 첫 페이지 밖
  검색, 종류 필터, 상세 선택, Enter 포커스 유지, 760px 안내 표시 통과.
- 실제 Chromium: 홈·기억 상세·자동화 목록·자동화 창·그래프·노드 설정 창·
  모양 설정 7개, 노드 창 Escape, 영어 지연 로딩·한국어 복귀·사용자 원문
  보존 통과. 기준 UI와 변경 후 원본 및 배포용 UI의 7개 화면 PNG·문구·
  버튼·선택 상태가 모두 정확히 일치. 비교 시 데이터·시각·뷰포트·모션 고정.
- npm 잠금 설치를 로컬 캐시에서 새로 실행하고, 배포용 81개 입력 모듈 조립과
  해시·자산·분리 청크 검사 통과. 조립한 배포 UI에서도 동일 브라우저 검사 통과.
- Go 복잡도: 5,962개 선언/클로저, 기존 부채 381개. 새 초과·기존 증가 없음.
- actionlint, Windows 빌드 shell 구문, git diff whitespace 검사 통과.
- 검토한 변경 파일만 별도 복사한 gitleaks 검사에서 비밀 발견 0개.

Better Design 로컬 검사를 적용했다. 기억 페이지 CSS 발견 0개. 기존 그래프 CSS의
별도 검사에서는 critical 0, serious 145, moderate 34개를 보고했다. 주로 px를
rem으로 바꾸라는 범용 권고로, 그래프 좌표·노드·드래그가 기존 고정 px 좌표를
공유하므로 일괄 치환하지 않았다. 스타일을 새로 바꾸지 않았고 실제 화면 비교로
기존 렌더를 별도 확인했다. 코드 검사만으로 시각 검토 통과를 주장하지 않는다.

## 증거

- Windows: `.local/share/arkism-build-artifacts/windows-contracts-ui-20261021/evidence`
  (사용자 홈 아래; check.log, check.json, http-cases.json).
- 전송 파일은 사용자가 승인한 6개로 제한했다. 최종 archive SHA-256:
  `a9066368e77b8b2319e0e19400d931b786dab9a84a6deb9d04ceb94f64fb2d4c`.
- 브라우저: `/tmp/arkism-ui-structure/{before,source,bundle}`. 비교 명령:
  `node desktop/tests/compare-ui-snapshots.cjs /tmp/arkism-ui-structure/before /tmp/arkism-ui-structure/source /tmp/arkism-ui-structure/bundle`.
- 로컬 로그: `/tmp/arkism-contract-response.log`, `/tmp/arkism-contract-python.log`,
  `/tmp/arkism-refactor-node.log`, `/tmp/arkism-frontend-browser.log`,
  `/tmp/arkism-ui-parity.log`, `/tmp/arkism-ui-api-complexity.json`.

## 남은 한계

이 결과는 로컬 구현·문서·명시한 검사 범위다. 원격 Actions/Codecov/Sonar의
최종 결과, 실제 PostgreSQL 및 외부 provider 통합, 모든 API 성공 분기,
실제 Windows WebView·업데이트 설치의 추가 수용 검사는 미실행이다.
새 Windows 설치파일은 만들지 않았다. 이전 전달 설치파일에는 이번 변경이
들어 있지 않다. 공개 게시·PR 생성·push·서버 규칙 변경은 하지 않았다.

기억 DOM 가상화, 전체 서버 기준 정렬/정확한 합계, 다른 목록의 추가 페이징,
모든 레거시 팩토리/번역 문구의 일괄 전환은 이번 작업의 완료 주장에 포함하지
않는다. 현재 앱은 요청한 기억 페이지만 읽고, 불러온 목록 기준 수치를 표시한다.

후속 기록(2026-10-21): 목록 가상화·후보/충돌/스킬/휴지통의 추가 분할 로딩과 관련 UI 코드 정리는 [다음 작업 기록](2026-10-21-list-virtualization.md)에 반영했다. 위 미실행 항목은 당시 상태이며, 최신 목록 처리 범위는 후속 기록을 따른다.
