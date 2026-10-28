# Strict offline baseline and exact scalar decay

Task: R1, R2, R4 (#151)
Research: P1 shared development baseline; synthetic verification only
Design: arkism-offline-b0-v1 typed I/O, canonical bytes and ratio-first exponential
Relation: faithful
Deviation: Python matches the Go JSON parser depth limit without changing interpreter globals. Historical fixture provenance remains fixed.

R1 introduces27 concrete Go DTOs and strict decoding of150 required fields.
Canonical hashes use the frozen typed representation, preserving integer
precision, binary64 values, Unicode bytes and array order. R2 provides matching
Python validation, canonical hashes and manifest loaders. It verifies hashes
transitively, keeps ranker inputs separate from evaluator gold/splits, rejects
unsafe paths and symlinks, and publishes completed JSON files exclusively.
R4 is an isolated exact half-life function. It validates finite inputs before
range checks and divides time by half-life before exponentiation, including
subnormal and overflow boundaries. It is not connected to B0 ranking.

Existing product search, service APIs and authorization are unchanged. The
original contract and independent golden assets remain read-only. Fixture
source_commit remains the historical value; execution provenance is separate.
Local and CI checks explicitly discover the hyphenated harness directory so
these tests cannot silently disappear from ordinary unittest collection.

## Local card acceptance

- R1 final root-owned race run:183 leaf cases /196 pass nodes, failures0, skips0.
  Concrete27 DTOs and the original eight canonical vectors were independently
  checked; package vet and complexity checks passed.
- R2 Python unittest:42 passed. Independent Go/Python comparison:1077 cases
  (63 accepted,1014 rejected), canonical/code/path differences0.
- R4 independent tests:46 leaf cases /52 pass nodes, failures0, skips0;
  Decay statement coverage100%. Three isolated faulty variants were rejected
  by unchanged expectations.
- Full check_local finished with exit0: Python286, Go complexity/vet/full race,
  and UI532 passed. PostgreSQL integration was not enabled.
- git diff --check and work-record report validation passed.

Independent review found and corrected Python recursion exceptions in deeply
nested invalid objects and rejection of finite309-digit integers in binary64
fields. Failing-before regressions were retained. Numeric comparisons now use
the original additive absolute-plus-relative tolerance, with a boundary test
that rejects the former max-tolerance implementation. An intentional duplicate
JSON tag negative fixture now uses runtime reflection so vet can check the
package without weakening the rejection test. Final independent cross-checks
ran against stable source hashes and removed their temporary Go test files.

Local acceptance of R1/R2/R4 permits dependent implementation. It does not
complete #151: R3 and R5–R8 remain. Remote publication awaits the immutable
contract exception and remote PR/CI have not run. PostgreSQL integration was
not enabled and is not accepted by these checks. No product build, deployment,
model call, participant collection or memory-performance claim was made.

## 한국어 작업 기록

작업 ID: R1, R2, R4 (#151)
변경 이유: Go/Python의 고정 입력·해시를 일치시키고 정답 유입과 결과 덮어쓰기를 막는다. 시간 감쇠는 제품 순위와 분리해 수학적으로 검산한다.
실행한 검증: R1 race183 leaf, R4 독립46 leaf, Python42검사, 교차언어1077사례 통과. 최종 전체검사 종료0: Python286, Go 복잡도/vet/전체race, UI532 PASS.
미실행: 원격 PR/CI, 제품 빌드·배포, DB 통합수용, 연구 본실험.
커밋 상태: 로컬 커밋으로 고정.
원격 상태: 원본계약 공개 예외 대기로 push/PR 미실행.

## PR418 lint follow-up

The remote Go linter found an overwritten initializer in codecString. Replacing
the initializer with a boolean declaration preserves the exhaustive switch
behavior. The cached pinned golangci-lint v2.14.0 reports zero issues.
The linter ran against the existing local module cache, without network or
package installation. The repeated complete local check finished with exit0: Python286, Go
complexity/vet/full race and UI532 passed.
This follow-up changes no contract bytes, field rules or expected results.
