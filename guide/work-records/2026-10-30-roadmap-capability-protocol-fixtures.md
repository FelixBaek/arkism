# Record tested MCP inventory compatibility fixtures

Task: CAP-07 (#156)
Research: Capability engineering fixture; no tool efficacy or independent-paper claim
Design: Reuse actual stdioTools/serverTools in a byte-identical, minimal dependency shadow with synthetic HOME/cache and local fake servers. Distinguish protocol compatibility from tool meaning and authority.
Relation: adapted
Deviation: Product probing code is read-only; modern client support and observed product gaps are separate integration work.

Why: A version label or an empty list cannot prove a compatible, complete inventory. Test legacy-only, modern-only, dual-era and timeout/error peers, plus adversarial initialization/result/pagination cases, against actual current functions.
Verified: Author and separate root replay each executed Python9 methods and actual Go1root/11subtests with race, zero failures/errors/skips. Root independently checked five criteria covering all11 expected/observed rows, real traces, nil failure versus known empty, unchanged67production files and CAP01's three artifacts. Official versioning/discovery/modern error envelopes were independently compared. Seven new private artifacts are frozen. The original fake modern-success omissions and root taxonomy-label preparation error were preserved and corrected separately from product findings.
Limits: Current inventory still sends legacy initialization only. Four actual gaps remain: accepting initialization error, accepting malformed initialization result, treating malformed tools as known empty and caching partial pagination as complete. Their reproduction is not a repair. Tool meaning/authentication/execution authority/live servers/network transport/Windows/macOS remain unknown. No tools/call, operational state, account settings, product code, build/deployment or Claude changes. The fixture card is verified; a narrow product integration requires its own need and dispatch. Whole roadmap21/63 after this card and PSY01 preparation, not product/research completion.

## 한국어 작업 기록

이유: 실제 초기화·캐시 경로로 네 종류 가짜 서버를 검증하고 오류·빈 목록·부분 목록을 구분한다.
검증: 작성자와root 각각Python9·Go11조건/race, root별도5검사 통과. 원소스67·CAP01 3핀 불변. 수정 전 fixture 준비 오류는 원자료를 보존했다.
한계: 현행 검증 누락4건은 재현만 했으며 제품 수정·실제 서버·도구 의미·권한 수용이 아니다. 빌드·배포·Claude 변경 없음.

Final public-record validation: check_local52908 exited0 with Python524, Go complexity/vet/fullrace and UI532. Staged publication audit passed. The acceptance artifacts remain private; this PR records verified scope and limitations only.
최종 공개 기록 검증: 전체52908 종료0(Python524·Go·UI532), 공개 검사 통과. 원 연구 자료는 공개하지 않는다.
