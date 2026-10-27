# ST-815 recall experiment: candidate expansion and local reranking

The proposed reranker **failed the preregistered quality gate**. Production search, thresholds, API contracts and host budgets are unchanged. The deliverable is a reproducible evaluation and regression suite, not a shipped reranking feature.

## Locked experiment

The 36 public synthetic memories and 144 Korean questions were authored and independently reviewed by agents before model outputs were inspected. Calibration and holdout each contain 36 relevant, 30 clearly irrelevant and 6 ambiguous questions. Concrete policy topics are separated; broader domains and writing templates overlap. All 36 memories are retrieval candidates in both splits. Ambiguous questions are excluded from binary quality metrics.

`freeze.json` seals the data. `experiment_plan.json` was written before calibration metrics. Calibration selected four candidates and a raw-logit threshold of **-5.25** from the preregistered grid. `results/selected_policy.json` was frozen before the first holdout evaluation. The threshold and labels were not changed after holdout. Raw logits are model-specific values, not probabilities.

The embedding model is `microsoft/harrier-oss-v1-0.6b` at revision `f9b9dc8d367d443f2479d27aa5d8d2850c0774ee`. Document input preserves `text + "\n" + summary`; query prefixes and embedding space are recorded in `model_vectors.json`. The local cross-encoder is `cross-encoder/mmarco-mMiniLMv2-L12-H384-v1`, revision `1427fd652930e4ba29e8149678df786c240d8825`, pinned INT8 ONNX artifact. Its full identity and exact text hashes are recorded in `model_scores.json`.

## Results

| Split / path | Relevant questions with target | Wrong returned documents | Precision | Irrelevant questions returning anything |
|---|---:|---:|---:|---:|
| Calibration baseline | 33/36 | 88 | 27.3% | 14/30 |
| Calibration selected rerank | 33/36 | 23 | 58.9% | 3/30 |
| Holdout baseline | 33/36 | 87 | 27.5% | 13/30 |
| Holdout expanded only | 35/36 | 140 | 20.0% | 26/30 |
| Holdout expanded + rerank | 30/36 | 26 | 53.6% | 4/30 |

Wrong documents include incorrect extras on relevant questions as well as documents returned for irrelevant questions. Precision uses all 66 unambiguous questions per split. The six ambiguous holdout questions returned something in 4/6 baseline cases and 6/6 expanded/reranked cases; these are observations, not graded errors.

Candidate expansion uses distance 0.55, 16 server candidates and unchanged relative vector pruning, versus baseline distance 0.38 and limit 6. The first four expanded candidates contained the target for **36/36 holdout relevant questions**. All six rerank misses occurred at the relevance threshold, before final packing. Five were negation-category questions and one was a synonym question. The new path lost five baseline hits and gained two, for a net loss of three. Thus lower noise does not justify enabling this model under the frozen non-regression criterion.

The diagnostic misses are `hol-q002`, `hol-q010`, `hol-q030`, `hol-q042`, `hol-q046`, and `hol-q058`. For `hol-q046`, the target scored -5.32769 and was dropped while an unrelated transcription rule scored -2.01957 and survived. This identifies a relevance error; it does not establish that training language or quantization caused it.

These figures are not comparable as an improvement over the previous 30-question experiment: the corpus, wording and label distribution differ. They are not measured production success rates or human-approved labels.

## What was executed

Actual PostgreSQL and `Service.Search` executed all baseline and expanded searches, including canonical approval/live/source/hash fences, JSON conversion, usage receipts and the default host budget. The test uses a fake human policy authorizer, not live DOLHARU or a real restricted-role authorization test. Query embeddings are replayed from real local-model vectors. All corpus texts fit the final 6000-character budget.

Reranking is an offline experiment over the exported authorized candidate order. Accepted rerank results are treated as relevance-qualified in the experiment and then receive the same final limit and kind caps. This does **not** implement production transfer authorization, revision revalidation after an asynchronous model call, host relevance evidence, timeout fallback, or model cost reservation. Those boundaries must be implemented and tested before any future adoption.

A second isolated PostgreSQL run produced identical IDs, candidate ordering, scores, lanes, vector evidence and drop counts for all 144 questions, excluding timing. SQL ties can depend on UUID; no returned vector-distance ties occurred in this corpus. The regression compares meaningful outputs, not randomized database IDs or timing bytes.

