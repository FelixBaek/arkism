# Local comparison fixture specification

Task: CRW-05.spec (#154)
Research: E engineering foundation; no method performance or statistical result
Design: Frozen CRW-01 sidecars, literal synthetic oracles and separated evaluator inputs
Relation: adapted
Deviation: Only the fixture/spec substage is accepted. Track-biased engineering splits exercise leakage checks and do not establish a research holdout. Runtime adapters await CRW-02/03/04.

The private comparison specification registers E-C0 through E-C4 and gives each
arm the same selected task, local tools, memory view and aggregate budget. The
single-agent arms include self-review and retry. Provider/model selection is
unbound; equal-token and equal-USD research comparisons remain unexecuted.
E-C4 is explicitly unsupported/not_run until a cited method and its source,
license and adaptation are selected. No executable method adapter is present.

The evaluator checks literal file bytes, final state, exact synthetic effect
receipts, source versions and intentional stops. It ignores a method's done
claim as proof of success. Failure, infrastructure error, evaluator error,
unknown and not_run retain distinct denominators. Known failed-call costs are
preserved; incomplete cost stays null while retaining its known lower bound.
Receipt/observation replay does not create a second charge.

There are3 template families,6 kernel roles,18 parameter slots and162 task
variants. The1,620 scheduled arm/seed rows are planned rows with zero actual
method executions. Arms, variants, seeds and renamed slots are not independent
research samples. The engineering split assigns coding to train, document
planning to validation and synthetic state changes to test; this deliberate
track bias is unsuitable as a finished fair experiment or research holdout.

## Verification and immutable evidence

The owner suite passed46 tests and the root independently reran that suite.
An additional16 independent tests passed, including all162 literal oracle and
fair-input checks, exact inventory/reproducibility, failed cost and unknown
lower bounds, duplicate effects, gold modification, extra context, deleted
sources and renamed ancestry. All20 source/generated/frozen-contract pins
matched after the final change.

Actual failing-before evidence exposed shared mutable arm inputs, split-label
leakage, not_run inferred as zero, bool/float revisions accepted as integers,
renamed template clones across splits and giant-cost overflow escaping the
controlled rejection. These were corrected and retested. Test authoring and
temporary inventory errors are recorded separately from implementation bugs.

Final private logical artifact pins:

| Private logical directory | Artifact | SHA256 |
|---|---|---|
| datasets | fixture_gen.py | de8883ebda60b8c7db29470080ec203a26d8dd0333a6a048263cef98c9b5febe |
| evaluators | evaluation_contract.py | e75fd084b6193baaf96007d24f11e434632acaf30a30f92df6d59fbe49292a95 |
| tests | test_evaluation_contract.py | 767f0eca9c36b3bc0ca6b5b07e1fa0964fb2ba14c3fe04ccaed28cd54e16cc8c |
| manifests | integrity_v1.json | 9688e7d4ad92920f92709fec513b0bf31af71944754a66f01347cb582d4bfd26 |

The accepted CRW-01 contract bytes and baseline v1 contract are unchanged.
The raw private lab artifacts are not included in this public change. Actual
runtime adapter isolation, complete fair-experiment design, main-trial sample
size/hypotheses and statistical acceptance remain pending. No model/provider,
external effect, product build, release or deployment was run for this substage.

## 한국어 작업 기록

작업 ID: CRW-05.spec (#154)
변경 이유: 실제 모델 실험 전에 공정한 입력·비용 분모와 독립 평가기의 오류 거절 경계를 모의 자료로 고정한다.
실행한 검증: 소유46검사 root재실행, 독립16검사와162개 literal oracle·공정성 확인,20개 파일해시·결정적 재생 대조 통과.
미실행: 실행 adapter·OS 격리·모델/제공자/외부 효과 호출·빌드·배포·연구용 holdout·본실험·통계 수용.
현재 한계: 하위 명세 단계만 수용했다. 전체 CRW-05는 진행 중이며 실제 method 실행0, 연구 독립 표본수 미정이다.
커밋 상태: 비공개 원문은 lab에 보존하고 공개 요약·해시·진행 기록만 같은 단계 PR에 포함한다.
