# Add versioned context receipts and current-use decisions

Task: CRW-03.modules (#154)
Research: E engineering adaptation; no effectiveness or novelty result
Design: Accepted crew v2 source/context projections, existing strict codec and optional reader capability
Relation: adapted
Deviation: Pure receipt/decision module and private synthetic fixtures only. Existing Client/meeting hooks require the subsequent integration handoff; this is not full CRW-03 acceptance.

The module adds the exact fourteen-field SourceObservation and fourteen-field
ContextObservation projections, strict decoding and typed canonical hashes.
Source bytes before clipping, rendered context, original-source revisions,
meeting snapshots and summary revisions remain distinct. It reuses actual v2
meeting/run/task identities and never substitutes a turn sequence for a CAS.

An optional reader capability leaves existing stores unchanged. Typed local
reader facts bind current source scope/ref/digest, lifecycle, live team policy
and sharing before the pure evaluator selects continue, refresh, restart or
block. Historical unknown receipts cannot become protected evidence merely
because their claimed text matches a current read. An unverified natural
language correction blocks current use; supported typed refresh/restart returns
a required plan, not completed execution or permission to reuse an old summary.

Modes off, observe and enforce_current_use separate evidence collection from
authority decisions. Capture requires an explicitly enabled attempt observer
and actual execution context; it does not enable one implicitly. Protected
historical lineage must match the execution, task, team and exact prior source.
Future summary CAS values cannot bind to an earlier observed meeting snapshot.
Collector sink errors, panics and pending delivery remain diagnostics and do
not change the authority decision or claim whole-run completeness.

Independent normal and race runs each passed169 leaf checks, including137
codec payloads and16 independent canonical/hash comparisons. Thirteen error
name aliases retain existing Go taxonomy; accept/reject and hash outcomes
match the frozen registry. Author normal/race runs each passed85 leaf checks.
Actual negative cases for unverified premises, historical unknown promotion
and future summary CAS were reproduced, fixed and rerun.

Private fixtures contain32 handwritten decisions, four canonical lineage
stages and a compatibility/deletion matrix. Python12methods/81leaf and the
fixture-to-Go race34leaf checks passed. Root independently reran Python tests,
checked all12 artifact hashes, fourteen-field DTOs, rendered hashes and parent
lineage. A native-handler preflight used local fake records to confirm exact
memory metadata, history GET200, source quarantine refusal and unsupported
generic knowledge-document reads. This is not live PostgreSQL verification.

Final check_local passed Python412, Go complexity/vet/full race and UI532.
Pinned cached lint reported zero issues. Final module source pins are recorded
with the private acceptance evidence; raw research artifacts remain private.

The adaptation follows the distinction between retrieval and later admission
in [TRACE](https://arxiv.org/abs/2609.33517), and explicit premise revalidation
in [ATR](https://arxiv.org/abs/2609.08015). It does not reproduce their complete
systems or provide an atomic target commit. Return-time admission alone is
not claimed as independent research novelty.

Limits: actual Client/meeting gates and their end-to-end regressions remain
pending. GET and later model dispatch are not atomic. Knowledge-document
metadata cannot substitute for an exact body. Unknown availability remains
null. No provider/model/effect/operational DB call, product build, release,
deployment, personal-memory outcome or R8 standalone acceptance was run.

## 한국어 작업 기록

작업 ID: CRW-03.modules (#154)
변경 이유: 과거 자료·요약과 현재 사용 가능 근거를 구분하는 선택적 receipt와 순수 판단 API를 만든다.
실행한 검증: 소유 일반/race85leaf, 독립 일반/race169leaf(137codec·16hash), fixture Python12methods/81leaf·Go race34leaf. Root artifact12hash·계보4단계·native fake handler4검사. 최종 전체check_local 종료0(Python412/Go/UI532), 고정lint0.
현재 상태: 보조 모듈·fixture 인계 관문만 수용한다. 전체 CRW-03과 실제 Client/회의 gate는 미수용이며 전체 완료12/63을 유지한다.
한계: read와 use의 원자성·완전한 TRACE/ATR·실제 refresh 실행·권한 인증·실제 모델/DB·제품 빌드·배포·R8 standalone 관문은 검증하지 않았다.
