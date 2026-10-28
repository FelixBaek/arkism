# 릴리즈 전 저장소 식별 회귀 검사

Task: none (관련 계약 ST-824)
Research: none
기준 소스: c636ffab13781feba74b47a2af9c524bcf54d981

## 이유와 변경

불완전한 `.git` 디렉터리를 저장소로 취급하면 저장소 밖 대화에도 프로젝트 키 또는 캡처의 저장소 태그를 부여한다. 빈 디렉터리, config만 있는 디렉터리, HEAD가 디렉터리인 경우를 각각 ProjectKey/repoKey로 재현했다(6개 하위 사례 모두 수정 전 실패).

ProjectKey는 공통 Git 디렉터리, 캡처의 일반 저장소 식별은 `.git` 안에서 HEAD가 일반 파일인지 확인한다. 잘못된 디렉터리는 상위 탐색을 계속한다. 정상 origin, remote 없는 저장소, worktree, 명시적 환경변수 override의 기존 동작은 유지한다. 기존 가져오기 fixture도 정상 저장소처럼 HEAD를 갖추도록 보강했다. 모든 Git 메타데이터나 악의적인 파일 교체까지 검증하는 변경은 아니다.

최신 소스에서 호출이 없는 CueMatch와 패키지 수준 desktopcontrol.Enter 두 함수만 제거했다(5줄). 실제 필터 cueRE, 추출 모델 조회 CaptureModel, Gate.Enter 메서드와 작업 제출 게이트는 유지한다. 과거 소스에서 미사용이었던 함수를 최신 소스에서도 미사용이라고 가정하지 않는다.

## 검증

- `go test -race -count=1 -run 'TestProjectKeyIgnoresIncompleteGitDirectory|TestRepoKeyIgnoresIncompleteGitDirectory' ./internal/host ./internal/host/capture`: 수정 전 6개 하위 사례 실패.
- `go test -race -count=1 -run 'TestProjectKey|TestRepoKey|TestForRepo|TestImportAgents' ./internal/host ./internal/host/capture`: 수정 후 두 패키지 통과.
- 기준 소스 UI: 단위 522개, 소스/지원되는 번들 브라우저 19회 통과. 소스/번들 8화면 PNG·컨트롤 일치. lint, 저장소 설정 범위의 typecheck, 프로덕션 94모듈 빌드 통과. 화면 소스는 이번 변경에서 수정하지 않았다.
- 기준 소스 Python 187개, Rust 214개 통과. Rust 2개는 실제 자격증명/SSH 검사가 필요해 ignored다. Linux·Windows·macOS vet, Clippy 경고 0, API 명세 303개 작업과 DTO 84개 작업, 비밀값 검사 0건, 셸 14개 구문 검사 통과.
- 최종 소스의 전체 PostgreSQL(pgvector)·race·커버리지 검사: 종료 0. memory 16그룹·crew 18그룹과 나머지 패키지를 포함한 35개 실행에서 최상위 3,032개 PASS/8개 SKIP, 하위 사례 포함 10,197개 PASS/9개 SKIP. 하위 사례를 독립 테스트 수로 세지 않는다. SKIP은 별도 조건이 필요한 실모델 호출·복사 DB·평가 내보내기·비공개 배포 스크립트·xxhsum·MCP 바이너리 조건이다.
- MCP 바이너리 조건은 현재 소스에서 바이너리를 따로 빌드해 `TestCrossAgentHTTPPostgresReuse` 전체를 재실행했다. 실제 stdio subprocess와 실제 임시 DB에서 Codex/Claude Code runtime 양방향 저장·사람 승인·재사용 및 철회/출처삭제/충돌 방어를 통과했다. 외부 모델은 호출하지 않았다.
- 전체 커버리지 게이트: memory 28,874/34,391(83.96%, 기준 83.5%), crewrun 2,630/3,119(84.32%, 기준 79.5%) 통과. 핵심 파일 tei 98.57%, candidate_quota 100%, context_budget 97.83%로 모두 기준 90%를 넘었다.
- `MINISIGN=<로컬 검증 도구> GOFLAGS=-tags=integration POSTGRES_TEST_DSN=<임시 루프백 DB> python3 tools/check_local.py`: 종료 0, 마지막 `Local checks passed.` 확인. Python·복잡도·기록·vet·전체 Go race·522개 UI 테스트를 순서대로 재검증했다. 비밀값은 기록하지 않는다.
- 세션 원본 JSON·UTF-8 파서 fuzz를 각 20초, 작업자 2개로 실행했다. 각각 55,656회·27,871회 입력을 검사해 두 명령 모두 종료 0. 파서 전체나 가능한 모든 입력의 정확성을 보증하는 결과는 아니다.
- 실제 Windows 설치/제거는 후보 커밋에 대한 별도 수동 워크플로의 산출물로 확인한다. 이 기록 작성 시에는 아직 실행 전이다.

## 한계와 실행 범위

테스트 설치파일 0.1.6은 같은 버전의 여러 빌드가 있어 버전 문자열만으로 소스를 식별할 수 없다. 최신 테스트 빌드의 소스는 110707dc이며 이후 main의 수정도 이번 기준 소스에 포함된다. 개인 PC의 실제 설치본이 어느 빌드인지는 확인하지 않았다.

기존 Rust 형식 검사에는 16개 파일의 형식 차이가 남는다. 이 변경의 제품 오류로 취급하거나 대량 재정렬하지 않았다. 현재 CI 필수 검사는 아니며 Clippy는 통과했다. 크로스 플랫폼 vet·브라우저 fixture만으로 실제 Windows 설치 동작을 통과로 표시하지 않는다.

사용자가 요청한 로컬 워크트리 정리는 원본 변경, 실행 중인 프로세스/열린 파일/서비스 참조, 설치파일을 보존하고 재검증한 172개만 제거했다. 기존 브랜치는 삭제하지 않았고 detached HEAD 12개는 별도 로컬 복구 브랜치에 보존했다. 이 로컬 정리는 제품 코드 변경이나 공개 릴리즈 생성이 아니다.

태그·공개 릴리즈·업데이트 피드·운영 서비스는 변경하지 않았다. 원본 검사 로그와 워크트리 복구 목록은 작업별 로컬 증거 폴더에 보존했다.
