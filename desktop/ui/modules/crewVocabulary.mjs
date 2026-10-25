// @ts-check
// Owns node vocabulary and configuration fields. Dependencies are explicit live ports, never a global scope.
export function createCrewVocabulary(_deps) {
 // 항상 넣는 자료 (pins.js)

  // ---------------------------------------------------------------- vocabulary (mirrors internal/memory/crew.go)
  const TABS = [["all", "전체"], ["call", "직접 부름"], ["trigger", "일정·웹훅"]];

  const EFFORTS = [["low", "낮음"], ["medium", "보통"], ["high", "높음"], ["max", "최고"]];

  // CLI that runs a model on the host (same table as `arkism crew`).
  function cliOf(model) {
    const m = (model || "").toLowerCase();
    if (m === "auto") return "Arkism 라우터";
    if (/^(gpt|o\d|codex)/.test(m)) return "Codex CLI";
    if (/^(opus|sonnet|haiku|fable|claude)/.test(m)) return "Claude Code CLI";
    if (m.startsWith("gemini") || m.startsWith("antigravity")) return "Gemini CLI";
    if (m.startsWith("glm")) return "claude-glm";
    if (m.startsWith("deepseek")) return "claude-ds";
    if (m.startsWith("grok")) return "Grok CLI";
    return "호스트 CLI";
  }


  // Graph node presets. "how" is the plain-words column of the Figma node table (191:7778).
  const NODES = {
    start: { label: "시작", sub: "$이름·화면에서 부르면", icon: "play", group: "시작" },
    schedule: { label: "일정 시작", sub: "정한 시간마다", icon: "calendar", group: "시작", hidden: true },
    webhook: { label: "웹훅 받기", sub: "주소로 요청이 오면", icon: "link", group: "시작", hidden: true },
    expert: { label: "전문가", sub: "페르소나·모델이 정해진 채로 들어가요", icon: "brain", group: "전문가", hidden: true },
    team: { label: "팀", sub: "여럿이 회의해 결론을 내요", icon: "users-three", group: "전문가", hidden: true },
    ai: { label: "AI 에이전트", sub: "도구를 쓰며 스스로 일해요", icon: "robot", group: "AI" },
    ai_once: { label: "AI에게 한 번 묻기", sub: "도구 없이 질문 한 번 · 빠르고 싸요", icon: "sparkle", group: "AI" },
    code: { label: "코드", sub: "Go·셸로 결과 다듬기 · 토큰 0", icon: "code", group: "처리" },
    http: { label: "HTTP 요청", sub: "외부 API 부르기", icon: "globe", group: "처리" },
    github: { label: "GitHub", sub: "PR·이슈·릴리스 (gh)", icon: "git-merge", group: "처리" },
    approval: { label: "사람 확인", sub: "진행 전에 내게 물어요", icon: "check-circle", group: "처리" },
    condition: { label: "조건 분기", sub: "예 / 아니오로 나누기", icon: "git-branch", group: "흐름" },
    merge: { label: "합치기", sub: "모두 끝나면 다음으로", icon: "git-merge", group: "흐름" },
    loop: { label: "목록마다 반복", sub: "항목마다 한 번씩", icon: "arrows-clockwise", group: "흐름" },
    wait: { label: "기다리기", sub: "정한 시간 뒤 이어서", icon: "clock-counter-clockwise", group: "흐름" },
    subflow: { label: "하위 워크플로우", sub: "다른 워크플로우를 노드로", icon: "tree-structure", group: "흐름" },
    notify: { label: "알림 보내기", sub: "텔레그램·슬랙·메일", icon: "paper-plane", group: "알림·끝" },
    end: { label: "보고하고 끝", sub: "부른 자리에 결과 돌려주기", icon: "flag", group: "알림·끝" },
    n8n: { label: "n8n으로 보내기", sub: "위 노드로 안 되는 연동", icon: "plug", group: "그 밖의 연동", hidden: true } // saved workflows still run it; only the 노드 추가 picker drops it
  };

  const IS_AI = (t) => t === "ai" || t === "ai_once" || t === "expert";

  const HUMAN_KIND = { 시작: "흐름 노드", 처리: "처리 노드", 흐름: "흐름 노드", "알림·끝": "알림·연동 노드", "그 밖의 연동": "알림·연동 노드", 전문가: "전문가 노드", AI: "AI 노드" };

  // Port colour of a node card: AI yellow · HTTP blue · 사람 확인 green · the rest grey (Figma 198:2350).
  const KIND_OF = (t) => (IS_AI(t) || t === "team" ? "ai" : t === "http" || t === "n8n" ? "http" : t === "approval" ? "human" : "plain");

  const MODES = { round_robin: "정해진 순서대로", moderator_picks: "사회자가 고름" };

  const PORTS = { condition: [["yes", "예"], ["no", "아니오"]], approval: [["approve", "승인"], ["reject", "거절"]], code: [["pass", "통과"], ["fail", "실패"]] };

  // Settings of the non-AI nodes: [key, label, kind, options]. Keys are what the run engine reads (contract.md).
  const FIELDS = {
    start: [], merge: [],
    schedule: [["cron", "언제 (분 시 일 월 요일)", "text", "0 9 * * 1-5"]],
    webhook: [["note", "메모", "text", "주소와 토큰은 서버에서 arkism-crew hook-url로 받아요"]],
    code: [["lang", "언어", "radio", [["go", "Go"], ["sh", "셸"]]], ["code", "코드 (stdin JSON → stdout JSON)", "code", "stdin JSON → 처리 → stdout JSON"], ["timeout", "최대 실행 시간", "text", "30s", "기본 30s · 최대 2h"]],
    http: [["method", "방식", "select", [["GET", "GET"], ["POST", "POST"], ["PUT", "PUT"], ["PATCH", "PATCH"], ["DELETE", "DELETE"]]], ["url", "주소", "text", "https://"], ["headers", "헤더 (한 줄에 하나)", "area"], ["body", "본문", "area"], ["key", "쓸 키 이름", "text", "", "설정 › 에이전트에 저장한 키"]],
    github: [["args", "gh 인자", "text", "pr create --fill"], ["repo_dir", "작업 폴더", "text", "/srv/…"]],
    approval: [["question", "물어볼 것", "area"]],
    condition: [["expr", "조건", "text", "in.tests.failed == 0", "예: in.tests.failed == 0 and in.risky == false"]],
    loop: [["path", "목록 위치", "text", "in.items"], ["concurrency", "동시에 도는 개수", "text", "1"]],
    wait: [["duration", "기다릴 시간", "text", "30m", "예: 30m, 2h"]],
    subflow: [["workflow", "부를 워크플로우", "workflow"], ["task", "넘길 일", "area", "", "{{input}}에 앞 결과가 들어가요"]],
    notify: [["channel", "보내는 곳", "radio", [["telegram", "텔레그램"], ["slack", "슬랙"], ["email", "메일"]]], ["to", "받는 곳 이름", "text", "", "서버 notify.json에 적은 이름"], ["message", "보낼 말", "area"]],
    end: [["message", "보고 형식", "text", "요약 + 링크", "부른 대화나 화면에 이 형식으로 돌려줘요."]],
    n8n: [["url", "n8n 웹훅 주소", "text", "https://automation.example.com/webhook/…"], ["mode", "결과 받기", "radio", [["sync", "바로 응답 (짧은 일)"], ["async", "끝나면 알려줌 (오래 걸리는 일)"]]], ["timeout", "기다릴 최대 시간", "text", "30m", "오래 걸리는 일은 n8n이 끝나면 결과를 보내 줘요."]],
    expert: [], team: []
  };

  return {
    get cliOf() { return cliOf; },
    get EFFORTS() { return EFFORTS; },
    get FIELDS() { return FIELDS; },
    get HUMAN_KIND() { return HUMAN_KIND; },
    get IS_AI() { return IS_AI; },
    get KIND_OF() { return KIND_OF; },
    get MODES() { return MODES; },
    get NODES() { return NODES; },
    get PORTS() { return PORTS; },
    get TABS() { return TABS; }
  };
}
