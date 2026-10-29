# SYS-03.spec A: 원자성 모델과 원장 대사

이 다섯 파일은 `synthetic_model` 범위의 실행 가능한 명세다. 제품 저장소 어댑터,
실제 DB 원자성, 권한 인증, 운영 exactly-once 또는 SYS-03 전체 완료를 뜻하지 않는다.
SYS-02 A의 고정 변환기를 원문 SHA로 확인한 뒤 읽기 전용으로 재사용한다.
SYS-01 Scope/full ref/clock과 DYN의 Event/Evidence canonical projection도 그 의존 경로로
그대로 재사용하며 기존 registry나 계약 bytes를 추가·수정하지 않는다.

`fault_schedules.json`은 독립 입력 역할의 외부 계획 observation과 장애 command,
수기로 정한 상태·비용 기대값을 보유한다. `replay`는 그 계획을 실행하는 선언 모델이다.
`validate_ledger(external, internal)`은 외부 계획에서 내부 주장을 다시 검산한다.
내부 원장은 각 command의 순서·request·event·payload·exact key·근거 identity/claim·
accepted sequence·durable receipt·consumption receipt·commit·revision·checkpoint·상태를
전부 대조해야 한다. 마지막 state hash가 같더라도 누락·중복 event/receipt를 허용하지 않는다.
정상 원장을 모델로 생성한 검사 자체는 독립 실행 증거가 아니다. 별도의 수기 상태/비용
기대값과 내부 원장 변조 음성 검사를 함께 실행한다.

단계는 다음처럼 분리된다.

- `accept`: 검증된 모의 event의 휘발성 수락이다. 아직 durable 책임을 주장하지 않는다.
- `durable`: TxD가 event receipt를 보존한다. state/consumption은 아직 바뀌지 않는다.
- `commit`: 하나의 선언된 TxA 안에서 state chain/revision, event application receipt,
  request 결과, 적격 논리 근거 consumption, checkpoint를 함께 확정한다.
- `publish`: commit·current key·writer epoch·revision·checkpoint를 확인한 뒤에만 query-visible이다.

TxD 후 장애는 미적용 pending으로 재시도한다. TxA 후 응답 유실이나 worker crash는 원래
commit/consumption receipt를 반환한다. 같은 event ID의 다른 payload 또는 최초 수락 좌표는
충돌이며, 같은 논리 근거의 다른 event ID는 별도 event receipt만 만들고 재학습하지 않는다.
소비 저장 슬롯은 `(exact DynamicsKeyV2, frozen EvidenceIdentityProjectionV2 SHA)`다.
새 hash를 고정 DYN identity인 것처럼 사용하지 않는다. 현재 모델의 state hash는 own domain의
**event-chain commitment**이고 실제 숫자 reducer state가 아니다. 새 학습 계수나 시간상수는 없다.

`semantic_updates`는 적격 첫 논리 근거의 반영 수이며 수치가 달라진 row 수가 아니다.
NOOUT/eta=0처럼 숫자 점프가 0이어도 고정 DYN 계약상 적격 근거 소비는 일어난다.
state/application receipt/consumption/checkpoint/durable writes와 DB transactions/flushes는
request 결과 write와 DB 전체 record write까지 각각 분리한다. 이 모델에서 성공한 Tx당 flush 1은 **선언된 장애 일정의 비용 단위**이며,
물리 DB flush 측정이나 운영 설정값이 아니다. 실제 수치 reducer 실행·DB 계측은 B 입력이다.

표시·서버 반환·packing·reader 포함·actual use·outcome은 서로 다른 관측이다.
같은 event/key·presentation_id·reader/path에서 packing 제외면 reader 포함 receipt를 만들 수 없다.
새 presentation_id는 별도 관측 주기이며 과거 reader 사실을 이번 사용 증거로 물려받지 않는다.
같은 주기·단계의 동일 관측은 중복, 다른 boolean은 presentation_conflict다.
과거의 유효한 읽기 사실은 삭제하지 않으며, 다른 주기의 현재 사용 증거와 구분한다.
다른 reader/path의 기여는 별도로 관측해야 한다. `outcome`은 use 증명이 아니다.
현재 source inventory에는 검색의 최종 Tx usage receipt, pinned injection 별도 receipt,
crew source read/briefing/summary handoff/turn input, outcome evaluation, persisted signal과
삭제 의존경로를 SHA와 원문 위치로 기록했다. 모든 미래 숨은 지속 경로를 증명한 목록이 아니다.

현재 native API에는 fixed DYN의 task/trial/processing sequence/real qualification이 없다.
SYS-02 B prepared intent나 accepted-ref 검증을 durable event/committed learning으로 바꾸지 않는다.
Native observation은 `native_qualification_unimplemented`로 남고 commit·consumption 비용은 0이다.
새 native logical evidence protocol은 선택하지 않았다. 필요하면 owner가 원천·손실·namespace를
검토한 별도 계약을 먼저 승인해야 한다. `synthetic_independent`로 native를 위장할 수 없다.

통합 후 검사:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s evaluation/memory-harness-v1/system/sys_03 -p 'test_*.py' -v
```

비공개 후보만 검사할 때는 `PYTHONPATH`에 SHA가 일치하는 기존 harness 디렉터리를 명시한다.
모듈은 고정 상대경로의 dependency bytes를 검사한 후에만 SYS-02 코드를 불러온다.
검사는 DB/API/provider/model 호출이나 Go 제품 빌드를 수행하지 않는다.

B에 남은 것은 저장 방식 ADR, 실제 accepted evaluation와 durable enqueue의 동일 transaction,
실제 state/consumption transaction, writer/CAS/epoch 구현, 새/upgrade 격리 schema,
실제 commit 경계 fault injection·race·삭제/권한 source fencing이다. A PASS로 대체할 수 없다.

후속 검증은 1e309 같은 지수 overflow를 decode 경계에서 거절하며, 직접 주입된 비유한 관측도 고정 LedgerError로 반환한다. 원 후보와 독립 발견은 별도로 보존했다.

한 presentation은 동결된 관측 주기다. 정당한 재packing·새 관측은 새 presentation_id를 쓴다. 같은 라벨도 event/key/reader/path가 다르면 별도 주기다. 이 규칙은 A 모델의 관측 단위를 정의하며 실제 native 모든 경로에 이미 강제된다는 뜻이 아니다.

관측 주기는 원 payload SHA·accepted sequence·accepted 시각에도 결합한다. 같은 논리 event/cycle의 payload나 수락 좌표가 달라지면 event_conflict이며 과거 reader를 상속하지 않는다. outcome 단계도 동일 관측 중복·상반 관측 충돌을 따르되 사용 증명으로 해석하지 않는다.
