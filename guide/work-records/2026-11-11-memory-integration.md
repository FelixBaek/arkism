# Memory and skill integration / 기억·스킬 통합

Task: P21–P33 integration, PR551 follow-through.
Research: none; integration of existing implementation and review corrections.
Why: preserve the completed local work and finish its missing review, verification
and reporting. The reviewed public base is `128beac1`; the interrupted local tip
is `03e94757`, containing61 commits beyond that base. PR #551 contains the first14
of those commits. Original implementation branches remain available locally.

## Scope and expected improvements / 기능과 기대 효과

| Area / 분야 | Behavior / 변경 | Expected benefit / 기대 효과 | Qualification / 한계 |
| --- | --- | --- | --- |
| Idle memory upkeep / 기억 정리 | Logged, undoable dream runs can merge paraphrases, handle contradictions, derive insights and propose or promote skills; reports exclude source text. | Reduce duplicate or contradictory memories and expose what automatic upkeep changed. | Automatic mode uses configured model decisions and policy gates; this is not a measured accuracy claim. |
| Skill selection and lifecycle / 스킬 선택·관리 | Semantic ordering, independent matching signals, explicit no-match behavior, generated-skill evidence reads, revision refusal, per-run outcome handling and undoable demotion. | Avoid unrelated skill application and keep poor revisions from replacing usable instructions. | Model-backed selection remains dependent on configured model quality; sparse-use skills have insufficient outcome evidence. |
| Recall / 기억 찾기 | Explicit aggregate-item widening, list/count handling, metadata filters, Korean query variants, English plural concepts, bounded vector distance and batched reranking. | Recover evidence for questions spanning several turns while retaining explicit request and filtering semantics. | Different query categories still have different quality; no universal90% claim. |
| Answers / 기억 기반 답변 | Subject/recorder views, record/event dates, twenty-record default, optional conversation rereading and matched raw passages. | Preserve details that extraction omitted and make temporal reasoning use the record's date. | Rereading and passages remain opt-in; passages can add embedding work and prompt size. |
| Runtime/API / 실행·오류 처리 | Slow model operations can return202 with a readable operation; field-specific errors, empty-answer errors, provider probes and bounded reread deadlines. | Make failures actionable and prevent long-running operations from appearing to hang. | Probe success is not a semantic quality test; asynchronous consumers must read operation state. |
| Capture, import and clients / 저장·연동 | Import metadata/time preservation, archive word search, capture/review/deletion repairs, file extraction, skill sync explanations and generated API/SDK contracts. | Preserve provenance across import and clarify permission/configuration failures. | Existing permission boundaries and material-insufficient research holds remain. |

The inherited feature details and settings are documented in `guide/server.md`,
the generated API contract and the earlier work records. This record groups the
integration behavior; it does not reclassify unexecuted checks as passing.

The follow-through independently reproduced and repaired source transfer/deletion
failures, three pending review findings and the live-lease crash-test race.
See [passage completion and evaluation audit](2026-11-11-claude-completion.md) and
[crash-probe validation](2026-11-11-dynamics-crash-probe-handshake.md) for commands,
results, failed attempts and remaining limits.

한국어: 기존 스킬·기억 정리 PR의14커밋과 그 뒤47커밋을 통합 대상으로 대조했다.
기대 효과는 중복 감소, 스킬 오적용 억제, 원문 세부 회수, 오류 원인 안내다.
실제 모든 분야90% 달성을 뜻하지 않는다. 원본 작업 이력과 평가 원본은 보존하고,
공개용 결과에는 개인 식별 테스트 문구나 비공개 평가 자료를 추가하지 않는다.

## Verification boundary / 검증 범위

Focused source-safety, real PostgreSQL, search/dream, configuration and crash
checks are recorded in the linked follow-through records. OpenAPI structural
validation passed437 operations; this is zero live HTTP requests. Complexity
validation passed with three explicitly retained legacy functions over the limit.
No new production build, deployment or model inference is included in these local
checks. Historical PR #551 still needs a new CI result after its fixes; an old
successful job does not validate this integrated tree.
