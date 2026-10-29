# Verify synthetic correction displays and actual-ID assignment replay

Task: PSY-02 (#157)
Research: Candidate B synthetic preparation; no human study or observed native inclusion
Design: Equal-information B1/B2 displays, fixed pre-action expected Y, independent task/authority action sets, deterministic virtual-ID/session assignment and isolated replay. Keep primary3 states separate from diagnostic3 states and B0 source-only auxiliary provenance.
Relation: adapted
Deviation: Static researcher preview only; actual native Y is null/not_run, not an observed zero. Real B0 rendering, researcher confound review and study approval remain downstream.

Why: Implement the stimulus/assignment/validation preparation while preserving storage/eligibility/context inclusion/answer-correctness distinctions. A valid renamed case exposed a dangling plan reference: the generator derived an old scenario-state ID rather than using the registry's actual ID. Fix the mapping without narrowing the valid ID domain or leaking state labels.
Verified:11 artifacts frozen. Author43methods/157leaf and fresh independent20methods/85leaf passed with zero failures/errors/skips. Independent actual CLI covered24 opaque case IDs, both sessions, assignment/replay, duplicate pair/ID and missing-plan guards, plus264 equivalence rows. The fix changes only build_plan and adds one positive regression; the other9 files and original25 dependency pins stay unchanged,27 pins stable. Original ID-leak, mobile-overflow, CLI-bootstrap and actual-ID failure evidence was preserved before fixes. Local BetterDesign reported zero critical/serious findings, with seven existing token-definition moderate findings; separate real Chrome desktop/mobile/200% text and Tab/Enter/Space checks passed. HTML/CSS were unchanged by the ID fix; no fresh visual rerun is claimed for that code-only follow-up.
Limits: The researcher preview exposes review labels and disables action buttons; participant fragments strip IDs/state/expected labels. This is not a participant-ready or actual B0 product screen. Manual expected Y, scripted responses and development coverage are not actual native Y, participant effects, sample/analysis approval or ethics consent. No live responses, recruitment, product UI/backend, model/DB/build/deployment or Claude changes. Synthetic implementation only is accepted; whole card remains in_progress and21/63 unchanged.

## 한국어 작업 기록

이유: 같은정보의표현·가상배정·세션격리재생을만들고,유효ID변경에도실제사례참조가끊기지않게한다.
검증: 작성자43/157leaf·독립20/85leaf·실제CLI24opaqueID양세션·정보대응264행 통과. 변경2파일/나머지9와의존25핀불변. 로컬코드검사와실제브라우저범위를별도로기록했다.
한계: nativeY는null이며예상합성값·실제값을구분한다. B0source-only·연구자심리검토·기관절차·사람연구는미완료,전체카드아닌합성구현만수용한다.

Public-record validation: check_local53294 exit0, Python567/Go complexity-vet-fullrace/UI532 and staged publication audit passed on parentf5aebd07 plus records. Unpublished DYN05 sources in a separate worktree are outside this passing scope and retain one failed normative-conflict test.
공개기록검증: 전체53294 종료0(Python567·Go·UI532),공개검사통과. 별도미발행DYN05의1FAIL은이검사와구분해미완료로유지한다.
