# Arkism

[![Checks](https://github.com/FelixBaek/arkism/actions/workflows/checks.yml/badge.svg)](https://github.com/FelixBaek/arkism/actions/workflows/checks.yml)
[![CodeQL](https://github.com/FelixBaek/arkism/actions/workflows/github-code-scanning/codeql/badge.svg)](https://github.com/FelixBaek/arkism/security/code-scanning)
[![Quality Gate](https://sonarcloud.io/api/project_badges/measure?project=FelixBaek_arkism&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=FelixBaek_arkism)
[![Security Rating](https://sonarcloud.io/api/project_badges/measure?project=FelixBaek_arkism&metric=security_rating)](https://sonarcloud.io/summary/new_code?id=FelixBaek_arkism)
[![Vulnerabilities](https://sonarcloud.io/api/project_badges/measure?project=FelixBaek_arkism&metric=vulnerabilities)](https://sonarcloud.io/summary/new_code?id=FelixBaek_arkism)
[![Secrets: gitleaks](https://img.shields.io/badge/secrets-gitleaks-blue)](https://github.com/FelixBaek/arkism/actions/workflows/checks.yml)
[![Dependabot](https://img.shields.io/badge/dependabot-enabled-brightgreen?logo=dependabot)](https://github.com/FelixBaek/arkism/security/dependabot)
[![Secret scanning](https://img.shields.io/badge/secret%20scanning-push%20protection-brightgreen?logo=github)](https://github.com/FelixBaek/arkism/security)

[English](README.md)

Arkism은 여러 AI 코딩 에이전트가 함께 쓰는 기억 저장소입니다.

Claude Code, Codex, Gemini CLI 같은 에이전트는 어제 알려 준 것을 잊고, 스킬도 따로따로 가집니다. Arkism은 그 옆에서 일합니다. 세션에서 남길 만한 내용을 모아 사람이 검토하게 하고, 알맞은 기억과 스킬을 모든 에이전트에 다시 건네줍니다. 데스크톱 앱에서 이 모두를 사용량, 모델 라우팅, 여러 에이전트 워크플로우와 함께 한곳에서 봅니다.

**이런 분께 맞습니다:** AI 코딩 에이전트를 매일 둘 이상 쓰고, 그 에이전트들이 맥락·스킬·한도를 나눠 쓰길 바라지만 데이터를 다른 클라우드에 보내고 싶지 않은 분.

- 기본은 내 PC에서 돌아갑니다. 계정, Docker, API 키가 필요 없습니다.
- AI가 쓴 기억은 검토하기 전까지 쓰이지 않습니다.
- 오픈 소스(MIT). Go 서버와 Tauri 데스크톱 앱.

## 화면

| HOME | MEMORY |
|---|---|
| ![홈 화면](guide/images/home.png) | ![기억 화면](guide/images/memory.png) |
| **AGENT** | **ROUTER** |
| ![에이전트 화면](guide/images/agent.png) | ![라우터 화면](guide/images/router.png) |

화면의 데이터는 모두 지어낸 예시입니다.

## 설치

[Releases](../../releases/latest)에서 내 운영체제 파일을 받습니다.

| 운영체제 | 파일 |
|---|---|
| Windows 10/11 | `Arkism-X.Y.Z-setup.exe` |
| macOS(Apple silicon) | `.dmg` |
| Linux | `.AppImage` 또는 `.deb` |

아직 서명하지 않은 빌드입니다. 처음 열 때 Windows SmartScreen이 확인을 묻습니다("추가 정보" → "실행"). macOS에서는 앱을 우클릭하고 "열기"를 고릅니다.

## 빠른 시작

1. Arkism을 설치하고 엽니다. **내장 모드**로 시작합니다. 앱이 이 PC에서 작은 데이터베이스와 Arkism 서버를 직접 띄우고, `127.0.0.1`에서만 받습니다.
2. 앱이 이 PC의 에이전트(Claude Code, Codex, ZCode, Gemini CLI, Grok, Claude 데스크톱)를 찾아 훅과 MCP로 연결합니다. 에이전트 설정을 바꾸기 전에 사본을 `~/.config/arkism/backups`에 남깁니다.
3. 평소처럼 에이전트를 씁니다. 기억, 스킬, 세션, 사용량이 앱에 나타납니다.
4. **MEMORY › 기억**에서 새 기억을 검토합니다. 에이전트가 쓰길 바라는 것만 승인합니다.

여러 PC가 Arkism 하나를 함께 쓰려면 [서버 모드](#서버-모드선택)를 보세요.

## 기능

| 메뉴 | 할 수 있는 일 |
|---|---|
| **HOME** | 기억·스킬·워크플로우·프롬프트를 한 번에 찾고, 메뉴마다 확인할 것을 봅니다. |
| **MEMORY** | 기억·스킬·세션·사용량. 기억을 찾고, 검토하고, 고치고, 합치고, 지웁니다. 스킬을 버전·검사 결과와 함께 봅니다. 지난 에이전트 세션을 읽고 내보냅니다. |
| **AGENT** | 캔버스에서 워크플로우를 만들고, 작업 에이전트와 팀을 꾸리고, 팀 회의를 열고, 디스코드를 잇고, 실행 비용을 봅니다. |
| **ROUTER** | 어떤 일을 어떤 모델이 맡았는지 보고, 라우팅 규칙과 대체 순서를 정하고, 모델 공급자와 계정을 넣습니다. |
| **CONNECT** | 에이전트·MCP 서버·플러그인을 에이전트마다 켜고 끄고, Git 저장소와 PR을 봅니다. |
| **GALLERY** | 디자인 시스템, 만든 이미지, 슬라이드. |
| **설정** | 연결(내장 또는 서버), 역할별 모델, 모양(한국어·영어, 밝게·어둡게), 수집, 백업, 데이터 지우기. |

## 어떻게 동작하나요

**기억.** 일하는 동안 Arkism은 에이전트의 훅으로 대화를 봅니다. 남길 만한 내용(결정, 취향, 통한 해결법)이 보이면 작은 모델에게 *기억 후보*로 적게 합니다. 이 모델은 내 에이전트 로그인(기본은 Claude)으로 돌아갑니다. 후보는 검토를 기다립니다. 검토한 기억은 나온 세션으로 가는 연결을 지닙니다. 새 세션을 시작하면 Arkism이 지금 일에 맞는 기억 몇 개를 정해진 분량 안에서 에이전트 맥락에 넣습니다.

**스킬.** 스킬은 `SKILL.md` 폴더입니다. Claude Code 등이 이미 쓰는 형식입니다. Arkism은 스킬마다 공용 사본 하나를 버전·이력과 함께 두고, 연결된 모든 에이전트에 설치합니다. 에이전트는 Arkism MCP 서버로 필요할 때 스킬을 찾을 수도 있습니다. Arkism은 어떤 스킬이 쓰였고 결과가 어땠는지 기록하고, 반복되는 일에서 새 스킬을 제안할 수 있습니다.

**라우팅.** 쓸 수 있는 모델(구독이나 API 키)을 **ROUTER › 모델 설정**에 넣습니다. 라우터는 일마다 내 규칙에 따라 모델을 고릅니다. 모델이 실패하거나 한도에 닿으면 대체 순서의 다음 모델로 넘깁니다. 라우팅은 어느 모델이 돌았는지, 얼마나 걸렸는지, 성공했는지만 남기고 프롬프트나 답은 저장하지 않습니다.

**검색.** 내장 모드는 낱말로 찾습니다. 같은 PC에서 [Ollama](https://ollama.com)가 임베딩 모델과 함께 돌면(`ollama pull qwen3-embedding:0.6b`) 뜻으로도 찾습니다. **설정 › 역할별 모델**에서 바꿀 수 있습니다.

## 개인정보

**저장하는 것**

- 기억과 기억 후보, 그리고 나온 세션을 가리키는 연결.
- 스킬과 그 버전.
- 세션 기록: 에이전트, 모델, 토큰 수, 쓴 도구와 스킬.
- 보관한 세션 대화. 내 PC에서 만든 키로 암호화합니다. 기억이 어디서 왔는지 보여 줄 때 씁니다.
- 라우팅·사용량 수치. 프롬프트나 답은 없습니다.

**저장 위치**

내장 모드에서는 모두 내 PC의 앱 데이터 폴더에 있습니다.

| 운영체제 | 폴더 |
|---|---|
| Windows | `%LOCALAPPDATA%\dev.arkism.desktop\runtime` |
| macOS | `~/Library/Application Support/dev.arkism.desktop/runtime` |
| Linux | `~/.local/share/dev.arkism.desktop/runtime` |

수집 설정과 에이전트 설정 백업은 `~/.config/arkism`에 있습니다. 서버 모드에서는 서버의 Postgres 데이터베이스에 있습니다.

**PC 밖으로 나가는 것.** 기억 후보를 쓰려고 Arkism은 대화의 필요한 부분을 기억 추출용으로 고른 모델에 내 에이전트 로그인으로 보냅니다. 에이전트가 이미 쓰는 그 공급자입니다. Arkism 제작자에게는 아무것도 보내지 않습니다.

**빼 두기**

- 프롬프트에서 `<private>…</private>`로 감싼 말은 수집·검색하지 않고 모델에도 보내지 않습니다.
- **설정 › 고급 › 수집**: 수집을 멈추거나, Arkism이 건드리지 않을 폴더를 적습니다.
- 폴더에 빈 `.arkism-ignore` 파일을 두면 그 폴더와 아래 폴더 전체에서 Arkism이 빠집니다.

**지우기·내보내기·백업**

- 앱에서 기억이나 세션을 지웁니다. 출처를 지우면 거기서 만든 기억도 함께 지워집니다.
- **MEMORY › 세션**에서 세션을 Markdown이나 JSON으로 내보냅니다.
- **설정 › 고급 › 데이터 › 지금 백업**은 전체 백업을 `문서/Arkism Backups`에 저장합니다. 앱은 하루에 한 번 스스로 백업하고 최근 7개를 남깁니다. 같은 화면에서 백업으로 되돌리거나 모든 데이터를 지울 수 있습니다.
- 앱을 지우면 에이전트 연결이 풀립니다(Windows 설치 제거 프로그램, 그 밖에는 `arkism uninstall-arkism`). 데이터 폴더는 직접 지우기 전까지 남습니다.

## 서버 모드(선택)

Arkism을 한 컴퓨터에서 돌리고 모든 PC를 거기에 잇습니다. 집 서버나 작은 팀에 알맞습니다.

1. 서버에 pgvector가 든 Postgres 17을 설치하고 API와 워커를 띄웁니다: [guide/server.md](guide/server.md#install-by-hand)(영어).
2. 서버에 `arkism` 도구를 설치하고 소유자 토큰을 읽을 수 있는 곳에 저장합니다. 자세한 내용: [guide/server.md](guide/server.md#connect-the-desktop-app-over-ssh)(영어).
3. 앱의 **설정 › 연결**에서 서버의 SSH 주소(예: `me@my-server`)만 적습니다. 앱이 평소 SSH 키로 로그인해 서버에서 연결 정보를 읽고, 그 SSH 연결로 API를 넘겨받습니다.

설정 값, systemd, 백업: [guide/server.md](guide/server.md), [guide/backup-restore.md](guide/backup-restore.md)(영어).
- Postgres를 직접 운영하지 않고 Supabase Cloud를 DB로 쓰는 방법: [guide/server.md](guide/server.md#use-supabase-cloud-as-the-database)(영어).

## 디스코드 봇

`arkism discord`는 **AGENT › 디스코드**에서 추가한 봇을 디스코드에 잇습니다. 연결한 채널에 글을 올리면 팀 회의, 워크플로우, 에이전트 답변 하나를 시작할 수 있습니다.

요약:

1. <https://discord.com/developers/applications>에서 애플리케이션을 만들고 **Bot**에서 **Reset Token**을 눌러 나온 토큰을 Arkism에 붙여 넣습니다.
2. 같은 **Bot** 화면에서 **Message Content Intent**를 켭니다.
3. `arkism discord invite APPLICATION_ID`로 초대합니다. 링크는 봇에 필요한 권한만 묻습니다. 관리자(Administrator) 권한은 주지 마세요.

전체 설정: [guide/discord.md](guide/discord.md)(영어).

## 개발

Go(`go.mod`의 버전)와 C 컴파일러, Node.js 22, Python 3이 필요합니다. Rust(stable)는 데스크톱 앱에만 필요합니다.

```bash
go vet ./...
go test -race ./...
node --test desktop/ui/*.test.js
cd desktop/src-tauri && cargo test  # 데스크톱 앱
```

저장소 구조, Postgres 통합 검사, 데스크톱 빌드: [guide/development.md](guide/development.md)(영어).

## 라이선스

[MIT](LICENSE)
