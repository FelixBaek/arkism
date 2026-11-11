# Reflect review refactor / 회상 검토 구조 정리

Related work: PR #552.

The integration check found three newly changed functions above the cognitive
complexity limit. Extracting passage-query embedding, one source's transactional
verification, and PostgreSQL erasure-test helpers preserves the same source,
transfer, dependency and final delivery fences without adding lint exclusions.
The source wrapper is stateless, and all source verification remains inside the
same transaction. PostgreSQL tests still erase both the selected source and its
parent during generation and require delivery to be rejected.

Using gocognit v1.2.1, the unchanged committed files reproduced scores of 42, 30
and 25. After the refactor the corresponding functions score 3, 9 and 18; new
helpers score at most 15. No function in the three files exceeds 20. The analyzer
ran from a temporary offline module with cached x/tools; repository dependencies
and lint thresholds were not changed by this refactor.

With the matching cached Go 1.27.4 toolchain, `GOTOOLCHAIN=local`, `GOPROXY=off`
and `GOMAXPROCS=2`, the focused reflect/reread race selection passed 42 top-level
tests (2.887s). The real loopback PostgreSQL passage selection passed two
top-level tests and two subtests (1.617s). Both had zero failures or skips.
The first invocation with the older toolchain was rejected by the module's
minimum version; an initial sandboxed test could not bind its loopback test
server. Neither attempt is counted as passing. The permitted loopback reruns
produced the results above. `git diff --check` passed.

```sh
go test -race ./internal/memory -run '^(TestReflect.*|TestVerbatimLines.*)$' -count=1 -timeout=300s -json
go test -race ./internal/memory -run '^TestPostgresReflectPassages' -count=1 -timeout=240s -json
```

CI 복잡도 실패 세 곳을 의미 단위 함수로 나누고 같은 검사기로 기준 충족을
확인했다. 원본·부모 삭제 시 응답 차단을 포함한 실제 PostgreSQL 회귀 검사와
회상·재조회 race 검사를 모두 다시 통과했다. 제품 빌드, 실제 모델 호출 및
전체 테스트 실행 결과를 의미하지 않으며 기존 기능 한계는 그대로 남는다.
