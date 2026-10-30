# Reuse the pinned policy schema and clarify constant-path fixture writes

Task: DYN-05.p1; R8 (#152)
Research: Offline policy comparison engineering
Design: Load only selected immutable models/projections/enums/primitives from the existing hash-pinned dynamics contract. The schema is a fixed local codec dependency, never a run-selected input path or source snapshot. Preserve all original schema values, typed validation order, canonical bytes and comparison behavior.
Relation: adapted
Deviation: Remote Sonar reported a failed security gate and8.4% duplicated lines after the initial local acceptance. Preserve that failed analysis and initial source. Replace copied declarations with exact existing contract declarations rather than changing quality thresholds or exclusions.

Why: A second1200-line schema copy duplicated its authoritative fixed contract. Reading exact SHA-checked declarations reduces maintenance drift. The reported test taint flows passed artifact bytes to constant-path Path.write_bytes data arguments; they did not demonstrate an input-controlled destination or product path traversal. Express the same writes through fixed-path open and stream.write so the data and destination are explicit. Extract one recursive shape helper and replace a nested ablation expression without changing validation meaning.
Verified: All25 model/projection definitions and every original enum/primitive are JSON-semantically identical to the original in-code dictionaries; the underlying952d contract remains byte-identical. Author22 Python methods pass including3 new pinned-schema tamper/read-error/equality checks. Original7 other source pins are unchanged. Required full check94001 exit0: Python627 (173 tools+405 evaluation+49 integrations), Go complexity/vet/fullrace and UI532. Fresh independent12 methods/62 leaf plus author22/45 leaf passed with zero failures/skips. Exact schema field iteration/required order,26 unchanged function ASTs, typed canonical literals and nine comparison flags were verified. Actual cold-import regular/tamper/symlink/parent-link/FIFO/no-writer/directory/missing/unreadable probes completed with bounded fixed errors. Root verified51 source pins and7 independent evidence files. Remote reanalysis remains pending.
Limits: This is a representation and file-I/O clarity change. It does not establish an exploitable product vulnerability or accept scientific effects. No schema hash/registry/fixed contract/test expectation/quality rule/exclusion was changed. The schema dependency is read once at module initialization; comparison reads remain only explicit run/registry inputs. Whole21/63 remains unchanged. No product build/deploy or Claude changes.

## 한국어 작업 기록

이유: 고정 계약과 같은 선언을1200줄 넘게 복제하던 코드를 원문 SHA를 확인해 재사용하도록 줄였다. 테스트의 상수 경로 쓰기는 파일 목적지와 데이터가 분명히 구분되도록 표현했다.
검증: 기존25모델·enum·primitive 값 정확동일, 원7소스 불변, Python22검사 및 전체94001 종료0(Python627·Go·UI532). 독립12메서드62세부·작성22/45세부 및 실제 cold-loader 파일 경계 검사 통과. root51핀·7증거 확인. 원격 재판정은 별도로 확인한다.
한계: 최초 원격 실패와 수정 전 소스는 보존한다. 실제 제품 경로 조작 취약성으로 과장하지 않으며 품질 기준을 낮추지 않는다. 제품 빌드·배포·Claude 변경 없음.
