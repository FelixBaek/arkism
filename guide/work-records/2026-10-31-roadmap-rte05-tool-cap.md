# RTE-05 tool-round cap for every Claude Code family step

Task: RTE-05 (#155)
Research: none
Design: Every Claude Code family step (claude, glm and custom CLIs) that is not once now runs with `--max-turns 50`, the owner's starting value; once keeps `--max-turns 1`. The cap is one constant, toolTurnCap, in the existing command builder, so ordered, smart, off, fixed, routed, research and fallback runs all get it. The Python-parity golden file is not edited: the golden test adds the deliberate flag to its recorded argv.
Relation: extends
Why: The RTE-05 card asks for one shared tool, call and time limit for every policy. The owner chose to put a tool limit on every step, start at 50 and adjust it from tests and real use (2026-10-31).
Verified: `go test -race ./internal/host/crew/ ./internal/host/` passed, as did go vet, gofmt on the changed files and the complexity gate. Command tests check 50 on claude, glm and custom steps, 1 on once steps and no flag on other CLIs; the research runner test checks 50. Two mutations (no cap, a different value) each failed the tests.
Limits: This caps model turns, not single tool calls; one turn may call several tools. Codex, Grok, Gemini and agy have no such switch and print plain text, so their tool use is neither stopped nor counted; counting needs each CLI's event output, which was not checked against real runs. A long research step can now stop at 50 turns. No real provider call was made. The full local check was not run on this commit. No build, deploy or merge.

## 한국어 작업 기록

제목: Claude 계열 모든 단계에 도구 차례 상한 50
이유: RTE-05 카드는 모든 정책 공통 도구·호출·시간 한도를 요구한다. 소유자가 모든 단계에 도구 한도를 걸고 50에서 시작해 시험·실사용을 보며 고치기로 정했다(2026-10-31).
검증: crew·host race 시험, go vet, 바뀐 파일 gofmt, 복잡도 검사가 통과했다. 명령 시험은 claude·glm·custom 일반 단계 50, once 단계 1, 다른 CLI는 플래그 없음을 확인했고 research 시험도 50을 확인했다. 변이 2건(상한 없음, 다른 값)을 모두 잡았다.
한계: 모델 차례 수 상한이지 도구 호출 하나하나의 상한은 아니다(한 차례에 도구 여러 번 가능). Codex·Grok·Gemini·agy는 이런 스위치가 없고 출력이 일반 글이라 막지도 세지도 못한다. 세려면 CLI별 이벤트 출력이 필요한데 실제 실행으로 확인하지 않았다. 긴 리서치 단계가 50차례에서 멈출 수 있다. 실제 제공자 호출 없음. 이 커밋에서 전체 로컬 검사는 돌리지 않았다. 빌드·배포·병합 없음.
