# DYN-02/03 fast/slow product wiring (off by default)

Task: DYN-02 (#152), DYN-03 (#152)
Research: DYN-02.fastslow and DYN-03 fast/slow substages wired into the native dynamics worker and the search mode file.
Design: The native worker keeps one state per target; an optional slow lane (`slow_tau_ns`, `slow_eta_positive`, both or neither, slower than the fast lane) adds `slow_value`, advanced by the same observation, time and operation inside the existing lifecycle transition. The search mode file takes an optional `fast_slow` block (sealed slow DynamicsConfigV2 plus a fast weight), checked with the DYN-02 FastSlowConfig rules against the policy's own dynamics config. The search publisher binds the native slow rates to that block, projects both lanes to the publication time and ranks on `w*fast + (1-w)*slow`. Mode `shadow` records only; new mode `on` requires the block and returns the native bridge ranking when it is selected, with its own final-fence snapshots, and the baseline otherwise.
Relation: adapted
Deviation: The native worker does not call ApplyEventFastSlow; it reuses its scalar numeric step per lane, so the two lanes share history by construction (one row, one history) rather than by a pair check. The mixed value is computed at read time, not stored.
Why: The owner asked for the fast/slow dynamics to run in the product behind an explicit flag, with the default unchanged and shadow never changing an answer.
Verified: see the commit record.
Limits: Lane rates and weight are test literals; no serving policy is accepted, so `on` is development wiring only. No product build, deploy or merge.

## 한국어 작업 기록

제목: fast/slow dynamics를 제품 검색에 연결(기본 꺼짐)
이유: 소유자가 fast/slow를 명시 설정일 때만 제품에서 돌게 하고, 기본값은 그대로, shadow는 답을 바꾸지 않게 하라고 했다.
검증: 커밋 기록 참조.
한계: 속도·비중은 시험용 값이고 제공 정책은 수용되지 않았다. 빌드·배포·병합은 하지 않았다.
