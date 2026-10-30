# read the work checkpoint before the routing selector

Task: RTE-03 (#155)
Research: Engineering ordering fix in the existing crew routing path; no routing quality, cost or experiment claim.
Design: runRouted reads the selected work checkpoint once before calling the selector. A failed read stops before any routing decision or provider call. The selector still receives only the task and group; the checkpoint text reaches the chosen model and every fallback as data, unchanged.
Relation: adapted
Deviation: Only the crew ordering step of the isolated integration proposal. No metadata-only preselection endpoint, server-bound decision id, reader/fence receipt or snapshot hash is added; the shared host.DecideRoute signature and Gateway routing stay unchanged.
Why: The crew selected a model before it had observed the checkpoint it was going to run with, and a missing checkpoint was only detected after a routing decision had been made. Observing first keeps selection inputs to what existed at decision time and makes a failed read abort before selection.
Verified: New test TestCheckpointIsReadBeforeSelectionAndNeverReachesSelector checks the order checkpoint, decide, run and that the selector gets the bare task; the missing-checkpoint test now also asserts the selector is never called. With the previous routing.go both tests fail; with the change they pass. The whole internal/host/crew package passed, also under -race; go vet and golangci-lint v2.14.0 reported 0 issues.
Limits: Whole RTE-03 is not accepted. Metadata features, presented-view hashes, revision reconciliation after selection and actual reader/fence receipts are still missing from the product path. Full check_local and unrestricted go test ./... were not run. No build, deploy or production change.

## 한국어 작업 기록

제목: 모델을 고르기 전에 작업 체크포인트를 먼저 읽기
이유: crew는 모델을 먼저 고른 뒤에 실행할 체크포인트를 읽었다. 그래서 체크포인트가 없어도 선택이 먼저 일어났다. 이제 먼저 읽고, 읽기 실패면 선택 전에 멈춘다.
검증: 새 시험이 순서(체크포인트→선택→실행)와 선택기에 과제 글만 가는지 확인한다. 체크포인트 없음 시험은 선택기가 불리지 않음도 확인한다. 옛 코드에서는 두 시험이 실패하고 새 코드에서 성공한다. crew 패키지 전체(일반·race), vet, lint 0건.
한계: RTE-03 전체는 미수용. 메타데이터 feature·제시 view 해시·선택 후 revision 대사·실제 reader/fence 영수증은 아직 제품 경로에 없다. 전체 check_local·무제한 go test 미실행. 빌드·배포 없음.
