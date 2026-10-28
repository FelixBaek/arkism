# Reproducible fixture, reference ranking and scheduled metrics

Task: R3, R5, R6, R7 (#151)
Research: P1 shared offline development baseline; unit_fixture only
Design: arkism-offline-b0-v1 fixture separation, ordinal-preserving RRF, greedy packing and scheduled-stream metrics
Relation: faithful
Deviation: Fixture publication completes a sibling staging directory before exclusively reserving the final directory and publishing complete files with manifests last. Whole-directory atomicity is not claimed. Nil ranked slices are rejected for typed-array consistency, not presented as an additional research requirement.

R3 creates eight reproducible fixture files, retaining the exact original
contract bytes. Policy/probes/candidates are separated from evaluator gold
and split data. Expected ranks remain in the development specification and
never enter ranker inputs. Existing outputs, including empty directories, are
preserved. Failed publication cleans only the generator's own inodes.

R5 preserves original lane ordinals after eligibility filtering, fuses all
eligible candidates before packing, retains first occurrence metadata and
uses the fixed tie ordering. Packing skips an oversized item so later smaller
items can fit. Costs are synthetic fixture units, not product tokenizer costs.
Existing product FuseScored behavior remains unchanged.

R6 reconciles every scheduled query and hash binding before scoring full
scope/Ref identities. Failed partial returns score zero while remaining in
the denominator and retaining raw counts. Stream macro and query weighted
recall are separate. Negative observed false positives, negative service
failures, ambiguous labels and null denominators retain distinct meanings.

## Verified locally

- R3:24 owned tests and20 independent checks passed. Five seed executions
  covered repeatability, uint64 boundaries, unchanged raw contract, eight
  artifacts, evaluator isolation and publication cleanup. Seven generated
  queries matched actual Go RankAndPack results and independent expectations.
- R5:70 leaf cases passed; three API statement coverage100%. Original11 RRF
  and five packing vectors passed. A limited clean-domain projection of the
  original product function matched300 comparisons. Four intentionally wrong
  isolated implementations failed unchanged expectations. This does not prove
  whole Service equivalence or product permission enforcement.
- R6:39 unittest methods /64 leaf cases and30 independent final-source checks
  passed. Hand-calculated stream macro0.625 and query weighted0.5, all six
  summary statistics and null/failure semantics matched. Four isolated wrong
  implementations were detected.
- R7:17 top-level tests /16 subtests, race and Windows/Darwin package vet
  passed. Independent13 final-source cases checked actual run(), seven-query
  output, reproducible bytes, input isolation, fixed contract and publication
  failures. Pinned golangci-lint v2.14.0 reports zero issues.
- Complete check_local finished with exit0: Python349, Go complexity/vet/full
  race and UI532 passed. PostgreSQL integration was not enabled.

R3 cleanup fault injection found two errors before correction: final-directory
open failure could leave a new empty directory, and staging cleanup failure
could return an error while leaving final output. Both failures were reproduced
and retested after correction. An injected failure that could not safely remove
its own staging inode was reported as such; it was never accepted as success.

No product build, installer build, release, deployment, live data access or
participant collection ran. Real memory quality, causality, product budget
equivalence and database deletion are outside this synthetic verification.
R7 directly exercises run() through Go tests. A standalone go build was not
executed. R8 reporting/acceptance and the full #151 issue remain incomplete.

## 한국어 작업 기록

작업 ID: R3, R5, R6, R7 (#151)
변경 이유: 합성 입력·정답을 분리하고, 실제 예정 질의의 실패와 빈 분모가 지표에서 사라지지 않게 한다.
실행한 검증: R3 24+독립20검사, R5 70leaf·제한된산식300대조, R6 39methods/64leaf·독립30, R7 17주검사/16하위·독립13검사 통과. 전체 check_local 종료0(Python349/Go 복잡도·vet·race/UI532), 고정lint0issues.
미실행: R8 builtbinary 완주, DB 통합수용, 제품 빌드·배포·연구 본실험.
커밋 상태: 전체검사 통과 후 다음 단계 로컬 커밋으로 고정.

## PR419 test-fixture review

A synthetic unused-oracle marker previously named secret was renamed to an
explicit non-credential sentinel. The metric hash-mismatch test now writes
JSON through a fixed opened target, separating payload from filename. These
address remote test-only security findings without changing the tested
conditions, expected values, production code or security rules. The complete
local suite passed again: Python349, Go complexity/vet/full race and UI532.
Remote reanalysis remains pending.
