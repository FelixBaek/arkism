# RTE-06 attribution of independent results to the original routing event

Task: RTE-06 (#155)
Research: Offline routing-evaluation attribution over synthetic fixtures; no learning, quality or effectiveness result
Design: A pure function in the private routing lab reuses the frozen RTE-01 validator (Outcome schema, record hash, bundle joins, Ref and personal receipt join). It keeps a ledger with a learning journal, quarantine, corrections and duplicates, bound to the base record hash. Crash resume replays the whole arrival log and must rebuild the persisted ledger exactly.
Relation: adapted
Deviation: The card's research/routing path maps to the private routing-lab folder, like RTE-01 and RTE-02. Crew task outcomes are covered by test fixtures, not a separate file. No runtime collector, API or product change.
Why: Independent results must reach the decision and attempt that produced them exactly once, and personal receipts, crew traces, self-ratings, rejected receipts, unrun attempts and late corrections must not become routing labels.
Verified: Each card criterion has a test: exact Ref and revision checks stay with the frozen validator; personal receipts stay excluded (OI-25); crew outcomes stay quarantined while the team contract is unaccepted; a repeated update applies once and every resume point gives the same state hash; another attempt of the same task follows the fixed first-label rule; pre-correction labels, post-label corrections and rejected receipts are all kept. Three independent reviews: the first rejected with seven reproduced defects, the second confirmed them fixed and found one more (malformed trace references), the third accepted that fix after 30 malformed references and no regression. 18 tests passed; nine mutations, one per fix, each failed a test against a passing control.
Limits: Structural joins over supplied records; no authenticated receipt, current native state or checker execution. Resume detects a ledger that disagrees with the supplied log, not a log and ledger replaced together. A correction that arrives before its target is lost. Crew online learning stays gated off. The research folder is private and not part of this repository.

## 한국어 작업 기록

제목: RTE-06 독립 결과를 원래 라우팅 사건에 귀속
이유: 독립 평가 결과는 그것을 만든 결정·시도에 정확히 한 번만 연결돼야 한다. 개인 receipt, crew 추적, 자기평가, 거부된 receipt, 실행 안 된 시도, 늦은 정정은 라우팅 라벨이 되면 안 된다.
검증: 완료 기준 6개마다 시험이 있다. 독립 검토 3회: 1차 결함 7건으로 반려, 2차 수정 확인과 새 결함 1건, 3차 그 수정 수용. 시험 18건 성공, 수정별 변이 9개 모두 잡힘(대조군 정상).
한계: 주어진 기록의 구조 결합만 본다. receipt 인증·현재 상태·checker 실행은 없다. 기록과 장부를 함께 바꾸면 못 잡는다. 대상보다 먼저 온 정정은 잃는다. crew 온라인 학습은 계속 차단한다. 연구 폴더는 비공개라 이 저장소에 없다.
