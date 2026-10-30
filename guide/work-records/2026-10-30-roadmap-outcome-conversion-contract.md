# Define independent outcome conversion and preserve nonlearning observations

Task: SYS-02.spec; R8 (#152)
Research: Offline structural acceptance/conversion specification and synthetic fixtures only
Design: Separate native accepted-observation metadata plans from synthetic DYN events; distinguish server return, delivery, actual use and task success. Bind exact references, checker metadata, original acceptance clocks, transport identity and logical evidence consumption independently.
Relation: adapted
Deviation: The immutable offline DYN qualification namespace remains synthetic. Native metadata cannot authenticate acceptance or produce a native learning event; actual service resolver and durable SYS03 handoff remain required.

Why: Reuse current service acceptance and SYS01/DYN types without treating caller assertions, missing results or observed failure as authorization or causal memory blame. Independent verification found that future evidence availability was wrongly rejected even though the fixed DYN contract permits the event as nonlearning. Retain the event and apply availability only to qualification.
Verified: Author38 methods/157 leaf and63 original fixtures passed. Fresh independent65 literal cases plus16 methods/40 leaf passed, including the original actual failure and a three-event sequence: unavailable evidence consumes zero, later qualification consumes once, subsequent duplicate consumes zero. The original35 test methods,63 case inputs/expectations and earlier literal values were preserved; only3 new literal fields were added. Root verified9 source/dependency pins and207 independent artifacts. Default collection361 methods/815 leaf includes SYS02 exactly38 times and all original323 IDs once. The unchanged collector logic passed separate missing/symlink/empty/import/skip/expected-failure controls (8 negative,1 positive). Full check36655 exit0: Python605 (173 tools,383 evaluation,49 integrations), Go complexity/vet/race and UI532.
Limits: A synthetic specification and collection stage only; whole SYS02 remains in_progress and whole-card count21/63 is unchanged. SyntheticIndex is not durable state or a native authentication service. A returned historical retry qualification does not demonstrate another state update; the observed index is unchanged. Actual native acceptedRef/Go adapter, qualification/event mapping, HTTP/isolated-PG acceptance and atomic durable consumption remain downstream. Host checker-byte verification differs from server hash-format validation. No product API/UI, operational database/model/checker, recruitment, build/deployment or Claude changes. A separate DYN05 worktree is outside this passing whole-check scope.

## 한국어 작업 기록

이유: 실제 수락·미관측·실패·근거 중복을 구분하는 계약을 구현하고, 유효하지만 학습 자격이 없는 사건을 잘못 거절하던 결함을 수정했다.
검증: 작성38메서드·157세부 검사, 독립65사례·16메서드40세부, 기본 수집361메서드815세부 통과. 전체36655 종료0(Python605·Go·UI532). 기존35검사·63사례의 기대값은 보존했고 현재9핀과 독립 증거207파일을 확인했다.
한계: A 계약·합성 검사만 수용했다. 실제 서비스 연결·native 사건·영속 저장은 미구현이며 전체 카드를 닫지 않는다. 재시도의 원 자격 반환을 실제 중복 점프로 과장하지 않는다.
