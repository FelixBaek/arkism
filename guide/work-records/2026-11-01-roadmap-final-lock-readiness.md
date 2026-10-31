# Validate final-lock values before making them immutable

Task: EXP-01 (#153), PR526 independent review
Research: Engineering validation of declared research inputs; no experiment or effect claim.
Design: Reuse the frozen parser and hash binding. Validate the separate v2 research values for types, numeric ranges, complete structured budgets/sample sizes and explicit unresolved states before both lock creation and verification. Exact decimal and fraction strings retain the integer-only shared codec.
Relation: adapted
Deviation: The owner still chooses the design and policy criteria. Description validation is a readiness boundary, not scientific or approval authentication. Existing v1 contracts, gold data and recorded owner values remain unchanged.
Why: Independent reproduction showed k=0, sample_size=0 and an unresolved minimum effect could be issued and verified as final_locked.
Verified: Root reran the full evaluation collector with the exact corrected files:597 tests passed. The author separately checked the lock v2 suite12, CLI rejection without output, a rehashed invalid lock, exact numeric boundaries and the unchanged parser/contract consumers. These overlapping checks are not added together. Invalid cases fail against the preserved original implementation and reject after the change.
Limits: A values file still requiring sample or cost recomputation is not final-ready. This does not authorize experiments, choose research values, authenticate owner provenance or certify model quality. No product build, deployment, provider call or operating-data access.

제목: 최종 잠금 전에 연구값의 준비 상태 검증
이유: 표본0·잘못된 k·미확정 최소 차이를 최종 확정으로 승인하는 결함을 재현했다.
검증: 정확한 수정본에서 전체 평가597개가 통과했다. 별도 잠금12개와 CLI·정확 수치·변조 검사는 중복 합산하지 않는다. 원 구현의 실패와 수정 후 거절 근거를 보존했다.
한계: 비용·표본 재계산이 남은 값은 잠그지 않는다. 연구값 결정·실험 승인·모델 효과의 증거가 아니며 원 계약과 소유자 기록은 바꾸지 않았다. 제품 빌드·배포 없음.
