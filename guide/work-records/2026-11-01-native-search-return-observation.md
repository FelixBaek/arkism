# Record native serving only after the final response transaction

Task: PR529, DYN-02/03 product connection
Research: Engineering correction of search observations; no quality or effect claim.
Design: Keep native serving intent private until the final response transaction succeeds. Preserve the computed baseline hash and leave actual baseline-return references unobserved when a native response is returned.
Relation: adapted
Deviation: No ranking arithmetic, policy, source protocol, response DTO or contract is changed. Shadow and fallback continue to return and record the baseline.
Why: Native responses overwrote baseline observations, and a final audit, fence or cancellation failure could still claim Served=true. The command help also omitted the already accepted on mode; it now lists on with its fast_slow requirement and development-only purpose.
Verified: Against the original source, four expected failing cases reproduced the observation errors. Actual Search tests cover on, shadow, invalid-source fallback, audit abort, changed selected-item revision and cancellation before the final transaction. The author archive passed targeted normal and race suites. Integration found that the two new test functions exceeded the changed-code complexity limit; setup and assertion helpers now keep the same checks within that limit. The integrating reviewer reran the exact split test source with the targeted race suite and pinned Go lint successfully. A separate actual PostgreSQL probe passed normal and race with genuine restricted LOGIN: the worker advanced fast/slow values, published the snapshot, and Search on returned the hand-calculated mixed ranking through the final fence. Shadow preserved the baseline, and deleting the actual native state row caused missing_state fallback to the baseline. Product Go source hashes matched when that probe ran; a later command-help string correction documents the existing on mode without changing flags, defaults or validation; the temporary database, role and cluster were cleaned up. Overlapping tests are not added together.
Limits: These are in-process Search fault tests and a scoped actual PostgreSQL worker/Search probe. Separate daemon startup/process integration, full remote CI, effectiveness evaluation and operational exercise remain unverified. Fast/slow values remain engineering inputs. No product build, deployment, provider call or operating database access.

제목: 최종 응답 트랜잭션 성공 후 새 순위 제공을 기록
이유: 새 순위 응답이 기존 결과의 관측 해시를 덮어쓰고 최종 실패도 제공 성공으로 기록하는 오류를 고쳤다.
검증: 원 구현에서 오류4개를 재현했다. 실제 Search의 on·shadow·대체 응답·감사 실패·항목 변경·취소 검사와 대상 일반·race·Go lint가 수정본에서 통과했다. 중복 검사는 합산하지 않는다.
검증 보완: 별도 실제 PostgreSQL·제한 LOGIN에서 worker 갱신·발행·on 예상 순위·shadow 기존 결과·상태 누락 대체 응답이 일반·race 모두 통과했다. 원 제품 소스 해시를 유지하고 임시 DB를 정리했다.
한계: 별도 daemon 시작·전체 원격 CI·운영·효과 검증을 뜻하지 않는다. 기본 설정과 시험용 수치는 바꾸지 않았다. 도움말에도 기존 on 모드와 fast_slow·개발용 조건을 명시했다. 제품 빌드·배포 없음.
