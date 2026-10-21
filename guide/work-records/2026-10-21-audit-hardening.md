# 2026-10-21 공개 평가 후속 작업

작업 ID: Task: none (공개 평가의 P0-1, P0-2, P1-4, P1-5, P2에 대한 첫 구현 묶음)

## 이유와 범위

평가 기준 커밋 `ae6d9f5`와 작업 기준 main
`efb51f0c2a31b7a00e1c58cbf7841a8e94d765c7`을 대조했다. 해시만 비교하던
Windows 업데이트에 독립된 게시자 인증을 추가하고, 품질 지표의 예외와 실제
필수 검사·배포 상태를 구별한다. 다른 OS의 앱 배포는 추가하지 않았다.

PR #109의 원시 Ed25519 형식은 채택하지 않았다. 실제 minisign과 Tauri 구현을
확인해 minisign 서명 파일과 Tauri의 base64 포장을 사용한다. 설치 파일뿐 아니라
manifest도 서명하고, 인증된 버전과 파일명까지 대조해 과거 파일의 재표시를 막는다.
키는 피드에서 받지 않고 Windows 앱에 고정한다. HTTPS는 리다이렉트에도 적용한다.

## 변경 결과

| 평가 항목 | 이번 변경 | 남은 일 |
| --- | --- | --- |
| P0-1 업데이트 진위 | 실제 Rust 경로의 minisign 검증, 서명 manifest, HTTPS 강제, Tauri 호환 `.sig`, 서명된 SHA256SUMS, Syft SPDX, 빌드 출처 검증과 릴리스 중단 게이트 | 운영 키 등록·보관 승인, 보호된 서명 환경 설정, 새 코드의 Windows 패키징·설치·업그레이드, 새 릴리스 |
| P0-2 지표와 부채 | Go/Rust/JS S3776 전역 무시 제거, 실제 Go 복잡도 보고·증가 금지, 패키지 커버리지 하한, Codecov patch 기준 | 새 Sonar/Codecov 원격 분석, 기존 복잡도 감소와 UI 렌더·E2E 확장 |
| P0-3 인증·인가 | 이번 묶음에서 변경하지 않음 | 중앙 인증과 라우트별 권한 계약, 토큰 정책·만료·회전 |
| P1-4 유지보수 구조 | CODEOWNERS·제보/PR 양식·행동 규칙, 실제 ruleset 조회, 누락 필수 검사의 적용안, hosted runner 전환 설정·런북 | ruleset 실제 적용, 독립 리뷰어·두 번째 유지보수자 확보 |
| P1-5 문서 | 현재 메뉴·Windows 배포·한영 프라이버시 정합성, CHANGELOG, 개발 명령과 설계 기록 분리 | 현재 화면 캡처 교체; 기존 캡처는 예시·구버전일 수 있음을 표시 |
| P1-6 큰 UI·전량 로드 | 이번 묶음에서 변경하지 않음 | UI 모듈 분리·언어 지연 로드·목록 페이지 처리·큰 Go/Rust 파일 분리 |
| P1-7 설치 원자성·SSH | 이번 묶음에서 변경하지 않음 | 설치 교체·복구, 자동 시작 제거의 실제 검증, SSH 최초 지문 확인 |
| P2 운영 검사 | Git 전체 이력 비밀 검사, 명시적 보안 업데이트 그룹, 릴리스 Coverage·CodeQL 근거 확인 | OpenAPI 계약 강화와 Sonar new-code 설정 검토 |

`evaluation/`은 기억 평가 프로그램, `evaluations/`는 과거 API 측정이다. 내용이
중복된 폴더로 간주해 지우지 않고 각 용도와 기존 근거 경로를 안내했다.

## 실행한 검증과 근거

- `MINISIGN=... python3 -m unittest discover -s tools -p 'test_*.py'`:
  90개 통과. 실제 minisign 서명·전체 자산 변조·다른 키·누락 SBOM·미서명 피드 게시
  거부·파일 목록 손상·CLI 비밀 비출력과 게이트 순서를 포함한다.
- `python3 -m unittest discover -s evaluation -p 'test_*.py'`: 8개 통과.
  `evaluation/reuse-v010/benchmark`의 같은 검사: 49개 통과.
- `cargo test --locked --manifest-path desktop/src-tauri/Cargo.toml`:
  Linux 검증 호스트에서 Rust 185개 통과, 기존 ignored 1개. 최신 서명 경계 수정 후
  `update_manifest::tests` 3개를 다시 실행해 통과했다. Windows 전용 테스트가 이
  호스트에서 0개 실행된 사실을 Windows 통과로 해석하지 않는다.
