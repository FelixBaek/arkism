# Contributing / 기여 안내

[English](#english) · [한국어](#한국어)

## English

1. Open an issue first for anything larger than a small fix.
2. Branch from `main` and open a pull request against `main`.
3. Before pushing, run the same checks CI runs:

   ```sh
   bash tools/install-minisign.sh /tmp/arkism-test-tools
   MINISIGN=/tmp/arkism-test-tools/minisign python3 tools/check_local.py
   node --test desktop/ui/*.test.js
   cargo test --locked --manifest-path desktop/src-tauri/Cargo.toml
   ```

4. Keep pull requests focused. The merge gate is the **Checks** workflow and the SonarCloud
   quality gate; the coverage workflow also enforces package floors and Codecov
   enforces changed Go/UI coverage. The actual required-check configuration is
   described in [quality and maintenance](guide/quality-and-maintenance.md).
5. Security problems go through [SECURITY.md](SECURITY.md), not public issues.

Contributions are licensed under the [MIT License](LICENSE).

## 한국어

1. 작은 수정보다 큰 변경은 먼저 이슈를 열어 주세요.
2. `main`에서 브랜치를 만들고 `main`으로 Pull Request를 보내 주세요.
3. push 전에 CI와 같은 검사를 돌려 주세요.

   ```sh
   bash tools/install-minisign.sh /tmp/arkism-test-tools
   MINISIGN=/tmp/arkism-test-tools/minisign python3 tools/check_local.py
   node --test desktop/ui/*.test.js
   cargo test --locked --manifest-path desktop/src-tauri/Cargo.toml
   ```

4. PR은 한 가지 목적만 담아 주세요. 병합 조건은 **Checks** 워크플로와 SonarCloud 품질 게이트이고,
   커버리지 워크플로는 패키지 하한을, Codecov는 바뀐 Go/UI 코드의 커버리지를 검사합니다.
   실제 필수 검사 설정은 [유지보수 안내](guide/quality-and-maintenance.md)를 보세요.
5. 보안 문제는 공개 이슈 대신 [SECURITY.md](SECURITY.md)의 방법으로 알려 주세요.

기여한 코드는 [MIT 라이선스](LICENSE)를 따릅니다.

Follow the [community conduct policy](CODE_OF_CONDUCT.md). Every code change
should record its reason, task ID (or `Task: none`), commands actually run,
results and remaining limits. Do not mark unrun checks as passed.
