# Preserve visible image evidence during ingestion

Task: MEMORY-QUALITY-90; general visual evidence follow-up.
Research status: development change, no measured visual accuracy gain.

The opt-in image reader previously requested exact OCR followed by only one short
sentence for a photo or diagram. That instruction could omit visible attributes
and relations needed by later questions, even when original image input exists.

The prompt still requests exact text in its original language first. A separately
headed visual description now requests concise visible objects, distinguishing
attributes, spatial relations and legible chart labels, units and values. It
requires uncertainty for unclear details and prohibits inventing unreadable
values or inferring personal identity, occupation, relationships or intentions
from appearance. Existing model budgets, image opt-in, transfer authorization,
privacy filtering and candidate review are unchanged.

This cannot recover information from an already stored text-only caption or an
unavailable original. It does not add image rereading to reflect. No benchmark
question, answer or image-specific attribute is embedded in the product prompt.

## Verification

The new regression uses canned image-model output through ExtractFileMemories.
It checks that the actual image request receives the grounding instructions,
that a description survives extraction and becomes hash-bound source evidence
for a review candidate, and that private and secret output is removed before
extraction and storage. Existing tests cover transfer denial before image
submission, missing opt-in, dry-run behavior and scanned PDF handling.

Focused race suite `go test -race ./internal/memory -run
'Test(FileImage|FileMedia|ScannedPDF)' -count=1` passed (1.371s).
A separately authorized direct-model probe used two locally drawn synthetic PNGs
(objects and a small chart), with expected attributes and numeric relationships
frozen before the four requests. Baseline and changed prompts both retained the
visible objects, spatial order and chart values. The changed prompt separated
visual description and explicitly described the chart ratio and missing units;
the baseline values already supported the same ratio. This demonstrates prompt
execution on these examples, not an accuracy improvement or ingestion end-to-end.
All four tool-free requests succeeded using gpt-6.1-terra with medium reasoning.
Returned usage totaled 1,810 input and 145 output tokens (1,955 total); dollar
cost is unavailable. There were no retries or executed tools. Private manifests,
image hashes, expected facts, request starts, raw responses and usage are retained.
No benchmark images or answers were used; the separate four-call probe does not
change the historical benchmark's schedule or denominator.

## 한국어

변경 이유: 이미지 설명을 한 문장으로 제한해 눈에 보이는 속성과 관계가 누락될 수
있는 지침을 보완한다. OCR 원문과 시각 설명을 분리하고 불확실성을 명시한다.
검증: 가짜 이미지 모델을 사용한 실제 추출 경로에서 비공개 내용 제거와 근거 연결을
검사한다. 실제 이미지 인식 정확도나 분야별90% 달성을 입증하는 검사는 아니다.
