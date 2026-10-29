# Implement scalar transition and revision isolation

Task: DYN-02.scalar (#152)
Research: P1 engineering foundation; no effectiveness study
Design: Reuse R4 decay and accepted DYN01 types/canonical validators. Return state, receipt, index and cursor together as pure owned values, with no DB commit.
Relation: adapted
Deviation: UnitStrength times event strength and the explicit R4 tau adapter are engineering choices. Fast/slow remains conditional and unsupported.

ApplyEvent and ResetRevision now preserve original-receipt retries, new-ID duplicate-evidence audit, qualified declared evidence consumption, decay-only unknown outcomes, exposure without scalar writes, revision isolation and lifecycle invalidation. Exact integer time differences precede floating conversion. Inputs and outputs own mutable fields independently.

The complete receipt prefix must follow the fixed scalar matrix for every full key: one initial activation, continuous before/after revisions, nonlearning revision preservation and no applied resurrection after invalidation. Supplied current state binds latest revision/reason/last-scalar coordinates and the original activation. Pending values match the explicit config before historical retry and same/new-ref reset. Active reasons come from the latest applied receipt; invalidated cause comes from the exact fence entry, while receipt.reason=invalidated remains a disposition marker.

Verified: final author32methods/74nodes/62leaf normal and race; fresh independent31methods/74nodes/65leaf normal and race, all0fail/skip. Independent literal canonical bytes/hashes, high-precision numeric oracles,1ns integer-clock case and two-way mutation checks cover the original criteria. Vet, complexity with no new >20 debt and cached pinned golangci-lint v2.14.0 report0issues. Exact final check_local25781 passed Python412/Go complexity-vet-fullrace/UI532. Original baseline R4 and accepted types/validators were unchanged by these four owned files.

Scalar semantics SHAfd81476b and transport91becd match the source artifact constant/regression. Final source pins are transition117494a4/test25d5ed6a/revision657e9815/test0da4b83b. Full names/counts/logs and byte archives are retained in the private evidence record; no private raw source reports or primary fulltexts are published here.

Independent failures I01 through I05 were corrected before acceptance: claim-conflict priority, current reason mismatch, pending initial mismatch on historical retry/reset, receipt revision gaps and repeated activation/anchor replacement. Source bytes and raw RED logs precede every fix. Two independent positive-fixture identity mistakes, author fixture preparation mistakes, root count-script assumption, metadata-pin refresh and a temporary complexity21 failure are recorded separately. Additional four whole-class mutations passed from their first run and are not labeled RED.

Earlier whole checks14354 and83365 passed their older sources while independent boundary failures remained; they were never final functional acceptance. The final25781 run and final independent65leaf bind the final source. Completed-agent message delivery also once failed to restart a followup; live status/source pins exposed it and an explicit restart corrected it. No work was claimed complete from that idle message alone.

Limits: accept DYN-02.scalar only. Whole DYN-02 stays partially_accepted/in_progress; optional fast/slow awaits its pilot gate and has no successful stub. Declared synthetic independence does not authenticate real task/run/trial relationships or actors. Internal structural coherence cannot reconstruct all prior numeric outcomes without original events; that is DYN03. The R4 half-life adapter is not generally bit-identical to directly rounded tau equations at zero tolerance; the observed numerical limit and explicit test tolerances were retained without loosening them. Pure value composition does not prove DB/provider atomicity, operational restore authorization or scientific efficacy. No model/provider/human/operational DB/product build/deploy/Claude edits. Whole-card count remains19/63; DYN03.scalar prerequisite is now met.

## 한국어 작업 기록

작업 ID: DYN-02.scalar (#152)
이유: 기존 감쇠·자료형을 재사용해 독립 근거의 한 번 반영, 중복/revision 격리와 상태·처리 이력 대조를 구현한다.
검증: 작성자32방법62세부·독립31방법65세부 일반/race, vet·복잡도·고정lint 및 최종전체Python412/Go/UI532 통과. I01~I05의 실제RED·원소스·준비오류·수치 한계를 구분 보존했다.
한계: scalar 하위 단계만 수용, fast/slow·모델효능·실제 권한·전체수치재생·DB원자성·제품빌드/배포는 미검증. 전체19/63 유지, DYN03.scalar 선행은 충족했다.
