# Preserve the Search complexity budget

Task: SYS-01.integration (#152)
Research: Engineering refactor; no policy or measured utility change
Design: Move the existing optional final-rejection observer assignment into a nil-safe method. Keep the condition and assignment identical and retain every arithmetic, packing and transaction operation.
Relation: adapted
Deviation: No source contract, observer meaning, complexity baseline or checker changes. The original frozen source and failed CI evidence are preserved.
Why: The new observer branch raised searchWithActor from20 to21 and failed the existing complexity gate.
Verified: Reproduced the actual unchanged CI inventory/baseline failure21; the extracted method has complexity2 and searchWithActor returns to20 with no violations. Existing meaningful context-view/stage regressions15methods23leaf passed in normal and race without failure/skip. The production-view followup separately passed its actual143/335 normal corpus and strict all-module lint with these exact two files; its final race remains separately tracked.
Limits: This refactor does not complete the production reader integration or whole SYS01. Existing default, API and baseline behavior are unchanged. No product build, deploy, main merge, UI change or full check_local run.

제목: 검색 함수의 기존 복잡도 한도 유지
이유: 추가된 관측 조건문으로 한도를1넘어 같은 조건·대입을 작은 함수로 분리했다.
검증: 실제 CI 기준21실패를 재현한 뒤 검색20·추출함수2로 통과했다. 기존 의미있는 일반/race15메서드23세부사례도 실패·skip없이 통과했다.
한계: 관측·수학·기본검색 동작은 유지하며 전체 카드나 실제 독자 연결 수용을 대신하지 않는다. 빌드·배포·원격main 병합 없음.
