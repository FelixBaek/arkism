# Add offline load planning and failure-preserving summaries

Task: SYS-05.spec; SYS-05.A; R8 (#152)
Research: Engineering preparation with synthetic offline observations; no service performance, scientific effect or model-quality claim.
Design: Reuse the pinned baseline I/O and existing required-suite collector. Add explicit deterministic workload plans and separately supplied received/outcome observations, with caller-supplied budgets, versioned repair-window witnesses, failure denominators and nine independent cost categories. Register the reviewed corpus once through its existing R8 owner.
Relation: adapted
Deviation: A preparation is separate from B runtime integration. The original shared positive fixture contained impossible processing chronology; preserve the old corpus and actual failures and use a separately reviewed corrected corpus. The v1 profile retains valid old within-window observations, while v2 requires explicit repair coverage; missing v2 evidence never silently falls back to v1.
Why: Prepare reproducible inputs and honest accounting before interpreting actual bounded-worker experiments. Preserve unknown, missing, timeout and failed observations instead of fabricating successful or zero-cost results.
Verified: In this exact b8-based candidate, tools173, evaluation577 and reuse49 Python methods passed (799 total). The default collector separately measured551methods1599leaf with old510 and new41 IDs each executed exactly once and0failure/error/skip/xfail/xpass. The collector replay overlaps evaluation577 and is not added to799. Frozen source6 hashes match the independently reviewed integration. Prior independent original12/60 and new11/68, new-corpus author41/116, and default-registration independent551/1599 plus11 controls remain separately pinned evidence rather than extra current-run counts. The initial tools run's2 sandbox socket errors are preserved; the same173 methods passed with authorized own-loopback execution.
Limits: Offline preparation/default registration only; SYS-05 remains in_progress and full integration is unaccepted. Whole25 verified, required24 remaining, conditional14 held. Actual daemon/queue/restore integration and same-workload/freshness/quality load measurements and complete cost reconciliation remain required. Full check_local, standalone acceptance/build, unrestricted all-Go tests, installed app, operating data, providers and scientific effects were not exercised here. No public/Claude edits, commits, pushes, product builds or deployments. Root must revalidate staged/history publication and the final composed source after its parallel changes.

## 한국어 작업 기록

제목: 오프라인 부하 요청계획·실패 보존 집계 준비
이유: 실제 서비스 비교 전에 재현 가능한 입력과 실패·미확인 값을 숨기지 않는 집계를 준비한다.
검증: 이 후보에서 Python 도구173·평가577·재사용49개, 총799개 통과. 기본 수집551메서드1599세부사례는 기존510개와 새41개를 각각1회 실행했으며 실패·오류·skip0이다. 이 수집은 평가577개와 겹쳐 별도 합산하지 않는다. 원6소스 해시와 이전 독립 검증 근거를 보존했다. 최초 소켓 권한오류2개는 보존하고 같은 도구173개를 자체 loopback 허용 범위에서 재실행해 통과했다.
한계: A 준비·R8 등록만 해당하며 SYS-05 전체는 진행 중이다. 전체25개 완료·필수24개 남음·선택14개 보류를 유지한다. 실제 서비스 부하·복원·동일조건 비교·제품 빌드·배포는 이 단계 검증 범위가 아니다. Root의 동시 변경과 합친 최종 소스 및 공개 staged/history 검사는 별도 수행한다.

## Root composition

The first composition exposed the preceding output-helper refactor's frozen codec hash conflict. That failure is retained; the original codec and dependency checks remain unchanged. After the separate parent fix moved filename validation to its CLI callsite, Root composed these same six source files with parent20ac95cf and reran the full577 evaluation tests successfully. This replay is not added to the earlier799 total. Publication audit and remote checks are tracked separately; the whole-card count remains25 with24 required remaining.

최초 통합에서 부모 출력 도구 수정의 고정 codec 해시 충돌을 발견해 실패를 보존했다. 부모의 별도 수정으로 원 codec을 복원하고 CLI 호출부에 검증을 옮긴 뒤, 동일6소스를 합친 최종 후보의 전체 평가577개가 통과했다. 이전799개와 중복 합산하지 않으며 전체 카드 완료 수는25개로 유지한다.
