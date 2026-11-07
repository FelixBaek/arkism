# Side-lane eligibility / 보조 검색 후보 제외 시점

Task: memory quality retrieval follow-on, independent review finding.
Research: none
Why: restricted conversation rows and restricted/conflicted candidates could consume the finite side-lane window before the caller rejected them.
Verified: real PostgreSQL reproduction failed before the fix for restricted sources/candidates. Fixed eligibility and related conversation/provisional/search/filter/mode tests passed in 3.619s.
Limits: this corrects candidate eligibility, not a claim of measured benchmark quality gain or general authorization prefiltering.

제목: 보조 검색에서 제외할 후보를 조회 개수 제한 전에 제거
이유: 반환할 수 없는 후보가 상위 20개를 차지하여 실제 가능한 결과가 사라지는 문제를 해결한다.
검증: 기존 코드에서 누락 재현 후 수정했다. 실제 DB와 관련 회귀 검사 3.619초 통과.
한계: 일반 기억 검색의 권한 처리와 후보·응답 예산은 바꾸지 않는다.
연구: 해당 없음

Only the existing caller predicates move into the repository candidate selection:
conversation source searches exclude sensitivity=restricted; pending candidate
searches exclude sensitivity=restricted and nonempty conflict_id. Conversation
conflict_id does not exclude a source, matching conversationTurns. Ordinary memory
search, other collections, all later per-record authorization, source checks and
privacy checks are unchanged. The 20-row side-lane window, five conversation
outputs and individual exact/keyword/vector modes remain unchanged.


The fixture creates exactly 20 excluded rows followed by one allowed row in each
of three scenarios. After the fix the allowed row is returned independently by
exact, keyword and vector modes. A conversation with a conflict_id remains
eligible, matching the caller, and an ordinary restricted approved-memory lookup
still reaches the ordinary authorization stage unchanged. The test initially
reused IDs across two candidate scopes, conflicting with the repository's global
(collection,id) key; distinct fixture IDs corrected that setup error. This was not
a product failure or a relaxed scope boundary. Prior restricted-source and
restricted-candidate failures were genuine pre-fix starvation reproductions.

No evaluation dataset, answer label, candidate cap or output budget was changed.
No provider calls, deployment, publication or production access occurred.
