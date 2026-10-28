# 릴리즈 검사에서 실제 자료 보존 확인

Task: none
Research: none
이전 검증 후보: f12ab79f19120d4be15283748d876c0898141515

## 문제와 변경

이전 후보의 Windows 패키징·설치·제거 워크플로는 통과했으나, runtime 진단은 주로 capabilities/토큰/백업 성공을 확인했고 일반 기억의 내용 복원은 비교하지 않았다. 제거 결과도 `database_preservation_tested=false`로 실제 DB 보존을 검증하지 않았음을 정직하게 표시했다. 단순 성공 상태를 자료 보존의 증거로 바꾸지 않고 실제 자료 검사를 추가한다.

- fresh-directory 진단에서 합성 출처와 기억 각 1개를 정식 API로 만들고 진단용 owner의 사람 승인 경로를 거친다. ID·범위·본문·revision·출처 span·승인 정보를 두 차례 백업/삭제/복원 뒤 읽어서 비교한다. 일반 앱의 승인 정책이나 일반 실행 경로는 바꾸지 않는다.
- stage/installed 공통 결과 검증은 행 수의 정수형·정확한 값, digest 형식, 두 복원 검증의 boolean을 요구한다. 예전 바이너리의 누락 필드나 문자열 true로 새 관문을 통과할 수 없다.
- 실제 bundled PostgreSQL의 합성 자료를 정지 상태에서 스냅샷한다. 동일 후보의 재설치, 실제 제품 제거 후 닫힌 DB 파일이 그대로인지 확인하고, 설치 폴더 밖에서 해시를 검증한 PG 복사본으로 DB를 다시 열어 논리 행도 비교한다. 같은 버전 재설치를 버전 업그레이드라고 표시하지 않는다.
- 별도로 공식 0.1.4 설치파일을 고정 hash로 검증하고, 이전 버전이 만든 합성 프로필에 정식 API로 자료·승인을 저장한다. 실제 후보 설치파일로 교체한 뒤 테스트 전용 Rust 하네스가 정상 Runtime 시작/마이그레이션 경로로 기존 프로필을 열고 자료·출처·revision·승인 및 고정 모델 설정을 비교한다.
- 업그레이드 엔트리는 `cfg(test)` ignored 시험에만 있다. 제품 CLI/IPC에 기존 디렉터리 우회 기능을 만들지 않는다. 실제 GitHub-hosted Windows·저장소·컴파일/실행 source SHA·RUNNER_TEMP 직계 소유 root·marker·일반 파일·정지한 PG17·빈 로컬 포트·외부 모델 비활성 조건을 검사한다. 개인 기기에서 hosted 환경값을 위조해 실행하지 않는다.
- CI에서 새 오프라인 회귀와 실제 자료/업그레이드 관문을 연결하고, 버전이 0.1.4 이하인 빌드는 업그레이드가 해당하지 않음을 false와 not_applicable로 명시한다. 보고·산출물에는 원문·토큰·암호 대신 행 수/boolean/hash만 남긴다.

## 로컬 검증과 근거

- 실제 임시 PostgreSQL18 + 현재 Rust 진단: 최종 6.19초, 종료 0. 합성 출처1·active 기억1의 두 번 복원 동일성, 기존 토큰 회전/403/reuse/backup/capabilities 모두 통과했다. 시스템 PG의 Unix socket 기본 경로 차이는 임시 initdb fixture의 TCP 전용 설정으로만 적응했고 시스템 PG와 제품 코드는 변경하지 않았다. Windows bundled PG17의 대체 검증으로 세지 않는다.
- Rust 전체 222개 통과, 3개 ignored. 기존 실계정 저장소/SSH 2개 외에 새 hosted 업그레이드 하네스가 기본 ignored이며, 정확한 Windows CI 단계에서만 명시 실행한다. Clippy 경고 0.
- PowerShell5 오프라인: DB 보존15·설치 진단99·기존 제품 제거139·이전 버전 경계14 = 267개 통과. 실제 설치/DB/registry 네이티브 호출은 0이다. 경로·동결 stage/tool 변조·실제 제거 선행 조건·자료 변경·멈춤 실패·누락/잘못된 형식·private 출력 덮어쓰기·raw JSON 날짜/승인 보존을 확인했다.
- `python3 tools/check_local.py`: 최종 종료 0. 이 재실행은 POSTGRES_TEST_DSN 없이 단위 검사 범위이며 DB 생략을 통합 성공으로 세지 않는다. Go 코드·테스트·go.mod·go.sum은 앞서 전체 DB/race/coverage를 통과한 f12ab79f와 바이트가 같다. 최신 후보의 전체 DB/Windows 관문은 별도로 다시 실행한다.
- 독립 검토에서 runtime.json의 JSON 파싱 예외가 비밀값을 오류에 포함할 수 있음을 발견했다. 고정 오류 코드로 가리고 malformed private metadata에서 원문 비노출·PG 실행 전 중단을 확인했다. API/fixture 파서도 단일 객체만 허용하고 private JSON/원시값/한 원소 배열의 오류를 고정 코드로 제한하는 회귀를 통과했다.
- 초기 fixture 계약 실패(source span 누락), 테스트 Name 변수 shadowing, 원격 의존 파일 전송 누락, SSH 120초 관측 상한은 원인별로 고쳤고 실제 terminal/PID/start/hash를 확인했다. 관측 상한만으로 작업을 재시작하거나 통과로 세지 않았다.
- 공식 0.1.4 setup: SHA256 `45f561595ae5fef7d236b7f913062b951c3bda139781f3540b5d033d6d3b31aa`, 77,956,340 bytes. 공식 API 자산 digest, 서명된 전체 hash 목록, installer/목록 등 minisign 서명5개를 기존 저장소 공개키로 검증했다. 태그/릴리즈 source는 `7bbbd879633bb5833b317f1e79ae079881fe7840`이다. 이 이전 배포에는 빌드 출처 증명 자산이 없어 소스 귀속은 공식 릴리즈·태그 메타데이터이며 독립적인 바이너리 소스 재현 증명은 아니다.

## 실행 시점의 한계

이 기록 작성 시 새 정확한 커밋의 hosted Windows 자료 보존·재설치·0.1.4 업그레이드 시험은 아직 실행 전이다. 오프라인 성공이나 이전 f12ab79f의 설치 결과로 이를 완료 표시하지 않는다. 원본 로그·서명 검증·소스 hash·실제 종료 결과는 작업별 로컬 증거 폴더에 보존한다.

실제 사용자의 데이터·계정·설치 앱을 시험 자료로 사용하지 않았으며 외부 모델 호출도 없다. 기존 Rust 형식 차이16파일은 기능 수정과 무관하여 대량 재정렬하지 않았다. 공개 태그·릴리즈·업데이트 피드는 만들거나 변경하지 않는다.
