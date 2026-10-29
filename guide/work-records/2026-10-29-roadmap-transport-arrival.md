# Preserve historical retry delivery times

Task: DYN-01 arrival followup (DYN-02 integration discovery)
Research: P1 engineering metadata boundary, no effectiveness result
Design: Preserve original logical event identity and accepted coordinates while treating arrival as transport-attempt metadata.
Relation: adapted
Deviation: A standalone DTO cannot establish whether a delivery is a first acceptance. Its arrival upper bound belongs to the stream/receipt-aware boundary, without weakening observed-time or hash validation.

Why: An exact retry with a later transport attempt retains its original accepted time. The previous EventV2 validator rejected arrived_at_ns greater than that original accepted time, so valid later deliveries never reached same-ID lookup, including strict file ingress.

The narrow fix moves only arrival-versus-acceptance comparison from standalone EventV2 to the first-ID branch of EventFileV2, after duplicate/conflict lookup and before new sequence acceptance. Nonnegative integer transport validation, original observed-time upper bound, semantic hashes and same-ID coordinate conflicts remain enforced. A separately versioned transport semantics supplement requires the corresponding NEW-event guard in the forthcoming ApplyEvent implementation. A decoded standalone DTO is not a first-acceptance authorization.

Verified: original source and exact new test bytes/logs were archived before the fix. Actual original RED has five failing leaves: four late delivery typed/strict ingress cases and one error-priority case on an already-rejected coordinate conflict. The latter was not an unsafe acceptance. Current original20methods plus two new methods pass normal execution:22methods/179leaf,0fail/skip. The same22methods/179leaf also passed race. Fresh independent17methods/17leaf passed normal/race,0fail/skip, checking strict ingress, MaxInt64 arrival, first-delivery/observed-time/hash/shape/context negatives and input immutability. The same independent tests on archived original validator produced9RED:3 false rejections and6 already-rejected input error-priority mismatches, not unsafe acceptances. Initial toolchain/unfrozen-tree compile preparation failures collected0 and are retained separately. Pinned golangci-lint v2.14.0 reports0issues; Full python3 tools/check_local.py also passed Python412/Go complexity-vet-fullrace/UI532, with0UI failure/skip. Staged publication audit, formatting and diff checks passed.

Limits: No transition/runner/CLI implementation is accepted by this validator followup. A complete EventFile validates first occurrence; partial-history resume still needs receipt-aware replay validation. Arrival values are declared synthetic metadata, not authenticated network timing. Original accepted implementation pins and reports remain historical, with this new revision recorded separately. No product build, deploy, model/provider/DB execution or Claude edits. This followup does not increase the19/63 verified-card count.

## 한국어 작업 기록

작업 ID: DYN-01 후속 수정
이유: 늦게 도착한 원사건 재전송이 단독 자료 검증에서 먼저 거절되는 오류를 수정한다.
검증: 원소스·새테스트·실제RED5세부 로그를 수정 전에 보관. 수정본22방법179세부 일반 통과. 동일179세부race와 독립17방법/17세부 일반·race, 고정lint0 통과. 원본독립RED9는 오거절3·기존거절 오류순서6으로 구분했다. 전체Python412/Go복잡도-vet-fullrace/UI532 및 공개감사·형식 검사를 통과했다.
한계: 자료 구조 검증과 새 사건의 수락 검증을 구분한다. 실제 전이·운영 인증·효능·빌드/배포·Claude 수정은 미수용이며 완료19/63은 동일하다.