Baseline PostgreSQL-to-host time across 144 sequential queries was p50 **27.86 ms**, p95 **52.19 ms**. Expanded retrieval was p50 **42.58 ms**, p95 **87.65 ms**. These exclude live embedding and reranker inference; they are not production end-to-end latency.

## Actual candidate-batch diagnostic

The primary score matrix processed all 36 documents in batches of eight. A later HTTP replay submitted the exact first four expanded candidates, without changing the selected policy. INT8 scores changed with batch composition: the largest absolute difference was **1.09353**, and 15 candidate decisions crossed the -5.25 threshold. A model/artifact identity alone therefore does not establish that a corpus-matrix threshold transfers unchanged to candidate-specific batches.

The actual replay still found **30/36 holdout targets**, returned **26 wrong documents**, and produced irrelevant-query results on **4/30** questions. The six target misses were the same; eight holdout result lists differed despite identical aggregate counts. Calibration replay found **32/36**, rather than the matrix's 33/36. This is a post-holdout diagnostic with an already-fixed policy, not a new independent validation. Neither path is adopted, and no threshold was retuned.

After three warmup requests, 136 sequential HTTP calls succeeded, eight zero-candidate questions made no call, and failures were zero. Client latency was p50 **24.03 ms**, p95 **33.88 ms**; process startup to readiness was **1.06 seconds**. These short synthetic pairs had at most 74 tokens. The measurements shared the development host with other checks and exclude embedding, retrieval and production authorization. They do not establish production p95 or capacity.

Input-contract validation caught an earlier diagnostic using post-budget `expanded` results instead of `expanded_candidates[:4]`. That measurement was superseded, retained privately as a failed experiment, and excluded from the numbers above. The corrected capture passed exact candidate ID/order checks for all 144 questions. `results/http_candidate_replay.json` preserves scores and observations without query/document text; `diagnose_http_replay.py` validates the capture and applies the frozen policy. A regression reproduces and rejects the post-budget-input mistake. The temporary HTTP process was stopped.

## Reproduce offline

No model download, network or credentials are needed to replay the frozen evaluation:

```sh
python3 -m unittest discover -s evaluation -p 'test_recall_*.py' -v
python3 evaluation/recall-quality-v1/prepare_vectors.py --verify
python3 evaluation/recall-quality-v1/evaluate.py calibrate \
  --candidates evaluation/recall-quality-v1/search_candidates.json \
  --output evaluation/recall-quality-v1/results
python3 evaluation/recall-quality-v1/evaluate.py holdout \
  --candidates evaluation/recall-quality-v1/search_candidates.json \
  --output evaluation/recall-quality-v1/results
```

The evaluation selector and model generators reject attempts to replace their frozen outputs. A repeated holdout command returns the already-unlocked result after checking calibration selection and input hashes. Replaying this known holdout is a regression test, not a new independent validation.

To regenerate PostgreSQL candidates, use a disposable PostgreSQL/pgvector database and a **new** output path:

```sh
POSTGRES_TEST_DSN='postgres://USER@127.0.0.1:PORT/DATABASE?sslmode=disable' \
ARKISM_RECALL_EXPERIMENT_EXPORT=/tmp/recall-candidates-new.json \
go test -race ./internal/memory -run '^TestPostgresRecallQualityExperimentExport$' -count=1 -v
```

The opt-in exporter records dataset, vector and test-source hashes. Regeneration includes timing differences, so do not replace the selected experiment's export. See `runtime/README.md` for explicit local-model installation and score regeneration; weights and virtual environments are excluded from the repository.

## Next experiment

Keep this result as a rejection regression. The observed bottleneck is preserving relevant memories when questions use negation, conditions and alternate expressions. A subsequent model or policy should be selected only on calibration material and evaluated on newly frozen, independently reviewed holdout topics; the now-known holdout cannot become fresh validation by retuning a threshold. Candidate broadening alone also remains rejected because its noise increased sharply.

Future calibration must use the same candidate batching and input construction as the serving path, or explicitly validate deterministic per-pair scoring. Reusing all-corpus INT8 logits for arbitrary candidate batches is insufficient.
