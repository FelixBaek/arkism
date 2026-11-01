# EXP-04 evidence index — implementation in progress

This file-only CLI prepares an evidence index and empty owner decision fields.
It reads only explicitly supplied local files. It does not choose a product
policy, approve a paper, enable a system, call models or access a database.

```sh
python3 -B evaluation/memory-harness-v1/experiments/exp_04/build_evidence_index.py \
  --package /absolute/package.json --artifact-root /absolute/artifacts \
  --out /absolute/new-report-directory

python3 -B -m unittest discover \
  -s evaluation/memory-harness-v1/experiments/exp_04 -p 'test_*.py' -v
```

`decision_template.json` is an empty `exp04-evidence-package-v1` input. With no
actual results the output remains missing/not_run, with no invented example
measurements. Synthetic test numbers are confined to test fixtures.

The package declares experiment family, stage/population, exact artifact bytes,
comparisons, coverage, structured claim requests, reuse registry, future data
questions and responsibility. Artifact records carry role, experiment ID,
source commit, relative file, raw SHA-256, byte length and review declaration.
Reviewed artifacts must link a separate hashed review-record artifact. These
are declarations, not authenticated approval. Source paths are constrained to
the explicit root; absolute paths, traversal, symlink parents/leaves, special
files, duplicate keys and nonfinite JSON numbers are rejected. Input and total
artifact sizes are bounded. Source bodies are not copied into reports.

The builder writes new `evidence_index.json`, `product_decision.json`,
`claim_evidence.csv`, and `next_questions.md`. It refuses an existing output
directory, creates private files, rolls back files from a failed new output,
and returns output/source/package hashes. The CSV connects each claim to its
experiment/file/commit/hash/review and protects cells from spreadsheet formulas.

Implemented checks and reporting:

- Read selected contrast numbers from the actual bound EXP03 analysis file,
  not caller-supplied performance prose. Check metric, family, population,
  stage, policies, computational plan hash, interval and count consistency.
- Preserve not_run/partial comparisons, all recorded arm statuses, fallback
  counts, cost totals/unknowns, analysis changes and comparison-family metadata.
  The cost-table hash binds individual source measurements, which remain in
  their original artifact rather than being copied as arbitrary nested bodies.
- Keep product readiness separate from research claim preparation. Authority,
  approval, revision, source, deletion, restore, scope or side-effect failures
  block product review regardless of a favorable quality result. Product
  coverage also needs normal utility, total costs/budget and SYS06 evidence.
- Always retain simple baseline/simplification/hold choices. Missing B6, strong
  nearest baseline, matched-write/time controls and answer quality limit the
  research scope. Recall does not become task success; an interval crossing
  zero does not become equivalence. Reported verdicts remain source verdicts.
- Flag repeated source/contribution pairs across papers, including supplied
  prior-use records. This is an accounting check, not a novelty judgment.
- Future questions carry required data codes and linked bottleneck review
  status; unreviewed/rejected evidence is not called an observed bottleneck.
  No question automatically authorizes a project or expensive algorithm spike.
- Owner decisions always remain null. Owner references/request date/unresolved
  reason are retained. Full-card acceptance, deployment and paper-promotion
  authorization remain false.

## Remaining engineering, separate from insufficient data

This is not full EXP-04 acceptance. The actual EXP02/native run-to-analysis
reconciliation is not implemented here. Merely listing raw-result/run-index
files is insufficient to prove all planned executions contributed to an
analysis; every executed comparison currently retains the explicit
`native_run_analysis_reconciliation_required` missing gate. Therefore this
implementation cannot produce a ready product/claim recommendation from a
fabricated success bundle. The formal native adapter is required upstream,
then its exact source/input/analysis linkage must be consumed here.

Current tests exercise real file reads and the CLI output/error/overwrite
paths, using labeled synthetic inputs. They do not validate actual research
results, a scientific final lock, independent novelty, participant outcomes,
operating service costs, or human adoption decisions. Full check_local for
this isolated branch has not yet run. No product build or deployment occurs.

