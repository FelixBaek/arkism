# Optional attempt observation helpers

Task: CRW-02 (#154)
Research: E development foundation; no measured effectiveness result
Design: Frozen CRW-01 accounting sidecars and existing native FLOW accounting
Relation: adapted
Deviation: A separately reviewed helper handoff precedes serialized integration. The full card remains in progress until its actual call paths and acceptance criteria are verified.

The current engine already reserves and settles durable FLOW attempts. The
observer must reuse that accounting and distinguish an exact canonical record
reference from an observation identity. Existing final-node traces are bounded
projections, not complete attempt ledgers. Unknown usage cannot establish a
zero cost or an equal-cost comparison.

The ownership plan adds `CRW-02.modules`, an intermediate helper-review and
handoff gate. The original instruction to integrate after CRW-02 is clarified
so full-card acceptance cannot depend on integration that waits for that same
acceptance. Shared FLOW, meeting and host producer/test paths are registered
under the single INTEGRATION writer. Worker ownership is unchanged. The
accepted private CRW-01 contract artifacts retain their bytes; a later private
dispatch overlay records this ownership revision.

The helper implementation provides presence-aware input/output/cache/reasoning
usage, stable identities, typed canonical hashes, exact replay deduplication and
separate per-call totals. Unknown values remain null; estimates and reported
components are not added twice. Empty collection is incomplete rather than
complete zero. Confirmed dispatch and canonical reservation identity cannot
be reversed by a later observer revision, and prior reported accounting cannot
be silently lowered or forgotten. Estimate-to-reported correction remains valid.

The owner race suite passed96 leaf tests; independent observer30 and host
normalizer54 tests passed (84 total in both normal and race runs). Actual failing-before probes caught empty-zero
completeness, contradictory dispatch states, lost paid history and canonical
reservation replacement. Positive reported amounts on an undispatched record
are rejected while observed pre-dispatch zero and estimated reservation values
remain valid. Independent race passed the same84 tests. The final frozen Go source also
passed check_local: Python412, Go complexity/vet/full race and UI532; cached
pinned golangci-lint2.14.0 reported0 issues. This validates helper code and
existing regressions, not observer-on/off business wiring.

Only actual crew_runs are representable by this v1 helper. Standalone meeting
and assist refs require the separately registered v2 gate. No shared caller is
wired yet, and host internal attempts have no accepted transport integration.
The optional sink is synchronous and must be short and bounded; it does not
promise latency independence for arbitrary I/O. Business-path integration
will use memory-only collection with separately reviewed delivery policy.
Integrated calls, full-card acceptance and remote checks remain pending.
No product build, release, deployment or actual provider/effect call is granted
by the ownership update. The fixed baseline contract is unchanged.

## 한국어 작업 기록

작업 ID: CRW-02 (#154)
변경 이유: 기존 FLOW 회계를 재사용하고 관측 helper 인계와 공유 호출부 통합의 순환 조건을 분리한다.
실행한 검증: 소유권 검증기·회귀43개, 소유 race96leaf, 독립 observer30+normalizer54(총84개)의 일반·race 검사 통과. 최종 check_local 종료0(Python412/Go 복잡도·vet·전체race/UI532), 고정 lint0issues.
미실행: 공유 호출부 통합 수용, 원격 CI, 실제 모델·효과 호출, 빌드·배포.
현재 한계: 이 기록은 진행 중이며 전체 카드 완료를 뜻하지 않는다. 실제 후속 검증 결과로 갱신한 뒤 단계 커밋한다.
커밋 상태: 로컬 최종 검증을 마친 helper 구현·기록을 단계 PR로 공개한다. 원격 검사와 전체 카드 수용은 별도다.
