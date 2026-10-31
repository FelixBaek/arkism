# CAP-03/04/05 replay on the real server paths

Task: CAP-03, CAP-04, CAP-05 (#156)
Research: capability engineering replay; development inputs only, no efficacy claim
Design: The isolated capability lab replayed client skill loading against a loopback fake hub and left four server paths as unknown stubs: ResolveSkills/resolveSkillRef, checkSkillUsageEvents, resolveSkillTrial and the server half of the crew trial checker (ObserveSkillTrial, RecordSkillTrialOutcome). `internal/memory/capability_replay_test.go` calls the real resolution and usage paths on the fake repository and on a disposable PostgreSQL; its original trial-checker test uses the fake repository only. It feeds the server-path answers to the five CAP-04 lab adapters that only read hub answers (current, current_pin_save_failed, always_fresh, pinned_available, all_change_stop). An unreachable repository stands for the lab's unknown condition. The CAP-05 chain is input -> checkpoint (the view resolved before the change) -> decision (adapter) -> effect (skill.usage accepted by the server).
Relation: adapted
Deviation: One representative input per condition instead of the lab's 12 per condition; compatible and incompatible are modelled as a new approved SKILL.md version, because the server holds no separate meaning or schema digest. selective_revalidation and the CAP-05 shadow are not replayed: they need premises the server does not return.

## Result

- CAP-03 stubs, now real: resolve answers typed per-ref errors and refuses an other-scope reader; an exact pin keeps serving the superseded version while latest-approved moves (pin_save_ok / pin_save_failure server half); usage is bound to a served version and hash (the fake accepted any body) while the run status stays caller-asserted; an unknown trial id is refused (the fake echoed it as active); only a separate checker credential may record trial outcomes.
- CAP-04: 20 cells (5 adapters x unchanged/compatible/incompatible/unknown) from real server answers equal the lab's `cap_04/out/a/results.jsonl` actions.
- CAP-05 no-change: on the incompatible input both the pinned old view and the followed new view are served and their usage is accepted; apart from identity fields the server answer for a compatible and an incompatible change is identical, so the server cannot feed a compatibility verdict. No product change follows from the report.

## Independent validation

The original regression contains three top-level tests and four subtests. The server-path and comparison tests each run against fake and PostgreSQL repositories; the trial-checker test runs against the fake repository. A separate disposable PostgreSQL review used genuine restricted LOGIN and real authorization for skill pin/latest resolution, scope/token denial, usage identity binding, permission revocation, trial-checker separation and replay conflicts. Original tests plus two independent LOGIN tests passed normal and race: five top-level tests, four subtests, zero failures or skips. These overlapping checks are not added together. The independent tests are private review evidence and do not change product behavior or the checked-in regression scope. No provider call or product build was performed.

## Left for an owner decision

- Whether `current` should stop or warn on an incompatible change (12/48 effects in the lab); this needs a declared meaning/schema premise on skill versions, which is new user-visible behaviour.
- Whether the no-change report should be shown anywhere. No existing read-only surface carries research reports, so none was added and no flag exists.
- A pin save failure is still silent on the client (CAP-03 residual).

## 한국어 기록

제목: CAP-03/04/05를 실제 서버 경로로 재생
이유: 격리 실험은 서버 4경로를 stub(unknown)으로 두었다. 실제 함수로 바꿔야 재생·비교·no-change 보고가 제품 코드 기준이 된다.
검증: 새 시험 3개(하위 4) 통과: server paths·비교 시험 2개는 fake·PostgreSQL, trial checker 시험 1개는 fake, CAP-04 20칸이 실험 결과와 일치, 변이 4/4 검출. 제품 코드 변경 없음.
한계: 조건마다 대표 입력 1개, selective·shadow는 서버에 의미·스키마 근거가 없어 재생하지 않았다. 표시 여부·비호환 처리는 소유자 결정으로 남긴다.
