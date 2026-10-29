# Add lifecycle dependency and restore metadata models

Task: SYS-04.spec; R8 (#152)
Research: Synthetic lifecycle/current-Eval metadata model, not actual DB erasure or restoration
Design: Preserve legacy-v1 behavior and add a separate closed seven-collection metadata-v2 profile with exact scope/ref/revision/hash, required AND edges, extraction/coverage bindings and unchanged six-field restore reconciliation. Register the exact module once in the default collector.
Relation: adapted
Deviation: Current Eval task/run/trial/calibration roots need their own profile; source liveness or calibration never establishes native learning qualification. The fixed contract's design-time labels are historical declarations, not current execution status; verification is recorded separately here.
Why: Detect incomplete source closure, stale/foreign metadata, invalid restoration and false deletion-completion claims while reusing the accepted dependency model and existing product semantics.
Verified: Author59 methods515leaf and fresh independent19/281 passed without skip. Original16/171 expectations were unchanged; eight Unicode/129–256-byte/Cf deletion-ID counterexamples were resolved by using the existing v2 Ref-ID validator at exactly two supported deletion paths. JSON contract/fixture3 bytes and all54 prior fixture plan outputs are identical; legacy strict labels and unsupported metadata fact-withdrawal/history remain unchanged. Signed64 clocks/known0 and positive counters retain prior meaning. Actual pinned Go scope/ref predicates119 input cases matched the Python literals in an isolated author overlay; this is separate from the default Python collection. Root reverified source5 and independent28 evidence, then exact R8 source6 and31 evidence. R8 collection451 old IDs plus59 new exactly once=510 methods1483leaf; existing8 negative+1 valid controls and2 external-inventory negative controls passed. Current root Python758(173 tools+536 evaluation+49 reuse), metadata63cards96gates passed.
Limits: A synthetic-model scope only. The collector's permanent guard rejects missing/empty/skip/symlink/import failures, while duplicate-tuple/partial-method mutants are detected by this stage's external full-ID inventory; no permanent expected-ID contract was added. Actual source DB/backup/restore/publication B and upstream SYS01/SYS03 integration acceptance remain separate. Current Go/UI/runtime configuration sources are unchanged from the native parent, so its relevant validation is reused rather than claiming a new full check_local. Original signed-clock errors, eight metadata-ID failures and cwd/reviewer preparation errors remain preserved. Whole22/63 and required27 remaining are unchanged; no product build, deploy, main or Claude changes.

## 한국어 작업 기록

제목: 현재 평가 참조를 포함한 수명·복원 모델과 기본 검사 등록
이유: 누락된 출처·오래된 참조·불완전 복원·삭제 완료 오해를 탐지하도록 원 모델을 별도 metadata 프로필로 보완한다.
검증: 작성자59/515·독립19/281 통과, 원8반례 기대변경 없이 해소. 기존54개 출력과3JSON 원문을 보존했다. R8기존451개+새59개를 정확1회 수집했고11개 방어 대조를 통과했다. root Python758·메타데이터63/96 통과.
한계: 모델과 합성 입력의 검증이며 실제DB 삭제·복원·게시 수용이 아니다. Go/UI소스는 부모와 같고 새전체check_local을 실행했다고 표시하지 않는다. 전체22/63·필수27개 미완료 유지, 빌드·배포·원격main·Claude 변경 없음.
