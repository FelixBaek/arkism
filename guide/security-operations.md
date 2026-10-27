# Windows update and release security

Only the Windows desktop installer is distributed by this workflow. Linux is
used as a verification/signing host; it is not an additional app release target.
These source changes do not retroactively sign existing public releases.

## Trust roots and verification order

`ARKISM_UPDATE_PUBLIC_KEY` is **base64 of the entire minisign public-key file**,
including its comment and final newline, as in Tauri. Compile it into the app;
never download or choose it from the feed. `ARKISM_UPDATE_FEED` is a static HTTPS
directory and `ARKISM_UPDATE_CHANNEL` is `stable` (default) or `beta`.
Missing/invalid keys, non-HTTPS URLs and HTTPS-to-HTTP redirects are refused.

1. Fetch a bounded `latest.json` (or `latest-beta.json`) and its `.minisig`.
2. Verify the exact manifest bytes with the compiled public key **before**
   interpreting the version, channel, installer filename or SHA-256.
3. Require the expected channel and a version newer than the installed version.
4. Download the bounded installer over HTTPS. Check its authenticated SHA-256
   and its independent minisign signature before staging or launching it.

Replacing the whole feed does not replace the trust root. Old signed releases
cannot be relabeled with a higher version without invalidating the manifest
signature. A stale feed can still withhold an update; signing does not provide
availability, prove the program is harmless or protect a stolen signing key.

The installer `.minisig` uses modern prehashed minisign (Ed25519 with BLAKE2b).
Its `.sig` is base64 of the **complete signature file**, matching Tauri's updater
format. The release also includes `latest-tauri.json` with a
`windows-x86_64` platform entry. A future plugin migration can use that manifest
and the same key/signatures; the current Arkism manifest has additional fields
and is independently signed. Raw Ed25519 signatures from draft PR #109 are a
different wire format and must not be mixed with this one.

## Key custody and bootstrap

Generate the production key on an operator-controlled signing device, keep an
encrypted offline backup, and independently record the public-key fingerprint.
For example, run `minisign -G -p update.pub -s update.key` interactively there.
Do not put either the private key or its password in source, build artifacts,
the feed directory, logs, or the Windows build job. Test fixtures contain only
an inert public key and signed inert bytes; their private key was discarded.

The maintainer configures these values; source code does not create or change
GitHub settings:

| Location | Value |
| --- | --- |
| Repository variable `ARKISM_UPDATE_PUBLIC_KEY` | Base64 of the full production `.pub` file, independently reviewed before building |
| Environment `release-signing` | Protect tag access and require a trusted human reviewer before exposing signing secrets |
| Environment secret `ARKISM_UPDATE_SIGNING_KEY` | Contents of the encrypted minisign secret-key file |
| Environment secret `ARKISM_UPDATE_SIGNING_PASSWORD` | Password for that key |

The protected signing job runs on a disposable GitHub-hosted runner **after**
the Windows build and provenance checks. It writes the key with mode `0600` in
an owned temporary directory and removes it in `finally`, including on failure.
No secret is passed to PR builds. Separate review of this environment and the
workflow is essential: repository write access alone must not grant signing
approval. For stricter custody, keep the key offline and run the same signing
and verification commands on the signing device; upload only verified assets.

Existing unsigned apps need a manually verified first installation to acquire
the pinned key. Serving a new public key over their old unsigned feed cannot
establish independent trust. Changing the configured key affects new builds
only; installed apps retain their old key. Planned rotation needs a verified
transition release; compromise requires an independently authenticated recovery
installation. Never silently accept a key distributed by the feed.

## Release gates and assets

The Windows build uses pinned runtime inputs and a pinned Syft executable
(version and archive SHA-256 in `release.yml`). Syft scans the staged Windows
payload plus Go/Rust dependency locks and produces SPDX JSON. Lockfile entries
may include build-only or unused dependencies; an SBOM is an inventory, not a
vulnerability scan or proof of complete binary attribution. Syft's automatic
update check is disabled.

The Windows build job creates an `actions/attest-build-provenance` attestation
for the final installer and SBOM. The signing job verifies both digests against
the repository, the exact source commit, the expected release workflow and a
GitHub-hosted runner before signing. The public `provenance.intoto.jsonl`
bundle permits independent verification; this does not assert a SLSA level or
bit-for-bit reproducibility.

