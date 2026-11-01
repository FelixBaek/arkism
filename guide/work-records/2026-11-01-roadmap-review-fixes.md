# Roadmap delivery review fixes / 전달 리뷰 보완

Task: PR #534, #150–#158 delivery; RTE-05, EXP-02/03/04
Research: Engineering validation only; actual-material and scientific acceptance holds remain.
Design: Estimate answer output after structured-event extraction and final answer-file selection; preserve valid partial text on interruption without counting raw tool diagnostics. Keep all invocation-count, unknown-cost and no-duplicate-effect boundaries. Classify evaluation Python tests as tests. Review every security source/sink before exact file/rule dispositions.
Relation: partial
Deviation: The local CLI has caller-selected filesystem capabilities; it is not a privileged HTTP or model-facing service. Test-image startup pacing is a synthetic cancellation fixture, not product performance or a product delay.
Why: Actual PR review found raw JSONL inflated lexical answer/cost estimates. Actual covered interop exposed Go test-image exit-hook diagnostics and timing assumptions; source guards correctly rejected unexpected stderr. Sonar source configuration omitted evaluation Python tests.
Verified: Initial new cost regression failed for Claude/Codex: wire token counts677/2554 versus answer-only3. The corrected output-file regression makes stdout draft much longer than the actual file answer and asserts both returned answer and estimated usage. Focused budget/observation race passed; final whole-host race passed in114.043 seconds with local test sockets. Evaluation820 tests passed. Complete covered native command package passed in41.859 seconds with69.7% statement coverage. Pinned lint0issues before a final test-only input-strengthening; remote CI/AI re-review remain pending. Independent Standards and Spec reviews have0remaining mandatory findings.
Limits: Preserve failed initial/midway evidence. Initial whole-host sandbox attempt was blocked by localhost socket permission and is not a code pass. The rerun authorizes only local test sockets. No product build, deployment, paid provider, operating database or actual study.

코드 리뷰 수정: CLI 실행 이벤트·tool 인자·중복 메시지가 최종 답변 토큰으로 계산되지 않도록 파싱 뒤 추정한다. Codex의 별도 최종 답변 파일이 실제 응답을 바꾸면 그 답변으로 추정을 갱신한다. 타임아웃에서 유효한 부분 답변은 추정으로 남기며 provider-reported 값으로 승격하지 않는다. 가격은 여전히 추정이고 실제 전체 제공자 청구액을 검증한 수치가 아니다.

커버리지 호환: 테스트 전용 TestMain의 CLI 진입에서 실제 시험 실행파일 부모를 GOCOVERDIR로 사용한다. 현재 Go/Python 시험 구동기는 그 실행파일을 자체 임시 폴더에 두고 정리한다. 사용자가 별도로 시험 실행파일을 영구 경로에 컴파일했다면 그 명시 경로에 coverage 자료가 남을 수 있다. 제품 코드나 verifier의 stderr0 조건은 바꾸지 않았다. 시험 이미지 run 진입에만250ms를 두어100ms의 실제 child 대기·kill·reap을 검증한다. 이는 native publication 시작 전의 실제 child timeout이며, 실제 중간 publication kill이라고 주장하지 않는다. 부분 산출물 archive 보존은 별도 기존 단위회귀로 검증한다. 이 시험 지연은 실제 start/end 비용 기록에 포함되며 과학·성능 결과로 사용하지 않는다.

Sonar 보안53건을 각각 source→sink→guard로 독립 두 축 검토했다. 실제 평가 test_*.py42건은 Go-owned temp tree, 현재 시험 실행파일, 정적 Python script/list argv(shell 없음), 변조·경로·반향거부 음성 시험의 고정 비민감 sentinel에 관한 항목이다. 테스트 본문·negative assertion을 삭제하지 않고 evaluation/**/test_*.py를 source exclusions와 test inclusions 양쪽에 정확하게 분류했다. source 전체나 보안 규칙 전체를 제외하지 않았다.

제품측 local CLI9개 경로 finding과 재현 가능한 bootstrap PRNG2개는 아래 정확한5개 파일·규칙으로 한정해 기존 저장소의 근거 있는 예외 방식을 따른다. 임의 보안 경고를 일괄 무시하는 설정이 아니다. 새 HTTP/권한 높은 서비스에 이 프로그램을 연결하려면 별도 호출자 제한 검토가 필요하다.

| 파일 | 규칙 | 개별 검토 근거 |
|---|---|---|
| experiments/exp_03/analyze_paired.py | pythonsecurity:S8707 | 명시적 local caller가 출력 경로를 선택하며 기존 파일은 exclusive 생성으로 덮어쓰지 않는다. bounded regular-file 읽기는 최종 symlink를 거부한다. parent 전체가 dirfd로 격리된다고 주장하지 않는다. |
| experiments/exp_03/locked_analysis.py | pythonsecurity:S8707 | 검증된 절대 경로 구성과 dirfd/no-follow 부모 순회, exclusive 생성·inode 확인 정리를 유지한다. |
| experiments/exp_04/build_evidence_index.py | pythonsecurity:S8707 | artifact 상대 경로는 dot/dotdot/빈요소·역슬래시·NUL/control을 거부한다. dirfd/no-follow·regular/크기 제한·0700 새폴더·0600 exclusive 파일·inode 확인 정리를 유지한다. 출력 이름은 render의 고정4개다. |
| experiments/exp_03/analyze_paired.py | python:S2245 | 고정 seed의 재현 가능한 bootstrap 표집이다. 인증키·보안 토큰·권한 결정에 사용하지 않는다. |
| experiments/exp_03/attribution_views.py | python:S2245 | 동일하게 재현 가능한 bootstrap 표집이며 비밀값 생성과 무관하다. |

위 표의 experiments는 evaluation/memory-harness-v1/experiments 아래다. 소스 경로·원문·생성 seed·검증 순서와 실제 권한/기준 hash는 유지한다. Sonar 자체 통과 여부는 #534 최신 head CI가 최종 근거이며, 이 검토 문서 자체를 통과로 세지 않는다.

Latest-main update before publication: upstream advanced to ea7bb9cb753a33273265bf02e577da157000334b with only desktop/frontend/package.json and package-lock.json security pins. Signed rebase completed without conflicts; tree comparison against the tested review-fix source differed only by those two upstream dependency files before the delivery-status documentation update. Remote CI and AI review must run on this new head; predecessor local results are not relabelled as new-head full results.
