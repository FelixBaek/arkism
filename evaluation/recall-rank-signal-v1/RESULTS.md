# Outcome weight in recall ranking: replay on the frozen recall set

Issue: FelixBaek/arkism#361 (parent #356). The server now multiplies each fused memory score by an outcome
factor read from its stored `memory_signal`: +5% per passed sample (three at most), ÷10 once any sample
failed, 1 without a judgement. `ARKISM_RANK_SIGNAL=off` disables it.

`replay.py` re-ranks exported server candidates with that factor, then applies the host floor (0.025) and the
kind caps the export recorded. Two exports of the same 36 memories and 144 questions (72 relevant, 60
irrelevant, 12 ambiguous excluded) are replayed:

- `search_candidates_ec057c4.json`: `TestPostgresRecallQualityExperimentExport` run on origin/main ec057c4
  with the frozen query and document vectors (no model call). This is the current code.
- `evaluation/recall-quality-v1/search_candidates.json`: the frozen 2026-10-18 export. origin/main's keyword
  lane no longer reproduces it, so its replay regression was not brought over.

Without signals the replay reproduces every exported final list of both exports
(`evaluation/test_recall_rank_signal.py`). Metrics are over the 36 relevant questions per split; wrong documents
also count what the 30 irrelevant questions returned.

## Current code (ec057c4), holdout

The exports carry no judged outcomes, so the real after equals before. The other rows assign synthetic
per-document signals to bound the weight; they are not evidence of production gain.

| | recall@1 | recall@6 | MRR | wrong docs |
|---|---:|---:|---:|---:|
| before | 0.833 | 0.833 | 0.833 | 12 |
| after, no judged outcomes (shipped) | 0.833 | 0.833 | 0.833 | 12 |
| ÷10, oracle: the 3 documents returned wrongly more often than rightly fail once | 0.833 | 0.833 | 0.833 | 12 |
| ÷10, worst: the other 33 documents fail once | 0.750 | 0.833 | 0.792 | 12 |
| ÷10, random signals, mean (min..max) of 200 draws | 0.781 (0.667..0.833) | 0.833 (0.833..0.833) | 0.807 (0.750..0.833) | 12 |
| ÷2, worst | 0.833 | 0.833 | 0.833 | 12 |
| ÷2, random signals, mean (min..max) | 0.817 (0.750..0.833) | 0.833 (0.833..0.833) | 0.825 (0.792..0.833) | 12 |

Calibration before: recall@1 0.833, recall@6 0.833, MRR 0.833, wrong 9; irrelevant questions returning
anything: 2/30 calibration, 0/30 holdout. 78 of 144 questions return nothing on the current code.

## Frozen 2026-10-18 export, holdout

| | recall@1 | recall@6 | MRR | wrong docs |
|---|---:|---:|---:|---:|
| before | 0.778 | 0.917 | 0.847 | 87 |
| ÷10, oracle: the 30 noisiest documents fail once | 0.694 | 0.917 | 0.801 | 87 |
| ÷10, worst: the other 6 documents fail once | 0.722 | 0.889 | 0.801 | 88 |
| ÷10, random signals, mean (min..max) | 0.639 (0.361..0.806) | 0.803 (0.639..0.944) | 0.710 (0.505..0.847) | 91.1 |
| ÷2, random signals, mean (min..max) | 0.737 (0.556..0.833) | 0.903 (0.833..0.944) | 0.811 (0.699..0.875) | 87.5 |

Calibration before: recall@1 0.806, recall@6 0.917, MRR 0.852, wrong 88. The full grid for both exports is
in `results.json`. Random signals: each document is unjudged with p=0.5, otherwise 1..5 samples each passing
with p=0.8 (seed 20261027).

## What this shows

- The premise of #356 is dated. On ec057c4 the synthetic set returns 12 wrong documents on holdout, not 87,
  and no irrelevant question returns anything; the price is recall@6 0.833 instead of 0.917 and 78 empty
  answers. The 27.5% precision figure describes the 2026-10-18 code.
- The weight cannot raise precision on either export: the wrong-document count never moves. Re-ordering the
  returned candidates does not change which candidates come back; only the limit, the host floor or a
  reranker can.
- A per-document failure is query-blind. Even the oracle that fails exactly the noisy documents does not
  help, because every memory here is also the right answer to two questions.
- ÷10 is the risky knob. On the current code noisy random signals cost about 5 points of recall@1 on
  average; on the 2026-10-18 export, where more candidates compete, up to 28 points of recall@6. ÷2 stays
  within 2 points everywhere. The shipped divisor stays 10 as specified; revisit it once real `memory_signal`
  rows exist.

## Limits

Only the server's returned candidates (≤6) are re-ranked offline, so a memory rising from outside that list,
which the live path allows over 3×limit fused rows, is not measured. Production outcome rates are unknown;
the random model is an assumption. The oracle uses the labels and is an upper bound, not a forecast. The
ec057c4 export is not sealed by `freeze.json`; it is reproducible from the frozen vectors with a disposable
Postgres.

---

## 전후 수치 (한국어)

현재 코드(ec057c4) holdout: recall@1 0.833, recall@6 0.833, MRR 0.833, 오답 12. 세트에 판정된 outcome이
없어 실제 전후는 같다. 합성 신호로 범위를 재면 ÷10은 무작위 신호에서 recall@1이 평균 5점 떨어지고
(2026-10-18 기록에서는 recall@6 최악 28점), ÷2는 어디서나 2점 안이다. 오답 수는 어떤 시나리오에서도 안 바뀐다.
반환된 후보 안의 순서만 바뀌고 어떤 후보가 오는지는 안 바뀌기 때문이다. 문서 단위 실패 신호는 질문을 구별하지
못해 정답 문서도 함께 내린다.

#356의 전제(정밀도 27.5%)는 2026-10-18 코드 기준이다. 현재 main은 오답 87→12, 무관 질문 오탐 13/30→0/30이고,
대신 recall@6이 0.917→0.833, 144문항 중 78문항이 빈 답이다.

출하 계수는 지시대로 ÷10이다. 실제 `memory_signal` 행이 쌓이면 ÷10부터 다시 본다. 오프라인 재생은 서버가
돌려준 후보 안에서만 순서를 바꾸므로, 실제 경로가 허용하는 3×limit 밖에서 올라오는 경우는 재지 않았다.

재현: `python3 -I evaluation/recall-rank-signal-v1/replay.py`
