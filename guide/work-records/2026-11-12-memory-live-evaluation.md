# Memory quality: actual diagnostic evaluation

Task: MEMORY-QUALITY-90
Research: previously exposed development diagnostic; no scientific acceptance.

## Purpose and measured configuration

The user authorized an isolated evaluation server build and actual model calls,
followed by PR checks and review. Deployment remains outside this task.
The first measured engine is ef3feeb39608e091de1c092bd9420cea47e4470e.
The evaluator obtains real retrieval prompts from the isolated server and runs
answer generation and grading externally. This exercises retrieval plus model
answers; it does not establish the server provider route or its budget handling.
The explicit evaluation profile enables ten conversation passages and disables
source rereading. Production defaults are not changed by this evaluation.

The model is gpt-6.1-terra with medium reasoning. A fixed historical grader
(`historical_mem0_style_v1`, SHA-256
`111f7b4e2a8039fea238130ee831dfd2d2b7e89f73785e75da036d9895df68c9`)
is retained for comparability, rather than substituting a more permissive grader.
Generation receives the question and retrieved evidence, never the gold answer.
The tool-free transport permits no tools or executor. First completed results
are retained by exact request fingerprint; failures, usage and duplicates remain
in the ledger. Subscription dollar cost is unavailable, not zero.

## First 32-question diagnostic

The question keys were frozen before model calls, with eight per category.
All questions come from an already exposed seven-conversation development set;
this is neither a fresh holdout nor the complete ten-conversation benchmark.

| Category | Correct / scheduled | Accuracy |
| --- | ---: | ---: |
| Single-hop | 7 / 8 | 87.5% |
| Temporal | 7 / 8 | 87.5% |
| Multi-hop | 8 / 8 | 100% |
| Open-domain | 6 / 8 | 75% |
| Total | 28 / 32 | 87.5% |

All 64 generation and grading requests succeeded, with zero missing answers,
invalid judgments or executed tools. Returned usage was 174,256 input and 4,688
output tokens, 178,944 total; wall time was 43.56 seconds. These small-category
scores do not establish the target of more than 90% in every category.

The four errors include a missing temporal anchor, an image attribute absent
from the available text caption, and two presumptive conclusions not established
by the retrieved text. They remain in the primary denominator. Neither gold
labels nor product facts are rewritten to raise the score. Raw questions,
answers, model responses and source comparisons are retained privately.

## Acquisition integrity and next measurement

An initial bulk retrieval attempt encountered transaction retries and was stopped;
its logs are retained. A later noncanonical 32-question retrieval-only sample is
excluded from the model score. The canonical 32 prompts all succeeded on their
first request. A source audit found 4,519 memories missing their own observation
anchors, all with hash-matching original source references and historical source
dates. The follow-up source-date repair is evaluated separately against the same
fixed questions. The full 1,085-question schedule is frozen; full results and any
90% acceptance are pending.

## 한국어 기록

변경 이유: 실제 검색과 모델 답변으로 분야별 품질을 측정하고 실패 원인을 보존한다.
검증: 사전 고정 32문항에서 28개 정답, 모델 호출 64회 성공. 도구 실행과 누락은 없다.
한계: 이미 개발에 사용한 소규모 자료이며 모든 분야 90%를 달성하지 못했다.
전체 평가와 수정본 비교는 아직 진행 중이다. 운영 배포는 하지 않는다.
