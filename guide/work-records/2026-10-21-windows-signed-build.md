# 2026-10-21 Windows 서명 설치 파일 검증

작업 ID: Task: none (공개 평가 P0-1, 사용자가 승인한 Windows 빌드)

## 변경 이유와 대상

체크섬과 설정만으로 서명된 업데이트를 입증할 수 없어 실제 Windows 설치 파일을
만들고 같은 공개키가 들어간 업데이트 코드로 검증했다. 사용자가 Claude 작업 완료
후 빌드를 허용한 것을 확인했다. 최신 main `e5f7250`에 검증한 서명·품질 변경을
올린 소스 `2e73b0eed5239607b528a6b6e03abb0abac53753`을 사용했다.
별도 브랜치의 공통 HTTP 인가 후속 변경은 이 설치 파일에 포함하지 않았다.

Git archive SHA256:
`b403c12e870b5c129502f454096bf7a5009afb69273541a36f6be1368b985603`.
버전은 `0.1.3`, 채널은 `stable`, 피드는 공개 GitHub latest/download다.
다른 OS 앱은 패키징하지 않았다.

## 실행한 검증

- Windows 네이티브 Go 컴포넌트와 PostgreSQL 17.6-1/pgvector 0.8.1 런타임을 준비하고
  Rust release와 NSIS 설치 파일 빌드에 성공했다. 최초 Cargo 프록시 실행 실패는
  설치된 실제 stable 도구 경로를 사용해 해결했다.
- Windows Rust 검사 270개 통과, 기존 opt-in 검사 4개 ignored. 설치·제거를 실행하는
  opt-in 검사를 실행한 것으로 계산하지 않았다.
- 실제 stage의 격리 런타임 검사 통과: `ok=true`, backup/restore/reuse/expiry 및
  capabilities HTTP 200 확인. 기존 설치·사용자 데이터는 사용하지 않았다.
- 별도 호스트 UI 검사 48개, 도구 검사 90개 및 서명 도구 검사 8개 통과.
- Syft 1.55.0으로 실제 Windows payload와 의존성 lock 파일을 검사해 SPDX-2.3의
  패키지 686개를 생성했다. 도구 archive 체크섬도 공식 배포값과 대조했다.
- 설치 파일, 두 manifest, SBOM, SHA256SUMS를 실제 minisign 키로 서명했다.
  `release_artifacts.py sign`과 별도 `verify` 통과. 정확히 12개 자산을 전달했다.
- Windows에서 실제 production `update_manifest.rs`를 가져오는 일회성 검사를
  추가 실행했다. 실제 71 MB 설치 파일과 서명 manifest를 허용하고, 파일 변조,
  다른 키, 다른 키로 서명한 완전한 대체 피드, 미서명/변조 manifest, 버전 재표시를
  거부했다. 이 검사는 기존 바이너리를 바꾸거나 설치 파일을 실행하지 않았다.
- 로컬 원본과 Windows 다운로드 폴더의 설치 파일 SHA256이 일치했다.

설치 파일: `Arkism-0.1.3-setup.exe`, 71,869,111 bytes.
SHA256: `30f2222f4f92b5c58cbe8f9a7e9ea1f56fa6e17b128050129719ee912df6530c`.
공개키 파일 SHA256: `7664f436424dbe19e5848ed134e58ba98765ecb05e5c198ccb53c94d4ba684cf`.

## 키 보관과 한계

새 빌드 전용 키를 호스트의 개인 디렉터리(0700)에 생성했다. 개인키는 암호 없는
파일(0600)이므로 호스트 파일 접근 권한에 의존한다. 개인키는 저장소·PC·릴리스
자산에 복사하지 않았다. PC에는 공개키가 고정된 앱과 서명 자산만 전달했다.
원격 서명 환경에 등록하거나 운영 키로 공개 채택한 상태는 아니다.

minisign 업데이트 인증은 통과했지만 Authenticode 인증서와 SmartScreen 평판은
없다. 로컬 빌드에는 GitHub hosted OIDC/SLSA attestation을 발급하지 않았다.
해당 출처 증명과 정확 커밋 릴리스 게이트는 실제 hosted workflow에서 별도로
검증해야 한다. 기존 미서명 앱이 이 서명을 검증하도록 소급 변경되지는 않는다.

미실행: 실제 사용자 설치·업그레이드·제거, 원격 CI/설정 변경, push·PR·태그·공개
릴리스. 기존 앱·사용자 데이터는 변경하지 않았다. 빌드/검사 로그와 산출물은
로컬 근거 폴더에 보관하며 바이너리·개인키를 공개 Git 이력에 넣지 않는다.

커밋 상태: 빌드 결과를 기록하는 로컬 문서 커밋. 설치 파일 소스는 위의
`2e73b0e`이며 이후 기록 커밋과 구별한다.