The signed bundle contains the installer and both signature encodings, SPDX
SBOM, Arkism and Tauri manifests, their detached signatures, provenance bundle,
`SHA256SUMS`, and `SHA256SUMS.minisig`. The same publisher key signs the manifests,
installer, SBOM and checksum inventory. The verifier rejects missing, extra,
linked, empty, duplicate or modified assets, mismatched keys, and inconsistent
version/channel/feed metadata. Signer errors and failed verification stop before
any release creation command. The exact-commit quality gate is rechecked too.

Offline verification of downloaded assets (using an independently trusted key):

```bash
export ARKISM_UPDATE_PUBLIC_KEY="$(base64 < /trusted/update.pub | tr -d '\n')"
python3 tools/release_artifacts.py verify --directory /path/to/downloaded-assets \
  --version X.Y.Z --feed https://github.com/FelixBaek/arkism/releases/latest/download
gh attestation verify /path/to/downloaded-assets/Arkism-X.Y.Z-setup.exe \
  --bundle /path/to/downloaded-assets/provenance.intoto.jsonl \
  --repo FelixBaek/arkism \
  --signer-workflow FelixBaek/arkism/.github/workflows/release.yml \
  --source-digest FULL_RELEASE_COMMIT_SHA --deny-self-hosted-runners
```

GitHub CLI must support `gh attestation verify`; older CLI releases do not.
For local signing, use `release_artifacts.py sign` with the same arguments and
`--key /private/update.key`. The feed publisher accepts **pre-signed bundles**
and a trusted public key; it never needs the private key. It verifies the staged
copy again before publishing. Readers racing the manifest/signature renames
may receive a verification failure and retry; no unauthenticated fallback exists.

Minisign proves update publisher authenticity. Authenticode and Windows
SmartScreen publisher reputation are separate; no Windows certificate is
provisioned by these changes. Publish a fresh tag only after actual Windows
installation/upgrade tests and independent key review. Do not replace existing
published assets or claim these source changes have already shipped.

