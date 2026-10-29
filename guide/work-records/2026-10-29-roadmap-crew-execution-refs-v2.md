# Versioned crew execution references

Task: R0 / CRW-01.execution_refs_v2 (#154)
Research: E contract foundation; no runtime or performance result
Design: Immutable crew v1 registry by hash reference plus closed v2 patches
Relation: adapted
Deviation: Independent meetings and legacy assists have actual record collections that v1 cannot represent. The new protocol is separate; accepted v1 consumers and evidence remain unchanged.

The existing engine stores workflow runs, meetings and assists in distinct
collections. Mapping their IDs into a fabricated crew_run would lose provenance.
The separate arkism-crew-research-v2 contract admits the actual record kind,
exact CAS revision and definition reference. Verified self roots explicitly name
their own collection and ID; null roots mean unknown and cannot construct a
complete attempt identity. Parent links join protocol, scope, full task and
root namespaces, preserving actual parent/child record versions.

The new crw2-attempt identity uses a16-field typed canonical preimage. Collection
namespaces separate equal ID text in different execution tables. Mutable run
CAS and usage do not become identity. General execution references are broader,
while effect records and human effect-approval CAS remain bound to crew_runs.
Meeting budget permission and OI-25 personal-memory exclusions are unchanged.

The private registry reuses18 immutable v1 models and adds closed patches and
three explicit proof/journal models, for21 models and220 fields. Provider leaf,
transport aggregate and reservation estimate views remain separate. Complete
membership requires a closed sender journal, exact members and pinned raw bytes;
a bare complete enum is insufficient. Caller pins provide local integrity only.
The protocol explicitly retains runtime_authenticated=false and run_coverage=unknown.

## Verified scope

Author35 tests and independent26 methods/53 leaf checks passed. Review checked
all five final hashes, three collection-separated identity vectors, exact refs,
root/parent joins, cost roles, failed leaf retention, strict JSON and observer
revision invariants. Initial probes caught task movement, role changes hiding
cost, dispatch/duration knowledge regression and undispatched reported usage;
the owner corrected them and independent checks passed.

Current directory governance separately verifies the three immutable v1 files
and five registered v2 files. The historical v1 directory-only audit is preserved
as evidence for its original snapshot. The approved RTE numeric-parser followup
is separately attributed and does not represent a v2 author change to a consumer.

| Private logical artifact | SHA256 |
|---|---|
| crew_contract_v2.json | 7780b97e9ec705bc32a0eff5c083d2a26966e9781f794da9c15b6d19db1f9b15 |
| crew_contract_v2_validation.py | 01747788f2094bf38fafe8776bac56fbdf6be4da7a378d64917211f362ac1eba |
| test_crew_contract_v2.py | b4e77e699e8acc709aa065919ecc622b20376488b5c8b6ad0b019bbd00c3f86c |
| crew_contract_v2_vectors.json | 2d2a2f117d557bd524ac0e36f4bddfc31ef987e90e52ccfdebdbd456f57e9297 |
| crew_contract_v2_delta.md | fe7e5a55503ef9a297b0482d28746bf399e3771797650cce04dfedc1bea45cb6 |

This record accepts the private contract extension. The separately verified
Go helper implementation is recorded in the observer-v2 work record; actual
caller integration remains pending. The unused experimental v1 Go helper is upgraded in place
with an explicit new discriminator before actual wiring; its8c14279d evidence
and private v1 consumers are preserved. No parallel unused implementation is
introduced. No model/API/effect call, build, deployment or runtime authentication
was performed for this contract gate.

## 한국어 작업 기록

작업 ID: R0 / CRW-01.execution_refs_v2 (#154)
변경 이유: 독립 회의·단독 실행의 실제 참조를 표현하면서 기존 v1 계약과 승인 경계를 유지한다.
실행한 검증: 소유35·독립26methods/53leaf,5SHA·등록8파일 구성·3collection ID 분리 확인 통과.
미실행: 이 계약 단계에서 실제 호출부 통합·인증·모델/효과 실행·빌드·배포. Go helper 검증은 별도 작업 기록으로 연결한다.
현재 한계: 계약·로컬 무결성만 수용했으며 caller hash는 실제 인증을 증명하지 않는다.
커밋 상태: 비공개 원문 대신 변경 이유·검증 범위·해시만 후속 단계 PR에 기록한다.
