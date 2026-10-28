# Baseline reports, collected tests and acceptance evidence

Task: R8 (#151); reviewed test fixes for R2, R3, R6 and progress maintenance
Research: P1 shared offline development baseline; no product or study outcome
Design: arkism-offline-b0-v1 CSV/report schema, evidence chain and required-test collection
Relation: faithful
Deviation: Standalone build/default acceptance execution is not authorized in this session. Mock protocol checks and run()-based integration remain separate from that unexecuted gate.

R8 exports exactly three CSV files and ReportSummary, preserving frozen columns,
CRLF, UTF-8 without BOM,17-digit floats and null empty cells. Existing outputs
are preserved. Publication failures, including CLI status failure, retract only
owned inodes; other files and symlinks are left untouched.

The acceptance runner records exact child commands, statuses and log hashes,
checks actual artifact bytes and transitive bindings, and keeps the historical
fixture reference commit separate from observed commit/dirty-file provenance.
Only allowlisted dirty file bytes are read. Missing required artifacts suppress
bundle publication. Zero tests, required skips, expected failures and missing
evidence cannot produce checks_passed. Review status remains
requires_independent_review.

The outer evaluation collector loads five harness test modules and turns both
runtime and import-time skips into failures. Root integration removed duplicate
direct discovery from check_local and CI. Public progress/ownership now records
locally accepted R1–R7 and the incomplete R8 runtime gate, instead of saying all
cards are planned. The original contract bytes and digest remain unchanged.

## Verified locally

- Report:23 owned tests and16 independent checks passed, including complete
  CSV row/column comparison, null/zero, failure rows, Unicode formatter and four
  raw artifacts.
- Acceptance:40 methods /48 leaf mock cases passed. The actual collector ran
  168 tests, with no failures or skips.
- Independent protocol checks compared14 check records and30 artifact entries,
  their raw hashes/lengths, four top-level bindings and nonrecursive evidence.
  Its build artifact was a nonexecuted test marker.
- A separate actual Go test run() → metrics → report seven-query pipeline
  passed without go build or the default acceptance runner.
- Independent boundary checks covered16 initial acceptance/collector cases,
  nine final-publication cases and four metadata-failure cases.
- The final complete workspace check_local exited0: Python412, Go
  complexity/vet/full race and UI532 passed. PostgreSQL integration was not enabled.
- Diff and work-record checks passed.

Failing-before regressions corrected an extra expected ReportSummary field, an
incomplete Ref oracle, import-time skips escaping collection, success diagnostics
surviving final bundle failure, and descriptors leaking on metadata failure.
Ownership is captured before public linking. A failed bundle now retracts owned
success records and publishes an honest failure diagnostic. Descriptor lifetime
is secured before metadata access. If one recovery attempt cannot establish an
inode identity, unknown temporary files are preserved and the cleanup limitation
is explicitly reported; they are never silently treated as cleaned. Final source
hashes remained stable during independent verification.

The default standalone path is implemented but has not run. R8's full execution
gate and #151 remain incomplete. No actual baseline-binary build, product or
installer build, release, deployment, paid call or participant collection ran.
These checks do not establish new retrieval quality, product authorization, real
token cost or database deletion completion. Reviewed test-only corrections from
PR418/419 are carried forward without changing expectations or security rules.

## 한국어 작업 기록

작업 ID: R8 (#151), R2/R3/R6 검토 후속 및 진행 문서 관리
변경 이유: 보고서와 실행 증거를 연결하고 0개 검사·필수skip·누락hash·실패한 저장이 완료로 보이지 않게 한다.
실행한 검증: report23/독립16, acceptance40methods·48leaf, collector168검사, 독립16+게시9+metadata4경계, 모의14check·30artifact 해시대조, 실제run() 기반7query연결. 최종 전체 check_local 종료0(Python412/Go/UI532).
미실행: standalone build·기본acceptance완주, DB통합수용, 제품빌드·릴리스·배포·연구본실험.
커밋 상태: 구현과 검증 기록을 같은 단계 PR로 관리한다. 실제 SHA·원격 상태는 진행표와 Git 이력을 따른다. 전체R8수용은 대기.
