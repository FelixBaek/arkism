# Accept the actual standalone offline baseline gate

Task: R8 (#151)
Research: Offline B0 contract measurement; no new performance result
Design: Run the existing default acceptance protocol against its frozen implementation after an explicit user exception for the verification CLI only.
Relation: faithful
Deviation: The earlier implementation record correctly left the standalone gate unrun. This follow-up executes that gate; product builds and research trials remain outside its scope.

The user explicitly permitted compiling and running only
`./cmd/memory-harness-baseline` and its acceptance checks in a fresh private
`/tmp` directory. The unchanged baseline checkout was
07ed21d8eca16cb93d0f9111c683ed751325df02. The default acceptance script used
Go1.27.3 with local/offline dependency settings. Its real build produced the
verification CLI ELF, then the actual binary processed the generated seed17
manifest. No mock executor or in-process run() substituted for this gate.

All14 required checks passed: Go328leaf, Python128methods, fixture generation,
CLI build/run, metrics/report generation and four negative CLI checks. Each
negative command returned the expected exit2. Failed, skipped, empty or missing
checks were not accepted. The original contract bytes and historical reference
commit27bb41807ff48d47559ba2cfc30feb300caacecf stayed unchanged.

An independent artifact adjudication recomputed exact Fraction-based ranking,
greedy selection, full references, six statistics and the query/stream/summary
CSV rows for all seven scheduled queries. It checked the30 artifact hashes,
actual build target/ELF identity, command/log hashes, negative outputs and a
clean source tree before/after. The reviewer disclosed prior authorship of the
acceptance/collector implementation; this new review is independent arithmetic
and artifact adjudication, not a new claim of source-code reviewer independence.
Earlier source reviews and corrected implementation evidence remain separate.

The exact executable SHA256 is
f762622f65e950f9002299bb1b93b3c8dec988c522e7ec3bbe2212cfbc3829d8;
the exact bundle SHA256 is
12e3ccb883c5d2e9f3608875a69fa500b38165eaa15b48fc16caf8e5b729819d.
All41 actual files were retained byte-for-byte in the private evidence archive.
The generated requires_independent_review flag and historical unrun record
were preserved. A separate parent acceptance records the successful review.

R8 is accepted as local offline B0 validation; the roadmap count is15/63.
CRW-05 runtime implementation remains in progress. Final check_local for this follow-up passed Python412, Go complexity/vet/full
race and UI532. The roadmap plan and exact-authorized publication audit are
checked before publication; commit records are checked against full SHA ranges.

Limits: this does not establish new search or holdout performance, equivalence
to the production service/database, a stronger preceding comparison, candidate
A readiness or main-study readiness. DYN/EXP/SYS assumptions remain separate.
No real provider/model, operational database, product/installer build, release,
deployment, participant collection or Claude work was changed or run.

## 한국어 작업 기록

작업 ID: R8 (#151)
변경 이유: 사용자가 허용한 검산 CLI 전용 빌드·인수 경로를 실제로 실행해 남아 있던 독립 실행 관문을 검증한다.
실행한 검증: 고정07ed 소스의 실제 기본14check, Go328leaf/Python128methods·음성CLI4개·7질의의 독립 산술/CSV 대조·30artifact hash·ELF target·source clean 확인.41실제 파일 바이트 보관. 상위문서변경 최종 check_local(Python412/Go 복잡도·vet·전체race/UI532) 통과. 공개·계획 검사는 게시 전, 커밋 형식은 full SHA 범위로 확인한다.
현재 상태: 별도 상위 수용 기록으로 R8 로컬 검산 완료. 로컬15/63, CRW-05 계속 진행 중. 산출물의 review_status와 과거 미실행 기록은 수정하지 않았다.
한계: 실제 서비스/DB·강한 선행 대조·새 holdout·후보A/연구 본실험 준비·제품/설치 빌드·릴리스·배포·Claude 수정은 수용하지 않는다.
