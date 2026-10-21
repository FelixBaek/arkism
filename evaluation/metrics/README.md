# Release metrics

One result per release, measured the same way each time, so the numbers form a
series. Run from the repository root at the release commit:

```sh
python3 evaluation/release_metrics.py            # name = git describe --tags
python3 evaluation/release_metrics.py --version v0.1.4
```

It runs `TestReleaseMetrics` (`internal/host/capture/release_metrics_test.go`),
writes `evaluation/metrics/<version>.json` and rebuilds the table below. No
network, model key or personal data is used: the input is
[`fixture.json`](fixture.json), the five tasks and facts of the frozen v0.1.0
reuse benchmark plus synthetic transcripts and five unrelated distractor rules.

<!-- metrics:start -->
| Version | Date | Commit | Compression (tokens) | Hook p50 / p95 / max ms | Recall tokens (static) | Savings vs static |
| --- | --- | --- | --- | --- | --- | --- |
| v0.1.3-50-g1a7e8c9 | 2026-10-21 | `1a7e8c9` | 19.27x | 0.3 / 0.382 / 0.782 | 128.0 (140.0) | 8.6% |
<!-- metrics:end -->

## What each number means

- **Compression (tokens)**: transcript tokens in ÷ stored memory text tokens
  out, pooled over the five tasks (cl100k_base). The transcript goes through the
  real capture steps (`readNew` keeps typed user text, `ExtractClaims` applies
  the claim and quote checks); the extraction model's answer is frozen to the
  task fact, so this measures the pipeline around the model, not the model.
- **Hook p50 / p95 / max ms**: wall time of `hookMain` for one
  `UserPromptSubmit` event (recall search, budget, session ledger writes, JSON
  output), against a keyword store served over loopback HTTP. 40 rounds × 5
  tasks, each in a fresh session. It excludes the process start of the
  `arkism` binary and any real database or embedding search.
- **Recall tokens (static)**: mean tokens the hook injects per task, next to a
  static instruction file that lists all ten rules every turn. The no-memory
  baseline injects 0 tokens. **Savings vs static** is `1 − recall / static`.
  The recall block carries a fixed header, so with ten short rules the saving
  is small; it grows with the number of stored rules.

## Limits

- Synthetic fixture only; not a measure of answer quality or of retrieval
  quality on real memories (the store matches keywords, so the right rule is
  found by construction). Live reuse results are in
  [`../reuse-v010`](../reuse-v010) and [`../reuse-20261015`](../reuse-20261015).
- Latency depends on the machine; compare rows measured on similar hardware
  (`environment` in each JSON file).
- Token savings count injected prompt tokens only, not tokens an agent spends
  rediscovering a rule without memory.
