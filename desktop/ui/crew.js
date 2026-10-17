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
  const { state, $, el, icon, button, segment, render, request, query, notice, detailShell, section, copyButton, markSelected, openDetail, ago, models } = ctx;
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
    ai: { label: "AI 에이전트", sub: "도구를 쓰며 스스로 일해요", icon: "robot", group: "AI" },
    ai_once: { label: "AI 한 번 호출", sub: "질문 한 번 · 빠르고 싸요", icon: "sparkle", group: "AI" },
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
    end: { label: "보고하고 끝", sub: "부른 자리에 결과 돌려주기", icon: "flag", group: "알림·끝" }
  };
  const IS_AI = (t) => t === "ai" || t === "ai_once";
  const PORTS = { condition: [["yes", "예"], ["no", "아니오"]], approval: [["approve", "승인"], ["reject", "거절"]], code: [["pass", "통과"], ["fail", "실패"]] };
  const ACCESS = [["read", "읽기만 (동시에 돌 수 있어요)"], ["write", "쓰기 (차례로 돌아요)"]];
  // Settings of the non-AI nodes: [key, label, kind, options]. Keys are what the run engine reads (contract.md).
  const FIELDS = {
    start: [], merge: [],
    schedule: [["cron", "언제 (분 시 일 월 요일)", "text", "0 9 * * 1-5"]],
    webhook: [["note", "메모", "text", "주소와 토큰은 서버에서 arkism-crew hook-url로 받아요"]],
    code: [["lang", "언어", "select", [["go", "Go"], ["sh", "셸"]]], ["code", "코드 (stdin JSON → stdout JSON)", "code"], ["timeout", "최대 실행 시간 (기본 30s, 최대 2h)", "text", "30s"]],
    http: [["method", "방식", "select", [["GET", "GET"], ["POST", "POST"], ["PUT", "PUT"], ["PATCH", "PATCH"], ["DELETE", "DELETE"]]], ["url", "주소", "text", "https://"], ["headers", "헤더 (한 줄에 하나)", "area"], ["body", "본문", "area"], ["key", "쓸 키 이름 (모델 설정에 저장한 키)", "text"]],
    github: [["args", "gh 인자", "text", "pr create --fill"], ["repo_dir", "작업 폴더", "text", "/srv/…"]],
    approval: [["question", "물어볼 것", "area"]],
    condition: [["expr", "조건 (예: in.tests.failed == 0 and in.risky == false)", "text"]],
    loop: [["path", "목록 위치", "text", "in.items"], ["concurrency", "동시에 도는 개수", "text", "1"]],
    wait: [["duration", "기다릴 시간 (예: 30m, 2h)", "text", "30m"]],
    subflow: [["workflow", "부를 워크플로우", "workflow"], ["task", "넘길 일 ({{input}}에 앞 결과가 들어가요)", "area"]],
    notify: [["channel", "보내는 곳", "select", [["telegram", "텔레그램"], ["slack", "슬랙"], ["email", "메일"]]], ["to", "받는 곳 이름 (서버 notify.json)", "text"], ["message", "보낼 말", "area"]],
    end: [["message", "보고 형식", "text", "요약 + 링크"]]
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
    if (!confirm(`'${name}' 워크플로우를 지울까요? 되돌릴 수 없어요. (지난 실행 기록은 남아요)`)) return false;
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
    if (!confirm(`'${rec.crew?.name || "이 워크플로우"}'를 보관할까요? 목록에서 사라지고 일정·웹훅도 멈춰요. 실행 기록은 남아요.`)) return;
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
  const modelOf = (n, x) => n.config?.model || x?.model || "기본 모델";
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
    for (let qi = 0; qi < queue.length; qi++) {
      const id = queue[qi];
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
  async function startRun(rec, text, only, nodesFrom) {
    const g = rec.crew.graph;
    if (!stepCount(g)) { notice("아직 할 일이 없어요. 편집에서 노드를 넣어 주세요."); return null; }
    const nodes = Object.fromEntries((g.nodes || []).map((n) => [n.id, { status: "pending" }]));
    let loops;
    if (nodesFrom) {
      for (const [id, st] of Object.entries(nodesFrom.nodes || {})) if (nodes[id] && st.status === "done") nodes[id] = { status: "done", output: st.output, port: st.port, runs: st.runs };
      loops = nodesFrom.loops;
    }
    const run = { crew_id: rec.id, crew_revision: rec.revision, trigger: "manual", status: "running", input: runInput(text, only), nodes, ...(only ? { only } : {}), ...(loops ? { loops } : {}) };
    try {
      const r = await request("/v1/crew/runs", { method: "POST", body: JSON.stringify({ scope: state.scope, run }) });
      notice(only ? "이 노드만 실행을 맡겼어요. 최근 실행에서 볼 수 있어요." : "실행을 맡겼어요. 서버 실행기가 10초 안에 시작해요.");
      return r;
    } catch (e) {
      if (nodesFrom) return startRun(rec, text, only);
      notice(`실행하지 못했어요 (${e.message}).`);
      return null;
    }
  }
  const RUN_LABEL = { succeeded: "완료", failed: "실패", waiting: "확인 대기", running: "도는 중", stopped: "멈춤" };
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
  function modelInput(value, placeholder = "deepseek-v4-flash") {
    const i = input(value, { maxLength: 80, placeholder });
    i.setAttribute("list", "crew-models");
    return i;
  }
  function modelList() {
    const dl = el("datalist");
    dl.id = "crew-models";
    // "auto" lets the router pick per run; the rest is the live list the runner reports (모델 설정), never a fixed one.
    const auto = el("option");
    auto.value = "auto";
    auto.label = "자동 — 라우터가 일마다 골라요";
    dl.append(auto);
    for (const m of new Set(models?.enabledModels() || [])) { const o = el("option"); o.value = m; dl.append(o); }
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
    if (/^(opus|sonnet|haiku|fable|claude)/.test(m)) {
      const img = el("img");
      img.src = "img/claude.svg";
      img.alt = "";
      box.append(img);
      return box;
    }
    const ref = /^(gpt|o\d|codex)/.test(m) ? "a-codex" : m.startsWith("gemini") ? "a-gemini" : "";
    if (!ref) { box.append(icon("robot")); return box; }
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("aria-hidden", "true");
    const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
    use.setAttribute("href", `#${ref}`);
    svg.append(use);
    box.append(svg);
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
  // The latest runs of every workflow, for 최근 실행 under the cards and the approvals card on top.
  let recent = null;
  let recentBusy = false;
  let recentAll = false;
  let waiting = [];
  async function loadRecent() {
    if (recentBusy) return;
    recentBusy = true;
    // Waiting runs come on their own (status=waiting): an approval older than the latest 30 runs still asks.
    const [all, wait] = await Promise.all([
      request(`/v1/crew/runs?${query({ limit: 30 })}`).then((r) => r.items || [], () => null),
      request(`/v1/crew/runs?${query({ status: "waiting", limit: 100 })}`).then((r) => r.items || [], () => null)]);
    recent = all || recent || [];
    byStatus = { key: "", items: null }; // a status filter reads again too
    waiting = wait || [];
    runsOf.clear();
    recentBusy = false;
    if (state.view === "crew") { drawCards(); drawRecent(); drawWaiting(); }
  }
  // One workflow's own history: the shared list above only holds the latest 30 runs of all workflows.
  const runsOf = new Map();
  async function loadRunsOf(id) {
    if (runsOf.get(id) === "busy") return;
    runsOf.set(id, "busy");
    try { runsOf.set(id, (await request(`/v1/crew/runs?${query({ crew_id: id, limit: 30 })}`)).items || []); } catch (_) { runsOf.set(id, []); }
    if (state.view === "crew") drawRecent();
  }
  const waitingRuns = () => {
    const seen = new Set();
    return [...waiting, ...(recent || [])].filter((x) => x.crew_run?.status === "waiting" && x.crew_run.waiting?.kind === "approval" && !seen.has(x.id) && seen.add(x.id));
  };
  const collabWorkers = (g) => aiNodes(g).filter((a) => a.config?.collab === "on").reduce((s, a) => s + (Number(a.config.max_workers) || 5), 0);
  function categories(list) {
    const counts = new Map();
    list.forEach((r) => { if (r.crew.category) counts.set(r.crew.category, (counts.get(r.crew.category) || 0) + 1); });
    return [...counts.entries()].sort((a, b) => a[0].localeCompare(b[0], "ko"));
  }
  const inTab = (r, tab) => tab === "all" || (tab === "trigger" ? triggered(r.crew) : !triggered(r.crew));
  const shownCards = () => workflows().filter((r) => inTab(r, state.crewTab) && (state.crewCat === "all" || r.crew.category === state.crewCat) &&
    (!state.query || `${r.crew.name} ${r.crew.handle || ""} ${r.crew.role || ""}`.toLowerCase().includes(state.query)));
  function renderList() {
    if (!TABS.some(([k]) => k === state.crewTab)) state.crewTab = "all";
    const tab = state.crewTab;
    const all = workflows().filter((r) => inTab(r, tab));
    const cats = categories(all);
    if (state.crewCat !== "all" && !cats.some(([c]) => c === state.crewCat)) state.crewCat = "all";
    // 보기 · 검색 · 카테고리: one row (the search box here and the one on top share state.query)
    const bar = el("div", "wf-bar");
    bar.append(segment(TABS.map(([k, l]) => [k, l, workflows().filter((r) => inTab(r, k)).length]), tab, (k) => { state.crewTab = k; state.crewCat = "all"; state.selected = null; render(); }), el("span", "grow"));
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
    bar.append(search);
    if (cats.length) {
      const cat = el("select", "wf-select");
      cat.setAttribute("aria-label", "카테고리");
      for (const [k, label] of [["all", "모든 카테고리"], ...cats.map(([c, n]) => [c, `${c} · ${n}`])]) {
        const o = el("option", "", label);
        o.value = k;
        o.selected = k === state.crewCat;
        cat.append(o);
      }
      cat.addEventListener("change", () => { state.crewCat = cat.value; drawCards(); });
      bar.append(cat);
    }
    $("#controls").replaceChildren(bar);
    $("#chips").replaceChildren();
    $("#stats").replaceChildren();
    const main = $("#table");
    main.className = "wf-main";
    const recentBox = el("section", "wf-recent");
    main.replaceChildren(el("div", "wf-grid"), recentBox);
    drawWaiting();
    drawCards();
    drawRecent();
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
  // Card (198:286): category chip and the 일정·웹훅 switch on top, name, one line, then $이름 · 단계 · 일정.
  // A cover photo (배경 사진) sits as a band on top of the card.
  function workflowCard(r) {
    const x = r.crew;
    const c = el("button", `crew-card workflow wf-card${x.cover?.url ? " has-cover" : ""}${state.selected === `crew:${r.id}` ? " selected" : ""}`);
    c.type = "button";
    c.dataset.id = `crew:${r.id}`;
    c.addEventListener("click", () => { state.selected = `crew:${r.id}`; markSelected(); renderDetail(); openDetail(); drawRecent(); });
    if (x.cover?.url) {
      const img = el("img", "crew-cover-img");
      img.alt = "";
      img.loading = "lazy";
      img.src = x.cover.url;
      img.addEventListener("error", () => img.remove());
      c.append(img);
    }
    const top = el("div", "crew-cover-top");
    if (x.category) top.append(el("span", "wf-chip", x.category));
    if (x.trigger) top.append(switchEl(x.enabled, `${x.name} 일정·웹훅 켜기`, (on) => quickSave(r, { enabled: on }, on ? "일정·웹훅을 켰어요." : "일정·웹훅을 껐어요.")));
    const n = stepCount(x.graph);
    const workers = collabWorkers(x.graph);
    const waits = waitingRuns().filter((w) => w.crew_run.crew_id === r.id).length;
    const meta = [`$${x.handle}`, n === 1 ? "단계 1개" : `${n}단계`, workers ? `협업 워커 ${workers}명` : "", waits ? `승인 ${waits}건 필요` : ""].filter(Boolean).join(" · ");
    const when2 = x.trigger ? (x.enabled ? (x.trigger.kind === "schedule" ? cronText(x.trigger.cron) : triggerText(x)) : `${triggerText(x)} · 꺼짐`) : "수동 실행";
    const foot = el("div", "crew-cover-foot");
    foot.append(el("small", "wf-card-meta", meta), el("small", `wf-card-when${triggered(x) ? " on" : ""}`, when2));
    c.append(top, el("strong", "crew-cover-title", x.name), el("p", "wf-card-role", x.role || ""), el("span", "grow"), foot);
    return c;
  }
  function newCard() {
    const c = el("button", "crew-card new workflow wf-card");
    c.type = "button";
    c.append(icon("plus"), el("strong", "", "새 워크플로우"), el("small", "", "AI 하나로 간단히 시작하거나 단계를 이어 그려요"));
    c.addEventListener("click", openCreate);
    return c;
  }
  const headActions = () => [button("새 워크플로우", "primary", null, openCreate)];

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
  function openCreate() {
    dialog?.remove();
    dialog = el("dialog", "crew-dialog wf-dialog wf-create");
    const form = el("form");
    form.method = "dialog";
    const head = el("div", "dialog-head");
    const close = el("button", "icon-btn plain");
    close.value = "cancel";
    close.setAttribute("aria-label", "닫기");
    close.append(icon("x"));
    head.append(el("h2", "", "새 워크플로우"), close);
    const pick = el("div", "crew-choice big wf-choice");
    const opt = (l, sub, fn) => {
      const b = el("button");
      b.type = "button";
      b.append(el("strong", "", l), el("small", "", sub));
      b.addEventListener("click", fn);
      return b;
    };
    pick.append(opt("AI로 만들기", "하고 싶은 일을 말하면 AI가 물어보고 그래프를 그려요.", () => { dialog.remove(); openBuildChat(); }),
      opt("직접 만들기", "바로 그래프 편집으로 가요. 제목은 나중에 AI가 채워요.", () => { dialog.remove(); void createBlank(); }));
    const more = button("템플릿·설정으로 만들기", "ghost small", null, () => { dialog.remove(); openSettings(null); });
    more.type = "button";
    const body = el("div", "dialog-body");
    body.append(pick, more);
    form.append(head, body);
    dialog.append(form);
    dialog.addEventListener("close", () => dialog.remove());
    document.body.append(dialog);
    dialog.showModal();
  }
  function freeHandle(name) {
    const h = name === UNTITLED ? "" : name.replace(/[^\p{L}\p{N}_-]/gu, "").slice(0, 40);
    return h && !workflows().some((r) => r.crew.handle === h) ? h : untitledHandle();
  }
  async function createBlank(graph, name = UNTITLED, role = "") {
    try {
      const out = { type: "workflow", name, handle: freeHandle(name), role, category: "", model: "", enabled: false,
        graph: graph || defaultGraph("blank", {}) };
      const saved = await save(null, out);
      await reload(saved.id);
      openGraph(byId(saved.id) || saved, { graphMode: true });
      return true;
    } catch (e) {
      notice(errorText(e));
      return false;
    }
  }
  function openBuildChat() {
    dialog?.remove();
    const box = (dialog = el("dialog", "crew-dialog wf-dialog wf-build"));
    const form = el("form");
    const head = el("div", "dialog-head");
    const close = el("button", "icon-btn plain");
    close.type = "button";
    close.setAttribute("aria-label", "닫기");
    close.append(icon("x"));
    close.addEventListener("click", () => dialog.close());
    head.append(el("h2", "", "AI로 만들기"), close);
    const log = el("div", "wf-build-log");
    const talk = [];
    const say = (who, text) => { talk.push([who, text]); log.append(el("p", who === "AI" ? "wf-build-ai" : "wf-build-me", text)); log.scrollTop = log.scrollHeight; };
    say("AI", "어떤 일을 자동으로 하고 싶어요? 언제 시작하고, 무엇을 받아서, 결과를 어디로 보낼지 알려 주세요.");
    const q = el("textarea");
    q.rows = 3;
    q.maxLength = 2000;
    q.placeholder = "예: 매일 아침 GitHub PR을 모아 요약해서 슬랙으로 보내 줘";
    const send = button("보내기", "primary", "paper-plane");
    send.type = "submit";
    const err = el("p", "form-error");
    err.setAttribute("role", "alert");
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      const text = q.value.trim();
      if (!text) return;
      q.value = "";
      say("나", text);
      send.disabled = true;
      err.textContent = "";
      const wait = el("p", "dim", "AI가 생각하는 중…");
      log.append(wait);
      try {
        const prompt = `Build an Arkism workflow from this conversation with the user. Ask only when something essential is missing (what it takes in, what it does, where the result goes), at most 3 questions in total; after that decide sensibly and build it.
${talk.map(([w, t]) => `${w === "AI" ? "Assistant" : "User"}: ${t}`).join("\n")}`;
        const a = await kit.assist("build", prompt, { purpose: "workflow" });
        if (!box.open) return; // closed while the AI was thinking: build nothing
        if (a.status !== "done") throw new Error(kit.assistError(a));
        const j = kit.parseJSON(a.output);
        if (j?.ask) { say("AI", String(j.ask).slice(0, 400)); return; }
        const g = j?.graph;
        if (!Array.isArray(g?.nodes) || !g.nodes.length) throw new Error("AI 답을 읽지 못했어요. 다시 보내 주세요.");
        g.edges = Array.isArray(g.edges) ? g.edges : [];
        say("AI", `${String(j.name || UNTITLED)}: 단계 ${g.nodes.length}개로 그렸어요. 편집 화면에서 확인해 주세요.`);
        if (await createBlank(layoutGraph(g), String(j.name || UNTITLED).slice(0, 60), String(j.role || "").slice(0, 200))) box.close();
      } catch (x) {
        err.textContent = x.message;
      } finally {
        wait.remove();
        send.disabled = false;
        q.focus();
      }
    });
    q.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); form.requestSubmit(); } });
    const foot = el("div", "dialog-foot");
    foot.append(q, send);
    const body = el("div", "dialog-body");
    body.append(log, err);
    form.append(head, body, foot);
    dialog.append(form);
    dialog.addEventListener("close", () => dialog.remove());
    document.body.append(dialog);
    dialog.showModal();
    q.focus();
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

  // 최근 실행 (198:286): the chosen workflow's runs, or every workflow's when none is chosen. 실행 기록 shows more.
  const RUN_BADGE = { succeeded: "done", failed: "fail", waiting: "wait", running: "live", stopped: "done" };
  function runRequest(cr) {
    try { return JSON.parse(cr.input || "{}").request || ""; } catch { return ""; }
  }
  function spanText(ms) {
    const s = Math.max(0, Math.round(ms / 1000));
    if (s < 60) return `${s}초`;
    if (s < 3600) return `${Math.round(s / 60)}분`;
    return `${Math.floor(s / 3600)}시간 ${Math.round((s % 3600) / 60)}분`;
  }
  function runTook(run) {
    const ends = Object.values(run.crew_run?.nodes || {}).map((n) => n.finished_at).filter(Boolean).map((t) => new Date(t).getTime());
    return ends.length && run.created_at ? spanText(Math.max(...ends) - new Date(run.created_at).getTime()) : "";
  }
  function runProgress(run, rec) {
    const work = (rec?.crew.graph?.nodes || []).filter(WORK).map((n) => n.id);
    const nodes = run.crew_run?.nodes || {};
    const ids = work.length ? work : Object.keys(nodes);
    return `${ids.filter((id) => nodes[id]?.status === "done").length}/${ids.length} 단계`;
  }
  function runRow(run) {
    const cr = run.crew_run || {};
    const rec = byId(cr.crew_id);
    const row = el("div", "wf-run");
    row.append(el("span", `wf-badge ${RUN_BADGE[cr.status] || ""}`, RUN_LABEL[cr.status] || cr.status || ""));
    const t = el("div", "wf-run-text");
    const loops = Object.values(cr.loops || {}).reduce((a, b) => a + b, 0);
    const bits = [rec?.crew.name, cr.status === "failed" ? (Object.values(cr.nodes || {}).find((n) => n.status === "failed")?.error || readable(cr.summary).split(/[.。]/)[0]).slice(0, 40) : "", runProgress(run, rec), runTook(run), loops ? `되돌아감 ${loops}번` : "", ago(run.created_at)].filter(Boolean);
    t.append(el("strong", "", runRequest(cr) || readable(cr.summary) || rec?.crew.name || "실행"), el("small", "", bits.join(" · ")));
    row.append(t);
    if (rec) row.append(button("상세 보기", "ghost", null, () => openGraph(rec, { run })));
    return row;
  }
  let runStatus = "all";
  let byStatus = { key: "", items: null };
  async function loadByStatus(key, crewId) {
    byStatus = { key, items: null };
    let items = [];
    try { items = (await request(`/v1/crew/runs?${query({ ...(crewId ? { crew_id: crewId } : {}), status: runStatus, limit: 30 })}`)).items || []; } catch (_) { /* empty list */ }
    if (byStatus.key !== key) return;
    byStatus = { key, items };
    if (state.view === "crew") drawRecent();
  }
  function drawRecent() {
    const box = $("#table .wf-recent");
    if (!box) return;
    const picked = state.selected?.startsWith("crew:") ? state.selected.slice(5) : null;
    const own = picked ? runsOf.get(picked) : null;
    if (picked && own === undefined) void loadRunsOf(picked);
    // 상태로 거르기: the server filters (status=…), so a failure older than the latest 30 runs still shows.
    const key = `${picked || ""}|${runStatus}`;
    if (runStatus !== "all" && byStatus.key !== key) void loadByStatus(key, picked);
    const filtered = runStatus !== "all" ? (byStatus.key === key && Array.isArray(byStatus.items) ? byStatus.items : null) : undefined;
    const loading = filtered === null || (filtered === undefined && (picked ? !Array.isArray(own) : recent === null));
    const list = filtered || (filtered === null ? [] : picked ? (Array.isArray(own) ? own : (recent || []).filter((x) => x.crew_run?.crew_id === picked)) : recent || []);
    const head = el("div", "wf-sec-head");
    const pick = el("select", "wf-select small");
    pick.setAttribute("aria-label", "실행 상태");
    for (const [k, label] of [["all", "모든 상태"], ["failed", "실패"], ["waiting", "확인 대기"], ["running", "도는 중"], ["succeeded", "완료"], ["stopped", "멈춤"]]) {
      const o = el("option", "", label);
      o.value = k;
      pick.append(o);
    }
    pick.value = runStatus;
    pick.addEventListener("change", () => { runStatus = pick.value; drawRecent(); });
    head.append(el("strong", "", "최근 실행"), el("small", "", loading && !list.length ? "불러오는 중…" : `${list.length}건`), el("span", "grow"), pick);
    if (list.length > 5) head.append(button(recentAll ? "줄이기" : "실행 기록", "ghost", null, () => { recentAll = !recentAll; drawRecent(); }));
    const rows = el("div", "wf-runs");
    const shown = recentAll ? list : list.slice(0, 5);
    rows.append(...shown.map(runRow));
    if (!loading && !list.length) rows.append(el("p", "wf-empty", runStatus !== "all" ? "이 상태인 실행이 없어요." : picked ? "이 워크플로우는 아직 실행한 적이 없어요." : "아직 실행한 적이 없어요."));
    box.replaceChildren(head, rows);
  }

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
    if (x.category) chips.append(el("span", "wf-chip", x.category));
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
        const kind = n.type === "ai_once" ? "한 번 호출" : n.config?.collab === "on" ? `에이전트 · 협업 워커 ${n.config?.max_workers || 5}` : "에이전트";
        who.append(el("strong", "", nodeTitle(n)), el("small", "", [kind, persona?.crew.name].filter(Boolean).join(" · ")));
        row.append(modelMark(modelOf(n, x)), who, el("span", "mono dim", modelOf(n, x)));
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
      if (x.category) top.append(el("span", "wf-chip", x.category));
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
      graphMode: !!opts.graphMode, review: !!opts.review, suggestions: null, reviewBusy: false, run: opts.run || null, runList: false });
    if (opts.select && ed.graph.nodes.some((n) => n.id === opts.select)) ed.sel = opts.select;
    state.view = "graph";
    state.selected = null;
    render();
    if (ed.run) void pollRun();
  }
  function leaveGraph() {
    if (ed.dirty && !confirm("저장하지 않은 변경이 있어요. 나갈까요?")) return;
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
  }
  async function saveGraph() {
    try {
      const saved = await save(ed.rec, { ...ed.rec.crew, graph: ed.graph });
      ed.dirty = false;
      await reload();
      ed.rec = byId(saved.id) || saved;
      state.view = "graph";
      render();
      notice("워크플로우를 저장했어요.");
      void autoTitle(ed.rec);
      return true;
    } catch (e) {
      notice(errorText(e));
      return false;
    }
  }
  // Edits keep the AI suggestions: applying one must not wipe the others (each re-checks with canApply).
  const touch = () => { ed.dirty = true; };
  function uid(type) {
    let i = 1;
    while (ed.graph.nodes.some((n) => n.id === `${type}${i}`)) i++;
    return `${type}${i}`;
  }
  function addNode(type, preset = {}) {
    const g = ed.graph;
    const anchor = g.nodes.find((n) => n.id === ed.sel) || g.nodes.filter((n) => n.type !== "end").at(-1) || g.nodes.at(-1);
    const n = { id: uid(type === "ai_once" ? "once" : type), type, title: preset.title || NODES[type].label, x: (anchor?.x || 0) + 260, y: anchor?.y || 120, config: { ...(preset.config || {}) } };
    for (const [k, , kind, opts] of FIELDS[type] || []) if (kind === "select" && n.config[k] === undefined) n.config[k] = opts[0][0];
    if (type === "ai") Object.assign(n.config, { goal: "{{input}}", access: "read", ...preset.config });
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
    const order = ["AI", "저장한 페르소나", "시작", "처리", "흐름", "알림·끝"];
    for (const [type, n] of Object.entries(NODES)) if (!n.hidden) out.push({ group: n.group, type, title: n.label, sub: n.sub, preset: {} });
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
      lead.append(icon(it.persona ? "users-three" : NODES[it.type].icon));
      const t = el("div", "");
      t.append(el("strong", "", it.title), el("small", "dim", it.sub));
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
    if (n.type === "ai") return [c.collab === "on" ? "에이전트 · 협업" : "에이전트", byId(c.persona)?.crew.name].filter(Boolean).join(" · ");
    if (n.type === "ai_once") return "한 번 호출";
    if (n.type === "code") return "코드 · 토큰 0";
    return NODES[n.type]?.label || n.type;
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
    const runNodes = ed.run?.crew_run?.nodes || {};
    const flagged = new Set((ed.review && ed.suggestions || []).flatMap((s) => [s.action?.node, s.action?.from, s.action?.to]).filter(Boolean));
    for (const n of g.nodes) {
      const st = runNodes[n.id];
      const box = el("div", `g-node ${n.type}${ed.sel === n.id ? " selected" : ""}${st ? ` st-${st.status}` : ""}${flagged.has(n.id) ? " flagged" : ""}`);
      box.style.left = `${n.x}px`;
      box.style.top = `${n.y}px`;
      box.dataset.id = n.id;
      const head = el("div", "g-node-head");
      const lead = el("span", "g-icon");
      lead.append(icon(NODES[n.type]?.icon || "stack"));
      const t = el("div", "");
      const sub = st ? [NODE_LABEL[st.status] || st.status, st.runs > 1 ? `${st.runs}회째` : ""].filter(Boolean).join(" · ") : nodeSub(n);
      t.append(el("strong", "", nodeTitle(n)), el("small", "dim", sub));
      head.append(lead, t);
      if (st) head.append(el("i", `dot ${{ done: "ready", running: "warn", waiting: "warn", failed: "error" }[st.status] || ""}`));
      box.append(head);
      for (const [k, v] of nodeSummary(n)) {
        const s = el("div", "g-node-field");
        s.append(el("small", "dim", k), el("span", k === "모델" ? "mono" : "", v));
        box.append(s);
      }
      if (!["start", "schedule", "webhook"].includes(n.type)) box.append(el("i", "g-port in"));
      const portBox = el("div", "g-outs");
      for (const [port, label] of portsOf(n, g)) {
        const row = el("div", "g-out");
        if (label) row.append(el("small", "dim", label));
        const dot = el("i", "g-port out");
        dot.dataset.port = port;
        dot.title = "끌어서 다음 노드에 이어요";
        if (!ed.run) dot.addEventListener("pointerdown", (ev) => startLink(ev, n, port, canvas, svg));
        row.append(dot);
        portBox.append(row);
      }
      box.append(portBox);
      if (!ed.run) head.addEventListener("pointerdown", (ev) => startDrag(ev, n, box, canvas, svg));
      box.addEventListener("click", (ev) => { ev.stopPropagation(); if (ed.sel !== n.id) selectOnCanvas(n.id); });
      canvas.append(box);
      nodeEls.set(n.id, box);
    }
    stage.append(canvas);
    stage.addEventListener("click", () => {
      if (ed.justPanned) { ed.justPanned = false; return; }
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
      if (!ev.ctrlKey) { ed.panX -= ev.deltaX; ed.panY -= ev.deltaY; } else ed.zoom = Math.min(2, Math.max(0.4, ed.zoom - ev.deltaY / 500));
      ev.preventDefault();
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
    if (ed.menu && !ed.run) {
      const menu = presetList();
      menu.addEventListener("click", (ev) => ev.stopPropagation());
      stage.append(menu);
    }
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
  function curve(a, b) {
    const dx = Math.max(40, Math.abs(b.x - a.x) / 2);
    return `M${a.x},${a.y} C${a.x + dx},${a.y} ${b.x - dx},${b.y} ${b.x},${b.y}`;
  }
  // A back edge leaves the right side, arcs over the top and comes into the target from the left.
  function backCurve(a, b, lift, k) {
    const top = Math.min(a.y, b.y) - lift;
    // Labels of back edges that share a span sit at different points of the top line, not on each other.
    const at = 0.3 + 0.4 * (k % 2);
    return { d: `M${a.x},${a.y} C${a.x + 60},${a.y} ${a.x + 60},${top} ${a.x - 40},${top} L${b.x + 40},${top} C${b.x - 60},${top} ${b.x - 60},${b.y} ${b.x},${b.y}`, mid: { x: b.x + (a.x - b.x) * at, y: top } };
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
      return a && b ? [outPoint(a, e.port), inPoint(b)] : [];
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
      const text = back ? `${portText ? `${portText} → ` : ""}다시 · ${used ? `${used}/${e.max}번째` : `최대 ${e.max}번`}` : (IS_AI(fromType) && e.port ? `${e.port} · AI가 고름` : "");
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
    if (ed.run || ed.review || !side) { render(false); return; }
    const t0 = performance.now();
    page.querySelectorAll(".g-node.selected").forEach((b) => b.classList.remove("selected"));
    if (id) page.querySelector(`.g-node[data-id="${CSS.escape(id)}"]`)?.classList.add("selected");
    for (const d of page.querySelector(".g-edges")?.drawn || []) d.p.classList.toggle("selected", d.i === edge);
    side.replaceWith(inspector());
    window.ArkismPerf?.note("canvas.select", performance.now() - t0, ed.graph.nodes.length);
  }
  // While a node is dragged only the lines touching it move; the rest of the canvas stays as drawn.
  function moveEdges(svg, nodeEls, id) {
    for (const d of svg.drawn || []) {
      if (d.e.from !== id && d.e.to !== id) continue;
      const pa = outPoint(nodeEls.get(d.e.from), d.e.port), pb = inPoint(nodeEls.get(d.e.to));
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
  const inPoint = (box) => { const dot = box.querySelector(".g-port.in"); return dot ? centerIn(dot, box) : { x: box.offsetLeft, y: box.offsetTop + 24 }; };
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
    const move = (e) => temp.setAttribute("d", curve(from, toCanvas(e, canvas)));
    const up = (e) => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      temp.remove();
      const target = document.elementFromPoint(e.clientX, e.clientY)?.closest(".g-node");
      if (!target || target.dataset.id === n.id) return;
      const to = target.dataset.id;
      const toNode = ed.graph.nodes.find((x) => x.id === to);
      if (["start", "schedule", "webhook"].includes(toNode.type)) { notice("시작 노드에는 선을 이을 수 없어요."); return; }
      if (ed.graph.edges.some((x) => x.from === n.id && x.to === to && (x.port || "") === port)) return;
      const edge = { from: n.id, to };
      if (port) edge.port = port;
      if (wouldCycle(ed.graph, n.id, to)) {
        edge.max = 2;
        notice("되돌아가는 선이에요. 최대 2번까지 다시 돌아요. 선을 눌러 횟수와 갈래 이름을 바꿀 수 있어요.");
      }
      ed.graph.edges.push(edge);
      ed.selEdge = ed.graph.edges.length - 1;
      ed.sel = null;
      touch();
      render(false);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
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

  // ---------------------------------------------------------------- inspector (198:2350 · 321:19936 · 321:21027)
  function inspector() {
    const box = el("aside", "g-right");
    if (ed.selEdge !== null && ed.graph.edges[ed.selEdge]) return edgeInspector(box, ed.graph.edges[ed.selEdge]);
    const n = ed.graph.nodes.find((x) => x.id === ed.sel);
    if (!n) {
      const x = ed.rec.crew;
      const backs = ed.graph.edges.filter(isBack);
      box.append(el("h3", "", "워크플로우"), kv([
        ["$이름", `$${x.handle}`],
        ["워커 기본", x.model || "노드마다"],
        ["멈추는 조건", [backs.length ? `되돌아가기 ${Math.max(...backs.map((e) => e.max))}번` : "", x.limits?.max_steps ? `단계 ${x.limits.max_steps}번` : "", x.limits?.max_minutes ? `${x.limits.max_minutes}분` : ""].filter(Boolean).join(" · ") || "정하지 않음"],
        ["일정·웹훅", x.trigger ? `${triggerText(x)}${x.enabled ? "" : " · 꺼짐"}` : "없어요"]
      ]), button("워크플로우 설정", "", "gear-six", () => openSettings(ed.rec, null, true)), el("i", "wf-rule"));
      box.append(el("h3", "", "노드 설정"), el("p", "dim", "노드를 누르면 여기서 고쳐요. 출력 점을 끌어 다음 노드에 이어요. 앞 노드로 이으면 되돌아가는 선이 돼요. 선을 누르면 선 설정이 열려요. 휠 클릭이나 스페이스를 누른 채 끌면 화면이 움직여요."));
      const g = ed.graph;
      box.append(kv([["노드", `${g.nodes.length}개`], ["AI 노드", `${aiNodes(g).length}개`], ["되돌아가는 선", `${g.edges.filter(isBack).length}개`]]));
      box.append(button("구조 검토", "", "sparkle", () => { ed.review = true; render(false); }));
      return box;
    }
    const head = el("div", "g-right-head");
    const lead = el("span", "g-icon");
    lead.append(icon(NODES[n.type]?.icon || "stack"));
    const t = el("div", "");
    t.append(el("strong", "", nodeTitle(n)), el("small", "dim", `${NODES[n.type]?.label || n.type} · ${NODES[n.type]?.sub || ""}`));
    head.append(lead, t, closeX("노드 설정 닫기", deselect));
    box.append(head);
    const title = input(n.title, { maxLength: 80 });
    title.addEventListener("input", () => { n.title = title.value; touch(); });
    title.addEventListener("change", () => render(false));
    box.append(field("이름", title));
    if (IS_AI(n.type)) aiFields(box, n);
    else for (const [key, label, kind, opt] of FIELDS[n.type] || []) {
      let i;
      if (kind === "select") i = select(opt, n.config[key] ?? opt[0][0]);
      else if (kind === "workflow") i = select([["", "고르세요"], ...workflows().filter((w) => w.id !== ed.rec.id).map((w) => [w.crew.handle, `$${w.crew.handle} · ${w.crew.name}`])], (n.config[key] || "").replace(/^\$/, ""));
      else if (kind === "area" || kind === "code") i = input(n.config[key], { tag: "textarea", rows: kind === "code" ? 12 : 4, cls: kind === "code" ? "code" : "" });
      else i = input(n.config[key], { placeholder: opt || "" });
      i.addEventListener("input", () => { n.config[key] = i.value; touch(); });
      i.addEventListener("change", () => {
        n.config[key] = i.value;
        if (key === "workflow") { const w = byHandle(i.value); if (w && (!n.title || n.title === NODES.subflow.label)) n.title = w.crew.name; }
        touch();
        render(false);
      });
      box.append(field(label, i));
    }
    sends(box, n);
    if (n.type === "code") box.append(el("p", "meta", "출력이 {\"ok\": true/false, …}면 통과·실패 갈래로 나뉘어요. 처음 한 번은 빌드에 1~2초, 30초 안에 끝나야 해요. 토큰은 들지 않아요."));
    if (n.type === "approval") box.append(el("p", "meta", "배포·게시처럼 되돌릴 수 없는 단계 바로 앞에 두세요. 답이 올 때까지 멈춰요."));
    if (n.type === "subflow") box.append(el("p", "meta", "부른 워크플로우를 끝까지 돌리고 그 결과를 다음 단계에 넘겨요. 세 겹까지만 부를 수 있어요."));
    if (WORK(n) && !ownerOf(ed.rec)) box.append(onlyRun(n));
    box.append(button("노드 지우기", "ghost danger", "trash", async () => {
      if (await confirmBox("노드를 지울까요?", "이 노드와 이어진 선이 함께 지워져요. 저장하기 전까지는 되돌리기로 살릴 수 있어요.", "노드 지우기")) removeNode(n.id);
    }));
    return box;
  }
  // AI 노드: 무엇·어디까지(목표·울타리·완료 기준)만 정하고, 어떻게는 AI가 정해요.
  function aiFields(box, n) {
    const c = n.config;
    const bind = (i, key, redraw) => {
      i.addEventListener("input", () => { c[key] = i.value; touch(); });
      i.addEventListener("change", () => { c[key] = i.value; touch(); if (redraw) render(false); });
      return i;
    };
    const row = el("div", "scope-fields");
    row.append(field("모델", bind(modelInput(c.model, ed.rec.crew.model || "deepseek-v4-flash"), "model", true), ed.rec.crew.model ? `비우면 ${ed.rec.crew.model}` : ""),
      field("추론", bind(select(EFFORTS, c.effort || "medium"), "effort")));
    box.append(modelList(), row);
    const persona = bind(select([["", "직접 쓰기"], ...personas().map((p) => [p.id, p.crew.name])], c.persona || ""), "persona", true);
    box.append(field("페르소나", persona, "설정 › 워크플로우에서 만든 페르소나를 고르거나 아래에 직접 써요"));
    const p = byId(c.persona);
    if (p) { const q = el("div", "quote"); q.append(el("div", "", p.crew.instructions || "")); box.append(q); }
    box.append(field(p ? "더할 지시" : "페르소나 지시", bind(input(c.instructions, { tag: "textarea", rows: 3, maxLength: 8000, placeholder: "너는 ○○다. …한다." }), "instructions")), skillsField(c));
    if (n.type === "ai_once") {
      box.append(field("질문 ({{input}}에 앞 결과가 들어가요)", bind(input(c.prompt, { tag: "textarea", rows: 4, maxLength: 8000 }), "prompt", true)));
      return;
    }
    box.append(field("목표 — 무엇을", bind(input(c.goal, { tag: "textarea", rows: 3, maxLength: 8000, placeholder: "하위 질문마다 출처 3개 이상 모아 줘. {{input}}에 앞 결과가 들어가요." }), "goal", true)),
      field("울타리 — 어디까지", bind(input(c.fence, { tag: "textarea", rows: 2, maxLength: 4000, placeholder: "웹 검색·브라우저만. 로그인·결제가 필요한 곳은 가지 않기." }), "fence")),
      field("완료 기준", bind(input(c.done, { maxLength: 1000, placeholder: "질문마다 출처 3개, 출처마다 요약 한 줄" }), "done")));
    const row2 = el("div", "scope-fields");
    row2.append(field("하는 일", bind(select(ACCESS, c.access || "read"), "access")), field("실패하면 다시", bind(select([["0", "안 함"], ["1", "1번"], ["2", "2번"], ["3", "3번"]], c.retries || "0"), "retries")));
    box.append(row2);
    // 협업 — 나눠서 맡기기: this node plans, workers do the parts at the same time, this node merges them.
    const on = c.collab === "on";
    const ch = el("div", "g-collab-head");
    ch.append(el("strong", "", "협업 — 나눠서 맡기기"), switchEl(on, "협업 켜기", (v) => { c.collab = v ? "on" : ""; if (v && !c.workers) c.workers = JSON.stringify([{ name: "워커", model: ed.rec.crew.model || "deepseek-v4-flash" }]); touch(); render(false); }));
    box.append(ch);
    if (!on) { box.append(el("p", "meta", "켜면 이 칸이 일을 나눠 워커에게 맡기고 결과를 합쳐요. 하위 질문이 몇 개일지 미리 모를 때 써요.")); return; }
    let workers = [];
    try { workers = JSON.parse(c.workers || "[]"); } catch { workers = []; }
    const saveWorkers = () => { c.workers = JSON.stringify(workers); touch(); };
    const list = el("div", "g-workers");
    workers.forEach((w, i) => {
      const r = el("div", "g-worker");
      const nm = input(w.name, { maxLength: 40, placeholder: "조사원" });
      const md = modelInput(w.model, ed.rec.crew.model || "deepseek-v4-flash");
      nm.addEventListener("input", () => { w.name = nm.value; saveWorkers(); });
      md.addEventListener("input", () => { w.model = md.value; saveWorkers(); });
      const del = el("button", "icon-btn plain");
      del.type = "button";
      del.setAttribute("aria-label", "워커 빼기");
      del.append(icon("x"));
      del.addEventListener("click", () => { workers.splice(i, 1); saveWorkers(); render(false); });
      r.append(nm, md, del);
      list.append(r);
    });
    box.append(el("small", "dim", "워커 역할 · 모델"), list, button("워커 역할 더하기", "ghost small", "plus", () => { workers.push({ name: "", model: ed.rec.crew.model || "" }); saveWorkers(); render(false); }));
    const row3 = el("div", "scope-fields");
    row3.append(field("최대 개수", bind(input(c.max_workers || "5", { type: "number", min: 1, max: 16 }), "max_workers", true)), field("동시에", bind(input(c.concurrency || "3", { type: "number", min: 1, max: 16 }), "concurrency", true)));
    box.append(row3, el("p", "meta", "실행할 때 AI가 일을 몇 개로 나눌지 정하고, 워커 결과를 이 칸이 합쳐요."));
    const row4 = el("div", "scope-fields");
    row4.append(field("필요한 것", bind(input(c.needs, { maxLength: 200, placeholder: "검색 결과 목록" }), "needs")), field("만드는 것", bind(input(c.makes, { maxLength: 200, placeholder: "질문별 자료 묶음" }), "makes")));
    box.append(row4);
  }
  // 스킬: the hub skills this step must follow (the runner puts their SKILL.md into its instructions).
  // Empty leaves it to the AI, which finds skills through the skill router as usual.
  function skillsField(c) {
    const all = state.data.skills || [];
    const picked = (c.skills || "").split(/[,\n]/).map((s) => s.trim()).filter(Boolean);
    const save = (list) => { c.skills = list.join(","); touch(); render(false); };
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
    } else if (PORTS[from?.type]) {
      const port = select(PORTS[from.type], e.port || PORTS[from.type][0][0]);
      port.addEventListener("change", () => { e.port = port.value; touch(); render(false); });
      box.append(field("갈래", port));
    }
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
      if (!(await confirmBox("선을 지울까요?", `${nodeTitle(from || {})} → ${nodeTitle(to || {})} 선이 지워져요. 저장하기 전까지는 되돌리기로 살릴 수 있어요.`, "선 지우기"))) return;
      ed.graph.edges = ed.graph.edges.filter((x) => x !== e);
      ed.selEdge = null;
      touch();
      render(false);
    }));
    return box;
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
    const h4b = el("h4", "", "AI 제안");
    if (ed.suggestions) h4b.append(el("small", "", `${ed.suggestions.length}개`));
    box.append(list, h4b);
    if (ed.reviewBusy) box.append(el("p", "dim", "AI가 그래프를 읽는 중…"));
    else if (!ed.suggestions) box.append(button("AI 제안 받기", "", "sparkle", aiReview), el("p", "meta", "토큰을 줄일 칸, 순서 문제, 멈추는 조건을 짚어 줘요. 실행할 때는 검토하지 않아요."));
    else if (!ed.suggestions.length) box.append(el("p", "dim", "남은 제안이 없어요."));
    for (const s of ed.suggestions || []) {
      const card = el("div", "wf-suggest");
      card.append(el("strong", "", s.title), el("p", "", s.body));
      const row = el("div", "wf-suggest-actions");
      row.append(el("span", "wf-tag", { tokens: "토큰 줄이기", order: "순서", stop: "멈추는 조건", safety: "안전" }[s.tag] || s.tag || "제안"));
      const dismiss = button("무시", "ghost small", null, () => { ed.suggestions = ed.suggestions.filter((x) => x !== s); render(false); });
      row.append(dismiss);
      if (s.action && canApply(s.action) && fresh(s)) row.append(button(applyLabel(s.action), "small", "check", () => { applyAction(s.action); ed.suggestions = (ed.suggestions || []).filter((x) => x !== s); render(false); notice("적용했어요. 저장하기 전까지는 되돌리기로 취소할 수 있어요."); }));
      else if (s.action) row.append(el("small", "dim", "그 노드나 선이 바뀌어 다시 받아야 해요"));
      card.append(row);
      box.append(card);
    }
    if (ed.suggestions && !ed.reviewBusy) box.append(button("바뀐 그래프로 다시 받기", "ghost small", "arrows-clockwise", aiReview));
    box.append(el("p", "meta", "[적용]을 누르면 그래프가 바로 바뀌고, 나머지 제안은 그대로 남아요. 실행 기록이 쌓이면 자주 다시 도는 노드도 짚어요."));
    return box;
  }
  async function aiReview() {
    ed.reviewBusy = true;
    render(false);
    const g = { nodes: ed.graph.nodes.map(({ id, type, title, config }) => ({ id, type, title, config })), edges: ed.graph.edges };
    // The graph as the AI reads it, taken from the live node objects (so a node deleted and re-added under the
    // same id while it thinks counts as a different node): edits made meanwhile make its suggestions stale.
    const asked = { nodes: Object.fromEntries(ed.graph.nodes.map((n) => [n.id, nodePrint(n)])), edges: structuredClone(ed.graph.edges) };
    const prompt = `Review the workflow graph below and give at most 4 improvements. Principles: steps an if can decide become code nodes (0 tokens); only steps that need judgement use AI. Cheap checks first. Back edges only where truly needed, with a count limit. Parallel branches must not need each other. A human check before any irreversible action.
Shape: {"suggestions":[{"title":"short Korean title","body":"two plain Korean sentences","tag":"tokens|order|stop|safety","action":null or {"type":"set_model","node":"id","model":"..."} or {"type":"set_edge_max","from":"id","to":"id","max":2} or {"type":"set_access","node":"id","access":"read|write"} or {"type":"insert_before","node":"id","new":{"type":"code|ai_once|approval","title":"...","config":{}}}}]}
Workflow: ${ed.rec.crew.name} ($${ed.rec.crew.handle}) — ${ed.rec.crew.role || ""}
Default worker model: ${ed.rec.crew.model || "none"}
Models allowed for set_model (only these): ${[...new Set(models?.enabledModels() || [])].join(", ") || "none — do not suggest set_model"}
Graph: ${JSON.stringify(g).slice(0, 14000)}`;
    try {
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
    } catch (e) {
      notice(e.message);
    } finally {
      ed.reviewBusy = false;
      render(false);
    }
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
  const applyLabel = (a) => ({ set_model: "모델 바꾸기", set_access: "바꾸기", set_edge_max: "횟수 바꾸기", insert_before: `${NODES[a.new?.type]?.label || "노드"} 넣기` })[a.type] || "적용";
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
    if (!ed.run || state.view !== "graph") return;
    try {
      const r = await request(`/v1/crew/runs/${encodeURIComponent(ed.run.id)}?${query()}`);
      if (!ed.run || r.id !== ed.run.id) return;
      ed.run = r;
      if (state.view === "graph") render(false);
    } catch (_) { /* a short outage: try again on the next tick */ }
    const st = ed.run?.crew_run?.status;
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
    if (r) { ed.run = r; render(false); void pollRun(); }
  }
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
    page.append(el("h4", "", "실행 단계"));
    const table = el("div", "wf-steptable");
    const headRow = el("div", "wf-steprow head");
    headRow.append(el("span", "", "단계"), el("span", "", "결과"), el("span", "", "시간"));
    table.append(headRow);
    runSteps(cr).filter(WORK).forEach((n, i) => {
      const st = cr.nodes[n.id];
      const row = el("div", `wf-steprow st-${st.status}`);
      const name = el("span", "");
      name.append(el("strong", "", `${i + 1} · ${nodeTitle(n)}${st.runs > 1 ? ` · ${st.runs}회째` : ""}`));
      if (st.error) name.append(el("small", "", st.error));
      else if (st.status === "pending") name.append(el("small", "", "앞 단계가 끝나면 실행해요."));
      if (st.status === "done" && ["succeeded", "failed", "stopped"].includes(cr.status)) name.append(button("여기부터 다시", "ghost", null, () => retryRun(n.id)));
      row.append(name, el("span", `wf-badge small ${{ failed: "fail", waiting: "wait", running: "live", pending: "line", skipped: "line" }[st.status] || ""}`, st.status === "pending" ? "실행 전" : stepState(n, st).split(" · ")[0]), el("span", "mono", nodeDur(st) || "—"));
      table.append(row);
    });
    page.append(table);
    const loops = Object.values(cr.loops || {}).reduce((a, b) => a + b, 0);
    if (loops) page.append(el("p", "meta", `되돌아가는 선을 ${loops}번 따라가 앞 단계를 다시 돌았어요.`));
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
    if (r) { ed.run = r; render(false); void pollRun(); }
  }

  function graphHead() {
    const rec = ed.rec;
    if (!rec) return [];
    if (ed.run) {
      const view = segment([["graph", "그래프"], ["list", "목록"]], ed.runList ? "list" : "graph", (k) => { ed.runList = k === "list"; render(false); });
      view.classList.add("wf-view");
      return [button("돌아가기", "ghost", "arrow-left", leaveGraph), view];
    }
    const save2 = button("저장", "primary", null, saveGraph);
    save2.disabled = !ed.dirty;
    const revert = button("되돌리기", "ghost", null, () => { ed.dirty = false; openGraph(byId(rec.id) || rec, { graphMode: ed.graphMode }); });
    revert.disabled = !ed.dirty;
    const review = button(`구조 검토${ed.suggestions?.length ? ` · ${ed.suggestions.length}` : ""}`, ed.review ? "active" : "", "sparkle", () => { ed.review = !ed.review; render(false); });
    const trial = button("실행", "", "play", runFromEditor);
    trial.disabled = ed.dirty;
    trial.title = ed.dirty ? "저장한 뒤에 실행할 수 있어요" : "저장된 워크플로우로 지금 한 번 돌려요";
    const settings = button("설정", "ghost", "gear-six", () => openSettings(ed.rec, null, true));
    settings.title = "이름·$이름·워커 기본 모델·멈추는 조건·일정·웹훅·배경 사진";
    // An owned workflow runs only through its agent (@); the server refuses a manual run.
    return [button("돌아가기", "ghost", "arrow-left", leaveGraph), settings, review, ...(ownerOf(rec) ? [] : [trial]), revert, save2];
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
    box.append(field("이름", name), field("한 줄 설명", role, "목록과 노드 선택에서 보여요"), field("지시", text, "역할·말투·꼭 지킬 것을 써요. 키나 토큰은 넣지 마세요."));
    const bar = el("div", "crew-actions");
    // AI로 마무리: Opus 5.5 (medium) finishes what is written so far; nothing is saved until 저장.
    const ai = button("AI로 마무리", "", "sparkle", async () => {
      if (!(d.name + d.role + d.instructions).trim()) { notice("이름이나 지시를 조금이라도 먼저 써 주세요."); return; }
      ai.disabled = true;
      notice("Opus 5.5가 페르소나를 마무리하는 중이에요…");
      try {
        const prompt = `Finish this persona draft. Keep the author's intent and the sentences already written, fill the empty fields, and turn the instructions into a short numbered list in Korean, opening with who the agent is ("You are …" in Korean), that makes the role, tone, must-dos and must-nots clear. Use only the keys name, role, instructions.\n\nName: ${d.name || "(empty)"}\nOne-line description: ${d.role || "(empty)"}\nInstructions:\n${d.instructions || "(empty)"}`;
        const a = await kit.assist("fill", prompt, { model: "opus 5.5", persona: { crew: { instructions: "You write personas for AI agents, in short, clear Korean." } } });
        if (a.status !== "done") throw new Error(kit.assistError(a));
        const got = kit.parseJSON(a.output || "") || {};
        if (!got.instructions && !got.name) throw new Error("AI 답을 읽지 못했어요. 다시 눌러 주세요.");
        if (got.name) name.value = d.name = String(got.name).slice(0, 60);
        if (got.role) role.value = d.role = String(got.role).slice(0, 200);
        if (got.instructions) text.value = d.instructions = String(got.instructions).slice(0, 20000);
        notice("채웠어요. 확인하고 저장해 주세요.");
      } catch (e) { notice(e.message); } finally { ai.disabled = false; }
    });
    const ok = button("저장", "primary", null, async () => {
      if (!d.name.trim() || !d.instructions.trim()) { notice("이름과 지시를 채워 주세요."); return; }
      ok.disabled = true;
      try {
        await save(rec, { ...(rec?.crew || {}), type: "persona", name: d.name.trim(), role: d.role.trim(), instructions: d.instructions });
        personaOpen = personaDraft = null;
        notice("페르소나를 저장했어요.");
        await reload();
      } catch (e) { notice(errorText(e)); ok.disabled = false; }
    });
    bar.append(ok, ai, button("취소", "ghost", null, () => { personaOpen = personaDraft = null; render(false); }));
    if (rec) {
      bar.append(button("지우기", "ghost danger", "trash", async () => {
        const users = personaUses(rec.id);
        // A node pointing at a deleted persona would lose its instructions silently.
        if (users.length) { notice(`${users.map((u) => u.crew.name).join(", ")}에서 쓰고 있어요. 노드에서 먼저 바꿔 주세요.`); return; }
        if (!confirm(`'${rec.crew.name}' 페르소나를 지울까요? 되돌릴 수 없어요.`)) return;
        try {
          await request(`/v1/crew/${encodeURIComponent(rec.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope, expected_revision: rec.revision }) });
          personaOpen = personaDraft = null;
          notice("지웠어요.");
          await reload();
        } catch (e) { notice(errorText(e)); }
      }));
    }
    box.append(bar);
    return box;
  }

  // ---------------------------------------------------------------- 에이전트 (Figma 208:17763 · 310:19277)
  // A work agent (crew type "agent", called @이름; workflows keep $이름): who it is, its instructions, model and how it
  // thinks, the skills·MCP·memory it may use, where it can be called and its monthly budget. Teams list their members by
  // agent id. Status comes from /v1/crew/agents/status, spend from /v1/crew/usage (estimated list prices).
  const MEMORY_KINDS = [["procedural", "작업 규칙"], ["decision", "결정"], ["lesson", "교훈"], ["user", "선호"]];
  const CALL_PLACES = [["claude_code", "Claude Code"], ["codex", "Codex"], ["zcode", "ZCode"], ["discord", "디스코드"], ["app", "arkism 앱"]];
  const DEPTH = [["low", "빠르게"], ["medium", "보통"], ["high", "깊게"]];
  const MODEL_MODES = [["auto", "Clef 자동 선택"], ["fixed", "모델 고정"]];
  const TEMPS = [["0.2", "0.2 · 정확"], ["0.7", "0.7 · 보통"], ["1", "1.0 · 자유"]];
  const FORMATS = [["text", "자유 텍스트"], ["markdown", "마크다운"], ["json", "JSON"]];
  const FILE_EDITS = [["ask", "확인 받고 진행"], ["auto", "알아서 고침"], ["never", "고치지 않음"]];
  const AGENT_STATUS = { working: ["일하는 중", "live"], idle: ["대기", "line"], off: ["꺼짐", "line"] };
  const agents = () => items().filter((r) => r.crew.type === "agent");
  const teams = () => items().filter((r) => r.crew.type === "team");
  const teamsOf = (id) => teams().filter((t) => (t.crew.members || []).some((m) => m.agent_id === id));
  const depthText = (e) => DEPTH.find(([k]) => k === e)?.[1] || "보통";
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
  function openWorkers(id, isNew = false) {
    state.workerNew = isNew;
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
  const statusBadge = (id) => {
    const [label, cls] = AGENT_STATUS[wk.status[id]] || AGENT_STATUS.idle;
    return el("span", `wf-badge small ${cls}`, label);
  };
  const workerMeta = (x) => [`@${x.handle || "이름"}`, x.model_mode === "auto" ? "Clef 자동" : x.model || "모델 미정"].join(" · ");
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
  const touchWorker = () => { wk.dirty = true; render(false); };
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
  function whoFields(d, isNew) {
    const row = el("div", "scope-fields");
    const name = bindText(input(d.name, { maxLength: 60, required: true, placeholder: "코드리뷰어" }), d, "name");
    const handle = bindText(input(d.handle, { maxLength: 40, pattern: "[\\p{L}\\p{N}_\\-]{1,40}", placeholder: "띄어쓰기 없이" }), d, "handle", (v) => v.replace(/^@/, ""));
    // A new agent's @이름 follows its name until the handle is typed in.
    if (isNew) name.addEventListener("input", () => { if (!handle.dataset.touched) { handle.value = d.handle = name.value.replace(/\s+/g, "").slice(0, 40); drawWorkerSave(); } });
    handle.addEventListener("input", () => { handle.dataset.touched = "1"; });
    row.append(field("이름", name), field("한 줄 소개", bindText(input(d.role, { maxLength: 200, placeholder: "커밋 전에 diff를 보고 고칠 곳을 알려 줘요" }), d, "role")));
    const row2 = el("div", "scope-fields");
    const cat = bindText(input(d.category, { maxLength: 40, placeholder: "개발" }), d, "category");
    cat.setAttribute("list", "wk-cats");
    const dl = el("datalist");
    dl.id = "wk-cats";
    for (const c of catsOf(agents())) { const o = el("option"); o.value = c; dl.append(o); }
    row2.append(field("@이름", handle, "팀과 같은 이름은 못 써요"), field("카테고리", cat, "격자에서 이 이름으로 묶여요"), dl);
    const face = el("button", "wk-face-btn");
    face.type = "button";
    face.append(avatarMark(d), el("span", "", validAvatar(d.avatar) ? "아바타 바꾸기" : "아바타 고르기"));
    face.addEventListener("click", async () => {
      const a = await pickAvatar(d.avatar);
      if (a === undefined) return;
      if (a) d.avatar = a; else delete d.avatar;
      touchWorker();
    });
    return [face, row, row2];
  }
  function instructionsField(d) {
    const t = bindText(input(d.instructions, { tag: "textarea", rows: 8, maxLength: 4000, placeholder: "## 역할\n너는 …다.\n## 말투\n…\n## 하지 말 것\n…", cls: "wk-instr" }), d, "instructions");
    return field("지시", t, "최대 4,000자 · 기억은 자료로만 쓰고 지시로 따르지 않아요");
  }
  // 일하는 방법: the workflow this agent runs when it is called; empty means it answers on its own.
  function workflowField(d) {
    const s = pick([["", "정하지 않음 (직접 답해요)"], ...allWorkflows().filter((w) => !ownerOf(w) || ownerOf(w).id === wk.id).map((w) => [w.crew.handle, `$${w.crew.handle} · ${w.crew.name}`])], (d.workflow || "").replace(/^\$/, ""),
      (v) => { d.workflow = v; touchWorker(); }, "일하는 방법");
    const w = allWorkflows().find((x) => x.crew.handle === (d.workflow || "").replace(/^\$/, ""));
    // The chosen workflow is shown as its own panel (name, numbered steps, one clear edit button) so it reads as part of this agent.
    if (!w) return field("쓸 워크플로우", s);
    const box = el("div", "wk-flow");
    {
      // Nodes on the same level run together, so they share one number (as in workflowDetail).
      const lvls = levels(w.crew.graph).map((lv) => lv.filter(WORK)).filter((lv) => lv.length);
      const steps = lvls.flat();
      const ai = steps.filter((n) => IS_AI(n.type)).length;
      const head = el("div", "wk-flow-head");
      const who = el("div", "wk-flow-who");
      const meta = el("small", "");
      meta.append(el("code", "", `$${w.crew.handle}`), el("span", "", `${lvls.length}단계`));
      if (ai) meta.append(el("span", "", `AI ${ai}곳`));
      who.append(el("strong", "", w.crew.name), meta);
      const mark = el("span", "wk-flow-mark"); mark.append(icon("tree-structure"));
      head.append(mark, who, button("단계 고치기", "small", "pencil-simple", () => openGraph(w)));
      const chain = el("ol", "wk-flow-steps");
      lvls.forEach((lv, i) => {
        const li = el("li", lv.some((n) => IS_AI(n.type)) ? "ai" : "");
        li.append(el("span", "n", String(i + 1)), el("span", "t", lv.map(nodeTitle).join(" · ")));
        if (lv.length > 1) li.append(el("small", "", "동시에"));
        chain.append(li);
      });
      box.append(head, chain);
    }
    const wrap = el("div", "wk-flow-wrap");
    wrap.append(box, field("다른 워크플로우로 바꾸기", s));
    return wrap;
  }
  function depthRow(d) {
    const r = el("div", "wk-depth");
    const l = el("div", "");
    l.append(el("strong", "", "생각 깊이"), el("small", "", "깊을수록 느리고 비싸요"));
    r.append(l, segment(DEPTH, d.effort || "medium", (k) => { d.effort = k; touchWorker(); }));
    return r;
  }
  function modelFields(d, full) {
    const auto = d.model_mode === "auto";
    const models2 = el("div", "scope-fields");
    models2.append(field("기본 모델", bindText(modelInput(d.model, "claude sonnet 5.5"), d, "model"), auto ? "Clef가 일마다 고르고, 정해 두면 이 모델을 먼저 봐요" : ""),
      field("막히면 넘길 모델", bindText(modelInput(d.fallback_model, "gpt-5.5"), d, "fallback_model")));
    const out = [modelList(), segment(MODEL_MODES, d.model_mode || "fixed", (k) => { d.model_mode = k; touchWorker(); }), models2];
    if (full) {
      out.push(field("이미지가 오면 볼 모델", bindText(modelInput(d.image_model, "claude opus 5.5"), d, "image_model"), "비우면 기본 모델이 봐요"));
      const row = el("div", "scope-fields");
      const temp = String(d.temperature ?? "0.7");
      row.append(field("창의성", pick(TEMPS.some(([k]) => k === temp) ? TEMPS : [...TEMPS, [temp, temp]], temp, (v) => { d.temperature = Number(v); touchWorker(); }, "창의성")),
        field("답변 길이 (토큰)", bindText(input(d.max_output_tokens || "", { type: "number", min: 1, max: 64000, placeholder: "2048" }), d, "max_output_tokens", (v) => (Number(v) > 0 ? Math.round(Number(v)) : 0))));
      out.push(row, field("출력 형식", pick(FORMATS, d.output_format || "text", (v) => { d.output_format = v; touchWorker(); }, "출력 형식")));
    }
    out.push(depthRow(d));
    return out;
  }
  function useFields(d) {
    const skills = (state.data.skills || []).map((s) => [s.name, (s.description || "").slice(0, 80)]);
    const servers = (ctx.toolServers?.() || []).map((s) => [s.name, s.desc || ""]);
    return [el("h4", "wk-label", "스킬"), listChips(d.skills || [], skills, "+ 스킬 추가", (v) => { d.skills = v; touchWorker(); }, "wk-skills"),
      el("h4", "wk-label", "MCP·도구"), listChips(d.tools || [], servers, "+ 도구 추가", (v) => { d.tools = v; touchWorker(); }, "wk-tools"),
      el("h4", "wk-label", "읽는 기억"), toggleChips(MEMORY_KINDS, (k) => !!d.memory?.[k] && d.memory[k] !== "off", (k, on) => {
        const m = { ...(d.memory || {}) };
        if (on) m[k] = "all"; else delete m[k];
        d.memory = m;
        touchWorker();
      })];
  }
  const placeChips = (d) => toggleChips(CALL_PLACES, (k) => (d.agents || []).includes(k), (k, on) => { d.agents = on ? [...(d.agents || []), k] : (d.agents || []).filter((a) => a !== k); touchWorker(); });
  function budgetFields(d) {
    const b = d.budget || {};
    const num = (key) => (v) => { d.budget = { ...(d.budget || {}), [key]: Number(v) > 0 ? Number(v) : 0 }; return v; };
    const row = el("div", "scope-fields");
    row.append(field("한 달 한도 ($)", bindText(input(b.monthly_usd || "", { type: "number", min: 0, step: "1", placeholder: "20" }), d, "_m", num("monthly_usd"))),
      field("이 금액 넘으면 묻기 ($, 한 번)", bindText(input(b.ask_over_usd || "", { type: "number", min: 0, step: "0.5", placeholder: "1" }), d, "_a", num("ask_over_usd"))));
    return [row, el("small", "dim", "한도에 닿으면 멈추지 않고 확인을 물어요. 비용은 공개 가격으로 셈한 추정이에요.")];
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
    if (!out.name || !out.handle || !out.instructions.trim()) { notice("이름·@이름·지시를 채워 주세요."); return; }
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
      notice(rec ? "저장했어요." : wk.teamsPick.size && joined < wk.teamsPick.size ? "만들었어요. 팀에 못 넣은 곳이 있어요 — 팀 화면에서 넣어 주세요." : "전문가를 만들었어요.");
      state.workerNew = false;
      await reload();
      workerPick(saved.id);
      render(false);
    } catch (e) { notice(errorText(e)); } finally { wk.busy = false; drawWorkerSave(); }
  }
  async function removeWorker(rec) {
    const inTeams = teamsOf(rec.id);
    if (inTeams.length) { notice(`${inTeams.map((t) => t.crew.name).join(", ")}에 들어 있어요. 팀에서 먼저 빼 주세요.`); return; }
    if (!confirm(`'${rec.crew.name}' 전문가를 지울까요? 되돌릴 수 없어요.`)) return;
    try {
      await request(`/v1/crew/${encodeURIComponent(rec.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope, expected_revision: rec.revision }) });
      notice("지웠어요.");
      wk.id = null;
      await reload();
    } catch (e) { notice(errorText(e)); }
  }
  // AI로 초안 만들기: the fill helper answers name·role·instructions·file_name·skills·tools as JSON.
  async function draftWorker(ask, btn) {
    const d = wk.draft;
    if (!ask.trim()) { notice("어떤 동료가 필요한지 한 줄 써 주세요."); return; }
    btn.disabled = true;
    notice("AI가 초안을 쓰는 중이에요…");
    try {
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
      Object.assign(d, {
        name: str(got.name, 60) || d.name, role: str(got.role, 200) || d.role, instructions: str(got.instructions, 4000) || d.instructions,
        file_name: /^[a-z0-9][a-z0-9-]{0,63}$/.test(got.file_name || "") ? got.file_name : d.file_name,
        skills: Array.isArray(got.skills) ? got.skills.map(String).filter((s) => names.has(s)).slice(0, 64) : d.skills,
        tools: Array.isArray(got.tools) ? got.tools.map(String).filter((s) => servers.has(s)).slice(0, 32) : d.tools
      });
      if (!d.handle) d.handle = d.name.replace(/\s+/g, "").slice(0, 40);
      wk.dirty = true;
      notice("초안을 채웠어요. 확인하고 만들어 주세요.");
      render(false);
    } catch (e) { notice(e.message); } finally { btn.disabled = false; }
  }
  // 대화 테스트: one question to the saved agent (assist kind try, by @이름); the answer shows under the question.
  function testPanel(rec) {
    const box = el("section", "wk-card wk-test");
    const q = input(wk.test?.q || "", { tag: "textarea", rows: 2, maxLength: 4000, placeholder: `@${rec.crew.handle}에게 물어볼 말` });
    q.setAttribute("aria-label", "테스트 질문");
    const answer = el("div", "wk-answer");
    if (wk.test?.a) answer.append(el("p", "", wk.test.a));
    const send = button("보내기", "primary small", null, async () => {
      if (!q.value.trim()) return;
      if (wk.dirty) { notice("저장한 뒤에 테스트해요. 저장된 설정으로 답해요."); return; }
      send.disabled = true;
      wk.test = { q: q.value, a: "" };
      answer.replaceChildren(el("p", "dim", "답을 기다리는 중…"));
      try {
        const a = await kit.assist("try", q.value, { expert: rec.crew.handle, persona: { crew: { instructions: rec.crew.instructions || "" } } });
        wk.test.a = a.status === "done" ? a.output || "(빈 답)" : kit.assistError(a);
      } catch (e) { wk.test.a = e.message; }
      answer.replaceChildren(el("p", "", wk.test.a));
      send.disabled = false;
    });
    const bar = el("div", "wk-foot");
    bar.append(send, button("닫기", "ghost small", null, () => { wk.test = null; render(false); }));
    const h = el("div", "wk-card-head");
    h.append(el("h3", "", "대화 테스트"), el("small", "", "저장된 설정으로 한 번 답해요 · 파일은 고치지 않아요"));
    box.append(h, q, bar, answer);
    return box;
  }
  function spendSection(id, d) {
    const sec = el("section", "wk-side-sec");
    sec.append(el("h3", "", "이번 달 한도"));
    const row = (wk.usage?.agents || []).find((a) => a.id === id);
    const spent = Number(row?.cost_usd) || 0;
    const limit = Number(row?.limit_usd) || Number(d.budget?.monthly_usd) || 0;
    const big = el("div", "wk-spend");
    big.append(el("strong", "mono", `$${spent.toFixed(2)}`), el("small", "", limit ? ` / $${limit}` : " · 한도 없음"));
    sec.append(big);
    if (limit) {
      const pct = Math.min(100, Math.round(row?.pct ?? (spent / limit) * 100));
      const bar = el("div", "wk-bar");
      const fill = el("i");
      fill.style.width = `${pct}%`;
      bar.append(fill);
      sec.append(bar, el("small", "dim", `월 $${limit} 중 ${pct}% 사용${wk.usage?.kpi?.cost_estimated ? " · 추정" : ""}`));
    } else sec.append(el("small", "dim", row ? `${row.calls || 0}번 불렀어요 · 추정` : "아직 쓴 기록이 없어요."));
    sec.append(...budgetFields(d), field("파일 고치기", pick(FILE_EDITS, d.file_edits || "ask", (v) => { d.file_edits = v; touchWorker(); }, "파일 고치기")));
    return sec;
  }
  // 전문가 (208:17763): category chips and a card grid on the left, the picked expert's settings on the right.
  const catsOf = (all) => [...new Set(all.map((r) => r.crew.category).filter(Boolean))];
  function expertCard(r) {
    const x = r.crew;
    const b = el("button", `wk-tile${r.id === wk.id ? " on" : ""}${x.enabled === false ? " off" : ""}`);
    b.type = "button";
    b.setAttribute("aria-pressed", String(r.id === wk.id));
    const badge = x.enabled === false ? el("span", "wf-badge small line", "꺼짐") : statusBadge(r.id);
    badge.classList.add("wk-tile-badge");
    const av = avatarMark(x);
    av.classList.add("wk-avatar");
    const flow = allWorkflows().find((w) => w.crew.handle === (x.workflow || "").replace(/^\$/, ""));
    b.append(badge, av);
    if (x.category) b.append(el("small", "wk-tile-cat", x.category));
    b.append(el("strong", "", x.name), el("p", "", x.role || ""), el("small", "wk-tile-model", x.model_mode === "auto" ? "Clef 자동" : x.model || "모델 미정"),
      el("span", `wk-tile-flow${flow ? "" : " none"}`, flow ? `→ ${flow.crew.name}` : "바로 답해요"));
    b.addEventListener("click", () => { if (r.id === wk.id) return; if (wk.dirty && !confirm("저장하지 않은 변경이 있어요. 버리고 옮길까요?")) return; workerPick(r.id); render(false); });
    return b;
  }
  function renderWorkers() {
    $("#controls").replaceChildren();
    const main = $("#table");
    main.className = "wk-main";
    void loadAgentInfo();
    if (state.workerNew) return renderWorkerNew(main);
    const all = agents();
    const cats = catsOf(all);
    if (wk.cat && !cats.includes(wk.cat)) wk.cat = "";
    const shown = all.filter((r) => !wk.cat || r.crew.category === wk.cat);
    if (!wk.id || !byId(wk.id)) workerPick(shown[0]?.id || all[0]?.id);
    const rec = wk.id ? byId(wk.id) : null;
    const d = wk.draft;
    const left = el("div", "wk-browse");
    if (cats.length) left.append(toggleChips([["", `모든 카테고리 ${all.length}`], ...cats.map((c) => [c, `${c} ${all.filter((r) => r.crew.category === c).length}`])],
      (k) => (wk.cat || "") === k, (k) => { wk.cat = k; render(false); }));
    const grid = el("div", "wk-grid");
    const add = el("button", "wk-tile wk-tile-new");
    add.type = "button";
    const plus = el("span", "wk-avatar"); plus.append(icon("plus"));
    add.append(plus, el("strong", "", "새 전문가"));
    add.addEventListener("click", () => openWorkers(null, true));
    grid.append(...shown.map(expertCard), add);
    left.append(grid);
    if (!rec || !d) {
      const empty = el("div", "wk-detail wk-empty");
      empty.append(el("h3", "", "전문가"), el("p", "", "역할·지시·모델·쓸 수 있는 스킬을 한 번 정해 두면, Claude Code·Codex 어디서든 @이름으로 불러 일을 맡겨요."), button("새 전문가", "primary", null, () => openWorkers(null, true)));
      main.replaceChildren(left, empty);
      return;
    }
    const pane = el("div", "wk-detail");
    const head = el("div", "wk-head");
    const av = avatarMark(d);
    av.classList.add("wk-avatar");
    const ht = el("div", "wk-head-text");
    const tags = el("div", "wk-title");
    if (d.category) tags.append(el("span", "wf-chip", d.category));
    tags.append(d.enabled === false ? el("span", "wf-badge small line", "꺼짐") : statusBadge(rec.id));
    ht.append(el("h2", "", d.name || "이름 없음"), el("small", "", d.role || ""), tags);
    head.append(av, ht, switchEl(d.enabled !== false, "켜기", (on) => { d.enabled = on; touchWorker(); }));
    const acts = el("div", "wk-head-actions");
    const tabs = segment([["edit", "편집"], ["test", "대화 테스트"]], wk.test ? "test" : "edit", (k) => { wk.test = k === "test" ? wk.test || { q: "", a: "" } : null; render(false); });
    const del = el("button", "wf-link quiet", "지우기");
    del.type = "button";
    del.addEventListener("click", () => removeWorker(rec));
    const ok = button("저장", "primary small", null, () => saveWorker(rec));
    drawWorkerSave = () => { ok.disabled = !wk.dirty || wk.busy; };
    drawWorkerSave();
    acts.append(tabs, del, ok);
    const call = el("div", "wk-call");
    const copy = el("button", "wf-link", "복사");
    copy.type = "button";
    copy.addEventListener("click", () => { navigator.clipboard?.writeText(`@${d.handle}`).then(() => notice("복사했어요."), () => {}); });
    call.append(raw(el("strong", "mono", `@${d.handle || "이름"}`)), el("small", "", "로 불러요"), copy);
    pane.append(head, acts, call);
    if (wk.test) { pane.append(testPanel(rec)); main.replaceChildren(left, pane); return; }
    const inTeams = teamsOf(rec.id);
    const team = raw(el("p", "wk-side-sub", inTeams.length ? inTeams.map((t) => `${t.crew.name} · ${tr(({ moderator: "진행자", worker: "작업자", reviewer: "리뷰어", scribe: "기록자" })[t.crew.members.find((m) => m.agent_id === rec.id)?.role] || "작업자")}`).join("  /  ") : tr("아직 어느 팀에도 없어요.")));
    pane.append(
      workerCard("일하는 방법", "", workflowField(d)),
      workerCard("실행", "", ...modelFields(d, true), el("h4", "wk-label", "부르는 곳"), placeChips(d)),
      workerCard("어떤 동료인가요?", `페르소나 · 고친 횟수 ${rec.revision} · 마크다운으로 써요`, ...whoFields(d, false), instructionsField(d)),
      ...(pins ? [pins.card({ rec, draft: d, sub: tr(`@${rec.crew.handle}에게 일을 맡길 때마다 먼저 넣어요. 위에 있을수록 먼저 들어가요.`), noun: tr("이 전문가는"), onChange: () => { wk.dirty = true; drawWorkerSave(); } })] : []),
      workerCard("쓸 수 있는 것", "", ...useFields(d)),
      workerCard("속한 팀", "", team));
    const spend = spendSection(rec.id, d);
    spend.className = "wk-card wk-spend-card";
    pane.append(spend);
    main.replaceChildren(left, pane);
  }
  function renderWorkerNew(main) {
    const d = wk.draft || (wk.draft = blankAgent());
    const back = el("button", "page-back");
    back.type = "button";
    back.append("← 전문가");
    back.addEventListener("click", () => { if (wk.dirty && !confirm("쓰던 내용을 버릴까요?")) return; openWorkers(null, false); });
    $(".main > .page-head").prepend(back);
    const form = el("div", "wk-form");
    const ai = el("div", "wk-ai");
    const ask = input("", { maxLength: 400, placeholder: "어떤 동료가 필요하세요? 예: “PR을 올리기 전에 테스트 빠진 곳을 찾아 주는 동료”" });
    ask.setAttribute("aria-label", "어떤 동료가 필요한지");
    const go2 = button("AI로 초안 만들기", "primary small", null, () => draftWorker(ask.value, go2));
    ai.append(ask, go2);
    const step = (n, title, sub, ...nodes) => {
      const c = workerCard(title, sub, ...nodes);
      c.querySelector(".wk-card-head").prepend(el("span", "wk-step", String(n)));
      return c;
    };
    const like = toggleChips([["", "빈 칸에서"], ...agents().map((r) => [r.id, r.crew.name])], (k) => (d.copy_of || "") === k, (k) => {
      const src = k ? byId(k)?.crew : null;
      wk.draft = src ? { ...structuredClone(src), name: `${src.name} 복사`.slice(0, 60), handle: `${src.handle || ""}2`.slice(0, 40), copy_of: k } : blankAgent();
      wk.dirty = !!src;
      render(false);
    });
    form.append(ai,
      step(1, "누구인가요", "", ...whoFields(d, true), el("h4", "wk-label", "비슷한 것에서 시작"), like),
      step(2, "어떻게 일하나요", "전문가가 일할 때 지키는 역할·말투·순서예요.", instructionsField(d), workflowField(d)),
      step(3, "어떤 모델로 생각하나요", "", ...modelFields(d, false)),
      step(4, "무엇을 쓸 수 있나요", "", ...useFields(d)),
      step(5, "예산", "", ...budgetFields(d)));
    const foot = el("div", "wk-foot");
    const ok = button("만들기", "primary small", null, () => saveWorker(null));
    foot.append(ok, button("취소", "ghost small", null, () => { wk.draft = null; openWorkers(null, false); }));
    form.append(foot);
    const side = el("aside", "wk-side");
    const pv = el("section", "wk-side-sec");
    const card = el("div", "wk-preview");
    // Redrawn as the fields are typed in (the inputs keep focus; only this card changes).
    drawWorkerSave = () => {
      const top = el("div", "wk-row-top");
      top.append(avatarMark(d), el("strong", "", d.name || "이름"));
      card.replaceChildren(top, el("small", "", `${workerMeta(d)} · ${depthText(d.effort)}`), el("p", "", d.role || "한 줄 소개가 여기에 보여요"));
    };
    drawWorkerSave();
    pv.append(el("h3", "wk-quiet", "미리보기"), card);
    const where = el("section", "wk-side-sec");
    where.append(el("h3", "", "어디서 부를 수 있나요"), placeChips(d), el("p", "wk-side-sub", `Claude Code·Codex에서 @${d.handle || "이름"}으로 부르면 이 전문가가 일을 맡아요.`));
    const tm = el("section", "wk-side-sec");
    tm.append(el("h3", "", "팀에 넣기"));
    if (teams().length) tm.append(toggleChips(teams().map((t) => [t.id, t.crew.name]), (k) => wk.teamsPick.has(k), (k, on) => { if (on) wk.teamsPick.add(k); else wk.teamsPick.delete(k); render(false); }));
    tm.append(el("p", "wk-side-sub", teams().length ? "만든 뒤 팀 화면에서도 넣을 수 있어요." : "아직 팀이 없어요. 팀을 만들면 거기서 넣어요."));
    side.append(pv, where, tm);
    main.replaceChildren(form, side);
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
    load: () => request(`/v1/crew?${query()}`).then((r) => r.items || [], () => []),
    count: () => workflows().length,
    settingsTab: personaTab,
    render: renderList,
    detail: renderDetail,
    head: headActions,
    views: {
      crew: { title: "자동화", count: () => `${workflows().length}개`, sub: () => { const on = workflows().filter((r) => triggered(r.crew)).length; return `단계가 정해진 워크플로우예요. 에이전트가 일하는 방법으로 쓰거나 $이름으로 불러요. ${on ? `일정·웹훅은 ${on}개 켜져 있어요.` : "켜진 일정·웹훅은 없어요."}`; } }
    },
    pages: {
      workers: { title: () => (state.workerNew ? "새 전문가" : "전문가"), count: () => (state.workerNew ? "" : `${agents().length}명`),
        sub: () => "",
        bare: () => !state.workerNew, head: () => [], render: renderWorkers },
      graph: { title: () => (ed.rec ? `${ed.rec.crew.name} · ${ed.run ? "실행" : "워크플로우 편집"}` : "워크플로우 편집"), sub: pageSub, head: graphHead, render: renderGraph }
    },
    // exported for tests in a browser console
    _test: { parseCron, nextRun, cronText, levels, wouldCycle, cliOf, ruleChecks, simpleNode, portsOf }
  };
};
