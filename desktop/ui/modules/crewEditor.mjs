// Owns canvas state and graph editing. Dependencies are explicit live ports, never a global scope.
export function createCrewEditor(deps) {
  // A new turn on every open, dropped on leave, so a save that returns after the user left (or reopened) leaves the editor alone.
  const sessions = globalThis.ArkismKeep.latest();
  let session = null;

  // ---------------------------------------------------------------- editor (03a · 03c · 03d) and run view (03b)
  const ed = { rec: null, graph: null, sel: null, selEdge: null, zoom: 1, panX: 0, panY: 0, dirty: false, filter: "", menu: false,
    graphMode: false, review: false, suggestions: null, reviewBusy: false, reviewAI: null, run: null, runList: false, timer: null };

  function openGraph(rec, opts = {}) {
    if (!rec) return;
    session = sessions.begin();
    deps.stopPolling();
    ed.rec = rec;
    // A record with no id is 새 워크플로우 (1511:4650): not saved yet, it opens on the canvas with 직접 부르기 → AI 작업 → 결과.
    ed.graph = structuredClone(rec.crew.graph || deps.defaultGraph(rec.id ? "blank" : "new", rec.crew));
    ed.graph.nodes.forEach((n) => { n.config ||= {}; });
    Object.assign(ed, { sel: null, selEdge: null, zoom: 1, panX: 0, panY: 0, dirty: false, filter: "", menu: false, drafting: false, drafted: null, draftText: "",
      graphMode: !!opts.graphMode || !rec.id, review: !!opts.review, linking: null, fromPopup: !!opts.fromPopup, suggestions: null, reviewBusy: false, reviewAI: null, run: opts.run || null, test: null, trigger: undefined, runList: false, runAfterSave: false });
    if (opts.select && ed.graph.nodes.some((n) => n.id === opts.select)) ed.sel = opts.select;
    Object.assign(hist, { past: [], future: [], base: snap(), saved: snap(), at: 0 });
    // The newest run of this workflow: each selected node's bottom row shows how that node did there.
    ed.last = null;
    if (!ed.run && rec.id) deps.request(`/v1/crew/runs?${deps.query({ crew_id: rec.id, limit: 1 })}`).then((r) => {
      const last = (r.items || []).find((x) => x.crew_run?.crew_id === rec.id);
      if (ed.rec !== rec || !last) return;
      ed.last = last;
      if (!nodeDlg && !ed.run) deps.render(false);
    }, () => {});
    deps.render(); // the editor's own window (crew.js drawGraph)
    if (ed.run) void deps.pollRun();
  }

  async function leaveGraph() {
    if (ed.dirty && !await ask("저장하지 않은 변경이 있어요. 나갈까요?")) return;
    sessions.cancel();
    deps.stopPolling();
    const rec = ed.rec;
    ed.rec = null;
    ed.run = null;
    deps.closeGraph();
    // A workflow an agent works by is edited from that agent, so leaving the editor goes back there (its unsaved draft stays).
    const owner = deps.ownerOf(rec);
    if (owner) { deps.resumeWorker(owner); return; }
    if (ed.fromPopup) deps.getAuto().open(deps.byId(rec.id) || rec); // opened from the workflow window: finish there
  }

  async function saveGraph() {
    try {
      const crew = { ...ed.rec.crew, graph: ed.graph };
      const tr = ed.trigger;
      if (tr?.kind === "schedule" && !deps.parseCron(tr.cron)) { deps.notice("시작 노드의 언제 칸 형식을 확인해 주세요."); return false; }
      if (tr) {
        // A newly set schedule or webhook starts on; one that was already there keeps its on/off.
        if (tr.kind) { crew.trigger = tr.kind === "schedule" ? { kind: "schedule", cron: tr.cron.trim() } : { kind: "webhook", label: tr.label.trim() }; crew.enabled = ed.rec.crew.trigger?.kind === tr.kind ? !!ed.rec.crew.enabled : true; } else { delete crew.trigger; crew.enabled = false; }
      }
      // 이름은 저장할 때 정해요: the first save of a new workflow asks for its name and $이름 in the settings window, which creates it.
      if (!ed.rec.id) { deps.openSettings(null, crew, true); return false; }
      const sent = snap(), at = session;
      const saved = await deps.save(ed.rec, crew);
      if (!at?.live()) { await deps.reload(); return false; } // saved, but the editor it was for is gone: callers (실행) must not go on
      if (snap() === sent) {
        ed.trigger = undefined;
        ed.dirty = false;
        // Older steps hold trigger drafts relative to the trigger just saved, so undo starts over from here.
        const now = snap();
        Object.assign(hist, { past: [], future: [], base: now, saved: now, at: 0 });
      } else {
        const now = snap();
        // Edits made while the save was pending are not saved yet; undo can still step back to what was.
        Object.assign(hist, { past: [sent], future: [], base: now, saved: sent, at: 0 });
        markDirty();
      }
      await deps.reload();
      if (!at?.live()) return false; // left or reopened while the list reloaded
      ed.rec = deps.byId(saved.id) || saved;
      deps.render();
      deps.notice(ed.fromPopup ? "저장했어요. '창으로 돌아가기'를 눌러 마무리해요." : "워크플로우를 저장했어요.");
      void deps.autoTitle(ed.rec);
      return true;
    } catch (e) {
      deps.notice(deps.errorText(e));
      return false;
    }
  }

  const markDirty = () => {
    ed.dirty = true;
    const win = deps.graphBody()?.closest("dialog");
    const save = win?.querySelector(".page-head .g-save"), run = win?.querySelector(".page-head .g-trial");
    if (save) save.disabled = false;
    if (run) { run.disabled = true; run.title = "저장한 뒤에 실행할 수 있어요"; }
    historyButtons();
  };
  const historyButtons = () => {
    const undoB = deps.graphBody()?.querySelector(".g-tools .g-undo"), redoB = deps.graphBody()?.querySelector(".g-tools .g-redo");
    if (undoB) undoB.disabled = !hist.past.length;
    if (redoB) redoB.disabled = !hist.future.length;
  };

  // Undo history: every canvas edit ends in touch(), so it snapshots the draft there.
  // base is the state after the last recorded edit; edits within 500ms of the previous one (typing, a drag) join its step.
  const hist = { past: [], future: [], base: "", saved: "", at: 0 };
  const snap = () => JSON.stringify({ graph: ed.graph, trigger: ed.trigger ?? null });

  // Edits keep the AI suggestions: applying one must not wipe the others (each re-checks with canApply).
  const touch = () => {
    const now = snap(), t = Date.now();
    if (now !== hist.base) {
      if (t - hist.at > 500) { hist.past.push(hist.base); if (hist.past.length > 100) hist.past.shift(); }
      Object.assign(hist, { base: now, at: t, future: [] });
    }
    markDirty();
  };

  function step(from, to) {
    if (!from.length || ed.run || !ed.graph) return;
    to.push(snap());
    hist.base = from.pop();
    hist.at = 0;
    const s = JSON.parse(hist.base);
    ed.graph = s.graph;
    ed.trigger = s.trigger ?? undefined;
    if (ed.sel && !ed.graph.nodes.some((n) => n.id === ed.sel)) ed.sel = null;
    if (ed.selEdge !== null && !ed.graph.edges[ed.selEdge]) ed.selEdge = null;
    ed.linking = null;
    // Stepping back to what was saved leaves nothing to save.
    if (hist.base === hist.saved) { ed.dirty = false; historyButtons(); } else markDirty();
    deps.render(false);
  }
  // A cancelled node window puts the draft back as it was, so its edits leave the history too.
  const keepHistory = () => { const h = { past: [...hist.past], future: [...hist.future], base: hist.base }; return () => { Object.assign(hist, h, { at: 0 }); historyButtons(); }; };
  const undo = () => step(hist.past, hist.future);
  const redo = () => step(hist.future, hist.past);

  function uid(type) {
    let i = 1;
    while (ed.graph.nodes.some((n) => n.id === `${type}${i}`)) i++;
    return `${type}${i}`;
  }

  function addNode(type, preset = {}) {
    const g = ed.graph;
    const anchor = g.nodes.find((n) => n.id === ed.sel) || g.nodes.filter((n) => n.type !== "end").at(-1) || g.nodes.at(-1);
    const n = { id: uid(type === "ai_once" ? "once" : type), type, title: preset.title || deps.NODES[type].label, x: (anchor?.x || 0) + 188 + 67, y: anchor?.y || 120, config: { ...(preset.config || {}) } };
    for (const [k, , kind, opts] of deps.FIELDS[type] || []) if ((kind === "select" || kind === "radio") && n.config[k] === undefined) n.config[k] = opts[0][0];
    // A new AI 작업 starts with no task (it works on what comes in) and the router picking the model.
    if (type === "ai") Object.assign(n.config, { goal: "", access: "read", model: "auto", ...preset.config });
    if (type === "expert") Object.assign(n.config, { goal: "{{input}}", access: "read", ...preset.config });
    if (type === "team") Object.assign(n.config, { topic: "{{input}}", ...preset.config });
    if (type === "ai_once") Object.assign(n.config, { prompt: "{{input}}", ...preset.config });
    if (type === "subflow") n.config.task ||= "{{input}}";
    g.nodes.push(n);
    if (anchor && anchor.type !== "end" && !deps.PORTS[anchor.type] && !deps.wouldCycle(g, anchor.id, n.id)) {
      // Insert after the anchor: anchor → new → anchor's old forward targets (back edges stay where they were).
      const outs = g.edges.filter((e) => e.from === anchor.id && !deps.isBack(e) && !e.port);
      g.edges = g.edges.filter((e) => !outs.includes(e));
      g.edges.push({ from: anchor.id, to: n.id }, ...outs.map((e) => ({ from: n.id, to: e.to })));
    }
    ed.sel = n.id;
    ed.selEdge = null;
    ed.menu = false;
    ed.graphMode = true;
    touch();
    deps.render(false);
    deps.openNodeDialog(n);
  }

  function removeNode(id) {
    const g = ed.graph;
    g.nodes = g.nodes.filter((n) => n.id !== id);
    g.edges = g.edges.filter((e) => e.from !== id && e.to !== id);
    // A new node may reuse this id: drop the suggestions about the removed one so none lands on the newcomer.
    if (ed.suggestions) ed.suggestions = ed.suggestions.filter((x) => !deps.targetsOf(x.action).includes(id));
    ed.sel = null;
    touch();
    deps.render(false);
  }

  const agents = () => deps.items().filter((r) => r.crew.type === "agent");

  // 노드 추가 (1434:8834): AI 작업 · 회의 first, then 전문가 (saved experts, and personas saved before #481 that no expert
  // repeats; both go in as an AI 작업), the rest. A workflow has one start, so the start kinds leave once it has one.
  function presetItems() {
    const out = [];
    const order = ["AI", "전문가", "시작", "처리", "흐름", "알림·끝", "그 밖의 연동"];
    const STARTS = ["start", "schedule", "webhook"];
    const hasStart = ed.graph.nodes.some((n) => STARTS.includes(n.type));
    for (const [type, n] of Object.entries(deps.NODES)) if (!n.hidden && !(hasStart && STARTS.includes(type))) out.push({ group: n.group, type, title: n.label, sub: n.sub, preset: {} });
    const named = new Set();
    for (const a of agents()) { named.add(a.crew.name.toLocaleLowerCase()); out.push({ group: "전문가", type: "ai", title: a.crew.name, sub: a.crew.role || "전문가", expert: a, preset: { title: a.crew.name, config: deps.expertConfig(a) } }); }
    for (const p of deps.personas()) if (!named.has(p.crew.name.toLocaleLowerCase())) out.push({ group: "전문가", type: "ai", title: p.crew.name, sub: "저장한 역할", persona: p, preset: { title: p.crew.name, config: { persona: p.id } } });
    return out.sort((a, b) => order.indexOf(a.group) - order.indexOf(b.group));
  }

  // The add menu is one list under group names; typing in the search narrows it.
  function presetList() {
    const box = deps.el("div", "g-menu");
    const search = deps.el("label", "search");
    const q = deps.el("input");
    q.type = "search";
    q.placeholder = "노드 찾기";
    q.value = ed.filter;
    q.setAttribute("aria-label", "노드 찾기");
    q.addEventListener("input", () => { ed.filter = q.value; box.querySelector(".g-presets").replaceWith(presetGroups()); });
    search.append(deps.icon("magnifying-glass"), q);
    box.append(search, presetGroups());
    return box;
  }

  function presetGroups() {
    const list = deps.el("div", "g-presets");
    const f = ed.filter.trim().toLowerCase();
    let group = "";
    for (const it of presetItems().filter((i) => !f || `${i.title} ${i.sub}`.toLowerCase().includes(f))) {
      if (it.group !== group) { group = it.group; list.append(deps.el("h4", "", group)); }
      const b = deps.el("button", "g-preset");
      b.type = "button";
      const lead = deps.el("span", "g-icon");
      const who = it.expert || it.persona;
      lead.append(who ? deps.avatarMark(who.crew) : deps.icon(deps.NODES[it.type].icon));
      const t = deps.el("div", "");
      t.append(deps.el("strong", "", it.title), deps.el("small", it.expert ? "dim mono" : "dim", it.sub));
      b.append(lead, t, deps.icon("plus", "icon dim"));
      b.addEventListener("click", () => addNode(it.type, it.preset));
      list.append(b);
    }
    if (!list.children.length) list.append(deps.el("p", "dim", "맞는 노드가 없어요."));
    return list;
  }

  function nodeSummary(n) {
    const c = n.config || {};
    const x = ed.rec?.crew;
    if (n.type === "ai") {
      // AI 작업 (1511:5757): the task line; the model only when one is set (자동 needs no row).
      const rows = (c.model && c.model !== "auto") || (!c.model && x?.model) ? [["모델", deps.modelOf(n, x)]] : [];
      rows.push(["할 일", !c.goal?.trim() ? "무엇을 할지 한 줄 적기" : c.goal === "{{input}}" ? "앞 단계 결과로 일해요" : c.goal]);
      if (c.collab === "on") rows.push(["협업", `워커 최대 ${c.max_workers || 5} · 동시 ${c.concurrency || 3}`]);
      return rows;
    }
    if (n.type === "expert") {
      const a = deps.byId(c.expert);
      return [["모델", c.model || a?.crew.model || x?.model || "자동"], ["목표", c.goal && c.goal !== "{{input}}" ? c.goal : "앞 단계 결과로 일해요"]];
    }
    if (n.type === "team") return [["팀", deps.byId(c.team)?.crew.name || "고르지 않았어요"], ["주제", c.topic && c.topic !== "{{input}}" ? c.topic : "앞 단계 결과"]];
    if (n.type === "n8n") return [["주소", (c.url || "").replace(/^https?:\/\//, "") || "—"]];
    if (n.type === "ai_once") return [["모델", deps.modelOf(n, x)], ["질문", c.prompt && c.prompt !== "{{input}}" ? c.prompt : "앞 단계 결과를 그대로 물어요"]];
    if (n.type === "subflow") return [["부르는 것", c.workflow ? deps.alias({ handle: c.workflow.replace(/^\$/, "") }) : "고르지 않았어요"]];
    if (n.type === "code") return [["코드", `${c.lang === "sh" ? "셸" : "Go"} · ${(c.code || "").split("\n").length}줄 · 토큰 0`]];
    if (n.type === "approval") return [["물어볼 것", c.question || "—"]];
    if (n.type === "condition") return [["조건", c.expr || "—"]];
    if (n.type === "schedule") return [["언제", deps.cronText(c.cron)]];
    if (n.type === "http") return [["요청", `${c.method || "GET"} ${c.url || ""}`]];
    if (n.type === "github") return [["gh", c.args || "—"]];
    if (n.type === "wait") return [["기다림", c.duration || "—"]];
    if (n.type === "loop") return [["목록", c.path || "—"]];
    if (n.type === "notify") return [["보내기", `${{ telegram: "텔레그램", slack: "슬랙", email: "메일" }[c.channel] || ""} ${c.to || ""}`]];
    if (n.type === "end") return c.message ? [["형식", c.message]] : [["돌려줄 것", "앞 노드 결과"]];
    if (n.type === "start") {
      // The card follows the trigger being edited (its draft) before the saved one.
      const t = ed.trigger || x?.trigger;
      if (t?.kind === "schedule") return [["언제", deps.cronText(t.cron)]];
      if (t?.kind === "webhook") return [["시작", "주소로 요청이 오면"]];
      return [["부르는 법", `${x?.handle ? deps.alias(x) : "@이름"} · 실행 버튼`]];
    }
    return [];
  }

  // An AI 작업 with nothing to do yet stays marked on the canvas (1511:5757).
  const needsTask = (n) => n.type === "ai" && !n.config?.goal?.trim() && !n.config?.instructions?.trim() && !n.config?.persona;

  function nodeSub(n) {
    const c = n.config || {};
    if (n.type === "ai" && needsTask(n)) return "여기부터 눌러 보세요";
    if (n.type === "ai" || n.type === "expert") return [n.type === "ai" ? "AI 작업" : "전문가", c.collab === "on" ? "협업" : "", n.type === "ai" ? deps.byId(c.from_expert || c.persona)?.crew.name : ""].filter(Boolean).join(" · ");
    if (n.type === "team") return "회의";
    if (n.type === "ai_once") return "한 번 묻기";
    if (n.type === "code") return "코드 · 토큰 0";
    return deps.NODES[n.type]?.label || n.type;
  }


  // 캔버스에서 바로 고치기: the card rows that are one value each. 모델 is a choice, 언제 a small schedule editor, the rest plain text.
  const INLINE = {
    모델: (n) => ["ai", "ai_once", "expert"].includes(n.type) && { key: "model", kind: "model" },
    목표: () => ({ key: "goal", area: true }),
    "할 일": () => ({ key: "goal" }),
    "돌려줄 것": () => ({ key: "message" }),
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
    return (ed.trigger ||= { kind: now?.kind || "", cron: now?.cron || n.config.cron || deps.DEFAULT_CRON, label: now?.label || n.config.note || "" });
  };

  function setCron(n, v) {
    const was = n.config.cron;
    n.config.cron = v;
    if (n.title === deps.cronText(was)) n.title = deps.cronText(v);
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
      deps.render(false);
    };
    const span = s.querySelector("span");
    if (kind === "cron") {
      // 매일·평일·주말 + 시각, or any 5-field cron underneath.
      const box = deps.el("div", "g-inline-cron");
      const f = (c.cron || "0 9 * * *").trim().split(/\s+/);
      const time = deps.el("input");
      time.type = "time";
      time.value = /^\d+$/.test(f[0]) && /^\d+$/.test(f[1]) ? `${f[1].padStart(2, "0")}:${f[0].padStart(2, "0")}` : "09:00";
      const days = deps.select([["*", "매일"], ["1-5", "평일"], ["0,6", "주말"]], f[4] || "*");
      const raw = deps.input(c.cron || "0 9 * * *", { maxLength: 64, cls: "mono" });
      const hint = deps.el("small", "dim");
      const show = () => { const x = deps.parseCron(raw.value); hint.textContent = x ? deps.cronText(raw.value) : "형식: 분 시 일 월 요일 (예: 0 9 * * 1-5)"; };
      const fromParts = () => { const [h, m] = time.value.split(":").map(Number); raw.value = `${m || 0} ${h || 0} * * ${days.value}`; show(); };
      time.addEventListener("change", fromParts);
      days.addEventListener("change", fromParts);
      raw.addEventListener("input", show);
      const row = deps.el("div", "g-inline-row");
      row.append(days, time);
      box.append(row, raw, hint);
      show();
      box.addEventListener("keydown", (e) => {
        if (e.key === "Escape") finish();
        if (e.key === "Enter") { e.preventDefault(); if (deps.parseCron(raw.value)) finish(raw.value.trim()); }
      });
      box.addEventListener("focusout", (e) => { if (!box.contains(e.relatedTarget)) finish(deps.parseCron(raw.value) ? raw.value.trim() : undefined); });
      for (const x of [box, time, days, raw]) x.addEventListener("click", (e) => e.stopPropagation());
      span.replaceWith(box);
      days.focus();
      return;
    }
    const i = kind === "model"
      ? deps.modelSelect(c.model, n.type === "expert" ? "전문가 기본" : ed.rec.crew.model ? `기본 · ${ed.rec.crew.model}` : "기본")
      : deps.input(c[key], { tag: area ? "textarea" : "input", rows: 3, maxLength: 8000 });
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
    const box = deps.graphBody()?.querySelector(`.g-node[data-id="${CSS.escape(n.id)}"]`);
    if (box) {
      box.querySelector(".g-node-head strong").textContent = deps.nodeTitle(n);
      box.querySelector(".g-node-head small").textContent = nodeSub(n);
      box.classList.toggle("needs-task", needsTask(n));
      const rows = box.querySelectorAll(".g-node-field > span");
      nodeSummary(n).forEach(([, v], i) => { if (rows[i]) rows[i].textContent = v; });
    }
    const t = deps.graphBody()?.querySelector(".g-right .g-node-name");
    if (t) t.textContent = deps.nodeTitle(n);
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
    const side = (dir) => ed.graph.edges.filter((e) => (dir ? e.from : e.to) === n.id).map((e) => deps.nodeIn(dir ? e.to : e.from)).filter(Boolean).map((x) => ({ type: x.type, title: deps.nodeTitle(x) }));
    return chat.field(i, {
      state: (talks[key] ||= {}), title: label, sub: `${deps.nodeTitle(n)} · ${deps.NODES[n.type]?.label || n.type}`,
      greeting: "이 칸에 들어갈 글을 같이 다듬어요. 무엇을 바꿀까요?", placeholder: "예: 더 짧고 구체적으로",
      quick: ["더 짧게", "더 구체적으로", "완료 기준 넣기"], preset: AI_PRESET[key],
      context: () => JSON.stringify({ workflow: ed.rec.crew.name, node: { type: n.type, title: deps.nodeTitle(n), config: n.config }, field: key, before: side(false), after: side(true) })
    });
  }

  // 노드를 누르면 오른쪽 패널에 기본 설정이 열리고, 자세히는 노드 설정 팝업을 열어요.
  // An expert or a saved persona shows its face; every other node the icon of its kind.
  const nodeMark = (n) => {
    const who = deps.byId(n.type === "expert" ? n.config?.expert : n.type === "ai" ? n.config?.from_expert || n.config?.persona : null);
    return who ? deps.avatarMark(who.crew) : deps.icon(deps.NODES[n.type]?.icon || "stack");
  };

  function nodePanel(box, n) {
    box.classList.add("node");
    const head = deps.el("div", "g-right-head");
    const lead = deps.el("span", "g-icon");
    lead.append(nodeMark(n));
    const t = deps.el("div", "");
    t.append(deps.el("strong", "g-node-name", deps.nodeTitle(n)), deps.el("small", "dim", deps.nodeLine(n)));
    const close = closeX("노드 설정 닫기", () => selectOnCanvas(null));
    head.append(lead, t, close);
    box.append(head);
    if (n.type === "ai") basicTask(box, n);
    else if (deps.IS_AI(n.type) || n.type === "team") basicAI(box, n);
    else deps.nodeBody(box, n);
    const acts = deps.el("div", "g-right-acts");
    acts.append(deps.button("자세히", "", "arrow-square-out", () => deps.openNodeDialog(n)), deps.button("노드 지우기", "ghost", null, async () => {
      if (await confirmBox("노드를 지울까요?", "이 노드와 이어진 선이 함께 지워져요. 저장하지 않고 나가면 원래대로 돌아가요.", "노드 지우기")) removeNode(n.id);
    }));
    box.append(acts);
    return box;
  }

  // AI 작업의 기본 (1511:5757): 할 일 한 줄과 모델(기본 자동)뿐. 전문가 불러오기 shows once there is an expert; 전문가로 저장 at the bottom.
  function basicTask(box, n) {
    const c = n.config;
    const task = deps.input(c.goal, { maxLength: 8000, placeholder: "무엇을 할지 한 줄 적기" });
    task.addEventListener("input", () => { c.goal = task.value; touch(); syncCard(n); });
    box.append(deps.field("할 일", aiField(task, n, "goal", "할 일"), "비우면 앞 단계 결과로 일해요"));
    const model = deps.modelSelect(c.model, ed.rec.crew.model ? `기본 · ${ed.rec.crew.model}` : "기본");
    model.addEventListener("change", () => { if (model.value) c.model = model.value; else delete c.model; touch(); deps.render(false); });
    box.append(deps.field("모델", model));
    const all = agents();
    if (all.length) {
      const from = deps.byId(c.from_expert);
      const pick = deps.select([["", from ? `${from.crew.name} · 바꾸기` : "고르지 않음"], ...all.filter((a) => a !== from).map((a) => [a.id, a.crew.name])], "");
      for (const o of pick.options) if (o.value) o.setAttribute("data-no-i18n", ""); // saved names stay as written
      pick.addEventListener("change", () => {
        const a = deps.byId(pick.value);
        if (!a) return;
        if (!n.title || n.title === deps.NODES.ai.label || n.title === from?.crew.name) n.title = a.crew.name;
        Object.assign(c, deps.expertConfig(a));
        touch();
        deps.render(false);
      });
      box.append(deps.field("전문가 불러오기", pick, "고르면 그 전문가의 지시·모델을 채워요"));
    }
    box.append(deps.el("p", "meta", "이름, 실행 범위, 나눠서 맡기기, 스킬은 자세히에서 정해요."), deps.button("이 노드를 전문가로 저장", "ghost", "user-plus", () => deps.saveAsExpert(n)));
  }

  // AI·전문가·팀 노드의 기본: 이름 · 모델 · 무엇을 할지. 실행 범위·협업·스킬은 자세히에서.
  function basicAI(box, n) {
    const c = n.config;
    const bind = (i, key) => { i.addEventListener("input", () => { c[key] = i.value; touch(); syncCard(n); }); return i; };
    const title = deps.input(n.title, { maxLength: 80 });
    title.addEventListener("input", () => { n.title = title.value; touch(); syncCard(n); });
    box.append(deps.field("노드 이름", title));
    if (n.type === "expert") box.append(deps.kv([["전문가", deps.byId(c.expert)?.crew.name || "고르지 않았어요"]]));
    if (n.type === "team") box.append(deps.kv([["팀", deps.byId(c.team)?.crew.name || "고르지 않았어요"]]));
    if (n.type === "ai" || n.type === "ai_once") box.append(deps.modelList(), deps.field("모델", bind(deps.modelInput(c.model, ed.rec.crew.model || "deepseek-v4-flash"), "model"), ed.rec.crew.model ? `비우면 ${ed.rec.crew.model}` : ""));
    const slot = "{{input}} 자리에 앞 단계 결과가 들어가요";
    const [key, label] = n.type === "team" ? ["topic", "회의 주제"] : n.type === "ai_once" ? ["prompt", "질문"] : ["goal", "목표"];
    box.append(deps.field(label, aiField(bind(deps.input(c[key], { tag: "textarea", rows: 4, maxLength: key === "topic" ? 4000 : 8000 }), key), n, key, label), slot));
    if (n.type !== "team") {
      const lab = deps.byId(c.persona) ? "더할 지시" : "페르소나 지시";
      box.append(deps.field(lab, aiField(bind(deps.input(c.instructions, { tag: "textarea", rows: 3, maxLength: 8000, placeholder: "너는 ○○다. …한다." }), "instructions"), n, "instructions", lab)));
    }
    box.append(deps.el("p", "meta", "전문가·팀 고르기, 실행 범위, 나눠서 맡기기, 스킬은 자세히에서 정해요."));
  }


  function renderGraph() {
    const t0 = performance.now();
    const rec = ed.rec;
    if (!rec) return;
    const simple = !ed.run && !ed.graphMode && deps.simpleNode(ed.graph);
    if (simple) return deps.renderSimple(simple);
    const page = deps.el("div", `graph-page${ed.run ? " run" : ""}`);
    const stage = deps.el("div", "g-stage");
    const canvas = deps.el("div", "g-canvas");
    canvas.style.transform = `translate(${ed.panX}px, ${ed.panY}px) scale(${ed.zoom})`;
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("class", "g-edges");
    const labels = deps.el("div", "g-labels");
    canvas.append(svg, labels);
    const g = ed.graph;
    const W = 188;
    const nodeEls = new Map();
    const runNodes = (ed.run || ed.test)?.crew_run?.nodes || {};
    const flagged = new Set((ed.review && ed.suggestions || []).flatMap((s) => [s.action?.node, s.action?.from, s.action?.to]).filter(Boolean));
    for (const n of g.nodes) {
      const st = runNodes[n.id];
      const box = deps.el("div", `g-node ${n.type}${ed.sel === n.id ? " selected" : ""}${st ? ` st-${st.status}` : ""}${flagged.has(n.id) ? " flagged" : ""}${!ed.run && needsTask(n) ? " needs-task" : ""}${ed.drafted?.changed.includes(n.id) ? " drafted" : ""}`);
      box.style.left = `${n.x}px`;
      box.style.top = `${n.y}px`;
      box.dataset.id = n.id;
      box.dataset.kind = deps.KIND_OF(n.type);
      const head = deps.el("div", "g-node-head");
      const lead = deps.el("span", "g-icon");
      lead.append(nodeMark(n));
      const t = deps.el("div", "");
      // 테스트 실행 (859:59212–63707): a finished node shows the first line of its result in its subtitle.
      const sub = ed.test && st ? (st.status === "done" && deps.readable(st.output || "").split("\n")[0].slice(0, 28)) || deps.stepState(n, st).split(" · ")[0]
        : st ? [deps.NODE_LABEL[st.status] || st.status, st.runs > 1 ? `${st.runs}회째` : ""].filter(Boolean).join(" · ") : nodeSub(n);
      t.append(deps.el("strong", "", deps.nodeTitle(n)), deps.el("small", "dim", sub));
      head.append(lead, t);
      if (ed.test && st) {
        const mark = deps.el("i", `g-state ${{ done: "done", running: "live", waiting: "live", failed: "fail" }[st.status] || ""}`);
        if (st.status === "done") mark.append(deps.icon("check"));
        head.append(mark);
        if (st.status === "done" && st.started_at && st.finished_at) box.append(deps.el("small", "g-node-dur mono", secText(new Date(st.finished_at) - new Date(st.started_at))));
      } else if (st) head.append(deps.el("i", `dot ${{ done: "ready", running: "warn", waiting: "warn", failed: "error" }[st.status] || ""}`));
      box.append(head);
      for (const [k, v] of nodeSummary(n)) {
        const s = deps.el("div", "g-node-field");
        s.append(deps.el("small", "dim", k), deps.el("span", k === "모델" ? "mono" : "", v));
        const spec = !ed.run && INLINE[k]?.(n);
        if (spec) {
          s.classList.add("editable");
          s.title = "눌러서 바로 고쳐요";
          s.addEventListener("click", (ev) => { if (ed.linking) return; ev.stopPropagation(); if (ed.sel !== n.id) selectOnCanvas(n.id); cardEdit(s, n, spec); });
        }
        box.append(s);
      }
      if (!ed.run && n.type === "ai" && !n.config.from_expert && agents().length) {
        const load = deps.el("button", "g-node-load", "전문가 불러오기 ›");
        load.type = "button";
        load.addEventListener("pointerdown", (ev) => ev.stopPropagation());
        load.addEventListener("click", (ev) => { ev.stopPropagation(); if (ed.sel !== n.id) selectOnCanvas(n.id); deps.openNodeDialog(n); });
        box.append(load);
      }
      if (!["start", "schedule", "webhook"].includes(n.type)) {
        // Several lines into one node: one named input point per line (GPT 의견 · Gemini 의견 …).
        const incoming = g.edges.filter((e) => e.to === n.id);
        if (incoming.length > 1) {
          const ins = deps.el("div", "g-ins");
          for (const e of incoming) {
            const row = deps.el("div", "g-in");
            const dot = deps.el("i", "g-port in");
            dot.dataset.edge = edgeKey(e);
            const src = g.nodes.find((x) => x.id === e.from);
            row.append(dot, deps.el("small", "dim", src ? deps.nodeTitle(src) : e.from));
            ins.append(row);
          }
          head.after(ins);
          box.append(ins);
        } else {
          const dot = deps.el("i", `g-port in${incoming.length ? "" : " unused"}`);
          box.append(dot);
        }
      }
      const portBox = deps.el("div", "g-outs");
      for (const [port, label] of deps.portsOf(n, g)) {
        const row = deps.el("div", "g-out");
        if (label) row.append(deps.el("small", "dim", label));
        const dot = deps.el("i", `g-port out${g.edges.some((e) => e.from === n.id && (e.port || "") === port) ? "" : " unused"}`);
        dot.dataset.port = port;
        dot.title = "끌어서 다음 노드에 이어요";
        if (!ed.run) dot.addEventListener("pointerdown", (ev) => startLink(ev, n, port, canvas, svg));
        row.append(dot);
        portBox.append(row);
      }
      box.append(portBox);
      if (!ed.run && deps.WORK(n) && !deps.ownerOf(rec)) box.append(nodeFoot(n));
      if (!ed.run) head.addEventListener("pointerdown", (ev) => startDrag(ev, n, box, canvas, svg));
      box.addEventListener("click", (ev) => {
        ev.stopPropagation();
        if (ed.linking && !ed.run) { if (ed.linking.from !== n.id) linkTo(deps.nodeIn(ed.linking.from), ed.linking.port, n.id); return; }
        if (!ed.run || ed.sel !== n.id) selectOnCanvas(n.id);
      });
      canvas.append(box);
      nodeEls.set(n.id, box);
    }
    stage.append(canvas);
    stage.addEventListener("click", () => {
      if (ed.justPanned) { ed.justPanned = false; return; }
      if (ed.linking) { ed.linking = null; deps.render(false); return; }
      if (ed.sel || ed.selEdge !== null) selectOnCanvas(null);
    });
    // 화면 이동: 휠 클릭(가운데 버튼)으로 끌거나 스페이스를 누른 채 끌어요 (Figma와 같아요).
    // Panels laid over the canvas (node picker, toolbar) keep their own wheel and drag: the list must scroll, not zoom.
    const onCanvas = (t) => t === stage || canvas.contains(t);
    // Capture phase, so a node under the pointer does not start its own drag.
    stage.addEventListener("pointerdown", (ev) => {
      if (!onCanvas(ev.target) || !(ev.button === 1 || (ev.button === 0 && deps.spaceDown))) return;
      ev.preventDefault();
      ev.stopPropagation();
      const sx = ev.clientX, sy = ev.clientY, px = ed.panX, py = ed.panY, win = stage.ownerDocument.defaultView; // the editor's own window
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
        win.removeEventListener("pointermove", move);
        win.removeEventListener("pointerup", up);
        win.removeEventListener("pointercancel", up);
        stage.classList.remove("panning");
        if (moved && ev.button === 0) ed.justPanned = true;
      };
      win.addEventListener("pointermove", move);
      win.addEventListener("pointerup", up);
      win.addEventListener("pointercancel", up);
    }, true);
    // A middle click would otherwise show the browser's auto-scroll or open a link.
    // and a space-drag would select text or start a native drag that cancels the pointer.
    stage.addEventListener("mousedown", (ev) => { if (onCanvas(ev.target) && (ev.button === 1 || (ev.button === 0 && deps.spaceDown))) ev.preventDefault(); }, true);
    stage.addEventListener("auxclick", (ev) => { if (ev.button === 1) ev.preventDefault(); });
    stage.classList.toggle("space", deps.spaceDown);
    stage.addEventListener("wheel", (ev) => {
      // 휠 = 마우스 자리를 기준으로 확대·축소; 가로 휠(트랙패드)·Shift+휠은 화면 이동.
      if (!onCanvas(ev.target)) return;
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
    const tools = deps.el("div", "g-tools");
    const zoomOut = deps.el("output", "", `${Math.round(ed.zoom * 100)}%`);
    const z = (d) => { ed.zoom = Math.min(2, Math.max(0.4, ed.zoom + d)); deps.render(false); };
    const fit = () => {
      const xs = g.nodes.map((n) => n.x), ys = g.nodes.map((n) => n.y);
      const r = stage.getBoundingClientRect();
      const w = Math.max(...xs) - Math.min(...xs) + W + 80, h = Math.max(...ys) - Math.min(...ys) + 240;
      ed.zoom = Math.min(1.2, Math.max(0.4, Math.min(r.width / w, r.height / h)));
      ed.panX = -Math.min(...xs) * ed.zoom + 40;
      ed.panY = -Math.min(...ys) * ed.zoom + 40;
      deps.render(false);
    };
    if (!ed.run) tools.append(deps.button("노드 추가", "ghost", "plus", (ev) => { ev.stopPropagation(); ed.menu = !ed.menu; deps.render(false); }));
    if (!ed.run) {
      const hb = (cls, ic, label, fn, off) => { const b = deps.button("", `ghost small ${cls}`, ic, fn); b.setAttribute("aria-label", label); b.title = label; b.disabled = off; return b; };
      tools.append(deps.el("i", "g-sep"), hb("g-undo", "caret-left", "되돌리기 (Ctrl+Z)", undo, !hist.past.length), hb("g-redo", "caret-right", "다시 하기 (Ctrl+Shift+Z)", redo, !hist.future.length), deps.el("i", "g-sep"));
    }
    tools.append(deps.button("−", "ghost small", null, () => z(-0.1)), zoomOut, deps.button("+", "ghost small", null, () => z(0.1)), deps.el("i", "g-sep"), deps.button("맞추기", "ghost small", null, fit));
    stage.append(tools);
    if (!ed.run && !ed.menu && (!rec.id || ed.drafted)) stage.append(draftBar());
    // No colour legend: every node already says what it is in words.
    if (ed.menu && !ed.run) {
      const menu = presetList();
      menu.addEventListener("click", (ev) => ev.stopPropagation());
      stage.append(menu);
    }
    if (ed.linking && !ed.run) {
      stage.classList.add("linking");
      const from = deps.nodeIn(ed.linking.from);
      const pick = deps.PORTS[from?.type]?.find(([k]) => k === ed.linking.port)?.[1] || ed.linking.port;
      const src = pick ? `${deps.nodeTitle(from || {})} · ${pick}` : deps.nodeTitle(from || {});
      stage.append(deps.el("div", "g-link-hint", `${src}에서 이을 노드를 누르세요 · Esc 취소`));
      stage.querySelector(`.g-node[data-id="${CSS.escape(ed.linking.from)}"] .g-port.out[data-port="${CSS.escape(ed.linking.port)}"]`)?.classList.add("linking");
    }
    const tb = testBadge();
    if (tb) stage.append(tb);
    let side = deps.inspector;
    if (ed.run) side = deps.runPanel;
    else if (ed.review) side = deps.reviewPanel;
    page.append(ed.run && ed.runList ? deps.runList() : stage, side());
    deps.graphBody().replaceChildren(page);
    requestAnimationFrame(() => {
      drawEdges(svg, labels, nodeEls);
      window.ArkismPerf?.note("canvas.render", performance.now() - t0, g.nodes.length);
    });
  }

  // AI 한 줄로 짜기 (1512:4718): one line and 노드 짜기 (only on the button, never by itself); the nodes it changed stay marked with 되돌리기.
  function draftBar() {
    const bar = deps.el("div", "g-draft");
    for (const t of ["click", "pointerdown", "wheel"]) bar.addEventListener(t, (ev) => ev.stopPropagation());
    const row = deps.el("form", "g-draft-row");
    const q = deps.input(ed.draftText, { maxLength: 500, placeholder: "하고 싶은 일을 한 줄로 적으면 AI가 노드를 짜 줘요 · 예: 매일 아침 열린 PR을 모아 요약해서 보내 줘" });
    q.setAttribute("aria-label", "AI에게 맡길 일");
    q.addEventListener("input", () => { ed.draftText = q.value; });
    const go = deps.button(ed.drafting ? "짜는 중…" : "노드 짜기", "primary small");
    go.type = "submit";
    go.disabled = ed.drafting;
    row.addEventListener("submit", (e) => { e.preventDefault(); void draftNodes(); });
    row.append(q, go);
    bar.append(row);
    if (ed.drafted) {
      const done = deps.el("div", "g-draft-done");
      done.append(deps.el("small", "", `AI가 노드 ${ed.drafted.changed.length}개를 바꿨어요. 바뀐 노드에 표시했어요.`), deps.button("되돌리기", "ghost small", "arrow-counter-clockwise", () => {
        ed.graph = ed.drafted.before;
        Object.assign(ed, { drafted: null, sel: null, selEdge: null });
        touch();
        deps.render(false);
      }));
      bar.append(done);
    }
    return bar;
  }

  async function draftNodes() {
    const text = ed.draftText.trim();
    if (!text) { deps.notice("무엇을 할지 한 줄 적어 주세요."); return; }
    if (ed.drafting) return;
    const at = session, before = structuredClone(ed.graph);
    ed.drafting = true;
    deps.render(false);
    try {
      const { graph: next, trigger } = await deps.draftGraph(text, before);
      if (at !== session) return;
      if (trigger) {
        const start = next.nodes.find((n) => n.type === "start") || next.nodes[0];
        Object.assign(trigDraft(start), trigger);
        if (start?.type === "start") start.title = trigger.kind === "schedule" ? deps.cronText(trigger.cron) : trigger.kind === "webhook" ? "웹훅으로 시작" : start.title;
      }
      const same = (a, b) => JSON.stringify([a.type, a.title, a.config || {}]) === JSON.stringify([b.type, b.title, b.config || {}]);
      const changed = next.nodes.filter((n) => { const o = before.nodes.find((x) => x.id === n.id); return !o || !same(o, n); }).map((n) => n.id);
      Object.assign(ed, { graph: next, drafted: { before, changed }, sel: null, selEdge: null });
      touch();
    } catch (e) {
      if (at === session) deps.notice(e.message || "AI 답을 읽지 못했어요. 다시 눌러 주세요.");
    } finally {
      if (at === session) { ed.drafting = false; deps.render(false); }
    }
  }

  // 실행 완료 · 5.0초 · 5단계 (859:63707): the pill on top of the stage once the editor's test run has ended.
  const secText = (ms) => (ms < 60000 ? `${(ms / 1000).toFixed(1)}초` : deps.spanText(ms));
 // 0.3초 (859:63707)
  function testBadge() {
    const cr = ed.test?.crew_run;
    if (!cr || cr.status === "running") return null;
    const steps = Object.entries(cr.nodes || {}).filter(([id, st]) => st.status === "done" && deps.WORK(deps.nodeIn(id) || {}));
    const t = Object.values(cr.nodes || {}).flatMap((st) => [st.started_at, st.finished_at]).filter(Boolean).map((x) => +new Date(x));
    const span = t.length ? secText(Math.max(...t) - Math.min(...t)) : "";
    const ok = cr.status === "succeeded";
    const b = deps.el("button", `g-test-badge${ok ? "" : " warn"}`);
    b.type = "button";
    b.append(deps.icon(ok ? "check" : "warning-circle"), [ok ? "실행 완료" : deps.RUN_LABEL[cr.status] || cr.status, span, `${steps.length}단계`].filter(Boolean).join(" · "));
    b.title = "실행 기록에서 보기";
    b.addEventListener("click", (ev) => { ev.stopPropagation(); const r = ed.test; ed.test = null; ed.run = r; deps.render(false); void deps.pollRun(); });
    return b;
  }

  const edgeKey = (e) => `${e.from}>${e.to}:${e.port || ""}`;

  // 노드 바닥 줄: the node's last result and ▷ 실행 (this node only); it shows on the selected or hovered node.
  function nodeFoot(n) {
    const foot = deps.el("div", "g-node-foot");
    const st = ed.last?.crew_run?.nodes?.[n.id];
    foot.append(deps.el("small", "dim", st && st.status !== "pending" ? [deps.NODE_LABEL[st.status] || st.status, deps.nodeDur(st), st.tokens ? `${st.tokens.toLocaleString()} 토큰` : ""].filter(Boolean).join(" · ") : "아직 실행 안 함"));
    const go = deps.el("button", "g-node-run");
    go.type = "button";
    go.append(deps.icon("play"), "실행");
    go.title = "이 노드만 실행";
    go.addEventListener("pointerdown", (ev) => ev.stopPropagation());
    go.addEventListener("click", (ev) => {
      ev.stopPropagation();
      const saved = !ed.dirty && (ed.rec.crew.graph?.nodes || []).some((x) => x.id === n.id);
      if (!saved) { deps.notice("저장한 뒤에 실행할 수 있어요."); return; }
      void deps.startRun(ed.rec, "", n.id);
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

  const edgeShape = (e, pa, pb, lift, k) => deps.isBack(e) ? backCurve(pa, pb, lift, k) : { d: curve(pa, pb), mid: { x: (pa.x + pb.x) / 2, y: (pa.y + pb.y) / 2 } };

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
      const back = deps.isBack(e);
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
      const portText = deps.PORTS[fromType]?.find(([k]) => k === e.port)?.[1] || e.port;
      const fan = !back && ed.graph.edges.filter((x) => x.from === e.from && (x.port || "") === (e.port || "") && !deps.isBack(x)).length;
      const text = back ? `${portText ? `${portText} → ` : ""}다시 · ${used ? `${used}/${e.max}번째` : `최대 ${e.max}번`}`
        : deps.IS_AI(fromType) && e.port ? `${e.port} · AI가 고름`
        : [deps.PORTS[fromType] ? portText || "항상" : "", fan > 1 ? `동시에 · ${fan}곳` : ""].filter(Boolean).join(" · ");
      if (text) {
        const chip = deps.el("span", `g-edge-label${back ? " back" : ""}${used ? " used" : ""}`, text);
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
    const page = deps.graphBody().querySelector(".graph-page");
    const side = page?.querySelector(":scope > .g-right");
    if (ed.run) { deps.render(false); return; }
    if (id && ed.review) { ed.review = false; deps.render(false); return; }
    if (!side) deps.render(false);
    else {
      const t0 = performance.now();
      page.querySelectorAll(".g-node.selected").forEach((b) => b.classList.remove("selected"));
      if (id) page.querySelector(`.g-node[data-id="${CSS.escape(id)}"]`)?.classList.add("selected");
      for (const d of page.querySelector(".g-edges")?.drawn || []) d.p.classList.toggle("selected", d.i === edge);
      if (!ed.review) side.replaceWith(deps.inspector());
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
    const win = canvas.ownerDocument.defaultView;
    const up = () => { win.removeEventListener("pointermove", move); win.removeEventListener("pointerup", up); if (moved) touch(); };
    win.addEventListener("pointermove", move);
    win.addEventListener("pointerup", up);
  }

  function startLink(ev, n, port, canvas, svg) {
    ev.stopPropagation();
    ev.preventDefault();
    const box = canvas.querySelector(`.g-node[data-id="${CSS.escape(n.id)}"]`);
    const from = outPoint(box, port);
    const temp = document.createElementNS("http://www.w3.org/2000/svg", "path");
    temp.setAttribute("class", "g-edge drawing");
    svg.append(temp);
    const sx = ev.clientX, sy = ev.clientY, win = canvas.ownerDocument.defaultView;
    const move = (e) => temp.setAttribute("d", curve(from, toCanvas(e, canvas)));
    const up = (e) => {
      win.removeEventListener("pointermove", move);
      win.removeEventListener("pointerup", up);
      temp.remove();
      // A click on the + (no drag): pick the target node next.
      if (Math.abs(e.clientX - sx) + Math.abs(e.clientY - sy) < 5) { ed.linking = { from: n.id, port }; deps.render(false); return; }
      const target = canvas.ownerDocument.elementFromPoint(e.clientX, e.clientY)?.closest(".g-node"); // the editor's own window
      if (target && target.dataset.id !== n.id) linkTo(n, port, target.dataset.id);
    };
    win.addEventListener("pointermove", move);
    win.addEventListener("pointerup", up);
  }


  // A new line from n's port to node `to`; it opens in the right panel to pick its kind.
  function linkTo(n, port, to) {
    ed.linking = null;
    const toNode = deps.nodeIn(to);
    if (!n || !toNode || to === n.id) { deps.render(false); return; }
    if (["start", "schedule", "webhook"].includes(toNode.type)) { deps.notice("시작 노드에는 선을 이을 수 없어요."); deps.render(false); return; }
    if (ed.graph.edges.some((x) => x.from === n.id && x.to === to && (x.port || "") === port)) { deps.render(false); return; }
    const edge = { from: n.id, to };
    if (port) edge.port = port;
    if (deps.wouldCycle(ed.graph, n.id, to)) {
      edge.max = 2;
      deps.notice("되돌아가는 선이에요. 최대 2번까지 다시 돌아요. 선을 눌러 횟수와 갈래 이름을 바꿀 수 있어요.");
    }
    ed.graph.edges.push(edge);
    ed.selEdge = ed.graph.edges.length - 1;
    ed.sel = null;
    ed.review = false;
    touch();
    deps.render(false);
  }

  // 지우기 확인 (241:27886 · 241:27897): a small window instead of window.confirm.
  function confirmBox(title, text, okLabel) {
    return new Promise((resolve) => {
      const dlg = deps.el("dialog", "crew-dialog wf-dialog wf-confirm");
      const form = deps.el("form");
      form.method = "dialog";
      const body = deps.el("div", "dialog-body");
      body.append(deps.el("h2", "", title), deps.el("p", "", text));
      const foot = deps.el("div", "dialog-foot");
      const cancel = deps.button("취소", "");
      cancel.type = "submit";
      cancel.value = "cancel";
      const ok = deps.button(okLabel, "danger");
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
    const x = deps.el("button", "icon-btn plain");
    x.type = "button";
    x.setAttribute("aria-label", label);
    x.append(deps.icon("x"));
    x.addEventListener("click", onClick);
    return x;
  };

  const deselect = () => { ed.sel = null; ed.selEdge = null; deps.render(false); };

  // Field builders call redraw(): the canvas normally, the open node window while one is up.
  let redraw = () => deps.render(false);

  let nodeDlg = null;

  return {
    get aiField() { return aiField; },
    get closeX() { return closeX; },
    get confirmBox() { return confirmBox; },
    get draftNodes() { return draftNodes; },
    get deselect() { return deselect; },
    get ed() { return ed; },
    get keepHistory() { return keepHistory; },
    get leaveGraph() { return leaveGraph; },
    get linkTo() { return linkTo; },
    get needsTask() { return needsTask; },
    get nodeDlg() { return nodeDlg; },
    set nodeDlg(value) { nodeDlg = value; },
    get nodePanel() { return nodePanel; },
    get openGraph() { return openGraph; },
    get redo() { return redo; },
    get redraw() { return redraw; },
    set redraw(value) { redraw = value; },
    get removeNode() { return removeNode; },
    get renderGraph() { return renderGraph; },
    get saveGraph() { return saveGraph; },
    get syncCard() { return syncCard; },
    get touch() { return touch; },
    get trigDraft() { return trigDraft; },
    get undo() { return undo; },
    get uid() { return uid; }
  };
}
