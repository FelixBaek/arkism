# Observe native Search stages and select an invocation context

Task: SYS-01.integration (#152)
Research: Isolated engineering verification; no model use, task success or causal result
Design: Reuse the existing baseline, final transaction, native service state, source permissions and correction/deletion APIs. Add an optional read-only native shadow adapter, a versioned invocation-only context selector and a separate stage observation protocol. Preserve original arithmetic fixtures and observation meanings.
Relation: adapted
Deviation: The actual baseline combines RRF with existing recency and signal, so it is not relabelled as the pure native arithmetic reference. Unsupported native features and actors use explicit fallback; no feature values or permissions are invented. The original SelectedRefs remains the full arithmetic rank, while the separate v2 records packing and final-return stages. Two inherited identifier comments are removed only in the publication copy; actual Go token equivalence is verified.
Why: Connect the existing scalar state to actual Search without changing default results, and distinguish ranked, packed, returned and reader-observed material when testing current-context exclusion and persistent correction.
Verified: Author normal/race61methods160leaf passed; fresh independent normal/race6methods23leaf passed with no failure or skip, and replayed the author corpus separately. Actual restricted PG/socket HTTP exercised correction/withdrawal/current-next-new-reader plus four changes after native read publication and before the final transaction (revision/delete/source/grant), returning no stale response or usage. Original315 JSON bytes matched. Root verified independent58 evidence pins. Root combined the public parent with exactly21 frozen source files after checking every affected parent byte; selected integration15 methods23leaf passed with no failure or skip. The publication-only comment cleanup preserves7155 Go tokens. Scoped21-file denylist/UTF8 and gitleaks checks passed. Earlier immutable315 arithmetic JSON results remain unchanged in author and independent replays. Root tools173 and metadata63cards96gates passed. OpenAPI preserves existing JSON formatting through five surgical edits and parses to exactly the author schema tree.
Limits: This stage does not complete whole SYS-01: production same-reader receipt-chain integration remains separate. The default remains off; shadow still returns the existing baseline. No full check_local or all-Go regression is claimed. No product build, deployment, main merge or external provider run. Whole22 accepted and required27 remaining are unchanged.

## 한국어 작업 기록

제목: 실제 검색의 읽기전용 진단과 호출별 기억 제외
이유: 기존 검색을 유지하면서 후보·포장·최종 반환을 구분하고, 현재 문맥 제외와 영구 정정·철회를 별개로 검증한다.
검증: 작성자 일반/race61/160, 독립 일반/race6/23 통과·실패·skip0. 실제 PG·HTTP 정정/철회와 최종 반환 직전4경쟁을 확인했다. 원315개 JSON이 동일하고 독립증거58개 해시를 대조했다. 현재 결합 복사본15메서드23세부사례 통과, 실패·skip0. 공개 복사본 주석만 정리했고7155개 Go 토큰 동일성을 확인했다. 변경21파일 공개범위 검사와 gitleaks, 도구173개 및 메타데이터63/96 통과. API 문서는 검증된 스키마와 구조가 같고 기존 서식을 보존했다.
한계: 전체 카드 완료가 아니며 실제 독자 전달 영수증과의 결합 수용은 남아 있다. 기본 off·기존 baseline 반환을 유지한다. 전체 check_local·빌드·배포·운영 반영을 실행했다고 표시하지 않는다.
