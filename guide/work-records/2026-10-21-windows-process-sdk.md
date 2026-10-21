# 2026-10-21 Windows 프로세스 API 선언 통합

작업 ID: Task: none (공개 평가 P1-6, Rust Windows FFI 유지보수)

## 이유와 변경

직전 전체 Windows 빌드에서 `CreateProcessW`와 `UpdateProcThreadAttribute`의
중복 선언 경고를 실제로 확인했다. 관리 프로세스와 업데이트 설치 프로그램이
각각 만든 startup/process 구조체와 함수 서명이 달랐다. 바이트 배치가 같은
것에 기대어 두 구현을 따로 유지하는 대신 공통 바인딩을 사용한다.

이미 Cargo.lock에 있던 Microsoft `windows-sys =0.61.2`를 Windows 전용 직접
의존성으로 고정했다. 기존 의존성 버전은 바꾸지 않았다. `windows_process.rs`가
프로세스 생성과 속성 목록 API 네 개, 보안·startup·process 타입을 제공한다.
두 호출자는 SDK의 `STARTUPINFOEXW`를 초기화하고 전체 크기와 확장 시작 플래그를
설정한 뒤 `StartupInfo` prefix를 전달한다. 수제 구조체 일곱 개와 중복 외부
함수 선언을 제거했다. Win64 크기·prefix offset 조건은 컴파일 시 확인한다.

주 앱과 관련 통합 검사 crate의 `clashing_extern_declarations` 경고를 오류로
승격했다. 현재 타입에 맞지 않는 호출은 SDK 함수 서명으로 검사받는다. 새 공유
파일의 해시도 native identity/runtime 검사 근거에 포함하며, identity 검사
스크립트는 그 해시를 실제 소스와 대조한다.

설계 참고: [Microsoft Rust for Windows](https://github.com/microsoft/windows-rs),
[CreateProcessW](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-createprocessw),
[UpdateProcThreadAttribute](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute).
실제 고정 버전의 crate 소스도 함께 대조했다.

## 실행한 검증

- 실제 Windows x64 release 검사: 202 + 70 + 19 = 291개 통과, 실패 0개,
  기존 opt-in 4개 ignored. 두 통합 검사에 같은 코어가 포함되므로 이 합계를
  서로 다른 시나리오 291개로 해석하지 않는다.
- 새 실제 프로세스 검사가 주 앱·runtime 검사 바이너리에서 각각 통과했다.
  현재 테스트 실행 파일을 전용 nonce·명시적 환경·세 개 표준 핸들로 실행하고,
  stdout/stderr 전달, PID, 종료 코드 7, 소유 Job의 빈 상태와 명시적 닫기를
  확인했다. 출력 대기와 종료 확인에는 시간 제한이 있다. 실패 시에도 소유
  프로세스만 정리하도록 기존 Job 소유권 경로를 사용한다.
- 실제 Windows production 앱 컴파일 통과. 원래 있던 API 선언 충돌 경고는
  0개로 줄었다. 주 앱 경고는 19개에서 17개가 됐으며 나머지를 숨기거나
  모든 경고가 해결됐다고 표시하지 않는다.
- 새 앱과 직전 `4ac870fd56551a6a7362733269d8d2fb3d023e1d` 빌드의 변경 없는
  Go/PostgreSQL 구성 요소로 격리 런타임 검사를 통과했다. 새 소유 데이터 경로의
  `ok=true`, `phase=complete`, 백업·복원·재사용·토큰 만료와 회전 결과를 확인했다.
  이후 소유 런타임 프로세스 잔여는 0개였다.
- Windows PowerShell parser로 바꾼 identity 검사 스크립트의 구문을 확인했다.
  `cargo metadata --locked --offline --no-deps`, 관련 파일 rustfmt, actionlint,
  diff 검사를 통과했다. 최종 관련 소스 아홉 개를 네이티브 archive와 바이트
  단위로 대조했다.

네이티브 소스 snapshot SHA256:
`38a511b3fb818c22b944822afb3b5551d5483bc9f7efcdc1d3a3dab5b88853e8`.
실행 로그·런타임 결과·빌드 확인 파일은 별도 로컬 근거 폴더에 보관한다.

## 실패와 검증 한계

처음 호스트에서 전체 `cargo metadata --offline`를 실행했을 때 이 호스트에 없는
`jiff-static` 다운로드가 필요해 실패했다. 네트워크 다운로드로 우회하지 않았다.
메타데이터는 의존성 다운로드가 없는 범위로 확인했고, 실제 대상 검증은 캐시가
있는 Windows에서 `--locked --offline` 빌드와 전체 검사로 수행했다.

실제 사용자 앱·DB, SSH 서버, 제품 설치·업그레이드·제거는 실행하지 않았다.
GitHub-hosted 전용의 전체 installer identity/runtime-control 검사는 여전히
미실행·ignored이며, 스크립트 구문 확인을 그 시나리오 통과로 계산하지 않는다.
기존 Job Object 타입·일부 외부 API 선언과 기타 Rust 경고는 후속 정리 대상이다.

다른 OS 앱 빌드, push·PR·공개 릴리스는 수행하지 않았다. 앞서 다운로드에
전달한 서명 설치 파일의 소스는 `4ac870f`이며 이번 SDK 변경을 포함하지 않는다.
이번 결과는 변경한 소스·Windows 앱 컴파일·격리 런타임의 검증이다.
