# Capture merge threshold: sweep on the frozen recall set

The capture gate (`internal/memory/capture_merge_gate.go`) opens a `merge_proposed` review when the new claim's
query-mode vector sits at or above `CaptureMergeSimilarity` (default 0.92) of an approved memory's document vector,
taking the highest such memory. `sweep.py` replays that comparison on the frozen `recall-quality-v1` vectors
(harrier-oss-v1-0.6b, same query prefix and document format as the server). No model is called.

## Labeled pairs

`pairs.json` crosses the 132 non-ambiguous questions with all 36 memories (4,752 pairs). A pair is `same` when the
memory is in the question's independently reviewed `relevant` list (72 pairs); every other pair is not.
This is a proxy. The frozen set holds questions a memory answers, not rewordings of a memory, so a `same` pair
here is looser than what the gate is meant to catch.

- `pair`: each pair at or above the threshold is a "same" prediction.
- `gate`: per question only the highest pair at or above the threshold is proposed (the gate's rule);
  recall is over the 72 questions that have a relevant memory.

## Result

`same` cosines: min 0.230, median 0.689, max 0.825. Highest non-`same` cosine: 0.661.

| threshold | pair P | pair R | pair F1 | gate P | gate R | gate F1 | proposals |
|---:|---:|---:|---:|---:|---:|---:|---:|
| 0.60 (below range, reference) | 0.863 | 0.875 | 0.869 | 0.955 | 0.875 | 0.913 | 66 |
| 0.65 (below range, reference) | 0.979 | 0.653 | 0.783 | 0.979 | 0.653 | 0.783 | 48 |
| 0.70 (below range, reference) | 1.000 | 0.361 | 0.531 | 1.000 | 0.361 | 0.531 | 26 |
| 0.75 (below range, reference) | 1.000 | 0.097 | 0.177 | 1.000 | 0.097 | 0.177 | 7 |
| 0.80–0.82 | 1.000 | 0.028 | 0.054 | 1.000 | 0.028 | 0.054 | 2 |
| 0.83–0.91 | – | 0.000 | 0.000 | – | 0.000 | 0.000 | 0 |
| **0.92 (default)** | – | 0.000 | 0.000 | – | 0.000 | 0.000 | 0 |
| 0.93–0.98 | – | 0.000 | 0.000 | – | 0.000 | 0.000 | 0 |

Every row from 0.80 to 0.98 is in `results.json`. Within 0.80–0.98 the best F1 is 0.054 at 0.80–0.82; 0.92 proposes
nothing. Below the range, the proxy pairs separate best near 0.60.

## Decision

The default stays 0.92. The data does not support a change: on this proxy no question reaches 0.83, and a
question that a memory answers is not a rewording of it, so the 0.60 optimum measures relevance, not sameness.
Lowering the gate to it would propose merges for claims that only share a topic. What the result does show is
that the 0.80–0.98 range is above everything this embedding space gives for question-to-memory pairs; deciding
the threshold needs a labeled set of claim rewordings versus same-topic distinct claims, embedded in query mode.

## Limits

- Proxy labels (question answered by memory), synthetic Korean data, 36 memories; no claim-to-claim pairs.
- Agent-reviewed labels, not human-approved.
- No new embeddings were made; the claim-rewording set needs a live embedder and was not built.
- calibration/holdout are not separated because no threshold was tuned.

Regenerate with `python3 sweep.py --write-pairs`; `evaluation/test_merge_threshold.py` checks the pairs, the
default against the Go constant, and that `results.json` reproduces.
