# Upgrade the experimental observer to the accepted execution-reference protocol

Task: CRW-02.modules (#154)
Research: E implementation correctness; no runtime effectiveness result
Design: Accepted crew v2 registry, common usage normalization and existing optional observer code
Relation: adapted
Deviation: The unused experimental helper is upgraded in place with a new discriminator. Accepted v1 contract bytes, private consumers and the earlier8c14279d implementation evidence remain unchanged.

The helper now represents real workflow, meeting and assist references, explicit
root/parent collection namespaces, the16-field crw2 identity and separate usage
roles. Provider leaf, aggregate and undispatched reservation estimates have
separate totals. A provider-only compatibility view is explicitly scoped and
cannot turn opaque observations into exact free usage. The existing host
normalizer's bytes and usage schema are unchanged.

Strict decoders reject duplicate, missing and unknown fields, invalid UTF-8 and
surrogates, lexical int64 coercion, nonfinite values and excess nesting. The
optional qualifier checks actual supplied parent proofs and closed membership
journals by raw hashes. Bare complete enums, unresolved parent relations and
partial coverage cannot qualify accounting. Existing capture, pending and sink
errors survive qualification; replaying records into a fresh collector cannot
erase those failures. Caller pins remain local integrity evidence, with
RuntimeAuthenticated=false and RunCoverage=unknown.

Independent normal and race runs each passed223 leaf checks:132 strict payloads
(nine valid/123 rejected),37 observer/qualification boundaries and54 unchanged
normalizer checks. Independent typed16/record hashes and five DTOs matched the
frozen Python v2 contract. The actual sink-failure-loss probe was reproduced and
fixed. A suspected partial-aggregate edge already rejected correctly and is
recorded as a passing boundary, not an additional implementation bug.

The same production helper passed root check_local: Python412, Go
complexity/vet/full race and UI532. After that run, two test-only unused
bindings were replaced while preserving factory calls and assertions. Those
two tests and pinned lint passed, and independent normal/race223 checks were
rerun against the final test hash. The earlier broad race is not reassigned to
the later test-expression bytes. Final pinned lint reported zero issues.

Final source pins:

| Source | SHA256 |
|---|---|
| attempt_observer.go | 485ef172a1d4d9e2f62abec2cda22602c3460e8a72cec6d9c90e16065fcb46a0 |
| attempt_observer_test.go | 54741fb9c3ccd5a1b7219e976ea05e992726be335bef9b50ce0efdf7c65e4350 |

The four source pins and42 protected private files were rechecked. Source
contains no private contract runtime dependency. No shared workflow/meeting/host
caller has been wired by this module phase, so the full CRW-02 card remains in
progress. Arbitrary I/O sink latency independence, authenticated sources,
exhaustive runtime coverage and real-model performance remain unverified.
No model/API/effect call, product build, release or deployment was run.

## 한국어 작업 기록

작업 ID: CRW-02.modules (#154)
변경 이유: 실제 실행 collection·명시 root·비용 역할을 구분하고 미사용 실험 helper를 별도 v2 계약으로 옮긴다.
실행한 검증: 독립 일반223/race223leaf, strict132입력·Python5DTO/hash 대조·보호42핀 확인. 동일 구현의 전체check_local 종료0(Python412/Go/UI532), 뒤의 test-only2binding 보정은 대상2·lint0·최종test핀 독립223씩으로 재검증.
미실행: 공유 호출부 연결·전체 CRW-02 수용·실제 인증/모델/효과/성능·빌드·배포.
현재 한계: 범위가 명시된 로컬 무결성·회계 helper이며 caller hash나 enum이 실행 권한을 주지 않는다.
커밋 상태: 이전8c14279d 증거는 보존하고 새 프로토콜·소스·검증 관계를 후속 단계 PR에 기록한다.
