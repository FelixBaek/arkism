## Change / 변경

Task: none
Why:
이유:

## Validation / 검증

Verified:
검증:
Limits:
한계:
Research: none

## Research design / 연구 설계

For a research-linked change, replace `Research: none` above with its protocol
or research link and fill all three fields. They describe the design and its
relationship to prior work; passing this check does not establish an effect.
Ordinary changes may keep `Research: none` and omit this section. If detail
fields are supplied, keep the whole set (use `none` where not applicable).

Design: none
Relation: none
Deviation: none

`Relation` / `구현 관계`: `faithful`, `adapted`, `partial`, or `unsupported`.
Korean aliases for the other keys are `설계 이유` and `원문과 다른 점`.
Record differences and their consequences in `Deviation`; use `none` only
when there is no deviation. Keep private hypotheses and original paper/card
text out of this public record. New research-linked commits use the same fields;
historical commits before the checker rule's start are not rewritten. The fixed
author-date cutoff is `2026-10-29T06:25:00+09:00`, matching the checker rollout.
An author date is editable: reviewers still need the actual research evidence.

The companion `.github/commit_template.txt` can be used for an individual commit
with `git -c commit.template=.github/commit_template.txt commit`; no shared Git
configuration is required.

Include actual commands and results. Mark Windows installation, upgrade, key
configuration, publication and other checks as unverified unless they ran.
Never include credentials, conversation excerpts or participant data.
