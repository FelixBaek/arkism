# Evaluation programs

This directory holds the memory-reuse protocols, fixtures and regression tests.
`reuse-v010`, `reuse-20261015` and `reuse-v2` are separate versioned experiments;
do not merge their contracts or rewrite frozen measurements to tidy the layout.

Historical API performance evidence lives in
[`evaluations/2026-10-15/api`](../evaluations/2026-10-15/api/README.md).
It measures a different subject and is not a duplicate memory benchmark.
Keep new memory evaluations here and link old evidence at its original path.

`recall-quality-v2` holds the tools and the aggregate results of the human-labeled
recall set (#366). The questions and labels themselves are held-out data and stay
outside the repository; its README says where.

`merge-threshold-v1` sweeps the capture merge threshold over pairs derived from the frozen
`recall-quality-v1` set; its RESULTS.md says why the proxy cannot settle the default.
