# Roadmap preparation and immutable baseline

Task: none (preparation for roadmap issue #150)
Research: none (development infrastructure; no experiment or participant result)
Source baseline: `7882ef5629321f5844a7a8aacc8f935a401e3d45`.

The roadmap needs a common baseline and file ownership before independent
implementation can share types or compare results. Existing product search,
workflow execution and evaluation programs are retained.

The preparation gate checks the original `arkism-offline-b0-v1` bytes against
SHA-256 `04463d838f397772743c5740a346230eb8e9db60db8dddc824d52e1f083790ef`.
It rejects a changed payload even if its manifest is repinned. Historical target
and command-directory fields in that data are provenance, not instructions to
run commands or access a live checkout. Runtime commands must use their assigned
test root. The baseline is separate from product authorization and data schemas.

The [ownership plan](../roadmap/ownership.md) assigns all 63 development cards,
keeps exact R1–R8 file ownership, and routes shared product changes through one
integration writer. It preserves specification, scalar, optional-extension and
acceptance gates. Private lab roots require a separate dispatch binding; the
public plan never supplies a machine's private root or running agent assignment.

Independent review corrected the earlier audit's card-level classification:
DYN-04 is optional under the original plan. There are 49 required and 14 optional
cards, with additional conditional substages. PSY-04 is assigned preparation
outputs only; collection and actual analysis paths require a separate execution
authorization gate. A matching gate name in this document is not that approval.

The existing work-record checker now supports Design, Relation and Deviation
with their Korean aliases. General changes and historical records remain
compatible. Its documented author-date boundary is a format policy, not a
trusted timestamp or proof of research fidelity. Both commit and pull-request
templates expose the fields without changing a user's shared Git configuration.

## Validation so far

- Roadmap gate: original digest, 63-card identity coverage and 93 dependency
  gates verified locally. This verifies preparation, not any card's acceptance.
- Targeted Python checks: 65 passed, no failures or skips. Mutations cover raw
  bytes, repinning, duplicate keys, missing cards, cycles, writer overlap,
  consumer ownership, unsafe paths, symlink ancestors and deferred execution.
- Research-record changes were checked with failing-before/passing-after
  regressions, including partial research metadata and template parsing.
- `git diff --check` passed.

`MINISIGN=<verified local minisign> python3 tools/check_local.py` finished with
exit0: Python tools173, evaluation22, measurement49, Go complexity/vet/full race
and UI532 all passed. PostgreSQL integration was not enabled for this run; its
skips are not database acceptance. Remote PR checks have not run yet. Publication of the immutable source is separately
reviewed against the operator's private-content rules; local availability alone
does not prove a public checkout contains it.

## 한국어

#150의 준비 범위다. 원본 계약 바이트를 바꾸지 않고 파일 소유·선행 수용·연구 기록
양식을 검사한다. 제품 검색·엔진을 교체하거나 실험 결과를 만들지 않는다.
앞선 감사에서 놓친 DYN-04의 선택 조건을 원본대로 복원했고, 사람 연구의 준비와
실제 수집 권한을 나눴다. 전체 카드 구현이나 연구 효과 검증 완료로 표시하지 않는다.

미실행: 새 연구 본실험, 유료 제공자 호출, 실제 참여자 수집, 운영 데이터 접근,
설치 파일 빌드, 공개 릴리즈와 배포. 원본 계약의 공개 여부도 실제 확인한 상태만
기록하며, 해시를 유지한 것과 공개 검사를 통과한 것을 혼동하지 않는다.
