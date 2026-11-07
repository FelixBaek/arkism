# 저장·복원·삭제·운영 추가 점검

작업 ID: MEM-IMPROVE-20261107-AUDIT
연구: 해당 없음
기준: 2d16d306. 이 보고서는 정적 코드·기존 시험 검토이며 실제 대규모 부하나 운영 환경을 시험한 결과가 아니다. 기존 11개 구현 범위와 별도인 추가 개선 후보다.

## 조사 범위와 기존 보호

| 영역 | 조사한 구현 | 기존 확인 가능한 시험/계약 |
| --- | --- | --- |
| 사용자 삭제 및 파생 데이터 | privacy_deletion.go, privacy_postgres.go, privacy_overlap.go, privacy_runner.go | privacy_store_postgres_test.go의 범위 제한·영향 과다 거부·부분 삭제 검증·백업 대기 |
| 저장소 보존 정리 | audit_retention.go | audit_retention_test.go의 미리보기·과거 행만 삭제·소유자/토큰 제한 |
| 개별 기억 복원 | memory_restore.go | 승인·출처·관계·revision·만료·격리 검증 후 복원 |
| 파일 복원 및 공개 재개 | blob_restore_linux.go, blob_restore.go, restore_publication.go | blob_restore_linux_test.go, restore_publication_test.go, backup_postgres_roundtrip_linux_test.go |
| 공간 이동·가져오기/내보내기 | space_stream.go, http_core_spaces.go | spaces_test.go의 출처·후보 상태·범위·삭제·변조·전송 권한 |
| 권한과 외부 전송 | http_authorization.go, postgres_authorizer.go, transfer.go | 등록별 권한 정책 강제, actor 일관성 확인, 출처 민감도와 외부전송 gate |
| 작업 큐 및 운영 조작 | search.go WorkOnce, ops_jobs.go | 임대·FinishJob·재시도, 개인정보 큐 공정성, 작업종류별 운영 조작 허용 목록 |
| 상태·관측·확장 경계 | http_core_health.go, extensions.go | 상태 API와 권한 있는 metrics, 확장 거절·panic containment 시험 |

위 표는 대표 경로를 읽은 범위다. 패키지 내 모든 분기나 운영 데이터에 대한 검증 완료를 의미하지 않는다. 최초 inventory에는 internal/memory 비시험 Go 파일 421개가 있다. 다른 담당의 전 영역 조사와 합쳐 범위를 추적한다.

## A01. 큰 삭제 요청의 중단·재개와 진행 상태

확인: privacy_deletion.go:14의 영향 한도는 1,000이다. RequestPrivacyDeletion은 영향 탐색을 한 트랜잭션에서 완료한 뒤 격리한다. privacy_postgres.go:12 이하의 탐색은 고정점 반복 횟수·출처/trace·파생 레코드 수 한도를 초과하면 ErrUnavailable로 거부한다. 기존 privacy_store_postgres_test.go:195와 consultation 범위 시험은 불완전한 영향 집합을 승인하지 않는 계약을 고정한다.

기회: 큰 사용자 이력도 삭제를 진행할 수 있도록, 새로운 읽기/쓰기 접근을 차단하는 범위 fence 아래에서 페이지별 영향 탐색과 삭제를 재개 가능한 작업으로 설계할 수 있다. 현재 안전 한도를 단순히 올리거나 영향 집합을 잘라 완료 처리하는 방식은 배제한다. 범위 fence와 겹친 삭제·보존 hold·백업 대기 의미를 먼저 고정해야 한다.

검증: 999/1,000/1,001개 및 많은 관계를 가진 격리 자료로 요청→중단→재개→쓰기 경합→최종 누락 없음 검증. 중간 상태에서 다른 scope에 접근하지 않고 삭제 대상이 다시 검색되지 않아야 한다. 현 한도에서 안전하게 거부하는 기존 동작도 유지한다. 실제 큰 자료에서 실패 빈도나 개선 속도는 미측정이다.

## A02. 보존 기간 정리의 bounded batch

확인: audit_retention.go:22의 expireCollection은 전체 해당 행 count 후 조건에 맞는 전체 행을 한 DELETE로 지운다. 소유자 권한, 최소 하루 이전 cutoff, preview/execute 구분은 이미 있다. audit_retention_test.go는 기능·권한을 검증하지만 큰 삭제의 lock/WAL/검색 지연을 측정하는 시험은 아니다.

기회: 고정 cutoff와 stable key/cursor로 제한된 batch를 지우고 재개 지점·삭제 수를 보고한다. 기준 시각이 달라져 새 감사기록을 지우지 않도록 해야 하며 dry-run과 실행 수는 시점 차이 때문에 다를 수 있음을 표현한다.

검증: 격리된 DB의 큰 감사/호출 이력에서 동일 최종 삭제 집합, 중단 재개, 동시 새 쓰기 보존, 권한 거부, DB lock 및 검색 지연 측정. 현재 운영 DB에서 정리 명령을 실행하지 않았다.

## A03. 권한 있는 상태 진단에서 실패한 구성요소 식별

확인: http_core_health.go:58의 readyz는 검색 준비와 blob 검사 중 하나라도 실패하면 일반 unavailable이다. metrics는 권한을 검사하고 indexes_ready를 노출하지만, 이 endpoint에서 blob/복원 공개 대기 등 구성요소별 원인을 함께 분류하지 않는다. healthz 버전·기능 조회, 일반 readiness, metrics 자체는 구현되어 있다.

기회: 무인증 readiness의 단순 계약은 유지하면서 권한 있는 운영 진단에 검색 인덱스/파일 저장/복원 공개 대기 등의 제한된 상태 코드를 추가한다. 원본 경로·인증값·개인 자료를 노출하지 않고 일시 실패와 운영자 조치가 필요한 대기를 구분한다.

검증: 구성요소별 fake 실패와 격리 복원 대기에서 상태 분류, 권한 거부, 제한시간, 자격증명 비노출 확인. 새 진단 조회가 장애 시 요청을 쌓거나 복원 fence를 우회하면 안 된다. 현재 운영 장애가 있다는 주장이나 개선 수치 측정은 하지 않는다.

## 아직 추가 과제로 확정하지 않은 것

작업 큐 재시도·운영 취소, 확장 hook 방어, 공간 스트리밍과 manifest 검사, 복원 공개 차단은 이미 있는 기능이다. 이번에 읽은 경로만으로 새로운 결함을 확정하지 않았으며 기존 기능을 다시 구현 대상으로 세지 않는다. 복잡한 권한·삭제/복원 경합의 전체 증명이나 신규 침투시험은 수행하지 않았다.


후속 구현·검증 상태는 [추가 개선 22개 기록](2026-11-07-memory-followups.md)을 따른다.
이 문서의 지적 사항은 구현 전 점검 결과이며, 측정 자료가 필요한 보류와 구현 완료는 후속 기록에서 구분한다.
