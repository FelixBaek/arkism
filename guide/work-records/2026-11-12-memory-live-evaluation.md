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
(public alias `historical_primary_v1`, SHA-256
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

## Source-date candidate comparison

The source-date candidate has the same code tree at commits 2aeb0030 and
e3b4b10a (the latter corrects only the commit work-record message). Its immutable
evaluation binary was built at 2aeb0030. The same 32 questions again scored
28/32, with unchanged per-category counts. All 64 model calls succeeded,
using 185,245 input and 4,697 output tokens (189,942 total) in 44.36 seconds.
This is not an accuracy gain.

The live prompts now contain 608 recovered observation anchors across the 32
questions. In the failed duration question, the answer changed from missing-start
abstention to a cited approximate interval using both endpoints; the frozen gold
still marks that interval wrong. The observation repair therefore changes actual
answer behavior, while its score effect remains unproven. No gold, question or
grader was changed to accept it.

## Full frozen 1,085-question diagnostic

The source-date candidate completed the entire scheduled set with 983 correct
answers out of 1,085 (90.60%). It does **not** meet the per-category target.

| Category | Correct / scheduled | Accuracy | More correct answers needed to exceed 90% |
| --- | ---: | ---: | ---: |
| Single-hop | 581 / 602 | 96.51% | 0 |
| Temporal | 202 / 233 | 86.70% | 8 |
| Multi-hop | 161 / 188 | 85.64% | 9 |
| Open-domain | 39 / 62 | 62.90% | 17 |

All 1,085 retrieval requests succeeded on their first attempt. Identical model
request fingerprints reused their first completed result; all 64 pilot calls
were preserved exactly. The full run used 2,147 unique answer/judge requests.
One answer stream terminated without a completion event. Its partial response
and missing usage were retained, and it remains non-correct in the denominator.
The run stopped, the failure was audited (no quota/auth/rate refusal was found),
and only unattempted work resumed. The failed fingerprint was never retried.
All started requests were resolved; no invalid grading responses were accepted.
There are 101 judged wrong answers plus the one transport failure.

Reported usage is 6,276,017 tokens, a **lower bound** because the truncated call
returned no usage. Dollar cost is unavailable. Wall time including acquisition
and the failure pause was 27.26 minutes. No external tools were executed.
The final consolidation issued zero new model calls. The frozen prompt-file
SHA-256 is `ec718c3ee233d962cfd9c2700a265a94ab0bb426d995c67afad2cb23525cdcc7`.

An early manual audit of 21 failures found five missing-evidence cases, five
imprecise gold/question inconsistencies, eight unsupported-inference cases,
two relative-week ambiguities and one unavailable visual attribute. These are
provisional diagnoses, not exclusions or a classification of all failures.
None was removed from the primary score. Historical scores used a different
execution path and are not a controlled baseline for claiming a gain/regression.
Further native-service validation is separate from this completed protocol.

## Acquisition integrity and next measurement

An initial bulk retrieval attempt encountered transaction retries and was stopped;
its logs are retained. A later noncanonical 32-question retrieval-only sample is
excluded from the model score. The canonical 32 prompts all succeeded on their
first request. A source audit found 4,519 memories missing their own observation
anchors, all with hash-matching original source references and historical source
dates. The follow-up source-date repair is evaluated separately against the same
fixed questions. The full 1,085-question schedule was frozen and completed as reported above;
per-category acceptance remains unmet. A separate dataset audit found this
historical subset omits two available questions, and three other conversations
were not part of it. It must not be represented as the full 1,540-question set.

## 한국어 기록

변경 이유: 실제 검색과 모델 답변으로 분야별 품질을 측정하고 실패 원인을 보존한다.
검증: 사전 고정 32문항에서 28개 정답, 모델 호출 64회 성공. 도구 실행과 누락은 없다.
한계: 이미 개발에 사용한 소규모 자료이며 모든 분야 90%를 달성하지 못했다.
전체 평가 결과는 983/1,085(90.60%)이며 단일 기억 검색만 90%를 넘었다.
한 번의 응답 중단은 실패 분모에 남겼으며 재시도하지 않았다. 운영 배포는 하지 않는다.
