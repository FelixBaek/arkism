# Revalidate declared context before reuse in meetings

Task: CRW-03 (#154)
Research: E engineering adaptation; no effectiveness or novelty result
Design: Existing scoped Record reads, accepted context helper, native meeting lease/budget/trace paths
Relation: adapted
Deviation: Optional observe/enforce integration. Native semantic changes block; refresh/restart remain explicit required plans until a protected new segment or run exists. No server schema or endpoint is added.

The Client now implements the optional receipt reader using existing scoped
source and current-team reads. Exact ID/scope/CAS, selected UTF8 content and
semantic guards remain separate. Current source state, expiry, erasure,
applicable approval and current team membership/sharing are checked without
inventing new approval requirements for supported legacy records. A successful
history GET is not current-use permission. Unsupported metadata or readers
remain unknown. Knowledge-document listing metadata is not an exact body.

The existing meeting driver reads versioned source material before briefing,
then rechecks every declared dependency before each primary/fallback call and
after a return before accepting its output. It also rechecks before candidate
capture. Invalid UTF8 or a body differing from the reader's declared digest
cannot create fresh lineage. Deletion/revocation blocks the whole dependent
cached transcript, rather than merely hiding its source excerpts.

Source-read material, the exact Task sent to the model, the actual persisted
briefing and later Task inputs have separate receipt stages and hashes. The
summary reference is an actual returned meeting snapshot containing that body;
a turn sequence is not a canonical revision. The native secret scrub is
preserved and its stored body is what the summary receipt hashes. Concurrent
unobserved model turns merged during a save cannot silently become protected
lineage. A resumed stored brief without protected lineage blocks enforcement.

Default off adds no source reads. Observe mode reports incomplete evidence
without failing the work because a collector sink failed. Explicit enforcement
uses current source/policy evidence separately from delivery diagnostics.
Discord filtering still excludes memories and nonshared sources before any
read. Existing FLOW control failures retain their native classification and
are not replaced by a later freshness diagnostic.

Verification: Client author race44leaf and independent normal/race49
leaf each; meeting author race14leaf and independent normal/race22leaf each.
The independent meeting baseline reproduced19 failing boundaries in the old
implementation. Further actual tests reproduced untracked merge promotion and
candidate capture after revocation; both now reject. Source/summary/Task hashes,
scope, fallback, post-return, Discord, native scrub and sink isolation were
checked with local fakes. Final `python3 tools/check_local.py` passed: Python412,
Go complexity/vet/full race and UI532. Pinned golangci-lint v2.14.0 returned
0 issues. All six integration/module source fingerprints match the independent
review. All five card criteria are accepted for this local fake integration;
live execution and research effects remain outside that acceptance.

Limits: checks are point-in-time reads, not atomic binding to later provider
transmission or target commit. Already sent material cannot be recalled by this
client gate. Stored history deletion and provider retention remain their native
privacy workflows, not a new claim here. Caller facts/hashes are not independent
authentication or verified semantic truth. No real model/provider/effect,
operational DB, product build, release, deployment or R8 standalone gate ran.

This is an adaptation of admission and explicit premise revalidation, with
attribution to [TRACE](https://arxiv.org/abs/2609.33517) and
[ATR](https://arxiv.org/abs/2609.08015); it is not their full reproduction or a
claim that return-time admission alone is novel.

## 한국어 작업 기록

작업 ID: CRW-03 (#154)
변경 이유: 기존 조회 API와 회의 흐름을 재사용해 원본·요약의 정확한 계보와 현재 사용 가능 근거를 구분한다.
실행한 검증: Client 소유race44leaf/독립일반·race49leaf, 회의소유race14leaf/독립일반·race22leaf. 기존 구현19실패, 동시 병합·capture 직전 철회·본문 불일치 등의 실제 재현과 수정 확인.
현재 상태: 최종 check_local(Python412/Go 복잡도·vet·전체race/UI532) 및 고정lint0issues 통과. 독립 검토와6소스핀 대조 뒤5완료 기준을 로컬 fake 통합 범위에서 수용했다. 전체 로컬 완료13/63.
한계: read/use 비원자성·자연어 의미정정 자동검증·실제 refresh/newrun·과거 본문 삭제·provider 회수·인증/성능·실제 모델/DB·제품 빌드·배포·R8 standalone은 검증하지 않았다.
