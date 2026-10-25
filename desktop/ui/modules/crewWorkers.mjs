// Owns agent drafts, resources and history. Dependencies are explicit live ports, never a global scope.
export function createCrewWorkers(deps) {


  // ---------------------------------------------------------------- 에이전트 (Figma 208:17763 · 310:19277)
  // A work agent (crew type "agent", called @이름; workflows keep $이름): who it is, its instructions, model and how it
  // thinks, the skills·MCP·memory it may use, where it can be called and its monthly budget. Teams list their members by
  // agent id. Status comes from /v1/crew/agents/status, spend from /v1/crew/usage (estimated list prices).
  const MEMORY_KINDS = [["procedural", "작업 규칙"], ["decision", "결정"], ["lesson", "교훈"], ["user", "선호"]];

  const MEMORY_KIND_HINT = { "작업 규칙": "이렇게 일하라고 정해 둔 규칙", "결정": "이미 정한 결정", "교훈": "실수에서 배운 것", "선호": "사용자가 좋아하는 방식" };

  const CALL_PLACES = [["claude_code", "Claude Code"], ["codex", "Codex"], ["zcode", "ZCode"], ["discord", "디스코드"], ["app", "arkism 앱"]];

  const DEPTH = [["low", "빠르게"], ["medium", "보통"], ["high", "깊게"]];

  const MODEL_MODES = [["auto", "자동 선택"], ["fixed", "모델 고정"]];

  const FORMATS = [["text", "자유 텍스트"], ["markdown", "마크다운"], ["json", "JSON"]];

  const FILE_EDITS = [["ask", "확인 받고 진행"], ["auto", "알아서 고침"], ["never", "고치지 않음"]];

  const agents = () => deps.items().filter((r) => r.crew.type === "agent");

  const teams = () => deps.items().filter((r) => r.crew.type === "team");

  const teamsOf = (id) => teams().filter((t) => (t.crew.members || []).some((m) => m.agent_id === id));

  const blankAgent = () => ({ type: "agent", name: "", handle: "", role: "", instructions: "", model_mode: "fixed", model: "", fallback_model: "", effort: "medium",
    skills: [], tools: [], agents: ["claude_code", "codex", "zcode", "app"], memory: { procedural: "all", decision: "all", lesson: "all" }, file_edits: "ask", enabled: true });

  const wk = { id: null, draft: null, dirty: false, q: "", busy: false, status: {}, usage: null, statusAt: 0, teamsPick: new Set(), test: null, cat: "" };

  function workerPick(id) {
    const rec = deps.byId(id);
    wk.id = rec?.id || null;
    wk.draft = rec ? structuredClone(rec.crew) : null;
    wk.dirty = false;
    wk.test = null;
  }

  // Usage › expert row asks for that expert's history tab (team.js openExpertHistory).
  // A separate window (popup-drag.js) reopens an automation by id.
  window.addEventListener("arkism:open-automation", (e) => { const r = deps.byId(e.detail?.id); if (r) deps.getAuto().open(r, e.detail.tab); });

  // over: open on top of the page the user is on (사용량), without going to the expert list.
  window.addEventListener("arkism:open-expert", (e) => {
    const { id, tab = "edit", over } = e.detail || {};
    if (!id) return;
    if (over && deps.byId(id) && !wk.pop) { workerPick(id); wk.openTab = tab; openExpertPopup(deps.byId(id));
      // loadAgentInfo repaints only the expert list, so swap in the numbers it fetched here
      loadAgentInfo().then(() => { const r = deps.byId(id); if (r) wk.pop?.dialog.querySelectorAll(".ex-metrics").forEach((x) => x.replaceWith(usageMetrics(r))); }).catch(console.error); } else openWorkers(id, false, tab);
  });

  function openWorkers(id, isNew = false, tab = "edit") {
    if (wk.pop) return;
    deps.state.workerNew = isNew;
    wk.openTab = tab;
    wk.open = true;
    if (isNew) { wk.id = null; wk.draft = blankAgent(); wk.dirty = false; wk.teamsPick = new Set(); } else workerPick(id);
    if (deps.state.view === "workers") deps.render(false); else deps.ctx.go("workers");
  }

  // working·idle·off per agent, and this month's estimated spend; both refresh at most every 20 s.
  async function loadAgentInfo() {
    if (Date.now() - wk.statusAt < 20000) return;
    wk.statusAt = Date.now();
    const [st, use] = await Promise.all([
      deps.request(`/v1/crew/agents/status?${deps.query()}`).then((r) => r.items || {}, () => ({})),
      deps.request(`/v1/crew/usage?${deps.query({ range: "30d", kind: "agent" })}`).then((r) => r, () => null)]);
    wk.status = st && !Array.isArray(st) ? st : {};
    wk.usage = use;
    if (deps.state.view === "workers" && !wk.dirty) deps.render(false);
  }

  function toggleChips(options, has, flip) {
    const row = deps.el("div", "wk-chips");
    for (const [k, label] of options) {
      const on = has(k);
      const b = deps.el("button", `wk-chip${on ? " on" : ""}`, label);
      b.type = "button";
      b.setAttribute("aria-pressed", String(on));
      b.addEventListener("click", () => flip(k, !on));
      row.append(b);
    }
    return row;
  }

  // Chips with ✕ for a list of names, and a box to add one from the suggestions.
  function listChips(list, suggest, placeholder, onChange, listId) {
    const row = deps.el("div", "wk-chips");
    for (const name of list) {
      const chip = deps.el("span", "wk-chip on mono", name);
      const x = deps.el("button", "icon-btn plain");
      x.type = "button";
      x.setAttribute("aria-label", `${name} 빼기`);
      x.append(deps.icon("x"));
      x.addEventListener("click", () => onChange(list.filter((n) => n !== name)));
      chip.append(x);
      row.append(chip);
    }
    const dl = deps.el("datalist");
    dl.id = listId;
    for (const [v, sub] of suggest) if (!list.includes(v)) { const o = deps.el("option", "", sub || ""); o.value = v; dl.append(o); }
    const add = deps.input("", { maxLength: 128, placeholder, cls: "wk-add" });
    add.setAttribute("list", listId);
    add.addEventListener("change", () => { const v = add.value.trim(); if (v && !list.includes(v)) onChange([...list, v]); });
    row.append(add, dl);
    return row;
  }

  const touchWorker = () => { wk.dirty = true; if (wk.redraw && wk.pop) { wk.redraw(); wk.paintHead?.(); drawWorkerSave(); } else deps.render(false); };

  function workerCard(title, sub, ...nodes) {
    const c = deps.el("section", "wk-card");
    const h = deps.el("div", "wk-card-head");
    h.append(deps.el("h3", "", title));
    if (sub) h.append(deps.el("small", "", sub));
    c.append(h, ...nodes);
    return c;
  }

  // Text inputs keep the draft without redrawing (focus stays); toggles redraw.
  let drawWorkerSave = () => {};

  const bindText = (node, d, key, conv = (v) => v) => { node.addEventListener("input", () => { d[key] = conv(node.value); wk.dirty = true; drawWorkerSave(); }); return node; };

  const pick = (options, value, onChange, label) => {
    const s = deps.select(options, value);
    s.setAttribute("aria-label", label);
    s.addEventListener("change", () => onChange(s.value));
    return s;
  };

  function instructionsField(d) {
    const t = bindText(deps.input(d.instructions, { tag: "textarea", rows: 3, maxLength: 4000, placeholder: "이 전문가가 지킬 역할·말투·순서를 적어 주세요", cls: "wk-instr" }), d, "instructions");
    return deps.field("지시", instructionsAI(t), "최대 4,000자 · 기억은 자료로만 쓰고 지시로 따르지 않아요");
  }

  // 일하는 순서 (868:61909 · 310:19277): the workflow this agent runs when it is called, in one box — name, 단계 고치기,
  // the chart, and at the bottom the choice to switch (empty means it answers on its own).
  function workflowField(d) {
    const s = pick([["", "없음 · 지시대로"], ...deps.allWorkflows().filter((w) => deps.ownerOf(w) ? deps.ownerOf(w).id === wk.id : !deps.triggered(w.crew)).map((w) => [w.crew.handle, w.crew.name])], (d.workflow || "").replace(/^\$/, ""),
      (v) => { d.workflow = v; touchWorker(); }, "일하는 순서");
    s.classList.add("ex-flow-pick");
    for (const o of s.options) if (o.value) o.setAttribute("data-no-i18n", ""); // saved names stay as written; the empty first option is UI text
    const w = deps.allWorkflows().find((x) => x.crew.handle === (d.workflow || "").replace(/^\$/, ""));
    const box = deps.el("div", "wk-flow");
    const head = deps.el("div", "wk-flow-head");
    const mark = deps.el("span", "wk-flow-mark"); mark.append(deps.icon("tree-structure"));
    head.append(mark, w ? deps.raw(deps.el("strong", "", w.crew.name)) : deps.el("strong", "", "워크플로우 없음 · 지시대로 일해요"));
    box.append(head);
    if (!w) head.append(deps.button("새 워크플로우 만들기", "small", "plus", () => deps.getAuto().create()));
    if (w) {
      // Nodes on the same level run together, so they share one number (as in workflowDetail).
      const lvls = deps.levels(w.crew.graph).map((lv) => lv.filter(deps.WORK)).filter((lv) => lv.length);
      head.append(deps.button("단계 고치기", "small", "pencil-simple", () => deps.openGraph(w)));
      const lim = w.crew.limits || {};
      box.append(flowChart(w, lvls));
      if (lim.max_minutes || lim.max_steps) {
        const l = deps.el("small", "wk-flow-limit");
        l.append(deps.el("span", "k", "멈추는 조건"), deps.el("span", "", [lim.max_minutes ? deps.tr(`${lim.max_minutes}분`) : "", lim.max_steps ? deps.tr(`${lim.max_steps}단계 넘으면 멈춤`) : ""].filter(Boolean).join(" · ")));
        box.append(l);
      }
    }
    const foot = deps.el("label", "wk-flow-foot");
    foot.append(deps.el("span", "", w ? "다른 워크플로우로 바꾸기" : "+ 워크플로우 연결"), s);
    box.append(foot);
    const f = deps.el("div", "field", "일하는 순서");
    f.append(box);
    return f;
  }

  // 흐름도 (868:61909): 시작 → one column per level (boxes of the same level stacked) → 끝, arrows between,
  // and each 되돌아가는 선 drawn under the columns it spans with its label. A CSS grid: col 1 start, then arrow/level pairs.
  function flowChart(w, lvls) {
    const g = deps.el("div", "ex-flow");
    g.style.gridTemplateColumns = `repeat(${lvls.length * 2 + 3}, auto)`;
    const at = (node, col, row = 1, span = 1) => { node.style.gridColumn = `${col} / span ${span}`; node.style.gridRow = String(row); g.append(node); return node; };
    const end = (cls, label) => { const e = deps.el("div", `ex-flow-end ${cls}`); e.append(deps.el("i", ""), deps.el("small", "", label)); return e; };
    const arrow = () => deps.el("span", "ex-flow-arrow", "→");
    at(end("start", "시작"), 1);
    const colOf = {};
    lvls.forEach((lv, i) => {
      at(arrow(), 2 + i * 2);
      const col = deps.el("div", "ex-flow-col");
      for (const n of lv) {
        colOf[n.id] = 3 + i * 2;
        const b = deps.el("div", `ex-flow-node${deps.IS_AI(n.type) ? " ai" : ""}`);
        const kind = deps.IS_AI(n.type) ? "AI" : deps.NODES[n.type]?.label || n.type;
        b.append(deps.el("small", "kind", lv.length > 1 ? `${kind} · 동시에` : kind), n.title ? deps.raw(deps.el("strong", "", n.title)) : deps.el("strong", "", deps.nodeTitle(n))); // a title the user gave stays; the node kind is UI text
        if (n.config?.model) b.append(deps.el("code", "m", n.config.model));
        col.append(b);
      }
      at(col, 3 + i * 2);
    });
    at(arrow(), 2 + lvls.length * 2);
    at(end("stop", "끝"), 3 + lvls.length * 2);
    const byId2 = Object.fromEntries((w.crew.graph?.nodes || []).map((n) => [n.id, n]));
    let row = 2;
    for (const e of (w.crew.graph?.edges || []).filter(deps.isBack)) {
      const a = colOf[e.to], b = colOf[e.from];
      // titles the user gave stay as written; the node kinds, the port and the limit are UI text
      const step = (n) => (n.title ? deps.raw(deps.el("span", "", n.title)) : deps.el("span", "", deps.nodeTitle(n)));
      const label = () => { const s = deps.el("small", "ex-flow-back-t", "↺ "); s.append(step(byId2[e.from] || { id: e.from }), ...(e.port ? [" · ", deps.el("span", "", e.port)] : []), " → ", step(byId2[e.to] || { id: e.to }), " · ", deps.el("span", "", `최대 ${e.max}번`)); return s; };
      if (!a || !b || a > b) { at(label(), 1, row++, lvls.length * 2 + 3); continue; }
      at(deps.el("div", "ex-flow-back"), a, row++, b - a + 1);
      at(label(), a, row++, lvls.length * 2 + 3 - a + 1);
    }
    const wrap = deps.el("div", "ex-flow-wrap");
    wrap.append(g);
    return wrap;
  }

  function depthRow(d) {
    const r = deps.el("div", "wk-depth");
    r.append(deps.el("strong", "", "생각 깊이"), deps.segment(DEPTH, d.effort || "medium", (k) => { d.effort = k; touchWorker(); }));
    return r;
  }

  // A model box with the maker's mark in front (868:61909); the mark follows what is typed.
  function modelBox(d, key, ph, label, list) {
    const w = deps.el("span", "ex-model");
    const i = bindText(deps.modelInput(d[key], ph, list), d, key);
    i.setAttribute("aria-label", label);
    let mark = deps.modelMark(d[key] || "");
    const tag = deps.el("em", "ex-model-kind", deps.tr(window.ArkismBrand?.kindLabel(d[key]) || ""));
    i.addEventListener("input", () => {
      const m = deps.modelMark(i.value); mark.replaceWith(m); mark = m;
      tag.textContent = deps.tr(window.ArkismBrand?.kindLabel(i.value) || "");
      if (key === "model") wk.onModel?.();
    });
    w.append(mark, i, tag);
    return w;
  }

  // 모델 (868:61909): 기본·예비 모델 and 생각 깊이.
  function modelCard(d) {
    const models2 = deps.el("div", "scope-fields");
    models2.append(deps.field("기본 모델", modelBox(d, "model", "고르기 · 예: claude sonnet 5.5", "기본 모델"), d.model_mode === "auto" ? "Clef가 일마다 고르고, 정해 두면 이 모델을 먼저 봐요" : ""),
      deps.field("예비 모델", modelBox(d, "fallback_model", "고르기 (선택)", "예비 모델")));
    return workerCard("모델", "", deps.modelList(), models2, depthRow(d));
  }

  const exRow = (label, ...nodes) => { const r = deps.el("div", "ex-run-row"); r.append(deps.el("span", "k", label), ...nodes); return r; };

  // A segment whose saved value is not one of the presets keeps it as an extra choice.
  const presetSeg = (opts, cur, onPick) => deps.segment(opts.some(([k]) => k === cur) ? opts : [...opts, [cur, cur]], cur, onPick);

  // 실행하는 곳 (1314:15710, states 1314:81780): Arkism's own path or one of this PC's agent runner connections
  // (`arkism crew backends`, read through agent_backends: names and checks, never the token). Only the name is saved.
  const RUNNER_LIMITS = [["model_choice", "모델 선택 안 됨"], ["cancel", "취소 안 됨"], ["tool_restriction", "도구 제한 안 됨"], ["usage", "사용량 확인 안 됨"]];
  function runnerState(b, any) {
    if (!b) return [["line", any ? "이 PC에 없는 연결" : "연결 없음"]];
    if (!b.status) return [["line", "확인 전"]];
    if (b.status !== "ok") return [["fail", "연결 실패"]];
    return [["live", "실행 가능"], ...RUNNER_LIMITS.filter(([k]) => !b.capabilities?.[k]).map(([k, label]) => [k === "usage" ? "line" : "wait", label])];
  }
  function runnerRows(d) {
    const pick = exRow("실행하는 곳"), state = exRow("연결 상태"), hint = exRow("");
    hint.replaceChildren(deps.el("small", "dim", "외부 실행기에는 읽기 작업만 보내요. 모델은 실행기 것을 쓰고, 비용은 실행기가 알려 줄 때만 보여요. 연결은 터미널에서 arkism crew backends add로 만들어요."));
    const draw = (list) => {
      const cur = String(d.backend || "local");
      const names = list.map((b) => b.name);
      const opts = [["local", "Arkism"], ...names.map((n) => [n, n])];
      if (cur !== "local" && !names.includes(cur)) opts.push([cur, cur]);
      pick.replaceChildren(deps.el("span", "k", "실행하는 곳"), deps.segment(opts, cur, (k) => { if (k === "local") delete d.backend; else d.backend = k; touchWorker(); draw(list); }));
      const b = list.find((x) => x.name === cur);
      const when = b?.checked_at ? new Date(b.checked_at) : null;
      state.replaceChildren(deps.el("span", "k", "연결 상태"), ...runnerState(b, cur !== "local").map(([cls, label]) => deps.el("span", `wf-badge small ${cls}`, label)),
        ...(when && !isNaN(when) ? [deps.el("small", "", `${String(when.getHours()).padStart(2, "0")}:${String(when.getMinutes()).padStart(2, "0")}에 확인 · 이 PC의 연결`)] : []));
      state.hidden = cur === "local";
      hint.hidden = cur === "local" && !names.length;
    };
    draw([]);
    window.__TAURI__?.core?.invoke("agent_backends").then((list) => draw(Array.isArray(list) ? list : []), () => draw([]));
    return [pick, state, hint];
  }

  // 실행 (868:61909): the title above one box of rows — 방식 · 창의성 · 답변 길이 · 출력 형식 · 파일 고치기 · 부르는 곳 · 이미지.
  function runCard(d) {
    const temp = String(d.temperature ?? "0.7");
    const len = String(Number(d.max_output_tokens) > 0 ? d.max_output_tokens : 0);
    const imgBox = modelBox(d, "image_model", "claude opus 5.5", "이미지가 오면 볼 모델", "crew-models-vision");
    // 이미지 모델 is only asked for when the main model reads text only (a vision model looks at images itself).
    const needsImage = () => window.ArkismBrand?.kind(d.model) !== "vision";
    const c = deps.el("section", "ex-run");
    const rows = deps.el("div", "ex-run-box");
    rows.append(...runnerRows(d),
      exRow("방식", deps.segment(MODEL_MODES, d.model_mode || "fixed", (k) => { d.model_mode = k; touchWorker(); })),
      exRow("창의성", presetSeg([["0.2", "낮음"], ["0.7", "보통"], ["1", "높음"]], temp, (k) => { d.temperature = Number(k); touchWorker(); })),
      exRow("답변 길이", presetSeg([["0", "자동"], ["1024", "짧게"], ["4096", "길게"]], len, (k) => { d.max_output_tokens = Number(k); touchWorker(); }), deps.el("small", "dim", "토큰 수 상한이에요")),
      exRow("출력 형식", deps.segment(FORMATS, d.output_format || "text", (k) => { d.output_format = k; touchWorker(); })),
      exRow("파일 고치기", deps.segment(FILE_EDITS, d.file_edits || "ask", (k) => { d.file_edits = k; touchWorker(); })),
      exRow("부르는 곳", placeChips(d)),
      exRow("이미지 모델", imgBox, deps.modelList("crew-models-vision", (m) => window.ArkismBrand?.kind(m) === "vision")));
    const imgRow = rows.lastElementChild;
    imgRow.hidden = !needsImage();
    wk.onModel = () => { imgRow.hidden = !needsImage(); }; // the 기본 모델 box calls this as it changes
    c.append(deps.el("h3", "", "실행"), rows);
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

  const skillRec = (name) => (deps.state.data.skills || []).find((x) => x.name === name);

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
      if (list.length >= 20) { deps.notice("항상 넣는 자료는 20개까지예요."); return false; }
      list.push({ collection, id, revision: 0, injection: collection === "knowledge_sources" ? "summary" : "full", priority: list.length ? Math.max(0, Math.min(...list.map((p) => p.priority)) - 5) : 100, ...patch });
    } else if (on) list[at] = { ...list[at], ...patch };
    else if (at >= 0) list.splice(at, 1);
    d.pinned = list;
    return true;
  }

  // 고정 칩 합계 토큰: the same rough estimate as the 항상 넣는 자료 table (pins.js: chars kept per mode / 2.5).
  const PIN_CAP = { full: 8000, summary: 380, reference: 80 };

  function pinChars(p) {
    const src = p.collection === "skills" ? (deps.state.data.skills || []).find((x) => x.id === p.id)
      : p.collection === "memories" ? (deps.state.data.memories || []).find((x) => x.id === p.id) : null;
    return p.collection === "skills" ? (src?.description || "").length * 6 : p.collection === "memories" ? (src?.text || "").length : 600; // shortcut: knowledge counts one chunk (the table has chunk_count), fine for a hint
  }

  const pinTok = (p, mode = p.injection) => Math.max(1, Math.round(Math.min(PIN_CAP[mode] || 0, pinChars(p)) / 2.5));

  const pinTokens = (d) => (d.pinned || []).reduce((n, p) => n + pinTok(p), 0);

  // One chip in 쓸 수 있는 것 (929:61931): a pinned one has the pin and the accent, and can be dragged to change the order.
  function useChip(d, label, { pin, mono, fresh, bad, onOpen, onRemove }) {
    const b = deps.el("button", `wk-chip on ex-uchip${pin ? " pinned" : ""}${mono ? " mono" : ""}${bad ? " bad" : ""}`);
    b.type = "button";
    if (fresh) { const dot = deps.el("i", "ex-dot"); dot.title = deps.tr("새 버전이 있어요"); b.append(dot); }
    if (pin) b.append(pinSvg(true));
    b.append(deps.raw(deps.el("span", "", label)));
    b.title = bad ? deps.tr(deps.pins?.STATUS[bad]?.[1] || "") : label;
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
    const w = deps.el("span", "ex-uchip-wrap");
    const x = deps.el("button", "ex-uchip-x");
    x.type = "button";
    x.setAttribute("aria-label", `${deps.tr("목록에서 빼기")}: ${label}`);
    x.append(deps.icon("x"));
    x.addEventListener("click", (e) => { e.stopPropagation(); onRemove(); touchWorker(); });
    w.append(b, x);
    return w;
  }


  // 쓸 수 있는 것 (929:61931): 스킬·도구·기억·지식 rows of chips (필요할 때 쓰기; a pin = 항상 먼저 넣기), the pin total,
  // then one box to find and add any of the four.
  function useCard(d, rec) {
    const c = workerCard("쓸 수 있는 것", "핀을 꽂은 것은 부를 때마다 먼저 넣어요. 칩을 누르면 자세히 볼 수 있어요.");
    const body = deps.el("div", "ex-use");
    c.append(body);
    const draw = () => {
      deps.pins?.loadLibrary();
      const res = rec && deps.pins ? deps.pins.resolution(rec) : null;
      const badOf = (p) => { const st = rec && deps.pins?.statusOf(rec, p, res); return st && st.status !== "ok" ? st.status : ""; };
      const grid = deps.el("div", "ex-use-rows");
      const row = (label, ...kids) => { const r = deps.el("div", "wk-chips"); r.append(...kids); grid.append(deps.el("span", "ex-use-k", label), r); return r; };
      // 스킬: the ones it may use, and any pinned skill.
      const pinnedSkills = pinOrder(d).filter((p) => p.collection === "skills").map((p) => (deps.state.data.skills || []).find((x) => x.id === p.id)?.name).filter(Boolean);
      const names = [...new Set([...(d.skills || []), ...pinnedSkills])];
      const skillKids = names.map((name) => {
        const sk = skillRec(name);
        const pin = sk ? skillPin(d, sk) : null;
        return useChip(d, name, { pin, mono: true, fresh: !!(pin?.revision && skillActiveVer(sk) > pin.revision), bad: pin ? badOf(pin) : "", onOpen: () => openUseDetail(d, rec, { collection: "skills", id: sk?.id, name }),
          onRemove: () => { d.skills = (d.skills || []).filter((n) => n !== name); if (sk) setPin(d, "skills", sk.id, false); } });
      });
      // What may still be added, per kind (the four "+ X 추가" lists).
      const left = (kind) => kind === "tools" ? (deps.ctx.toolServers?.() || []).filter((s) => !(d.tools || []).includes(s.name)).map((s) => ({ id: s.name, title: s.name, meta: s.desc || "", add: () => { d.tools = [...(d.tools || []), s.name]; } }))
        : kind === "skills" ? (deps.pins?.candidates("skills") || []).filter((x) => !names.includes(x.title)).map((x) => ({ ...x, add: () => { d.skills = [...(d.skills || []), x.title]; } }))
        : (deps.pins?.candidates(kind) || []).filter((x) => !pinOf(d, kind, x.id)).map((x) => ({ ...x, add: () => setPin(d, kind, x.id, true) }));
      row("스킬", ...skillKids, ...addPick("skills", "+ 스킬 추가", left("skills")));
      const servers = (deps.ctx.toolServers?.() || []).map((s) => [s.name, s.desc || ""]);
      const tools = listChips(d.tools || [], servers, "+ 도구 추가", (v) => { d.tools = v; touchWorker(); }, "wk-tools");
      tools.classList.add("ex-tools");
      tools.querySelectorAll(".wk-add, datalist").forEach((n) => n.remove()); // the shared "+ 도구 추가" below instead
      tools.append(...addPick("tools", "+ 도구 추가", left("tools")));
      grid.append(deps.el("span", "ex-use-k", "도구"), tools);
      // 기억: pinned memories, then the kinds it reads when it looks things up.
      const memPins = pinOrder(d).filter((p) => p.collection === "memories").map((p) => useChip(d, deps.pins?.known(p)?.title || p.id, { pin: p, bad: badOf(p), onOpen: () => openUseDetail(d, rec, p), onRemove: () => setPin(d, p.collection, p.id, false) }));
      const kinds = toggleChips(MEMORY_KINDS, (k) => !!d.memory?.[k] && d.memory[k] !== "off", (k, on) => {
        const m = { ...(d.memory || {}) };
        if (on) m[k] = "all"; else delete m[k];
        d.memory = m;
        touchWorker();
      });
      for (const k of kinds.children) { k.classList.add("ex-kind"); k.title = deps.tr(MEMORY_KIND_HINT[k.textContent] || ""); }
      row("기억", ...memPins, ...addPick("memories", "+ 기억 추가", left("memories")));
      row("찾아볼 기억", ...kinds.children); // which kinds it searches when it looks things up, apart from the pinned ones
      row("지식", ...pinOrder(d).filter((p) => p.collection === "knowledge_sources").map((p) => useChip(d, deps.pins?.known(p)?.title || p.id, { pin: p, bad: badOf(p), onOpen: () => openUseDetail(d, rec, p), onRemove: () => setPin(d, p.collection, p.id, false) })),
        ...addPick("knowledge_sources", "+ 지식 추가", left("knowledge_sources")));
      const n = (d.pinned || []).length;
      const sum = deps.raw(deps.el("small", "ex-pin-sum", n ? deps.tr(`고정 ${n}개 · 약 ${pinTokens(d).toLocaleString("ko-KR")} 토큰 · 넣는 순서는 고정 칩을 끌어서 바꿔요`) : deps.tr("핀을 꽂으면 부를 때마다 먼저 넣어요")));
      body.replaceChildren(grid, sum);
    };
    deps.pins?.watch(body, draw);
    draw();
    return c;
  }

  // + 추가 (쓸 수 있는 것): one popup to find and add from 기억·스킬·도구·지식 — a search box over tabs and a list.
  function addPick(kind, placeholder, items) {
    const id = `ex-add-${kind}`;
    const dl = deps.el("datalist");
    dl.id = id;
    if (kind === "skills") dl.dataset.tabs = deps.tr("전체");
    items.forEach((it, i) => {
      const o = deps.el("option", "", it.meta || "");
      o.value = it.title;
      o.dataset.key = String(i);
      if (kind === "skills") { const sk = (deps.state.data.skills || []).find((x) => x.id === it.id); o.dataset.tab = (sk && deps.ctx.skillCategory?.(sk)) || deps.tr("분류 안 됨"); }
      dl.append(o);
    });
    const add = deps.input("", { maxLength: 128, placeholder, cls: "wk-add" });
    add.setAttribute("list", id);
    add.dataset.comboEmpty = deps.tr("더할 수 있는 승인된 자료가 없어요.");
    add.addEventListener("change", () => {
      const it = items[Number(add.dataset.comboKey)] || items.find((x) => x.title === add.value.trim());
      add.value = "";
      if (it && it.add() !== false) touchWorker();
    });
    return [add, dl];
  }

  // 칩 상세 (929:61931): what it is, 항상 먼저 넣기, 넣는 방식, 버전 · 새 버전 안내, 이 전문가에서 빼기 · 완료.
  const MODE_LINE = { full: "전체: 내용을 다 넣어요", summary: "요약: 줄여서 넣어요", reference: "참조: 제목만 넣고 필요할 때 읽어요" };

  const USE_KIND_NAME = { skills: "스킬", memories: "기억", knowledge_sources: "지식" };

  function openUseDetail(d, rec, item) {
    const isSkill = item.collection === "skills";
    const sk = isSkill ? skillRec(item.name) : null;
    const id = isSkill ? sk?.id : item.id;
    const known = !isSkill && deps.pins ? deps.pins.known(item) : null;
    const title = isSkill ? item.name : known?.title || item.id;
    const dlg = deps.el("dialog", "crew-dialog ex-skill-dlg");
    const head = deps.el("div", "dialog-head");
    const words = deps.el("div", "ex-dlg-title");
    words.append(deps.el("small", "ex-dlg-kind", USE_KIND_NAME[item.collection] || ""), deps.raw(deps.el("h2", isSkill ? "mono" : "", title)));
    const x = deps.el("button", "icon-btn ghost");
    x.type = "button";
    x.setAttribute("aria-label", "닫기");
    x.append(deps.icon("x"));
    x.addEventListener("click", () => dlg.close());
    head.append(words, x);
    const body = deps.el("div", "dialog-body");
    const foot = deps.el("div", "dialog-foot");
    const draw = () => {
      body.replaceChildren();
      if (isSkill && !sk) { body.append(deps.el("p", "dim", "스킬 목록에서 찾지 못했어요. 이름이 바뀌었거나 아직 승인되지 않았을 수 있어요.")); return; }
      const pin = pinOf(d, item.collection, id);
      const text = isSkill ? sk.description : item.collection === "memories" ? (deps.state.data.memories || []).find((m) => m.id === id)?.text : known?.meta;
      if (text) body.append(deps.raw(deps.el("p", "ex-dlg-text", text)));
      const order = pinOrder(d);
      const tgl = deps.el("div", "ex-pin-toggle");
      const l = deps.el("div", "");
      l.append(deps.el("strong", "", "항상 먼저 넣기"), deps.raw(deps.el("small", "", pin ? deps.tr(`켜면 부를 때마다 먼저 넣어요 · 고정 ${order.length}개 중 ${order.indexOf(pin) + 1}번째`) : deps.tr("켜면 부를 때마다 먼저 넣어요"))));
      tgl.append(l, deps.switchEl(!!pin, "항상 먼저 넣기", (on) => { if (setPin(d, item.collection, id, on)) { touchWorker(); draw(); } }));
      body.append(tgl);
      if (pin) {
        const mode = deps.el("div", "ex-dlg-sec");
        mode.append(deps.el("span", "ex-dlg-k", "넣는 방식"), deps.segment(deps.pins?.MODES || [["full", "전체"], ["summary", "요약"], ["reference", "참조"]], pin.injection || "full", (k) => { setPin(d, item.collection, id, true, { injection: k }); touchWorker(); draw(); }),
          deps.raw(deps.el("small", "", `${deps.tr(MODE_LINE[pin.injection] || "")} · ${deps.tr(`약 ${pinTok(pin).toLocaleString("ko-KR")} 토큰`)}`)));
        const vers = [["0", "최신"]];
        if (isSkill) for (let v = Math.max(skillVerCount(sk), pin.revision || 0); v >= 1; v--) vers.push([String(v), deps.tr(`고정 v${v}`)]);
        else for (const v of new Set([known?.revision, pin.revision].filter(Boolean))) vers.push([String(v), deps.tr(`고정 v${v}`)]);
        const ver = deps.el("div", "ex-dlg-sec");
        const vrow = deps.el("div", "ex-dlg-row");
        vrow.append(pick(vers, String(pin.revision || 0), (v) => { setPin(d, item.collection, id, true, { revision: Number(v) }); touchWorker(); draw(); }, "버전"), deps.el("small", "", "고정이면 이 버전만 써요. 최신이면 새 버전을 따라가요."));
        ver.append(deps.el("span", "ex-dlg-k", "버전"), vrow);
        body.append(mode, ver);
        const act = isSkill ? skillActiveVer(sk) : 0;
        const keep = `${id}:${act}:${pin.revision}`;
        if (isSkill && pin.revision && pin.revision < act && !wk.keptVer?.has(keep)) {
          const note = deps.el("div", "ex-newver");
          const acts = deps.el("div", "ex-newver-acts");
          acts.append(deps.button(deps.tr(`v${act}로 바꾸기`), "primary small", null, () => { setPin(d, item.collection, id, true, { revision: act }); touchWorker(); draw(); }),
            deps.button(deps.tr(`v${pin.revision} 유지`), "small", null, () => { (wk.keptVer ||= new Set()).add(keep); draw(); }));
          note.append(deps.raw(deps.el("span", "", deps.tr(`v${act}가 나왔어요`))), acts);
          body.append(note);
        }
      }
      if (isSkill) {
        const users = agents().filter((r) => (r.crew.skills || []).includes(item.name)).length;
        const at = sk.updated_at ? new Date(sk.updated_at) : null;
        body.append(deps.raw(deps.el("small", "ex-dlg-meta", [deps.tr(`이 스킬을 쓰는 전문가 ${users}명`), at && !isNaN(at) ? deps.tr(`마지막 업데이트 ${String(at.getMonth() + 1).padStart(2, "0")}-${String(at.getDate()).padStart(2, "0")}`) : ""].filter(Boolean).join(" · "))));
      }
    };
    const out = deps.el("button", "ex-dlg-out", "이 전문가에서 빼기");
    out.type = "button";
    out.addEventListener("click", () => { if (id) setPin(d, item.collection, id, false); if (isSkill) d.skills = (d.skills || []).filter((n) => n !== item.name); dlg.close(); touchWorker(); });
    foot.append(out, deps.button("완료", "primary", null, () => dlg.close()));
    dlg.append(head, body, foot);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    draw();
    dlg.showModal();
  }

  // 부르는 곳 (868:61909): the agents as their logos, the rest by name; a pressed one is lit.
  const PLACE_LOGO = { claude_code: "claude", codex: "openai", zcode: "zai", discord: "discord", app: "arkism" };
 // brand.js names
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
      const w = deps.el("label", "ex-money");
      const i = deps.input(d.budget?.[key] || "", { type: "number", min: 0, step, placeholder: ph });
      i.setAttribute("aria-label", label);
      i.addEventListener("input", () => { d.budget = { ...(d.budget || {}), [key]: Number(i.value) > 0 ? Number(i.value) : 0 }; wk.dirty = true; drawWorkerSave(); });
      w.append(deps.el("span", "cur", "$"), i, deps.el("span", "lab", label));
      return w;
    };
    const row = deps.el("div", "scope-fields");
    row.append(money("monthly_usd", "월 한도", "20", "1"), money("ask_over_usd", "1회 한도", "0.50", "0.5"));
    const out = [row];
    if (id) {
      const use = (wk.usage?.agents || []).find((a) => a.id === id);
      const spent = Number(use?.cost_usd) || 0;
      const limit = Number(use?.limit_usd) || Number(d.budget?.monthly_usd) || 0;
      const box = deps.el("div", "ex-spend");
      const top = deps.el("div", "ex-spend-top");
      top.append(deps.el("span", "", "이번 달 한도"), deps.el("span", "", limit ? `$${spent.toFixed(2)} / $${limit} · ${Math.min(100, Math.round(use?.pct ?? (spent / limit) * 100))}%` : `$${spent.toFixed(2)} · 한도 없음`));
      box.append(top);
      if (limit) { const bar = deps.el("div", "wk-bar"); const fill = deps.el("i"); fill.style.width = `${Math.min(100, Math.round(use?.pct ?? (spent / limit) * 100))}%`; bar.append(fill); box.append(bar); }
      const ask = Number(d.budget?.ask_over_usd) || 0;
      const foot = deps.el("div", "ex-spend-top");
      foot.append(deps.el("small", "", "1회 실행 상한"), deps.el("small", "", ask ? `$${ask} · 넘으면 멈추고 물어봐요` : "정하지 않음"));
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
    deps.request(`/v1/discord?${deps.query()}`).then((d) => { wk.bots = (d.bots || []).map(botOf); }, () => { wk.bots = []; }).then(() => wk.pop?.dialog.open && wk.redraw());
  }

  async function saveBotChannels(b, channels, okText) {
    try {
      const saved = await deps.request(`/v1/discord/bots/${encodeURIComponent(b.id)}/update`, { method: "POST", body: JSON.stringify({ scope: deps.state.scope, expected_revision: b.revision,
        bot: { name: b.name, channels, allowed: b.allowed || { user_ids: [], role_ids: [] }, ...(b.target ? { target: b.target } : {}), ...(b.guild_id ? { guild_id: b.guild_id } : {}) } }) });
      Object.assign(b, saved?.id ? botOf(saved) : { channels, revision: (b.revision || 0) + 1 });
      deps.notice(okText);
    } catch (e) { deps.notice(deps.errorText(e)); }
    wk.redraw();
  }

  function channelCard(rec) {
    loadBots();
    const add = deps.button("+ 채널 연결", "ghost small", null, () => { wk.chAdd = wk.chAdd ? null : { bot: wk.bots?.[0]?.id || "", channel: "" }; wk.redraw(); });
    const list = deps.el("div", "ex-ch-list");
    const bots = wk.bots || [];
    const mine = bots.flatMap((b) => (b.channels || []).filter((c) => c.action?.kind === "agent" && c.action.id === rec.id).map((c) => ({ c, b })));
    for (const { c, b } of mine) {
      const r = deps.el("div", "ex-ch-row");
      const mode = deps.button(c.auto_start ? "글이 올라오면 답해요" : `@${rec.crew.handle}로 부를 때만`, "ghost small", null,
        () => saveBotChannels(b, b.channels.map((x) => (x === c ? { ...x, auto_start: !x.auto_start } : x)), "바꿨어요."));
      mode.title = deps.tr("눌러서 바꿔요");
      const warn = ["needs_permission", "token_invalid"].includes(b.state);
      r.append(deps.raw(deps.el("span", "t", `디스코드 · #${c.name || c.channel_id}`)), deps.raw(deps.el("small", "", b.name || "")), ...(warn ? [deps.el("span", "wf-badge small warn", "확인 필요")] : []), mode,
        deps.button("빼기", "ghost small", null, async () => { if (await ask(deps.tr(`#${c.name || c.channel_id} 연결을 뺄까요?`))) saveBotChannels(b, b.channels.filter((x) => x !== c), "연결을 뺐어요.").catch(() => {}); }));
      list.append(r);
    }
    if (wk.bots === null) list.append(deps.el("p", "dim", "불러오는 중…"));
    else if (!mine.length) list.append(deps.el("p", "dim", bots.length ? "연결된 채널이 없어요." : "연결된 디스코드 봇이 없어요."));
    const out = [list];
    if (wk.chAdd && bots.length) {
      const f = wk.chAdd;
      const b = bots.find((x) => x.id === f.bot) || bots[0];
      const known = wk.chKnown?.[b.id];
      if (known === undefined) {
        wk.chKnown = { ...(wk.chKnown || {}), [b.id]: null };
        deps.request(`/v1/discord/bots/${encodeURIComponent(b.id)}/channels?${deps.query()}`).then((r) => r.items || [], () => [])
          .then((items) => { wk.chKnown[b.id] = items; wk.redraw(); });
      }
      const row = deps.el("div", "ex-ch-add");
      const botSel = deps.select(bots.map((x) => [x.id, x.name || x.id]), b.id);
      botSel.className = "wf-select";
      botSel.setAttribute("aria-label", "봇");
      botSel.addEventListener("change", () => { f.bot = botSel.value; f.channel = ""; wk.redraw(); });
      const chSel = deps.select([["", known ? "채널 고르기" : "채널 불러오는 중…"], ...(known || []).map((c) => [c.channel_id, `#${c.name}${c.parent ? ` · ${c.parent}` : ""}`])], f.channel);
      chSel.className = "wf-select";
      chSel.setAttribute("aria-label", "채널");
      chSel.addEventListener("change", () => { f.channel = chSel.value; });
      row.append(botSel, chSel, deps.button("연결", "primary small", null, async () => {
        const id = chSel.value;
        if (!id) { window.ArkismNeed(chSel, "채널을 골라 주세요."); return; }
        const had = (b.channels || []).find((x) => x.channel_id === id);
        if (had && !await ask(deps.tr("이 채널은 이미 다른 곳에 연결돼 있어요. 이 전문가로 바꿀까요?"))) return;
        const ch = { channel_id: id, name: (known || []).find((x) => x.channel_id === id)?.name || "", action: { kind: "agent", id: rec.id }, auto_start: false };
        wk.chAdd = null;
        saveBotChannels(b, [...(b.channels || []).filter((x) => x.channel_id !== id), ch], "채널을 연결했어요.").catch(() => {});
      }));
      out.push(row);
    } else if (wk.chAdd) out.push(deps.el("small", "dim", "먼저 디스코드 봇을 추가해 주세요."));
    out.push(deps.el("small", "dim", "봇 토큰·연결 상태는 설정 › 메신저에서 관리해요. 바꾸면 바로 저장돼요."));
    const c = workerCard("채널 연결", "", ...out);
    c.classList.add("ex-ch");
    c.querySelector(".wk-card-head")?.append(add);
    return c;
  }

  // What goes to the server: the contract fields only, empty ones left out.
  function workerOut(d) {
    const out = { type: "agent", name: (d.name || "").trim(), handle: (d.handle || "").trim().replace(/^@/, ""), role: (d.role || "").trim(), instructions: d.instructions || "",
      model_mode: d.model_mode || "fixed", enabled: d.enabled !== false };
    for (const k of ["category", "model", "fallback_model", "image_model", "effort", "output_format", "file_name", "file_edits", "template", "backend"]) if (String(d[k] || "").trim()) out[k] = String(d[k]).trim();
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
    if (out.model_mode === "fixed" && !out.model) { window.ArkismNeed("기본 모델", "기본 모델을 고르거나 방식을 자동(Clef)으로 바꿔 주세요."); return; }
    if (agents().some((r) => r.id !== rec?.id && r.crew.handle === out.handle) || teams().some((r) => r.crew.handle === out.handle)) { deps.notice(`@${out.handle}은 이미 있어요. 다른 이름을 써 주세요.`); return; }
    wk.busy = true;
    drawWorkerSave();
    try {
      const saved = await deps.save(rec, out);
      // 팀에 넣기 (new agent only): each picked team gets it as a worker at the end.
      let joined = 0;
      for (const tid of rec ? [] : wk.teamsPick) {
        const t = deps.byId(tid);
        if (!t) continue;
        const members = t.crew.members || [];
        try {
          await deps.save(t, { ...t.crew, members: [...members, { agent_id: saved.id, order: members.length + 1, role: "worker" }] });
          joined++;
        } catch (_) { /* reported below */ }
      }
      const fresh = rec ? [] : wk.newTeams || [];
      for (const t of fresh) {
        try {
          await deps.save(null, { type: "team", name: t.name, handle: t.handle, role: "", members: [{ agent_id: saved.id, order: 1, role: "worker" }], mode: "round_robin",
            ending: { max_rounds: 4, token_cap: 100000, end_on_all_approved: true, end_on_moderator: true }, context: [], templates: [] });
          joined++;
        } catch (_) { /* reported below */ }
      }
      if (!rec) wk.newTeams = [];
      deps.notice(rec ? "저장했어요." : wk.teamsPick.size + fresh.length && joined < wk.teamsPick.size + fresh.length ? "만들었어요. 팀에 못 넣은 곳이 있어요 — 팀 화면에서 넣어 주세요." : "전문가를 만들었어요.");
      deps.state.workerNew = false;
      await deps.reload();
      workerPick(saved.id || rec?.id); // the preview server answers {} — keep the open record
      if (!rec && wk.pop) { const dlg = wk.pop.dialog; wk.pop = null; dlg.close(); openExpertPopup(deps.byId(saved.id)); } else { wk.ai = wk.instrAI = null; wk.paintHead?.(); wk.paintAct?.(); wk.redraw?.(); }
    } catch (e) { deps.notice(deps.errorText(e)); } finally { wk.busy = false; drawWorkerSave(); }
  }

  async function removeWorker(rec) {
    const inTeams = teamsOf(rec.id);
    if (inTeams.length) { deps.notice(`${inTeams.map((t) => t.crew.name).join(", ")}에 들어 있어요. 팀에서 먼저 빼 주세요.`); return; }
    if (!await askTyped(`'${rec.crew.name}' 전문가를 삭제할까요?\n되돌릴 수 없어요. 확인하려면 이름 '${rec.crew.name}'을 그대로 입력하세요.`, rec.crew.name, "삭제")) return;
    try {
      await deps.request(`/v1/crew/${encodeURIComponent(rec.id)}`, { method: "DELETE", body: JSON.stringify({ scope: deps.state.scope, expected_revision: rec.revision }) });
      deps.notice("지웠어요.");
      wk.id = null;
      wk.dirty = false;
      wk.pop?.dialog.close();
      await deps.reload();
    } catch (e) { deps.notice(deps.errorText(e)); }
  }

  // AI로 초안 만들기: the fill helper answers name·role·instructions·file_name·skills·tools as JSON, each one a proposal.
  async function draftWorker(ask) {
    const skills = (deps.state.data.skills || []).slice(0, 200).map((s) => s.name).join(", ");
    const tools = (deps.ctx.toolServers?.() || []).map((s) => s.name).join(", ");
    const prompt = `Define a work agent. Request: ${ask}\n\nWrite the instructions in Korean under three Markdown headings in Korean (role, tone, things not to do), briefly (<= 4000 chars).\nSkills available: ${skills || "(none)"}\nMCP servers: ${tools || "(none)"}`;
    const a = await deps.kit.assist("fill", prompt, { persona: { crew: { instructions: "You design AI work agents and write in short, clear Korean." } } });
    if (a.status !== "done") throw new Error(deps.kit.assistError(a));
    const got = deps.kit.parseJSON(a.output || "") || {};
    if (!got.name && !got.instructions) throw new Error("AI 답을 읽지 못했어요. 다시 눌러 주세요.");
    const str = (v, n) => String(v || "").slice(0, n);
    const names = new Set((deps.state.data.skills || []).map((s) => s.name));
    const servers = new Set((deps.ctx.toolServers?.() || []).map((s) => s.name));
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

  const expertPopup = window.ArkismCrewPopup({ el: deps.el, button: deps.button });

  const faceOf = (x, cls = "") => {
    if (!deps.validAvatar(x?.avatar)) { const m = deps.modelMark(x?.model); m.classList.add("ex-mark"); return m; }
    const s = deps.el("span", `ex-art ${cls}`.trim());
    s.style.setProperty("--face-bg", `var(--avatar-${x.avatar.bg})`);
    const img = deps.el("img");
    img.src = `img/avatars/${x.avatar.art}.png`;
    img.alt = "";
    s.append(img);
    return s;
  };

  function expertCard(r) {
    const x = r.crew;
    const b = deps.el("button", `ex-card${x.enabled === false ? " off" : ""}`);
    b.type = "button";
    if (deps.validAvatar(x.avatar)) b.style.setProperty("--face-bg", `var(--avatar-${x.avatar.bg})`);
    const art = deps.el("span", "ex-card-art");
    art.append(faceOf(x));
    b.append(art);
    if (x.category) { const c = deps.el("small", "ex-card-cat"); c.append(deps.icon(deps.ctx.catIcon(x.category)), deps.raw(deps.el("span", "", x.category))); b.append(c); }
    b.append(deps.raw(deps.el("strong", "", x.name)));
    deps.raw(b); // the card holds only the expert's own words, so its title is kept too; the off note is translated here
    b.title = x.enabled === false ? `${x.name} · ${deps.tr("꺼짐")}` : x.name; // 763:57845: an off card is only dimmed
    b.addEventListener("click", () => { workerPick(r.id); openExpertPopup(r); });
    b.ctxItems = () => [{ label: "열기", run: () => b.click() }, { label: "@이름 복사", off: !x.handle, run: () => navigator.clipboard.writeText(`@${x.handle}`).then(() => deps.notice("복사했어요."), () => deps.notice("복사하지 못했어요.")) }];
    return b;
  }

  function renderWorkers() {
    deps.$("#controls").replaceChildren();
    const main = deps.$("#table");
    main.className = "ex-main";
    void loadAgentInfo();
    const all = agents();
    const cats = catsOf(all);
    if (wk.cat && !cats.includes(wk.cat)) wk.cat = "";
    const shown = all.filter((r) => !wk.cat || r.crew.category === wk.cat);
    const panel = deps.el("section", "ex-panel");
    // Same chips as 팀 (tm-cats): icon, name, count.
    const top = deps.el("div", "tm-cats");
    const chips = deps.el("div", "tm-chips");
    for (const key of ["", ...cats]) {
      const c = deps.el("button", `tm-chip${(wk.cat || "") === key ? " active" : ""}`); c.type = "button";
      const name = deps.el("span", "", key || "모든 카테고리");
      if (key) name.setAttribute("data-no-i18n", "");
      c.append(deps.icon(deps.ctx.catIcon(key)), name, deps.el("output", "", String(key ? all.filter((r) => r.crew.category === key).length : all.length)));
      c.addEventListener("click", () => { wk.cat = key; deps.render(false); });
      chips.append(c);
    }
    top.append(chips, deps.button("+ 새 전문가", "primary", null, () => openWorkers(null, true)));
    const grid = deps.el("div", "ex-cards");
    const add = deps.el("button", "ex-card ex-card-new");
    add.type = "button";
    add.append(deps.el("span", "ex-plus", "+"), deps.el("strong", "", "새 전문가"), deps.el("small", "", "페르소나와 워크플로우를 정해 시작해요"));
    add.addEventListener("click", () => openWorkers(null, true));
    grid.append(...shown.map(expertCard), add);
    panel.append(top, grid);
    if (!all.length) panel.append(deps.el("p", "ex-empty", "역할·지시·모델·쓸 수 있는 스킬을 한 번 정해 두면, Claude Code·Codex 어디서든 @이름으로 불러 일을 맡겨요."));
    main.replaceChildren(panel);
    if ((wk.open || deps.state.workerNew) && !wk.pop) {
      wk.open = false;
      const rec = !deps.state.workerNew && wk.id ? deps.byId(wk.id) : null;
      if (deps.state.workerNew || rec) openExpertPopup(rec);
    }
  }


  // ---- inline editing in the popup head (868:61909): click a text, Enter or leaving the box keeps it, Esc cancels.
  function inlineEdit(tag, cls, get, set, { max, ph, validate }) {
    const b = deps.el(tag, `ex-inline ${cls}`);
    b.tabIndex = 0;
    b.setAttribute("role", "button");
    const paint = () => { b.toggleAttribute("data-no-i18n", !!get()); b.textContent = get() || ph; b.classList.toggle("empty", !get()); }; // the user's words stay; only the placeholder is UI text
    paint();
    const start = () => {
      const i = deps.input(get(), { maxLength: max, placeholder: ph, cls: `ex-inline-input ${cls}` });
      i.setAttribute("aria-label", ph);
      b.replaceWith(i);
      i.focus();
      i.select();
      let done = false;
      const finish = (commit) => {
        if (done) return;
        done = true;
        const err = commit ? validate?.(i.value.trim()) : "";
        if (err) deps.notice(err); else if (commit) set(i.value.trim());
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
    const box = deps.el("div", "ex-est");
    const paint = () => {
      const d = wk.draft;
      const row = (wk.usage?.agents || []).find((a) => a.id === wk.id);
      const tok = 1200 + Math.round((d?.instructions || "").length / 2);
      const avg = row?.calls ? row.cost_usd / row.calls : null;
      const secs = (wk.calls || []).filter((c) => c.ok && c.latency_ms > 0).map((c) => Math.max(1, Math.round(c.latency_ms / 1000))).sort((a, b) => a - b);
      const time = !secs.length ? "기록 없음" : secs[0] === secs[secs.length - 1] ? deps.tr(`약 ${secs[0]}초`) : deps.tr(`${secs[0]}~${secs[secs.length - 1]}초`);
      // label · dotted leader · value on each line
      const line = (k, v) => { const r = deps.el("div", "ex-est-row"); r.append(deps.el("span", "", k), deps.el("i", ""), deps.raw(deps.el("span", "v", v))); return r; };
      box.replaceChildren(line("예상 토큰", deps.tr(`약 ${tok >= 1000 ? `${(tok / 1000).toFixed(1)}K` : tok}`)), line("예상 비용", avg == null ? deps.tr("기록 없음") : deps.tr(`약 ${"$" + avg.toFixed(avg < 0.1 ? 3 : 2)}`)), line("예상 시간", time));
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
    wk.versions = undefined; wk.histOpen = new Set(); wk.calls = null; wk.testLog = []; // 테스트 기록 belongs to this expert only
    wk.tab = "edit";
    const face = deps.el("button", "ex-face");
    face.type = "button";
    face.setAttribute("aria-label", "아바타 고르기");
    const paintFace = () => face.replaceChildren(wk.draft.avatar && deps.validAvatar(wk.draft.avatar) ? faceOf(wk.draft, "big")
      : isNew ? deps.el("span", "ex-art big none plus", "+") : (() => { const s = deps.el("span", "ex-art big none"); s.append(deps.avatarMark(wk.draft)); return s; })());
    face.addEventListener("click", async () => {
      const a = await deps.pickAvatar(wk.draft.avatar);
      if (a === undefined) return;
      if (a) wk.draft.avatar = a; else delete wk.draft.avatar;
      wk.dirty = true;
      paintFace();
      drawWorkerSave();
    });
    const acts = deps.el("div", "ex-acts");
    // Each tab marks the window (data-tab) so the head can lay itself out the way that screen's design does.
    const mark = (box, key) => { wk.tab = key; const dl = box.closest("dialog"); if (dl) dl.dataset.tab = key; };
    const tabs = isNew ? [{ key: "edit", label: "새 전문가", render: (box) => { mark(box, "edit"); editBody(box, null); } }]
      : [{ key: "edit", label: "개요", render: (box) => { mark(box, "edit"); editBody(box, deps.byId(wk.id)); } },
        { key: "test", label: "테스트", render: (box) => { mark(box, "test"); drawWorkerSave = baseSave; wk.bar = wk.okBtn = null; box.append(testPanel(deps.byId(wk.id))); } },
        { key: "usage", label: "사용량", render: (box) => { mark(box, "usage"); wk.bar = wk.okBtn = null; usageTab(box, deps.byId(wk.id)); } },
        { key: "history", label: "기록", render: (box) => { mark(box, "history"); wk.bar = wk.okBtn = null; historyTab(box, deps.byId(wk.id)); } }];
    const pop = expertPopup.open({
      title: "", sub: "", avatar: face, tabs, initial: isNew ? "edit" : (wk.openTab || "edit"), actions: [acts], cls: `ex-pop${isNew ? " ex-new" : ""}`, popout: isNew ? null : { event: "arkism:open-expert", detail: { id: wk.id }, title: deps.byId(wk.id)?.crew?.name || "" },
      onClose: () => { if (wk.pop !== pop) return; wk.pop = null; deps.state.workerNew = false; wk.bar = wk.okBtn = wk.estPaint = null; if (isNew && !wk.keep) wk.draft = null; wk.keep = false; deps.render(false); }
    });
    wk.pop = pop;
    wk.openTab = "edit";
    const names = pop.dialog.querySelector(".crew-popup-who > div");
    // 868:61909: the tabs sit in the head (a pill group).
    const headEl = pop.dialog.querySelector(".crew-popup-head");
    headEl.append(pop.dialog.querySelector(".crew-popup-tabs"));
    if (!isNew) {
      deps.request(`/v1/crew/agents/${encodeURIComponent(rec.id)}/calls?${deps.query({ limit: "30" })}`).then((r) => { wk.calls = r.items || []; wk.estPaint?.(); }, () => { wk.calls = []; });
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
      const copy = deps.el("button", cls);
      copy.type = "button";
      copy.setAttribute("aria-label", "@이름 복사");
      copy.append(deps.icon("copy"));
      copy.addEventListener("click", () => { navigator.clipboard?.writeText(`@${d.handle}`).then(() => deps.notice("복사했어요."), () => {}); });
      return copy;
    };
    wk.paintHead = () => {
      const d = wk.draft;
      paintFace();
      const cat = deps.el("button", "ex-cat");
      cat.type = "button";
      cat.setAttribute("aria-label", "카테고리");
      cat.append(...deps.ctx.catChipBody(d.category));
      // A pick repaints the head, so the chip never shows an old choice.
      cat.addEventListener("click", () => deps.ctx.catMenu(cat, { list: catsOf(agents()), current: d.category || "", onPick: (v) => {
        if (v) d.category = v; else delete d.category;
        wk.dirty = true; wk.paintHead(); drawWorkerSave();
      } }));
      const taken = (h) => agents().some((r) => r.id !== wk.id && r.crew.handle === h) || teams().some((r) => r.crew.handle === h);
      const line = deps.el("div", "ex-line");
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
      acts.append(estimateNode()); // no on/off switch here (user request); 켜기 and 전문가 삭제 sit at the end of 개요
    };
    wk.redraw = () => { const sc = wk.scrollKeep ?? pop.body.scrollTop; wk.scrollKeep = sc; pop.select(wk.tab); pop.body.scrollTop = sc; requestAnimationFrame(() => { pop.body.scrollTop = sc; wk.scrollKeep = null; }); };
    if (isNew) names.replaceChildren(deps.el("h2", ""), deps.el("p", ""));
    wk.paintHead();
    wk.paintAct();
  }

  const baseSave = drawWorkerSave;


  // 일별 호출 막대: by_day[].agent comes from /v1/crew/usage?kind=agent&agent_id= (the server adds agent/agent_failed per day); without it the bars stay empty.
  function dailyBars(id, tile) {
    const bars = deps.el("span", "ex-bars");
    bars.setAttribute("aria-hidden", "true");
    tile.append(bars);
    deps.request(`/v1/crew/usage?${deps.query({ range: "30d", kind: "agent", agent_id: id })}`).then((u) => {
      const days = (u.by_day || []).slice(-30).map((x) => [Number(x.agent) || 0, Number(x.agent_failed) || 0]);
      const top = Math.max(0, ...days.map(([a, f]) => a + f));
      if (!top) { bars.remove(); return; } // no per-day agent counts from this server: no flat dotted line
      bars.replaceChildren(...days.map(([a, f]) => { const i = deps.el("i", f ? "bad" : ""); i.style.height = `${Math.max(2, Math.round(((a + f) / top) * 28))}px`; return i; }));
    }, () => {});
  }

  const metric = (big, small) => { const t = deps.el("div", "ex-metric"); t.append(deps.el("strong", "", big), deps.el("small", "", small)); return t; };

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
    const names = new Set((deps.state.data.skills || []).map((s) => s.name));
    const servers = new Set((deps.ctx.toolServers?.() || []).map((s) => s.name));
    const prompt = `Edit a work agent definition. Request: ${ask}\n\nCurrent: ${JSON.stringify({ role: d.role || "", instructions: d.instructions || "", effort: d.effort || "medium", skills: d.skills || [], tools: d.tools || [] })}\n\nAnswer JSON with only the fields that should change, among role (<=200 chars), instructions (Korean Markdown, <=4000 chars), effort (low|medium|high), skills (list), tools (list).\nSkills available: ${[...names].slice(0, 200).join(", ") || "(none)"}\nMCP servers: ${[...servers].join(", ") || "(none)"}`;
    // json, not fill: the fill guide asks for a whole new agent, and the edit prompt describes its own shape
    const a = await deps.kit.assist("json", prompt, { persona: { crew: { instructions: "You edit AI work agents and write in short, clear Korean." } } });
    if (a.status !== "done") throw new Error(deps.kit.assistError(a));
    const got = deps.kit.parseJSON(a.output || "") || {};
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
    const bar = deps.el("div", "ex-savebar");
    const ok = deps.button("저장", "primary small", null, () => saveWorker(rec));
    bar.append(ok, deps.button("취소", "ghost small", null, () => { wk.draft = structuredClone(rec.crew); wk.dirty = false; wk.ai = wk.instrAI = null; wk.paintHead(); wk.paintAct(); wk.redraw(); drawWorkerSave(); }));
    wk.bar = bar;
    wk.okBtn = ok;
    drawWorkerSave = baseSave;
    drawWorkerSave(); // 저장 starts off until something changes
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
      useCard(d, rec), budgetCard(rec ? rec.id : null, d), ...(rec ? [channelCard(rec), enableRow(), dangerRow(rec), bar] : [teamsCard(), bar]));
  }

  // 켜기: the expert's on/off, moved here from the head (#222); the switch is redrawn on a flip and keeps focus.
  function enableRow() {
    const row = deps.el("div", "ex-enable");
    const paint = () => row.replaceChildren(deps.el("strong", "", "켜기"), deps.switchEl(wk.draft.enabled !== false, "켜기", (on) => {
      wk.draft.enabled = on; wk.dirty = true; paint(); row.querySelector(".switch")?.focus(); drawWorkerSave();
    }));
    paint();
    return row;
  }

  // 전문가 삭제: last thing in 개요, apart from everything else, and it asks for the name typed back.
  function dangerRow(rec) {
    const row = deps.el("div", "ex-danger");
    const t = deps.el("div", "");
    t.append(deps.el("strong", "", "전문가 삭제"), deps.el("small", "", "이 전문가와 설정을 지워요. 되돌릴 수 없어요."));
    row.append(t, deps.button("전문가 삭제", "danger small", "trash", () => removeWorker(rec)));
    return row;
  }


  // 테스트 (868:62591): what was asked on the right, the expert's answer under its face, the line to type at the bottom.
  function testPanel(rec) {
    const box = deps.el("div", "ex-test");
    const log = deps.el("ol", "ex-test-log");
    log.setAttribute("aria-live", "polite");
    const turns = (wk.testLog ||= []);
    const paint = () => {
      log.replaceChildren(...turns.flatMap((t) => {
        const me = deps.el("li", "ex-test-me", t.q);
        const ai = deps.el("li", `ex-test-ai${t.busy ? " busy" : ""}${t.err ? " err" : ""}`);
        const who = deps.el("div", "ex-test-who");
        const face = faceOf(rec.crew, "small");
        who.append(face, deps.raw(deps.el("strong", "", rec.crew.name)), deps.raw(deps.el("small", "", [rec.crew.role, rec.crew.model].filter(Boolean).join(" · "))));
        const text = deps.el("div", "ex-test-text", t.a);
        ai.append(who, text);
        if (!t.busy && t.secs) ai.append(deps.raw(deps.el("small", "ex-test-meta", deps.tr(`${t.secs}초`))));
        return [me, ai];
      }));
      if (!turns.length) log.append(deps.el("li", "ex-test-empty", "물어볼 말을 보내면 저장된 설정으로 답해요."));
      log.scrollTop = log.scrollHeight;
    };
    paint();
    const q = deps.input("", { maxLength: 4000, placeholder: deps.tr(`@${rec.crew.handle}에게 맡길 일…`) });
    q.setAttribute("aria-label", "테스트 질문");
    const send = deps.button("실행", "primary", null, async () => {
      if (!q.value.trim()) { q.focus(); return; }
      if (wk.dirty) { deps.notice("저장하지 않은 변경이 있어요. 테스트는 저장된 설정으로 답하니 먼저 저장해 주세요."); return; }
      send.disabled = true;
      const t = { q: q.value, a: "답을 기다리는 중…", busy: true };
      turns.push(t);
      q.value = "";
      paint();
      const t0 = Date.now();
      try {
        const a = await deps.kit.assist("try", t.q, { expert: rec.crew.handle, persona: { crew: { instructions: rec.crew.instructions || "" } } });
        if (a.status === "done") t.a = a.output || "(빈 답)"; else { t.a = deps.kit.assistError(a); t.err = true; }
      } catch (e) { t.a = e.message; t.err = true; }
      t.busy = false;
      t.secs = Math.max(1, Math.round((Date.now() - t0) / 1000));
      if (box.isConnected) paint();
      send.disabled = false;
    });
    q.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.isComposing) { e.preventDefault(); send.click(); } });
    const bar = deps.el("div", "ex-test-bar");
    const hist = deps.el("button", "ex-link", "실행 기록 보기");
    hist.type = "button";
    hist.addEventListener("click", () => { wk.tab = "history"; wk.redraw(); });
    bar.append(q, send, hist);
    box.append(deps.el("p", "ex-test-note", "여기서 보낸 요청도 실제 실행이에요. 실행 기록과 이번 달 한도에 남아요."), log, bar);
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
    const m = deps.el("div", "ex-metrics");
    const runsTile = metric(`${use?.calls || 0}회`, "최근 30일 실행");
    m.append(runsTile, metric(fmtTok(use?.tokens || 0), "전체 토큰"), metric(use ? `$${Number(use.cost_usd || 0).toFixed(2)}` : "$0.00", "추정 비용"));
    dailyBars(rec.id, runsTile);
    return m;
  }

  const histSec = (title, sub, body) => { const s = deps.el("section", "ex-hsec"); const h = deps.el("div", "ex-hsec-head"); h.append(deps.el("h3", "", title), deps.el("small", "", sub)); s.append(h, body); return s; };

  // 사용량 (from Usage › 전문가): the numbers, then every call with its model, tokens, time and cost.
  function usageTab(box, rec) {
    box.classList.add("ex-body");
    box.append(usageMetrics(rec), histSec("사용 기록", "부를 때마다 한 줄씩 남아요", runsBox(rec)));
  }

  function historyTab(box, rec) {
    box.classList.add("ex-body");
    const sec = histSec;
    const vbox = deps.el("div", "tm-hist");
    if (wk.versions === undefined) {
      wk.versions = null;
      deps.request(`/v1/crew/versions/${encodeURIComponent(rec.id)}?${deps.query()}`).then((r) => { wk.versions = r.items || []; }, () => { wk.versions = []; }).then(() => { if (wk.tab === "history") wk.redraw(); });
    }
    const list = wk.versions || [];
    list.forEach((h, i) => {
      const cur = h.revision === rec.revision;
      const r = deps.el("div", "tm-hist-row");
      const sum = list[i + 1] ? agentChanges(list[i + 1].crew, h.crew).slice(0, 2).join(" · ") || "저장" : h.revision === 1 ? "처음 만듦" : "처음 기록";
      r.append(deps.el("b", cur ? "cur" : "", `v${h.revision}`), deps.el("time", "", stamp(h.at)), deps.raw(deps.el("span", "", sum.split(" · ").map((x) => deps.tr(x)).join(" · "))));
      const acts = deps.el("span", "tm-hist-acts");
      if (cur) acts.append(deps.el("em", "", "지금 버전"));
      else {
        const see = deps.el("button", "tm-link quiet", wk.histOpen.has(h.revision) ? "접기" : "바뀐 점 보기"); see.type = "button"; see.setAttribute("aria-expanded", String(wk.histOpen.has(h.revision)));
        see.addEventListener("click", () => { if (!wk.histOpen.delete(h.revision)) wk.histOpen.add(h.revision); wk.redraw(); });
        const back = deps.el("button", "tm-link plain", "이 버전으로 변경"); back.type = "button";
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
      if (wk.histOpen.has(h.revision)) vbox.append(window.ArkismVersionDiff.render(deps.el, list[0].crew, h.crew, `v${h.revision} 버전으로 바꾸면`));
    });
    if (!list.length) vbox.append(deps.el("p", "ex-hist-empty", wk.versions ? "버전 기록이 아직 없어요." : "불러오는 중…"));
    box.append(sec("버전 기록", "저장할 때마다 버전이 하나 생겨요. 다른 버전으로 바꿔도 지금 버전은 남아요.", vbox), sec("최근 실행", "테스트 탭에서 보낸 것도 여기 남아요", runsBox(rec)));
  }

  // Runs of its workflow and its own calls, newest first.
  function runsBox(rec) {
    const rbox = deps.el("div", "tm-hist");
    rbox.append(deps.el("p", "ex-hist-empty", "불러오는 중…"));
    const wf = deps.allWorkflows().find((w) => w.crew.handle === (rec.crew.workflow || "").replace(/^\$/, ""));
    const runsP = wf ? deps.request(`/v1/crew/runs?${deps.query({ crew_id: wf.id, limit: "30" })}`).then((r) => r.items || [], () => []) : Promise.resolve([]);
    const callsP = deps.request(`/v1/crew/agents/${encodeURIComponent(rec.id)}/calls?${deps.query({ limit: "30" })}`).then((r) => r.items || [], () => []);
    Promise.all([runsP, callsP]).then(([runs, calls]) => {
      const rows = [...runs.map((x) => ({ at: String(x.created_at), x })), ...calls.map((c) => ({ at: String(c.at), c }))].sort((a, b) => b.at.localeCompare(a.at));
      if (!rows.length) { rbox.replaceChildren(deps.el("p", "ex-hist-empty", "아직 실행 기록이 없어요.")); return; }
      rbox.replaceChildren(...rows.slice(0, 50).map(({ x, c }) => {
        const row = deps.el("div", "tm-hist-row");
        if (c) {
          const cost = Number(c.cost_usd) || 0;
          const secs = c.latency_ms > 0 ? deps.tr(`${Math.max(1, Math.round(c.latency_ms / 1000))}초`) : "";
          row.append(deps.el("b", c.ok ? "ok" : "warn", c.ok ? "✓" : "!"), deps.el("time", "", stamp(c.at)), deps.el("i", "tm-hist-src", CALL_FROM[c.trigger] || c.trigger || "직접"),
            deps.raw(deps.el("span", "", `${c.model || "-"} · ${fmtTok((c.input_tokens || 0) + (c.output_tokens || 0))} ${deps.tr("토큰")}`)),
            deps.raw(deps.el("small", "", c.ok ? [secs, `$${cost.toFixed(2)}`].filter(Boolean).join(" · ") : [deps.tr("멈춤"), c.error].filter(Boolean).join(" · "))));
          return row;
        }
        const run = x.crew_run || {};
        let said = "";
        try { said = JSON.parse(run.input || "{}").request || ""; } catch { said = run.input || ""; }
        const ok = run.status === "succeeded", live = run.status === "running";
        row.append(deps.el("b", live ? "run" : ok ? "ok" : "warn", live ? "" : ok ? "✓" : "!"), deps.el("time", "", stamp(x.created_at)), deps.el("i", "tm-hist-src", CALL_FROM[run.trigger] || run.trigger || "직접"),
          deps.raw(deps.el("span", "", (said || run.summary || deps.tr("(입력 없음)")).slice(0, 120))), deps.el("small", "", deps.RUN_LABEL[run.status] || run.status || ""));
        return row;
      }));
    }).catch(() => rbox.replaceChildren(deps.el("p", "ex-hist-empty", "실행 기록을 불러오지 못했어요.")));
    return rbox;
  }


  // 새 전문가: start from an existing expert (a copy) instead of empty fields.
  function likeRow() {
    const d = wk.draft;
    const like = toggleChips([["", "빈 칸에서"], ...agents().map((r) => [r.id, r.crew.name])], (k) => (d.copy_of || "") === k, (k) => {
      const src = k ? deps.byId(k)?.crew : null;
      const keepTeams = wk.teamsPick;
      wk.draft = src ? { ...structuredClone(src), name: `${src.name} 복사`.slice(0, 60), handle: `${src.handle || ""}2`.slice(0, 40), copy_of: k, workflow: "" } : blankAgent(); // a workflow has one owner, so the copy starts without it
      wk.teamsPick = keepTeams;
      wk.dirty = !!src;
      wk.paintHead();
      wk.redraw();
    });
    const r = deps.el("div", "ex-like");
    r.append(deps.el("span", "wk-label", "비슷한 것에서 시작"), like);
    return r;
  }

  // 팀에 넣기 (선택): teams the new expert joins once made, or a new team made with it.
  function teamsCard() {
    const teamRow = deps.el("div", "wk-chips");
    for (const t of teams()) {
      const on = wk.teamsPick.has(t.id);
      const b = deps.raw(deps.el("button", `wk-chip${on ? " on" : ""}`, t.crew.name));
      b.type = "button";
      b.setAttribute("aria-pressed", String(on));
      b.addEventListener("click", () => { if (on) wk.teamsPick.delete(t.id); else wk.teamsPick.add(t.id); wk.redraw(); });
      teamRow.append(b);
    }
    for (const [i, t] of (wk.newTeams || []).entries()) {
      const b = deps.el("button", "wk-chip on");
      // the name is read from its own untranslated span, the action from a hidden translatable one
      const name = deps.raw(deps.el("span", "", t.name)), act = deps.el("span", "", "새 팀 빼기");
      name.id = `wk-nt-name-${i}`; act.id = `wk-nt-act-${i}`; act.hidden = true;
      b.append(name, " · ", deps.el("span", "", "새 팀"), act);
      b.type = "button";
      b.setAttribute("aria-pressed", "true");
      b.setAttribute("aria-labelledby", `${act.id} ${name.id}`);
      b.addEventListener("click", () => { wk.newTeams = wk.newTeams.filter((x) => x !== t); wk.redraw(); });
      teamRow.append(b);
    }
    const nt = deps.el("button", "wk-chip", "+ 새 팀");
    nt.type = "button";
    nt.addEventListener("click", () => newTeamPopup());
    teamRow.append(nt);
    return workerCard("팀에 넣기 (선택)", "", deps.el("small", "wk-label", teams().length ? "만든 뒤 팀 화면에서도 넣을 수 있어요" : "아직 팀이 없어요. 새 팀을 만들면 거기서 넣어요."), teamRow);
  }

  // 만들기 · 취소: 취소 asks before throwing away what was typed.
  function createBar() {
    const bar = deps.el("div", "ex-savebar always");
    const ok = deps.button("만들기", "primary small", null, () => saveWorker(null));
    bar.append(ok, deps.button("취소", "ghost small", null, async () => { if (wk.dirty && !await ask("쓰던 내용을 버릴까요?")) return; wk.pop.dialog.close(); }), deps.el("small", "ex-hint", "만들면 바로 @이름으로 부를 수 있어요"));
    wk.bar = null;
    wk.okBtn = ok;
    drawWorkerSave = baseSave;
    return bar;
  }


  // + 새 팀 (310:19277): name and @이름 only. A team needs at least one member, so it is created right after this
  // expert, with the expert as its worker (round_robin); the team screen edits the rest.
  function newTeamPopup() {
    const dlg = deps.el("dialog", "crew-dialog ex-skill-dlg");
    const head = deps.el("div", "dialog-head");
    head.append(deps.el("h2", "", "새 팀"));
    const body = deps.el("div", "dialog-body");
    const name = deps.input("", { maxLength: 60, placeholder: "예: 릴리스 팀" });
    const handle = deps.input("", { maxLength: 40, pattern: "[\\p{L}\\p{N}_\\-]{1,40}", placeholder: "띄어쓰기 없이" });
    name.addEventListener("input", () => { if (!handle.dataset.touched) handle.value = name.value.replace(/\s+/g, "").slice(0, 40); });
    handle.addEventListener("input", () => { handle.dataset.touched = handle.value ? "1" : ""; });
    body.append(deps.field("이름", name), deps.field("@이름", handle, "만들기를 누르면 이 전문가를 넣은 팀이 같이 만들어져요"));
    const foot = deps.el("div", "dialog-foot");
    foot.append(deps.button("취소", "ghost", null, () => dlg.close()), deps.button("추가", "primary", null, () => {
      const n = name.value.trim(), h = handle.value.trim().replace(/^@/, "");
      if (!n) { window.ArkismNeed(name, "이름을 적어 주세요."); return; }
      if (!/^[\p{L}\p{N}_-]{1,40}$/u.test(h)) { window.ArkismNeed(handle, "@이름은 띄어쓰기 없이 글자·숫자·_·-로 적어 주세요."); return; }
      if (h === wk.draft.handle || agents().some((r) => r.crew.handle === h) || teams().some((r) => r.crew.handle === h) || (wk.newTeams || []).some((t) => t.handle === h)) { deps.notice("이미 쓰는 이름이에요."); return; }
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

  return {
    get agents() { return agents; },
    get blankAgent() { return blankAgent; },
    get openWorkers() { return openWorkers; },
    get renderWorkers() { return renderWorkers; },
    get teams() { return teams; },
    get wk() { return wk; },
    get workerOut() { return workerOut; },
    get workerPick() { return workerPick; }
  };
}
