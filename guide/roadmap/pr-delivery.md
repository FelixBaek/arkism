# 로드맵 PR 기능·기대 개선·검증 기록

이 문서는 #150–#158 관련 공개 PR의 기능과 기대 개선을 정리한다. 기대 개선은 설계상 목적이며, 별도 실측 자료가 없으면 검색 정확도·성능·비용 감소가 입증됐다는 뜻이 아니다. 검사·준비 문서·정상 no-change 결과를 새 제품 기능으로 세지 않는다.

## 이번 통합 전달

이번 통합 PR은 최신 공개 main 위에 기존에 검증한 로컬 변경을 올린다. 서로 연결된 실행기·오프라인 실험·검색 변경을 같은 코드 상태로 검토한다. 공개 PR 링크·최종 CI·병합 결과는 아래 전달 상태에서 갱신한다.

| 기능 | 사용자/운영 관점의 기대 개선 | 확인한 동작과 한계 |
|---|---|---|
| 실행기 공통 tool/time/retry 예산과 응답 시작 보호 | 과도한 재시도·중복 실행·응답 도중 모델 전환 위험 감소 | 알려진 CLI 보고는 사후 집계다. 숨은 provider 작업까지 선제 제한한다는 뜻은 아니다. 기존 FLOW 예약을 재사용한다. |
| 호출 횟수 미확인 CLI 실행 전 거부 | 횟수를 모르는 실행이 예산 검사를 우회하지 않게 함 | Grok/Gemini/agy CLI처럼 현재 event adapter가 없는 종류는 tool_call_count_unsupported로 거부한다. 실제 실행0이며 지원되는 설정된 다음 경로로 넘길 수 있다. tool-free HTTP 대화 경로와 구분한다. |
| 원자료→native 입력→실행 계획→기록→검산 | 같은 입력·정책의 실행과 실패 원인을 재현하기 쉬워짐 | 원래 DTO/codec/고정 hash는 유지한다. 정답은 순위 입력에 주지 않는다. 실제 연구 자료의 진위를 새로 보증하지 않는다. |
| 재시도별 불변 출력 보관·전체 비용 원장 | 중복 결과 선택이나 실패 비용 누락으로 비교가 유리해지는 문제 감소 | 먼저 완료된 시도만 주 분석에 쓴다. 정답인 시도만 고르는 규칙이 아니다. 실패·중복 비용은 모두 보존한다. |
| 최종 잠금·독립 검토 원문 pin·REF·작업 채점 연결 | 다른 입력이나 검토 자료가 최종 분석으로 둔갑하는 위험 감소 | hash는 원문 식별이며 사람/과학적 승인·등록 시점 인증이 아니다. |
| 같은 실행에 대한 기준별 민감도와 후속 evidence 보고 | 기준을 바꿨을 때 결론이 얼마나 달라지는지 추적 가능 | 추가 실행/독립 표본으로 세지 않는다. 결측 view 전체를 계산 불가로 남기며 추가 view는 항상 탐색 분석이다. |
| 일반 dynamic 기억 검색의 명시 설정 연결 | 제공된 native 상태에 따른 순위를 실제 검색 반환에서 시험 가능 | 기본 off, shadow는 기존 반환. 기존 권한·상태·시각·최종 fence·fallback 유지. 정확도 향상은 별도 실자료 검증이 필요하다. |
| 실제 검색→host 예산→receipt→독립 checker→상태→다음 검색 연쇄 | 개별 helper 검사에서 놓칠 수 있는 연결 누락 검출 | 실제 PG/HTTP와 로컬 작업을 사용한다. 자동 capture hook idle·연속 publisher 부하·실제 모델 전달·과학 효과를 검증한 것은 아니다. 새 기능 수로 세지 않는다. |

