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
