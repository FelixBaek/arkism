# 2026-10-21 인증·토큰·설치 복구 통합 Windows 서명 빌드

작업 ID: Task: none (공개 평가 P0-1, P0-3, P1-7 및 승인한 Windows 빌드)

## 이유와 소스

첫 서명 빌드 `2e73b0e` 이후의 공통 HTTP 인가, 토큰 만료·회전, 자동 시작 정리,
설치 payload·바로가기·등록 복구와 NSIS 연결을 포함한 전체 Windows 앱을 다시
만들었다. 기존 Windows 빌드 승인을 사용했으며 다른 OS 앱은 빌드하지 않았다.

제품 소스: `4ac870fd56551a6a7362733269d8d2fb3d023e1d`.
Git archive SHA256:
`246ee87c49348713733159e40897855d3de1f1c572c207efc04d147c9b28e5cc`.
버전 `0.1.3`, 채널 `stable`, 고정 공개키는 첫 로컬 서명 빌드와 같다.
이 기록을 추가한 문서 커밋과 제품 소스 커밋을 구분한다.

## 실제 실행한 검증

- 새 소유 Windows 빌드 공간에서 전체 Go 컴포넌트와 Rust release를 만들었다.
  검사한 PostgreSQL 17.6-1/pgvector 0.8.1 캐시를 사용했다. 실제 제품 payload로
  제거 파일 생성→전체 목록 고정→최종 NSIS 설치 파일 컴파일을 완료했다.
- Windows Rust 검사 286개 통과, 실패 0개, 기존 opt-in 4개 ignored.
  제외된 실제 설치·제거 검사를 통과로 계산하지 않았다. Rust FFI 선언 등의
  기존 컴파일러 경고는 남아 있으며 경고가 없다고 표시하지 않는다.
- 실제 stage 앱의 새 테스트 데이터 경로로 runtime check 통과. 최종 결과
  `ok=true`, `phase=complete`, 백업·복원·재사용과 토큰 만료·회전 검사를
  확인했다. 실제 설치 앱이나 사용자 DB를 대상으로 실행하지 않았다.
- 체크섬을 다시 확인한 Syft 1.55.0으로 실제 payload와 Go/Rust lock 파일을
  검사했다. SPDX-2.3 패키지 691개를 생성했다. 설치 보조 프로그램과 제거
  파일을 포함한 stage가 입력이다.
- 로컬의 기존 minisign 개인키로 설치 파일, 두 manifest, SBOM, SHA256SUMS를
  서명했다. `release_artifacts.py sign`과 별도 `verify` 모두 통과했다.
  정확히 12개 자산만 전달했다. 개인키를 PC·저장소·릴리스 폴더에 복사하지 않았다.
- Windows에서 production `update_manifest.rs`를 불러오는 별도 검사 4개 통과.
  그중 실제 파일 검사는 완성된 77 MB 설치 파일과 서명 manifest를 허용하고,
  설치 파일 변조, 다른 키, 다른 키로 서명한 전체 대체 피드, 미서명·변조
  manifest와 버전 재표시를 거부했다. 일회성 검사 파일은 빌드 후 별도로 추가한
  검사 입력이며 제품 바이너리에 포함하지 않았다.
- Windows 설치 파일의 실제 제품명·제품 버전·설명·게시자 resource를 확인했다.
  로컬 원본, Windows 검증 폴더, 최종 다운로드 폴더의 설치 파일 해시가 일치한다.
  자산 12개를 각각 대조했고 최종 소유 앱·검사 프로세스 잔여는 0개였다.

## 산출물과 경계

설치 파일: `Arkism-0.1.3-setup.exe`, 77,370,692 bytes.
SHA256: `112487ab617bb1b8f450721156a27a4a9c6bcae4a2fc9f1378332acf71db12ff`.

Windows 전달 폴더: `Downloads\Arkism-0.1.3-signed-4ac870f`.
원본 자산과 비밀 없는 검사 근거는 별도 로컬 산출물 폴더에 보관한다.
소스·바이너리·서명의 연결은 이번 로컬 빌드에 대한 검증이며 재현 빌드나
공개 GitHub OIDC 출처 증명을 발급한 것으로 해석하지 않는다.

실제 제품 설치·업그레이드·제거, 공개 릴리스, push·PR·태그, 운영 키 등록,
원격 CI/Codecov/Sonar 판정은 실행하지 않았다. 설치 복구 코어와 연결 검사
159개·오프라인 정책 216개·NSIS 제어 6개의 세부 결과와 한계는
`2026-10-21-windows-installer-bridge.md`에 기록했다. 실제 설치·제거 검증,
보관 버전의 개수·용량 제한, 전체 제거 잠금은 별도 후속 작업이다.

업데이트의 minisign 인증과 Windows Authenticode는 구별한다. 최종 설치 파일의
실제 Authenticode 상태는 `NotSigned`였으며 인증서 서명·SmartScreen 평판을
확보했다고 표시하지 않는다. 개인키 보관·운영 채택의 한계는 첫 서명 빌드
기록과 동일하다.
