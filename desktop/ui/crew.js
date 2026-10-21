// 워크플로우 (Figma v2 · 한글 03 · 03a~03h). Experts, teams and automations became one kind: a workflow — a graph of
// AI, code and people steps that any agent calls with $이름 and the host runner drives (docs/workflow/contract.md).
// app.js passes the shared helpers in once; this file owns the list, the detail panel, the settings window, the
// graph editor (with the one-step simple view and the structure review) and the run view.
// A one-node run takes JSON (an object or a list) as is, so a loop or code node can be tried on real structure.
function runInput(text, only) {
  if (only && /^[[{]/.test((text || "").trim())) {
    try { return JSON.stringify(JSON.parse(text)); } catch { /* not JSON: send it as a request text */ }
  }
  return JSON.stringify({ request: text || "" });
}
window.ArkismCrew = (ctx) => {
  // 팀 이름(데이터)과 화면 문구가 한 줄에 섞일 때: 줄은 raw로 두고 문구만 만들 때 번역한다.
  const raw = (n) => { n.setAttribute("data-no-i18n", ""); return n; };
  const tr = (s) => (window.ArkismI18n ? window.ArkismI18n.translate(s) : s);
  const { state, $, el, icon, button, segment, render, request, query, notice, detailShell, section, copyButton, markSelected, ago, models } = ctx;
  const kit = window.ArkismAssist(ctx);
  const pins = window.ArkismPins?.(ctx); // 항상 넣는 자료 (pins.js)

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
    n8n: { label: "n8n으로 보내기", sub: "위 노드로 안 되는 연동", icon: "plug", group: "그 밖의 연동" }
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
    http: [["method", "방식", "select", [["GET", "GET"], ["POST", "POST"], ["PUT", "PUT"], ["PATCH", "PATCH"], ["DELETE", "DELETE"]]], ["url", "주소", "text", "https://"], ["headers", "헤더 (한 줄에 하나)", "area"], ["body", "본문", "area"], ["key", "쓸 키 이름", "text", "", "설정 › 모델 설정에 저장한 키"]],
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

  // ---------------------------------------------------------------- data
  const items = () => state.data.crew || [];
  // A workflow an agent works by belongs to that agent: no $ call, and no place in 자동화.
  const ownerOf = (w) => items().find((r) => r.crew.type === "agent" && r.crew.workflow && r.crew.workflow.replace(/^\$/, "") === w.crew.handle);
  const allWorkflows = () => items().filter((r) => r.crew.type === "workflow");
  const workflows = () => allWorkflows().filter((r) => !ownerOf(r));
  const personas = () => items().filter((r) => r.crew.type === "persona");
  // template: a saved agent definition (instructions·model·effort·skills). 새 워크플로우 can start from one.
  const templates = () => items().filter((r) => r.crew.type === "template");
  const byId = (id) => items().find((r) => r.id === id);
  const byHandle = (h) => workflows().find((r) => r.crew.handle === (h || "").replace(/^\$/, ""));
  async function reload(selectId) {
    state.data.crew = (await request(`/v1/crew?${query()}`)).items || [];
    if (selectId !== undefined) state.selected = selectId ? `crew:${selectId}` : null;
    render();
    if (wk.pop?.dialog.open) wk.redraw(); // an open expert popup lists the workflow just made (render skips it)
  }
  const ERRORS = {
    secret_detected: "키나 토큰처럼 보이는 값이 있어 저장하지 않았어요. 키는 설정 › 모델 설정에 넣어 주세요.",
    invalid_request: "빈 칸이나 형식을 확인해 주세요. $이름은 띄어쓰기 없이 글자·숫자·_·-만 써요. 앞으로 가는 선이 원을 이루면 저장되지 않아요(되돌아가는 선은 횟수를 정해요).",
    revision_conflict: "다른 곳에서 먼저 고쳤어요. 닫고 다시 열어 주세요.",
    conflict: "같은 $이름이 이미 있어요. 다른 이름을 써 주세요."
  };
  const errorText = (e) => ERRORS[e.message] || (e.message === "HTTP 409" ? ERRORS.conflict : `저장하지 못했어요 (${e.message}).`);
  async function save(rec, crew) {
    if (rec) return request(`/v1/crew/${encodeURIComponent(rec.id)}`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: rec.revision, crew }) });
    return request("/v1/crew", { method: "POST", body: JSON.stringify({ scope: state.scope, crew }) });
  }
  async function quickSave(rec, patch, okText) {
    try {
      const saved = await save(rec, { ...rec.crew, ...patch });
      Object.assign(rec, saved);
      if (okText) notice(okText);
      render(false);
      renderDetail();
    } catch (e) {
      notice(errorText(e));
    }
  }
  // 지우기: 정의를 영구히 지운다(지난 실행 기록은 남음). 보관과 달리 되돌릴 수 없어 이름을 한 번 더 묻는다.
  async function remove(rec) {
    const name = rec.crew?.name || "";
    if (!await ask(`'${name}' 워크플로우를 지울까요? 되돌릴 수 없어요. (지난 실행 기록은 남아요)`)) return false;
    try {
      await request(`/v1/crew/${encodeURIComponent(rec.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope, expected_revision: rec.revision }) });
      notice("지웠어요.");
      await reload(null);
      return true;
    } catch (e) {
      notice(errorText(e));
      return false;
    }
  }
  async function archive(rec) {
    if (!await ask(`'${rec.crew?.name || "이 워크플로우"}'를 보관할까요? 목록에서 사라지고 일정·웹훅도 멈춰요. 실행 기록은 남아요.`)) return;
    try {
      await request(`/v1/crew/${encodeURIComponent(rec.id)}`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: rec.revision, archive: true }) });
      notice("보관했어요. 기록은 남아 있어요.");
      await reload(null);
    } catch (e) {
      notice(errorText(e));
    }
  }

  // ---------------------------------------------------------------- graph helpers
  const WORK = (n) => !["start", "schedule", "webhook", "end"].includes(n.type);
  const isBack = (e) => (e.max || 0) > 0;
  const fwd = (g) => (g?.edges || []).filter((e) => !isBack(e));
  // Edges by the node they leave, so walks over the graph stay linear in its size.
  const outsOf = (edges) => {
    const m = new Map();
    for (const e of edges) (m.get(e.from) || m.set(e.from, []).get(e.from)).push(e);
    return m;
  };
  const nodeTitle = (n) => n.title || NODES[n.type]?.label || n.type;
  const stepCount = (g) => (g?.nodes || []).filter(WORK).length;
  const aiNodes = (g) => (g?.nodes || []).filter((n) => IS_AI(n.type));
  // One AI step and nothing else that works: the editor shows the simple form instead of the canvas (03d).
  const simpleNode = (g) => {
    const work = (g?.nodes || []).filter(WORK);
    return work.length === 1 && IS_AI(work[0].type) && !(g.edges || []).some(isBack) ? work[0] : null;
  };
  const modelOf = (n, x) => n.config?.model || (n.type === "expert" && byId(n.config?.expert)?.crew.model) || x?.model || "기본 모델";
  function levels(graph) {
    const g = graph || { nodes: [], edges: [] };
    const edges = fwd(g);
    const depth = {};
    const incoming = {};
    g.nodes.forEach((n) => { incoming[n.id] = 0; });
    edges.forEach((e) => { incoming[e.to] = (incoming[e.to] || 0) + 1; });
    const queue = g.nodes.filter((n) => !incoming[n.id]).map((n) => n.id);
    queue.forEach((id) => { depth[id] = 0; });
    const left = { ...incoming };
    const outs = outsOf(edges);
    for (const id of queue) { // the array iterator also visits ids pushed during the loop
      for (const e of outs.get(id) || []) {
        depth[e.to] = Math.max(depth[e.to] || 0, depth[id] + 1);
        if (--left[e.to] === 0) queue.push(e.to);
      }
    }
    const out = [];
    for (const n of g.nodes) {
      if (depth[n.id] === undefined) continue;
      (out[depth[n.id]] ||= []).push(n);
    }
    return out.filter(Boolean);
  }
  // Would a new forward edge from → to close a circle of forward edges? Then it has to be a back edge.
  function wouldCycle(graph, from, to) {
    if (from === to) return true;
    const seen = new Set();
    const outs = outsOf(fwd(graph));
    const walk = (id) => {
      if (id === from) return true;
      if (seen.has(id)) return false;
      seen.add(id);
      return (outs.get(id) || []).some((e) => walk(e.to));
    };
    return walk(to);
  }
  // The ports a node's outgoing edges can use. AI nodes name their own branches (the AI picks one), plus a free port.
  function portsOf(n, g) {
    if (PORTS[n.type]) return PORTS[n.type];
    if (n.type === "end") return [];
    if (IS_AI(n.type)) {
      const named = [...new Set((g.edges || []).filter((e) => e.from === n.id && e.port).map((e) => e.port))];
      return [...named.map((p) => [p, p]), ["", named.length ? "다른 갈래" : ""]];
    }
    return [["", ""]];
  }
  function defaultGraph(kind, x) {
    const tpl = kind.startsWith("tpl:") ? byId(kind.slice(4))?.crew : null;
    if (tpl) {
      // The template's instructions·model·effort·skills go into the one AI node; the engine reads the same keys.
      const config = { model: tpl.model || x.model || "", goal: "{{input}}", access: "read" };
      if (tpl.instructions) config.instructions = tpl.instructions;
      if (tpl.effort) config.effort = tpl.effort;
      // Templates name skills; the node keeps hub skill ids (a name with no match stays as written).
      const hub = state.data.skills || [];
      if (tpl.skills?.length) config.skills = tpl.skills.map((n) => (hub.find((k) => k.id === n || k.name === n)?.id || n)).join(",");
      return { nodes: [{ id: "start", type: "start", title: "시작", x: 40, y: 160 },
        { id: "ai1", type: "ai", title: tpl.name || "AI 에이전트", x: 320, y: 120, config },
        { id: "end", type: "end", title: "보고하고 끝", x: 620, y: 160, config: {} }],
      edges: [{ from: "start", to: "ai1" }, { from: "ai1", to: "end" }] };
    }
    if (kind === "simple") {
      return { nodes: [{ id: "start", type: "start", title: "시작", x: 40, y: 160 },
        { id: "ai1", type: "ai", title: x.name || "AI 에이전트", x: 320, y: 120, config: { model: x.model || "", goal: "{{input}}", access: "read" } },
        { id: "end", type: "end", title: "보고하고 끝", x: 620, y: 160, config: {} }],
      edges: [{ from: "start", to: "ai1" }, { from: "ai1", to: "end" }] };
    }
    return { nodes: [{ id: "start", type: "start", title: "시작", x: 40, y: 160 }, { id: "end", type: "end", title: "보고하고 끝", x: 420, y: 160, config: {} }], edges: [{ from: "start", to: "end" }] };
  }

  // 구조 검토 · 규칙 검사 (03c): what code can decide on its own, before any AI is asked.
  // POST is how most search APIs read, so only PUT·PATCH·DELETE count as changing something elsewhere.
  const IRREVERSIBLE = (n) => n.type === "github" || n.type === "notify" || (n.type === "http" && ["PUT", "PATCH", "DELETE"].includes((n.config?.method || "").toUpperCase()));
  function ruleChecks(x, g = x.graph) {
    const nodes = g?.nodes || [];
    const edges = fwd(g);
    const out = [];
    const targets = new Set(edges.map((e) => e.to));
    const outs = outsOf(edges);
    const roots = nodes.filter((n) => !targets.has(n.id));
    const seen = new Set();
    const walk = (id) => { if (seen.has(id)) return; seen.add(id); (outs.get(id) || []).forEach((e) => walk(e.to)); };
    roots.forEach((n) => walk(n.id));
    const orphan = nodes.filter((n) => !seen.has(n.id));
    const ends = nodes.filter((n) => !outs.has(n.id));
    out.push({ ok: !orphan.length && roots.length <= 1, label: "시작→끝 도달", detail: orphan.length ? `닿지 않는 노드: ${orphan.map(nodeTitle).join(", ")}` : roots.length > 1 ? `시작이 ${roots.length}개예요` : `끝 ${ends.length}곳` });
    const backs = (g?.edges || []).filter(isBack);
    out.push({ ok: true, label: "되돌아가기 제한", detail: backs.length ? backs.map((e) => `${nodeTitle(nodes.find((n) => n.id === e.from) || {})} → ${nodeTitle(nodes.find((n) => n.id === e.to) || {})} 최대 ${e.max}번`).join(" · ") : "되돌아가는 선이 없어요" });
    const selfCall = nodes.filter((n) => n.type === "subflow" && (n.config?.workflow || "").replace(/^\$/, "") === x.handle);
    out.push({ ok: !selfCall.length, label: "고리 없음", detail: selfCall.length ? "하위 워크플로우가 자기 자신을 불러요" : "하위 워크플로우가 자기를 부르지 않아요" });
    const noModel = aiNodes(g).filter((n) => !n.config?.model && !x.model);
    out.push({ ok: !noModel.length, label: "AI 노드 모델", detail: noModel.length ? `모델이 비었어요: ${noModel.map(nodeTitle).join(", ")} (기본 워커 모델을 정해도 돼요)` : `${aiNodes(g).length}개 모두 정해졌어요` });
    const risky = nodes.filter(IRREVERSIBLE).filter((n) => !upstream(g, n.id).some((u) => u.type === "approval"));
    if (risky.length) out.push({ ok: false, label: "되돌릴 수 없는 동작", detail: `${risky.map(nodeTitle).join(", ")} 앞에 사람 확인이 없어요` });
    const writers = levels(g).filter((lv) => lv.filter((n) => IS_AI(n.type) && n.config?.access === "write").length > 1);
    if (writers.length) out.push({ ok: true, label: "쓰기 노드", detail: "쓰기 노드끼리는 갈래가 나뉘어도 차례로 돌아요" });
    return out;
  }
  function upstream(g, id) {
    const out = new Map();
    const walk = (n) => fwd(g).filter((e) => e.to === n).forEach((e) => { if (!out.has(e.from)) { out.set(e.from, g.nodes.find((x) => x.id === e.from)); walk(e.from); } });
    walk(id);
    return [...out.values()].filter(Boolean);
  }

  // ---------------------------------------------------------------- runs
  // 실행: a run the host engine picks up within ten seconds. The editor opens the run view (03b) on it.
  async function startRun(rec, text, only, nodesFrom, until) {
    const g = rec.crew.graph;
    if (!stepCount(g)) { notice("아직 할 일이 없어요. 편집에서 노드를 넣어 주세요."); return null; }
    const nodes = Object.fromEntries((g.nodes || []).map((n) => [n.id, { status: "pending" }]));
    let loops;
    if (nodesFrom) {
      for (const [id, st] of Object.entries(nodesFrom.nodes || {})) if (nodes[id] && st.status === "done") nodes[id] = { status: "done", output: st.output, port: st.port, runs: st.runs };
      loops = nodesFrom.loops;
    }
    // 여기까지: what the chosen step does not need is marked skipped, so the run stops after it.
    if (until) { const need = new Set([until, ...upstream(g, until).map((n) => n.id)]); for (const id of Object.keys(nodes)) if (!need.has(id)) nodes[id] = { status: "skipped" }; }
    const run = { crew_id: rec.id, crew_revision: rec.revision, trigger: "manual", status: "running", input: runInput(text, only), nodes, ...(only ? { only } : {}), ...(loops ? { loops } : {}) };
    try {
      const r = await request("/v1/crew/runs", { method: "POST", body: JSON.stringify({ scope: state.scope, run }) });
      notice(only ? "이 노드만 실행을 맡겼어요. 최근 실행에서 볼 수 있어요." : until ? "여기까지 실행을 맡겼어요. 서버 실행기가 10초 안에 시작해요." : "실행을 맡겼어요. 서버 실행기가 10초 안에 시작해요.");
      return r;
    } catch (e) {
      if (nodesFrom) return startRun(rec, text, only);
      notice(`실행하지 못했어요 (${e.message}).`);
      return null;
    }
  }
  const RUN_LABEL = { succeeded: "완료", failed: "실패", waiting: "확인 대기", running: "실행 중", stopped: "멈춤" };
  const NODE_LABEL = { done: "완료", running: "실행 중", waiting: "기다리는 중", failed: "실패", skipped: "건너뜀", pending: "기다리는 중" };
  async function decide(run, approve) {
    const cr = run.crew_run;
    try {
      await request(`/v1/crew/runs/${encodeURIComponent(run.id)}/decision`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: run.revision, node_id: cr.waiting.node_id, approve }) });
      notice(approve ? "승인했어요. 다음 단계로 넘어가요." : "거절했어요. 그 갈래는 멈춰요.");
    } catch (e) {
      notice(e.message === "revision_conflict" ? "실행 상태가 바뀌었어요. 다시 열어 주세요." : `보내지 못했어요 (${e.message}).`);
    }
  }
  async function stopRun(run) {
    try {
      await request(`/v1/crew/runs/${encodeURIComponent(run.id)}/progress`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: run.revision, run: { ...run.crew_run, status: "stopped", waiting: undefined } }) });
      notice("멈췄어요. 돌던 단계는 끝까지 가고 다음 단계는 시작하지 않아요.");
    } catch (e) {
      notice(`멈추지 못했어요 (${e.message}).`);
    }
  }

  // ---------------------------------------------------------------- small builders
  function kv(rows) {
    const t = el("div", "crew-kv");
    for (const [k, v, aside] of rows) {
      if (v === null || v === undefined || v === "") continue;
      const r = el("div", "crew-kv-row");
      const val = el("div", "crew-kv-val");
      val.append(...(Array.isArray(v) ? v : [v]));
      r.append(el("span", "crew-kv-key", k), val);
      if (aside) r.append(aside);
      t.append(r);
    }
    return t;
  }
  function switchEl(on, label, onFlip) {
    const s = el("button", "switch");
    s.type = "button";
    s.setAttribute("role", "switch");
    s.setAttribute("aria-checked", String(!!on));
    s.setAttribute("aria-label", label);
    s.append(el("i"));
    s.addEventListener("click", (e) => { e.stopPropagation(); onFlip(!on); });
    return s;
  }
  function field(label, input, hint) {
    const f = el("label", "field", label);
    f.append(input);
    if (hint) f.append(el("small", "dim", hint));
    return f;
  }
  function input(value, attrs = {}) {
    const i = el(attrs.tag || "input", attrs.cls || "");
    if (attrs.tag === "textarea") i.rows = attrs.rows || 4;
    for (const [k, v] of Object.entries(attrs)) if (!["tag", "cls", "rows"].includes(k)) i[k] = v;
    i.value = value ?? "";
    return i;
  }
  function select(options, value) {
    const s = el("select");
    for (const [k, label] of options) { const o = el("option", "", label); o.value = k; o.selected = k === value; s.append(o); }
    return s;
  }
  function modelInput(value, placeholder = "deepseek-v4-flash", list = "crew-models") {
    const i = input(value, { maxLength: 80, placeholder });
    i.setAttribute("list", list);
    return i;
  }
  // The same live list as a plain choice (자동화 팝업): "" = the fallback, "auto" = the router picks; a saved model off the list stays choosable.
  function modelSelect(value, emptyLabel) {
    const ids = [...new Set([...(models?.enabledModels() || []), ...(value && value !== "auto" ? [value] : [])])];
    return select([["", emptyLabel], ["auto", "자동 — 라우터가 일마다 골라요"], ...ids.map((m) => [m, m])], value || "");
  }
  // only: keep the models this returns true for (the 이미지 picker lists vision models only).
  function modelList(id = "crew-models", only = null) {
    const dl = el("datalist");
    dl.id = id;
    // "auto" lets the router pick per run; the rest is the live list the runner reports (모델 설정), never a fixed one.
    const auto = el("option");
    auto.value = "auto";
    auto.label = tr("자동 — 라우터가 일마다 골라요");
    if (!only) dl.append(auto);
    // Grouped by provider, the provider's logo before its name; each model says on the right what it takes in. The server keeps a plain model name (no provider), so a name offered by several providers
    // says so on every copy: picking either saves the same name, and the runner picks the CLI from the name.
    const byProvider = models?.enabledByProvider?.() || [];
    const where = new Map();
    for (const p of byProvider) for (const m of p.models) where.set(m, [...(where.get(m) || []), p.name]);
    for (const p of byProvider) for (const m of p.models) {
      if (only && !only(m)) continue;
      const o = el("option");
      o.value = m;
      o.label = where.get(m).length > 1 ? where.get(m).join(" · ") : m; // the head already names a single provider
      o.dataset.group = p.name;
      o.dataset.groupIcon = p.id;
      o.dataset.tag = tr(window.ArkismBrand?.kindLabel(m) || "");
      dl.append(o);
    }
    return dl;
  }

  // ---------------------------------------------------------------- cron (5 fields, same rules as the run engine)
  const DOW = ["일", "월", "화", "수", "목", "금", "토"];
  function cronField(f, min, max) {
    const out = new Set();
    for (const part of f.split(",")) {
      const [range, stepRaw] = part.split("/");
      const step = Number(stepRaw || 1);
      let [a, b] = range === "*" ? [min, max] : range.split("-").map(Number);
      if (b === undefined) b = stepRaw ? max : a;
      if (!(step > 0) || Number.isNaN(a) || Number.isNaN(b)) return null;
      for (let v = a; v <= b; v += step) out.add(max === 7 && v === 7 ? 0 : v);
    }
    return out;
  }
  function parseCron(expr) {
    const f = (expr || "").trim().split(/\s+/);
    if (f.length !== 5) return null;
    const p = [cronField(f[0], 0, 59), cronField(f[1], 0, 23), cronField(f[2], 1, 31), cronField(f[3], 1, 12), cronField(f[4], 0, 7)];
    return p.some((x) => !x) ? null : { p, f };
  }
  function nextRun(expr, from = new Date()) {
    const c = parseCron(expr);
    if (!c) return null;
    const t = new Date(from);
    t.setSeconds(0, 0);
    for (let i = 0; i < 60 * 24 * 32; i++) {
      t.setMinutes(t.getMinutes() + 1);
      const [mi, h, dom, mon, dow] = c.p;
      const domAny = c.f[2] === "*", dowAny = c.f[4] === "*";
      const dayOk = domAny && dowAny ? true : domAny ? dow.has(t.getDay()) : dowAny ? dom.has(t.getDate()) : dom.has(t.getDate()) || dow.has(t.getDay());
      if (mi.has(t.getMinutes()) && h.has(t.getHours()) && mon.has(t.getMonth() + 1) && dayOk) return t;
    }
    return null;
  }
  function cronText(expr) {
    const c = parseCron(expr);
    if (!c) return expr || "—";
    const [m, h, dom, mon, dow] = c.f;
    if (!/^\d+$/.test(m) || !/^\d+$/.test(h) || mon !== "*") return expr;
    const at = `${h.padStart(2, "0")}:${m.padStart(2, "0")}`;
    if (dom === "*" && dow === "*") return `매일 ${at}`;
    if (dom === "*" && dow === "1-5") return `평일 ${at}`;
    if (dom === "*" && /^[0-7](,[0-7])*$/.test(dow)) return `${dow.split(",").map((d) => DOW[Number(d) % 7]).join("·")} ${at}`;
    if (dow === "*" && /^\d+$/.test(dom)) return `매달 ${dom}일 ${at}`;
    return expr;
  }
  const when = (d) => (d ? new Intl.DateTimeFormat(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric", weekday: "short", hour: "2-digit", minute: "2-digit" }).format(d) : "—");
  function triggerText(c) {
    const t = c.trigger || {};
    if (t.kind === "schedule") return `일정 · ${cronText(t.cron)}`;
    if (t.kind === "webhook") return `웹훅${t.label ? ` · ${t.label}` : ""}`;
    return "";
  }
  const triggered = (x) => !!(x.trigger && x.enabled);

  // ---------------------------------------------------------------- list (v4 · 198:286)
  // A small mark for a model: the maker's logo when the app has one, otherwise a robot.
  function modelMark(model) {
    const m = (model || "").toLowerCase();
    const box = el("span", "wf-mark");
    box.append(window.ArkismBrand?.mark(m) || icon(m === "auto" ? "sparkle" : "robot"));
    return box;
  }
  // 전문가 아바타 (Figma 아바타 635:57217): a preset drawing on one of the server's background colours
  // (crewAvatarBgs in internal/memory/crew.go); without one the model mark stands in.
  const AVATAR_BGS = [["cream", "크림"], ["sand", "모래"], ["tan", "황토"], ["rose", "장미"], ["lilac", "라일락"], ["sky", "하늘"], ["mint", "민트"], ["sage", "세이지"]];
  const AVATARS = ("001 005 006 007 011 014 015 016 019 021 026 027 032 034 039 042 043 044 048 050 051 054 057 061 062 063 064 069 071 072 " +
    "075 078 080 084 087 091 092 093 094 097 098 101 102 103 104 105 106 107 108 109 110 111 112 113 114 115 116 117 118 119 120 121 122 123 124 " +
    "125 126 127 128 129 130 131 132 133 134 135 136 137 138 139 140 141 142 143 144 145 146 147 148 149 150 151 152 153 154 155 156 157 158 159").split(" ");
  const validAvatar = (a) => !!a && AVATARS.includes(a.art) && AVATAR_BGS.some(([k]) => k === a.bg);
  function faceImg(art, bg) {
    const box = el("span", "wf-mark wk-face");
    box.style.setProperty("--face-bg", `var(--avatar-${bg})`);
    const img = el("img");
    img.src = `img/avatars/${art}.png`;
    img.alt = "";
    box.append(img);
    return box;
  }
  const avatarMark = (x) => validAvatar(x?.avatar) ? faceImg(x.avatar.art, x.avatar.bg) : modelMark(x?.model);
  // Resolves to {art, bg}, null to clear, or undefined when cancelled.
  function pickAvatar(cur) {
    return new Promise((resolve) => {
      const dlg = el("dialog", "crew-dialog wk-face-dialog");
      const form = el("form");
      form.method = "dialog";
      const pick = { art: validAvatar(cur) ? cur.art : AVATARS[0], bg: validAvatar(cur) ? cur.bg : "cream" };
      const radio = (name, value, label, on) => {
        const opt = el("label", `wk-face-opt ${name}`);
        const r = el("input");
        r.type = "radio";
        r.name = name;
        r.value = value;
        r.checked = on;
        r.setAttribute("aria-label", label);
        r.addEventListener("change", () => { pick[name] = value; if (name === "bg") grid.style.setProperty("--face-bg", `var(--avatar-${value})`); });
        opt.append(r);
        return opt;
      };
      const bgs = el("div", "wk-face-bgs");
      bgs.setAttribute("role", "radiogroup");
      bgs.setAttribute("aria-label", "배경색");
      for (const [k, label] of AVATAR_BGS) {
        const o = radio("bg", k, label, k === pick.bg);
        o.style.setProperty("--face-bg", `var(--avatar-${k})`);
        bgs.append(o);
      }
      const grid = el("div", "wk-face-grid");
      grid.setAttribute("role", "radiogroup");
      grid.setAttribute("aria-label", "아바타");
      grid.style.setProperty("--face-bg", `var(--avatar-${pick.bg})`);
      for (const n of AVATARS) {
        const o = radio("art", n, `아바타 ${n}`, n === pick.art);
        const img = el("img");
        img.src = `img/avatars/${n}.png`;
        img.alt = "";
        img.loading = "lazy";
        o.append(img);
        grid.append(o);
      }
      const body = el("div", "dialog-body");
      body.append(bgs, grid);
      const head = el("div", "dialog-head");
      head.append(el("h2", "", "아바타 고르기"));
      const foot = el("div", "dialog-foot");
      const clear = button("모델 아이콘 쓰기", "ghost");
      clear.type = "submit";
      clear.value = "clear";
      clear.classList.add("wk-face-clear");
      const cancel = button("취소", "ghost");
      cancel.type = "submit";
      cancel.value = "cancel";
      const ok = button("고르기", "primary");
      ok.type = "submit";
      ok.value = "ok";
      foot.append(clear, cancel, ok);
      form.append(head, body, foot);
      let answer;
      form.addEventListener("submit", (e) => { const v = e.submitter?.value; answer = v === "ok" ? { ...pick } : v === "clear" ? null : undefined; });
      dlg.append(form);
      dlg.addEventListener("close", () => { dlg.remove(); resolve(answer); });
      document.body.append(dlg);
      dlg.showModal();
      (grid.querySelector("input:checked") || grid.querySelector("input")).focus();
      grid.querySelector("input:checked")?.closest("label").scrollIntoView({ block: "center" });
    });
  }
  // The latest runs of every workflow, for the approvals card on top (a workflow's own runs are in its popup's 기록 tab).
  let recent = null;
  let recentBusy = false;
  let waiting = [];
  async function loadRecent() {
    if (recentBusy) return;
    recentBusy = true;
    // Waiting runs come on their own (status=waiting): an approval older than the latest 30 runs still asks.
    const [all, wait] = await Promise.all([
      request(`/v1/crew/runs?${query({ limit: 30 })}`).then((r) => r.items || [], () => null),
      request(`/v1/crew/runs?${query({ status: "waiting", limit: 100 })}`).then((r) => r.items || [], () => null)]);
    recent = all || recent || [];
    waiting = wait || [];
    recentBusy = false;
    if (state.view === "crew") { drawCards(); drawWaiting(); }
  }
  const waitingRuns = () => {
    const seen = new Set();
    return [...waiting, ...(recent || [])].filter((x) => x.crew_run?.status === "waiting" && x.crew_run.waiting?.kind === "approval" && !seen.has(x.id) && seen.add(x.id));
  };
  function categories(list) {
    const counts = new Map();
    list.forEach((r) => { if (r.crew.category) counts.set(r.crew.category, (counts.get(r.crew.category) || 0) + 1); });
    return [...counts.entries()].sort((a, b) => a[0].localeCompare(b[0], "ko"));
  }
  const inTab = (r, tab) => tab === "all" || (tab === "trigger" ? triggered(r.crew) : !triggered(r.crew));
  const shownCards = () => workflows().filter((r) => inTab(r, state.crewTab) && (state.crewCat === "all" || r.crew.category === state.crewCat) &&
    (!state.query || `${r.crew.name} ${r.crew.handle || ""} ${r.crew.role || ""}`.toLowerCase().includes(state.query)));
  // 자동화 상세·새로 만들기 팝업 (crew-automation.js): built on first use, once every helper below exists.
  let autoKit = null;
  const getAuto = () => (autoKit ||= window.ArkismAutomation({
    state, el, icon, button, notice, request, query, byId, kit, catMenu: ctx.catMenu, catChipBody: ctx.catChipBody, NODES, IS_AI, WORK, UNTITLED, RUN_LABEL,
    levels, nodeTitle, stepCount, isBack, modelOf, modelMark, avatarMark, personas, agents, workflows, ownerOf, categories, ruleChecks, waitingRuns,
    cronText, nextRun, parseCron, when, switchEl, copyButton, coverCredit, coverPicker, input, select, modelInput, modelSelect, freeHandle, layoutGraph, defaultGraph,
    save, reload, archive, remove, errorText, startRun, decide, stopRun, askTask, openGraph, runRequest, readable, nodeDur, spanText, runMs, runTookText: runTook, ago,
    duplicate: (r) => dupButton(r).click(),
    refreshRuns: async () => { recent = null; await loadRecent(); },
    openSettingsNew: () => openSettings(null),
    openSettings: (r) => openSettings(r),
    openAutomation: (id, tab) => { const r = byId(id); if (r) getAuto().open(r, tab); },
    afterClose: () => { markSelected(); if (state.view === "crew") drawCards(); }
  }));
  function renderList() {
    if (!TABS.some(([k]) => k === state.crewTab)) state.crewTab = "all";
    const tab = state.crewTab;
    const all = workflows().filter((r) => inTab(r, tab));
    const cats = categories(all);
    if (state.crewCat !== "all" && !cats.some(([c]) => c === state.crewCat)) state.crewCat = "all";
    // 카테고리 칩 · 검색 · 실행 방식 · 새 워크플로우: one row (the search box here and the one on top share state.query)
    $("#app").classList.add("wide"); // the card opens a popup: no side panel
    const bar = el("div", "wf-bar auto-bar-top");
    const chips = el("div", "auto-cats");
    const chip = (k, label, n) => {
      const c = el("button", `auto-cat-chip${state.crewCat === k ? " on" : ""}`);
      c.type = "button";
      c.setAttribute("aria-pressed", String(state.crewCat === k));
      c.append(icon(ctx.catIcon(k === "all" ? "" : k)), label, el("small", "", String(n)));
      c.addEventListener("click", () => { state.crewCat = k; state.selected = null; render(); });
      return c;
    };
    chips.append(chip("all", "모든 카테고리", all.length), ...cats.map(([c, n]) => chip(c, c, n)));
    bar.append(chips, el("span", "grow"));
    const search = el("label", "search wf-search");
    const q = el("input");
    q.type = "search";
    q.placeholder = "이름이나 $이름 검색";
    q.setAttribute("aria-label", "워크플로우 검색");
    q.value = $("#search")?.value || "";
    q.addEventListener("input", () => {
      state.query = q.value.trim().toLowerCase();
      if ($("#search")) $("#search").value = q.value;
      drawCards();
    });
    search.append(icon("magnifying-glass"), q);
    const how = el("select", "wf-select");
    how.setAttribute("aria-label", "실행 방식");
    for (const [k, l] of TABS) {
      const o = el("option", "", k === "all" ? "모든 실행 방식" : `${l} · ${workflows().filter((r) => inTab(r, k)).length}`);
      o.value = k;
      o.selected = k === tab;
      how.append(o);
    }
    how.addEventListener("change", () => { state.crewTab = how.value; state.crewCat = "all"; state.selected = null; render(); });
    const aiNew = el("button", "ex-ai-btn"); // 새 워크플로우 with the AI chat already open (ai-chat.js look)
    aiNew.type = "button";
    aiNew.append(icon("sparkle"), el("span", "", "AI로 초안"));
    aiNew.addEventListener("click", () => getAuto().create(true));
    bar.append(search, how, aiNew, button("+ 새 워크플로우", "primary", null, () => getAuto().create()));
    $("#controls").replaceChildren(bar);
    $("#chips").replaceChildren();
    $("#stats").replaceChildren();
    const main = $("#table");
    main.className = "wf-main";
    main.replaceChildren(el("div", "wf-grid")); // 최근 실행 moved into the popup's 기록 tab (198:286)
    drawWaiting();
    drawCards();
    void loadRecent();
  }
  // 확인해 주세요: runs that wait for a person's answer, with the answer buttons right there.
  function drawWaiting() {
    const box = $("#stats");
    if (!box || state.view !== "crew") return;
    const out = [];
    // 이 PC의 Arkism에는 워크플로우 실행기가 없다: 만들고 저장은 되지만 실행은 서버에서.
    if (state.mode === "local") out.push(el("p", "local-note wf-local", "이 PC 모드에서는 워크플로우를 만들고 저장할 수 있어요. 실행과 $이름 호출은 서버에 연결했을 때 돼요."));
    for (const run of waitingRuns()) {
      const rec = byId(run.crew_run.crew_id);
      const node = (rec?.crew.graph?.nodes || []).find((n) => n.id === run.crew_run.waiting.node_id);
      const card = el("div", "wf-attn");
      const t = el("div", "");
      t.append(el("strong", "", `확인해 주세요 · ${rec?.crew.name || "워크플로우"}`), el("p", "", `${node ? `${nodeTitle(node)} — ` : ""}${run.crew_run.waiting.question || node?.config?.question || "다음 단계로 갈까요?"}`));
      const acts = el("div", "wf-attn-actions");
      const after = async () => { recent = null; await loadRecent(); };
      acts.append(button("거절", "", null, async () => { await decide(run, false); void after(); }), button("승인", "primary", null, async () => { await decide(run, true); void after(); }));
      if (rec) acts.prepend(button("실행 보기", "ghost", null, () => openGraph(rec, { run })));
      card.append(t, acts);
      out.push(card);
    }
    box.replaceChildren(...out);
  }
  function drawCards() {
    const grid = $("#table .wf-grid");
    if (!grid) return;
    grid.replaceChildren(...shownCards().map(workflowCard), newCard());
    markSelected();
  }
  // Card (198:286): photo, the 일정 badge and the name. The 일정·웹훅 switch, one line and $이름 · 단계 live in the popup.
  // A cover photo (배경 사진) sits as a band on top of the card.
  function workflowCard(r) {
    const x = r.crew;
    const c = el("button", `crew-card workflow wf-card${x.cover?.url ? " has-cover" : ""}${state.selected === `crew:${r.id}` ? " selected" : ""}`);
    c.type = "button";
    c.dataset.id = `crew:${r.id}`;
    c.addEventListener("click", () => { state.selected = `crew:${r.id}`; markSelected(); getAuto().open(r); });
    if (x.cover?.url) {
      const img = el("img", "crew-cover-img");
      img.alt = "";
      img.loading = "lazy";
      img.src = x.cover.url;
      img.addEventListener("error", () => img.remove());
      c.append(img);
    }
    const top = el("div", "crew-cover-top");
    // The card says only when it runs (스케줄 or 웹훅); the time and the webhook's source are in the popup.
    const kind = x.trigger?.kind === "schedule" ? "스케줄" : "웹훅";
    const when2 = x.trigger ? (x.enabled ? kind : `${kind} · 꺼짐`) : "수동 실행";
    // 일정 배지: as long as its text, pinned to the card's top right, cut with … past the card's width.
    const badge = el("span", `wf-card-when${triggered(x) ? " on" : ""}`);
    badge.title = x.trigger ? triggerText(x) : when2;
    badge.append(el("i"), el("span", "", when2));
    top.prepend(badge);
    c.title = [x.role, `$${x.handle}`].filter(Boolean).join(" · ");
    c.append(top, el("strong", "crew-cover-title", x.name));
    return c;
  }
  function newCard() {
    const c = el("button", "crew-card new workflow wf-card");
    c.type = "button";
    c.append(icon("plus"), el("strong", "", "새 워크플로우"), el("small", "", "AI 하나로 간단히 시작하거나 단계를 이어 그려요"));
    c.addEventListener("click", () => getAuto().create());
    return c;
  }
  const headActions = () => []; // 새 워크플로우 sits in the list's own bar

  // ---------------------------------------------------------------- 새 워크플로우: AI로 만들기 / 직접 만들기
  const UNTITLED = "제목 없음";
  function untitledHandle() {
    const taken = new Set(workflows().map((r) => r.crew.handle));
    let i = 1;
    while (taken.has(`제목없음${i}`)) i++;
    return `제목없음${i}`;
  }
  // Nodes from the AI carry no place on the canvas: lay them out left to right by distance from the start.
  function layoutGraph(g) {
    // The server keeps node settings as text: numbers and lists from the AI become strings, unknown edge ends are dropped.
    g.nodes = g.nodes.filter((n) => n?.id && NODES[n.type]).slice(0, 100).map((n) => ({ id: String(n.id).slice(0, 40), type: n.type, title: String(n.title || NODES[n.type].label).slice(0, 80),
      config: Object.fromEntries(Object.entries(n.config || {}).slice(0, 20).map(([k, v]) => [k, typeof v === "string" ? v : JSON.stringify(v)])) }));
    const ids = new Set(g.nodes.map((n) => n.id));
    g.edges = g.edges.filter((e) => ids.has(e.from) && ids.has(e.to) && e.from !== e.to).map(({ from, to, port, max }) => ({ from, to, ...(port ? { port: String(port).slice(0, 20) } : {}), ...(max ? { max: Math.min(10, Number(max) || 0) } : {}) }));
    const depth = {};
    const roots = g.nodes.filter((n) => !g.edges.some((e) => e.to === n.id)).map((n) => n.id);
    const queue = (roots.length ? roots : [g.nodes[0]?.id]).filter(Boolean).map((id) => [id, 0]);
    while (queue.length) {
      const [id, d] = queue.shift();
      if (depth[id] !== undefined || d > g.nodes.length) continue;
      depth[id] = d;
      for (const e of g.edges) if (e.from === id) queue.push([e.to, d + 1]);
    }
    const rows = {};
    for (const n of g.nodes) {
      const d = depth[n.id] ?? 0;
      rows[d] = (rows[d] || 0) + 1;
      Object.assign(n, { x: 40 + d * 280, y: 40 + (rows[d] - 1) * 140, config: n.config || {} });
    }
    return g;
  }
  function freeHandle(name) {
    const h = name === UNTITLED ? "" : name.replace(/[^\p{L}\p{N}_-]/gu, "").slice(0, 40);
    return h && !workflows().some((r) => r.crew.handle === h) ? h : untitledHandle();
  }
  // An untitled workflow gets its name·설명 from the AI once it has a few real steps.
  async function autoTitle(rec) {
    const steps = (rec.crew.graph?.nodes || []).filter((n) => !["start", "end", "schedule", "webhook"].includes(n.type));
    if (rec.crew.name !== UNTITLED || steps.length < 2) return;
    try {
      const a = await kit.assist("title", `Give this workflow a title and a one-line description.\nSteps: ${JSON.stringify(steps.map(({ type, title, config }) => ({ type, title, config }))).slice(0, 6000)}`, { purpose: "workflow" });
      if (a.status !== "done") return;
      const j = kit.parseJSON(a.output);
      const name = String(j?.name || "").trim().slice(0, 60);
      if (!name || name === UNTITLED) return;
      const cur = byId(rec.id) || rec;
      if (cur.crew.name !== UNTITLED) return; // the user named it meanwhile
      const handle = cur.crew.handle.startsWith("제목없음") ? freeHandle(name) : cur.crew.handle;
      const saved = await save(cur, { ...cur.crew, name, handle, role: cur.crew.role || String(j.role || "").slice(0, 200) });
      await reload();
      if (ed.rec?.id === saved.id) { ed.rec = byId(saved.id) || saved; if (state.view === "graph") render(false); }
      notice(`AI가 제목을 '${name}'(으)로, 부르는 이름을 $${handle}로 붙였어요. 설정에서 바꿀 수 있어요.`);
    } catch { /* naming is best effort */ }
  }

  function runRequest(cr) {
    try { return JSON.parse(cr.input || "{}").request || ""; } catch { return ""; }
  }
  function spanText(ms) {
    const s = Math.max(0, Math.round(ms / 1000));
    if (s < 60) return `${s}초`;
    if (s < 3600) return `${Math.round(s / 60)}분`;
    return `${Math.floor(s / 3600)}시간 ${Math.round((s % 3600) / 60)}분`;
  }
  function runMs(run) {
    const ends = Object.values(run.crew_run?.nodes || {}).map((n) => n.finished_at).filter(Boolean).map((t) => new Date(t).getTime());
    return ends.length && run.created_at ? Math.max(...ends) - new Date(run.created_at).getTime() : 0;
  }
  const runTook = (run) => (runMs(run) ? spanText(runMs(run)) : "");

  // ---------------------------------------------------------------- detail (198:286 오른쪽)
  function renderDetail() {
    const r = state.selected?.startsWith("crew:") ? byId(state.selected.slice(5)) : null;
    if (!r || r.crew.type !== "workflow") return summary();
    workflowDetail(r);
  }
  function summary() {
    const body = detailShell("워크플로우");
    body.classList.add("wf-detail");
    const head = el("div", "wf-d-title");
    head.append(el("h2", "", "워크플로우"),
      el("p", "", "어느 에이전트에서든 $이름으로 불러요. 부르면 서버 실행기가 그래프대로 돌리고, 부른 자리에는 진행과 결과를 보고해요. AI 칸은 칸마다 정한 모델이 맡아요."));
    const s = section("지금");
    s.append(kv([["워크플로우", `${workflows().length}개`], ["일정·웹훅", `${workflows().filter((r) => triggered(r.crew)).length}개 켜짐`], ["페르소나", `${personas().length}개 저장`], ["에이전트", `${agents().length}명`]]));
    body.append(head, s, el("p", "meta", "카드를 누르면 여기서 실행하고 고쳐요."));
  }
  const dupButton = (r) => button("복제", "", null, () => {
    const copy = structuredClone(r.crew);
    copy.name = `${copy.name} 복사`.slice(0, 60);
    copy.handle = `${copy.handle}2`.slice(0, 40);
    copy.enabled = false;
    openSettings(null, copy);
  });
  const linkBtn = (label, onClick) => { const b = el("button", "wf-link", label); b.type = "button"; b.addEventListener("click", onClick); return b; };
  function workflowDetail(r) {
    const x = r.crew;
    const body = detailShell("선택한 워크플로우");
    body.classList.add("wf-detail");
    const head = el("div", "wf-d-title");
    head.append(el("h2", "", x.name), el("p", "", x.role || ""));
    const chips = el("div", "wf-chips");
    if (x.category) { const c = el("span", "wf-chip"); c.append(icon(ctx.catIcon(x.category)), x.category); chips.append(c); }
    const handle = el("span", "wf-chip mono", `$${x.handle}`);
    chips.append(handle, copyButton(`$${x.handle}`));
    head.append(chips);
    const credit = coverCredit(x);
    if (credit) head.append(credit);
    body.append(head);
    // 확인해 주세요: what the rule checks found, before anything else.
    const checks = ruleChecks(x);
    const bad = checks.filter((c) => !c.ok);
    if (bad.length) {
      const card = el("div", "wf-attn small");
      const t = el("div", "");
      t.append(el("strong", "", `구조 검토 · 확인할 곳 ${bad.length}`));
      for (const c of bad) t.append(el("p", "", `${c.label}: ${c.detail}`));
      card.append(t, button("구조 검토", "", null, () => openGraph(r, { review: true })));
      body.append(card);
    }
    const runWrap = el("div", "");
    runWrap.hidden = true;
    const runBtn = button("실행", "primary", null, () => {
      runWrap.hidden = !runWrap.hidden;
      if (!runWrap.firstChild) runWrap.append(runBox(r));
      runWrap.querySelector("input")?.focus();
    });
    const acts = el("div", "crew-actions wf-acts");
    // An owned workflow runs only through its agent (@); the server refuses a manual run.
    if (!ownerOf(r)) acts.append(runBtn);
    acts.append(button("편집", "", null, () => openGraph(r)), dupButton(r), button("설정", "ghost", null, () => openSettings(r)), button("보관", "ghost", null, () => archive(r)));
    body.append(acts, runWrap);

    const run = section("실행", "실행기가 노드를 따라가요");
    const stops = el("div", "wf-chips");
    const backs = (x.graph?.edges || []).filter(isBack);
    const chips2 = [x.limits?.max_minutes ? `${x.limits.max_minutes}분` : "", x.limits?.max_steps ? `단계 ${x.limits.max_steps}번` : "", backs.length ? `되돌아가기 ${Math.max(...backs.map((e) => e.max))}번` : ""].filter(Boolean);
    (chips2.length ? chips2 : ["정하지 않음"]).forEach((t) => stops.append(el("span", "wf-chip", t)));
    const next = x.trigger?.kind === "schedule" && x.enabled ? nextRun(x.trigger.cron) : null;
    const model = el("span", "wf-model");
    model.append(modelMark(x.model), el("span", "mono", x.model || "노드마다 정해요"));
    const trig = el("span", "", x.trigger ? triggerText(x) : "없어요");
    const trigSub = el("small", "dim", x.trigger ? (x.enabled ? (next ? `다음 ${when(next)}` : "켜져 있어요") : "꺼져 있어요") : "$이름·화면에서만 돌아요");
    run.append(kv([
      ["모델", [model, el("small", "dim", "워커 기본 모델")], linkBtn("바꾸기", () => openSettings(r))],
      ["멈추는 조건", stops, linkBtn("바꾸기", () => openSettings(r))],
      ["일정·웹훅", [trig, trigSub], x.trigger ? switchEl(x.enabled, "일정·웹훅 켜기", (on) => quickSave(r, { enabled: on })) : linkBtn("만들기", () => openSettings(r))]
    ]));
    if (x.trigger?.kind === "webhook") {
      // The token never comes to the app: the owner prints the address and token on the server with this command.
      const cmd = `arkism-crew hook-url ${r.id}`;
      const line = el("div", "wf-hook");
      line.append(el("code", "mono", cmd), copyButton(cmd));
      run.append(line, el("p", "meta", "웹훅 주소와 토큰은 서버에서 이 명령으로 받아요. 토큰은 Arkism에 저장하지 않아요."));
    }
    run.append(el("p", "meta", `어느 에이전트에서 $${x.handle}로 부르든 그래프대로 돌고, 부른 자리에는 진행과 결과를 보고해요.`));
    body.append(run);

    const ais = aiNodes(x.graph);
    if (ais.length) {
      const s = section("AI 노드", `${ais.length}개`);
      const list = el("div", "wf-rows");
      for (const n of ais) {
        const row = el("button", "wf-row");
        row.type = "button";
        row.title = "눌러서 이 노드 고치기";
        row.addEventListener("click", () => openGraph(r, { select: n.id }));
        const who = el("div", "");
        const persona = byId(n.config?.persona);
        const expert = n.type === "expert" ? byId(n.config?.expert) : null;
        const who0 = n.type === "expert" ? "전문가" : "에이전트";
        const kind = n.type === "ai_once" ? "한 번 묻기" : n.config?.collab === "on" ? `${who0} · 협업 워커 ${n.config?.max_workers || 5}` : who0;
        who.append(el("strong", "", nodeTitle(n)), el("small", "", [kind, expert?.crew.name || persona?.crew.name].filter(Boolean).join(" · ")));
        row.append(expert ? avatarMark(expert.crew) : modelMark(modelOf(n, x)), who, el("span", "mono dim", modelOf(n, x)));
        list.append(row);
      }
      s.append(list);
      body.append(s);
    }
    const order = section("진행 순서", `${stepCount(x.graph)}단계`);
    order.querySelector("h3").append(linkBtn("그래프로 편집", () => openGraph(r, { graphMode: true })));
    const ol = el("ol", "crew-steps wf-steps");
    const backTargets = new Set(backs.map((e) => e.to));
    levels(x.graph).map((lv) => lv.filter(WORK)).filter((lv) => lv.length).forEach((lv, i) => {
      const li = el("li", "crew-step");
      li.tabIndex = 0;
      li.title = "눌러서 이 단계 고치기";
      li.addEventListener("click", () => openGraph(r, { select: lv[0].id, graphMode: true }));
      li.addEventListener("keydown", (e) => { if (e.key === "Enter") openGraph(r, { select: lv[0].id, graphMode: true }); });
      const how = lv.length > 1 ? "동시에" : lv[0].type === "approval" ? "확인 후" : backTargets.has(lv[0].id) ? "↺ 되돌아올 수 있어요" : "";
      li.append(el("span", "mono", String(i + 1).padStart(2, "0")), el("strong", "", lv.map(nodeTitle).join(" · ")));
      if (how) li.append(el("small", "", how));
      ol.append(li);
    });
    order.append(ol);
    body.append(order);
    if (!bad.length) {
      const ok = el("p", "wf-ok");
      ok.append(icon("check"), "규칙 검사를 모두 통과했어요 ·");
      ok.append(linkBtn("구조 검토", () => openGraph(r, { review: true })));
      body.append(ok);
    }
  }
  function runBox(rec) {
    const box = el("div", "try-box");
    const row = el("div", "try-row");
    const q = el("input");
    q.placeholder = `$${rec.crew.handle}에게 맡길 일…`;
    const go2 = el("button", "icon-btn");
    go2.type = "button";
    go2.setAttribute("aria-label", "실행");
    go2.append(icon("play"));
    const send = async () => {
      go2.disabled = true;
      const r = await startRun(rec, q.value.trim());
      go2.disabled = false;
      if (r) openGraph(rec, { run: r });
    };
    go2.addEventListener("click", send);
    q.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.isComposing) void send(); });
    row.append(q, go2);
    box.append(row);
    return box;
  }
  // A summary that is a node's JSON output reads as its answer text, not as raw JSON.
  function readable(text) {
    if (!text || !/^[[{]/.test(text.trim())) return text || "";
    try {
      const v = JSON.parse(text);
      const pick = (o) => (o && typeof o === "object" ? (typeof o.output === "string" ? o.output : typeof o.summary === "string" ? o.summary : typeof o.text === "string" ? o.text : null) : null);
      const direct = pick(v);
      if (direct) return direct.slice(0, 200);
      for (const x of Object.values(v || {})) { const t = typeof x === "string" && /^[[{]/.test(x) ? pick((() => { try { return JSON.parse(x); } catch { return null; } })()) : pick(x); if (t) return t.slice(0, 200); }
      return JSON.stringify(v).slice(0, 120);
    } catch { return text.slice(0, 200); }
  }
  function coverCredit(x) {
    const cv = x.cover;
    if (!cv) return null;
    const p = el("small", "crew-credit dim", "사진 ");
    const who = el("a", "", cv.author);
    who.href = cv.author_url;
    const site = el("a", "", "Unsplash");
    site.href = "https://unsplash.com/?utm_source=arkism&utm_medium=referral";
    for (const a of [who, site]) { a.target = "_blank"; a.rel = "noreferrer"; }
    p.append(who, " · ", site);
    return p;
  }

  // ---------------------------------------------------------------- 배경 사진 (03h): Unsplash through the server (the key stays on the server)
  // card() gives what the list card shows (name, role, category, meta) so the preview reads like the real card.
  function coverPicker(initial, hint, card) {
    let cover = initial || null;
    let page = 1;
    const wrap = el("div", "crew-cover-pick");
    const preview = el("div", "crew-cover-preview");
    const drawPreview = () => {
      preview.replaceChildren();
      const x = card();
      const mini = el("div", "crew-card wf-card wf-preview has-cover");
      if (cover) {
        const img = el("img", "crew-cover-img");
        img.alt = "";
        img.src = cover.thumb;
        mini.append(img);
      } else mini.append(el("span", "wf-preview-empty", "배경 없음"));
      const top = el("div", "crew-cover-top");
      if (x.category) { const c = el("span", "wf-chip"); c.append(icon(ctx.catIcon(x.category)), x.category); top.append(c); }
      const foot = el("div", "crew-cover-foot");
      foot.append(el("small", "wf-card-meta", x.meta || ""));
      mini.append(top, el("strong", "crew-cover-title", x.name || "이름"));
      if (x.role) mini.append(el("p", "wf-card-role", x.role));
      mini.append(foot);
      preview.append(mini);
      const credit = cover ? coverCredit({ cover }) : null;
      if (credit) preview.append(credit);
      clear.hidden = !cover;
    };
    const search = input(hint, { maxLength: 100, placeholder: "영어로 찾으면 더 많아요 (예: code, sunrise, desk)" });
    const go = button("찾기", "", "magnifying-glass", () => find(1));
    const clear = button("배경 빼기", "ghost small", null, () => { cover = null; drawPreview(); });
    const row = el("div", "crew-cover-search");
    row.append(search, go);
    const grid = el("div", "crew-cover-grid");
    const more = button("더 보기", "ghost small", null, () => find(page + 1));
    more.hidden = true;
    const status = el("small", "dim");
    search.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); void find(1); } });
    async function find(n) {
      const q = search.value.trim();
      if (!q) return;
      go.disabled = more.disabled = true;
      status.textContent = "찾는 중…";
      try {
        const res = await request(`/v1/unsplash/search?${query({ q, page: n })}`);
        page = n;
        if (n === 1) grid.replaceChildren();
        for (const ph of res.items) {
          const b = el("button", "crew-cover-thumb");
          b.type = "button";
          b.title = `${ph.alt || ""} · ${ph.author}`.replace(/^ · /, "");
          const img = el("img");
          img.alt = ph.alt || "";
          img.loading = "lazy";
          img.src = ph.thumb;
          b.append(img);
          b.addEventListener("click", () => {
            cover = { id: ph.id, url: ph.url, thumb: ph.thumb, author: ph.author, author_url: ph.author_url };
            grid.querySelectorAll(".on").forEach((x) => x.classList.remove("on"));
            b.classList.add("on");
            b.dataset.picked = "✓ 고름";
            drawPreview();
            request(`/v1/unsplash/photos/${encodeURIComponent(ph.id)}/use`, { method: "POST", body: JSON.stringify({ scope: state.scope }) }).catch(() => {});
          });
          grid.append(b);
        }
        status.textContent = grid.childElementCount ? "" : "맞는 사진이 없어요. 다른 말로 찾아 보세요.";
        more.hidden = page >= Math.min(res.total_pages || 1, 50); // the server stops at page 50
      } catch (e) {
        status.textContent = e.message === "dependency_unavailable"
          ? "사진 검색을 쓸 수 없어요. 서버에 Unsplash 키(MEMORY_UNSPLASH_ACCESS_KEY)가 있는지 확인해 주세요."
          : `찾지 못했어요 (${e.message}).`;
      } finally {
        go.disabled = more.disabled = false;
      }
    }
    const head = el("div", "crew-cover-head");
    const credits = el("div", "wf-preview-foot");
    credits.append(clear);
    head.append(preview, credits);
    wrap.append(row, status, grid, more);
    drawPreview();
    // side: the preview card and 배경 빼기, which the window shows in its right column (198:2952 · 198:3038).
    return { node: wrap, side: head, value: () => cover, refresh: drawPreview };
  }

  // ---------------------------------------------------------------- 새 워크플로우 · 설정 (03f · 03h)
  let dialog = null;
  function openSettings(rec, preset, fromEditor = false, startWith = "") {
    dialog?.remove();
    const base = preset || rec?.crew || { type: "workflow", name: "", handle: "", role: "", category: "", model: "", enabled: false };
    const c = structuredClone(base);
    dialog = el("dialog", "crew-dialog wf-dialog wf-settings");
    const form = el("form");
    form.method = "dialog";
    const head = el("div", "dialog-head");
    const ht = el("div", "");
    ht.append(el("h2", "", rec ? `${c.name} 고치기` : "새 워크플로우"), el("p", "", "키·토큰은 넣지 마세요. 키는 설정 › 모델 설정에 넣어요."));
    const close = el("button", "icon-btn plain");
    close.value = "cancel";
    close.setAttribute("aria-label", "닫기");
    close.append(icon("x"));
    head.append(ht, close);
    const bodyEl = el("div", "dialog-body");
    const err = el("p", "form-error");
    err.setAttribute("role", "alert");
    const name = input(c.name, { required: true, maxLength: 60 });
    const handle = input(c.handle, { required: true, maxLength: 40, pattern: "[\\p{L}\\p{N}_\\-]{1,40}", placeholder: "띄어쓰기 없이" });
    const role = input(c.role, { maxLength: 200, placeholder: "주제를 받아 … 까지 해요" });
    const category = input(c.category, { maxLength: 40, placeholder: "글쓰기·문서" });
    category.setAttribute("list", "crew-cats");
    const cats = el("datalist");
    cats.id = "crew-cats";
    categories(workflows()).forEach(([n]) => { const o = el("option"); o.value = n; cats.append(o); });
    // $이름 follows the name while it was still derived from it (or untitled), also when editing a saved workflow.
    const follows = !rec || c.handle === c.name.replace(/\s+/g, "").slice(0, 40) || c.handle.startsWith("제목없음");
    name.addEventListener("input", () => { if (follows && !handle.dataset.touched) handle.value = name.value.replace(/\s+/g, "").slice(0, 40); cover.refresh(); });
    handle.addEventListener("input", () => { handle.dataset.touched = "1"; handle.value = handle.value.replace(/^\$/, ""); cover.refresh(); });
    role.addEventListener("input", () => cover.refresh());
    category.addEventListener("input", () => cover.refresh());
    const row1 = el("div", "scope-fields");
    row1.append(field("이름", name, "카드에 크게 보여요"), field("$이름", handle, "어느 에이전트에서든 이 이름으로 불러요"));
    const row2 = el("div", "scope-fields");
    row2.append(field("설명", role), field("카테고리", category));
    bodyEl.append(cats, modelList(), row1, row2);
    let start = startWith || (preset?.graph ? "copy" : "simple");
    let fromTemplate = () => {};
    if (!rec) {
      const from = preset?.name?.replace(/ 복사$/, "") || "";
      const opts = [["simple", "AI 하나로 간단히", "단계 하나로 시작해요."], ["blank", "빈 그래프", "노드를 직접 이어 그려요."], ...(preset?.graph ? [["copy", `${from} 복제해서`, `${from} 그래프를 그대로 가져와요.`]] : []),
        ...(preset ? [] : templates().map((t) => [`tpl:${t.id}`, t.crew.name, `템플릿 · ${t.crew.role || "지시·모델로 AI 단계 하나를 만들어요"}`]))];
      // A template fills the empty name·설명·카테고리·모델 so the card reads right away.
      fromTemplate = (k) => {
        const t = k.startsWith("tpl:") ? byId(k.slice(4))?.crew : null;
        if (!t) return;
        if (!name.value.trim()) { name.value = t.name || ""; if (!handle.dataset.touched) handle.value = name.value.replace(/\s+/g, "").slice(0, 40); }
        if (!role.value.trim() && t.role) role.value = t.role;
        if (!category.value.trim() && t.category) category.value = t.category;
        if (!model.value.trim() && t.model) model.value = t.model;
      };
      const pick = el("div", "crew-choice big wf-choice");
      const draw = () => pick.replaceChildren(...opts.map(([k, l, sub]) => {
        const b = el("button", k === start ? "on" : "");
        b.type = "button";
        b.setAttribute("aria-pressed", String(k === start));
        b.append(el("strong", "", `${l}${k === start ? " ✓" : ""}`), el("small", "", sub));
        b.addEventListener("click", () => { start = k; fromTemplate(k); draw(); cover.refresh(); });
        return b;
      }));
      draw();
      bodyEl.append(field("어떻게 시작할까요", pick, "단계가 하나면 간단한 입력 칸으로, 단계를 더하면 그래프로 바뀌어요"));
    }
    const model = modelInput(c.model);
    const kind = select([["", "없음 — $이름·화면에서만"], ["schedule", "일정"], ["webhook", "웹훅"]], c.trigger?.kind || "");
    const cron = input(c.trigger?.cron || "0 9 * * 1-5", { maxLength: 64, cls: "mono" });
    const label = input(c.trigger?.label, { maxLength: 60, placeholder: "GitHub PR 열림" });
    const on = el("input");
    on.type = "checkbox";
    on.checked = !!c.enabled;
    const onLabel = el("label", "check-line");
    onLabel.append(on, "켜기");
    const hintOf = () => { const n = nextRun(cron.value); return n ? `${cronText(cron.value)} · 다음 ${when(n)}` : "형식: 분 시 일 월 요일 (예: 0 9 * * 1-5)"; };
    const cronHint = el("small", "dim", hintOf());
    cron.addEventListener("input", () => { cronHint.textContent = hintOf(); });
    const cronField2 = field("언제 (분 시 일 월 요일)", cron);
    cronField2.append(cronHint);
    const labelField = field("웹훅 이름", label, "주소와 토큰은 서버에서 받아요");
    const sync = () => { cronField2.hidden = kind.value !== "schedule"; labelField.hidden = kind.value !== "webhook"; onLabel.hidden = !kind.value; };
    kind.addEventListener("change", sync);
    sync();
    const row3 = el("div", "scope-fields");
    row3.append(field("워커 기본 모델", model, "AI 노드에 모델을 안 정하면 이 모델로 돌아요"), field("일정·웹훅", kind));
    const row4 = el("div", "scope-fields");
    row4.append(cronField2, labelField, onLabel);
    const maxMin = input(c.limits?.max_minutes || "", { type: "number", min: 1, max: 1440, placeholder: "30" });
    const maxSteps = input(c.limits?.max_steps || "", { type: "number", min: 1, max: 500, placeholder: "60" });
    const row5 = el("div", "scope-fields");
    row5.append(field("최대 시간 (분)", maxMin), field("최대 단계 수", maxSteps, "되돌아가기까지 합친 노드 실행 횟수"));
    const cover = coverPicker(c.cover, c.category || "", () => {
      const n = rec ? stepCount(c.graph) : start === "blank" ? 0 : start === "copy" ? stepCount(c.graph) : 1; // simple and a template: one AI step
      const when3 = kind.value === "schedule" ? cronText(cron.value) : kind.value === "webhook" ? "웹훅" : "수동 실행";
      return { name: name.value.trim(), role: role.value.trim(), category: category.value.trim(), meta: [`$${handle.value.trim() || "이름"}`, n ? (n === 1 ? "단계 1개" : `${n}단계`) : "단계 없음", when3].join(" · ") };
    });
    if (startWith) { fromTemplate(startWith); cover.refresh(); } // 에이전트 › 워크플로우로 만들기
    bodyEl.append(row3, row4, el("small", "dim", "멈추는 조건 — 비워 두면 정하지 않아요. 되돌아가는 선은 선마다 횟수를 정해요."), row5, field("배경 사진", cover.node, "고른 사진은 카드 뒤에 깔리고, 아래쪽 그라데이션으로 글씨가 보여요 · 사진: Unsplash"), err);
    // Two columns (198:2952 · 198:3038): the fields, and on the right the card as the list will show it.
    const mainCol = el("div", "wf-dlg-main");
    mainCol.append(...bodyEl.childNodes);
    const sideCol = el("div", "wf-dlg-side");
    const ph = el("div", "wf-sec-head");
    ph.append(el("strong", "", "미리보기"), el("small", "", "목록에 이렇게 보여요"));
    const note = el("div", "wf-info");
    note.append(el("strong", "", rec ? "사진 고르는 법" : "만든 뒤에"), el("p", "", rec ? "가로 사진이 잘 맞아요. 글씨가 아래에 올라가니 아래쪽이 너무 밝지 않은 사진을 고르세요." : "AI 하나로 시작하면 간단 편집에서 모델·목표만 정하면 돼요. 단계를 더하면 그래프 편집기로 바뀌어요."));
    sideCol.append(ph, cover.side, note);
    bodyEl.classList.add("wf-dlg-grid");
    bodyEl.append(mainCol, sideCol);
    kind.addEventListener("change", () => cover.refresh());
    const foot = el("div", "dialog-foot");
    const cancel = button("취소", "ghost");
    cancel.type = "submit";
    cancel.value = "cancel";
    const ok = button(rec ? "저장" : "만들기", "primary");
    ok.type = "submit";
    ok.value = "save";
    if (rec) {
      const del = button("지우기", "ghost danger", "trash", async () => { if (await remove(rec)) dialog.close(); });
      del.type = "button";
      foot.append(del, el("span", "grow"));
    }
    foot.append(cancel, ok);
    form.append(head, bodyEl, foot);
    form.addEventListener("submit", async (e) => {
      if (e.submitter?.value !== "save") return;
      e.preventDefault();
      if (kind.value === "schedule" && !parseCron(cron.value)) { err.textContent = "언제 칸의 형식을 확인해 주세요."; return; }
      const out = { ...c, type: "workflow", name: name.value.trim(), handle: handle.value.trim(), role: role.value.trim(), category: category.value.trim(), model: model.value.trim() };
      if (kind.value) { out.trigger = kind.value === "schedule" ? { kind: "schedule", cron: cron.value.trim() } : { kind: "webhook", label: label.value.trim() }; out.enabled = on.checked; } else { delete out.trigger; out.enabled = false; }
      const limits = {};
      if (Number(maxMin.value) > 0) limits.max_minutes = Math.min(1440, Math.round(Number(maxMin.value)));
      if (Number(maxSteps.value) > 0) limits.max_steps = Math.min(500, Math.round(Number(maxSteps.value)));
      if (Object.keys(limits).length) out.limits = limits; else delete out.limits;
      const cv = cover.value();
      if (cv) out.cover = cv; else delete out.cover;
      if (!rec) out.graph = start === "copy" ? c.graph : defaultGraph(start, out);
      if (fromEditor) out.graph = ed.graph; // settings saved from the editor keep the graph being edited
      ok.disabled = true;
      try {
        const saved = await save(rec, out);
        dialog.close();
        if (fromEditor) {
          await reload();
          ed.rec = byId(saved.id) || saved;
          ed.dirty = false;
          state.view = "graph";
          render();
          notice("워크플로우 설정과 그래프를 저장했어요.");
          return;
        }
        await reload(saved.id);
        notice(rec ? "저장했어요." : "만들었어요.");
        if (!rec) openGraph(byId(saved.id) || saved, { graphMode: start === "blank" });
      } catch (x) {
        err.textContent = errorText(x);
        err.scrollIntoView({ block: "nearest" });
      } finally {
        ok.disabled = false;
      }
    });
    dialog.append(form);
    dialog.addEventListener("close", () => dialog.remove());
    document.body.append(dialog);
    dialog.showModal();
  }

  // ---------------------------------------------------------------- editor (03a · 03c · 03d) and run view (03b)
  const ed = { rec: null, graph: null, sel: null, selEdge: null, zoom: 1, panX: 0, panY: 0, dirty: false, filter: "", tab: "AI", menu: false,
    graphMode: false, review: false, suggestions: null, reviewBusy: false, run: null, runList: false, timer: null };
  function openGraph(rec, opts = {}) {
    if (!rec) return;
    stopPolling();
    ed.rec = rec;
    ed.graph = structuredClone(rec.crew.graph || defaultGraph("blank", rec.crew));
    ed.graph.nodes.forEach((n) => { n.config ||= {}; });
    Object.assign(ed, { sel: null, selEdge: null, zoom: 1, panX: 0, panY: 0, dirty: false, filter: "", menu: false,
      graphMode: !!opts.graphMode, review: !!opts.review, linking: null, fromPopup: !!opts.fromPopup, suggestions: null, reviewBusy: false, run: opts.run || null, test: null, trigger: undefined, runList: false });
    if (opts.select && ed.graph.nodes.some((n) => n.id === opts.select)) ed.sel = opts.select;
    // The newest run of this workflow: each selected node's bottom row shows how that node did there.
    ed.last = null;
    if (!ed.run) request(`/v1/crew/runs?${query({ crew_id: rec.id, limit: 1 })}`).then((r) => {
      const last = (r.items || []).find((x) => x.crew_run?.crew_id === rec.id);
      if (ed.rec !== rec || !last) return;
      ed.last = last;
      if (state.view === "graph" && !nodeDlg && !ed.run) render(false);
    }, () => {});
    state.view = "graph";
    state.selected = null;
    render();
    if (ed.run) void pollRun();
  }
  async function leaveGraph() {
    if (ed.dirty && !await ask("저장하지 않은 변경이 있어요. 나갈까요?")) return;
    stopPolling();
    const rec = ed.rec;
    ed.rec = null;
    ed.run = null;
    // A workflow an agent works by is edited from that agent, so leaving the editor goes back there (its unsaved draft stays).
    const owner = ownerOf(rec);
    if (owner) { state.workerNew = false; if (wk.id !== owner.id) workerPick(owner.id); state.view = "workers"; render(); return; }
    state.view = "crew";
    state.selected = `crew:${rec.id}`;
    render();
    if (ed.fromPopup) getAuto().open(byId(rec.id) || rec); // opened from the workflow window: finish there
  }
  async function saveGraph() {
    try {
      const crew = { ...ed.rec.crew, graph: ed.graph };
      const tr = ed.trigger;
      if (tr?.kind === "schedule" && !parseCron(tr.cron)) { notice("시작 노드의 언제 칸 형식을 확인해 주세요."); return false; }
      if (tr) {
        // A newly set schedule or webhook starts on; one that was already there keeps its on/off.
        if (tr.kind) { crew.trigger = tr.kind === "schedule" ? { kind: "schedule", cron: tr.cron.trim() } : { kind: "webhook", label: tr.label.trim() }; crew.enabled = ed.rec.crew.trigger ? !!ed.rec.crew.enabled : true; } else { delete crew.trigger; crew.enabled = false; }
      }
      const saved = await save(ed.rec, crew);
      ed.trigger = undefined;
      ed.dirty = false;
      await reload();
      ed.rec = byId(saved.id) || saved;
      state.view = "graph";
      render();
      notice(ed.fromPopup ? "저장했어요. '창으로 돌아가기'를 눌러 마무리해요." : "워크플로우를 저장했어요.");
      void autoTitle(ed.rec);
      return true;
    } catch (e) {
      notice(errorText(e));
      return false;
    }
  }
  // Edits keep the AI suggestions: applying one must not wipe the others (each re-checks with canApply).
  const touch = () => {
    ed.dirty = true;
    const save = document.querySelector(".page-head .g-save"), run = document.querySelector(".page-head .g-trial");
    if (save) save.disabled = false;
    if (run) { run.disabled = true; run.title = "저장한 뒤에 실행할 수 있어요"; }
  };
  function uid(type) {
    let i = 1;
    while (ed.graph.nodes.some((n) => n.id === `${type}${i}`)) i++;
    return `${type}${i}`;
  }
  function addNode(type, preset = {}) {
    const g = ed.graph;
    const anchor = g.nodes.find((n) => n.id === ed.sel) || g.nodes.filter((n) => n.type !== "end").at(-1) || g.nodes.at(-1);
    const n = { id: uid(type === "ai_once" ? "once" : type), type, title: preset.title || NODES[type].label, x: (anchor?.x || 0) + 260, y: anchor?.y || 120, config: { ...(preset.config || {}) } };
    for (const [k, , kind, opts] of FIELDS[type] || []) if ((kind === "select" || kind === "radio") && n.config[k] === undefined) n.config[k] = opts[0][0];
    if (type === "ai" || type === "expert") Object.assign(n.config, { goal: "{{input}}", access: "read", ...preset.config });
    if (type === "team") Object.assign(n.config, { topic: "{{input}}", ...preset.config });
    if (type === "ai_once") Object.assign(n.config, { prompt: "{{input}}", ...preset.config });
    if (type === "subflow") n.config.task ||= "{{input}}";
    g.nodes.push(n);
    if (anchor && anchor.type !== "end" && !PORTS[anchor.type] && !wouldCycle(g, anchor.id, n.id)) {
      // Insert after the anchor: anchor → new → anchor's old forward targets (back edges stay where they were).
      const outs = g.edges.filter((e) => e.from === anchor.id && !isBack(e) && !e.port);
      g.edges = g.edges.filter((e) => !outs.includes(e));
      g.edges.push({ from: anchor.id, to: n.id }, ...outs.map((e) => ({ from: n.id, to: e.to })));
    }
    ed.sel = n.id;
    ed.selEdge = null;
    ed.menu = false;
    ed.graphMode = true;
    touch();
    render(false);
    openNodeDialog(n);
  }
  function removeNode(id) {
    const g = ed.graph;
    g.nodes = g.nodes.filter((n) => n.id !== id);
    g.edges = g.edges.filter((e) => e.from !== id && e.to !== id);
    // A new node may reuse this id: drop the suggestions about the removed one so none lands on the newcomer.
    if (ed.suggestions) ed.suggestions = ed.suggestions.filter((x) => !targetsOf(x.action).includes(id));
    ed.sel = null;
    touch();
    render(false);
  }
  const TAB_NAMES = { "저장한 페르소나": "페르소나" };
  function presetItems() {
    const out = [];
    const order = ["전문가", "AI", "저장한 페르소나", "시작", "처리", "흐름", "알림·끝", "그 밖의 연동"];
    for (const [type, n] of Object.entries(NODES)) if (!n.hidden) out.push({ group: n.group, type, title: n.label, sub: n.sub, preset: {} });
    for (const a of items().filter((r) => r.crew.type === "agent")) out.push({ group: "전문가", type: "expert", title: a.crew.name, sub: a.crew.model || "모델 자동", expert: a, preset: { title: a.crew.name, config: { expert: a.id } } });
    for (const p of personas()) out.push({ group: "저장한 페르소나", type: "ai", title: p.crew.name, sub: "AI 에이전트로 넣어요", persona: true, preset: { title: p.crew.name, config: { persona: p.id } } });
    return out.sort((a, b) => order.indexOf(a.group) - order.indexOf(b.group));
  }
  // The add menu shows one category at a time; typing in the search looks through every category.
  function presetList() {
    const box = el("div", "g-menu");
    const groups = [...new Set(presetItems().map((i) => i.group))];
    if (!groups.includes(ed.tab)) ed.tab = groups[0];
    const search = el("label", "search");
    const q = el("input");
    q.type = "search";
    q.placeholder = "노드 찾기";
    q.value = ed.filter;
    q.setAttribute("aria-label", "노드 찾기");
    const tabs = el("div", "segment g-tabs");
    const sync = () => {
      tabs.querySelectorAll(".tab").forEach((b) => b.classList.toggle("active", !ed.filter.trim() && b.dataset.group === ed.tab));
      box.querySelector(".g-presets").replaceWith(presetGroups());
    };
    for (const g of groups) {
      const t = el("button", "tab", TAB_NAMES[g] || g);
      t.type = "button";
      t.dataset.group = g;
      t.addEventListener("click", () => { ed.tab = g; ed.filter = q.value = ""; sync(); });
      tabs.append(t);
    }
    q.addEventListener("input", () => { ed.filter = q.value; sync(); });
    search.append(icon("magnifying-glass"), q);
    box.append(search, tabs, el("div", "g-presets"));
    sync();
    return box;
  }
  function presetGroups() {
    const list = el("div", "g-presets");
    const f = ed.filter.trim().toLowerCase();
    let group = "";
    for (const it of presetItems().filter((i) => !f || `${i.title} ${i.sub}`.toLowerCase().includes(f))) {
      if (it.group !== group) { group = it.group; list.append(el("h4", "", group)); }
      const b = el("button", "g-preset");
      b.type = "button";
      const lead = el("span", "g-icon");
      lead.append(it.expert ? avatarMark(it.expert.crew) : icon(it.persona ? "users-three" : NODES[it.type].icon));
      const t = el("div", "");
      t.append(el("strong", "", it.title), el("small", it.expert ? "dim mono" : "dim", it.sub));
      b.append(lead, t, icon("plus", "icon dim"));
      b.addEventListener("click", () => addNode(it.type, it.preset));
      list.append(b);
    }
    if (!list.children.length) list.append(el("p", "dim", "맞는 노드가 없어요."));
    return list;
  }
  function nodeSummary(n) {
    const c = n.config || {};
    const x = ed.rec?.crew;
    if (n.type === "ai") {
      const rows = [["모델", modelOf(n, x)], ["목표", c.goal && c.goal !== "{{input}}" ? c.goal : "앞 단계 결과로 일해요"]];
      if (c.collab === "on") rows.push(["협업", `워커 최대 ${c.max_workers || 5} · 동시 ${c.concurrency || 3}`]);
      return rows;
    }
    if (n.type === "expert") {
      const a = byId(c.expert);
      return [["모델", c.model || a?.crew.model || x?.model || "자동"], ["목표", c.goal && c.goal !== "{{input}}" ? c.goal : "앞 단계 결과로 일해요"]];
    }
    if (n.type === "team") return [["팀", byId(c.team)?.crew.name || "고르지 않았어요"], ["주제", c.topic && c.topic !== "{{input}}" ? c.topic : "앞 단계 결과"]];
    if (n.type === "n8n") return [["주소", (c.url || "").replace(/^https?:\/\//, "") || "—"]];
    if (n.type === "ai_once") return [["모델", modelOf(n, x)], ["질문", c.prompt && c.prompt !== "{{input}}" ? c.prompt : "앞 단계 결과를 그대로 물어요"]];
    if (n.type === "subflow") return [["부르는 것", c.workflow ? `$${c.workflow.replace(/^\$/, "")}` : "고르지 않았어요"]];
    if (n.type === "code") return [["코드", `${c.lang === "sh" ? "셸" : "Go"} · ${(c.code || "").split("\n").length}줄 · 토큰 0`]];
    if (n.type === "approval") return [["물어볼 것", c.question || "—"]];
    if (n.type === "condition") return [["조건", c.expr || "—"]];
    if (n.type === "schedule") return [["언제", cronText(c.cron)]];
    if (n.type === "http") return [["요청", `${c.method || "GET"} ${c.url || ""}`]];
    if (n.type === "github") return [["gh", c.args || "—"]];
    if (n.type === "wait") return [["기다림", c.duration || "—"]];
    if (n.type === "loop") return [["목록", c.path || "—"]];
    if (n.type === "notify") return [["보내기", `${{ telegram: "텔레그램", slack: "슬랙", email: "메일" }[c.channel] || ""} ${c.to || ""}`]];
    if (n.type === "end") return [["형식", c.message || "요약 + 링크"]];
    if (n.type === "start") return [["받는 것", "부를 때 쓴 문장"]];
    return [];
  }
  function nodeSub(n) {
    const c = n.config || {};
    if (n.type === "ai" || n.type === "expert") return [c.collab === "on" ? "전문가 · 협업" : "전문가", n.type === "ai" ? byId(c.persona)?.crew.name : ""].filter(Boolean).join(" · ");
    if (n.type === "team") return "팀 · 회의";
    if (n.type === "ai_once") return "한 번 묻기";
    if (n.type === "code") return "코드 · 토큰 0";
    return NODES[n.type]?.label || n.type;
  }

  // 캔버스에서 바로 고치기: the card rows that are one value each. 모델 is a choice, 언제 a small schedule editor, the rest plain text.
  const INLINE = {
    모델: (n) => ["ai", "ai_once", "expert"].includes(n.type) && { key: "model", kind: "model" },
    목표: () => ({ key: "goal", area: true }),
    질문: () => ({ key: "prompt", area: true }),
    주제: () => ({ key: "topic", area: true }),
    "물어볼 것": () => ({ key: "question", area: true }),
    조건: () => ({ key: "expr" }),
    gh: () => ({ key: "args" }),
    기다림: () => ({ key: "duration" }),
    목록: () => ({ key: "path" }),
    형식: () => ({ key: "message" }),
    보내기: () => ({ key: "to" }),
    주소: () => ({ key: "url" }),
    요청: () => ({ key: "url" }),
    언제: () => ({ key: "cron", kind: "cron" })
  };
  // The workflow's trigger draft that 저장 keeps (shared by the start node popup and the canvas schedule editor).
  const trigDraft = (n) => {
    const now = ed.rec.crew.trigger;
    return (ed.trigger ||= { kind: now?.kind || "", cron: now?.cron || n.config.cron || "0 9 * * 1-5", label: now?.label || n.config.note || "" });
  };
  function setCron(n, v) {
    const was = n.config.cron;
    n.config.cron = v;
    if (n.title === cronText(was)) n.title = cronText(v);
    const tr = trigDraft(n);
    tr.kind = "schedule";
    tr.cron = v;
  }
  function cardEdit(s, n, { key, kind, area }) {
    if (s.querySelector("input, textarea, select")) return;
    const c = n.config;
    let over = false;
    const finish = (v) => {
      if (over) return;
      over = true;
      if (v !== undefined && v !== (c[key] ?? "")) {
        if (kind === "cron") setCron(n, v);
        else if (kind === "model" && !v) delete c.model;
        else c[key] = v;
        touch();
      }
      render(false);
    };
    const span = s.querySelector("span");
    if (kind === "cron") {
      // 매일·평일·주말 + 시각, or any 5-field cron underneath.
      const box = el("div", "g-inline-cron");
      const f = (c.cron || "0 9 * * *").trim().split(/\s+/);
      const time = el("input");
      time.type = "time";
      time.value = /^\d+$/.test(f[0]) && /^\d+$/.test(f[1]) ? `${f[1].padStart(2, "0")}:${f[0].padStart(2, "0")}` : "09:00";
      const days = select([["*", "매일"], ["1-5", "평일"], ["0,6", "주말"]], f[4] || "*");
      const raw = input(c.cron || "0 9 * * *", { maxLength: 64, cls: "mono" });
      const hint = el("small", "dim");
      const show = () => { const x = parseCron(raw.value); hint.textContent = x ? cronText(raw.value) : "형식: 분 시 일 월 요일 (예: 0 9 * * 1-5)"; };
      const fromParts = () => { const [h, m] = time.value.split(":").map(Number); raw.value = `${m || 0} ${h || 0} * * ${days.value}`; show(); };
      time.addEventListener("change", fromParts);
      days.addEventListener("change", fromParts);
      raw.addEventListener("input", show);
      const row = el("div", "g-inline-row");
      row.append(days, time);
      box.append(row, raw, hint);
      show();
      box.addEventListener("keydown", (e) => {
        if (e.key === "Escape") finish();
        if (e.key === "Enter") { e.preventDefault(); if (parseCron(raw.value)) finish(raw.value.trim()); }
      });
      box.addEventListener("focusout", (e) => { if (!box.contains(e.relatedTarget)) finish(parseCron(raw.value) ? raw.value.trim() : undefined); });
      for (const x of [box, time, days, raw]) x.addEventListener("click", (e) => e.stopPropagation());
      span.replaceWith(box);
      days.focus();
      return;
    }
    const i = kind === "model"
      ? modelSelect(c.model, n.type === "expert" ? "전문가 기본" : ed.rec.crew.model ? `기본 · ${ed.rec.crew.model}` : "기본")
      : input(c[key], { tag: area ? "textarea" : "input", rows: 3, maxLength: 8000 });
    i.classList.add("g-inline");
    i.addEventListener("click", (e) => e.stopPropagation());
    i.addEventListener("pointerdown", (e) => e.stopPropagation());
    if (kind === "model") i.addEventListener("change", () => finish(i.value));
    i.addEventListener("keydown", (e) => {
      if (e.key === "Escape") { e.preventDefault(); finish(); }
      if (e.key === "Enter" && (!area || e.ctrlKey || e.metaKey)) { e.preventDefault(); finish(i.value); }
    });
    i.addEventListener("blur", () => finish(kind === "model" ? undefined : i.value));
    span.replaceWith(i);
    i.focus();
    if (i.select && kind !== "model") i.select();
  }
  // A panel edit shows on the canvas card without redrawing the page (a redraw would drop the field being typed in).
  function syncCard(n) {
    const box = document.querySelector(`.g-node[data-id="${CSS.escape(n.id)}"]`);
    if (box) {
      box.querySelector(".g-node-head strong").textContent = nodeTitle(n);
      const rows = box.querySelectorAll(".g-node-field > span");
      nodeSummary(n).forEach(([, v], i) => { if (rows[i]) rows[i].textContent = v; });
    }
    const t = document.querySelector(".g-right .g-node-name");
    if (t) t.textContent = nodeTitle(n);
  }
  // ✦ on a prompt-like field: an AI chat about just that field (shared component; no icon until it is loaded).
  const AI_PRESET = {
    goal: "You help write the goal of one AI step in an Arkism workflow. Reply in the user's language (Korean by default). A good goal says what to produce and when it is done, in one or two sentences; keep {{input}} where the previous step's result goes. Do not decide how to do it.",
    instructions: "You help write the persona instruction of one AI step in an Arkism workflow. Reply in the user's language (Korean by default). Write it as role, tone and limits, as 'You are X. You do Y.' sentences in the user's language, short and concrete.",
    prompt: "You help write a one-shot LLM prompt for one step in an Arkism workflow. Reply in the user's language (Korean by default). Keep {{input}} where the previous step's result goes; ask for a clear output format.",
    topic: "You help write the meeting topic for a team of AI experts in an Arkism workflow. Reply in the user's language (Korean by default). State the question to settle and what a finished answer looks like; keep {{input}} where the previous step's result goes.",
    question: "You help write the question a person answers before an Arkism workflow continues (approve or reject). Reply in the user's language (Korean by default). One short question that says what will happen if they approve.",
    message: "You help write the message an Arkism workflow sends or returns at this step. Reply in the user's language (Korean by default). Short, scannable, with the result first.",
    task: "You help write the task an Arkism workflow hands to a sub-workflow. Reply in the user's language (Korean by default). Keep {{input}} where the previous step's result goes."
  };
  // ✦ on the panel's text fields: ai-chat.js draws the one icon and the talk; the talk per node·field outlives redraws.
  const aiFieldTalk = new WeakMap();
  function aiField(i, n, key, label) {
    const chat = window.ArkismAIChat;
    if (!chat?.field || !AI_PRESET[key]) return i;
    if (!aiFieldTalk.has(n)) aiFieldTalk.set(n, {});
    const talks = aiFieldTalk.get(n);
    const side = (dir) => ed.graph.edges.filter((e) => (dir ? e.from : e.to) === n.id).map((e) => nodeIn(dir ? e.to : e.from)).filter(Boolean).map((x) => ({ type: x.type, title: nodeTitle(x) }));
    return chat.field(i, {
      state: (talks[key] ||= {}), title: label, sub: `${nodeTitle(n)} · ${NODES[n.type]?.label || n.type}`,
      greeting: "이 칸에 들어갈 글을 같이 다듬어요. 무엇을 바꿀까요?", placeholder: "예: 더 짧고 구체적으로",
      quick: ["더 짧게", "더 구체적으로", "완료 기준 넣기"], preset: AI_PRESET[key],
      context: () => JSON.stringify({ workflow: ed.rec.crew.name, node: { type: n.type, title: nodeTitle(n), config: n.config }, field: key, before: side(false), after: side(true) })
    });
  }
  // 노드를 누르면 오른쪽 패널에 기본 설정이 열리고, 자세히는 노드 설정 팝업을 열어요.
  function nodePanel(box, n) {
    box.classList.add("node");
    const head = el("div", "g-right-head");
    const lead = el("span", "g-icon");
    const ex = n.type === "expert" ? byId(n.config?.expert) : null;
    lead.append(ex ? avatarMark(ex.crew) : icon(NODES[n.type]?.icon || "stack"));
    const t = el("div", "");
    t.append(el("strong", "g-node-name", nodeTitle(n)), el("small", "dim", nodeLine(n)));
    const close = closeX("노드 설정 닫기", () => selectOnCanvas(null));
    head.append(lead, t, close);
    box.append(head);
    if (IS_AI(n.type) || n.type === "team") basicAI(box, n);
    else nodeBody(box, n);
    const acts = el("div", "g-right-acts");
    acts.append(button("자세히", "", "arrow-square-out", () => openNodeDialog(n)), button("노드 지우기", "ghost", null, async () => {
      if (await confirmBox("노드를 지울까요?", "이 노드와 이어진 선이 함께 지워져요. 저장하지 않고 나가면 원래대로 돌아가요.", "노드 지우기")) removeNode(n.id);
    }));
    box.append(acts);
    return box;
  }
  // AI·전문가·팀 노드의 기본: 이름 · 모델 · 무엇을 할지. 실행 범위·협업·스킬은 자세히에서.
  function basicAI(box, n) {
    const c = n.config;
    const bind = (i, key) => { i.addEventListener("input", () => { c[key] = i.value; touch(); syncCard(n); }); return i; };
    const title = input(n.title, { maxLength: 80 });
    title.addEventListener("input", () => { n.title = title.value; touch(); syncCard(n); });
    box.append(field("노드 이름", title));
    if (n.type === "expert") box.append(kv([["전문가", byId(c.expert)?.crew.name || "고르지 않았어요"]]));
    if (n.type === "team") box.append(kv([["팀", byId(c.team)?.crew.name || "고르지 않았어요"]]));
    if (n.type === "ai" || n.type === "ai_once") box.append(modelList(), field("모델", bind(modelInput(c.model, ed.rec.crew.model || "deepseek-v4-flash"), "model"), ed.rec.crew.model ? `비우면 ${ed.rec.crew.model}` : ""));
    const slot = "{{input}} 자리에 앞 단계 결과가 들어가요";
    const [key, label] = n.type === "team" ? ["topic", "회의 주제"] : n.type === "ai_once" ? ["prompt", "질문"] : ["goal", "목표"];
    box.append(field(label, aiField(bind(input(c[key], { tag: "textarea", rows: 4, maxLength: key === "topic" ? 4000 : 8000 }), key), n, key, label), slot));
    if (n.type !== "team") {
      const lab = byId(c.persona) ? "더할 지시" : "페르소나 지시";
      box.append(field(lab, aiField(bind(input(c.instructions, { tag: "textarea", rows: 3, maxLength: 8000, placeholder: "너는 ○○다. …한다." }), "instructions"), n, "instructions", lab)));
    }
    box.append(el("p", "meta", "전문가·팀 고르기, 실행 범위, 나눠서 맡기기, 스킬은 자세히에서 정해요."));
  }

  function renderGraph() {
    const t0 = performance.now();
    const rec = ed.rec;
    if (!rec) { state.view = "crew"; render(); return; }
    $("#controls").replaceChildren();
    const simple = !ed.run && !ed.graphMode && simpleNode(ed.graph);
    if (simple) return renderSimple(simple);
    const page = el("div", `graph-page${ed.run ? " run" : ""}`);
    const stage = el("div", "g-stage");
    const canvas = el("div", "g-canvas");
    canvas.style.transform = `translate(${ed.panX}px, ${ed.panY}px) scale(${ed.zoom})`;
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("class", "g-edges");
    const labels = el("div", "g-labels");
    canvas.append(svg, labels);
    const g = ed.graph;
    const W = 220;
    const nodeEls = new Map();
    const runNodes = (ed.run || ed.test)?.crew_run?.nodes || {};
    const flagged = new Set((ed.review && ed.suggestions || []).flatMap((s) => [s.action?.node, s.action?.from, s.action?.to]).filter(Boolean));
    for (const n of g.nodes) {
      const st = runNodes[n.id];
      const box = el("div", `g-node ${n.type}${ed.sel === n.id ? " selected" : ""}${st ? ` st-${st.status}` : ""}${flagged.has(n.id) ? " flagged" : ""}`);
      box.style.left = `${n.x}px`;
      box.style.top = `${n.y}px`;
      box.dataset.id = n.id;
      box.dataset.kind = KIND_OF(n.type);
      const head = el("div", "g-node-head");
      const lead = el("span", "g-icon");
      const expertRec = n.type === "expert" ? byId(n.config?.expert) : null;
      lead.append(expertRec ? avatarMark(expertRec.crew) : icon(NODES[n.type]?.icon || "stack"));
      const t = el("div", "");
      // 테스트 실행 (859:59212–63707): a finished node shows the first line of its result in its subtitle.
      const sub = ed.test && st ? (st.status === "done" && readable(st.output || "").split("\n")[0].slice(0, 28)) || stepState(n, st).split(" · ")[0]
        : st ? [NODE_LABEL[st.status] || st.status, st.runs > 1 ? `${st.runs}회째` : ""].filter(Boolean).join(" · ") : nodeSub(n);
      t.append(el("strong", "", nodeTitle(n)), el("small", "dim", sub));
      head.append(lead, t);
      if (ed.test && st) {
        const mark = el("i", `g-state ${{ done: "done", running: "live", waiting: "live", failed: "fail" }[st.status] || ""}`);
        if (st.status === "done") mark.append(icon("check"));
        head.append(mark);
        if (st.status === "done" && st.started_at && st.finished_at) box.append(el("small", "g-node-dur mono", secText(new Date(st.finished_at) - new Date(st.started_at))));
      } else if (st) head.append(el("i", `dot ${{ done: "ready", running: "warn", waiting: "warn", failed: "error" }[st.status] || ""}`));
      box.append(head);
      for (const [k, v] of nodeSummary(n)) {
        const s = el("div", "g-node-field");
        s.append(el("small", "dim", k), el("span", k === "모델" ? "mono" : "", v));
        const spec = !ed.run && INLINE[k]?.(n);
        if (spec) {
          s.classList.add("editable");
          s.title = "눌러서 바로 고쳐요";
          s.addEventListener("click", (ev) => { if (ed.linking) return; ev.stopPropagation(); if (ed.sel !== n.id) selectOnCanvas(n.id); cardEdit(s, n, spec); });
        }
        box.append(s);
      }
      if (!["start", "schedule", "webhook"].includes(n.type)) {
        // Several lines into one node: one named input point per line (GPT 의견 · Gemini 의견 …).
        const incoming = g.edges.filter((e) => e.to === n.id);
        if (incoming.length > 1) {
          const ins = el("div", "g-ins");
          for (const e of incoming) {
            const row = el("div", "g-in");
            const dot = el("i", "g-port in");
            dot.dataset.edge = edgeKey(e);
            const src = g.nodes.find((x) => x.id === e.from);
            row.append(dot, el("small", "dim", src ? nodeTitle(src) : e.from));
            ins.append(row);
          }
          head.after(ins);
          box.append(ins);
        } else {
          const dot = el("i", `g-port in${incoming.length ? "" : " unused"}`);
          box.append(dot);
        }
      }
      const portBox = el("div", "g-outs");
      for (const [port, label] of portsOf(n, g)) {
        const row = el("div", "g-out");
        if (label) row.append(el("small", "dim", label));
        const dot = el("i", `g-port out${g.edges.some((e) => e.from === n.id && (e.port || "") === port) ? "" : " unused"}`);
        dot.dataset.port = port;
        dot.title = "끌어서 다음 노드에 이어요";
        if (!ed.run) dot.addEventListener("pointerdown", (ev) => startLink(ev, n, port, canvas, svg));
        row.append(dot);
        portBox.append(row);
      }
      box.append(portBox);
      if (!ed.run && WORK(n) && !ownerOf(rec)) box.append(nodeFoot(n));
      if (!ed.run) head.addEventListener("pointerdown", (ev) => startDrag(ev, n, box, canvas, svg));
      box.addEventListener("click", (ev) => {
        ev.stopPropagation();
        if (ed.linking && !ed.run) { if (ed.linking.from !== n.id) linkTo(nodeIn(ed.linking.from), ed.linking.port, n.id); return; }
        if (!ed.run || ed.sel !== n.id) selectOnCanvas(n.id);
      });
      canvas.append(box);
      nodeEls.set(n.id, box);
    }
    stage.append(canvas);
    stage.addEventListener("click", () => {
      if (ed.justPanned) { ed.justPanned = false; return; }
      if (ed.linking) { ed.linking = null; render(false); return; }
      if (ed.sel || ed.selEdge !== null) selectOnCanvas(null);
    });
    // 화면 이동: 휠 클릭(가운데 버튼)으로 끌거나 스페이스를 누른 채 끌어요 (Figma와 같아요).
    // Capture phase, so a node under the pointer does not start its own drag.
    stage.addEventListener("pointerdown", (ev) => {
      if (!(ev.button === 1 || (ev.button === 0 && spaceDown))) return;
      ev.preventDefault();
      ev.stopPropagation();
      const sx = ev.clientX, sy = ev.clientY, px = ed.panX, py = ed.panY;
      let moved = false;
      stage.classList.add("panning");
      try { stage.setPointerCapture(ev.pointerId); } catch { /* the pointer is already gone */ }
      const move = (e) => {
        ed.panX = px + e.clientX - sx;
        ed.panY = py + e.clientY - sy;
        moved ||= Math.abs(e.clientX - sx) + Math.abs(e.clientY - sy) > 3;
        canvas.style.transform = `translate(${ed.panX}px, ${ed.panY}px) scale(${ed.zoom})`;
      };
      const up = (e) => {
        if (e?.type === "pointerup") move(e); // the last move can arrive only with the release
        window.removeEventListener("pointermove", move);
        window.removeEventListener("pointerup", up);
        window.removeEventListener("pointercancel", up);
        stage.classList.remove("panning");
        if (moved && ev.button === 0) ed.justPanned = true;
      };
      window.addEventListener("pointermove", move);
      window.addEventListener("pointerup", up);
      window.addEventListener("pointercancel", up);
    }, true);
    // A middle click would otherwise show the browser's auto-scroll or open a link.
    // and a space-drag would select text or start a native drag that cancels the pointer.
    stage.addEventListener("mousedown", (ev) => { if (ev.button === 1 || (ev.button === 0 && spaceDown)) ev.preventDefault(); }, true);
    stage.addEventListener("auxclick", (ev) => { if (ev.button === 1) ev.preventDefault(); });
    stage.classList.toggle("space", spaceDown);
    stage.addEventListener("wheel", (ev) => {
      // 휠 = 마우스 자리를 기준으로 확대·축소; 가로 휠(트랙패드)·Shift+휠은 화면 이동.
      ev.preventDefault();
      if (ev.shiftKey || Math.abs(ev.deltaX) > Math.abs(ev.deltaY)) { ed.panX -= ev.deltaX || ev.deltaY; ed.panY -= ev.shiftKey ? 0 : ev.deltaY; }
      else {
        const r = stage.getBoundingClientRect(), mx = ev.clientX - r.left, my = ev.clientY - r.top;
        const next = Math.min(2, Math.max(0.4, ed.zoom * Math.exp(-ev.deltaY / 500)));
        ed.panX = mx - (mx - ed.panX) * next / ed.zoom;
        ed.panY = my - (my - ed.panY) * next / ed.zoom;
        ed.zoom = next;
      }
      canvas.style.transform = `translate(${ed.panX}px, ${ed.panY}px) scale(${ed.zoom})`;
      zoomOut.textContent = `${Math.round(ed.zoom * 100)}%`;
    }, { passive: false });
    const tools = el("div", "g-tools");
    const zoomOut = el("output", "", `${Math.round(ed.zoom * 100)}%`);
    const z = (d) => { ed.zoom = Math.min(2, Math.max(0.4, ed.zoom + d)); render(false); };
    const fit = () => {
      const xs = g.nodes.map((n) => n.x), ys = g.nodes.map((n) => n.y);
      const r = stage.getBoundingClientRect();
      const w = Math.max(...xs) - Math.min(...xs) + W + 80, h = Math.max(...ys) - Math.min(...ys) + 240;
      ed.zoom = Math.min(1.2, Math.max(0.4, Math.min(r.width / w, r.height / h)));
      ed.panX = -Math.min(...xs) * ed.zoom + 40;
      ed.panY = -Math.min(...ys) * ed.zoom + 40;
      render(false);
    };
    if (!ed.run) tools.append(button("노드 추가", "ghost", "plus", (ev) => { ev.stopPropagation(); ed.menu = !ed.menu; render(false); }));
    if (!ed.run) tools.append(el("i", "g-sep"));
    tools.append(button("−", "ghost small", null, () => z(-0.1)), zoomOut, button("+", "ghost small", null, () => z(0.1)), el("i", "g-sep"), button("맞추기", "ghost small", null, fit));
    stage.append(tools);
    if (!ed.menu) {
      const legend = el("div", "g-legend");
      for (const [k, label] of [["ai", "AI"], ["http", "HTTP"], ["human", "사람 확인"], ["plain", "시작·끝"]]) {
        const it = el("span", "g-legend-item");
        it.dataset.kind = k;
        it.append(el("i", "g-port"), label);
        legend.append(it);
      }
      stage.append(legend);
    }
    if (ed.menu && !ed.run) {
      const menu = presetList();
      menu.addEventListener("click", (ev) => ev.stopPropagation());
      stage.append(menu);
    }
    if (ed.linking && !ed.run) {
      stage.classList.add("linking");
      const from = nodeIn(ed.linking.from);
      const pick = PORTS[from?.type]?.find(([k]) => k === ed.linking.port)?.[1] || ed.linking.port;
      const src = pick ? `${nodeTitle(from || {})} · ${pick}` : nodeTitle(from || {});
      stage.append(el("div", "g-link-hint", `${src}에서 이을 노드를 누르세요 · Esc 취소`));
      stage.querySelector(`.g-node[data-id="${CSS.escape(ed.linking.from)}"] .g-port.out[data-port="${CSS.escape(ed.linking.port)}"]`)?.classList.add("linking");
    }
    const tb = testBadge();
    if (tb) stage.append(tb);
    let side = inspector;
    if (ed.run) side = runPanel;
    else if (ed.review) side = reviewPanel;
    page.append(ed.run && ed.runList ? runList() : stage, side());
    $("#table").replaceChildren(page);
    requestAnimationFrame(() => {
      drawEdges(svg, labels, nodeEls);
      window.ArkismPerf?.note("canvas.render", performance.now() - t0, g.nodes.length);
    });
  }
  // 실행 완료 · 5.0초 · 5단계 (859:63707): the pill on top of the stage once the editor's test run has ended.
  const secText = (ms) => (ms < 60000 ? `${(ms / 1000).toFixed(1)}초` : spanText(ms)); // 0.3초 (859:63707)
  function testBadge() {
    const cr = ed.test?.crew_run;
    if (!cr || cr.status === "running") return null;
    const steps = Object.entries(cr.nodes || {}).filter(([id, st]) => st.status === "done" && WORK(nodeIn(id) || {}));
    const t = Object.values(cr.nodes || {}).flatMap((st) => [st.started_at, st.finished_at]).filter(Boolean).map((x) => +new Date(x));
    const span = t.length ? secText(Math.max(...t) - Math.min(...t)) : "";
    const ok = cr.status === "succeeded";
    const b = el("button", `g-test-badge${ok ? "" : " warn"}`);
    b.type = "button";
    b.append(icon(ok ? "check" : "warning-circle"), [ok ? "실행 완료" : RUN_LABEL[cr.status] || cr.status, span, `${steps.length}단계`].filter(Boolean).join(" · "));
    b.title = "실행 기록에서 보기";
    b.addEventListener("click", (ev) => { ev.stopPropagation(); const r = ed.test; ed.test = null; ed.run = r; render(false); void pollRun(); });
    return b;
  }
  const edgeKey = (e) => `${e.from}>${e.to}:${e.port || ""}`;
  // 노드 바닥 줄: the node's last result and ▷ 실행 (this node only); it shows on the selected or hovered node.
  function nodeFoot(n) {
    const foot = el("div", "g-node-foot");
    const st = ed.last?.crew_run?.nodes?.[n.id];
    foot.append(el("small", "dim", st && st.status !== "pending" ? [NODE_LABEL[st.status] || st.status, nodeDur(st), st.tokens ? `${st.tokens.toLocaleString()} 토큰` : ""].filter(Boolean).join(" · ") : "아직 실행 안 함"));
    const go = el("button", "g-node-run");
    go.type = "button";
    go.append(icon("play"), "실행");
    go.title = "이 노드만 실행";
    go.addEventListener("pointerdown", (ev) => ev.stopPropagation());
    go.addEventListener("click", (ev) => {
      ev.stopPropagation();
      const saved = !ed.dirty && (ed.rec.crew.graph?.nodes || []).some((x) => x.id === n.id);
      if (!saved) { notice("저장한 뒤에 실행할 수 있어요."); return; }
      void startRun(ed.rec, "", n.id);
    });
    foot.append(go);
    return foot;
  }
  // Both ends leave and enter flat; the handle is 45% of the gap, at least 40px (Figma 198:2350).
  function curve(a, b) {
    const dx = Math.max(40, Math.abs(b.x - a.x) * 0.45);
    return `M${a.x},${a.y} C${a.x + dx},${a.y} ${b.x - dx},${b.y} ${b.x},${b.y}`;
  }
  // A back edge leaves the right side, arcs over the top and comes into the target from the left.
  function backCurve(a, b, lift, k) {
    const top = Math.min(a.y, b.y) - lift;
    // Labels of back edges that share a span sit at different points of the top line, not on each other.
    const at = 0.3 + 0.4 * (k % 2);
    return { d: `M${a.x},${a.y} C${a.x + 110},${a.y} ${a.x + 110},${top} ${a.x - 40},${top} L${b.x + 40},${top} C${b.x - 110},${top} ${b.x - 110},${b.y} ${b.x},${b.y}`, mid: { x: b.x + (a.x - b.x) * at, y: top } };
  }
  const edgeShape = (e, pa, pb, lift, k) => isBack(e) ? backCurve(pa, pb, lift, k) : { d: curve(pa, pb), mid: { x: (pa.x + pb.x) / 2, y: (pa.y + pb.y) / 2 } };
  function drawEdges(svg, labels, nodeEls) {
    const t0 = performance.now();
    svg.replaceChildren();
    labels.replaceChildren();
    const loops = ed.run?.crew_run?.loops || {};
    const typeOf = new Map(ed.graph.nodes.map((n) => [n.id, n.type]));
    const drawn = [];
    let lift = 60, k = 0;
    // Read every port position before writing anything: a write between reads makes the browser lay out again each time.
    const points = ed.graph.edges.map((e) => {
      const a = nodeEls.get(e.from), b = nodeEls.get(e.to);
      return a && b ? [outPoint(a, e.port), inPoint(b, e)] : [];
    });
    ed.graph.edges.forEach((e, i) => {
      const [pa, pb] = points[i];
      if (!pa || !pb) return;
      const back = isBack(e);
      const at = back ? { lift: (lift += 32), k: k++ } : { lift: 0, k: 0 };
      const shape = edgeShape(e, pa, pb, at.lift, at.k);
      const used = loops[`${e.from}>${e.to}:${e.port || ""}`] || 0;
      const p = document.createElementNS("http://www.w3.org/2000/svg", "path");
      p.setAttribute("d", shape.d);
      p.setAttribute("class", `g-edge${back ? " back" : ""}${e.port === "no" || e.port === "reject" || e.port === "fail" ? " alt" : ""}${used ? " used" : ""}${ed.selEdge === i ? " selected" : ""}`);
      const hit = document.createElementNS("http://www.w3.org/2000/svg", "path");
      hit.setAttribute("d", shape.d);
      hit.setAttribute("class", "g-edge-hit");
      const title = document.createElementNS("http://www.w3.org/2000/svg", "title");
      title.textContent = ed.run ? "" : "눌러서 선 설정";
      hit.append(title);
      if (!ed.run) hit.addEventListener("click", (ev) => { ev.stopPropagation(); selectOnCanvas(null, i); });
      svg.append(p, hit);
      const fromType = typeOf.get(e.from);
      const portText = PORTS[fromType]?.find(([k]) => k === e.port)?.[1] || e.port;
      const fan = !back && ed.graph.edges.filter((x) => x.from === e.from && (x.port || "") === (e.port || "") && !isBack(x)).length;
      const text = back ? `${portText ? `${portText} → ` : ""}다시 · ${used ? `${used}/${e.max}번째` : `최대 ${e.max}번`}`
        : IS_AI(fromType) && e.port ? `${e.port} · AI가 고름`
        : [PORTS[fromType] ? portText || "항상" : "", fan > 1 ? `동시에 · ${fan}곳` : ""].filter(Boolean).join(" · ");
      if (text) {
        const chip = el("span", `g-edge-label${back ? " back" : ""}${used ? " used" : ""}`, text);
        chip.style.left = `${shape.mid.x}px`;
        chip.style.top = `${shape.mid.y}px`;
        if (!ed.run) chip.addEventListener("click", (ev) => { ev.stopPropagation(); selectOnCanvas(null, i); });
        labels.append(chip);
        at.chip = chip;
      }
      drawn.push(Object.assign(at, { i, e, p, hit }));
    });
    svg.drawn = drawn;
    window.ArkismPerf?.note("canvas.edges", performance.now() - t0, ed.graph.edges.length);
  }
  // Picking a node or a line only swaps the selection marks and the side panel; the canvas stays as drawn.
  function selectOnCanvas(id, edge = null) {
    ed.sel = id;
    ed.selEdge = edge;
    const page = $("#table .graph-page");
    const side = page?.querySelector(":scope > .g-right");
    if (ed.run) { render(false); return; }
    if (id && ed.review) { ed.review = false; render(false); return; }
    if (!side) render(false);
    else {
      const t0 = performance.now();
      page.querySelectorAll(".g-node.selected").forEach((b) => b.classList.remove("selected"));
      if (id) page.querySelector(`.g-node[data-id="${CSS.escape(id)}"]`)?.classList.add("selected");
      for (const d of page.querySelector(".g-edges")?.drawn || []) d.p.classList.toggle("selected", d.i === edge);
      if (!ed.review) side.replaceWith(inspector());
      window.ArkismPerf?.note("canvas.select", performance.now() - t0, ed.graph.nodes.length);
    }
  }
  // While a node is dragged only the lines touching it move; the rest of the canvas stays as drawn.
  function moveEdges(svg, nodeEls, id) {
    for (const d of svg.drawn || []) {
      if (d.e.from !== id && d.e.to !== id) continue;
      const pa = outPoint(nodeEls.get(d.e.from), d.e.port), pb = inPoint(nodeEls.get(d.e.to), d.e);
      if (!pa || !pb) continue;
      const shape = edgeShape(d.e, pa, pb, d.lift, d.k);
      d.p.setAttribute("d", shape.d);
      d.hit.setAttribute("d", shape.d);
      if (d.chip) { d.chip.style.left = `${shape.mid.x}px`; d.chip.style.top = `${shape.mid.y}px`; }
    }
  }
  function outPoint(box, port) {
    const dot = box.querySelector(`.g-port.out[data-port="${CSS.escape(port || "")}"]`) || box.querySelector(".g-port.out");
    return dot ? centerIn(dot, box) : null;
  }
  const inPoint = (box, e) => { const dot = (e && box.querySelector(`.g-port.in[data-edge="${CSS.escape(edgeKey(e))}"]`)) || box.querySelector(".g-port.in"); return dot ? centerIn(dot, box) : { x: box.offsetLeft, y: box.offsetTop + 24 }; };
  function centerIn(dot, box) {
    let x = dot.offsetWidth / 2, y = dot.offsetHeight / 2, n = dot;
    while (n && n !== box) { x += n.offsetLeft; y += n.offsetTop; n = n.offsetParent; }
    return { x: box.offsetLeft + x, y: box.offsetTop + y };
  }
  function toCanvas(ev, canvas) {
    const r = canvas.getBoundingClientRect();
    return { x: (ev.clientX - r.left) / ed.zoom, y: (ev.clientY - r.top) / ed.zoom };
  }
  function startDrag(ev, n, box, canvas, svg) {
    if (ev.button !== 0) return;
    ev.stopPropagation();
    const start = toCanvas(ev, canvas);
    const ox = n.x, oy = n.y;
    let moved = false;
    const nodeEls = new Map([...canvas.querySelectorAll(".g-node")].map((b) => [b.dataset.id, b]));
    let frame = 0;
    const move = (e) => {
      const p = toCanvas(e, canvas);
      n.x = Math.round(ox + p.x - start.x);
      n.y = Math.round(oy + p.y - start.y);
      box.style.left = `${n.x}px`;
      box.style.top = `${n.y}px`;
      moved = true;
      // One redraw per screen frame, however many pointer events arrive.
      frame ||= requestAnimationFrame(() => {
        frame = 0;
        const t0 = performance.now();
        moveEdges(svg, nodeEls, n.id);
        window.ArkismPerf?.note("canvas.drag", performance.now() - t0, ed.graph.edges.length);
      });
    };
    const up = () => { window.removeEventListener("pointermove", move); window.removeEventListener("pointerup", up); if (moved) touch(); };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  }
  function startLink(ev, n, port, canvas, svg) {
    ev.stopPropagation();
    ev.preventDefault();
    const box = canvas.querySelector(`.g-node[data-id="${CSS.escape(n.id)}"]`);
    const from = outPoint(box, port);
    const temp = document.createElementNS("http://www.w3.org/2000/svg", "path");
    temp.setAttribute("class", "g-edge drawing");
    svg.append(temp);
    const sx = ev.clientX, sy = ev.clientY;
    const move = (e) => temp.setAttribute("d", curve(from, toCanvas(e, canvas)));
    const up = (e) => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      temp.remove();
      // A click on the + (no drag): pick the target node next.
      if (Math.abs(e.clientX - sx) + Math.abs(e.clientY - sy) < 5) { ed.linking = { from: n.id, port }; render(false); return; }
      const target = document.elementFromPoint(e.clientX, e.clientY)?.closest(".g-node");
      if (target && target.dataset.id !== n.id) linkTo(n, port, target.dataset.id);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  }

  // A new line from n's port to node `to`; it opens in the right panel to pick its kind.
  function linkTo(n, port, to) {
    ed.linking = null;
    const toNode = nodeIn(to);
    if (!n || !toNode || to === n.id) { render(false); return; }
    if (["start", "schedule", "webhook"].includes(toNode.type)) { notice("시작 노드에는 선을 이을 수 없어요."); render(false); return; }
    if (ed.graph.edges.some((x) => x.from === n.id && x.to === to && (x.port || "") === port)) { render(false); return; }
    const edge = { from: n.id, to };
    if (port) edge.port = port;
    if (wouldCycle(ed.graph, n.id, to)) {
      edge.max = 2;
      notice("되돌아가는 선이에요. 최대 2번까지 다시 돌아요. 선을 눌러 횟수와 갈래 이름을 바꿀 수 있어요.");
    }
    ed.graph.edges.push(edge);
    ed.selEdge = ed.graph.edges.length - 1;
    ed.sel = null;
    ed.review = false;
    touch();
    render(false);
  }
  // 지우기 확인 (241:27886 · 241:27897): a small window instead of window.confirm.
  function confirmBox(title, text, okLabel) {
    return new Promise((resolve) => {
      const dlg = el("dialog", "crew-dialog wf-dialog wf-confirm");
      const form = el("form");
      form.method = "dialog";
      const body = el("div", "dialog-body");
      body.append(el("h2", "", title), el("p", "", text));
      const foot = el("div", "dialog-foot");
      const cancel = button("취소", "");
      cancel.type = "submit";
      cancel.value = "cancel";
      const ok = button(okLabel, "danger");
      ok.type = "submit";
      ok.value = "ok";
      foot.append(cancel, ok);
      form.append(body, foot);
      let yes = false;
      form.addEventListener("submit", (e) => { yes = e.submitter?.value === "ok"; });
      dlg.append(form);
      dlg.addEventListener("close", () => { dlg.remove(); resolve(yes); });
      document.body.append(dlg);
      dlg.showModal();
      cancel.focus();
    });
  }
  const closeX = (label, onClick) => {
    const x = el("button", "icon-btn plain");
    x.type = "button";
    x.setAttribute("aria-label", label);
    x.append(icon("x"));
    x.addEventListener("click", onClick);
    return x;
  };
  const deselect = () => { ed.sel = null; ed.selEdge = null; render(false); };
  // Field builders call redraw(): the canvas normally, the open node window while one is up.
  let redraw = () => render(false);
  let nodeDlg = null;

  // ---------------------------------------------------------------- node window (1024:60724 … 1033:69906)
  // A field whose control is a row of buttons: a <div>, since a <label> would click its first button.
  function group(label, control, hint) {
    const f = el("div", "field", label);
    f.append(control);
    if (hint) f.append(el("small", "dim", hint));
    return f;
  }
  function nodeBody(box, n) {
    if (IS_AI(n.type) || n.type === "team") { aiFields(box, n); return; }
    const title = input(n.title, { maxLength: 80 });
    title.addEventListener("input", () => { n.title = title.value; touch(); syncCard(n); });
    box.append(field("이름", title));
    let pair = null; // n8n: 결과 받기 and 기다릴 최대 시간 share a row (1033:69906)
    const starts = ["start", "schedule", "webhook"].includes(n.type);
    if (starts) triggerFields(box, n);
    // 반복 (목록 위치 · 동시에 도는 개수) and 하위 워크플로우 (부를 워크플로우 · 넘길 일) sit side by side (1033:67xxx)
    const row = ["loop", "subflow"].includes(n.type) ? el("div", "scope-fields") : null;
    if (row) box.append(row);
    for (const [key, label, kind, opt, hint] of starts ? [] : FIELDS[n.type] || []) {
      let i;
      if (kind === "radio") {
        const f = group(label, segment(opt, n.config[key] ?? opt[0][0], (v) => { n.config[key] = v; touch(); redraw(); }), hint);
        if (n.type === "n8n") { pair = el("div", "scope-fields"); pair.append(f); box.append(pair); } else box.append(f);
        continue;
      }
      if (kind === "select") i = select(opt, n.config[key] ?? opt[0][0]);
      else if (kind === "workflow") i = select([["", "고르세요"], ...workflows().filter((w) => w.id !== ed.rec.id).map((w) => [w.crew.handle, `$${w.crew.handle} · ${w.crew.name}`])], (n.config[key] || "").replace(/^\$/, ""));
      else if (kind === "code" || (kind === "area" && n.type !== "subflow")) i = input(n.config[key], { tag: "textarea", rows: kind === "code" ? 12 : 4, cls: kind === "code" ? "code" : "" });
      else i = input(n.config[key], { placeholder: typeof opt === "string" ? opt : "" });
      i.addEventListener("input", () => { n.config[key] = i.value; touch(); });
      i.addEventListener("change", () => {
        n.config[key] = i.value;
        touch();
        if (key === "workflow") { const w = byHandle(i.value); if (w && (!n.title || n.title === NODES.subflow.label)) n.title = w.crew.name; redraw(); } else syncCard(n);
      });
      const fi = ["question", "message", "task"].includes(key) ? aiField(i, n, key, label) : i;
      if (pair) { pair.append(field(label, fi)); if (hint) box.append(el("p", "meta", hint)); pair = null; continue; }
      (row || box).append(field(label, fi, hint));
    }
    sends(box, n);
    if (n.type === "code") box.append(el("p", "meta", "출력이 {\"ok\": true/false, …}면 통과·실패 갈래로 나뉘어요. 처음 한 번은 빌드에 1~2초, 30초 안에 끝나야 해요. 토큰은 들지 않아요."));
    if (n.type === "approval") box.append(el("p", "meta", "배포·게시처럼 되돌릴 수 없는 단계 바로 앞에 두세요. 답이 올 때까지 멈춰요."));
    if (n.type === "subflow") box.append(el("div", "wf-attn small", "자기 자신을 부를 수 없어요. 최대 세 겹까지 연결해요."));
    if (n.type === "n8n") box.append(el("p", "meta", "끝나면 알려줌을 고르면 n8n이 callback_url로 결과를 돌려줄 때까지 멈춰요. 서버에 공개 주소가 있어야 해요."));
    if (WORK(n) && !ownerOf(ed.rec)) box.append(onlyRun(n));
  }
  // 시작 노드 팝업 (1033:66756): how the workflow starts. It edits a draft of the workflow's trigger that 저장 keeps.
  function triggerFields(box, n) {
    const tr = trigDraft(n);
    box.append(group("시작", segment([["", "직접 부름"], ["schedule", "일정"], ["webhook", "웹훅"]], tr.kind, (v) => { tr.kind = v; touch(); redraw(); })));
    if (tr.kind === "schedule") {
      const cron = input(tr.cron, { maxLength: 64, cls: "mono" });
      const hintOf = () => { const x = nextRun(cron.value); return x ? `${cronText(cron.value)} · 다음 실행 ${new Intl.DateTimeFormat(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric" }).format(x)}` : "형식: 분 시 일 월 요일 (예: 0 9 * * 1-5)"; };
      const f = field("언제 (분 시 일 월 요일)", cron);
      const hint = el("small", "dim", hintOf());
      f.append(hint);
      cron.addEventListener("input", () => { tr.cron = cron.value; if (n.type === "schedule") n.config.cron = cron.value; hint.textContent = hintOf(); touch(); });
      box.append(f);
    }
    if (tr.kind === "webhook") {
      const label = input(tr.label, { maxLength: 60, placeholder: "GitHub PR 열림" });
      label.addEventListener("input", () => { tr.label = label.value; if (n.type === "webhook") n.config.note = label.value; touch(); });
      box.append(field("웹훅 메모", label, "주소와 토큰은 서버에서 arkism-crew hook-url로 받아요"));
    }
    if (!tr.kind) box.append(el("p", "meta", `$${ed.rec.crew.handle}로 부르거나 실행을 눌렀을 때 시작해요.`));
  }
  const nodeKind = (n) => (n.type === "team" ? "팀 노드" : HUMAN_KIND[NODES[n.type]?.group] || "노드");
  const nodeLine = (n) => (IS_AI(n.type) || n.type === "team" ? nodeKind(n) : `${nodeKind(n)} · ${NODES[n.type]?.sub || ""}`);
  // The node window edits the node in place; 취소 (or ×, Esc) puts the graph back as it was when the window opened.
  function openNodeDialog(n, before = { graph: structuredClone(ed.graph), dirty: ed.dirty, trigger: structuredClone(ed.trigger) }) {
    if (!n) return;
    nodeDlg?.remove();
    let keep = false;
    const dlg = (nodeDlg = el("dialog", `crew-dialog wf-dialog node-dialog${IS_AI(n.type) || n.type === "team" ? " wide" : ""}`));
    const head = el("div", "dialog-head");
    const lead = el("span", "g-icon");
    const t = el("div", "");
    const h = el("h2", "");
    const sub = el("p", "");
    t.append(h, sub);
    head.append(lead, t, closeX("노드 설정 닫기", () => dlg.close()));
    const body = el("div", "dialog-body");
    const card = el("div", "node-card");
    body.append(card);
    const foot = el("div", "dialog-foot");
    const left = el("div", ""), right = el("div", "");
    left.append(button("적용", "primary", null, () => { keep = true; dlg.close(); }), button("취소", "ghost", null, () => dlg.close()));
    if (WORK(n) && !ownerOf(ed.rec)) right.append(button("이 노드만 실행", "", null, () => {
      const saved = !ed.dirty && (ed.rec.crew.graph?.nodes || []).some((x) => x.id === n.id);
      if (!saved) { notice("저장한 뒤에 실행할 수 있어요."); return; }
      keep = true;
      dlg.close();
      void startRun(ed.rec, "", n.id);
    }));
    right.append(button("노드 지우기", "ghost", null, async () => {
      if (await confirmBox("노드를 지울까요?", "이 노드와 이어진 선이 함께 지워져요. 저장하지 않고 나가면 원래대로 돌아가요.", "노드 지우기")) { keep = true; dlg.close(); removeNode(n.id); }
    }));
    foot.append(left, right);
    const draw = () => {
      card.replaceChildren();
      nodeBody(card, n);
      const ex = n.type === "expert" ? byId(n.config?.expert) : null;
      lead.replaceChildren(ex ? avatarMark(ex.crew) : n.type === "team" ? teamMark(byId(n.config?.team)) : icon(NODES[n.type]?.icon || "stack"));
      lead.classList.toggle("plain", !ex && n.type !== "team");
      h.textContent = nodeTitle(n);
      sub.textContent = nodeLine(n);
    };
    redraw = () => { const y = body.scrollTop; draw(); body.scrollTop = y; render(false); keepDlg(); };
    const keepDlg = () => { if (nodeDlg === dlg && !dlg.isConnected) document.body.append(dlg); };
    dlg.append(head, body, foot);
    dlg.addEventListener("close", () => {
      dlg.remove();
      if (nodeDlg === dlg) { nodeDlg = null; redraw = () => render(false); }
      if (!keep) { ed.graph.nodes = before.graph.nodes; ed.graph.edges = before.graph.edges; ed.dirty = before.dirty; ed.trigger = before.trigger; }
      if (!ed.graph.nodes.some((x) => x.id === ed.sel)) ed.sel = null; // the panel stays on this node
      render(false);
    });
    draw();
    document.body.append(dlg);
    dlg.showModal();
  }
  // ---------------------------------------------------------------- inspector (198:2350 · 321:19936 · 321:21027)
  function inspector() {
    const box = el("aside", "g-right");
    if (ed.selEdge !== null && ed.graph.edges[ed.selEdge]) return edgeInspector(box, ed.graph.edges[ed.selEdge]);
    if (ed.sel && nodeIn(ed.sel)) return nodePanel(box, nodeIn(ed.sel));
    {
      const x = ed.rec.crew;
      const backs = ed.graph.edges.filter(isBack);
      box.append(el("h3", "", "워크플로우"), kv([
        ["$이름", `$${x.handle}`],
        ["워커 기본", x.model || "노드마다"],
        ["멈추는 조건", [backs.length ? `되돌아가기 ${Math.max(...backs.map((e) => e.max))}번` : "", x.limits?.max_steps ? `단계 ${x.limits.max_steps}번` : "", x.limits?.max_minutes ? `${x.limits.max_minutes}분` : ""].filter(Boolean).join(" · ") || "정하지 않음"],
        ["일정·웹훅", x.trigger ? `${triggerText(x)}${x.enabled ? "" : " · 꺼짐"}` : "없어요"]
      ]), button("워크플로우 설정", "", "gear-six", () => openSettings(ed.rec, null, true)), el("i", "wf-rule"));
      box.append(el("h3", "", "노드 설정"), el("p", "dim", "노드를 누르면 여기서 고쳐요. 출력 점을 끌어 다음 노드에 이어요. 앞 노드로 이으면 되돌아가는 선이 돼요. 선을 누르면 선 설정이 열려요. 휠로 확대·축소하고, 휠 클릭이나 스페이스를 누른 채 끌면 화면이 움직여요."));
      const g = ed.graph;
      box.append(kv([["노드", `${g.nodes.length}개`], ["AI 노드", `${aiNodes(g).length}개`], ["되돌아가는 선", `${g.edges.filter(isBack).length}개`]]));
      box.append(button("구조 검토", "", "sparkle", () => { ed.review = true; render(false); }));
      return box;
    }
  }
  // A team's mark: its cover photo, or the members' faces.
  function teamMark(t) {
    const x = t?.crew;
    if (x?.cover?.thumb || x?.cover?.url) {
      const img = el("img", "node-team-photo");
      img.alt = "";
      img.src = x.cover.thumb || x.cover.url;
      return img;
    }
    return x ? teamFaces(x) : icon("users-three");
  }
  function teamFaces(x) {
    const f = el("span", "node-faces");
    for (const m of (x.members || []).slice(0, 5)) { const a = byId(m.agent_id); if (a) f.append(avatarMark(a.crew)); }
    if (!f.childElementCount) f.append(icon("users-three"));
    return f;
  }
  // 전문가·팀 고르기 (1024:60724 · 1024:61783): the picked one as a face card with 바꾸기, the others as chips.
  function pickCard(label, cur, all, onPick) {
    const wrap = el("div", "field");
    wrap.append(label);
    const card = el("div", "node-pick");
    const lead = el("span", "node-pick-lead");
    const txt = el("div", "");
    if (cur) {
      const x = cur.crew;
      lead.append(x.type === "team" ? teamMark(cur) : avatarMark(x));
      const line = x.type === "team"
        ? [`@${x.handle}`, MODES[x.mode] || "", x.ending?.max_rounds ? `최대 ${x.ending.max_rounds}라운드` : ""]
        : [`@${x.handle}`, x.model || "모델 자동", x.skills?.length ? `스킬 ${x.skills.length}개` : ""];
      txt.append(el("strong", "", x.name), el("small", "dim", line.filter(Boolean).join(" · ")));
      if (x.type === "team") txt.append(teamFaces(x));
    } else txt.append(el("strong", "dim", "고르세요"));
    const change = select([["", cur ? "바꾸기" : "고르기"], ...all.map((r) => [r.id, r.crew.name])], "");
    change.className = "node-pick-change";
    change.setAttribute("aria-label", `${label} 바꾸기`);
    change.addEventListener("change", () => { const r = byId(change.value); if (r) onPick(r); });
    card.append(lead, txt, change);
    const chips = el("div", "chip-row");
    for (const r of all.filter((r) => r !== cur).slice(0, 4)) {
      const b = el("button", "chip node-chip");
      b.type = "button";
      b.append(r.crew.type === "team" ? teamMark(r) : avatarMark(r.crew), r.crew.name);
      b.addEventListener("click", () => onPick(r));
      chips.append(b);
    }
    const add = el("button", "chip node-chip dim", label === "팀" ? "+ 새 팀" : "+ 새 전문가");
    add.type = "button";
    add.addEventListener("click", () => newMemberPopup(label === "팀", onPick));
    chips.append(add);
    wrap.append(card, chips);
    return wrap;
  }
  // + 새 전문가 · + 새 팀 (1024:60724 · 1024:61783): make one right from the node. A new expert needs a name, @이름 and instruction;
  // a new team starts with the expert picked here as its worker.
  function newMemberPopup(isTeam, onPick) {
    const first = agents()[0];
    if (isTeam && !first) { notice("팀은 전문가로 만들어요. 먼저 전문가를 만들어 주세요."); return; }
    const dlg = el("dialog", "crew-dialog ex-skill-dlg");
    const head = el("div", "dialog-head");
    head.append(el("h2", "", isTeam ? "새 팀" : "새 전문가"));
    const body = el("div", "dialog-body");
    const name = input("", { maxLength: 60, placeholder: isTeam ? "예: 릴리스 팀" : "예: 릴리스 매니저" });
    const handle = input("", { maxLength: 40, pattern: "[\\p{L}\\p{N}_\\-]{1,40}", placeholder: "띄어쓰기 없이" });
    name.addEventListener("input", () => { if (!handle.dataset.touched) handle.value = name.value.replace(/\s+/g, "").slice(0, 40); });
    handle.addEventListener("input", () => { handle.dataset.touched = handle.value ? "1" : ""; });
    const ins = input("", { tag: "textarea", rows: 3, maxLength: 8000, placeholder: "너는 ○○다. …한다." });
    const firstPick = select(agents().map((r) => [r.id, r.crew.name]), first?.id);
    body.append(field("이름", name), field("@이름", handle), ...(isTeam ? [field("첫 전문가", firstPick), el("p", "meta", "팀 화면에서 더 넣거나 고칠 수 있어요.")] : [field("지시", ins)]));
    const foot = el("div", "dialog-foot");
    foot.append(button("취소", "ghost", null, () => dlg.close()), button("만들기", "primary", null, async () => {
      const n = name.value.trim(), h = handle.value.trim().replace(/^@/, "");
      if (!n || !/^[\p{L}\p{N}_-]{1,40}$/u.test(h) || (!isTeam && !ins.value.trim())) { notice(isTeam ? "이름과 @이름(띄어쓰기 없이)을 채워 주세요." : "이름·@이름·지시를 채워 주세요."); return; }
      if (agents().some((r) => r.crew.handle === h) || teams().some((r) => r.crew.handle === h)) { notice(`@${h}은 이미 있어요. 다른 이름을 써 주세요.`); return; }
      const crew = isTeam
        ? { type: "team", name: n, handle: h, role: "", members: [{ agent_id: firstPick.value, order: 1, role: "worker" }], mode: "round_robin",
          ending: { max_rounds: 4, token_cap: 100000, end_on_all_approved: true, end_on_moderator: true }, context: [], templates: [] }
        : workerOut({ ...blankAgent(), name: n, handle: h, instructions: ins.value });
      try {
        const saved = await save(null, crew);
        state.data.crew.push(saved);
        dlg.close();
        onPick(saved);
      } catch (e) { notice(errorText(e)); }
    }));
    dlg.append(head, body, foot);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal();
    name.focus();
  }
  // AI·전문가·팀 노드 (1024:60724 · 1024:61783): 누가 맡나 · 무엇을 하나 on the left, 실행 범위 · 나눠서 맡기기 on the right.
  // 무엇·어디까지(목표·울타리·완료 기준)만 정하고, 어떻게는 AI가 정해요.
  function aiFields(box, n) {
    const c = n.config;
    const bind = (i, key, again) => {
      i.addEventListener("input", () => { c[key] = i.value; touch(); });
      i.addEventListener("change", () => { c[key] = i.value; touch(); if (again) redraw(); });
      return i;
    };
    const cols = el("div", "node-cols");
    const L = el("div", "node-col"), R = el("div", "node-col");
    cols.append(L, R);
    box.append(modelList(), cols);
    // 누가 맡나
    L.append(el("h3", "", "누가 맡나"));
    L.append(group("노드 종류", segment([["expert", "전문가"], ["team", "팀"], ["ai", "AI 에이전트"], ["ai_once", "AI에게 한 번 묻기"]], n.type, (k) => {
      if (k === n.type) return;
      const was = n.type, text = c.goal || c.prompt || c.topic || "{{input}}";
      n.type = k;
      if (k === "team") c.topic ||= text;
      else if (k === "ai_once") c.prompt ||= text;
      else { c.goal ||= text; c.access ||= "read"; }
      if (!n.title || n.title === NODES[was]?.label) n.title = NODES[k].label;
      touch();
      redraw();
    })));
    const named = (r, label) => { if (!n.title || n.title === label || n.title === NODES[n.type].label) n.title = r.crew.name; };
    let p = null;
    if (n.type === "expert") {
      const cur = byId(c.expert);
      L.append(pickCard("전문가", cur, agents(), (r) => { named(r, cur?.crew.name); c.expert = r.id; touch(); redraw(); }));
      p = cur;
    } else if (n.type === "team") {
      const cur = byId(c.team);
      L.append(pickCard("팀", cur, teams(), (r) => { named(r, cur?.crew.name); c.team = r.id; touch(); redraw(); }));
    } else {
      const row = el("div", "scope-fields");
      row.append(field("모델", bind(modelInput(c.model, ed.rec.crew.model || "deepseek-v4-flash"), "model", true), ed.rec.crew.model ? `비우면 ${ed.rec.crew.model}` : ""),
        field("추론", bind(select(EFFORTS, c.effort || "medium"), "effort")));
      L.append(row);
      const persona = bind(select([["", "직접 쓰기"], ...personas().map((x) => [x.id, x.crew.name])], c.persona || ""), "persona", true);
      L.append(field("페르소나", persona, "설정 › 워크플로우에서 만든 페르소나를 고르거나 아래에 직접 써요"));
      p = byId(c.persona);
      if (p) { const q = el("div", "quote"); q.append(el("div", "", p.crew.instructions || "")); L.append(q); }
    }
    // 무엇을 하나
    L.append(el("i", "wf-rule"), el("h3", "", "무엇을 하나"));
    const title = input(n.title, { maxLength: 80 });
    title.addEventListener("input", () => { n.title = title.value; touch(); });
    L.append(field("노드 이름", title));
    const slot = "{{input}} 자리에 앞 단계 결과가 들어가요";
    if (n.type === "team") L.append(field("회의 주제", bind(input(c.topic, { tag: "textarea", rows: 3, maxLength: 4000 }), "topic", true), slot));
    else if (n.type === "ai_once") L.append(field("질문", bind(input(c.prompt, { tag: "textarea", rows: 4, maxLength: 8000 }), "prompt", true), slot));
    else L.append(field("목표", bind(input(c.goal, { tag: "textarea", rows: 3, maxLength: 8000, placeholder: "하위 질문마다 출처 3개 이상 모아 줘." }), "goal", true), slot));
    if (n.type !== "team") L.append(field(p ? "더할 지시" : "페르소나 지시", bind(input(c.instructions, { tag: "textarea", rows: 3, maxLength: 8000, placeholder: "너는 ○○다. …한다." }), "instructions")), skillsField(c));
    if (n.type === "expert" || n.type === "ai" || n.type === "team") {
      // 실행 범위
      R.append(el("h3", "", "실행 범위"),
        field("허용 범위", bind(input(c.fence, { maxLength: 4000, placeholder: "웹 검색과 자료 읽기만 허용" }), "fence")),
        field("완료 기준", bind(input(c.done, { maxLength: 1000, placeholder: "질문마다 출처 3개와 요약" }), "done")),
        group("하는 일", segment([["read", "읽기만 · 동시"], ["write", "쓰기 · 차례로"]], c.access || "read", (v) => { c.access = v; touch(); redraw(); })),
        field("실패하면 다시", bind(select([["0", "안 함"], ["1", "1번"], ["2", "2번"], ["3", "3번"]], c.retries || "0"), "retries")));
      if (n.type !== "team") collabFields(R, n, bind);
    }
    sends(R, n);
    if (WORK(n) && !ownerOf(ed.rec)) R.append(onlyRun(n));
  }
  // 나눠서 맡기기: this node plans, workers (saved experts) do the parts at the same time, this node merges them.
  function collabFields(R, n, bind) {
    const c = n.config;
    const on = c.collab === "on";
    const first = () => agents()[0];
    const ch = el("div", "g-collab-head");
    ch.append(el("h3", "", "나눠서 맡기기"), switchEl(on, "나눠서 맡기기 켜기", (v) => {
      c.collab = v ? "on" : "";
      if (v && !c.workers) c.workers = JSON.stringify([first() ? { expert: first().id } : { name: "워커", model: ed.rec.crew.model || "deepseek-v4-flash" }]);
      touch();
      redraw();
    }));
    R.append(el("i", "wf-rule"), ch);
    if (!on) { R.append(el("p", "meta", "켜면 이 노드가 일을 나눠 워커에게 맡기고 결과를 합쳐요. 하위 질문이 몇 개일지 미리 모를 때 써요.")); return; }
    let workers = [];
    try { workers = JSON.parse(c.workers || "[]"); } catch { workers = []; }
    const saveWorkers = () => { c.workers = JSON.stringify(workers); touch(); };
    const list = el("div", "g-workers");
    workers.forEach((w, i) => {
      const r = el("div", "g-worker");
      const del = el("button", "icon-btn plain");
      del.type = "button";
      del.setAttribute("aria-label", "워커 빼기");
      del.append(icon("x"));
      del.addEventListener("click", () => { workers.splice(i, 1); saveWorkers(); redraw(); });
      if (w.expert || !w.model) {
        // An expert worker: its model and instructions follow the expert (the runner reads them).
        const a = byId(w.expert);
        const lead = el("span", "node-pick-lead");
        lead.append(a ? avatarMark(a.crew) : icon("robot"));
        const pick = select([["", "전문가 고르기"], ...agents().map((x) => [x.id, x.crew.role ? `${x.crew.name} · ${x.crew.role}` : x.crew.name])], w.expert || "");
        pick.setAttribute("aria-label", "워커 전문가");
        pick.addEventListener("change", () => { workers[i] = pick.value ? { expert: pick.value } : { name: "", model: "" }; saveWorkers(); redraw(); });
        r.classList.add("expert");
        r.append(lead, pick, del);
      } else {
        // A worker saved before experts: a name and a model.
        const nm = input(w.name, { maxLength: 40, placeholder: "조사원" });
        const md = modelInput(w.model, ed.rec.crew.model || "deepseek-v4-flash");
        nm.addEventListener("input", () => { w.name = nm.value; saveWorkers(); });
        md.addEventListener("input", () => { w.model = md.value; saveWorkers(); });
        r.append(nm, md, del);
      }
      list.append(r);
    });
    R.append(group("워커 — 전문가", list, "워커의 모델은 그 전문가 설정을 따라요"),
      button("워커 더하기", "ghost small", "plus", () => { workers.push(first() ? { expert: first().id } : { name: "", model: ed.rec.crew.model || "" }); saveWorkers(); redraw(); }));
    const row3 = el("div", "scope-fields");
    row3.append(field("최대 개수", bind(input(c.max_workers || "5", { type: "number", min: 1, max: 16 }), "max_workers", true)), field("동시에", bind(input(c.concurrency || "3", { type: "number", min: 1, max: 16 }), "concurrency", true)));
    const row4 = el("div", "scope-fields");
    row4.append(field("필요한 것", bind(input(c.needs, { maxLength: 200, placeholder: "검색 결과" }), "needs")), field("만드는 것", bind(input(c.makes, { maxLength: 200, placeholder: "질문별 자료 묶음" }), "makes")));
    R.append(row3, row4, el("p", "meta", "실행할 때 AI가 일을 몇 개로 나눌지 정하고, 워커 결과를 이 노드가 합쳐요."));
  }
  // 스킬: the hub skills this step must follow (the runner puts their SKILL.md into its instructions).
  // Empty leaves it to the AI, which finds skills through the skill router as usual.
  function skillsField(c) {
    const all = state.data.skills || [];
    const picked = (c.skills || "").split(/[,\n]/).map((s) => s.trim()).filter(Boolean);
    const save = (list) => { c.skills = list.join(","); touch(); redraw(); };
    const row = el("div", "chip-row");
    for (const id of picked) {
      const chip = el("span", "chip", all.find((s) => s.id === id)?.name || id);
      const x = el("button", "icon-btn plain");
      x.type = "button";
      x.setAttribute("aria-label", `${id} 빼기`);
      x.append(icon("x"));
      x.addEventListener("click", () => save(picked.filter((p) => p !== id)));
      chip.append(x);
      row.append(chip);
    }
    const dl = el("datalist");
    dl.id = "crew-skills";
    for (const s of all) if (!picked.includes(s.id)) { const o = el("option", "", (s.description || "").slice(0, 80)); o.value = s.id; dl.append(o); }
    const add = input("", { maxLength: 128, placeholder: "스킬 이름으로 찾아 더하기" });
    add.setAttribute("list", "crew-skills");
    add.addEventListener("change", () => { const v = add.value.trim(); if (all.some((s) => s.id === v) && !picked.includes(v)) save([...picked, v]); });
    const f = el("div", "field", "스킬");
    f.append(row, add, dl, el("small", "dim", "고르면 이 단계는 그 스킬을 꼭 따라요. 비우면 AI가 알아서 찾아 써요."));
    return f;
  }
  // 받는 값 · 보내는 곳: where this node's input comes from and where its branches go.
  function sends(box, n) {
    const ins = ed.graph.edges.filter((e) => e.to === n.id && !isBack(e));
    if (ins.length) {
      const s = el("div", "g-sends");
      s.append(el("h4", "", "받는 값"));
      for (const e of ins) {
        const from = ed.graph.nodes.find((x) => x.id === e.from);
        const r = el("div", "g-send");
        r.append(el("span", "mono", ins.length > 1 || n.type === "merge" ? `in.${e.from}` : "{{input}}"), el("span", "", `← ${from ? nodeTitle(from) : e.from}${e.port ? ` (${e.port})` : ""}`));
        s.append(r);
      }
      box.append(s);
    }
    const outs = ed.graph.edges.filter((e) => e.from === n.id);
    if (outs.length) {
      const s = el("div", "g-sends");
      s.append(el("h4", "", IS_AI(n.type) && new Set(outs.map((e) => e.port || "")).size > 1 ? "보내는 곳 · AI가 하나를 골라요" : "보내는 곳"));
      for (const e of outs) {
        const to = ed.graph.nodes.find((x) => x.id === e.to);
        const r = el("div", "g-send");
        const label = PORTS[n.type]?.find(([p]) => p === e.port)?.[1] || e.port || "다음";
        r.append(el("span", "dim", isBack(e) ? `${label} · ↺ 최대 ${e.max}번` : label), el("span", "", `→ ${to ? nodeTitle(to) : e.to}`));
        s.append(r);
      }
      box.append(s);
    }
  }
  function edgeInspector(box, e) {
    const from = ed.graph.nodes.find((n) => n.id === e.from), to = ed.graph.nodes.find((n) => n.id === e.to);
    const head = el("div", "g-right-head");
    const t = el("div", "");
    t.append(el("strong", "", isBack(e) ? "되돌아가는 선" : "선"), el("small", "", `${nodeTitle(from || {})} → ${nodeTitle(to || {})}`));
    head.append(t, closeX("선 설정 닫기", deselect));
    box.append(head);
    if (from && IS_AI(from.type)) {
      const port = input(e.port, { maxLength: 20, placeholder: "통과, 근거 부족…" });
      port.addEventListener("change", () => { const v = port.value.trim(); if (v) e.port = v; else delete e.port; touch(); render(false); });
      box.append(field("갈래 이름", port, "이름 붙은 갈래가 둘 이상이면 AI가 결과를 보고 하나를 골라요"));
    } else if (from) box.append(edgeKinds(e, from));
    if (from && !isBack(e)) box.append(fanOut(e, from));
    const back = wouldCycle({ ...ed.graph, edges: ed.graph.edges.filter((x) => x !== e) }, e.from, e.to);
    if (back) {
      const max = select(Array.from({ length: 10 }, (_, i) => [String(i + 1), `${i + 1}번`]), String(e.max || 2));
      max.addEventListener("change", () => { e.max = Number(max.value); touch(); render(false); });
      box.append(field("최대 몇 번 되돌아갈까요", max, "넘으면 실행을 멈추고 이유를 남겨요. 성공한 노드 결과는 그대로 두고 대상부터 다시 돌아요."));
    } else box.append(el("p", "meta", "앞으로 가는 선이에요. 앞 노드로 이으면 되돌아가는 선이 돼요."));
    if (!from || !IS_AI(from.type)) {
      const info = el("div", "wf-info");
      info.append(el("strong", "", "AI 노드에서 나가는 선이면"), el("p", "", "갈래 이름(예: more, 근거 부족)을 붙일 수 있어요. 이름 붙은 갈래가 둘 이상이면 AI가 결과를 보고 하나를 골라요."));
      box.append(info);
    }
    box.append(button("선 지우기", "ghost danger", "trash", async () => {
      if (!(await confirmBox("선을 지울까요?", `${nodeTitle(from || {})} → ${nodeTitle(to || {})} 선이 지워져요. 저장하지 않고 나가면 원래대로 돌아가요.`, "선 지우기"))) return;
      ed.graph.edges = ed.graph.edges.filter((x) => x !== e);
      ed.selEdge = null;
      touch();
      render(false);
    }));
    return box;
  }
  // 선 종류: what the engine follows. A plain line (no port) always goes on; approval·condition·code end on a named port.
  // There is no line for "the node failed": a failed node stops the run (code's 실패 is its {"ok": false} output).
  const EDGE_KINDS = [["", "항상", null], ["approve", "승인", "approval"], ["reject", "거절", "approval"], ["yes", "예", "condition"], ["no", "아니오", "condition"], ["pass", "성공 시", "code"], ["fail", "실패 시", "code"]];
  function edgeKinds(e, from) {
    const wrap = el("div", "field");
    wrap.append("선 종류");
    const list = el("div", "g-kinds");
    for (const [port, label, only] of EDGE_KINDS) {
      const b = el("button", `g-kind${(e.port || "") === port ? " active" : ""}`);
      b.type = "button";
      const why = !only || only === from.type ? ""
        : port === "fail" ? "아직 서버 미지원"
        : port === "pass" ? "다른 노드는 항상과 같아요"
        : only === "approval" ? "사람 확인 노드에서만" : "조건 노드에서만";
      b.append(el("span", "", label));
      if (why) { b.disabled = true; b.append(el("small", "dim", why)); }
      b.setAttribute("aria-pressed", String((e.port || "") === port));
      b.addEventListener("click", () => { if (port) e.port = port; else delete e.port; touch(); render(false); });
      list.append(b);
    }
    wrap.append(list);
    return wrap;
  }
  // 동시에 보내기: more lines from the same port; the engine follows every one of them at once.
  function fanOut(e, from) {
    const same = ed.graph.edges.filter((x) => x.from === e.from && (x.port || "") === (e.port || "") && !isBack(x));
    const wrap = el("div", "field");
    wrap.append("동시에 보내기");
    if (same.length > 1) wrap.append(el("small", "dim", `이 갈래는 ${same.length}곳으로 동시에 가요: ${same.map((x) => nodeTitle(nodeIn(x.to) || {})).join(", ")}`));
    const free = ed.graph.nodes.filter((x) => x.id !== from.id && !["start", "schedule", "webhook"].includes(x.type) && !same.some((y) => y.to === x.id));
    const add = select([["", "+ 함께 보낼 노드"], ...free.map((x) => [x.id, nodeTitle(x)])], "");
    add.addEventListener("change", () => { if (add.value) linkTo(from, e.port || "", add.value); });
    wrap.append(add);
    return wrap;
  }
  // 이 노드만 실행: the saved graph with just this node, given the value typed here (as {{input}}).
  function onlyRun(n) {
    const s = el("div", "g-sends");
    s.append(el("h4", "", "이 노드만 실행"));
    const row = el("div", "try-row");
    const q = el("input");
    q.placeholder = "넣을 값 (비워도 돼요)";
    const go2 = button("실행", "small", "play", async () => {
      go2.disabled = true;
      await startRun(ed.rec, q.value.trim(), n.id);
      go2.disabled = false;
    });
    const saved = !ed.dirty && (ed.rec.crew.graph?.nodes || []).some((x) => x.id === n.id);
    go2.disabled = !saved;
    if (!saved) go2.title = "저장한 뒤에 실행할 수 있어요";
    row.append(q, go2);
    s.append(row, el("p", "meta", "앞 단계는 건너뛰고 이 노드에만 값을 넣어 돌려요. 결과는 최근 실행에 남아요."));
    return s;
  }

  // ---------------------------------------------------------------- 단계 하나 — 간단히 (03d)
  function renderSimple(n) {
    // 198:2608: the one step's form on the left; what it takes and makes, a one-node run and 단계 더하기 on the right.
    const page = el("div", "wf-simple");
    const form = el("div", "wf-simple-form g-right");
    const head = el("div", "g-right-head");
    const lead = el("span", "g-icon");
    lead.append(icon(NODES[n.type].icon));
    const t = el("div", "");
    t.append(el("strong", "", nodeTitle(n)), el("small", "dim", `${NODES[n.type].label} · 단계 하나`));
    head.append(lead, t);
    form.append(head);
    const title = input(n.title, { maxLength: 80 });
    title.addEventListener("input", () => { n.title = title.value; touch(); });
    form.append(field("이름", title));
    aiFields(form, n);
    const side = el("aside", "g-right wf-simple-side");
    const c = n.config || {};
    side.append(el("h3", "", "주고받는 것"), kv([["필요한 것", c.needs || "앞 단계 결과"], ["만드는 것", c.makes || "정하지 않음"], ["실패하면", Number(c.retries) ? `${c.retries}번 다시 → 멈추고 보고` : "멈추고 보고"]]), el("i", "wf-rule"));
    // An owned workflow runs only through its agent (@); the server refuses a manual run.
    const owned = ownerOf(ed.rec);
    if (!owned) side.append(onlyRun(n), el("i", "wf-rule"));
    const run = button("실행", "primary", "play", async () => {
      if (ed.dirty && !(await saveGraph())) return;
      await runFromEditor();
    });
    const more = el("div", "wf-side-block");
    more.append(el("h3", "", "단계 더하기"), el("p", "wf-text", "단계를 더하면 그래프 편집기로 바뀌어요. 지금 내용은 첫 단계로 그대로 남아요."),
      button("단계 추가 — 그래프로 바뀌어요", "wf-full center", null, () => { ed.graphMode = true; ed.menu = true; render(false); }));
    const acts = el("div", "crew-actions wf-run-acts");
    if (!owned) acts.append(run);
    acts.append(button("워크플로우 설정", "ghost", "gear-six", () => openSettings(ed.rec, null, true)));
    side.append(more, el("i", "wf-rule"), acts);
    page.append(form, side);
    $("#table").replaceChildren(page);
  }


  // ---------------------------------------------------------------- 구조 검토 (03c)
  // Rule checks run in code; AI suggestions come from the assist runner as JSON and apply with one click.
  function reviewPanel() {
    const box = el("aside", "g-right review");
    const head = el("div", "g-right-head");
    const checks = ruleChecks(ed.rec.crew, ed.graph);
    const t = el("div", "");
    t.append(el("strong", "", "구조 검토"), el("small", "dim", `규칙 검사 ${checks.filter((c) => c.ok).length}/${checks.length} 통과${ed.suggestions ? ` · 제안 ${ed.suggestions.length}개` : ""}`));
    const close = el("button", "icon-btn plain");
    close.type = "button";
    close.setAttribute("aria-label", "구조 검토 닫기");
    close.append(icon("x"));
    close.addEventListener("click", () => { ed.review = false; render(false); });
    head.append(t, close);
    const h4 = el("h4", "", "규칙 검사");
    h4.append(el("small", "", `코드 · ${checks.filter((c) => c.ok).length}개 통과`));
    box.append(head, h4);
    // Passed checks read as small chips (the detail on hover); the ones that failed keep their sentence.
    const list = el("div", "wf-checks");
    for (const c of checks) {
      const r = el("div", `wf-check${c.ok ? "" : " bad"}`);
      r.title = c.detail;
      r.append(icon(c.ok ? "check" : "warning-circle"), el("strong", "", c.label));
      if (!c.ok) r.append(el("small", "", c.detail));
      list.append(r);
    }
    box.append(list, reviewAI());
    box.append(el("p", "meta", "[적용]을 누르면 그래프가 바로 바뀌고, 나머지 제안은 그대로 남아요. 실행 기록이 쌓이면 자주 다시 도는 노드도 짚어요."));
    return box;
  }
  // AI 제안 (ai-chat.js): the same button → bubble → purple list as the expert window; 적용 changes the graph at once.
  const REVIEW_TAG = { tokens: "토큰 줄이기", order: "순서", stop: "멈추는 조건", safety: "안전" };
  function reviewAI() {
    ed.reviewAI ||= {};
    const st = ed.reviewAI;
    const asProp = (s) => ({ s, label: REVIEW_TAG[s.tag] || s.tag || "제안", from: null, to: `${s.title}\n${s.body}`, apply: () => {
      if (!s.action) { notice("이 제안은 직접 고쳐 주세요. 노드를 눌러 바꿀 수 있어요."); return false; }
      if (!canApply(s.action) || !fresh(s)) { notice("그 노드나 선이 바뀌어 다시 받아야 해요"); return false; }
      applyAction(s.action);
      ed.suggestions = (ed.suggestions || []).filter((x) => x !== s);
      render(false);
      notice("적용했어요. 저장하면 반영돼요.");
    } });
    // a node deleted since keeps its suggestion out of the list (deleteNode filters ed.suggestions)
    st.props = (st.props || []).filter((p) => p.applied || (ed.suggestions || []).includes(p.s));
    return window.ArkismAIChat.button({ state: st, label: "AI 제안", title: "AI 제안", sub: "토큰을 줄일 칸, 순서 문제, 멈추는 조건을 짚어 줘요",
      greeting: "이 그래프를 읽고 고칠 곳을 4개까지 짚어 드려요. 특히 볼 곳이 있으면 말해 주세요. 실행할 때는 검토하지 않아요.",
      placeholder: "예: 비용이 많이 드는 곳을 먼저 봐 줘", quick: ["전체를 검토해 줘", "토큰 줄일 곳", "멈추는 조건 확인"],
      onDrop: (p) => { ed.suggestions = (ed.suggestions || []).filter((x) => x !== p.s); render(false); },
      ask: async (want) => {
        const list = await aiReview(want);
        return { say: list.length ? `제안 ${list.length}개를 찾았어요. 버튼 아래 목록에서 보고 적용하세요.` : "고칠 곳을 찾지 못했어요.", props: list.map(asProp) };
      } });
  }
  async function aiReview(want) {
    const g = { nodes: ed.graph.nodes.map(({ id, type, title, config }) => ({ id, type, title, config })), edges: ed.graph.edges };
    // The graph as the AI reads it, taken from the live node objects (so a node deleted and re-added under the
    // same id while it thinks counts as a different node): edits made meanwhile make its suggestions stale.
    const asked = { nodes: Object.fromEntries(ed.graph.nodes.map((n) => [n.id, nodePrint(n)])), edges: structuredClone(ed.graph.edges) };
    const prompt = `Review the workflow graph below and give at most 4 improvements. Principles: steps an if can decide become code nodes (0 tokens); only steps that need judgement use AI. Cheap checks first. Back edges only where truly needed, with a count limit. Parallel branches must not need each other. A human check before any irreversible action.
The owner asks: ${want}
Shape: {"suggestions":[{"title":"short Korean title","body":"two plain Korean sentences","tag":"tokens|order|stop|safety","action":null or {"type":"set_model","node":"id","model":"..."} or {"type":"set_edge_max","from":"id","to":"id","max":2} or {"type":"set_access","node":"id","access":"read|write"} or {"type":"insert_before","node":"id","new":{"type":"code|ai_once|approval","title":"...","config":{}}}}]}
Workflow: ${ed.rec.crew.name} ($${ed.rec.crew.handle}) — ${ed.rec.crew.role || ""}
Default worker model: ${ed.rec.crew.model || "none"}
Models allowed for set_model (only these): ${[...new Set(models?.enabledModels() || [])].join(", ") || "none — do not suggest set_model"}
Graph: ${JSON.stringify(g).slice(0, 14000)}`;
    const a = await kit.assist("review", prompt, { purpose: "workflow" });
    if (a.status !== "done") throw new Error(kit.assistError(a));
    const j = kit.parseJSON(a.output);
    const list = Array.isArray(j) ? j : j?.suggestions;
    if (!Array.isArray(list)) throw new Error("AI 답을 읽지 못했어요. 다시 해 주세요.");
    ed.suggestions = list.filter((s) => s?.title).slice(0, 4).map((s) => {
      const action = s.action || null;
      // What the targets looked like when the AI read them: a suggestion only applies to that same state.
      const seen = stateOf(action, asked);
      return { title: String(s.title).slice(0, 80), body: String(s.body || "").slice(0, 400), tag: String(s.tag || "").slice(0, 20), action, seen };
    });
    render(false); // the flagged nodes on the canvas and the counts in the head
    return ed.suggestions;
  }
  const nodeIn = (id) => ed.graph.nodes.find((n) => n.id === id);
  const targetsOf = (a) => (a ? [a.node, a.from, a.to].filter(Boolean) : []);
  // Each node object gets its own number on first sight, so a deleted and re-added node with the same id,
  // title and settings still differs from the one the AI read.
  const nodeTokens = new WeakMap();
  let nodeTokenSeq = 0;
  const nodePrint = (n) => {
    if (!nodeTokens.has(n)) nodeTokens.set(n, ++nodeTokenSeq);
    return JSON.stringify([nodeTokens.get(n), n.type, n.title, n.config]);
  };
  // The edges a suggestion depends on: between two nodes for a back-edge count, into the node for an insert.
  const edgePrint = (a, edges) => JSON.stringify(edges.filter((e) => (a.type === "set_edge_max" ? e.from === a.from && e.to === a.to : !isBack(e) && e.to === a.node))
    .map((e) => [e.from, e.to, e.port || "", e.max || 0]));
  // A suggestion's targets and edges, from the live graph or from the snapshot taken when the AI was asked.
  // Node prints and edge prints live apart, so a node whose id happens to be "edges" cannot hide either.
  function stateOf(a, snap) {
    const nodes = Object.fromEntries(targetsOf(a).map((id) => {
      if (snap) return [id, snap.nodes[id] ?? null];
      const n = nodeIn(id);
      return [id, n ? nodePrint(n) : null];
    }));
    if (a?.type !== "set_edge_max" && a?.type !== "insert_before") return { nodes, edges: "" };
    const edgeList = snap ? snap.edges : ed.graph.edges;
    return { nodes, edges: edgePrint(a, edgeList) };
  }
  // Still the graph the AI looked at: every target exists and is unchanged since the review.
  const fresh = (s) => {
    const now = stateOf(s.action);
    return Object.values(now.nodes).every((v) => v !== null) && Object.entries(now.nodes).every(([k, v]) => s.seen?.nodes?.[k] === v) && now.edges === s.seen?.edges;
  };
  function canApply(a) {
    if (a.type === "set_model" || a.type === "set_access") return !!nodeIn(a.node);
    if (a.type === "set_edge_max") return ed.graph.edges.some((e) => e.from === a.from && e.to === a.to && isBack(e)) && a.max >= 1 && a.max <= 10;
    if (a.type === "insert_before") return !!nodeIn(a.node) && ["code", "ai_once", "approval"].includes(a.new?.type);
    return false;
  }
  function applyAction(a) {
    const g = ed.graph;
    if (a.type === "set_model") nodeIn(a.node).config.model = String(a.model || "").slice(0, 80);
    if (a.type === "set_access") nodeIn(a.node).config.access = a.access === "write" ? "write" : "read";
    if (a.type === "set_edge_max") g.edges.filter((e) => e.from === a.from && e.to === a.to && isBack(e)).forEach((e) => { e.max = a.max; });
    if (a.type === "insert_before") {
      const target = nodeIn(a.node);
      const id = uid(a.new.type === "ai_once" ? "once" : a.new.type);
      const config = Object.fromEntries(Object.entries(a.new.config || {}).map(([k, v]) => [k, String(v)]));
      if (a.new.type === "code") { config.lang ||= "go"; config.code ||= "package main\n\n// 입력(stdin JSON)을 걸러 {\"ok\": true/false, ...}를 출력해요.\nfunc main() {}\n"; }
      g.nodes.push({ id, type: a.new.type, title: String(a.new.title || NODES[a.new.type].label).slice(0, 80), x: target.x - 40, y: target.y + 220, config });
      g.edges.filter((e) => e.to === target.id && !isBack(e)).forEach((e) => { e.to = id; });
      g.edges.push({ from: id, to: target.id });
    }
    touch();
  }

  // ---------------------------------------------------------------- run view (03b)
  function stopPolling() { if (ed.timer) { clearTimeout(ed.timer); ed.timer = null; } }
  async function pollRun() {
    stopPolling();
    const key = ed.run ? "run" : "test"; // the run view, or the test run drawn on the editor
    if (!ed[key]?.id || state.view !== "graph") return;
    try {
      const r = await request(`/v1/crew/runs/${encodeURIComponent(ed[key].id)}?${query()}`);
      if (!ed[key] || r.id !== ed[key].id) return;
      ed[key] = r;
      if (state.view === "graph" && !nodeDlg) render(false);
    } catch (_) { /* a short outage: try again on the next tick */ }
    const st = ed[key]?.crew_run?.status;
    if (st === "running" || st === "waiting") ed.timer = setTimeout(pollRun, 2500);
  }
  // 이번 실행 (198:2436 오른쪽): when and how it goes, the steps, why it went back, the workers, the budget.
  const RUN_STATE = { running: "실행기가 진행 중", waiting: "확인을 기다려요", succeeded: "끝났어요", failed: "멈췄어요", stopped: "멈췄어요" };
  const STEP_CLS = { running: "live", waiting: "live", failed: "fail", done: "", pending: "quiet", skipped: "quiet" };
  const hhmm = (t) => (t ? new Intl.DateTimeFormat(globalThis.ArkismI18n?.locale() || "ko-KR", { hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(t)) : "");
  const nodeDur = (st) => (st?.started_at && st.finished_at ? spanText(new Date(st.finished_at) - new Date(st.started_at)) : "");
  function stepState(n, st) {
    const port = st.port ? PORTS[n.type]?.find(([k]) => k === st.port)?.[1] || st.port : "";
    if (st.status === "done") return [port && port !== "통과" && port !== "승인" ? `${port}` : NODE_LABEL.done, nodeDur(st)].filter(Boolean).join(" · ");
    if (st.status === "running") return "실행 중";
    if (st.status === "waiting") return n.type === "approval" ? "확인 대기" : "기다리는 중";
    if (st.status === "failed") return st.error ? `실패 · ${st.error.slice(0, 24)}` : "실패";
    return NODE_LABEL[st.status] || st.status;
  }
  // The steps the run reached, in graph order (a node that ran twice shows once, with how often).
  const runSteps = (cr) => levels(ed.graph).flat().filter((n) => cr.nodes?.[n.id]);
  function failedNode(cr) {
    const id = Object.entries(cr.nodes || {}).find(([, st]) => st.status === "failed")?.[0];
    return id ? nodeIn(id) : null;
  }
  // 확인 대기 · 실패: the amber card on top, with the answer buttons or the reason.
  function runAttn(run, after) {
    const cr = run.crew_run || {};
    if (cr.status === "waiting" && cr.waiting?.kind === "approval") {
      const node = nodeIn(cr.waiting.node_id);
      const a = el("div", "wf-attn crew-approval");
      const t = el("div", "");
      t.append(el("strong", "", node ? `${nodeTitle(node)} · 사람 확인 대기` : "사람 확인 대기"), el("p", "", cr.waiting.question || node?.config?.question || "다음 단계로 갈까요?"));
      const row = el("div", "wf-attn-actions crew-approval-actions");
      row.append(button("거절", "", null, async () => { await decide(run, false); after(); }), button("승인", "primary", null, async () => { await decide(run, true); after(); }));
      a.append(t, row);
      return a;
    }
    if (cr.status === "failed") {
      const n = failedNode(cr);
      const a = el("div", "wf-attn small");
      const t = el("div", "");
      t.append(el("strong", "", n ? `${nodeTitle(n)} 단계가 실패했어요` : "실행이 멈췄어요"), el("p", "", readable(cr.summary) || cr.nodes?.[n?.id]?.error || "이유가 남지 않았어요."));
      a.append(t);
      return a;
    }
    return null;
  }
  function runPanel() {
    const box = el("aside", "g-right run");
    const run = ed.run;
    const cr = run.crew_run || {};
    const req = runRequest(cr);
    const head = el("div", "wf-run-head");
    head.append(el("small", "", "이번 실행"), el("strong", "", `${hhmm(run.created_at)} 시작 · ${RUN_STATE[cr.status] || cr.status || ""}`));
    if (req) head.append(el("small", "", `“${req.slice(0, 60)}”`));
    box.append(head);
    const again = rerunOf();
    if (again) box.append(el("h4", "", "다시 실행"), kv([["시작한 곳", again.from || "—"], ["다시 쓰는 결과", `${again.reused}단계 (앞선 실행)`], ["설정", "실패했을 때와 같아요"]]),
      el("h4", "", "끝나면"), el("p", "meta", "새 결과가 실행 기록에 쌓이고, 실패 기록도 함께 남아요."));
    const attn = ed.runList ? null : runAttn(run, pollRun); // the list view shows it above the table
    if (attn) box.append(attn);
    const steps = runSteps(cr);
    const work = steps.filter(WORK);
    const h = el("h4", "", "단계");
    h.append(el("small", "", `${work.filter((n) => cr.nodes[n.id].status === "done").length} / ${work.length}`));
    box.append(h);
    const list = el("div", "wf-run-steps");
    for (const n of steps) {
      const st = cr.nodes[n.id];
      const r = el("div", `wf-run-step st-${st.status}`);
      r.append(el("span", "", `${nodeTitle(n)}${st.runs > 1 ? ` (${st.runs}회째)` : ""}`), el("small", STEP_CLS[st.status] || "", stepState(n, st)));
      r.title = st.error || "";
      list.append(r);
    }
    box.append(list);
    const loops = Object.entries(cr.loops || {}).filter(([, v]) => v > 0);
    for (const [k, v] of loops) {
      const [ft, port] = k.split(":");
      const [f, to] = ft.split(">");
      const e = ed.graph.edges.find((x) => x.from === f && x.to === to && (x.port || "") === (port || ""));
      const card = el("div", "wf-why");
      const t = el("strong", "", "고른 길과 이유");
      t.append(el("small", "", `되돌아감 ${v}${e ? `/${e.max}` : "번"}`));
      const portText = PORTS[nodeIn(f)?.type]?.find(([p]) => p === port)?.[1] || port;
      card.append(t, el("p", "", `${nodeTitle(nodeIn(f) || { type: f })}${portText ? ` (${portText})` : ""} → ${nodeTitle(nodeIn(to) || { type: to })}로 ${v}번 되돌아갔어요. 성공한 노드 결과는 그대로 두고 그 칸부터 다시 돌아요.`));
      box.append(card);
    }
    for (const n of steps) {
      const out = cr.nodes?.[n.id]?.output;
      if (n.type !== "ai" || !out) continue;
      try {
        const v = JSON.parse(out);
        if (Array.isArray(v?.workers) && v.workers.length) {
          const wh = el("h4", "", "협업 워커");
          wh.append(el("small", "", `${nodeTitle(n)} · ${v.workers.length}명`));
          box.append(wh);
          const wl = el("div", "wf-run-steps");
          for (const w of v.workers) {
            const r = el("div", "wf-run-step");
            r.title = w.task || "";
            r.append(el("span", "", w.worker || "워커"), el("small", w.ok ? "" : "fail", w.ok ? "완료" : "실패"));
            wl.append(r);
          }
          box.append(wl);
        }
      } catch { /* plain text output */ }
    }
    // 예산: time against limits.max_minutes and node runs against limits.max_steps (tokens are not counted per run).
    const lim = ed.rec.crew.limits || {};
    const lastEnd = Math.max(0, ...Object.values(cr.nodes || {}).map((s) => (s.finished_at ? new Date(s.finished_at).getTime() : 0)));
    const live = cr.status === "running" || cr.status === "waiting";
    const spent = run.created_at ? (live ? Date.now() : lastEnd || Date.now()) - new Date(run.created_at).getTime() : 0;
    const used = Object.values(cr.nodes || {}).reduce((s, x) => s + (x.status === "pending" ? 0 : x.runs || 1), 0);
    const cards = el("div", "wf-budget");
    const card = (label, value, of, pct) => {
      const c = el("div", "wf-budget-card");
      const v = el("strong", "", value);
      if (of) v.append(el("small", "", ` / ${of}`));
      c.append(el("small", "", label), v);
      if (pct !== null) c.append(el("small", "", `${pct}% 사용`));
      cards.append(c);
    };
    card("시간", spanText(spent), lim.max_minutes ? `${lim.max_minutes}분` : "", lim.max_minutes ? Math.min(100, Math.round(spent / 600 / lim.max_minutes)) : null);
    card("단계", String(used), lim.max_steps ? `${lim.max_steps}번` : "", lim.max_steps ? Math.min(100, Math.round((used / lim.max_steps) * 100)) : null);
    box.append(el("h4", "", "예산"), cards);
    if (cr.summary && cr.status !== "failed") { box.append(el("h4", "", "결과")); const pre = el("pre", "try-answer"); pre.textContent = cr.summary; box.append(pre); }
    const acts = el("div", "crew-actions wf-run-acts");
    if (cr.status === "running" || cr.status === "waiting") acts.append(button("멈추기", "", "stop-circle", async () => { await stopRun(ed.run); void pollRun(); }));
    if (cr.status === "failed") acts.append(button("실패한 곳부터 다시", "", null, retryRun));
    acts.append(button("편집으로", "ghost", null, () => { stopPolling(); ed.run = null; render(false); }));
    box.append(acts);
    return box;
  }
  // 실패한 곳부터 다시 (198:6994): the done nodes keep their results, the rest runs again.
  // 여기부터 다시: from and every step after it run again; the past run stays as it was.
  async function retryRun(from) {
    let cr = ed.run.crew_run || {};
    if (typeof from === "string") {
      const again = new Set([from]);
      for (let grew = true; grew; ) { grew = false; for (const e of fwd(ed.rec.crew.graph)) if (again.has(e.from) && !again.has(e.to)) { again.add(e.to); grew = true; } }
      cr = { ...cr, loops: {}, nodes: Object.fromEntries(Object.entries(cr.nodes || {}).filter(([id]) => !again.has(id))) };
    }
    const r = await startRun(ed.rec, runRequest(cr), null, cr);
    if (!r) return;
    // 실패한 곳부터 재실행 (198:6994): the list of steps, where it started again and how many results it reuses.
    const work = levels(ed.graph).flat().filter(WORK);
    const at = work.findIndex((n) => cr.nodes?.[n.id]?.status !== "done");
    ed.rerun = { id: r.id, from: at < 0 ? "" : `${at + 1} · ${nodeTitle(work[at])}`, reused: work.filter((n) => cr.nodes?.[n.id]?.status === "done").length };
    ed.run = r;
    ed.runList = true;
    render(false);
    void pollRun();
  }
  const rerunOf = () => (ed.rerun && ed.run && ed.rerun.id === ed.run.id ? ed.rerun : null);
  // 목록으로 보기 (198:5360 · 198:6908): the same run as a table of steps instead of the graph.
  function runList() {
    const run = ed.run;
    const cr = run.crew_run || {};
    const page = el("div", "wf-runlist");
    const attn = runAttn(run, pollRun);
    if (attn) {
      if (cr.status === "failed") attn.append(button("실패한 곳부터 다시", "primary", null, retryRun));
      page.append(attn);
    }
    const again = rerunOf();
    page.append(el("h4", "", again ? "현재 단계" : "실행 단계"));
    const table = el("div", "wf-steptable");
    const headRow = el("div", "wf-steprow head");
    headRow.append(el("span", "", "단계"), el("span", "", "결과"), el("span", "", "시간"));
    table.append(headRow);
    runSteps(cr).filter(WORK).forEach((n, i, all) => {
      const st = cr.nodes[n.id];
      const row = el("div", `wf-steprow st-${st.status}`);
      const name = el("span", "");
      name.append(el("strong", "", `${i + 1} · ${nodeTitle(n)}${st.runs > 1 ? ` · ${st.runs}회째` : ""}`));
      if (st.error) name.append(el("small", "", st.error));
      else if (again && st.status === "done" && !st.started_at) name.append(el("small", "", "앞선 실행 결과를 사용해요."));
      else if (again && st.status === "running") name.append(el("small", "", "같은 설정으로 다시 실행하고 있어요."));
      else if (again && st.status === "pending" && i) name.append(el("small", "", `${nodeTitle(all[i - 1])}가 끝나면 이어서 실행해요.`));
      else if (st.status === "pending") name.append(el("small", "", "앞 단계가 끝나면 실행해요."));
      if (st.status === "done" && ["succeeded", "failed", "stopped"].includes(cr.status)) name.append(button("여기부터 다시", "ghost", null, () => retryRun(n.id)));
      row.append(name, el("span", `wf-badge small ${{ failed: "fail", waiting: "wait", running: "live", pending: "line", skipped: "line" }[st.status] || ""}`, st.status === "pending" ? "실행 전" : stepState(n, st).split(" · ")[0]), el("span", "mono", nodeDur(st) || "—"));
      table.append(row);
    });
    page.append(table);
    const loops = Object.values(cr.loops || {}).reduce((a, b) => a + b, 0);
    if (loops) page.append(el("p", "meta", `되돌아가는 선을 ${loops}번 따라가 앞 단계를 다시 돌았어요.`));
    if (again) page.append(el("p", "meta", "새 결과가 도착하면 실행 기록에 표시돼요."));
    return page;
  }

  // 실행할 때 맡길 일: a small window instead of window.prompt, which the desktop webview may block.
  function askTask(rec) {
    return new Promise((resolve) => {
      const dlg = el("dialog", "crew-dialog wf-dialog ask-dialog");
      const form = el("form");
      form.method = "dialog";
      const q = input("", { maxLength: 4000, placeholder: `$${rec.crew.handle}에게 맡길 일 (비워도 돼요)` });
      const body = el("div", "dialog-body");
      body.append(el("h2", "", `${rec.crew.name} 실행`), q);
      const foot = el("div", "dialog-foot");
      const cancel = button("취소", "ghost");
      cancel.type = "submit";
      cancel.value = "cancel";
      const ok = button("실행", "primary", "play");
      ok.type = "submit";
      ok.value = "run";
      foot.append(cancel, ok);
      form.append(body, foot);
      let answer = null;
      form.addEventListener("submit", (e) => { if (e.submitter?.value === "run") answer = q.value.trim(); });
      dlg.append(form);
      dlg.addEventListener("close", () => { dlg.remove(); resolve(answer); });
      document.body.append(dlg);
      dlg.showModal();
      q.focus();
    });
  }
  async function runFromEditor() {
    const text = await askTask(ed.rec);
    if (text === null) return;
    const r = await startRun(ed.rec, text);
    if (r) { ed.test = r; render(false); void pollRun(); }
  }

  function graphHead() {
    const rec = ed.rec;
    if (!rec) return [];
    if (ed.run) {
      const view = segment([["graph", "그래프"], ["list", "목록"]], ed.runList ? "list" : "graph", (k) => { ed.runList = k === "list"; render(false); });
      view.classList.add("wf-view");
      headTitle(button("돌아가기", "ghost", "arrow-left", leaveGraph));
      return [view];
    }
    const save2 = button("저장", "primary g-save", null, saveGraph);
    save2.disabled = !ed.dirty;
    const review = button(`구조 검토${ed.suggestions?.length ? ` · ${ed.suggestions.length}` : ""}`, ed.review ? "active" : "", "sparkle", () => { ed.review = !ed.review; render(false); });
    const testing = ["running", "waiting"].includes(ed.test?.crew_run?.status);
    const trial = button(testing ? "실행 중…" : ed.test ? "다시 실행" : "실행", "g-trial", "play", runFromEditor);
    trial.disabled = ed.dirty || testing;
    trial.title = ed.dirty ? "저장한 뒤에 실행할 수 있어요" : "저장된 워크플로우로 지금 한 번 돌려요";
    const settings = button("설정", "ghost", "gear-six", () => openSettings(ed.rec, null, true));
    settings.title = "이름·$이름·워커 기본 모델·멈추는 조건·일정·웹훅·배경 사진";
    // An owned workflow runs only through its agent (@); the server refuses a manual run.
    headTitle(button(ed.fromPopup ? "창으로 돌아가기" : "돌아가기", "ghost", "arrow-left", leaveGraph));
    return [settings, review, ...(ownerOf(rec) ? [] : [trial]), save2];
  }
  // 제목 줄: ← 돌아가기 at the far left, then the workflow's name with what this screen is on a smaller line under it.
  // page-back is the class the app clears on every screen change (head-back would be cleared again right after head()).
  function headTitle(back) {
    const line = $(".page-head .title-line");
    if (!line) return;
    back.classList.add("page-back", "g-head-back");
    line.prepend(back);
    if (!rerunOf()) $("#title").replaceChildren(el("span", "", ed.rec.crew.name), el("small", "g-title-sub", ed.run ? "실행" : "워크플로우 편집"));
  }
  // Space held = hand tool on the graph, as in Figma.
  let spaceDown = false;
  const typing = () => ["INPUT", "TEXTAREA", "SELECT"].includes(document.activeElement?.tagName) || document.activeElement?.isContentEditable;
  const setSpace = (on) => { spaceDown = on; document.querySelector(".g-stage")?.classList.toggle("space", on); };
  document.addEventListener("keydown", (e) => {
    if (e.code !== "Space" || state.view !== "graph" || typing()) return;
    e.preventDefault();
    if (!spaceDown) setSpace(true);
  });
  document.addEventListener("keyup", (e) => { if (e.code === "Space") setSpace(false); });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape" && ed.linking && state.view === "graph") { ed.linking = null; render(false); } });
  window.addEventListener("blur", () => setSpace(false));
  document.addEventListener("keydown", (e) => {
    if (state.view !== "graph" || ed.run || (!ed.sel && ed.selEdge === null)) return;
    if (["INPUT", "TEXTAREA", "SELECT"].includes(document.activeElement?.tagName)) return;
    if (e.key === "Delete" || e.key === "Backspace") {
      e.preventDefault();
      if (ed.sel) removeNode(ed.sel);
      else { ed.graph.edges.splice(ed.selEdge, 1); ed.selEdge = null; touch(); render(false); }
    }
  });

  async function explain(prompt) {
    const a = await kit.assist("explain", prompt, { purpose: "skill" });
    if (a.status !== "done") throw new Error(kit.assistError(a));
    return a.output.trim();
  }
  // ---------------------------------------------------------------- 설정 › 워크플로우 › 페르소나
  // Saved personas an AI node picks under 페르소나. One opens at a time and saves on its own.
  let personaOpen = null;
  let personaDraft = null;
  const personaAI = new WeakMap(); // the AI talk per open draft (ai-chat.js state), gone with the draft
  const personaUses = (id) => workflows().filter((r) => (r.crew.graph?.nodes || []).some((n) => n.config?.persona === id));
  function personaTab({ group }) {
    const g = group("페르소나", "AI 노드의 ‘페르소나’에서 골라 써요. 지시는 그 노드가 일할 때 역할·말투로 들어가요.");
    const list = el("div", "pg-card");
    const rows = [...personas(), ...(personaOpen === "new" ? [{ id: "new", crew: personaDraft }] : [])];
    if (!rows.length) list.append(el("p", "pg-hint", "아직 저장한 페르소나가 없어요."));
    for (const r of rows) {
      const isOpen = personaOpen === r.id;
      const row = el("div", "cat");
      const head = el("button", "cat-head");
      head.type = "button";
      head.setAttribute("aria-expanded", String(isOpen));
      const used = r.id === "new" ? 0 : personaUses(r.id).length;
      head.append(icon("robot", "icon dim"), el("strong", "", r.crew.name || "새 페르소나"), el("span", "cat-rules", r.crew.role || ""),
        el("span", "tag", used ? `노드 ${used}곳에서 씀` : "안 씀"), icon(isOpen ? "caret-down" : "caret-right"));
      head.addEventListener("click", () => {
        personaOpen = isOpen ? null : r.id;
        personaDraft = isOpen ? null : structuredClone(r.crew);
        render(false);
      });
      row.append(head);
      if (isOpen) row.append(personaEditor(r.id === "new" ? null : r));
      list.append(row);
    }
    const add = button("새 페르소나", "dashed", "plus", () => {
      personaOpen = "new";
      personaDraft = { type: "persona", name: "", role: "", instructions: "" };
      render(false);
    });
    add.disabled = personaOpen === "new";
    g.append(list, add);
    return [g];
  }
  function personaEditor(rec) {
    const d = personaDraft;
    const box = el("div", "cat-body");
    const name = input(d.name, { maxLength: 60, placeholder: "꼼꼼한 검토자" });
    const role = input(d.role, { maxLength: 200, placeholder: "PR을 보안·데이터 손실 위주로 봐요" });
    const text = input(d.instructions, { tag: "textarea", rows: 8, maxLength: 20000, placeholder: "너는 ○○다. …한다." });
    name.addEventListener("input", () => { d.name = name.value; });
    role.addEventListener("input", () => { d.role = role.value; });
    text.addEventListener("input", () => { d.instructions = text.value; });
    if (!personaAI.has(d)) personaAI.set(d, { box: {}, text: {} });
    const talk = personaAI.get(d);
    const put = (node, key, v) => { d[key] = v; if (node.isConnected) node.value = v; };
    // AI로 마무리 (ai-chat.js): Opus 5.5 (medium) finishes what is written so far as proposals; nothing is saved until 저장.
    const ai = window.ArkismAIChat.button({ state: talk.box, label: "AI로 마무리", title: "AI로 마무리", sub: "쓴 데까지 보고 빈 칸과 지시를 마무리해요",
      greeting: "어떻게 마무리할까요? '그대로 마무리해 줘'를 누르면 쓴 데까지 보고 빈 칸을 채우고 지시를 번호 목록으로 정리해요.",
      placeholder: "예: 말투는 정중하게, 보안 문제는 꼭 먼저 말하게", quick: ["그대로 마무리해 줘", "더 짧게", "하지 말 것을 넣어 줘"],
      ask: async (want) => {
        if (!(d.name + d.role + d.instructions).trim()) throw new Error(tr("이름이나 지시를 조금이라도 먼저 써 주세요."));
        const prompt = `Finish this persona draft. Keep the author's intent and the sentences already written, fill the empty fields, and turn the instructions into a short numbered list in Korean, opening with who the agent is ("You are …" in Korean), that makes the role, tone, must-dos and must-nots clear. Use only the keys name, role, instructions.\nThe author asks: ${want}\n\nName: ${d.name || "(empty)"}\nOne-line description: ${d.role || "(empty)"}\nInstructions:\n${d.instructions || "(empty)"}`;
        const a = await kit.assist("fill", prompt, { model: "opus 5.5", persona: { crew: { instructions: "You write personas for AI agents, in short, clear Korean." } } });
        if (a.status !== "done") throw new Error(kit.assistError(a));
        const got = kit.parseJSON(a.output || "") || {};
        if (!got.instructions && !got.name) throw new Error(tr("AI 답을 읽지 못했어요. 다시 눌러 주세요."));
        const prop = (node, key, label, max) => {
          const to = got[key] ? String(got[key]).slice(0, max) : "", from = d[key];
          return to && to !== from ? { label, from, to, apply: () => put(node, key, to), undo: () => put(node, key, from) } : null;
        };
        const props = [prop(name, "name", "이름", 60), prop(role, "role", "한 줄 설명", 200), prop(text, "instructions", "지시", 20000)].filter(Boolean);
        return { say: props.length ? "마무리한 곳을 아래에 넣었어요. 보고 적용한 뒤 저장해 주세요." : "", props };
      } });
    const textAI = window.ArkismAIChat.field(text, { state: talk.text, title: "지시 다듬기", preset: "You edit the instructions of a persona for AI agents (who it is, tone, must-dos, must-nots). Write short, clear Korean, as a numbered list. No keys or tokens.",
      context: () => JSON.stringify({ name: d.name, role: d.role }) });
    box.append(ai, field("이름", name), field("한 줄 설명", role, "목록과 노드 선택에서 보여요"), field("지시", textAI, "역할·말투·꼭 지킬 것을 써요. 키나 토큰은 넣지 마세요."));
    const bar = el("div", "crew-actions");
    const ok = button("저장", "primary", null, async () => {
      if (!d.name.trim()) { window.ArkismNeed(name, "이름을 적어 주세요."); return; }
      if (!d.instructions.trim()) { window.ArkismNeed(text, "지시를 적어 주세요."); return; }
      ok.disabled = true;
      try {
        await save(rec, { ...(rec?.crew || {}), type: "persona", name: d.name.trim(), role: d.role.trim(), instructions: d.instructions });
        personaOpen = personaDraft = null;
        notice("페르소나를 저장했어요.");
        await reload();
      } catch (e) { notice(errorText(e)); ok.disabled = false; }
    });
    bar.append(ok, button("취소", "ghost", null, () => { personaOpen = personaDraft = null; render(false); }));
    if (rec) {
      bar.append(button("지우기", "ghost danger", "trash", async () => {
        const users = personaUses(rec.id);
        // A node pointing at a deleted persona would lose its instructions silently.
        if (users.length) { notice(`${users.map((u) => u.crew.name).join(", ")}에서 쓰고 있어요. 노드에서 먼저 바꿔 주세요.`); return; }
        if (!await ask(`'${rec.crew.name}' 페르소나를 지울까요? 되돌릴 수 없어요.`)) return;
        try {
          await request(`/v1/crew/${encodeURIComponent(rec.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope, expected_revision: rec.revision }) });
          personaOpen = personaDraft = null;
          notice("지웠어요.");
          await reload();
        } catch (e) { notice(errorText(e)); }
      }));
    }
    bar.append(ai.dock); // AI로 마무리 at the right end of 저장·취소; its purple list stays above the fields
    box.append(bar);
    return box;
  }

  // ---------------------------------------------------------------- 에이전트 (Figma 208:17763 · 310:19277)
  // A work agent (crew type "agent", called @이름; workflows keep $이름): who it is, its instructions, model and how it
  // thinks, the skills·MCP·memory it may use, where it can be called and its monthly budget. Teams list their members by
  // agent id. Status comes from /v1/crew/agents/status, spend from /v1/crew/usage (estimated list prices).
  const MEMORY_KINDS = [["procedural", "작업 규칙"], ["decision", "결정"], ["lesson", "교훈"], ["user", "선호"]];
  const MEMORY_KIND_HINT = { "작업 규칙": "이렇게 일하라고 정해 둔 규칙", "결정": "이미 정한 결정", "교훈": "실수에서 배운 것", "선호": "사용자가 좋아하는 방식" };
  const CALL_PLACES = [["claude_code", "Claude Code"], ["codex", "Codex"], ["zcode", "ZCode"], ["discord", "디스코드"], ["app", "arkism 앱"]];
  const DEPTH = [["low", "빠르게"], ["medium", "보통"], ["high", "깊게"]];
  const MODEL_MODES = [["auto", "Clef 자동 선택"], ["fixed", "모델 고정"]];
  const FORMATS = [["text", "자유 텍스트"], ["markdown", "마크다운"], ["json", "JSON"]];
  const FILE_EDITS = [["ask", "확인 받고 진행"], ["auto", "알아서 고침"], ["never", "고치지 않음"]];
  const agents = () => items().filter((r) => r.crew.type === "agent");
  const teams = () => items().filter((r) => r.crew.type === "team");
  const teamsOf = (id) => teams().filter((t) => (t.crew.members || []).some((m) => m.agent_id === id));
  const blankAgent = () => ({ type: "agent", name: "", handle: "", role: "", instructions: "", model_mode: "fixed", model: "", fallback_model: "", effort: "medium",
    skills: [], tools: [], agents: ["claude_code", "codex", "zcode", "app"], memory: { procedural: "all", decision: "all", lesson: "all" }, file_edits: "ask", enabled: true });
  const wk = { id: null, draft: null, dirty: false, q: "", busy: false, status: {}, usage: null, statusAt: 0, teamsPick: new Set(), test: null, cat: "" };
  function workerPick(id) {
    const rec = byId(id);
    wk.id = rec?.id || null;
    wk.draft = rec ? structuredClone(rec.crew) : null;
    wk.dirty = false;
    wk.test = null;
  }
  // Usage › expert row asks for that expert's history tab (team.js openExpertHistory).
  // A separate window (popup-drag.js) reopens an automation by id.
  window.addEventListener("arkism:open-automation", (e) => { const r = byId(e.detail?.id); if (r) getAuto().open(r, e.detail.tab); });
  // over: open on top of the page the user is on (사용량), without going to the expert list.
  window.addEventListener("arkism:open-expert", (e) => {
    const { id, tab = "edit", over } = e.detail || {};
    if (!id) return;
    if (over && byId(id) && !wk.pop) { workerPick(id); wk.openTab = tab; openExpertPopup(byId(id));
      // loadAgentInfo repaints only the expert list, so swap in the numbers it fetched here
      loadAgentInfo().then(() => { const r = byId(id); if (r) wk.pop?.dialog.querySelectorAll(".ex-metrics").forEach((x) => x.replaceWith(usageMetrics(r))); }).catch(console.error); } else openWorkers(id, false, tab);
  });
  function openWorkers(id, isNew = false, tab = "edit") {
    if (wk.pop) return;
    state.workerNew = isNew;
    wk.openTab = tab;
    wk.open = true;
    if (isNew) { wk.id = null; wk.draft = blankAgent(); wk.dirty = false; wk.teamsPick = new Set(); } else workerPick(id);
    if (state.view === "workers") render(false); else ctx.go("workers");
  }
  // working·idle·off per agent, and this month's estimated spend; both refresh at most every 20 s.
  async function loadAgentInfo() {
    if (Date.now() - wk.statusAt < 20000) return;
    wk.statusAt = Date.now();
    const [st, use] = await Promise.all([
      request(`/v1/crew/agents/status?${query()}`).then((r) => r.items || {}, () => ({})),
      request(`/v1/crew/usage?${query({ range: "30d", kind: "agent" })}`).then((r) => r, () => null)]);
    wk.status = st && !Array.isArray(st) ? st : {};
    wk.usage = use;
    if (state.view === "workers" && !wk.dirty) render(false);
  }
  function toggleChips(options, has, flip) {
    const row = el("div", "wk-chips");
    for (const [k, label] of options) {
      const on = has(k);
      const b = el("button", `wk-chip${on ? " on" : ""}`, label);
      b.type = "button";
      b.setAttribute("aria-pressed", String(on));
      b.addEventListener("click", () => flip(k, !on));
      row.append(b);
    }
    return row;
  }
  // Chips with ✕ for a list of names, and a box to add one from the suggestions.
  function listChips(list, suggest, placeholder, onChange, listId) {
    const row = el("div", "wk-chips");
    for (const name of list) {
      const chip = el("span", "wk-chip on mono", name);
      const x = el("button", "icon-btn plain");
      x.type = "button";
      x.setAttribute("aria-label", `${name} 빼기`);
      x.append(icon("x"));
      x.addEventListener("click", () => onChange(list.filter((n) => n !== name)));
      chip.append(x);
      row.append(chip);
    }
    const dl = el("datalist");
    dl.id = listId;
    for (const [v, sub] of suggest) if (!list.includes(v)) { const o = el("option", "", sub || ""); o.value = v; dl.append(o); }
    const add = input("", { maxLength: 128, placeholder, cls: "wk-add" });
    add.setAttribute("list", listId);
    add.addEventListener("change", () => { const v = add.value.trim(); if (v && !list.includes(v)) onChange([...list, v]); });
    row.append(add, dl);
    return row;
  }
  const touchWorker = () => { wk.dirty = true; if (wk.redraw && wk.pop) { wk.redraw(); drawWorkerSave(); } else render(false); };
  function workerCard(title, sub, ...nodes) {
    const c = el("section", "wk-card");
    const h = el("div", "wk-card-head");
    h.append(el("h3", "", title));
    if (sub) h.append(el("small", "", sub));
    c.append(h, ...nodes);
    return c;
  }
  // Text inputs keep the draft without redrawing (focus stays); toggles redraw.
  let drawWorkerSave = () => {};
  const bindText = (node, d, key, conv = (v) => v) => { node.addEventListener("input", () => { d[key] = conv(node.value); wk.dirty = true; drawWorkerSave(); }); return node; };
  const pick = (options, value, onChange, label) => {
    const s = select(options, value);
    s.setAttribute("aria-label", label);
    s.addEventListener("change", () => onChange(s.value));
    return s;
  };
  function instructionsField(d) {
    const t = bindText(input(d.instructions, { tag: "textarea", rows: 3, maxLength: 4000, placeholder: "이 전문가가 지킬 역할·말투·순서를 적어 주세요", cls: "wk-instr" }), d, "instructions");
    return field("지시", instructionsAI(t), "최대 4,000자 · 기억은 자료로만 쓰고 지시로 따르지 않아요");
  }
  // 일하는 순서 (868:61909 · 310:19277): the workflow this agent runs when it is called, in one box — name, 단계 고치기,
  // the chart, and at the bottom the choice to switch (empty means it answers on its own).
  function workflowField(d) {
    const s = pick([["", "없음 · 지시대로"], ...allWorkflows().filter((w) => ownerOf(w) ? ownerOf(w).id === wk.id : !triggered(w.crew)).map((w) => [w.crew.handle, w.crew.name])], (d.workflow || "").replace(/^\$/, ""),
      (v) => { d.workflow = v; touchWorker(); }, "일하는 순서");
    s.classList.add("ex-flow-pick");
    const w = allWorkflows().find((x) => x.crew.handle === (d.workflow || "").replace(/^\$/, ""));
    const box = el("div", "wk-flow");
    const head = el("div", "wk-flow-head");
    const mark = el("span", "wk-flow-mark"); mark.append(icon("tree-structure"));
    head.append(mark, el("strong", "", w ? w.crew.name : "워크플로우 없음 · 지시대로 일해요"));
    box.append(head);
    // shortcut: the new workflow opens on its own; it is picked here afterwards, not linked to this expert automatically
    if (!w) head.append(button("새 워크플로우 만들기", "small", "plus", () => getAuto().create()));
    if (w) {
      // Nodes on the same level run together, so they share one number (as in workflowDetail).
      const lvls = levels(w.crew.graph).map((lv) => lv.filter(WORK)).filter((lv) => lv.length);
      head.append(button("단계 고치기", "small", "pencil-simple", () => openGraph(w)));
      const lim = w.crew.limits || {};
      box.append(flowChart(w, lvls));
      if (lim.max_minutes || lim.max_steps) {
        const l = el("small", "wk-flow-limit");
        l.append(el("span", "k", "멈추는 조건"), el("span", "", [lim.max_minutes ? tr(`${lim.max_minutes}분`) : "", lim.max_steps ? tr(`${lim.max_steps}단계 넘으면 멈춤`) : ""].filter(Boolean).join(" · ")));
        box.append(l);
      }
    }
    const foot = el("label", "wk-flow-foot");
    foot.append(el("span", "", w ? "다른 워크플로우로 바꾸기" : "+ 워크플로우 연결"), s);
    box.append(foot);
    const f = el("div", "field", "일하는 순서");
    f.append(box);
    return f;
  }
  // 흐름도 (868:61909): 시작 → one column per level (boxes of the same level stacked) → 끝, arrows between,
  // and each 되돌아가는 선 drawn under the columns it spans with its label. A CSS grid: col 1 start, then arrow/level pairs.
  function flowChart(w, lvls) {
    const g = el("div", "ex-flow");
    g.style.gridTemplateColumns = `repeat(${lvls.length * 2 + 3}, auto)`;
    const at = (node, col, row = 1, span = 1) => { node.style.gridColumn = `${col} / span ${span}`; node.style.gridRow = String(row); g.append(node); return node; };
    const end = (cls, label) => { const e = el("div", `ex-flow-end ${cls}`); e.append(el("i", ""), el("small", "", label)); return e; };
    const arrow = () => el("span", "ex-flow-arrow", "→");
    at(end("start", "시작"), 1);
    const colOf = {};
    lvls.forEach((lv, i) => {
      at(arrow(), 2 + i * 2);
      const col = el("div", "ex-flow-col");
      for (const n of lv) {
        colOf[n.id] = 3 + i * 2;
        const b = el("div", `ex-flow-node${IS_AI(n.type) ? " ai" : ""}`);
        const kind = IS_AI(n.type) ? "AI" : NODES[n.type]?.label || n.type;
        b.append(el("small", "kind", lv.length > 1 ? `${kind} · 동시에` : kind), el("strong", "", nodeTitle(n)));
        if (n.config?.model) b.append(el("code", "m", n.config.model));
        col.append(b);
      }
      at(col, 3 + i * 2);
    });
    at(arrow(), 2 + lvls.length * 2);
    at(end("stop", "끝"), 3 + lvls.length * 2);
    const byId2 = Object.fromEntries((w.crew.graph?.nodes || []).map((n) => [n.id, n]));
    let row = 2;
    for (const e of (w.crew.graph?.edges || []).filter(isBack)) {
      const a = colOf[e.to], b = colOf[e.from];
      const label = `↺ ${nodeTitle(byId2[e.from] || { id: e.from })}${e.port ? ` · ${e.port}` : ""} → ${nodeTitle(byId2[e.to] || { id: e.to })} · 최대 ${e.max}번`;
      if (!a || !b || a > b) { at(el("small", "ex-flow-back-t", label), 1, row++, lvls.length * 2 + 3); continue; }
      at(el("div", "ex-flow-back"), a, row++, b - a + 1);
      at(el("small", "ex-flow-back-t", label), a, row++, lvls.length * 2 + 3 - a + 1);
    }
    const wrap = el("div", "ex-flow-wrap");
    wrap.append(g);
    return wrap;
  }
  function depthRow(d) {
    const r = el("div", "wk-depth");
    r.append(el("strong", "", "생각 깊이"), segment(DEPTH, d.effort || "medium", (k) => { d.effort = k; touchWorker(); }));
    return r;
  }
  // A model box with the maker's mark in front (868:61909); the mark follows what is typed.
  function modelBox(d, key, ph, label, list) {
    const w = el("span", "ex-model");
    const i = bindText(modelInput(d[key], ph, list), d, key);
    i.setAttribute("aria-label", label);
    let mark = modelMark(d[key] || "");
    const tag = el("em", "ex-model-kind", tr(window.ArkismBrand?.kindLabel(d[key]) || ""));
    i.addEventListener("input", () => {
      const m = modelMark(i.value); mark.replaceWith(m); mark = m;
      tag.textContent = tr(window.ArkismBrand?.kindLabel(i.value) || "");
      if (key === "model") wk.onModel?.();
    });
    w.append(mark, i, tag);
    return w;
  }
  // 모델 (868:61909): 기본·예비 모델 and 생각 깊이.
  function modelCard(d) {
    const models2 = el("div", "scope-fields");
    models2.append(field("기본 모델", modelBox(d, "model", "claude sonnet 5.5", "기본 모델"), d.model_mode === "auto" ? "Clef가 일마다 고르고, 정해 두면 이 모델을 먼저 봐요" : ""),
      field("예비 모델", modelBox(d, "fallback_model", "고르기 (선택)", "예비 모델")));
    return workerCard("모델", "", modelList(), models2, depthRow(d));
  }
  const exRow = (label, ...nodes) => { const r = el("div", "ex-run-row"); r.append(el("span", "k", label), ...nodes); return r; };
  // A segment whose saved value is not one of the presets keeps it as an extra choice.
  const presetSeg = (opts, cur, onPick) => segment(opts.some(([k]) => k === cur) ? opts : [...opts, [cur, cur]], cur, onPick);
  // 실행 (868:61909): the title above one box of rows — 방식 · 창의성 · 답변 길이 · 출력 형식 · 파일 고치기 · 부르는 곳 · 이미지.
  function runCard(d) {
    const temp = String(d.temperature ?? "0.7");
    const len = String(Number(d.max_output_tokens) > 0 ? d.max_output_tokens : 0);
    const imgBox = modelBox(d, "image_model", "claude opus 5.5", "이미지가 오면 볼 모델", "crew-models-vision");
    // 이미지 모델 is only asked for when the main model reads text only (a vision model looks at images itself).
    const needsImage = () => window.ArkismBrand?.kind(d.model) !== "vision";
    const c = el("section", "ex-run");
    const rows = el("div", "ex-run-box");
    rows.append(
      exRow("방식", segment(MODEL_MODES, d.model_mode || "fixed", (k) => { d.model_mode = k; touchWorker(); })),
      exRow("창의성", presetSeg([["0.2", "낮음"], ["0.7", "보통"], ["1", "높음"]], temp, (k) => { d.temperature = Number(k); touchWorker(); })),
      exRow("답변 길이", presetSeg([["0", "자동"], ["1024", "짧게"], ["4096", "길게"]], len, (k) => { d.max_output_tokens = Number(k); touchWorker(); }), el("small", "dim", "토큰 수 상한이에요")),
      exRow("출력 형식", segment(FORMATS, d.output_format || "text", (k) => { d.output_format = k; touchWorker(); })),
      exRow("파일 고치기", segment(FILE_EDITS, d.file_edits || "ask", (k) => { d.file_edits = k; touchWorker(); })),
      exRow("부르는 곳", placeChips(d)),
      exRow("이미지 모델", imgBox, modelList("crew-models-vision", (m) => window.ArkismBrand?.kind(m) === "vision")));
    const imgRow = rows.lastElementChild;
    imgRow.hidden = !needsImage();
    wk.onModel = () => { imgRow.hidden = !needsImage(); }; // the 기본 모델 box calls this as it changes
    c.append(el("h3", "", "실행"), rows);
    return c;
  }
  // 스킬 칩 (Figma 929:61931): the pin on a chip keeps the skill in d.pinned (the same list as the 항상 넣는 자료 card), the name opens its detail.
  const pinSvg = (on) => {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("class", "icon");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("fill", on ? "currentColor" : "none");
    svg.setAttribute("stroke", "currentColor");
    svg.setAttribute("stroke-width", "2");
    svg.setAttribute("stroke-linecap", "round");
    svg.setAttribute("stroke-linejoin", "round");
    svg.setAttribute("aria-hidden", "true");
    for (const dd of ["M12 17v5", "M9 10.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H8a2 2 0 0 0 0 4 1 1 0 0 1 1 1z"]) {
      const p = document.createElementNS("http://www.w3.org/2000/svg", "path");
      p.setAttribute("d", dd);
      svg.append(p);
    }
    return svg;
  };
  const skillRec = (name) => (state.data.skills || []).find((x) => x.name === name);
  const skillPin = (d, sk) => (d.pinned || []).find((p) => p.collection === "skills" && p.id === sk.id);
  const skillVerCount = (sk) => Math.max(1, (sk.version_history || []).length);
  const skillActiveVer = (sk) => { const i = (sk.version_history || []).indexOf(sk.active_version); return i >= 0 ? i + 1 : skillVerCount(sk); };
  // Pins up to the server's 20; a new one goes after the others (priority 5 below the lowest).
  const pinOf = (d, collection, id) => (d.pinned || []).find((p) => p.collection === collection && p.id === id);
  const pinOrder = (d) => (d.pinned || []).map((p, i) => [p, i]).sort((a, b) => b[0].priority - a[0].priority || a[1] - b[1]).map(([p]) => p);
  function setPin(d, collection, id, on, patch = {}) {
    const list = (d.pinned || []).slice();
    const at = list.findIndex((p) => p.collection === collection && p.id === id);
    if (on && at < 0) {
      if (list.length >= 20) { notice("항상 넣는 자료는 20개까지예요."); return false; }
      list.push({ collection, id, revision: 0, injection: collection === "knowledge_sources" ? "summary" : "full", priority: list.length ? Math.max(0, Math.min(...list.map((p) => p.priority)) - 5) : 100, ...patch });
    } else if (on) list[at] = { ...list[at], ...patch };
    else if (at >= 0) list.splice(at, 1);
    d.pinned = list;
    return true;
  }
  // 고정 칩 합계 토큰: the same rough estimate as the 항상 넣는 자료 table (pins.js: chars kept per mode / 2.5).
  const PIN_CAP = { full: 8000, summary: 380, reference: 80 };
  function pinChars(p) {
    const src = p.collection === "skills" ? (state.data.skills || []).find((x) => x.id === p.id)
      : p.collection === "memories" ? (state.data.memories || []).find((x) => x.id === p.id) : null;
    return p.collection === "skills" ? (src?.description || "").length * 6 : p.collection === "memories" ? (src?.text || "").length : 600; // shortcut: knowledge counts one chunk (the table has chunk_count), fine for a hint
  }
  const pinTok = (p, mode = p.injection) => Math.max(1, Math.round(Math.min(PIN_CAP[mode] || 0, pinChars(p)) / 2.5));
  const pinTokens = (d) => (d.pinned || []).reduce((n, p) => n + pinTok(p), 0);
  // One chip in 쓸 수 있는 것 (929:61931): a pinned one has the pin and the accent, and can be dragged to change the order.
  function useChip(d, label, { pin, mono, fresh, bad, onOpen, onRemove }) {
    const b = el("button", `wk-chip on ex-uchip${pin ? " pinned" : ""}${mono ? " mono" : ""}${bad ? " bad" : ""}`);
    b.type = "button";
    if (fresh) { const dot = el("i", "ex-dot"); dot.title = tr("새 버전이 있어요"); b.append(dot); }
    if (pin) b.append(pinSvg(true));
    b.append(raw(el("span", "", label)));
    b.title = bad ? tr(pins?.STATUS[bad]?.[1] || "") : label; // the chip shows one line; the whole text on hover
    b.addEventListener("click", onOpen);
    if (pin) {
      b.draggable = true;
      b.addEventListener("dragstart", (e) => { wk.dragPin = pin; e.dataTransfer.effectAllowed = "move"; b.classList.add("dragging"); });
      b.addEventListener("dragend", () => b.classList.remove("dragging"));
      b.addEventListener("dragover", (e) => { if (wk.dragPin && wk.dragPin !== pin) { e.preventDefault(); b.classList.add("over"); } });
      b.addEventListener("dragleave", () => b.classList.remove("over"));
      b.addEventListener("drop", (e) => {
        e.preventDefault();
        const from = wk.dragPin;
        wk.dragPin = null;
        if (!from || from === pin) return;
        const list = pinOrder(d).filter((p) => p !== from);
        list.splice(list.indexOf(pin), 0, from);
        list.forEach((p, i) => { p.priority = Math.max(0, 100 - i * 5); }); // 위에 있을수록 먼저: 100, 95, 90 …
        d.pinned = list;
        touchWorker();
      });
    }
    if (!onRemove) return b;
    // ✕ at the top right on hover takes it off the list (a pin and the skill name both go)
    const w = el("span", "ex-uchip-wrap");
    const x = el("button", "ex-uchip-x");
    x.type = "button";
    x.setAttribute("aria-label", `${tr("목록에서 빼기")}: ${label}`);
    x.append(icon("x"));
    x.addEventListener("click", (e) => { e.stopPropagation(); onRemove(); touchWorker(); });
    w.append(b, x);
    return w;
  }
  const USE_KINDS = [["memories", "기억"], ["skills", "스킬"], ["tools", "도구"], ["knowledge_sources", "지식"]];
  // 쓸 수 있는 것 (929:61931): 스킬·도구·기억·지식 rows of chips (필요할 때 쓰기; a pin = 항상 먼저 넣기), the pin total,
  // then one box to find and add any of the four.
  function useCard(d, rec) {
    const c = workerCard("쓸 수 있는 것", "핀을 꽂은 것은 부를 때마다 먼저 넣어요. 칩을 누르면 자세히 볼 수 있어요.");
    const body = el("div", "ex-use");
    const head = c.querySelector(".wk-card-head");
    let leftOf = () => [];
    const plus = button("+ 추가", "small", null, () => openUseAdd(leftOf));
    head.append(plus);
    c.append(body);
    const draw = () => {
      pins?.loadLibrary();
      const res = rec && pins ? pins.resolution(rec) : null;
      const badOf = (p) => { const st = rec && pins?.statusOf(rec, p, res); return st && st.status !== "ok" ? st.status : ""; };
      const grid = el("div", "ex-use-rows");
      const row = (label, ...kids) => { const r = el("div", "wk-chips"); r.append(...kids); grid.append(el("span", "ex-use-k", label), r); return r; };
      // 스킬: the ones it may use, and any pinned skill.
      const pinnedSkills = pinOrder(d).filter((p) => p.collection === "skills").map((p) => (state.data.skills || []).find((x) => x.id === p.id)?.name).filter(Boolean);
      const names = [...new Set([...(d.skills || []), ...pinnedSkills])];
      const skillKids = names.map((name) => {
        const sk = skillRec(name);
        const pin = sk ? skillPin(d, sk) : null;
        return useChip(d, name, { pin, mono: true, fresh: !!(pin?.revision && skillActiveVer(sk) > pin.revision), bad: pin ? badOf(pin) : "", onOpen: () => openUseDetail(d, rec, { collection: "skills", id: sk?.id, name }),
          onRemove: () => { d.skills = (d.skills || []).filter((n) => n !== name); if (sk) setPin(d, "skills", sk.id, false); } });
      });
      // What may still be added, per kind (the four "+ X 추가" lists and the card's "+ 추가" share it).
      const left = (kind) => kind === "tools" ? (ctx.toolServers?.() || []).filter((s) => !(d.tools || []).includes(s.name)).map((s) => ({ id: s.name, title: s.name, meta: s.desc || "", add: () => { d.tools = [...(d.tools || []), s.name]; } }))
        : kind === "skills" ? (pins?.candidates("skills") || []).filter((x) => !names.includes(x.title)).map((x) => ({ ...x, add: () => { d.skills = [...(d.skills || []), x.title]; } }))
        : (pins?.candidates(kind) || []).filter((x) => !pinOf(d, kind, x.id)).map((x) => ({ ...x, add: () => setPin(d, kind, x.id, true) }));
      leftOf = left;
      row("스킬", ...skillKids);
      const servers = (ctx.toolServers?.() || []).map((s) => [s.name, s.desc || ""]);
      const tools = listChips(d.tools || [], servers, "+ 도구 추가", (v) => { d.tools = v; touchWorker(); }, "wk-tools");
      tools.classList.add("ex-tools");
      tools.querySelectorAll(".wk-add, datalist").forEach((n) => n.remove()); // adding goes through + 추가
      grid.append(el("span", "ex-use-k", "도구"), tools);
      // 기억: pinned memories, then the kinds it reads when it looks things up.
      const memPins = pinOrder(d).filter((p) => p.collection === "memories").map((p) => useChip(d, pins?.known(p)?.title || p.id, { pin: p, bad: badOf(p), onOpen: () => openUseDetail(d, rec, p), onRemove: () => setPin(d, p.collection, p.id, false) }));
      const kinds = toggleChips(MEMORY_KINDS, (k) => !!d.memory?.[k] && d.memory[k] !== "off", (k, on) => {
        const m = { ...(d.memory || {}) };
        if (on) m[k] = "all"; else delete m[k];
        d.memory = m;
        touchWorker();
      });
      for (const k of kinds.children) { k.classList.add("ex-kind"); k.title = tr(MEMORY_KIND_HINT[k.textContent] || ""); }
      row("기억", ...memPins);
      row("찾아볼 기억", ...kinds.children); // which kinds it searches when it looks things up, apart from the pinned ones
      row("지식", ...pinOrder(d).filter((p) => p.collection === "knowledge_sources").map((p) => useChip(d, pins?.known(p)?.title || p.id, { pin: p, bad: badOf(p), onOpen: () => openUseDetail(d, rec, p), onRemove: () => setPin(d, p.collection, p.id, false) })));
      const n = (d.pinned || []).length;
      const sum = raw(el("small", "ex-pin-sum", n ? tr(`고정 ${n}개 · 약 ${pinTokens(d).toLocaleString("ko-KR")} 토큰 · 넣는 순서는 고정 칩을 끌어서 바꿔요`) : tr("핀을 꽂으면 부를 때마다 먼저 넣어요")));
      body.replaceChildren(grid, sum);
    };
    pins?.watch(body, draw);
    draw();
    return c;
  }
  // + 추가 (쓸 수 있는 것): one popup to find and add from 기억·스킬·도구·지식 — a search box over tabs and a list.
  function openUseAdd(leftOf) {
    const dlg = el("dialog", "crew-dialog ex-add-dlg");
    const head = el("div", "dialog-head");
    head.append(el("h2", "", "쓸 수 있는 것 추가"));
    const x = el("button", "icon-btn ghost");
    x.type = "button";
    x.setAttribute("aria-label", "닫기");
    x.append(icon("x"));
    x.addEventListener("click", () => dlg.close());
    head.append(x);
    const q = el("input", "ex-add-search");
    q.type = "search";
    q.placeholder = tr("기억·스킬·도구·지식에서 찾기");
    q.setAttribute("aria-label", q.placeholder);
    const tabs = el("div", "ex-add-tabs");
    tabs.setAttribute("role", "tablist");
    const list = el("div", "ex-add-list");
    let tab = "";
    const draw = () => {
      tabs.replaceChildren(...[["", "전체"], ...USE_KINDS].map(([k, name]) => {
        const b = button(name, `ex-add-tab${k === tab ? " on" : ""}`, null, () => { tab = k; draw(); });
        b.setAttribute("role", "tab");
        b.setAttribute("aria-selected", String(k === tab));
        return b;
      }));
      const term = q.value.trim().toLowerCase();
      const rows = [];
      for (const [kind, name] of USE_KINDS) {
        if (tab && tab !== kind) continue;
        for (const it of leftOf(kind)) {
          if (term && !`${it.title} ${it.meta || ""}`.toLowerCase().includes(term)) continue;
          const r = el("div", "ex-add-row");
          const words = el("div", "ex-add-words");
          words.append(raw(el("b", kind === "skills" || kind === "tools" ? "mono" : "", it.title)), el("small", "", [tab ? "" : tr(name), it.meta || ""].filter(Boolean).join(" · ")));
          r.append(words, button("추가", "small", null, () => { if (it.add() !== false) { touchWorker(); draw(); } }));
          rows.push(r);
        }
      }
      list.replaceChildren(...(rows.length ? rows : [el("p", "dim", term ? "찾는 것이 없어요." : "더할 수 있는 승인된 자료가 없어요.")]));
    };
    q.addEventListener("input", draw);
    const body = el("div", "dialog-body");
    body.append(q, tabs, list);
    const foot = el("div", "dialog-foot");
    foot.append(button("완료", "primary", null, () => dlg.close()));
    dlg.append(head, body, foot);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    draw();
    dlg.showModal();
    q.focus();
  }
  // 칩 상세 (929:61931): what it is, 항상 먼저 넣기, 넣는 방식, 버전 · 새 버전 안내, 이 전문가에서 빼기 · 완료.
  const MODE_LINE = { full: "전체: 내용을 다 넣어요", summary: "요약: 줄여서 넣어요", reference: "참조: 제목만 넣고 필요할 때 읽어요" };
  const USE_KIND_NAME = { skills: "스킬", memories: "기억", knowledge_sources: "지식" };
  function openUseDetail(d, rec, item) {
    const isSkill = item.collection === "skills";
    const sk = isSkill ? skillRec(item.name) : null;
    const id = isSkill ? sk?.id : item.id;
    const known = !isSkill && pins ? pins.known(item) : null;
    const title = isSkill ? item.name : known?.title || item.id;
    const dlg = el("dialog", "crew-dialog ex-skill-dlg");
    const head = el("div", "dialog-head");
    const words = el("div", "ex-dlg-title");
    words.append(el("small", "ex-dlg-kind", USE_KIND_NAME[item.collection] || ""), raw(el("h2", isSkill ? "mono" : "", title)));
    const x = el("button", "icon-btn ghost");
    x.type = "button";
    x.setAttribute("aria-label", "닫기");
    x.append(icon("x"));
    x.addEventListener("click", () => dlg.close());
    head.append(words, x);
    const body = el("div", "dialog-body");
    const foot = el("div", "dialog-foot");
    const draw = () => {
      body.replaceChildren();
      if (isSkill && !sk) { body.append(el("p", "dim", "스킬 목록에서 찾지 못했어요. 이름이 바뀌었거나 아직 승인되지 않았을 수 있어요.")); return; }
      const pin = pinOf(d, item.collection, id);
      const text = isSkill ? sk.description : item.collection === "memories" ? (state.data.memories || []).find((m) => m.id === id)?.text : known?.meta;
      if (text) body.append(raw(el("p", "ex-dlg-text", text)));
      const order = pinOrder(d);
      const tgl = el("div", "ex-pin-toggle");
      const l = el("div", "");
      l.append(el("strong", "", "항상 먼저 넣기"), raw(el("small", "", pin ? tr(`켜면 부를 때마다 먼저 넣어요 · 고정 ${order.length}개 중 ${order.indexOf(pin) + 1}번째`) : tr("켜면 부를 때마다 먼저 넣어요"))));
      tgl.append(l, switchEl(!!pin, "항상 먼저 넣기", (on) => { if (setPin(d, item.collection, id, on)) { touchWorker(); draw(); } }));
      body.append(tgl);
      if (pin) {
        const mode = el("div", "ex-dlg-sec");
        mode.append(el("span", "ex-dlg-k", "넣는 방식"), segment(pins?.MODES || [["full", "전체"], ["summary", "요약"], ["reference", "참조"]], pin.injection || "full", (k) => { setPin(d, item.collection, id, true, { injection: k }); touchWorker(); draw(); }),
          raw(el("small", "", `${tr(MODE_LINE[pin.injection] || "")} · ${tr(`약 ${pinTok(pin).toLocaleString("ko-KR")} 토큰`)}`)));
        const vers = [["0", "최신"]];
        if (isSkill) for (let v = Math.max(skillVerCount(sk), pin.revision || 0); v >= 1; v--) vers.push([String(v), tr(`고정 v${v}`)]);
        else for (const v of new Set([known?.revision, pin.revision].filter(Boolean))) vers.push([String(v), tr(`고정 v${v}`)]);
        const ver = el("div", "ex-dlg-sec");
        const vrow = el("div", "ex-dlg-row");
        vrow.append(pick(vers, String(pin.revision || 0), (v) => { setPin(d, item.collection, id, true, { revision: Number(v) }); touchWorker(); draw(); }, "버전"), el("small", "", "고정이면 이 버전만 써요. 최신이면 새 버전을 따라가요."));
        ver.append(el("span", "ex-dlg-k", "버전"), vrow);
        body.append(mode, ver);
        const act = isSkill ? skillActiveVer(sk) : 0;
        const keep = `${id}:${act}:${pin.revision}`;
        if (isSkill && pin.revision && pin.revision < act && !wk.keptVer?.has(keep)) {
          const note = el("div", "ex-newver");
          const acts = el("div", "ex-newver-acts");
          acts.append(button(tr(`v${act}로 바꾸기`), "primary small", null, () => { setPin(d, item.collection, id, true, { revision: act }); touchWorker(); draw(); }),
            button(tr(`v${pin.revision} 유지`), "small", null, () => { (wk.keptVer ||= new Set()).add(keep); draw(); }));
          note.append(raw(el("span", "", tr(`v${act}가 나왔어요`))), acts);
          body.append(note);
        }
      }
      if (isSkill) {
        const users = agents().filter((r) => (r.crew.skills || []).includes(item.name)).length;
        const at = sk.updated_at ? new Date(sk.updated_at) : null;
        body.append(raw(el("small", "ex-dlg-meta", [tr(`이 스킬을 쓰는 전문가 ${users}명`), at && !isNaN(at) ? tr(`마지막 업데이트 ${String(at.getMonth() + 1).padStart(2, "0")}-${String(at.getDate()).padStart(2, "0")}`) : ""].filter(Boolean).join(" · "))));
      }
    };
    const out = el("button", "ex-dlg-out", "이 전문가에서 빼기");
    out.type = "button";
    out.addEventListener("click", () => { if (id) setPin(d, item.collection, id, false); if (isSkill) d.skills = (d.skills || []).filter((n) => n !== item.name); dlg.close(); touchWorker(); });
    foot.append(out, button("완료", "primary", null, () => dlg.close()));
    dlg.append(head, body, foot);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    draw();
    dlg.showModal();
  }
  // 부르는 곳 (868:61909): the agents as their logos, the rest by name; a pressed one is lit.
  const PLACE_LOGO = { claude_code: "claude", codex: "openai", zcode: "zai", discord: "discord", app: "arkism" }; // brand.js names
  function placeChips(d) {
    const row = toggleChips(CALL_PLACES, (k) => (d.agents || []).includes(k), (k, on) => { d.agents = on ? [...(d.agents || []), k] : (d.agents || []).filter((a) => a !== k); touchWorker(); });
    [...row.children].forEach((b, i) => {
      const [k, label] = CALL_PLACES[i];
      const logo = window.ArkismBrand?.mark(PLACE_LOGO[k]);
      if (!logo) return;
      b.classList.add("ex-place-logo");
      b.setAttribute("aria-label", label);
      b.title = label;
      b.replaceChildren(logo);
    });
    return row;
  }
  // 예산 (868:61909): amounts with the name inside the box on the right ('월 한도' · '1회 한도'), then this month's use.
  function budgetCard(id, d) {
    const money = (key, label, ph, step) => {
      const w = el("label", "ex-money");
      const i = input(d.budget?.[key] || "", { type: "number", min: 0, step, placeholder: ph });
      i.setAttribute("aria-label", label);
      i.addEventListener("input", () => { d.budget = { ...(d.budget || {}), [key]: Number(i.value) > 0 ? Number(i.value) : 0 }; wk.dirty = true; drawWorkerSave(); });
      w.append(el("span", "cur", "$"), i, el("span", "lab", label));
      return w;
    };
    const row = el("div", "scope-fields");
    row.append(money("monthly_usd", "월 한도", "20", "1"), money("ask_over_usd", "1회 한도", "0.50", "0.5"));
    const out = [row];
    if (id) {
      const use = (wk.usage?.agents || []).find((a) => a.id === id);
      const spent = Number(use?.cost_usd) || 0;
      const limit = Number(use?.limit_usd) || Number(d.budget?.monthly_usd) || 0;
      const box = el("div", "ex-spend");
      const top = el("div", "ex-spend-top");
      top.append(el("span", "", "이번 달 한도"), el("span", "", limit ? `$${spent.toFixed(2)} / $${limit} · ${Math.min(100, Math.round(use?.pct ?? (spent / limit) * 100))}%` : `$${spent.toFixed(2)} · 한도 없음`));
      box.append(top);
      if (limit) { const bar = el("div", "wk-bar"); const fill = el("i"); fill.style.width = `${Math.min(100, Math.round(use?.pct ?? (spent / limit) * 100))}%`; bar.append(fill); box.append(bar); }
      const ask = Number(d.budget?.ask_over_usd) || 0;
      const foot = el("div", "ex-spend-top");
      foot.append(el("small", "", "1회 실행 상한"), el("small", "", ask ? `$${ask} · 넘으면 멈추고 물어봐요` : "정하지 않음"));
      box.append(foot);
      out.push(box);
    }
    return workerCard("예산", "", ...out);
  }
  // 채널 연결 (868:61909, 1012:60747): the Discord channels bound to this expert (bot channel action kind "agent").
  // Same data as the team popup: GET /v1/discord, POST /v1/discord/bots/{id}/update with the whole channel list.
  const botOf = (r) => (r?.discord_bot ? { ...r.discord_bot, id: r.id, revision: r.revision } : r);
  function loadBots() {
    if (wk.bots !== undefined) return;
    wk.bots = null;
    request(`/v1/discord?${query()}`).then((d) => { wk.bots = (d.bots || []).map(botOf); }, () => { wk.bots = []; }).then(() => wk.pop?.dialog.open && wk.redraw());
  }
  async function saveBotChannels(b, channels, okText) {
    try {
      const saved = await request(`/v1/discord/bots/${encodeURIComponent(b.id)}/update`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: b.revision,
        bot: { name: b.name, channels, allowed: b.allowed || { user_ids: [], role_ids: [] }, ...(b.target ? { target: b.target } : {}), ...(b.guild_id ? { guild_id: b.guild_id } : {}) } }) });
      Object.assign(b, saved?.id ? botOf(saved) : { channels, revision: (b.revision || 0) + 1 });
      notice(okText);
    } catch (e) { notice(errorText(e)); }
    wk.redraw();
  }
  function channelCard(rec) {
    loadBots();
    const add = button("+ 채널 연결", "ghost small", null, () => { wk.chAdd = wk.chAdd ? null : { bot: wk.bots?.[0]?.id || "", channel: "" }; wk.redraw(); });
    const list = el("div", "ex-ch-list");
    const bots = wk.bots || [];
    const mine = bots.flatMap((b) => (b.channels || []).filter((c) => c.action?.kind === "agent" && c.action.id === rec.id).map((c) => ({ c, b })));
    for (const { c, b } of mine) {
      const r = el("div", "ex-ch-row");
      const mode = button(c.auto_start ? "글이 올라오면 답해요" : `@${rec.crew.handle}로 부를 때만`, "ghost small", null,
        () => saveBotChannels(b, b.channels.map((x) => (x === c ? { ...x, auto_start: !x.auto_start } : x)), "바꿨어요."));
      mode.title = tr("눌러서 바꿔요");
      const warn = ["needs_permission", "token_invalid"].includes(b.state);
      r.append(raw(el("span", "t", `디스코드 · #${c.name || c.channel_id}`)), raw(el("small", "", b.name || "")), ...(warn ? [el("span", "wf-badge small warn", "확인 필요")] : []), mode,
        button("빼기", "ghost small", null, async () => { if (await ask(tr(`#${c.name || c.channel_id} 연결을 뺄까요?`))) saveBotChannels(b, b.channels.filter((x) => x !== c), "연결을 뺐어요.").catch(() => {}); }));
      list.append(r);
    }
    if (wk.bots === null) list.append(el("p", "dim", "불러오는 중…"));
    else if (!mine.length) list.append(el("p", "dim", bots.length ? "연결된 채널이 없어요." : "연결된 디스코드 봇이 없어요."));
    const out = [list];
    if (wk.chAdd && bots.length) {
      const f = wk.chAdd;
      const b = bots.find((x) => x.id === f.bot) || bots[0];
      const known = wk.chKnown?.[b.id];
      if (known === undefined) {
        wk.chKnown = { ...(wk.chKnown || {}), [b.id]: null };
        request(`/v1/discord/bots/${encodeURIComponent(b.id)}/channels?${query()}`).then((r) => r.items || [], () => [])
          .then((items) => { wk.chKnown[b.id] = items; wk.redraw(); });
      }
      const row = el("div", "ex-ch-add");
      const botSel = select(bots.map((x) => [x.id, x.name || x.id]), b.id);
      botSel.className = "wf-select";
      botSel.setAttribute("aria-label", "봇");
      botSel.addEventListener("change", () => { f.bot = botSel.value; f.channel = ""; wk.redraw(); });
      const chSel = select([["", known ? "채널 고르기" : "채널 불러오는 중…"], ...(known || []).map((c) => [c.channel_id, `#${c.name}${c.parent ? ` · ${c.parent}` : ""}`])], f.channel);
      chSel.className = "wf-select";
      chSel.setAttribute("aria-label", "채널");
      chSel.addEventListener("change", () => { f.channel = chSel.value; });
      row.append(botSel, chSel, button("연결", "primary small", null, async () => {
        const id = chSel.value;
        if (!id) { window.ArkismNeed(chSel, "채널을 골라 주세요."); return; }
        const had = (b.channels || []).find((x) => x.channel_id === id);
        if (had && !await ask(tr("이 채널은 이미 다른 곳에 연결돼 있어요. 이 전문가로 바꿀까요?"))) return;
        const ch = { channel_id: id, name: (known || []).find((x) => x.channel_id === id)?.name || "", action: { kind: "agent", id: rec.id }, auto_start: false };
        wk.chAdd = null;
        saveBotChannels(b, [...(b.channels || []).filter((x) => x.channel_id !== id), ch], "채널을 연결했어요.").catch(() => {});
      }));
      out.push(row);
    } else if (wk.chAdd) out.push(el("small", "dim", "먼저 디스코드 봇을 추가해 주세요."));
    out.push(el("small", "dim", "봇 토큰·연결 상태는 설정 › 메신저에서 관리해요. 바꾸면 바로 저장돼요."));
    const c = workerCard("채널 연결", "", ...out);
    c.classList.add("ex-ch");
    c.querySelector(".wk-card-head")?.append(add);
    return c;
  }
  // What goes to the server: the contract fields only, empty ones left out.
  function workerOut(d) {
    const out = { type: "agent", name: (d.name || "").trim(), handle: (d.handle || "").trim().replace(/^@/, ""), role: (d.role || "").trim(), instructions: d.instructions || "",
      model_mode: d.model_mode || "fixed", enabled: d.enabled !== false };
    for (const k of ["category", "model", "fallback_model", "image_model", "effort", "output_format", "file_name", "file_edits", "template"]) if (String(d[k] || "").trim()) out[k] = String(d[k]).trim();
    for (const k of ["skills", "tools", "agents", "install"]) if (d[k]?.length) out[k] = d[k];
    if (String(d.workflow || "").trim()) out.workflow = String(d.workflow).trim().replace(/^\$/, "");
    if (d.memory && Object.keys(d.memory).length) out.memory = d.memory;
    if (d.temperature !== undefined && d.temperature !== null && d.temperature !== "") out.temperature = Number(d.temperature);
    if (Number(d.max_output_tokens) > 0) out.max_output_tokens = Number(d.max_output_tokens);
    const b = {};
    if (Number(d.budget?.monthly_usd) > 0) b.monthly_usd = Number(d.budget.monthly_usd);
    if (Number(d.budget?.ask_over_usd) > 0) b.ask_over_usd = Number(d.budget.ask_over_usd);
    if (Object.keys(b).length) out.budget = b;
    if (d.avatar) out.avatar = d.avatar;
    if (d.pinned?.length) out.pinned = d.pinned;
    return out;
  }
  async function saveWorker(rec) {
    const out = workerOut(wk.draft);
    if (!out.name || !out.handle || !out.instructions.trim()) { window.ArkismNeed(!out.name ? "이름" : !out.handle ? "@이름" : "지시", !out.name ? "이름을 적어 주세요." : !out.handle ? "@이름을 적어 주세요." : "지시를 적어 주세요."); return; }
    if (agents().some((r) => r.id !== rec?.id && r.crew.handle === out.handle) || teams().some((r) => r.crew.handle === out.handle)) { notice(`@${out.handle}은 이미 있어요. 다른 이름을 써 주세요.`); return; }
    wk.busy = true;
    drawWorkerSave();
    try {
      const saved = await save(rec, out);
      // 팀에 넣기 (new agent only): each picked team gets it as a worker at the end.
      let joined = 0;
      for (const tid of rec ? [] : wk.teamsPick) {
        const t = byId(tid);
        if (!t) continue;
        const members = t.crew.members || [];
        try {
          await save(t, { ...t.crew, members: [...members, { agent_id: saved.id, order: members.length + 1, role: "worker" }] });
          joined++;
        } catch (_) { /* reported below */ }
      }
      const fresh = rec ? [] : wk.newTeams || [];
      for (const t of fresh) {
        try {
          await save(null, { type: "team", name: t.name, handle: t.handle, role: "", members: [{ agent_id: saved.id, order: 1, role: "worker" }], mode: "round_robin",
            ending: { max_rounds: 4, token_cap: 100000, end_on_all_approved: true, end_on_moderator: true }, context: [], templates: [] });
          joined++;
        } catch (_) { /* reported below */ }
      }
      if (!rec) wk.newTeams = [];
      notice(rec ? "저장했어요." : wk.teamsPick.size + fresh.length && joined < wk.teamsPick.size + fresh.length ? "만들었어요. 팀에 못 넣은 곳이 있어요 — 팀 화면에서 넣어 주세요." : "전문가를 만들었어요.");
      state.workerNew = false;
      await reload();
      workerPick(saved.id || rec?.id); // the preview server answers {} — keep the open record
      if (!rec && wk.pop) { const dlg = wk.pop.dialog; wk.pop = null; dlg.close(); openExpertPopup(byId(saved.id)); } else { wk.ai = wk.instrAI = null; wk.paintHead?.(); wk.paintAct?.(); wk.redraw?.(); }
    } catch (e) { notice(errorText(e)); } finally { wk.busy = false; drawWorkerSave(); }
  }
  async function removeWorker(rec) {
    const inTeams = teamsOf(rec.id);
    if (inTeams.length) { notice(`${inTeams.map((t) => t.crew.name).join(", ")}에 들어 있어요. 팀에서 먼저 빼 주세요.`); return; }
    if (!await askTyped(`'${rec.crew.name}' 전문가를 삭제할까요?\n되돌릴 수 없어요. 확인하려면 이름 '${rec.crew.name}'을 그대로 입력하세요.`, rec.crew.name, "삭제")) return;
    try {
      await request(`/v1/crew/${encodeURIComponent(rec.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope, expected_revision: rec.revision }) });
      notice("지웠어요.");
      wk.id = null;
      wk.dirty = false;
      wk.pop?.dialog.close();
      await reload();
    } catch (e) { notice(errorText(e)); }
  }
  // AI로 초안 만들기: the fill helper answers name·role·instructions·file_name·skills·tools as JSON, each one a proposal.
  async function draftWorker(ask) {
    const skills = (state.data.skills || []).slice(0, 200).map((s) => s.name).join(", ");
    const tools = (ctx.toolServers?.() || []).map((s) => s.name).join(", ");
    const prompt = `Define a work agent. Request: ${ask}\n\nWrite the instructions in Korean under three Markdown headings in Korean (role, tone, things not to do), briefly (<= 4000 chars).\nSkills available: ${skills || "(none)"}\nMCP servers: ${tools || "(none)"}`;
    const a = await kit.assist("fill", prompt, { persona: { crew: { instructions: "You design AI work agents and write in short, clear Korean." } } });
    if (a.status !== "done") throw new Error(kit.assistError(a));
    const got = kit.parseJSON(a.output || "") || {};
    if (!got.name && !got.instructions) throw new Error("AI 답을 읽지 못했어요. 다시 눌러 주세요.");
    const str = (v, n) => String(v || "").slice(0, n);
    const names = new Set((state.data.skills || []).map((s) => s.name));
    const servers = new Set((ctx.toolServers?.() || []).map((s) => s.name));
    const name = fieldProp("name", "이름", str(got.name, 60) || undefined);
    if (name) {
      // the @이름 and file name follow the name unless the person typed their own
      const file = /^[a-z0-9][a-z0-9-]{0,63}$/.test(got.file_name || "") ? got.file_name : null;
      const set = name.apply;
      name.apply = () => { if (!wk.draft.handle) wk.draft.handle = str(got.name, 60).replace(/\s+/g, "").slice(0, 40); if (file) wk.draft.file_name = file; set(); wk.paintHead?.(); };
    }
    const props = [name, fieldProp("role", "소개", str(got.role, 200) || undefined), fieldProp("instructions", "지시", str(got.instructions, 4000) || undefined),
      Array.isArray(got.skills) && fieldProp("skills", "스킬", got.skills.map(String).filter((s) => names.has(s)).slice(0, 64)),
      Array.isArray(got.tools) && fieldProp("tools", "도구", got.tools.map(String).filter((s) => servers.has(s)).slice(0, 32))].filter(Boolean);
    return { say: props.length ? "초안을 만들었어요. 버튼 아래 목록에서 보고 적용한 뒤 만들어 주세요." : "채울 곳을 찾지 못했어요. 말을 바꿔 다시 물어봐 주세요.", props };
  }
  // 전문가 (208:17763 · 868:61909 · 310:19277): photo cards in one panel; a card opens the detail popup (개요·테스트·기록),
  // and 새 전문가 opens the same popup frame as a form. Everything the old right-hand panel did lives in the popup.
  const catsOf = (all) => [...new Set(all.map((r) => r.crew.category).filter(Boolean))];
  const expertPopup = window.ArkismCrewPopup({ el, button });
  const faceOf = (x, cls = "") => {
    if (!validAvatar(x?.avatar)) { const m = modelMark(x?.model); m.classList.add("ex-mark"); return m; }
    const s = el("span", `ex-art ${cls}`.trim());
    s.style.setProperty("--face-bg", `var(--avatar-${x.avatar.bg})`);
    const img = el("img");
    img.src = `img/avatars/${x.avatar.art}.png`;
    img.alt = "";
    s.append(img);
    return s;
  };
  function expertCard(r) {
    const x = r.crew;
    const b = el("button", `ex-card${x.enabled === false ? " off" : ""}`);
    b.type = "button";
    if (validAvatar(x.avatar)) b.style.setProperty("--face-bg", `var(--avatar-${x.avatar.bg})`);
    const art = el("span", "ex-card-art");
    art.append(faceOf(x));
    b.append(art);
    if (x.category) { const c = el("small", "ex-card-cat"); c.append(icon(ctx.catIcon(x.category)), el("span", "", x.category)); b.append(c); }
    b.append(el("strong", "", x.name));
    b.title = x.enabled === false ? `${x.name} · 꺼짐` : x.name; // the full name, when two lines cut it; 763:57845: an off card is only dimmed
    b.addEventListener("click", () => { workerPick(r.id); openExpertPopup(r); });
    return b;
  }
  function renderWorkers() {
    $("#controls").replaceChildren();
    const main = $("#table");
    main.className = "ex-main";
    void loadAgentInfo();
    const all = agents();
    const cats = catsOf(all);
    if (wk.cat && !cats.includes(wk.cat)) wk.cat = "";
    const shown = all.filter((r) => !wk.cat || r.crew.category === wk.cat);
    const panel = el("section", "ex-panel");
    // Same chips as 팀 (tm-cats): icon, name, count.
    const top = el("div", "tm-cats");
    const chips = el("div", "tm-chips");
    for (const key of ["", ...cats]) {
      const c = el("button", `tm-chip${(wk.cat || "") === key ? " active" : ""}`); c.type = "button";
      const name = el("span", "", key || "모든 카테고리");
      if (key) name.setAttribute("data-no-i18n", "");
      c.append(icon(ctx.catIcon(key)), name, el("output", "", String(key ? all.filter((r) => r.crew.category === key).length : all.length)));
      c.addEventListener("click", () => { wk.cat = key; render(false); });
      chips.append(c);
    }
    top.append(chips, button("+ 새 전문가", "primary", null, () => openWorkers(null, true)));
    const grid = el("div", "ex-cards");
    const add = el("button", "ex-card ex-card-new");
    add.type = "button";
    add.append(el("span", "ex-plus", "+"), el("strong", "", "새 전문가"), el("small", "", "페르소나와 워크플로우를 정해 시작해요"));
    add.addEventListener("click", () => openWorkers(null, true));
    grid.append(...shown.map(expertCard), add);
    panel.append(top, grid);
    if (!all.length) panel.append(el("p", "ex-empty", "역할·지시·모델·쓸 수 있는 스킬을 한 번 정해 두면, Claude Code·Codex 어디서든 @이름으로 불러 일을 맡겨요."));
    main.replaceChildren(panel);
    if ((wk.open || state.workerNew) && !wk.pop) {
      wk.open = false;
      const rec = !state.workerNew && wk.id ? byId(wk.id) : null;
      if (state.workerNew || rec) openExpertPopup(rec);
    }
  }

  // ---- inline editing in the popup head (868:61909): click a text, Enter or leaving the box keeps it, Esc cancels.
  function inlineEdit(tag, cls, get, set, { max, ph, validate }) {
    const b = el(tag, `ex-inline ${cls}`);
    b.tabIndex = 0;
    b.setAttribute("role", "button");
    const paint = () => { b.textContent = get() || ph; b.classList.toggle("empty", !get()); };
    paint();
    const start = () => {
      const i = input(get(), { maxLength: max, placeholder: ph, cls: `ex-inline-input ${cls}` });
      i.setAttribute("aria-label", ph);
      b.replaceWith(i);
      i.focus();
      i.select();
      let done = false;
      const finish = (commit) => {
        if (done) return;
        done = true;
        const err = commit ? validate?.(i.value.trim()) : "";
        if (err) notice(err); else if (commit) set(i.value.trim());
        i.replaceWith(b);
        paint();
      };
      i.addEventListener("keydown", (e) => {
        if (e.key === "Enter") { e.preventDefault(); finish(true); }
        if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); finish(false); }
      });
      i.addEventListener("blur", () => finish(true));
    };
    b.addEventListener("click", start);
    b.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); start(); } });
    return b;
  }
  // 1회 예상 (868:61909): a rough size of one call (instructions + a base prompt), its average price and how long recent calls took.
  function estimateNode() {
    const box = el("div", "ex-est");
    const paint = () => {
      const d = wk.draft;
      const row = (wk.usage?.agents || []).find((a) => a.id === wk.id);
      const tok = 1200 + Math.round((d?.instructions || "").length / 2);
      const avg = row?.calls ? row.cost_usd / row.calls : null;
      const secs = (wk.calls || []).filter((c) => c.ok && c.latency_ms > 0).map((c) => Math.max(1, Math.round(c.latency_ms / 1000))).sort((a, b) => a - b);
      const time = !secs.length ? "기록 없음" : secs[0] === secs[secs.length - 1] ? tr(`약 ${secs[0]}초`) : tr(`${secs[0]}~${secs[secs.length - 1]}초`);
      // label · dotted leader · value on each line
      const line = (k, v) => { const r = el("div", "ex-est-row"); r.append(el("span", "", k), el("i", ""), raw(el("span", "v", v))); return r; };
      box.replaceChildren(line("예상 토큰", tr(`약 ${tok >= 1000 ? `${(tok / 1000).toFixed(1)}K` : tok}`)), line("예상 비용", avg == null ? tr("기록 없음") : tr(`약 ${"$" + avg.toFixed(avg < 0.1 ? 3 : 2)}`)), line("예상 시간", time));
    };
    paint();
    wk.estPaint = paint;
    return box;
  }
  // 저장 줄: always there (868:61909); 저장 lights up once something changed.
  drawWorkerSave = () => { if (wk.okBtn) wk.okBtn.disabled = !!wk.busy || (!!wk.bar && !wk.dirty); if (wk.bar) wk.bar.classList.toggle("dirty", !!wk.dirty); wk.estPaint?.(); };

  function openExpertPopup(rec) {
    if (wk.pop) return;
    const isNew = !rec;
    if (isNew) { wk.id = null; wk.draft = blankAgent(); wk.dirty = false; wk.teamsPick = new Set(); wk.newTeams = []; wk.ai = wk.instrAI = null; wk.autoHandle = ""; }
    wk.bots = undefined; wk.chAdd = null; wk.chKnown = {}; // fresh channel bindings per opening
    wk.versions = undefined; wk.histOpen = new Set(); wk.calls = null;
    wk.tab = "edit";
    const face = el("button", "ex-face");
    face.type = "button";
    face.setAttribute("aria-label", "아바타 고르기");
    const paintFace = () => face.replaceChildren(wk.draft.avatar && validAvatar(wk.draft.avatar) ? faceOf(wk.draft, "big")
      : isNew ? el("span", "ex-art big none plus", "+") : (() => { const s = el("span", "ex-art big none"); s.append(avatarMark(wk.draft)); return s; })());
    face.addEventListener("click", async () => {
      const a = await pickAvatar(wk.draft.avatar);
      if (a === undefined) return;
      if (a) wk.draft.avatar = a; else delete wk.draft.avatar;
      wk.dirty = true;
      paintFace();
      drawWorkerSave();
    });
    const acts = el("div", "ex-acts");
    // Each tab marks the window (data-tab) so the head can lay itself out the way that screen's design does.
    const mark = (box, key) => { wk.tab = key; const dl = box.closest("dialog"); if (dl) dl.dataset.tab = key; };
    const tabs = isNew ? [{ key: "edit", label: "새 전문가", render: (box) => { mark(box, "edit"); editBody(box, null); } }]
      : [{ key: "edit", label: "개요", render: (box) => { mark(box, "edit"); editBody(box, byId(wk.id)); } },
        { key: "test", label: "테스트", render: (box) => { mark(box, "test"); drawWorkerSave = baseSave; wk.bar = wk.okBtn = null; box.append(testPanel(byId(wk.id))); } },
        { key: "usage", label: "사용량", render: (box) => { mark(box, "usage"); wk.bar = wk.okBtn = null; usageTab(box, byId(wk.id)); } },
        { key: "history", label: "기록", render: (box) => { mark(box, "history"); wk.bar = wk.okBtn = null; historyTab(box, byId(wk.id)); } }];
    const pop = expertPopup.open({
      title: "", sub: "", avatar: face, tabs, initial: isNew ? "edit" : (wk.openTab || "edit"), actions: [acts], cls: `ex-pop${isNew ? " ex-new" : ""}`, popout: isNew ? null : { event: "arkism:open-expert", detail: { id: wk.id }, title: byId(wk.id)?.crew?.name || "" },
      onClose: () => { if (wk.pop !== pop) return; wk.pop = null; state.workerNew = false; wk.bar = wk.okBtn = wk.estPaint = null; if (isNew && !wk.keep) wk.draft = null; wk.keep = false; render(false); }
    });
    wk.pop = pop;
    wk.openTab = "edit";
    const names = pop.dialog.querySelector(".crew-popup-who > div");
    // 868:61909: the tabs sit in the head (a pill group).
    const headEl = pop.dialog.querySelector(".crew-popup-head");
    headEl.append(pop.dialog.querySelector(".crew-popup-tabs"));
    if (!isNew) {
      request(`/v1/crew/agents/${encodeURIComponent(rec.id)}/calls?${query({ limit: "30" })}`).then((r) => { wk.calls = r.items || []; wk.estPaint?.(); }, () => { wk.calls = []; });
    }
    // Closing with unsaved changes asks first (the shared frame's 닫기 button, 바깥 Esc).
    const guard = (e) => {
      if (!wk.dirty || wk.busy) return;
      e.preventDefault(); e.stopImmediatePropagation();
      void ask("저장하지 않은 변경이 있어요. 버리고 닫을까요?").then((yes) => { if (yes) { wk.dirty = false; pop.dialog.close(); } });
    };
    pop.dialog.addEventListener("cancel", guard);
    pop.dialog.querySelector(".crew-popup-actions > .btn:last-child, .crew-popup-actions > button:last-child")?.addEventListener("click", guard, true);
    const copyBtn = (d, cls) => {
      const copy = el("button", cls);
      copy.type = "button";
      copy.setAttribute("aria-label", "@이름 복사");
      copy.append(icon("copy"));
      copy.addEventListener("click", () => { navigator.clipboard?.writeText(`@${d.handle}`).then(() => notice("복사했어요."), () => {}); });
      return copy;
    };
    wk.paintHead = () => {
      const d = wk.draft;
      paintFace();
      const cat = el("button", "ex-cat");
      cat.type = "button";
      cat.setAttribute("aria-label", "카테고리");
      cat.append(...ctx.catChipBody(d.category));
      // A pick repaints the head, so the chip never shows an old choice.
      cat.addEventListener("click", () => ctx.catMenu(cat, { list: catsOf(agents()), current: d.category || "", onPick: (v) => {
        if (v) d.category = v; else delete d.category;
        wk.dirty = true; wk.paintHead(); drawWorkerSave();
      } }));
      const taken = (h) => agents().some((r) => r.id !== wk.id && r.crew.handle === h) || teams().some((r) => r.crew.handle === h);
      const line = el("div", "ex-line");
      const handle = inlineEdit("span", "ex-handle", () => (d.handle ? `@${d.handle}` : ""), (v) => { d.handle = v.replace(/^@/, ""); wk.dirty = true; drawWorkerSave(); wk.paintHead(); },
        { max: 41, ph: "@이름", validate: (v) => (!v.replace(/^@/, "") ? "@이름을 비울 수 없어요." : taken(v.replace(/^@/, "")) ? "이미 쓰는 이름이에요." : "") });
      // A new expert's @이름 follows its name until the @이름 itself is edited.
      const named = (v) => { if (isNew && (!d.handle || d.handle === wk.autoHandle)) { d.handle = wk.autoHandle = v.replace(/\s+/g, "").slice(0, 40); wk.paintHead(); } };
      line.append(inlineEdit("h2", "ex-name", () => d.name, (v) => { d.name = v; wk.dirty = true; named(v); drawWorkerSave(); }, { max: 60, ph: "이름", validate: (v) => (v ? "" : "이름을 비울 수 없어요.") }), handle, ...(isNew ? [] : [copyBtn(d, "icon-btn plain ex-copy")]));
      names.replaceChildren(cat, line, inlineEdit("p", "ex-role", () => d.role, (v) => { d.role = v; wk.dirty = true; drawWorkerSave(); }, { max: 200, ph: "한 줄 소개" }));
    };
    wk.paintAct = () => {
      acts.replaceChildren();
      if (isNew) return;
      acts.append(estimateNode(), switchEl(wk.draft.enabled !== false, "켜기", (on) => { wk.draft.enabled = on; wk.dirty = true; wk.paintAct(); drawWorkerSave(); })); // 전문가 삭제 sits at the end of 개요
    };
    wk.redraw = () => { const sc = wk.scrollKeep ?? pop.body.scrollTop; wk.scrollKeep = sc; pop.select(wk.tab); pop.body.scrollTop = sc; requestAnimationFrame(() => { pop.body.scrollTop = sc; wk.scrollKeep = null; }); }; // parts that fill in after the redraw would otherwise pull the page up a little on every click
    if (isNew) names.replaceChildren(el("h2", ""), el("p", ""));
    wk.paintHead();
    wk.paintAct();
  }
  const baseSave = drawWorkerSave;

  // 일별 호출 막대: by_day[].agent comes from /v1/crew/usage?kind=agent&agent_id= (the server adds agent/agent_failed per day); without it the bars stay empty.
  function dailyBars(id, tile) {
    const bars = el("span", "ex-bars");
    bars.setAttribute("aria-hidden", "true");
    tile.append(bars);
    request(`/v1/crew/usage?${query({ range: "30d", kind: "agent", agent_id: id })}`).then((u) => {
      const days = (u.by_day || []).slice(-30).map((x) => [Number(x.agent) || 0, Number(x.agent_failed) || 0]);
      const top = Math.max(0, ...days.map(([a, f]) => a + f));
      if (!top) { bars.remove(); return; } // no per-day agent counts from this server: no flat dotted line
      bars.replaceChildren(...days.map(([a, f]) => { const i = el("i", f ? "bad" : ""); i.style.height = `${Math.max(2, Math.round(((a + f) / top) * 28))}px`; return i; }));
    }, () => {});
  }
  const metric = (big, small) => { const t = el("div", "ex-metric"); t.append(el("strong", "", big), el("small", "", small)); return t; };
  const fmtTok = (n) => (n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e3 ? `${Math.round(n / 1e3)}K` : String(n));
  // AI 제안 · AI로 초안 (ai-chat.js): one button; the talk opens as a bubble out of it and proposals list under it
  // (nothing lands before 저장). fix proposes changes to the saved fields; draft fills a new one.
  function aiBox(mode) {
    const draft = mode === "draft";
    wk.ai ||= {};
    return window.ArkismAIChat.button({ state: wk.ai, label: draft ? "AI로 초안" : "AI 제안", title: draft ? "AI로 초안 만들기" : "AI 제안",
      sub: draft ? "필요한 동료를 말하면 초안을 채워요" : "바꾸고 싶은 걸 말하면 바뀌는 곳을 보여 줘요",
      greeting: draft ? "어떤 일을 맡길 동료가 필요하세요?" : "어떻게 바꿀까요? 지시·스킬·도구·생각 깊이를 바꿔 볼 수 있어요.",
      placeholder: draft ? "예: PR 올리기 전에 테스트 빠진 곳을 찾아 주는 동료" : "예: 배포 전에는 꼭 나한테 물어보게 해 줘",
      quick: draft ? ["코드 리뷰해 주는 동료", "회의록 정리하는 동료", "자료 찾아 요약하는 동료"] : ["배포 전엔 꼭 물어보게", "더 짧게 답하게", "필요한 스킬 골라 줘"],
      ask: draft ? draftWorker : proposeFix });
  }
  // One proposal per field: shown as 전→후, written into the draft on 적용 and back on 되돌리기.
  const DEP = { low: "빠르게", medium: "보통", high: "깊게" };
  function fieldProp(key, label, to, show = (v) => v) {
    const d = wk.draft, from = d[key] ?? (Array.isArray(to) ? [] : "");
    if (to === undefined || JSON.stringify(to) === JSON.stringify(from)) return null;
    const set = (v) => { wk.draft[key] = v; touchWorker(); };
    return { label, from: show(from), to: show(to), apply: () => set(to), undo: () => set(from) };
  }
  // 지시 칸의 반짝이: the same talk, about this one field (preset per field, ai-chat.js field()).
  function instructionsAI(t) {
    wk.instrAI ||= {};
    return window.ArkismAIChat.field(t, { state: wk.instrAI, title: "지시 다듬기", preset: "You edit the instructions of an AI work agent: who it is, tone, order of work, things not to do. Write Korean Markdown, at most 4000 characters, short and clear.",
      context: () => JSON.stringify({ name: wk.draft.name || "", role: wk.draft.role || "", skills: wk.draft.skills || [] }) });
  }
  async function proposeFix(ask) {
    const d = wk.draft;
    const names = new Set((state.data.skills || []).map((s) => s.name));
    const servers = new Set((ctx.toolServers?.() || []).map((s) => s.name));
    const prompt = `Edit a work agent definition. Request: ${ask}\n\nCurrent: ${JSON.stringify({ role: d.role || "", instructions: d.instructions || "", effort: d.effort || "medium", skills: d.skills || [], tools: d.tools || [] })}\n\nAnswer JSON with only the fields that should change, among role (<=200 chars), instructions (Korean Markdown, <=4000 chars), effort (low|medium|high), skills (list), tools (list).\nSkills available: ${[...names].slice(0, 200).join(", ") || "(none)"}\nMCP servers: ${[...servers].join(", ") || "(none)"}`;
    // json, not fill: the fill guide asks for a whole new agent, and the edit prompt describes its own shape
    const a = await kit.assist("json", prompt, { persona: { crew: { instructions: "You edit AI work agents and write in short, clear Korean." } } });
    if (a.status !== "done") throw new Error(kit.assistError(a));
    const got = kit.parseJSON(a.output || "") || {};
    const props = [
      typeof got.role === "string" && fieldProp("role", "소개", got.role.slice(0, 200)),
      typeof got.instructions === "string" && fieldProp("instructions", "지시", got.instructions.slice(0, 4000)),
      ["low", "medium", "high"].includes(got.effort) && fieldProp("effort", "생각 깊이", got.effort, (v) => DEP[v] || v || "보통"),
      Array.isArray(got.skills) && fieldProp("skills", "스킬", got.skills.map(String).filter((s) => names.has(s)).slice(0, 64)),
      Array.isArray(got.tools) && fieldProp("tools", "도구", got.tools.map(String).filter((s) => servers.has(s)).slice(0, 32))
    ].filter(Boolean);
    return { say: props.length ? `${props.map((p) => p.label).join("·")}을 바꾸자고 제안해요. 보라색 목록에서 바뀌는 곳을 보고 적용하세요.` : "바꿀 곳을 찾지 못했어요. 말을 바꿔 다시 물어봐 주세요.", props };
  }

  // 저장 · 취소 (868:61909 · 938:62634): always at the bottom; 저장 lights up once something changed.
  function saveBar(rec) {
    const bar = el("div", "ex-savebar");
    const ok = button("저장", "primary small", null, () => saveWorker(rec));
    bar.append(ok, button("취소", "ghost small", null, () => { wk.draft = structuredClone(rec.crew); wk.dirty = false; wk.ai = wk.instrAI = null; wk.paintHead(); wk.paintAct(); wk.redraw(); drawWorkerSave(); }));
    wk.bar = bar;
    wk.okBtn = ok;
    drawWorkerSave = baseSave;
    return bar;
  }
  // 개요 (868:61909): numbers, AI 제안, 모델, 실행, 일하는 방법, 쓸 수 있는 것, 예산, 채널, 삭제.
  // 새 전문가 (rec null): the same screen with empty fields — AI로 초안 and 비슷한 것에서 시작 in place of the numbers,
  // 팀에 넣기 in place of 채널, and 만들기 in place of 저장.
  function editBody(box, rec) {
    const d = wk.draft;
    box.classList.add("ex-body");
    if (rec) box.append(usageMetrics(rec));
    const ai = aiBox(rec ? "fix" : "draft"); // the button sits at the right end of the bottom bar; its purple list here
    box.append(ai);
    if (!rec) box.append(likeRow());
    const bar = rec ? saveBar(rec) : createBar();
    bar.append(ai.dock);
    box.append(modelCard(d), runCard(d),
      workerCard("일하는 방법", "", instructionsField(d), workflowField(d)),
      useCard(d, rec), budgetCard(rec ? rec.id : null, d), ...(rec ? [channelCard(rec), dangerRow(rec), bar] : [teamsCard(), bar]));
  }
  // 전문가 삭제: last thing in 개요, apart from everything else, and it asks for the name typed back.
  function dangerRow(rec) {
    const row = el("div", "ex-danger");
    const t = el("div", "");
    t.append(el("strong", "", "전문가 삭제"), el("small", "", "이 전문가와 설정을 지워요. 되돌릴 수 없어요."));
    row.append(t, button("전문가 삭제", "danger small", "trash", () => removeWorker(rec)));
    return row;
  }

  // 테스트 (868:62591): what was asked on the right, the expert's answer under its face, the line to type at the bottom.
  function testPanel(rec) {
    const box = el("div", "ex-test");
    const log = el("ol", "ex-test-log");
    log.setAttribute("aria-live", "polite");
    const turns = (wk.testLog ||= []);
    const paint = () => {
      log.replaceChildren(...turns.flatMap((t) => {
        const me = el("li", "ex-test-me", t.q);
        const ai = el("li", `ex-test-ai${t.busy ? " busy" : ""}${t.err ? " err" : ""}`);
        const who = el("div", "ex-test-who");
        const face = faceOf(rec.crew, "small");
        who.append(face, raw(el("strong", "", rec.crew.name)), raw(el("small", "", [rec.crew.role, rec.crew.model].filter(Boolean).join(" · "))));
        const text = el("div", "ex-test-text", t.a);
        ai.append(who, text);
        if (!t.busy && t.secs) ai.append(raw(el("small", "ex-test-meta", tr(`${t.secs}초`))));
        return [me, ai];
      }));
      if (!turns.length) log.append(el("li", "ex-test-empty", "물어볼 말을 보내면 저장된 설정으로 답해요."));
      log.scrollTop = log.scrollHeight;
    };
    paint();
    const q = input("", { maxLength: 4000, placeholder: tr(`@${rec.crew.handle}에게 맡길 일…`) });
    q.setAttribute("aria-label", "테스트 질문");
    const send = button("실행", "primary", null, async () => {
      if (!q.value.trim()) { q.focus(); return; }
      if (wk.dirty) { notice("저장하지 않은 변경이 있어요. 테스트는 저장된 설정으로 답하니 먼저 저장해 주세요."); return; }
      send.disabled = true;
      const t = { q: q.value, a: "답을 기다리는 중…", busy: true };
      turns.push(t);
      q.value = "";
      paint();
      const t0 = Date.now();
      try {
        const a = await kit.assist("try", t.q, { expert: rec.crew.handle, persona: { crew: { instructions: rec.crew.instructions || "" } } });
        if (a.status === "done") t.a = a.output || "(빈 답)"; else { t.a = kit.assistError(a); t.err = true; }
      } catch (e) { t.a = e.message; t.err = true; }
      t.busy = false;
      t.secs = Math.max(1, Math.round((Date.now() - t0) / 1000));
      if (box.isConnected) paint();
      send.disabled = false;
    });
    q.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.isComposing) { e.preventDefault(); send.click(); } });
    const bar = el("div", "ex-test-bar");
    const hist = el("button", "ex-link", "실행 기록 보기");
    hist.type = "button";
    hist.addEventListener("click", () => { wk.tab = "history"; wk.redraw(); });
    bar.append(q, send, hist);
    box.append(el("p", "ex-test-note", "여기서 보낸 요청도 실제 실행이에요. 실행 기록과 이번 달 한도에 남아요."), log, bar);
    return box;
  }

  // What differs between two saved versions of an expert, in the person's words (기록 › 판 이력).
  function agentChanges(a, b) {
    const out = [];
    const s = (v) => v || "없음";
    if ((a.name || "") !== (b.name || "")) out.push(`이름 ${s(a.name)} → ${s(b.name)}`);
    if ((a.handle || "") !== (b.handle || "")) out.push(`@이름 ${s(a.handle)} → ${s(b.handle)}`);
    if ((a.role || "") !== (b.role || "")) out.push("소개 바꿈");
    if ((a.category || "") !== (b.category || "")) out.push(`카테고리 ${s(a.category)} → ${s(b.category)}`);
    if ((a.instructions || "") !== (b.instructions || "")) out.push("지시 바꿈");
    if ((a.model || "") !== (b.model || "")) out.push(`기본 모델 ${s(a.model)} → ${s(b.model)}`);
    if ((a.fallback_model || "") !== (b.fallback_model || "")) out.push(`예비 모델 ${s(a.fallback_model)} → ${s(b.fallback_model)}`);
    if ((a.effort || "medium") !== (b.effort || "medium")) out.push("생각 깊이 바꿈");
    if ((a.workflow || "") !== (b.workflow || "")) out.push(`일하는 순서 ${s(a.workflow)} → ${s(b.workflow)}`);
    for (const n of b.skills || []) if (!(a.skills || []).includes(n)) out.push(`스킬 ${n} 추가`);
    for (const n of a.skills || []) if (!(b.skills || []).includes(n)) out.push(`스킬 ${n} 뺌`);
    for (const n of b.tools || []) if (!(a.tools || []).includes(n)) out.push(`도구 ${n} 추가`);
    for (const n of a.tools || []) if (!(b.tools || []).includes(n)) out.push(`도구 ${n} 뺌`);
    if ((a.budget?.monthly_usd || 0) !== (b.budget?.monthly_usd || 0)) out.push(`한 달 한도 $${a.budget?.monthly_usd || 0} → $${b.budget?.monthly_usd || 0}`);
    if ((a.budget?.ask_over_usd || 0) !== (b.budget?.ask_over_usd || 0)) out.push(`1회 한도 $${a.budget?.ask_over_usd || 0} → $${b.budget?.ask_over_usd || 0}`);
    if (JSON.stringify(a.pinned || []) !== JSON.stringify(b.pinned || [])) out.push("고정한 것 바꿈");
    if (JSON.stringify(a.agents || []) !== JSON.stringify(b.agents || [])) out.push("부르는 곳 바꿈");
    if (JSON.stringify(a.memory || {}) !== JSON.stringify(b.memory || {})) out.push("기억 종류 바꿈");
    if ((a.avatar?.art || "") !== (b.avatar?.art || "")) out.push("아바타 바꿈");
    return out;
  }
  const stamp = (at) => { const t = new Date(at); const p = (n) => String(n).padStart(2, "0"); return isNaN(t) ? "" : `${p(t.getMonth() + 1)}-${p(t.getDate())} ${p(t.getHours())}:${p(t.getMinutes())}`; };
  const CALL_FROM = { manual: "직접", test: "테스트", discord: "디스코드", claude_code: "Claude Code", codex: "Codex", zcode: "ZCode", app: "arkism 앱", schedule: "일정", webhook: "웹훅", meeting: "회의" };
  // 기록 (868:63273): 판 이력 (GET /v1/crew/versions/{id}) and 최근 실행 (its calls and the runs of the workflow it works by).
  // 최근 30일 실행 · 전체 토큰 · 추정 비용 (개요 and 사용량 tabs).
  function usageMetrics(rec) {
    const use = (wk.usage?.agents || []).find((a) => a.id === rec.id);
    const m = el("div", "ex-metrics");
    const runsTile = metric(`${use?.calls || 0}회`, "최근 30일 실행");
    m.append(runsTile, metric(fmtTok(use?.tokens || 0), "전체 토큰"), metric(use ? `$${Number(use.cost_usd || 0).toFixed(2)}` : "$0.00", "추정 비용"));
    dailyBars(rec.id, runsTile);
    return m;
  }
  const histSec = (title, sub, body) => { const s = el("section", "ex-hsec"); const h = el("div", "ex-hsec-head"); h.append(el("h3", "", title), el("small", "", sub)); s.append(h, body); return s; };
  // 사용량 (from Usage › 전문가): the numbers, then every call with its model, tokens, time and cost.
  function usageTab(box, rec) {
    box.classList.add("ex-body");
    box.append(usageMetrics(rec), histSec("사용 기록", "부를 때마다 한 줄씩 남아요", runsBox(rec)));
  }
  function historyTab(box, rec) {
    box.classList.add("ex-body");
    const sec = histSec;
    const vbox = el("div", "tm-hist");
    if (wk.versions === undefined) {
      wk.versions = null;
      request(`/v1/crew/versions/${encodeURIComponent(rec.id)}?${query()}`).then((r) => { wk.versions = r.items || []; }, () => { wk.versions = []; }).then(() => { if (wk.tab === "history") wk.redraw(); });
    }
    const list = wk.versions || [];
    list.forEach((h, i) => {
      const cur = h.revision === rec.revision;
      const r = el("div", "tm-hist-row");
      const sum = list[i + 1] ? agentChanges(list[i + 1].crew, h.crew).slice(0, 2).join(" · ") || "저장" : h.revision === 1 ? "처음 만듦" : "처음 기록";
      r.append(el("b", cur ? "cur" : "", `v${h.revision}`), el("time", "", stamp(h.at)), raw(el("span", "", sum.split(" · ").map((x) => tr(x)).join(" · "))));
      const acts = el("span", "tm-hist-acts");
      if (cur) acts.append(el("em", "", "지금 버전"));
      else {
        const see = el("button", "tm-link quiet", wk.histOpen.has(h.revision) ? "접기" : "바뀐 점 보기"); see.type = "button"; see.setAttribute("aria-expanded", String(wk.histOpen.has(h.revision)));
        see.addEventListener("click", () => { if (!wk.histOpen.delete(h.revision)) wk.histOpen.add(h.revision); wk.redraw(); });
        const back = el("button", "tm-link plain", "이 버전으로 변경"); back.type = "button";
        back.addEventListener("click", async () => {
          if (!await ask(`v${h.revision} 버전으로 변경할까요? 새 버전으로 저장돼요.`)) return;
          wk.draft = structuredClone(h.crew); wk.dirty = true; wk.versions = undefined;
          await saveWorker(rec);
        });
        acts.append(see, back);
      }
      r.append(acts);
      vbox.append(r);
      // what changing to this version would change: now (-) against that version (+)
      if (wk.histOpen.has(h.revision)) vbox.append(window.ArkismVersionDiff.render(el, list[0].crew, h.crew, `v${h.revision} 버전으로 바꾸면`));
    });
    if (!list.length) vbox.append(el("p", "ex-hist-empty", wk.versions ? "버전 기록이 아직 없어요." : "불러오는 중…"));
    box.append(sec("버전 기록", "저장할 때마다 버전이 하나 생겨요. 다른 버전으로 바꿔도 지금 버전은 남아요.", vbox), sec("최근 실행", "테스트 탭에서 보낸 것도 여기 남아요", runsBox(rec)));
  }
  // Runs of its workflow and its own calls, newest first.
  function runsBox(rec) {
    const rbox = el("div", "tm-hist");
    rbox.append(el("p", "ex-hist-empty", "불러오는 중…"));
    const wf = allWorkflows().find((w) => w.crew.handle === (rec.crew.workflow || "").replace(/^\$/, ""));
    const runsP = wf ? request(`/v1/crew/runs?${query({ crew_id: wf.id, limit: "30" })}`).then((r) => r.items || [], () => []) : Promise.resolve([]);
    const callsP = request(`/v1/crew/agents/${encodeURIComponent(rec.id)}/calls?${query({ limit: "30" })}`).then((r) => r.items || [], () => []);
    Promise.all([runsP, callsP]).then(([runs, calls]) => {
      const rows = [...runs.map((x) => ({ at: String(x.created_at), x })), ...calls.map((c) => ({ at: String(c.at), c }))].sort((a, b) => b.at.localeCompare(a.at));
      if (!rows.length) { rbox.replaceChildren(el("p", "ex-hist-empty", "아직 실행 기록이 없어요.")); return; }
      rbox.replaceChildren(...rows.slice(0, 50).map(({ x, c }) => {
        const row = el("div", "tm-hist-row");
        if (c) {
          const cost = Number(c.cost_usd) || 0;
          const secs = c.latency_ms > 0 ? tr(`${Math.max(1, Math.round(c.latency_ms / 1000))}초`) : "";
          row.append(el("b", c.ok ? "ok" : "warn", c.ok ? "✓" : "!"), el("time", "", stamp(c.at)), el("i", "tm-hist-src", CALL_FROM[c.trigger] || c.trigger || "직접"),
            raw(el("span", "", `${c.model || "-"} · ${fmtTok((c.input_tokens || 0) + (c.output_tokens || 0))} ${tr("토큰")}`)),
            raw(el("small", "", c.ok ? [secs, `$${cost.toFixed(2)}`].filter(Boolean).join(" · ") : [tr("멈춤"), c.error].filter(Boolean).join(" · "))));
          return row;
        }
        const run = x.crew_run || {};
        let said = "";
        try { said = JSON.parse(run.input || "{}").request || ""; } catch { said = run.input || ""; }
        const ok = run.status === "succeeded";
        row.append(el("b", ok ? "ok" : "warn", ok ? "✓" : "!"), el("time", "", stamp(x.created_at)), el("i", "tm-hist-src", CALL_FROM[run.trigger] || run.trigger || "직접"),
          raw(el("span", "", (said || run.summary || tr("(입력 없음)")).slice(0, 120))), el("small", "", RUN_LABEL[run.status] || run.status || ""));
        return row;
      }));
    }).catch(() => rbox.replaceChildren(el("p", "ex-hist-empty", "실행 기록을 불러오지 못했어요.")));
    return rbox;
  }

  // 새 전문가: start from an existing expert (a copy) instead of empty fields.
  function likeRow() {
    const d = wk.draft;
    const like = toggleChips([["", "빈 칸에서"], ...agents().map((r) => [r.id, r.crew.name])], (k) => (d.copy_of || "") === k, (k) => {
      const src = k ? byId(k)?.crew : null;
      const keepTeams = wk.teamsPick;
      wk.draft = src ? { ...structuredClone(src), name: `${src.name} 복사`.slice(0, 60), handle: `${src.handle || ""}2`.slice(0, 40), copy_of: k } : blankAgent();
      wk.teamsPick = keepTeams;
      wk.dirty = !!src;
      wk.paintHead();
      wk.redraw();
    });
    const r = el("div", "ex-like");
    r.append(el("span", "wk-label", "비슷한 것에서 시작"), like);
    return r;
  }
  // 팀에 넣기 (선택): teams the new expert joins once made, or a new team made with it.
  function teamsCard() {
    const teamRow = el("div", "wk-chips");
    for (const t of teams()) {
      const on = wk.teamsPick.has(t.id);
      const b = el("button", `wk-chip${on ? " on" : ""}`, t.crew.name);
      b.type = "button";
      b.setAttribute("aria-pressed", String(on));
      b.addEventListener("click", () => { if (on) wk.teamsPick.delete(t.id); else wk.teamsPick.add(t.id); wk.redraw(); });
      teamRow.append(b);
    }
    for (const t of wk.newTeams || []) {
      const b = el("button", "wk-chip on", `${t.name} · 새 팀`);
      b.type = "button";
      b.setAttribute("aria-pressed", "true");
      b.setAttribute("aria-label", `${t.name} 새 팀 빼기`);
      b.addEventListener("click", () => { wk.newTeams = wk.newTeams.filter((x) => x !== t); wk.redraw(); });
      teamRow.append(b);
    }
    const nt = el("button", "wk-chip", "+ 새 팀");
    nt.type = "button";
    nt.addEventListener("click", () => newTeamPopup());
    teamRow.append(nt);
    return workerCard("팀에 넣기 (선택)", "", el("small", "wk-label", teams().length ? "만든 뒤 팀 화면에서도 넣을 수 있어요" : "아직 팀이 없어요. 새 팀을 만들면 거기서 넣어요."), teamRow);
  }
  // 만들기 · 취소: 취소 asks before throwing away what was typed.
  function createBar() {
    const bar = el("div", "ex-savebar always");
    const ok = button("만들기", "primary small", null, () => saveWorker(null));
    bar.append(ok, button("취소", "ghost small", null, async () => { if (wk.dirty && !await ask("쓰던 내용을 버릴까요?")) return; wk.pop.dialog.close(); }), el("small", "ex-hint", "만들면 바로 @이름으로 부를 수 있어요"));
    wk.bar = null;
    wk.okBtn = ok;
    drawWorkerSave = baseSave;
    return bar;
  }

  // + 새 팀 (310:19277): name and @이름 only. A team needs at least one member, so it is created right after this
  // expert, with the expert as its worker (round_robin); the team screen edits the rest.
  function newTeamPopup() {
    const dlg = el("dialog", "crew-dialog ex-skill-dlg");
    const head = el("div", "dialog-head");
    head.append(el("h2", "", "새 팀"));
    const body = el("div", "dialog-body");
    const name = input("", { maxLength: 60, placeholder: "예: 릴리스 팀" });
    const handle = input("", { maxLength: 40, pattern: "[\\p{L}\\p{N}_\\-]{1,40}", placeholder: "띄어쓰기 없이" });
    name.addEventListener("input", () => { if (!handle.dataset.touched) handle.value = name.value.replace(/\s+/g, "").slice(0, 40); });
    handle.addEventListener("input", () => { handle.dataset.touched = handle.value ? "1" : ""; });
    body.append(field("이름", name), field("@이름", handle, "만들기를 누르면 이 전문가를 넣은 팀이 같이 만들어져요"));
    const foot = el("div", "dialog-foot");
    foot.append(button("취소", "ghost", null, () => dlg.close()), button("추가", "primary", null, () => {
      const n = name.value.trim(), h = handle.value.trim().replace(/^@/, "");
      if (!n) { window.ArkismNeed(name, "이름을 적어 주세요."); return; }
      if (!/^[\p{L}\p{N}_-]{1,40}$/u.test(h)) { window.ArkismNeed(handle, "@이름은 띄어쓰기 없이 글자·숫자·_·-로 적어 주세요."); return; }
      if (h === wk.draft.handle || agents().some((r) => r.crew.handle === h) || teams().some((r) => r.crew.handle === h) || (wk.newTeams || []).some((t) => t.handle === h)) { notice("이미 쓰는 이름이에요."); return; }
      wk.newTeams = [...(wk.newTeams || []), { name: n, handle: h }];
      dlg.close();
      wk.redraw();
    }));
    dlg.append(head, body, foot);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal();
    name.focus();
  }

  const pageSub = () => {
    if (!ed.rec) return "";
    const x = ed.rec.crew;
    if (ed.run) return `$${x.handle} · ${RUN_LABEL[ed.run.crew_run?.status] || ""}`;
    return `워크플로우 › ${x.name} · $${x.handle} · ${ed.dirty ? "저장하지 않은 변경이 있어요" : "변경은 저장을 눌러야 남아요"}`;
  };

  return {
    explain, assist: (...a) => kit.assist(...a), assistError: (a) => kit.assistError(a), parseJSON: (t) => kit.parseJSON(t),
    runWorkflow: (rec, text) => startRun(rec, text),
    skillExperts: () => null,
    openWorkers,
    load: () => request(`/v1/crew?${query()}`).then((r) => {
      // ?expert=<id> opens that expert's popup once, right after the first load (Figma 843:59277).
      const want = !wk.urlDone && new URLSearchParams(location.search).get("expert");
      wk.urlDone = true;
      if (want) setTimeout(() => { if (byId(want)?.crew.type === "agent") openWorkers(want); }, 400);
      return r.items || [];
    }, () => []),
    count: () => workflows().length,
    settingsTab: personaTab,
    render: renderList,
    detail: renderDetail,
    head: headActions,
    views: {
      crew: { bare: true, title: "자동화", count: () => `${workflows().length}개`, sub: () => { const on = workflows().filter((r) => triggered(r.crew)).length; return `단계가 정해진 워크플로우예요. 에이전트가 일하는 방법으로 쓰거나 $이름으로 불러요. ${on ? `일정·웹훅은 ${on}개 켜져 있어요.` : "켜진 일정·웹훅은 없어요."}`; } }
    },
    pages: {
      workers: { title: () => "전문가", count: () => `${agents().length}명`,
        sub: () => "",
        bare: () => true, head: () => [], render: renderWorkers },
      graph: { title: () => (rerunOf() ? "실패한 곳부터 재실행" : ed.rec ? `${ed.rec.crew.name} · ${ed.run ? "실행" : "워크플로우 편집"}` : "워크플로우 편집"), sub: pageSub, headInTitle: true, head: graphHead, render: renderGraph }
    },
    // exported for tests in a browser console
    _test: { parseCron, nextRun, cronText, levels, wouldCycle, cliOf, ruleChecks, simpleNode, portsOf }
  };
};
