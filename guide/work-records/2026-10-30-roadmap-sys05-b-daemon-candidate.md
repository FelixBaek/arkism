# wire dynamics worker and refresh into the daemon

Task: SYS-05.B (#152)
Research: Engineering integration with synthetic fixtures in an isolated database and loopback HTTP; no load, quality or scientific claim.
Design: Explicit paired flags load and pin both config files once before any database work; absent flags keep the feature off and recovery or admin modes reject them. The worker reuses the existing work round and 90 s deadline. The API process refreshes with an explicit read-only token, rotates its starting scope and joins its background loop before the stop receipt.
Relation: adapted
Deviation: Static review of the draft found three defects (lost 90 s deadline, fixed refresh start scope, lost stop/join failure); all are fixed with regression tests. The load file now passes the shared Unicode check before hashing. The JSON projection was split into smaller functions to meet the existing complexity limit. The PG fixture now gives its candidate source a span, which candidate creation requires.
Why: SYS-05 B must run as separate worker and API daemons with the same pinned plan before load comparisons.
Verified: Against an own disposable PostgreSQL, go test ./cmd/memoryd passed in normal and -race runs (138 pass events each, 0 failures), including a separate worker and API where the worker published, the API refreshed its own cache, actual HTTP Search returned the target and a read-only token could not publish. The six independent Unicode counterexamples are rejected; with the original loader four load-file cases fail. golangci-lint v2.14.0 reported 0 issues; vet and tools/complexity passed.
Limits: Not accepted. Restore wiring, long-run fairness, same-condition load comparison and the nine-cost measurement remain. Windows, worst-case memory and a real installed daemon were not exercised. Full check_local and unrestricted go test ./... were not run. No product build, deploy or operating data. SYS-05 stays in_progress; 25/63 done, 24 required remaining.

## 한국어 작업 기록

제목: 데몬에 동적 작업자·읽기 전용 갱신 연결
이유: 부하 비교 전에 같은 고정 계획으로 작업자·API 데몬을 따로 띄울 수 있어야 한다.
검증: 자체 임시 PG에서 cmd/memoryd 전체 일반·race 각 138건 통과·실패 0. 작업자 게시→별도 API 캐시 갱신→실제 HTTP 검색 결과 확인, 읽기 토큰 게시 거부 확인. 독립 Unicode 반례 6건 거부(원래 로더는 4건 실패). lint 0건, vet·복잡도 통과.
한계: 미수용. 복원 연결·장시간 공정성·같은 조건 부하 비교·9비용 측정이 남았다. Windows·최악 메모리·실제 설치 데몬은 검사하지 않았다. 전체 check_local·무제한 go test 미실행. 전체 25개 완료·필수 24개 남음 유지.