검증 출처: 로컬 개발 snapshot의 전체 check_local 850.659초, Python179/820/49, frontend591, 실제PG memory470.601초·daemon35.117초·host105.167초. 이후 시험1개를 추가하고 daemon 전체65개(통합10개) race41.865초·lint0을 확인했다. 최신 main 반영 후 공개 후보 검사는 별도로 실행하며, 이 이전 결과를 새 main/원격 CI 통과로 바꾸어 쓰지 않는다. [정책·실행 기록](../work-records/2026-11-01-roadmap-policy-serving-cli.md).

## 이미 main에 병합된 통합·제품 연결 PR

| PR | 기능 | 기대 개선 | 검증/한계 |
|---|---|---|---|
| [#526](https://github.com/FelixBaek/arkism/pull/526) | 아래 원본51개 PR의 단계별 내용과 통합 보정 | 여러 작업을 같은 main 기준에서 검토·실행 가능 | GitHub merged 확인. 원본 PR을 개별 병합했다는 뜻은 아니다. 배포·실험과 구분한다. |
| [#527](https://github.com/FelixBaek/arkism/pull/527) | opt-in 라우팅 귀속 기록·권한 있는 paginated export·pilot gate | 실제 선택·시도 기록을 분석 자료와 연결하기 쉬워짐 | GitHub merged 확인. 전송은 최선 노력이며 실제 pilot 효과를 입증하지 않는다. |
| [#528](https://github.com/FelixBaek/arkism/pull/528) | 기존 skill 서버 경로와 다섯 비교군의 replay 검사·no-change 기록 | mock만으로 놓치는 pin·권한·checker 경계 검출 | GitHub merged 확인. 제품 코드·UI 변경 없음. 실제 skill-use 효과는 별도 자료 필요. |
| [#529](https://github.com/FelixBaek/arkism/pull/529) | fast/slow 상태를 explicit shadow/on 검색에 연결 | 두 시간 규모의 값을 실제 순위 경로에서 비교 가능 | GitHub merged 확인. 기본 off이며 당시 dynamic은 거부했다. 이번 전달의 일반 dynamic과 구분한다. 계수·효과 수용은 별도. |

## #526에 포함된 원본 PR별 기록

아래 포함 관계는 #526의 공개 Included pull requests 목록에 근거한다. 원본 PR의 merged=false와 내용의 통합 반영은 다르다. 원본 중 일부는 뒤 단계가 같거나 더 넓은 내용으로 포함한다. 원본 PR은 다른 개발 브랜치를 base로 둔 경우가 많으므로 별도로 다시 병합하지 않는다. 각 링크의 Evidence는 그 작성 시점의 기록이며 현재 HEAD의 재실행 결과와 혼합하지 않는다.

| 원본 PR | 기능/변경 | 기대 개선 | 검증 범위·한계 |
|---|---|---|---|
| [#417](https://github.com/FelixBaek/arkism/pull/417) | 고정 기준 계약·파일 소유권·연구 기록 검사 준비 | 서로 다른 구현이 같은 기준과 수정 경계를 사용하도록 한다 | 준비·계약이며 제품 기능 효과 아님. 원 PR Evidence 참조. |
| [#418](https://github.com/FelixBaek/arkism/pull/418) | 엄격한 기준 DTO·codec와 정확한 지수 감쇠 | 잘못된 자료·숫자 표현을 초기에 거부하고 계산을 재현한다 | 고정 기준 구현. 원 PR Evidence 참조. |
| [#419](https://github.com/FelixBaek/arkism/pull/419) | 합성 자료·참조 순위·실패 보존 지표·오프라인 CLI | 실패가 빠지거나 참조 순위가 서로 다른 비교를 줄인다 | 후속 단계가 포함·확장한 내용. 원 PR Evidence 참조. |
| [#420](https://github.com/FelixBaek/arkism/pull/420) | 보고서·인수 증거·진행표 | 완료 주장과 실제 실행 근거를 함께 확인하기 쉬워진다 | 보고·인수 도구이며 실험 효과 아님. 원 PR Evidence 참조. |
| [#421](https://github.com/FelixBaek/arkism/pull/421) | CRW-01 계약 검증 기록 | 팀 실행의 입력·비용·결과 의미를 추적하기 쉬워진다 | 문서·검증 기록. 원 PR Evidence 참조. |
| [#422](https://github.com/FelixBaek/arkism/pull/422) | 선택적 실행 시도·사용량 집계 helper | 재시도와 부분 사용량을 구분해 볼 수 있다 | 관측·집계이며 실제 총 비용은 자료 필요. 원 PR Evidence 참조. |
| [#423](https://github.com/FelixBaek/arkism/pull/423) | 버전 있는 실행 참조와 근거 자격 검사 | 다른 버전·자격 없는 근거가 실행 결과로 섞이는 일을 줄인다 | 명시적 근거만 사용. 원 PR Evidence 참조. |
| [#424](https://github.com/FelixBaek/arkism/pull/424) | 실제 호출자에 시도 집계 연결 | helper만 존재하던 관측을 실제 호출 경계에 남긴다 | 선택적 관측. 원 PR Evidence 참조. |
| [#425](https://github.com/FelixBaek/arkism/pull/425) | 문맥 영수증과 명시적 수용 결정 | 찾은 자료와 실제 전달·수용한 자료를 구분한다 | 영수증은 작업 성공 증명 아님. 원 PR Evidence 참조. |
| [#426](https://github.com/FelixBaek/arkism/pull/426) | 재사용 전 현재 버전·사용 가능 근거 재검사 | 정정·삭제·버전 변경 뒤 낡은 자료를 재사용할 위험을 줄인다 | 기존 권한 검사를 유지. 원 PR Evidence 참조. |
| [#427](https://github.com/FelixBaek/arkism/pull/427) | 중단 복구의 동일 효과·영수증 구분 | 복구하면서 같은 효과를 중복 적용하는 일을 줄인다 | 복구 근거가 충분한 범위. 원 PR Evidence 참조. |
| [#428](https://github.com/FelixBaek/arkism/pull/428) | 실제 검산 CLI 인수 결과 기록 | 실행하지 않은 검사를 완료로 오인하기 어렵게 한다 | 문서·검증 기록. 원 PR Evidence 참조. |
| [#442](https://github.com/FelixBaek/arkism/pull/442) | 팀 비교 실행·독립 평가 검증 기록 | 자기 평가와 독립 평가를 분리해 비교할 수 있다 | 실제 팀 효과를 새로 입증한 것 아님. 원 PR Evidence 참조. |
| [#443](https://github.com/FelixBaek/arkism/pull/443) | 실패 분석과 책임 추정 분리 기록 | 관측된 실패를 곧바로 특정 구성원의 책임으로 단정하지 않게 한다 | 문서·분석 범위. 원 PR Evidence 참조. |
| [#444](https://github.com/FelixBaek/arkism/pull/444) | scalar dynamics 계약과 엄격한 validator | 시간·관측·상태 입력이 다른 계산으로 해석되는 일을 줄인다 | 계약 구현. 원 PR Evidence 참조. |
| [#445](https://github.com/FelixBaek/arkism/pull/445) | 제한된 팀 채택·연구 준비 기록 | 개발 수용과 연구 인수를 분리한다 | 준비·결정 기록. 원 PR Evidence 참조. |
| [#446](https://github.com/FelixBaek/arkism/pull/446) | 늦은 전송 재시도 처리 보정 | 원래 사건과 늦게 도착한 재전송을 구분하며 유효 재시도를 보존한다 | 사건 의미를 새 결과로 바꾸지 않음. 원 PR Evidence 참조. |
| [#469](https://github.com/FelixBaek/arkism/pull/469) | scalar 상태 전이와 revision 격리 | 독립 결과를 한 번 반영하고 새 revision에 이전 상태가 섞이지 않게 한다 | 실제 계수·효과 선택은 별도. 원 PR Evidence 참조. |
| [#477](https://github.com/FelixBaek/arkism/pull/477) | 합성 검색 비교 계약과 필수 회귀 수집 | 기본 검색과 새 정책 비교의 누락을 줄인다 | 계약·합성 검사. 원 PR Evidence 참조. |
| [#478](https://github.com/FelixBaek/arkism/pull/478) | 확정 사건 재생과 읽기 전용 probe | 다시 계산한 상태를 확인하면서 원 상태를 바꾸지 않는다 | 실제 capture 수용과 별도. 원 PR Evidence 참조. |
| [#482](https://github.com/FelixBaek/arkism/pull/482) | EXP-01 합성 자료 생성·검증 | 사건·질문·정답·split을 분리해 자료 누출을 줄인다 | 합성 자료가 실제 연구 자료를 대신하지 않음. 원 PR Evidence 참조. |
| [#483](https://github.com/FelixBaek/arkism/pull/483) | CAP-07 호환 검사 수용 기록 | 지원·미지원 경계와 검증 근거를 찾기 쉬워진다 | 문서·인수 기록. 원 PR Evidence 참조. |
| [#484](https://github.com/FelixBaek/arkism/pull/484) | 필수 일곱 정책의 EXP-02 비교 명세 검증 | 비교 정책·예산·단계가 서로 어긋나는 실행 계획을 거부한다 | 비교 명세이며 실제 연구 실행 아님. 원 PR Evidence 참조. |
| [#485](https://github.com/FelixBaek/arkism/pull/485) | CAP-02 합성 구현·독립 검사 기록 | 합성 시험의 적용 범위와 한계를 확인하기 쉬워진다 | 문서·합성 증거. 원 PR Evidence 참조. |
| [#487](https://github.com/FelixBaek/arkism/pull/487) | 라우팅 검증 기록과 공개 메타데이터 정리 | 라우팅 완료 근거와 공개 범위를 분명히 한다 | 문서 정리 자체를 개발 성과로 세지 않음. 원 PR Evidence 참조. |
| [#490](https://github.com/FelixBaek/arkism/pull/490) | 독립 결과 변환 계약과 비학습 사건 보존 | 자격 없는 사건·중복을 학습 결과로 잘못 세는 일을 줄인다 | 관측되지 않은 결과는 unknown 유지. 원 PR Evidence 참조. |
| [#491](https://github.com/FelixBaek/arkism/pull/491) | 등록 정책 실행기와 보조 가중치 0의 순서 규칙 | 정책 ID별 실행과 0 가중치의 기존 순위 보존을 재현한다 | 새 정책 우월성을 입증하지 않음. 원 PR Evidence 참조. |
| [#492](https://github.com/FelixBaek/arkism/pull/492) | 권한 있는 native 결과 해석과 prepared intent | 실제 근거를 권한 검사 후 상태 반영 의도로 연결한다 | 권한 승격 없음. 원 PR Evidence 참조. |
| [#493](https://github.com/FelixBaek/arkism/pull/493) | 원자성 원장 모델과 명시적 관측 주기 | 실패·재시도·관측 시각을 기록해 중복·부분 적용을 구분한다 | 모델 검증과 실제 저장소 검증 구분. 원 PR Evidence 참조. |
| [#500](https://github.com/FelixBaek/arkism/pull/500) | 검증된 native 상태 연결 선행 작업 통합 | 따로 검증한 선행을 같은 코드 경로로 묶는다 | 통합 준비 단계. 원 PR Evidence 참조. |
| [#501](https://github.com/FelixBaek/arkism/pull/501) | 선택 카드 보류 근거·재검토 조건 기록 | 자료나 필요가 없는 선택 기능을 무리하게 개발하지 않게 한다 | 기능 추가 아님. 원 PR Evidence 참조. |
| [#502](https://github.com/FelixBaek/arkism/pull/502) | 수락 결과 원자 저장과 native 상태 인계 | 결과 기록과 상태 반영 인계가 따로 남는 위험을 줄인다 | 기존 범위·권한·정정·삭제 경계 유지. 원 PR Evidence 참조. |
| [#503](https://github.com/FelixBaek/arkism/pull/503) | 수명·복원 메타데이터 모델 | 복원과 현재 실행의 상태를 구분할 수 있다 | 메타데이터 모델만으로 실제 복원 수용 아님. 원 PR Evidence 참조. |
| [#504](https://github.com/FelixBaek/arkism/pull/504) | 검색 단계 관측과 호출별 기억 제외 | 검색·선택·packing·반환을 구분하고 특정 호출에서 기억을 뺄 수 있다 | 전체 host 전달 증명은 후속 작업. 원 PR Evidence 참조. |
| [#505](https://github.com/FelixBaek/arkism/pull/505) | 실제 전달·복원 통합과 최신 main 반영 | 원문 전달·독자 영수증·정정/삭제/복원 경계의 연결을 확인한다 | 제품 빌드·배포와 구분. 원 PR Evidence 참조. |
| [#506](https://github.com/FelixBaek/arkism/pull/506) | SYS-05 부하 계획·실패 보존 집계 | 지연·최신성·비용 비교에서 실패·미실행이 빠지는 일을 줄인다 | 오프라인 준비가 실제 성능 결과 아님. 원 PR Evidence 참조. |
| [#507](https://github.com/FelixBaek/arkism/pull/507) | 서버 total 부재 시 skill 목록 개수 검사 보정 | 목록만 제공되는 정상 응답이 잘못된 UI 검사 실패로 이어지는 일을 줄인다 | 검사 수정이며 새 UI 기능 아님. 원 PR Evidence 참조. |
| [#509](https://github.com/FelixBaek/arkism/pull/509) | 선택 전에 작업 checkpoint 읽기 | 선택 이후에야 맥락을 읽어 의사결정 근거가 어긋나는 일을 줄인다 | 기존 view 보호와 함께 적용. 원 PR Evidence 참조. |
| [#510](https://github.com/FelixBaek/arkism/pull/510) | SYS-06 flag 행렬·통합 보고서 검사 | off/shadow/dynamic·fallback의 필수 증거가 빠진 보고를 거부한다 | 계획·validator만으로 서버 연결 완료 아님. 원 PR Evidence 참조. |
| [#511](https://github.com/FelixBaek/arkism/pull/511) | RTE-06 결과 귀속 검증 기록 | 어떤 결과가 어떤 실행 시도에 연결됐는지 검토하기 쉬워진다 | 실제 독립 보상 자료는 별도. 원 PR Evidence 참조. |
| [#514](https://github.com/FelixBaek/arkism/pull/514) | 동적 worker·발행 갱신·daemon 연결 | 수락된 결과를 durable 처리하고 API가 발행 상태를 읽게 한다 | 명시 설정이며 기본 자동 활성화 아님. 원 PR Evidence 참조. |
| [#515](https://github.com/FelixBaek/arkism/pull/515) | 명시적 off/shadow 검색 설정 | 기본 반환을 보존하면서 새 순위를 진단할 수 있다 | 당시 일반 dynamic은 거부; 이번 전달에서 별도 구현. 원 PR Evidence 참조. |
| [#516](https://github.com/FelixBaek/arkism/pull/516) | 선택 직전 작업 view 재검사 | 분류·선택에 사용한 작업 근거가 도중에 바뀌는 문제를 줄인다 | 기존 범위·선택 보호 유지. 원 PR Evidence 참조. |
| [#517](https://github.com/FelixBaek/arkism/pull/517) | 효과가 불명인 쓰기 단계의 재실행 중지 | 이미 변경했을 수 있는 작업을 다른 모델이 다시 실행하는 일을 막는다 | 알려진 미실행과 구분. 원 PR Evidence 참조. |
| [#518](https://github.com/FelixBaek/arkism/pull/518) | 동일 fault-window 기준의 sync/async 비교 | 서로 다른 측정 기준으로 생긴 잘못된 성능 비교를 줄인다 | 합성 기술 비교이며 우월성 판단 없음. 원 PR Evidence 참조. |
| [#519](https://github.com/FelixBaek/arkism/pull/519) | 라우팅 단계 최대 네 번 시도 | 끝없는 대체 실행과 예산 소모를 제한한다 | 시도 수가 개별 도구 호출 수는 아님. 원 PR Evidence 참조. |
| [#520](https://github.com/FelixBaek/arkism/pull/520) | Claude 계열 최대 50 turns | 과도한 모델 차례를 제한한다 | turn과 개별 tool call 구분; 후속 공통 집계로 보완. 원 PR Evidence 참조. |
| [#521](https://github.com/FelixBaek/arkism/pull/521) | 정답 없는 EXP-01 자료의 실제 native 경로 실행 | 정답을 ranker에 넘기지 않고 생성 자료를 실제 기능에 연결한다 | 원 단계의 시각·범위·vector 한계와 후속 보완 구분. 원 PR Evidence 참조. |
| [#522](https://github.com/FelixBaek/arkism/pull/522) | 일곱 native 정책 EXP-02 파일럿 adapter | 정책별 실제 순위를 같은 계획에서 얻어 검산한다 | 탐색 파일럿이며 확인 연구 gate 아님. 원 PR Evidence 참조. |
| [#523](https://github.com/FelixBaek/arkism/pull/523) | fast/slow 상태 전이·재생·조회 | 빠른 상태와 느린 상태를 같은 사건 이력에서 분리해 계산한다 | 비중·속도는 실제 효과를 입증한 값 아님. 원 PR Evidence 참조. |
| [#525](https://github.com/FelixBaek/arkism/pull/525) | EXP-01 v2 불변 최종 잠금 | 누락 값·변경된 입력·자기 검토 자료가 최종 잠금으로 섞이지 않게 한다 | 원문 hash는 사람 승인·등록 시점 인증 아님. 원 PR Evidence 참조. |

## 보류와 적용 범위

실제 독립 oracle·참여자/실험 관측·실제 reward/비용·skill-use 자료가 없는 연구 인수는 보류한다. PSY 준비 문서와 실제 결과 없는 prototype은 실제 효과 검증으로 세지 않는다. 원래 19개 조건부 개발 범위의 코드 근거와 실제 자료/사람 결정을 구분한다. 기본 off는 유지하며 이 전달은 제품 빌드·운영 enable·배포를 포함하지 않는다. UI 추가도 없다.

## 전달 상태

- 대상 base: `main` (공개 원격에서 확인).
- 새 공개 후보: `codex/roadmap-delivery-20261101`.
- PR: 생성 전. 생성 뒤 이 행에 실제 링크를 기록한다.
- 최신 main 반영: `15edfca69efa4565592b1f355a3cd5063d6fa4df`; 세션 메시지 변경을 보존하고 API 정의를 함께 합쳤다.
- 공개 후보 검사: 전체 `tools/check_local.py` 종료0/1246.806초, Go vet·전체 race 및 Python179/820/49·프런트591개 통과(프런트 실패·누락0), 고정2970파일 변경0, lint0. 자체 임시PG 종료·삭제 확인. 일부 불변 Go 패키지는 캐시이며 개별 Go skip은 별도 미계수. 제품 빌드 없음.
- 원격 CI/AI 리뷰/병합: 아직 미실행. 실제 GitHub 결과를 확인한 뒤 진행한다.
- 이슈: 병합 후 #151/#152/#154/#155 조건부 종료 후보. #150/#153/#156/#158 보류 유지; 삭제된 #157은 완료로 세지 않는다.
