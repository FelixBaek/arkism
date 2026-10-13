# Contributing / 기여 안내

[English](#english) · [한국어](#한국어)

## English

1. Open an issue first for anything larger than a small fix.
2. Branch from `main` and open a pull request against `main`.
3. Before pushing, run the same checks CI runs:

   ```sh
   go vet ./... && go test ./...
   node --test desktop/ui/*.test.js
   cargo test --locked --manifest-path desktop/src-tauri/Cargo.toml
   ```

4. Keep pull requests focused. The merge gate is the **Checks** workflow and the SonarCloud
   quality gate; CodeRabbit and Gitar leave review comments, Codecov reports coverage.
5. Security problems go through [SECURITY.md](SECURITY.md), not public issues.

Contributions are licensed under the [MIT License](LICENSE).

## 한국어

1. 작은 수정보다 큰 변경은 먼저 이슈를 열어 주세요.
2. `main`에서 브랜치를 만들고 `main`으로 Pull Request를 보내 주세요.
3. push 전에 CI와 같은 검사를 돌려 주세요.

   ```sh
   go vet ./... && go test ./...
   node --test desktop/ui/*.test.js
   cargo test --locked --manifest-path desktop/src-tauri/Cargo.toml
   ```

4. PR은 한 가지 목적만 담아 주세요. 병합 조건은 **Checks** 워크플로와 SonarCloud 품질 게이트이고,
   CodeRabbit·Gitar는 리뷰 의견을, Codecov는 커버리지를 보여 줍니다.
5. 보안 문제는 공개 이슈 대신 [SECURITY.md](SECURITY.md)의 방법으로 알려 주세요.

기여한 코드는 [MIT 라이선스](LICENSE)를 따릅니다.
