# Connect optional attempt observation to existing crew calls

Task: CRW-02 (#154)
Research: E engineering instrumentation; no effectiveness or billing result
Design: Accepted crew v2 protocol, existing FLOW receipts, host usage normalizer and trace producer
Relation: adapted
Deviation: Programmatic opt-in and local fake schedules only. Actual child delegations, opaque backends and vendor CLI internals retain partial coverage; they are not invented model receipts.

The existing workflow, collaboration and meeting call paths now capture actual
run/definition revisions, stable phases, retries and fallback lineage. FLOW
reservation and settlement keep their existing authority and retry rules. A
lost settlement response retains captured paid usage while still blocking
redispatch. Observation is off by default and never grants approval or effects.

An optional private local journal carries host leaf observations across the
runner process. It captures failed calls before a later fallback replaces the
operational result. Strict versioned records, parent proofs and closed exact
membership journals contain refs, hashes and accounting only. Bounded reads,
exclusive writer ownership and inode checks preserve unknown/foreign files.
A killed process or malformed/missing evidence retains valid earlier charges
and marks capture incomplete. Provider reports, estimates and unknowns remain
distinct; unlabelled legacy numbers are preserved as nonadditive diagnostics.

Original host journals prove one local sender boundary. Causal retries and
workers can outlive that boundary. The report API therefore returns an
independent captured snapshot and a separate controller-boundary evidence
view. It verifies the original local closure first, then applies the unchanged
v2 qualifier to exact transitive membership. Actual reserved-record hashes
identify opening events; a record-set digest binds captured revisions at
closing. Original evidence remains available unchanged. A report refers to its
captured snapshot, not later revisions or whole-run coverage. Overlapping
controller groups and per-call aggregate costs are separate views, never
additional charges. RuntimeAuthenticated=false and RunCoverage=unknown remain.

Trace delivery reports pending, missing, invalid receipt and sink failures
without changing the existing work result. Its 64-event final-node projection
remains separate from attempt history. Standalone meetings and assists use
their actual collections; missing scope or revision evidence stays incomplete.
Child delegations use a real non-model parent and remain partial through waits,
resume and completion. Duration observation uses its own monotonic clock. Automatic run wall duration
is not captured; the existing Timing API keeps caller-supplied/unknown wall
duration separate from the sum of provider-call durations.

Verified so far: root runtime/actual-runner/FLOW/report race19methods/22leaf;
separately rerun meeting/receipt race20leaf; deterministic concurrent
plan1/worker2/synthesis1 replay preserved eight attempt IDs and leaf cost3.
Independent core11 and host19 leaf checks each passed normal and race runs.
The root independently reran child10 leaf race checks. After the initial full
check and further boundary fixes, final check_local passed Python412, Go
complexity/vet/full race and UI532 on all21 unchanged final Go source pins.
Pinned cache-only lint reported zero issues. Earlier passes remain separate
from this final-source verification.

Independent review found missing failed-call terminal records, standalone
fallback linkage, foreign resolved-definition scope, local-boundary ancestry,
child context rebinding and clock provenance errors. Reproductions and source
revision relations are retained privately. Shared file handoffs and the final
combined diff are reviewed by a different author.

Local card acceptance is complete for the fake instrumentation scope. Vendor
internal loops, opaque backends and unobserved usage cannot claim exhaustive
billing. No real model/provider/effect/operational DB call, product build,
release, deployment or personal-memory outcome publication was performed.
R8 standalone acceptance remains unrun.

## 한국어 작업 기록

작업 ID: CRW-02 (#154)
변경 이유: 기존 호출·재시도·회의·host 사용량 경로를 재사용해 실패 비용과 누락 상태를 보존한다.
실행한 검증: root race19methods/22leaf, 독립 core11/host19 일반·race, 회의20/하위10 race, 협업 일정2회 ID·비용 동일. 최종 고정21소스 전체 check_local 종료0(Python412/Go/UI532), 고정 lint0.
현재 상태: 후속 독립 검토의 버전·노드·nullable 결과 revision 경계 보정과 최종 전체 검사를 마쳐 로컬 fake 관측 카드 범위만 수용했다.
미실행: 실제 provider 청구·전체 실행 누락 증명·모델 성능·제품 빌드·배포·개인 기억 outcome 발행·R8 standalone 관문.
한계: 원 host 경계와 controller의 겹치는 인과 경계를 구분하며, 보고서는 동봉한 snapshot에만 해당한다. run wall 시간은 자동 수집하지 않아 caller-supplied/unknown이고 호출 시간 합과 구별한다. caller hash는 인증이 아니다.
