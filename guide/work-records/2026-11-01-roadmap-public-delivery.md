# Public main roadmap delivery / 공개 main 로드맵 전달

Task: #150–#158 roadmap delivery and PR catalogue
Research: Engineering delivery only; actual-material acceptance remains held.
Design: Apply reviewed owned changes to a fresh public clone, preserve latest-main session delivery, compare every overlapping API leaf, preserve frozen baseline bytes and all authority/revision guards.
Relation: partial
Deviation: One integrated PR carries three functional commits. Existing original PRs were integrated through #526; their individual open state is not proof that their code is missing.
Why: Deliver the authorized implementation against current public main and record every PR functionality, expected benefit, actual validation and limit separately.
Verified: Latest public base is 15edfca69efa4565592b1f355a3cd5063d6fa4df. API three-way semantic merge preserves 289 paths and 562 schemas. Current-public candidate lint reports zero issues. Current-public whole-tree tools/check_local.py passed with exit0 in1246.806 seconds: Python179/820/49, Go vet and whole-tree race, frontend591 passed with0failed/0skipped. All2970 pinned source files were unchanged. Own disposable PostgreSQL stopped0 and its directory was removed. Some unchanged Go packages use cache; individual Go skips were not separately counted. The product build checks were disabled. Existing frozen baseline bytes are unchanged. Current remote CI and AI review remain pending. Commit-range work-record check passed for the first two signed commits.
Limits: No product build, deployment, operating database or paid provider call. Synthetic/native functional evidence is not scientific efficacy or actual participant/model delivery. Current required CI and AI review are required before merge.

기능·기대 개선·근거·한계는 guide/roadmap/pr-delivery.md에 기록했다. 새 전달 기능과 이미 병합된 #526–#529 및 통합 원본 PR 51개를 각각 구분한다. 기대 개선을 측정된 성능이나 과학 효과로 표현하지 않는다.

이슈 종료는 병합 후 완료 기준에 따라 수행한다. #151·#152·#154·#155는 조건부 종료 후보이고 #150·#153·#156·#158은 열린 상태를 유지한다. #152의 선택적 DYN04 미선택과 #155의 오프라인/가짜 파일럿 범위는 명시한다. 삭제된 #157은 완료로 세거나 임의로 복구하지 않는다.

현재 승인 범위는 개발·검증·PR·라벨·검사 후 병합이며, 실제 자료가 필요한 보류는 유지한다. 공개 정책 검사는 기존 공개 객체의 정확한 동일성을 검토한다. 사용자는 이미 공개된 파일 한 개의 동일 경로·동일 원문 상속만 예외로 승인했다. 원 검사 finding은 보존하고 다른 자료·경로에는 예외를 확장하지 않는다. 검사 프로그램과 비공개 규칙은 수정하지 않는다.

기존 작업기록의 미커밋·미공개 표시는 원 개발 시점의 상태다. 이번 전달 상태는 이 기록과 PR catalogue 및 GitHub의 실제 CI/리뷰/병합 기록을 기준으로 한다. 전체 검사 중 코드 bytes는 불변이고 Git 커밋 메타데이터만 이동했다; 검사는 -buildvcs=false를 사용했다. 검사 종료 뒤 변경은 공개 전달 문서뿐이다.
