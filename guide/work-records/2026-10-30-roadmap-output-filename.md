# Explicit output filename boundary

Task: R2/R6.integration (#150), PR505
Research: none
Design: Derive the native leaf with os.path.basename, validate it against Path.name and keep original path validation before descriptor-relative stat/link publication.
Relation: adapted
Deviation: No fixed contract, output bytes, parent selection, checker, rule or permission changes.
Why: The integration scan reported a command-line output path reaching os.link. Make the single-component destination boundary explicit at that sink.
Verified: Existing contract42, metrics39, fixture generation24 and mocked acceptance40 tests passed. Separate21 actual filesystem observations match the original implementation, including Unicode filenames, explicit absolute parents, destination races, pinned-parent swaps and failure cleanup. All15 tracked harness JSON files remain byte-identical to the integration parent, including the fixed43941-byte contract.
Limits: The remote security result for this change is pending. The caller still selects the output directory. An adversary already able to replace entries inside that directory can replace the temporary source inode before publication; both versions exhibit this separate mutable-directory limitation. This change does not claim to fix it or authorize an untrusted parent. No product build, deployment, operating data access or main merge.

제목: 출력 파일 이름의 경계를 명시
이유: 명령줄 출력 경로에 대한 보안 검사 경고를 검토하고 실제 게시 시 사용하는 이름을 단일 경로 요소로 명시했다.
검증: 기존 계약42·지표39·fixture24·모의 인수40개 검사가 통과했다. 실제 파일시스템21개 관측은 수정 전과 같으며 고정 JSON15개도 동일하다.
한계: 원격 보안 재검사 결과는 아직 없다. 출력 폴더를 고르는 권한과 같은 폴더의 파일을 바꿀 수 있는 공격자 문제는 별도이며 해결된 것으로 표시하지 않는다. 제품 빌드·배포·운영 데이터 접근·main 병합은 하지 않았다.

## Preserve the pinned codec at the consumer boundary

The first change cleared the actual remote Sonar gate, but full evaluation then rejected its changed codec hash at the frozen SYS-01 dependency boundary. The direct145 checks above did not exercise that consumer. Preserve the failing combined-import evidence and restore the original codec bytes rather than changing the frozen dependency pin. Apply the same native leaf validation in the metrics CLI before calling the unchanged writer.

The final parent candidate passed all536 evaluation tests with the frozen dependency checks enabled. A separate private A-composed candidate passed577; these are different invocations, not additive counts. Twenty-three actual metrics CLI/filesystem observations matched the original, including failure and race behavior. The new callsite's remote security verdict remains pending; the prior implementation's successful scan is not evidence for it. The mutable-directory limitation above is unchanged.

첫 수정은 원격 보안 검사를 통과했지만 전체 평가에서 고정 codec 해시와 충돌했다. 기존 검사·해시는 그대로 두고 codec 원본을 복원했다. 같은 검증을 지표 CLI 호출부에 적용한 최종 부모 후보에서 전체 평가536개가 통과했다. A를 합친 별도 후보577개와 실제 CLI·파일 동작23개도 확인했다. 새 호출부의 원격 판정은 아직 없으며 같은 폴더 쓰기권한 문제를 해결한 것으로 표시하지 않는다.
