# Preserve decision-time metadata and bounded feature-file admission

Task: RTE-03 (#155)
Research: Isolated routing feature preparation; no deployed metadata reader or routing experiment
Design: Freeze one common preselection candidate view; bind exact scope/ref/revision/content hash and reader/fence receipt availability. Audit selected-model presentation separately without feeding packing or future outcomes into preselection features.
Relation: adapted
Deviation: Separate proposed fixture protocol; existing RTE-01 contracts and shared host/Gateway routing APIs are unchanged. Actual SYS reader/fence and host/API integration remain required.

Why: Implement deterministic decision-time metadata features while preserving unknown versus known-empty history and explicit synthetic provenance. Independent review found that a FIFO input could block before the ordinary-file check. Open nonblocking, then retain no-follow, file-type and size checks before reading.
Verified: Author52 methods/89 leaf passed, preserving the original44 methods and adding8 admission regressions. The original FIFO failure, source bytes and terminated-process evidence were preserved before the fix. The author reran13 schema documents and reproduced15 generated data files byte-for-byte. Four of55 original pins changed intentionally; the other51 remained unchanged. Fresh independent follow-up37 methods/99 leaf (original28/87, new admission8/11 and original FIFO regression1) and author52/89 replay passed without failures/errors/skips. Nine additional actual CLI processes terminated and were reaped; the no-writer FIFO returned the fixed input_type rejection. Original44 method source,19 earlier evidence hashes and the original report remained unchanged. Root verified all55 current source pins and25 independent artifacts. Isolated engineering substage is accepted.
Limits: Synthetic receipt hashes do not establish current authorization, source truth or an operational reader. Token estimates are not tokenizer measurements. Actual crew ordering/API integration and SYS01/03/04 owner proofs are not implemented. No product UI, Gateway, database, model/provider, build, deployment or Claude changes. Rejecting a FIFO does not establish a deadline for arbitrary remote regular-file I/O. Whole RTE-03 remains in_progress;21/63 is unchanged.

## 한국어 작업 기록

이유: 선택 전에 알 수 있었던 메타데이터만 동결하고, 특수 파일 입력에서 CLI가 멈추는 실제 결함을 고쳤다.
검증: 작성자52메서드·89세부 검사 통과. 기존44개 검사를 유지하고8개를 추가했다. 원 실패와 소스를 보존했다. 독립37메서드·99세부 검사와 작성자 재생도 통과했다. 현재55핀과 독립 증거25파일을 확인해 격리 공학 구현만 수용했다.
한계: 실제 reader/fence·호스트/API 연결은 미구현이다. 합성 해시로 권한이나 실제 최신성을 증명하지 않는다. 전체 카드 완료로 세지 않는다.

Public-record validation: check_local55678 exit0, Python567 (173 tools,345 evaluation,49 integrations), Go complexity/vet/full race and UI532 passed. This clean66f parent plus records excludes the unfinished DYN05 worktree and its known numerical-rule failure. Publication audit passed with only the previously authorized fixed-contract exception. Initial sandbox socket errors were preserved and the identical suite reran with test-local sockets allowed.
공개기록검증: 전체55678 종료0(Python567·Go복잡도/vet/race·UI532), 공개검사 통과. 별도DYN05 미완료/1FAIL과 구분한다. 최초 샌드박스 소켓 차단 로그는 보존했다.