Primary references: [Tauri updater](https://v2.tauri.app/plugin/updater/),
[minisign](https://github.com/jedisct1/minisign),
[GitHub provenance](https://github.com/actions/attest-build-provenance),
[GitHub attestation verification](https://cli.github.com/manual/gh_attestation_verify),
[Syft](https://github.com/anchore/syft).

## SSH server identity on Windows

Before the first authenticated desktop SSH operation, Arkism displays the
resolved host, port, key type and SHA-256 fingerprint. Compare it with the
administrator through a separate trusted channel, then explicitly approve it.
The checkbox and approval button are both required; Cancel and Escape save no
approval. Existing system `known_hosts` entries do not grant app approval.

The discovery handshake disables target public-key, password, interactive,
host-based and GSSAPI authentication, forwarding and remote commands. It can
send the target username in an SSH `none` authentication request. Discovery is
not itself proof of identity; the independent comparison establishes trust.
User-controlled SSH configuration and any configured ProxyCommand/ProxyJump
remain trusted local configuration. A jump host can use its configured login;
the no-credential discovery claim applies to the final target.

Approvals live in the app runtime's separate `ssh-hosts` directory, protected
for the current Windows user and SYSTEM. Authenticated operations use strict
checking and exactly that approved key. They disable system/global key fallback,
external KnownHostsCommand, DNS key trust, automatic key updates and reused
multiplexed connections. The app does not edit or delete the user's `.ssh`
files. Approval challenges expire after five minutes, are consumed once and
reject a record changed during review. Pin snapshots are immutable: replacing
an approval does not silently change a running tunnel's reconnect identity.

A changed key blocks authentication and opens a review showing both old and new
fingerprints. Confirm the change independently before replacing it. Reconnect
a tunnel after approving a replacement. The desktop connection test, API tunnel,
Git screen and skill execution share this policy and the active connection's
explicit port and identity file. App-managed Windows agents receive the same
pin in `agents.json`; older profiles without it fail closed and need reconnecting
in the updated app. Explicit CLI `--ssh-host` and environment-only setups use
strict system `known_hosts` and require operators to provision trusted keys.

Windows SSH processes run inside the existing owned Job with bounded I/O and
timeouts. The opt-in native regression test uses fresh keys, a separate `-F`
configuration and a loopback-only Go SSH fixture; it never authenticates to a
real server or changes user SSH settings. The Windows CI job runs this test.

## PR #109 런타임 보안과 운영

아래 운영 경계는 최신 minisign·공통 라우트 인가 구현에 맞춰 통합했다.
Linux/macOS 배포 예제와 이전 raw Ed25519 서명 도구는 포함하지 않는다.

## 신뢰 경계

| 경계·위협 | 통제 | 남는 한계 |
|---|---|---|
| 브라우저 → 로컬 API, DNS 리바인딩 | 루프백 Host 검사, bearer 또는 gateway 전용 헤더, CORS 비개방 | 같은 OS 사용자는 파일·프로세스에 접근할 수 있음 |
| 에이전트 → 다른 사용자·프로젝트 | 저장소 범위 조건, FORCE RLS, 역할별 권한, 현재 정본 재검사 | DB superuser·OS 관리자 침해는 이 경계 밖 |
| 외부 문서·모델 → 기억 | 비밀 검사, 자료/지시 분리, 후보만 생성, 사람 승인 | 의미적 사실성·프롬프트 공격 완전 차단을 보장하지 않음 |
| 파일·복원 → 저장소 | 내용주소·루트 제한·링크 방어, 복원 검토·쓰기 펜스 | 디스크와 백업 키 보호는 운영자 책임 |
| 제공자·느린 클라이언트 → 게이트웨이 | 8MiB 본문, 16KiB 헤더, 헤더 5초/본문 30초/idle 60초, 요청에 묶인 최대 10분 upstream, 쓰기당 30초 | 제공자 자체 장애·호스트 전체 자원 고갈 |
| 업데이트 피드 침해 | 앱에 고정한 minisign 공개키로 manifest 원문·설치파일 검증, 기존 downgrade 차단 | 서명키·빌드 시스템 침해, 새 앱 이전의 기존 무서명 설치본 |

API·게이트웨이·crewrun은 로컬 IP에 바인딩한다. 서버 API의 비루프백 운영은 별도 인증·TLS·접근 정책 검토가 필요하다. SSH 전달 HTTP Host는 localhost 또는 루프백 IP다. TLS 프록시로 crew callback을 공개하는 운영자는 backend Host를 루프백으로 재작성하고 별도 인증·접근 정책을 유지한다. 게이트웨이 인증 유예 플래그는 운영에서 쓰지 않는다. TLS 종료가 없으므로 앱 자체 HSTS, Redis/Kafka/Kubernetes 설정은 적용 대상이 아니다.

## 인증과 교체

보호된 API의 인증 누락은 401과 `WWW-Authenticate`, 유효 주체의 권한 부족은 403이다. Google 교환 요청은 프로세스당 burst 10회, 6초당 1회 보충이다. 게이트웨이 인증 실패는 burst 20회, 초당 1회 보충한다. 성공한 인증은 실패 한도를 소비하지 않는다. 둘 다 클라이언트가 위조할 수 있는 IP 헤더를 쓰지 않는다.

Google 로그인 서버는 코드·PKCE verifier·loopback redirect를 받고 Google 토큰 endpoint에서 직접 받은 클레임의 issuer/audience/expiry/이메일을 검증한다. **Google 브라우저 클라이언트의 state 생성·보관 구현은 이 저장소에 없다.** 연결하는 클라이언트가 CSPRNG state와 PKCE를 생성하고, 같은 로그인 시도의 정확한 state·redirect를 확인한 뒤 코드를 교환해야 한다. Rust의 `wait_redirect`는 MCP 로그인용이며 Google state 구현의 증거로 재사용하지 않는다. 실제 Google 로그인 end-to-end는 별도 확인이다.

게이트웨이 토큰은 단일 호스트 자격이다. 교체는 새 랜덤 32바이트(64 hex) 생성 → 0600 파일 원자 교체 → 게이트웨이 재시작 → 연결된 에이전트 설정 갱신 → 구 토큰 거부·새 토큰 성공 확인 순서다. 점검 창 동안 클라이언트를 중지한다. 무중단 다중 토큰 교체를 지원한다고 표시하지 않는다. principal 토큰은 별도 주체 발급·검증 뒤 이전 주체를 폐기할 수 있다.

OS 키링이 가용하면 기존 키 로더가 저장·읽기 검증 후 파일에서 평문을 제거한다. 가용하지 않으면 0600 평문 fallback을 유지한다. 키링 이관 실패 시 원본을 지우지 않는다. 키링·DB·블롭·백업을 포함하는 디스크는 OS 전체 디스크 암호화로 보호하고 복구키는 별도 보관한다. 호스트의 실제 암호화 상태는 운영자가 확인한다.

## 상태·확장·보존

- 외장 PostgreSQL 하나가 정본이다. 풀 기본값은 프로세스당 최대 10연결, 연결 수명 1시간, idle 5분이며 DSN `pool_max_conns`, `pool_max_conn_lifetime`, `pool_max_conn_idle_time`으로 조정한다. 전체 API/worker 수 × 풀 한도 + migration/backup 여유가 DB 한도보다 작아야 한다.
- 검색은 마지막 현재성 검사·사용/감사 기록 단계만 scope별로 순서를 정해 작은 표의 Serializable 재시도 충돌을 줄인다. 검색·제공자 호출과 다른 scope는 병렬이며, 기다리는 요청은 context 취소를 따른다. DB 격리 수준과 재검사는 유지한다. 이 조정은 한 서비스 인스턴스 안에서만 적용된다.
- embedding·삭제 잡은 DB 리스/CAS로 여러 worker가 처리할 수 있다. crewrun 예약 실행은 같은 호스트·상태 경로의 singleton이다. 호스트 간 cron 다중 실행, API의 모든 유료 생성 동작의 전역 exactly-once, 수평 확장 전체를 보장하지 않는다.
- `MEMORY_CANDIDATE_EXPIRY_DAYS=30`과 `MEMORY_CANDIDATE_EXPIRY_SCOPE=workspace/project/user/agent`를 **둘 다** 설정하면 정상 worker가 시작 시와 매시간 최대 100개의 기한 지난 비충돌 후보를 만료한다. worker에 해당 scope의 `expire:memory_candidates` 권한이 필요하다. 기본 비활성, 복구 모드 비활성이다. approved 기억을 자동 삭제하지 않는다.
- `audit_events`(허용·거부 기록)만 보존 기간이 있다. `MEMORY_AUDIT_RETENTION_DAYS`(기본 30, 0이면 끔)보다 오래된 행을 `memoryd -audit-expiry preview|execute`가 지운다. 스키마 소유자(`arkism_init`) 연결로만 돌고, 지우기 전 건수를 로그에 남기며, `preview`는 세기만 한다. 서버는 `tools/audit_retention.sh execute`를 타이머(`deploy/systemd/arkism-audit-retention.*`)로 매일 돌리고, 데스크톱은 하루 한 번 스스로 돌린다. source_events·provider_usage·삭제 원장은 삭제·비용·근거 추적에 참여하므로 날짜만 보고 지우는 TTL을 넣지 않는다. 운영자는 DB/블롭 크기·증가량을 매주 기록하고 30일 성장량과 최소 2배 백업/복원 공간을 확보한다. 저장소 여유 20% 미만이면 수집 규모를 줄이고 보존 정책 검토를 시작한다. 이 수치는 운영 경보 기준이지 측정된 수용량이 아니다.
- 용량 회수는 암호화 백업·복원 검증 → 사용하지 않는 승인 기억의 사람 검토 → 기존 삭제 API·삭제 영수증 → 백업 보존 기한 경과 후 정리 순서다. 감사·삭제 원장을 임의 SQL로 지우지 않는다. 조직별 보존 기한·법적 보존은 해당 운영자가 정한다. 자동 보존 purge는 `audit_events` 보존 기간 하나뿐이다.
- 손상된 큐 payload는 같은 트랜잭션에서 컬럼 상태를 failed로 격리하고 원래 payload/doc를 그대로 남긴다. 이후 잡은 계속 진행한다. 관리자가 원본을 안전하게 복구하거나 공식 삭제 절차로 정리한다. 일반 record API로 손상 payload를 읽을 수 있다고 가정하지 않는다. 패닉은 원문 없이 오류가 되고, 정상 재시도 한도를 소비한다.
- 알림은 transient HTTP(429/5xx/통신 실패)·SMTP(4xx/네트워크 실패)만 최대 3회 재시도한다. 실패는 워크플로 노드 오류로 남는다. 응답 유실 후 중복 알림이 가능하며 exactly-once 전송은 아니다.

## 진단·목표·복구

`memoryd --log-level debug|info|warn|error` 또는 `ARKISM_LOG_LEVEL`로 JSON 로그를 조절한다. 본문·검색어·비밀은 로그에 넣지 않는다. `--pprof 127.0.0.1:6060`은 별도의 `MEMORY_DIAGNOSTICS_TOKEN`(32바이트 이상) 없이는 시작하지 않는다. bearer를 헤더로 보내고 pprof 파일은 비공개로 보관한다. heap·CPU profile을 `go tool pprof`로 열면 flame graph를 볼 수 있다. 진단 listener는 기본 꺼짐이며 API 포트에 노출하지 않는다.

데스크톱은 매분 네 종류의 로그가 8MiB를 넘으면 최근 1MiB를 최대 3개 snapshot으로 보관하고 활성 파일을 비운다. 실행 중 파일 핸들을 유지하기 위한 copy/truncate 방식이라 경계의 일부 로그는 유실될 수 있다. 분당 쓰기량만큼 순간 상한을 넘을 수 있다. Windows 자식 Go 프로세스의 512MiB 연성 메모리 목표는 실제 대표 부하로 조정한다. 강제 상한이 아니다.

| 지표 | 수용 목표 | 검증 방법 |
|---|---|---|
| lexical 검색 | p95 ≤ 800ms | `tools/http_load.py`, 실제 HTTP·인증·SQL·응답 읽기, 100회 이상 |
| vector 포함 검색 | p95 ≤ 1500ms | 같은 도구, 실제 승인된 embedder·대표 데이터 |
| Context 구성(LLM 제외) | p95 ≤ 2000ms | 실제 해당 API의 외부 관측 |
| 수집(색인 제외) | p95 ≤ 500ms | 합성 원본·독립 측정 |
| 백업/복원 | RPO ≤ 24시간, RTO ≤ 4시간 | 일일 외부 백업·분기 복원 훈련 |

이 값들은 기존 요구사항의 수용 목표다. 현재 운영 SLO 충족이나 SLA를 선언하는 수치가 아니다. 측정 실패·오류 응답은 좋은 지연으로 세지 않는다. 월간 목표 초과나 복원 실패가 있으면 새 기능 출시를 멈추고 원인·용량·회귀부터 수정한다. 단일 호스트 장애는 서비스 중단이며 자동 HA를 제공하지 않는다.

측정 입력은 운영자가 소유한 `~/.config/arkism/http-load` 안에 둔다. 토큰 파일은 0600이다. 별도 폴더는 운영자가 `ARKISM_HTTP_LOAD_DIR` 환경변수로 지정한다. CLI 경로는 이 폴더 밖·부모 탐색·외부로 향하는 링크를 거부한다. 측정 URL은 루프백 IP만 허용하며 원격 서버는 SSH 전달을 사용한다. 폴더는 그룹/기타 사용자 쓰기 권한이 없어야 한다.

`python3 tools/http_load.py --base-url http://127.0.0.1:8091 --token-file PRIVATE_TOKEN_PATH --body-file SYNTHETIC_SEARCH_JSON --requests 100 --concurrency 4 --p95-ms 800`은 집계 p50/p95/p99·성공/실패·HTTP 상태별 건수만 출력한다. 오류 본문이나 경로는 출력하지 않는다. 30회 미만·오류 1건 이상·p95 초과는 실패다. 공개 CI의 별도 합성 검사는 정확성과 측정 동작만 검증하며 장비별 성능 보증을 대체하지 않는다. 결과를 모델 검색 정확도·품질 향상으로 해석하지 않는다.

문제가 생기면 읽기 전용 상태/메트릭 확인 → 위험한 쓰기 중지 → 비공개 근거 보존 → 백업과 호환 스키마 확인 → 수정 또는 검증된 버전 재설치 → 복원·권한·삭제 재검증 순서로 처리한다. 사후 분석은 `guide/postmortem-template.md`를 따른다.
