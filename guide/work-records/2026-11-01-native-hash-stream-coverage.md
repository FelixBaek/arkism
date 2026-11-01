# Hash-stream regression coverage / 해시 스트림 회귀 보완

Task: PR #534, EXP-02 native hashing transport
Research: Fixed synthetic engineering vectors only; no scientific or product acceptance.
Design: Exercise the existing stream boundary directly in the covered parent test process. Keep the compiled fixed type whitelist, strict envelope/payload decoder, redacted failures and terminal I/O errors. Expected values use hand canonical bytes and the fixed domain prefix, computed independently without the native hash API.
Relation: partial
Deviation: Tests only. Product source, coverage thresholds, exclusions and hash contracts are unchanged.
Why: Actual Codecov patch coverage was76.83215%, below80%, with54hash-stream lines uncounted. Existing Python interop exercises real child images, whose separate profile files do not contribute to the uploaded parent profile. Add meaningful direct semantic/failure tests rather than relax the threshold or remove the real functional chain.
Verified: Latest distinct-example hash-stream race target passes1.023 seconds. nativeHashStream statement coverage91.7%, nativeHashFailure100%, nativeHashRequest100%. Full current covered command package passed in55.744 seconds with77.3% total statement coverage; this total-package measure differs from the PR changed-line coverage gate, whose latest remote result remains pending; remote new-head CI and AI review must run after this test-only change.
Limits: Prior constructor typo and an independently calculated second example without the domain prefix failed; corrected using the fixed domain formula and both hand examples. Those logs remain failed evidence. An earlier broad race run compiled that wrong fixture and cannot be counted as a current pass. No product build, deployment, operating database or paid provider call.

두 개의 서로 다른 stream ID를 기존 고정 hand example과 도메인 분리 공식으로 검산했다. 정확한 두 응답과 순서·EOF를 확인한다. 잘못된 JSON, 누락·중복·추가 envelope 필드, 잘못된 type, codec에는 있지만 인터페이스에는 허용되지 않은 type, 잘못된 payload, 후행 자료, 빈 입력을 거부하고 원문 canary를 반향하지 않는지 확인한다. Reader/Writer 실패는 성공으로 처리하지 않는다. 기존 codec을 다시 호출해 정답을 만드는 시험은 아니다.

원격 커버리지 실패는 유지하고 최신 CI로 다시 확인한다. 이 보완으로 실제 모델·참여자·연구 결과 인수 수를 올리지 않는다. PR 기능·기대 개선 목록과 제품 기본 off 및 실제자료 보류도 유지한다.

Final upstream refresh: before merge, main advanced from ea7bb9c to635dffb with local test-version and HTTPS redirect changes in7desktop/installer files. Signed rebase preserved them without conflict; before this documentation update the tree differed from the previously tested head21df3aa only by those7upstream files. Prior-head patch coverage89.12530% passed; that result is retained for its exact head and not relabelled as a new-head pass. Current full CI/AI review must run again. No product build was performed.