- 암호로 보호한 임시 minisign 키를 stdin으로 잠금 해제해 실제 서명·검증 모두
  성공했다. 테스트 키는 임시 폴더 종료와 함께 제거했고 운영 키를 만들지 않았다.
- 격리 PostgreSQL에서 `python3 tools/ci_go_test.py --race --integration --coverage --jobs 4`:
  전체 통과. memory 24523/29271 = 83.78%, crewrun 1829/2299 = 79.56%.
  하한은 각각 83.5%·79.5%로 초기화했다. 코어 파일도 기존 90% 하한을 통과했다
  (tei 98.61%, candidate_quota 100%, context_budget 97.83%).
- `go run ./tools/complexity --report ...`: 5,756개 선언·클로저 중 382개가 20 초과.
  기존 부채는 보고에 남겼으며 신규 초과·기존 값 증가는 실패한다. Sonar의 인지
  복잡도와 다른 지표임을 명시했다. 검사 도구 회귀 2개도 통과했다.
- `tools/check_local.py`의 UI 검사: 46개 통과. UI 소스·화면은 바꾸지 않았다.
- `actionlint` 1.7.12: 바꾼 Actions 5개 파싱·권한·표현식 검사 통과.
  셸 `bash -n`과 `git diff --check` 통과.
- 고정 Syft 1.55.0으로 Windows Go 바이너리와 의존성 lock 파일을 검사해
  SPDX-2.3 패키지 528개를 생성했다. 실제 전체 NSIS payload 검사는 아직 아니다.
- gitleaks 8.28.0: 현재 작업 트리와 공개 main의 239커밋 이력에서 발견 0.
- 인증된 GitHub API로 현재 ruleset의 실제 필수 검사 11개를 확인했다.
  classic protection 404만으로 보호가 없다고 판단하지 않았다. 제안한 추가
  검사 4개는 파일로만 준비했으며 서버 설정을 바꾸지 않았다.
- 실제 기준 main SHA를 넣은 `check_release_quality.py --timeout-seconds 0`:
  Checks·SonarCloud·Coverage·CodeQL 네 실행 근거 모두 검증 성공. 이것은 기준
  main의 결과이며 이번 미게시 변경의 원격 CI 통과를 뜻하지 않는다.

`MINISIGN=... python3 tools/check_local.py` 최종 필수 검사도 통과했다.
Python 90+8+49개, Go vet·race, 복잡도 검사, 작업 기록 검사, UI 46개를 포함한다.
DB 통합 검사는 위의 별도 일회용 PostgreSQL 실행 결과다.

## 실패와 환경 한계

최초 Rust 검사 7개는 sandbox의 루프백 소켓 제한 때문에 실패했다. 같은 검사를
루프백 사용이 가능한 실행에서 다시 돌려 통과했다. 최초 전체 Go 검사는 공유
임시 저장소의 `Disk quota exceeded`로 실패했다. 홈의 별도 폴더로 옮긴 두 번의
검사는 홈 자체가 Git 저장소인 탓에 저장소 밖 경로 fixture 2개가 실패했다.
Git 검색 환경변수는 직접 파일을 따라가는 제품 코드에 영향을 주지 않았다.
사전검사로 확인한 `/var/tmp`의 전용 폴더와 새 임시 DB에서 전체 검사를 다시
통과시켰다. 실패 기록을 삭제하거나 시험을 생략하지 않았고 운영 DB는 쓰지 않았다.

검사 로그와 커버리지·SBOM 실측 파일은 로컬의 `arkism-audit-*` 근거로 보관한다.
공개 커밋에 원대화·자격 증명·개인 설정·테스트 DB를 포함하지 않는다.

## 미실행과 다음 순서

운영 키·GitHub 변수/비밀·서명 환경·ruleset의 실제 변경, push·PR·태그·공개 릴리스,
Windows에서 새 설치/기존 데이터 업그레이드, 실제 Sigstore/OIDC 발급 및 검증은
실행하지 않았다. 로컬 GitHub CLI에는 `gh attestation` 명령이 없어 실제 출처
검증은 hosted 릴리스 작업과 이후 독립 검증에서 확인해야 한다. Authenticode
게시자 인증서와 SmartScreen 평판은 minisign과 별개이며 추가하지 않았다.

다음 코드 우선순위는 P0-3 인증·인가 누락 방지다. 이후 UI 로딩·모듈 구조와
Windows 설치 복구·제거·SSH 지문을 별도 검증 가능한 변경으로 진행한다.
독립 리뷰어 확보와 운영 키의 신뢰 결정은 코드만으로 완료할 수 없다.

커밋 상태: 로컬 작업 브랜치의 커밋에 포함. 외부 push·PR·배포 없음.