Native execution costs: when an analysis includes `exp03-native-execution-costs-v1`, the separately listed cost-ledger artifact and exact RunIndex must match. The consumer validates the closed EXP02 index and recomputes all attempts, source/price hash bindings, additive totals and missing declared requirements. Nonadditive/peak measurements remain individual. Native batch costs are never divided across queries; query-level unknown costs and the separate required native-run-to-analysis reconciliation gate remain visible. This file-only reconciliation grants no execution, scientific or product approval.

Fresh native reconciliation is opt-in through all five arguments: `--native-replays FILE --native-input-root DIR --native-evidence-root DIR --native-output-root DIR --native-cli PROGRAM`. No program named in recorded run metadata is executed. The separately supplied program must match the projection plan's pinned binary hash. The `exp04-native-replays-v1` sidecar contains `comparisons` entries with `comparison_id`, `schedule_artifact_id`, `projection_plan_artifact_id`, `run_plan_artifact_id`, `run_index_artifact_id`, `observations_artifact_id`, `query_cost_ledger_artifact_id`, `native_cost_ledger_artifact_id` and `native_report_artifact_ids`. Schedule/projection/observations/native report artifact roles are `schedule`, `projection_plan`, `observations`, `native_verification`. Existing plan/index/ledger roles apply. Every consumed Python or Go source file must be separately byte-pinned in the package. Only a fresh projection matching the saved analysis, observations, both ledgers and exact raw verifier reports removes the native reconciliation gate. Authority, final science review, unknown query allocation and unresolved inference remain separate.

## 최종 입력과 task 현재 코드의 v2 재계산

`exp04-native-replays-v2`의 comparison은 v1 입력에 `final_inputs_artifact_id`를 추가한다. 파일럿이면 null이며, 최종 입력이 있으면 별도 인자 `--trusted-final-reviews`의 `exp04-trusted-final-review-pins-v1` 문서가 `comparisons: {comparison_id: expected_owner_review_sha256}`를 제공해야 한다. API는 마지막 `trusted_owner_reviews` dict 인자로 받는다. 예상 pin의 키는 최종 비교와 정확히 같아야 하며 legacy v1 context는 이를 무시하지 않고 거부한다. 이는 선언 원문을 식별하며 사람의 권한을 인증하지 않는다.

실제 projector를 최종 입력과 외부 pin으로 다시 실행해 REF·task·원본 native 출력·비용·원장·현재 코드 근거를 대조한다. package에는 `final_inputs`, `source_code`, `native_program` role이 추가된다. 현재 consumer/projector/의존 코드와 task 확장 코드, 실제 호출할 이미지 bytes가 등록되어야 한다. 이미지 등록 hash는 실행 전 projection pin, 실행 후 fresh pin/현재 경로 bytes와도 같아야 한다. package 원문과 caller pin 문서도 작업 뒤 다시 확인한다.

결과의 `computational_final_binding_verified`, `task_contract_bound`, `task_contract_replayed`는 각 기술 검사 범위만 뜻한다. grader artifact가 없는 pregrade 계약은 task replay로 표시하지 않는다. source 본문/질문은 출력하지 않고 식별 hash만 연결한다. worker의 연구/제품 결정은 null, 배포/논문 승격 권한은 false이며 합성 과제 성공은 실제 연구 효과·독립 정답·기관 승인 인수가 아니다.


### Source-pinned attribution replay

Optional native context `exp04-native-replays-v3` retains every v2 field and adds nullable `attribution_inputs_artifact_id` per comparison (artifact role `attribution_inputs`). Each non-null envelope requires a separately supplied expected raw SHA in the exact comparison mapping. CLI flag `--trusted-attribution-pins` reads `{"protocol":"exp04-trusted-attribution-pins-v1","comparisons":{"comparison_id":"expected_raw_sha256"}}`; API `build` accepts `trusted_attribution_inputs`. Missing/extra pins reject, as do pins on v1/v2 or without native context.

The consumer freshly runs the native projection with the original final-review pin and independent view pin, including exact CLI input provenance when present. Every view rule/label source and current adapter code must be registered evidence. A saved sensitivity report alone is insufficient. Reconciliation v3 reports `attribution_sensitivity_replayed`, expected envelope SHA and false label-truth/timing authentication; scientific/product decisions remain unchanged.
