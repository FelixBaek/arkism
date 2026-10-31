# Distinguish fast/slow observations from the frozen scalar contract

Task: PR529, DYN-02/03 product connection
Research: Engineering observation compatibility; no accepted parameter policy or effectiveness claim.
Design: Add a separate fast/slow descriptor and observation protocol for both shadow and on, with an explicit mode. Preserve the existing scalar descriptor, scalar JSON, stages-v2 contract, arithmetic and publication hash domains.
Relation: adapted
Deviation: Extend observation metadata only. Ranking, response DTOs, authorization, configuration defaults and state updates are unchanged.
Why: Fast/slow attempts previously identified themselves with the frozen scalar shadow-only contract, including when on returned a native response. That contract could not truthfully describe both modes.
Verified: The original source failed the new shadow/on metadata tests while the literal legacy scalar JSON control passed. The isolated correction passed focused normal and race suites: 34 top-level passes and 101 pass events in each, with no failures. One existing database-only test was skipped because these metadata runs intentionally had no database DSN; the new metadata tests did not skip. Tests exercise actual Search in both modes, source fallback, rejected final audit, explicit off, early fallback, failed arithmetic with a nonempty digest and unobserved ranking, legacy decode and legacy JSON preservation. The integrating reviewer split the new test helpers without removing assertions and reran pinned changed-code Go lint with zero findings. Separate PostgreSQL functional evidence is recorded in the return-observation work record; overlapping counts are not added together.
Limits: Served means successful Search return, not HTTP delivery or actual use. Empty selected refs can remain unobserved even with a failed arithmetic digest; status and stages distinguish this. Existing publication hashes do not directly bind every runtime parameter. No daemon startup, effectiveness result, product build, deployment, provider call or operating-data access is claimed. Full remote CI on the final head remains required.

제목: fast/slow 관측을 고정된 scalar 계약과 구분
이유: on에서 새 결과를 반환해도 기존 shadow 전용 계약으로 기록하던 오류를 고쳤다. fast/slow의 shadow·on에 별도 계약과 명시 모드를 사용한다.
검증: 원본에서 두 모드 오류를 재현하고 기존 scalar JSON 대조군은 통과했다. 수정본 대상 일반·race 각각 최상위 34개와 하위 포함 101개 통과, 실패 0개였다. DB 연결을 의도적으로 주지 않아 기존 DB 전용 검사 1개는 건너뛰었으며 새 관측 검사는 모두 실행했다. 기존 scalar JSON·고정 계약·stages-v2·계산·소스 해시 영역을 유지했다.
한계: 관측 성공은 Search 반환 성공이며 실제 전달·사용·효과를 뜻하지 않는다. 실패한 계산 해시가 있어도 빈 목록은 미관측일 수 있다. 기존 발행 해시가 모든 설정을 직접 묶는다고 주장하지 않는다. 최종 원격 CI는 별도로 필요하며 제품 빌드·배포·운영 DB·외부 모델 호출은 하지 않았다.
