# Native 상태 영속화 범위

Task: SYS-02; SYS-03; DYN-03
Research: 실제 서비스의 수락·저장·재시도 경계. 연구 효과나 기억의 인과 기여 검증이 아니다.

이 구현은 기존 `RecordMemoryOutcome`이 수락한 독립 검사 결과를 별도 native 상태에 연결한다. 기존 권한·trace·target/evidence revision·checker hash 형식·요청 재시도 검증을 재사용한다. 일반 evaluation API의 새 참조 기능을 삭제하지 않으며, 이 native 경로는 실제 outcome 생성기가 만들지 않는 spec/trial/calibration 참조를 거부한다. task/trial 컬렉션이 존재한다는 사실을 실제 outcome의 과제 결속으로 대신하지 않는다.

## 설정과 수락

`Service.NativeDynamics`를 명시적으로 설정하고 현재 승인된 기억을 별도 등록해야 한다. 기본 서비스는 이 기능을 자동으로 켜지 않는다. 설정·등록은 기억을 승인하는 동작이 아니다. 초기값·감쇠·강화 계수·시간 단위는 호출자가 명시한 검증된 설정이며 제품 기본 연구 계수를 새로 정하지 않는다.

원인 outcome을 저장하는 transaction은 수락 시각을 저장 codec으로 다시 읽고 durable capture를 함께 저장한다. 기능이 꺼졌거나 등록이 없었던 원본도 당시 상태를 기록하며, 나중의 재시도에서 과거 사건을 새 사건으로 채우지 않는다. 과거 표시가 없는 자료는 unknown이다. 클라이언트 시각으로 바꾸거나 시간을 조금 더해 순서를 맞추지 않는다.

## 한 번의 반영과 읽기

capture 처리 transaction은 numeric state·관측 seen·적격 positive 소비·event·checkpoint·capture 결과를 함께 확정한다. 같은 요청의 재시도와 새 전송 ID로 재포장한 동일 관측을 구분한다. false는 관측되었지만 강화·감점 점프를 만들지 않는다. 명시적인 값0과 값이 없는 경우도 구분한다. 설정 버전 변경으로 같은 관측을 다시 학습하지 않는다.

상태를 읽거나 반영할 때 현재 정본·숨긴 요청 hash·capture 표시·출처·실행·권한을 다시 검사한다. 먼저 준비된 DTO나 이전에 읽은 값은 계속 사용할 권한이 아니다. 이 상태는 현재 실제 outcome의 native identity를 사용하며 고정 오프라인 DYN 계약의 합성 Task/Trial 자격으로 이름만 바꾸지 않는다.

## 삭제와 복구의 경계

출처 또는 기억을 삭제하는 소유 transaction은 즉시 무효화 표시와 정리 cursor를 남긴다. 물리 정리는 별도 제한된 페이지 transaction으로 진행하고 페이지와 cursor를 함께 확정한다. 기능이 꺼져도 기존 자료 정리는 계속한다. 무효화된 오래된 상태를 재시도로 살리지 않으며 native 정리가 남았으면 기존 삭제 부모의 완료 표시를 막는다.

API에서 숨긴 것과 payload·검색 문서의 실제 제거는 다른 검사다. 이 구현의 정리 완료를 모든 외부 저장소·백업의 삭제 완료로 해석하지 않는다. 한 페이지에서 받은 행 수는 DB가 물리적으로 읽은 모든 행·IO의 상한이 아니다.

## 검증 상태

이 단계의 고정 통합 기준은 main `a389082b77cc57f45c174a561cb443a2d533e83f`와 검토된 로드맵 선행이다. 작성자 격리 검사와 root의 선택 회귀 결과는 작업 기록에 구분한다. 새 소스의 독립21메서드·93세부사례가 일반/race 모두 통과했고, 실제 과거7e schema4와 제한 역할의 원본 자료에서 최종 schema5로 올리는 검증도 통과했다. 원본 bytes·수락 시각·hash·legacy unknown을 보존하고 과거 사건을 채우지 않는지 확인했다. 호스트 checker 원본 bytes/hash와 서버 형식 검증은 별도로 검사했다.

실제 Search/HTTP shadow 연결, 표시·저장·읽기·답변 단계의 전체 증거 연결, 삭제 후 retained-data 재구축·새 프로세스 복원·최종 게시 fence는 이 저장 구현만으로 완료되지 않는다. SYS-02는 기존 A 명세와 이 native 프로토콜의 실제 B 격리 수용 기준을 충족했다. 고정 합성 DYN에 없는 Task/Trial 결속을 만들지 않았으며 이전 A의 metadata-only 설명을 현재 durable B의 미구현으로 해석하지 않는다. SYS-03 전체 완료나 제품 빌드·배포 수용은 별도다. 원자성 모델 검사·실제 격리 DB 검사·전체 카드 수용을 구분한다.

Local SYS-01/SYS-03/SYS-04 integration is now accepted in the pinned latest-main candidate; see [delivery and lifecycle](native-delivery-lifecycle.md) for exact reader, recovery, trust and test boundaries. Default feature enablement and deployment remain separate.
