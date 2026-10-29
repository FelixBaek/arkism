# Validate the separate scalar dynamics contract

Task: DYN-01 (#152)
Research: P1 engineering foundation, no effectiveness result
Design: Preserve immutable baseline Scope/Ref/codec/B0 and define accepted-time scalar events, state lifetime, sidecar availability and fixed policy interfaces before implementing transitions.
Relation: adapted
Deviation: Fixed rank fusion and explicit regression parameters are engineering choices. No biological simulator or production outcome/authentication adapter is introduced.

The six owned implementation files contain the reviewed machine contract,
engineering decisions, P1 provenance contract and Go types/strict validators/tests.
There are28primary DTOs,20auxiliary records,23closed hash projections and the
existing budget alias. Registry associations use explicit acyclic projections;
missing clocks never become fabricated activation times. Original Scope/Ref and
all19 native error codes retain their exact meanings. Input/output roles and
completion manifests are distinct, and a manifest never references itself.

Same-ID retries preserve original accepted coordinates; new logical events with
already-consumed evidence have an explicit non-learning history. Unknown and
unattributable outcomes do not consume reinforcement evidence. A qualified
NOOUT outcome retains eligibility even when numeric jump is disabled. Ablations
bind the actual selected B2 ranker parameter hash and compare both configurations.
These are verified metadata rules, not executed transition or policy algorithms.

Verified: author20methods/168leaf normal and race, existing baseline299leaf;
independent16methods/163leaf normal and race; vet0, no new complexity debt,
pinned golangci-lint v2.14.0 actual0issues. NEW-worktree check_local passed
Python412/Go complexity-vet-fullrace/UI532 on exact code67e3 with the same
20tests before their function-prefix rename. A separate byte/AST comparison
proved name-only changes, and the exact original TestDynamicsContract regex
subsequently collected20methods/168leaf in normal/race with0fail/skip/duplicates.
The initial zero-collection command is retained as notPASS. Independent named
command followup81f18a86 confirms the new test5876 and unchanged code/types/docs.

Initial actual metadata-rule, mode and error-path failures and later RuleSHA
crossbinding REDs retain source bytes/logs before correction. Compilation or
fixture preparation mistakes are separate from functional defects. Independent
static340/public-projection64/followup36 checks precede the Go implementation
and are not counted as runtime tests. Final source pins: types0908b04e,
validator67e3e19d, tests5876c3c8; main contract952d663b, P1b6e965bb.

Limits: DYN-01 accepts contracts/types/validators only. ApplyEvent/ResetRevision,
ReplayPrefix/ReadAt, ranking/runner/CLI bodies are absent, with no successful
placeholder. Synthetic fixture metadata does not verify live DB/actor authority,
actual evaluation independence or causal truth. Existing product outcome/trace
and SYS-02 boundaries remain mandatory. No actual policy experiment, model,
provider, human, operational DB, product build/deploy or Claude change ran.
Local verified roadmap count18/63 is separate from product or study completion.

## 한국어 작업 기록

작업 ID: DYN-01 (#152)
이유: 사건의 관측·도착·수락 시각, 정확 revision과 상태 수명, 중요도 출처를 별도 계약과 Go 자료형으로 고정한다.
검증: 소유20방법/168세부·독립16방법/163세부 일반/race, 기존 baseline299세부, vet·복잡도·고정lint 및 전체Python412/Go/UI532 통과. 원정규식도20방법을 실제수집했으며 이름변경 외 의미는 동일하다. 실제RED·이전소스·정적검사·미실행을 분리 보존한다.
한계: 실제상태전이·재생·정책·CLI·모델·효능·운영권한·제품빌드/배포·Claude 변경은 미실행. 합성 metadata는 실제 evaluator/권한 증명이 아니며 검증 완료18/63이다.
