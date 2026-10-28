import { createCrewVocabulary } from "./modules/crewVocabulary.mjs";
import { createCrewModel } from "./modules/crewModel.mjs";
import { createCrewExecution } from "./modules/crewExecution.mjs";
import { createCrewSchedule } from "./modules/crewSchedule.mjs";
import { createCrewEditor } from "./modules/crewEditor.mjs";
import { createCrewNodes } from "./modules/crewNodes.mjs";
import { createCrewReview } from "./modules/crewReview.mjs";
import { createCrewRunView } from "./modules/crewRunView.mjs";
import { createCrewPersonas } from "./modules/crewPersonas.mjs";
import { createCrewWorkers } from "./modules/crewWorkers.mjs";
import { FLOW_KINDS, flowItems, flowAlias, flowKind } from "./modules/flowCatalog.mjs";
import { createFlowHistory, flowNeedsDecision } from "./modules/flowHistory.mjs";
// FLOW keeps role, team and workflow definitions intact and presents their existing editors in one catalogue.
// app.js passes the shared helpers in once; this file owns the list, the detail panel, the settings window, the
// graph editor (with the one-step simple view and the structure review) and the run view.
// A one-node run takes JSON (an object or a list) as is, so a loop or code node can be tried on real structure.
function runInput(text, only) {
  if (only && /^[[{]/.test((text || "").trim())) {
    try { return JSON.stringify(JSON.parse(text)); } catch { /* not JSON: send it as a request text */ }
  }
  return JSON.stringify({ request: text || "" });
}
export const createCrew = (ctx) => {
  // 팀 이름(데이터)과 화면 문구가 한 줄에 섞일 때: 줄은 raw로 두고 문구만 만들 때 번역한다.
  const raw = (n) => { n.setAttribute("data-no-i18n", ""); return n; };
  const tr = (s) => (window.ArkismI18n ? window.ArkismI18n.translate(s) : s);
  const { state, $, el, icon, button, segment, render, request, query, notice, detailShell, section, copyButton, markSelected, ago, models } = ctx;
  const kit = window.ArkismAssist(ctx);
  const pins = window.ArkismPins?.(ctx);
  const crewVocabulary = createCrewVocabulary({

  });


  // ---------------------------------------------------------------- data
  const items = () => state.data.crew || [];
  // A workflow an agent works by belongs to that agent: no $ call, and no place in 자동화.
  const ownerOf = (w) => items().find((r) => r.crew.type === "agent" && r.crew.workflow && r.crew.workflow.replace(/^\$/, "") === w.crew.handle);
  const allWorkflows = () => items().filter((r) => r.crew.type === "workflow");
  const workflows = () => allWorkflows().filter((r) => !ownerOf(r));
  const flows = () => flowItems(items());
  const personas = () => items().filter((r) => r.crew.type === "persona");
  // template: a saved agent definition (instructions·model·effort·skills). 새 워크플로우 can start from one.
  const templates = () => items().filter((r) => r.crew.type === "template");
  const byId = (id) => items().find((r) => r.id === id);
  const flowHistory = createFlowHistory({ state, el, button, request, query, notice, byId, ago, go: ctx.go });
  const byHandle = (h) => workflows().find((r) => r.crew.handle === (h || "").replace(/^\$/, ""));
  async function reload(selectId) {
    state.data.crew = (await request(`/v1/crew?${query()}`)).items || [];
    if (selectId !== undefined) state.selected = selectId ? `crew:${selectId}` : null;
    render();
    if (crewWorkers.wk.pop?.dialog.open) crewWorkers.wk.redraw?.(); // an open expert popup lists the workflow just made (render skips it)
  }
  const ERRORS = {
    secret_detected: "키나 토큰처럼 보이는 값이 있어 저장하지 않았어요. 키는 설정 › 에이전트에 넣어 주세요.",
    invalid_request: "빈 칸이나 형식을 확인해 주세요. $이름은 띄어쓰기 없이 글자·숫자·_·-만 써요. 앞으로 가는 선이 원을 이루면 저장되지 않아요(되돌아가는 선은 횟수를 정해요).",
    revision_conflict: "다른 곳에서 먼저 고쳤어요. 닫고 다시 열어 주세요.",
    handle_taken: "이미 쓰는 이름이에요. 다른 이름을 써 주세요."
  };
  const errorText = (e) => ERRORS[e.message] || (e.message === "HTTP 409" ? ERRORS.handle_taken : `저장하지 못했어요 (${e.message}).`);
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
  const crewModel = createCrewModel({
    get byId() { return byId; },
    get IS_AI() { return crewVocabulary.IS_AI; },
    get NODES() { return crewVocabulary.NODES; },
    get PORTS() { return crewVocabulary.PORTS; },
    get state() { return state; }
  });

  const crewExecution = createCrewExecution({
    get notice() { return notice; },
    get request() { return request; },
    get runInput() { return runInput; },
    get state() { return state; },
    get stepCount() { return crewModel.stepCount; },
    get upstream() { return crewModel.upstream; }
  });


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
  // "auto" names the router that picks (Clef, Jev…), read once from the routing overview; older servers keep the plain label.
  let router = "";
  request(`/v1/routing/overview?${query({ range: "1h" })}`).then((d) => {
    router = d?.policy?.classifier || "";
    for (const o of document.querySelectorAll("option[data-router]")) { o.label = autoLabel(); if (o.parentElement?.tagName === "SELECT") o.textContent = o.label; }
  }, () => {});
  const autoLabel = () => (router ? tr(`자동 — ${router} 라우터가 일마다 골라요`) : tr("자동 — 라우터가 일마다 골라요"));
  // The same live list as a plain choice (자동화 팝업, 그래프 노드): "" = the fallback, "auto" = the router picks, then the models grouped
  // like 설정 › 역할별 모델 (logo, MAX·PRO·API, input kind). The value is the plain model name; a saved model off the list stays choosable.
  function modelSelect(value, emptyLabel) {
    const s = select([["", emptyLabel], ["auto", autoLabel()]], value || "");
    s.querySelector('option[value="auto"]').dataset.router = "1";
    for (const p of models?.pickerGroups?.() || []) s.append(models.modelGroup(p, (m) => m));
    if (value && value !== "auto" && ![...s.querySelectorAll("option")].some((o) => o.value === value)) s.append(Object.assign(el("option", "", value), { value }));
    s.value = value || "";
    return s;
  }
  // only: keep the models this returns true for (the 이미지 picker lists vision models only).
  function modelList(id = "crew-models", only = null) {
    const dl = el("datalist");
    dl.id = id;
    // "auto" lets the router pick per run; the rest is the live list the runner reports (설정 › 에이전트), never a fixed one.
    const auto = el("option");
    auto.value = "auto";
    auto.label = autoLabel();
    auto.dataset.router = "1";
    if (!only) dl.append(auto);
    // Grouped by provider, the provider's logo before its name; each model says on the right what it takes in. The server keeps a plain model name (no provider), so a name offered by several providers
    // says so on every copy: picking either saves the same name, and the runner picks the CLI from the name.
    // One group per way a provider is connected (MAX·PRO·API, as in 설정 › 역할별 모델).
    const byProvider = models?.pickerGroups?.() || [];
    const where = new Map();
    for (const p of byProvider) for (const m of p.models) where.set(m, [...new Set([...(where.get(m) || []), p.name])]);
    for (const p of byProvider) for (const m of p.models) {
      if (only && !only(m)) continue;
      const o = el("option");
      o.value = m;
      o.label = where.get(m).join(" · "); // 1209:68243: every row names its provider; a model on several routes lists them all
      o.dataset.group = p.name;
      o.dataset.groupIcon = p.id;
      o.dataset.groupBadges = o.dataset.badges = p.badges.join(",");
      o.dataset.tag = tr(window.ArkismBrand?.kindLabel(m) || "");
      dl.append(o);
    }
    return dl;
  }
  const crewSchedule = createCrewSchedule({

  });


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
    return [...waiting, ...(recent || [])].filter((x) => x.crew_run?.status === "waiting" && flowNeedsDecision(x.crew_run.waiting) && !seen.has(x.id) && seen.add(x.id));
  };
  function categories(list) {
    const counts = new Map();
    list.forEach((r) => { if (r.crew.category) counts.set(r.crew.category, (counts.get(r.crew.category) || 0) + 1); });
    return [...counts.entries()].sort((a, b) => a[0].localeCompare(b[0], "ko"));
  }
  const shownCards = () => flows().filter((r) => (!state.flowKind || state.flowKind === "all" || r.crew.type === state.flowKind) && (state.crewCat === "all" || r.crew.category === state.crewCat) &&
    (!state.query || `${r.crew.name} ${r.crew.handle || ""} ${r.crew.role || ""}`.toLowerCase().includes(state.query)));
  // 자동화 상세·새로 만들기 팝업 (crew-automation.js): built on first use, once every helper below exists.
  let autoKit = null;
  const getAuto = () => (autoKit ||= window.ArkismAutomation({
    state, el, icon, button, notice, request, query, byId, kit, catMenu: ctx.catMenu, catChipBody: ctx.catChipBody, NODES: crewVocabulary.NODES, IS_AI: crewVocabulary.IS_AI, WORK: crewModel.WORK, UNTITLED, RUN_LABEL: crewExecution.RUN_LABEL,
    levels: crewModel.levels, nodeTitle: crewModel.nodeTitle, stepCount: crewModel.stepCount, isBack: crewModel.isBack, modelOf: crewModel.modelOf, modelMark, avatarMark, personas, agents: crewWorkers.agents, workflows, ownerOf, categories, ruleChecks: crewModel.ruleChecks, waitingRuns,
    cronText: crewSchedule.cronText, nextRun: crewSchedule.nextRun, parseCron: crewSchedule.parseCron, when: crewSchedule.when, switchEl, copyButton, coverCredit, coverPicker, input, select, modelInput, modelSelect, freeHandle, layoutGraph, defaultGraph: crewModel.defaultGraph,
    save, reload, archive, remove, errorText, openRunResult: flowHistory.openResult, startRun: crewExecution.startRun, decide: crewExecution.decide, stopRun: crewExecution.stopRun, askTask: crewRunView.askTask, openGraph: crewEditor.openGraph, runRequest, readable, nodeDur: crewRunView.nodeDur, spanText, runMs, runTookText: runTook, ago,
    duplicate: (r) => dupButton(r).click(),
    flowAlias: (crew) => flowAlias({ crew }, items()),
    refreshRuns: async () => { recent = null; await loadRecent(); },
    openSettingsNew: () => openSettings(null),
    openSettings: (r) => openSettings(r),
    openAutomation: (id, tab) => { const r = byId(id); if (r) getAuto().open(r, tab); },
    afterClose: () => { markSelected(); if (state.view === "crew") drawCards(); }
  }));
  function renderList() {
    const all = flows();
    const cats = categories(all);
    if (state.crewCat !== "all" && !cats.some(([c]) => c === state.crewCat)) state.crewCat = "all";
    // 카테고리 칩 · 검색 · 실행 방식 · 새 워크플로우: one bar (the search box here and the one on top share state.query)
    $("#app").classList.add("wide"); // the card opens a popup: no side panel
    const bar = el("div", "wf-bar auto-bar-top");
    const chips = el("div", "auto-cats");
    const chip = (k, label, n) => {
      const c = el("button", `auto-cat-chip${state.crewCat === k ? " on" : ""}`);
      c.type = "button";
      c.setAttribute("aria-pressed", String(state.crewCat === k));
      c.append(icon(ctx.catIcon(k === "all" ? "" : k)), k === "all" ? label : raw(el("span", "", label)), el("small", "", String(n)));
      c.addEventListener("click", () => { state.crewCat = k; state.selected = null; render(); });
      return c;
    };
    chips.append(chip("all", "모든 카테고리", all.length), ...cats.map(([c, n]) => chip(c, c, n)));
    bar.append(chips, el("span", "grow"));
    const search = el("label", "search wf-search");
    const q = el("input");
    q.type = "search";
    q.placeholder = "이름이나 @이름 검색";
    q.setAttribute("aria-label", "워크플로우 검색");
    q.value = $("#search")?.value || "";
    q.addEventListener("input", () => {
      state.query = q.value.trim().toLowerCase();
      if ($("#search")) $("#search").value = q.value;
      drawCards();
    });
    search.append(icon("magnifying-glass"), q);
    const kind = el("select", "wf-select");
    kind.setAttribute("aria-label", "실행 방식");
    for (const [key, label] of FLOW_KINDS) {
      const option = el("option", "", label);
      option.value = key;
      option.selected = key === (state.flowKind || "all");
      kind.append(option);
    }
    kind.addEventListener("change", () => { state.flowKind = kind.value; state.selected = null; drawCards(); });
    bar.append(search, kind, button("+ 새 워크플로우", "primary", null, openFlowCreate));
    $("#controls").replaceChildren(bar);
    $("#chips").replaceChildren();
    $("#stats").replaceChildren();
    const main = $("#table");
    main.className = "wf-main";
    main.replaceChildren(el("div", "wf-grid"));
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
    if (state.mode === "local") out.push(el("p", "local-note wf-local", "이 PC에서는 만들고 저장만 돼요. 실행은 서버에 연결하면 돼요."));
    for (const run of waitingRuns()) {
      const rec = byId(run.crew_run.crew_id);
      const node = (rec?.crew.graph?.nodes || []).find((n) => n.id === run.crew_run.waiting.node_id);
      const card = el("div", "wf-attn");
      const t = el("div", "");
      t.append(el("strong", "", `확인해 주세요 · ${rec?.crew.name || "워크플로우"}`), el("p", "", `${node ? `${crewModel.nodeTitle(node)} — ` : ""}${run.crew_run.waiting.question || node?.config?.question || "다음 단계로 갈까요?"}`));
      const acts = el("div", "wf-attn-actions");
      const after = async () => { recent = null; await loadRecent(); };
      acts.append(button("거절", "", null, async () => { await crewExecution.decide(run, false); void after(); }), button("승인", "primary", null, async () => { await crewExecution.decide(run, true); void after(); }));
      // a FLOW run opens its result (final text, delivery) in its own 기록; others open the step view
      if (rec) acts.prepend(button("실행 보기", "ghost", null, () => run.crew_run.invocation_mode === "flow" ? openFlow(rec, run) : crewEditor.openGraph(rec, { run })));
      card.append(t, acts);
      out.push(card);
    }
    box.replaceChildren(...out);
  }
  function drawCards() {
    const grid = $("#table .wf-grid");
    if (!grid) return;
    grid.replaceChildren(...shownCards().map(flowCard));
    markSelected();
  }
  // run: a FLOW run to show in its 기록 (a workflow opens it directly; an agent or team lists it under 부른 실행 결과)
  function openFlow(rec, run = null) {
    if (rec.crew.type === "workflow") { getAuto().open(rec, run ? "history" : undefined, run); return; }
    window.dispatchEvent(new CustomEvent(rec.crew.type === "team" ? "arkism:open-team" : "arkism:open-expert", { detail: { id: rec.id, over: true, ...(run ? { tab: "history" } : {}) } }));
  }
  function flowCard(rec) {
    if (rec.crew.type === "workflow") return workflowCard(rec);
    const x = rec.crew;
    const card = raw(el("button", `crew-card workflow wf-card flow-card${x.cover?.url ? " has-cover" : ""}${x.enabled === false ? " flow-disabled" : ""}`));
    card.type = "button";
    card.dataset.id = `crew:${rec.id}`;
    card.dataset.flowKind = x.type;
    card.title = x.enabled === false ? `${x.name} · ${tr("꺼짐")}` : x.name;
    const top = el("div", "flow-card-top");
    const alias = raw(el("code", "flow-alias", flowAlias(rec, items())));
    if (x.cover?.url) {
      // On a photo the card reads like a workflow card (name, @name); the avatar moves to the bottom-right corner.
      // a photo that fails to load: the card is drawn again as one without a photo (role, category, avatar top-left)
      coverImg(card, x.cover, () => card.replaceWith(flowCard({ ...rec, crew: { ...x, cover: undefined } })));
      top.append(el("span", "flow-kind", tr(flowKind(rec))));
      const foot = el("div", "flow-card-foot");
      foot.append(alias);
      if (validAvatar(x.avatar)) foot.append(avatarMark(x));
      card.append(top, raw(el("strong", "crew-cover-title", x.name)), foot);
    } else {
      top.append(avatarMark(x), el("span", "flow-kind", tr(flowKind(rec))));
      card.append(top, raw(el("strong", "crew-cover-title", x.name)), raw(el("p", "flow-card-role", x.role || "")), raw(el("small", "flow-card-category", x.category || "")), alias);
    }
    card.addEventListener("click", () => openFlow(rec));
    card.ctxItems = () => [{ label: "열기", run: () => card.click() }, { label: "@이름 복사", off: !x.handle, run: () => Promise.resolve().then(() => navigator.clipboard.writeText(flowAlias(rec, items()))).then(() => notice("복사했어요."), () => notice("복사하지 못했어요.")) }];
    return card;
  }
  function openFlowCreate() {
    const dlg = el("dialog", "crew-dialog flow-create");
    dlg.setAttribute("aria-label", "새 워크플로우");
    const head = el("header", "flow-create-head");
    head.append(el("h2", "", "새 워크플로우"), button("닫기", "ghost", null, () => dlg.close()));
    const choices = el("div", "flow-create-options");
    const choose = (label, desc, iconName, action) => {
      const pick = el("button", "flow-create-option");
      pick.type = "button";
      const text = el("span");
      text.append(el("strong", "", label), el("small", "", desc));
      pick.append(icon(iconName), text);
      pick.addEventListener("click", () => { dlg.close(); action(); });
      return pick;
    };
    choices.append(
      choose("리서치", researchRecord() ? "지금 리서치에 개선안을 받아 검토해요" : "출처를 찾아 정리하는 리서치를 만들어요", "magnifying-glass", () => void previewResearch()),
      choose("단독 실행", "역할·모델·도구를 정해 혼자 일해요", "robot", () => window.dispatchEvent(new CustomEvent("arkism:open-expert", { detail: { create: true, over: true } }))),
      choose("협업·회의", "여러 역할이 회의해요", "users-three", () => window.dispatchEvent(new CustomEvent("arkism:open-team", { detail: { over: true } }))),
      choose("단계 구성", "일정·분기·승인 순서를 정해요", "tree-structure", () => getAuto().create())
    );
    dlg.append(head, choices);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal();
    choices.querySelector("button")?.focus();
  }
  async function previewResearch() {
    try {
      const proposal = await request(`/v1/crew/templates/research?${query()}`);
      if (proposal.crew?.type !== "workflow" || !proposal.crew.graph || (proposal.existing_id && (!Number.isInteger(proposal.expected_revision) || proposal.expected_revision < 1))) throw new Error("리서치 개선안을 불러오지 못했어요.");
      const existing = proposal.existing_id ? { id: proposal.existing_id, revision: proposal.expected_revision, crew: proposal.crew } : null;
      openSettings(existing, proposal.crew, false, "", proposal);
    } catch (error) { notice(errorText(error)); }
  }
  function researchRecord() {
    const named = items().filter((r) => ["agent", "team", "workflow"].includes(r.crew.type) && r.crew.handle === "리서치");
    return named.find((r) => r.crew.type === "workflow") || named[0];
  }
  function coverImg(card, cover, onFail = (img) => { img.remove(); card.classList.remove("has-cover"); }) {
    const img = el("img", "crew-cover-img");
    img.alt = "";
    img.loading = "lazy";
    img.src = cover.url;
    img.addEventListener("error", () => onFail(img));
    card.append(img);
  }
  // Card (198:286): photo, the 일정 badge and the name. The 일정·웹훅 switch, one line and $이름 · 단계 live in the popup.
  // A cover photo (배경 사진) sits as a band on top of the card.
  function workflowCard(r) {
    const x = r.crew;
    const c = el("button", `crew-card workflow wf-card${x.cover?.url ? " has-cover" : ""}${state.selected === `crew:${r.id}` ? " selected" : ""}`);
    c.type = "button";
    c.dataset.id = `crew:${r.id}`;
    c.dataset.flowKind = "workflow";
    c.addEventListener("click", () => { state.selected = `crew:${r.id}`; markSelected(); getAuto().open(r); });
    if (x.cover?.url) coverImg(c, x.cover);
    const top = el("div", "crew-cover-top");
    const kind = x.trigger?.kind === "schedule" ? "스케줄" : "웹훅";
    const when2 = x.trigger ? (x.enabled ? kind : `${kind} · 꺼짐`) : "수동 실행";
    const badge = el("span", `wf-card-when${crewSchedule.triggered(x) ? " on" : ""}`);
    badge.title = x.trigger ? crewSchedule.triggerText(x) : when2;
    badge.append(el("i"), el("span", "", when2));
    top.prepend(badge);
    c.title = [x.role, flowAlias(r, items())].filter(Boolean).join(" · ");
    const aliases = [flowAlias(r, items()), x.research?.deep_alias ? `@${x.research.deep_alias}` : ""].filter(Boolean).join(" · ");
    c.append(top, raw(el("strong", "crew-cover-title", x.name)), raw(el("code", "flow-alias", aliases)));
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
    g.nodes = g.nodes.filter((n) => n?.id && crewVocabulary.NODES[n.type]).slice(0, 100).map((n) => ({ id: String(n.id).slice(0, 40), type: n.type, title: String(n.title || crewVocabulary.NODES[n.type].label).slice(0, 80),
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
      Object.assign(n, { x: 40 + d * (188 + 67), y: 40 + (rows[d] - 1) * 140, config: n.config || {} });
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
      if (crewEditor.ed.rec?.id === saved.id) { crewEditor.ed.rec = byId(saved.id) || saved; drawGraph(); }
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
    s.append(kv([["워크플로우", `${workflows().length}개`], ["일정·웹훅", `${workflows().filter((r) => crewSchedule.triggered(r.crew)).length}개 켜짐`], ["페르소나", `${personas().length}개 저장`], ["에이전트", `${crewWorkers.agents().length}명`]]));
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
    head.append(raw(el("h2", "", x.name)), raw(el("p", "", x.role || "")));
    const chips = el("div", "wf-chips");
    if (x.category) { const c = el("span", "wf-chip"); c.append(icon(ctx.catIcon(x.category)), raw(el("span", "", x.category))); chips.append(c); }
    const handle = el("span", "wf-chip mono", `$${x.handle}`);
    chips.append(handle, copyButton(`$${x.handle}`));
    head.append(chips);
    const credit = coverCredit(x);
    if (credit) head.append(credit);
    body.append(head);
    // 확인해 주세요: what the rule checks found, before anything else.
    const checks = crewModel.ruleChecks(x);
    const bad = checks.filter((c) => !c.ok);
    if (bad.length) {
      const card = el("div", "wf-attn small");
      const t = el("div", "");
      t.append(el("strong", "", `구조 검토 · 확인할 곳 ${bad.length}`));
      for (const c of bad) t.append(el("p", "", `${c.label}: ${c.detail}`));
      card.append(t, button("구조 검토", "", null, () => crewEditor.openGraph(r, { review: true })));
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
    acts.append(button("편집", "", null, () => crewEditor.openGraph(r)), dupButton(r), button("설정", "ghost", null, () => openSettings(r)), button("보관", "ghost", null, () => archive(r)));
    body.append(acts, runWrap);

    const run = section("실행", "실행기가 노드를 따라가요");
    const stops = el("div", "wf-chips");
    const backs = (x.graph?.edges || []).filter(crewModel.isBack);
    const chips2 = [x.limits?.max_minutes ? `${x.limits.max_minutes}분` : "", x.limits?.max_steps ? `단계 ${x.limits.max_steps}번` : "", backs.length ? `되돌아가기 ${Math.max(...backs.map((e) => e.max))}번` : ""].filter(Boolean);
    (chips2.length ? chips2 : ["정하지 않음"]).forEach((t) => stops.append(el("span", "wf-chip", t)));
    const next = x.trigger?.kind === "schedule" && x.enabled ? crewSchedule.nextRun(x.trigger.cron) : null;
    const model = el("span", "wf-model");
    model.append(modelMark(x.model), el("span", "mono", x.model || "노드마다 정해요"));
    const trig = el("span", "", x.trigger ? crewSchedule.triggerText(x) : "없어요");
    const trigSub = el("small", "dim", x.trigger ? (x.enabled ? (next ? `다음 ${crewSchedule.when(next)}` : "켜져 있어요") : "꺼져 있어요") : "$이름·화면에서만 돌아요");
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

    const ais = crewModel.aiNodes(x.graph);
    if (ais.length) {
      const s = section("AI 노드", `${ais.length}개`);
      const list = el("div", "wf-rows");
      for (const n of ais) {
        const row = el("button", "wf-row");
        row.type = "button";
        row.title = "눌러서 이 노드 고치기";
        row.addEventListener("click", () => crewEditor.openGraph(r, { select: n.id }));
        const who = el("div", "");
        const persona = byId(n.config?.persona);
        const expert = n.type === "expert" ? byId(n.config?.expert) : null;
        const who0 = n.type === "expert" ? "전문가" : "에이전트";
        const kind = n.type === "ai_once" ? "한 번 묻기" : n.config?.collab === "on" ? `${who0} · 협업 워커 ${n.config?.max_workers || 5}` : who0;
        who.append(el("strong", "", crewModel.nodeTitle(n)), el("small", "", [kind, expert?.crew.name || persona?.crew.name].filter(Boolean).join(" · ")));
        row.append(expert ? avatarMark(expert.crew) : modelMark(crewModel.modelOf(n, x)), who, el("span", "mono dim", crewModel.modelOf(n, x)));
        list.append(row);
      }
      s.append(list);
      body.append(s);
    }
    const order = section("진행 순서", `${crewModel.stepCount(x.graph)}단계`);
    order.querySelector("h3").append(linkBtn("그래프로 편집", () => crewEditor.openGraph(r, { graphMode: true })));
    const ol = el("ol", "crew-steps wf-steps");
    const backTargets = new Set(backs.map((e) => e.to));
    crewModel.levels(x.graph).map((lv) => lv.filter(crewModel.WORK)).filter((lv) => lv.length).forEach((lv, i) => {
      const li = el("li", "crew-step");
      li.tabIndex = 0;
      li.title = "눌러서 이 단계 고치기";
      li.addEventListener("click", () => crewEditor.openGraph(r, { select: lv[0].id, graphMode: true }));
      li.addEventListener("keydown", (e) => { if (e.key === "Enter") crewEditor.openGraph(r, { select: lv[0].id, graphMode: true }); });
      const how = lv.length > 1 ? "동시에" : lv[0].type === "approval" ? "확인 후" : backTargets.has(lv[0].id) ? "↺ 되돌아올 수 있어요" : "";
      li.append(el("span", "mono", String(i + 1).padStart(2, "0")), el("strong", "", lv.map(crewModel.nodeTitle).join(" · ")));
      if (how) li.append(el("small", "", how));
      ol.append(li);
    });
    order.append(ol);
    body.append(order);
    if (!bad.length) {
      const ok = el("p", "wf-ok");
      ok.append(icon("check"), "규칙 검사를 모두 통과했어요 ·");
      ok.append(linkBtn("구조 검토", () => crewEditor.openGraph(r, { review: true })));
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
      const r = await crewExecution.startRun(rec, q.value.trim());
      go2.disabled = false;
      if (r) crewEditor.openGraph(rec, { run: r });
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
      if (x.category) { const c = el("span", "wf-chip"); c.append(icon(ctx.catIcon(x.category)), raw(el("span", "", x.category))); top.append(c); }
      const foot = el("div", "crew-cover-foot");
      foot.append(el("small", "wf-card-meta", x.meta || ""));
      mini.append(top, x.name ? raw(el("strong", "crew-cover-title", x.name)) : el("strong", "crew-cover-title", "이름"));
      if (x.role) mini.append(raw(el("p", "wf-card-role", x.role)));
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
  function openSettings(rec, preset, fromEditor = false, startWith = "", proposal = null) {
    dialog?.remove();
    const base = preset || rec?.crew || { type: "workflow", name: "", handle: "", role: "", category: "", model: "", enabled: false };
    const c = structuredClone(base);
    dialog = el("dialog", "crew-dialog wf-dialog wf-settings");
    const form = el("form");
    form.method = "dialog";
    const head = el("div", "dialog-head");
    const ht = el("div", "");
    ht.append(el("h2", "", proposal ? "리서치 개선안 검토" : rec ? `${c.name} 고치기` : "새 워크플로우"), el("p", "", "키·토큰은 넣지 마세요. 키는 설정 › 에이전트에 넣어요."));
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
    handle.addEventListener("input", () => { handle.dataset.touched = "1"; handle.value = handle.value.replace(/^[@$]/, ""); cover.refresh(); });
    role.addEventListener("input", () => cover.refresh());
    category.addEventListener("input", () => cover.refresh());
    const row1 = el("div", "scope-fields");
    row1.append(field("이름", name, "카드에 크게 보여요"), field("@이름", handle, "기존 $이름 호출도 계속 쓸 수 있어요"));
    const row2 = el("div", "scope-fields");
    row2.append(field("설명", role), field("카테고리", category));
    bodyEl.append(cats, modelList(), row1, row2);
    const deepAlias = proposal && c.research ? input(c.research.deep_alias || proposal.aliases?.deep || "", { required: true, maxLength: 40, pattern: "[\\p{L}\\p{N}_\\-]{1,40}" }) : null;
    if (deepAlias) { deepAlias.setAttribute("aria-label", "깊은 조사 @이름"); deepAlias.addEventListener("input", () => { deepAlias.value = deepAlias.value.replace(/^@/, ""); }); bodyEl.append(field("깊은 조사 @이름", deepAlias, "같은 리서치에서 깊은 조사를 바로 요청해요. 기존 별칭과 겹치지 않아야 해요.")); }
    let start = startWith || (preset?.graph ? "copy" : "simple");
    let fromTemplate = () => {};
    if (!rec && !proposal) {
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
    const hintOf = () => { const n = crewSchedule.nextRun(cron.value); return n ? `${crewSchedule.cronText(cron.value)} · 다음 ${crewSchedule.when(n)}` : "형식: 분 시 일 월 요일 (예: 0 9 * * 1-5)"; };
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
      const n = rec ? crewModel.stepCount(c.graph) : start === "blank" ? 0 : start === "copy" ? crewModel.stepCount(c.graph) : 1; // simple and a template: one AI step
      const when3 = kind.value === "schedule" ? crewSchedule.cronText(cron.value) : kind.value === "webhook" ? "웹훅" : "수동 실행";
      return { name: name.value.trim(), role: role.value.trim(), category: category.value.trim(), meta: [flowAlias({ crew: { ...c, handle: handle.value.trim() || "이름" } }, items()), n ? (n === 1 ? "단계 1개" : `${n}단계`) : "단계 없음", when3].join(" · ") };
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
    if (proposal) {
      note.classList.add("research-review");
      if (c.research) note.append(el("strong", "", "근거를 보고 깊게 조사해요"), el("p", "", "시간이 오래 걸린다는 이유로 전환하지 않아요. 근거가 부족할 때 추가 조사로 이어져요."), el("p", "", "기본 호출과 깊은 조사 호출은 같은 리서치를 사용해요. 기존 모델·예산·승인·스킬 설정을 유지하는지 변경 사항과 입력 값을 확인해 주세요. 저장하기 전에는 적용되지 않아요."));
      else note.append(el("strong", "", "리서치 템플릿 검토"), el("p", "", "이 제안에는 자동 심층 전환이 포함되지 않아요. 변경 사항과 모델·한도를 확인한 뒤 만들어 주세요."));
      const changes = el("ul");
      for (const change of proposal.changes || []) if (typeof change === "string") changes.append(raw(el("li", "", change)));
      note.append(changes);
      if (proposal.aliases?.standard && proposal.aliases?.deep) {
        const aliases = raw(el("p"));
        const paintAliases = () => { aliases.textContent = `@${handle.value || proposal.aliases.standard} · @${deepAlias?.value || proposal.aliases.deep}`; };
        handle.addEventListener("input", paintAliases); deepAlias?.addEventListener("input", paintAliases); paintAliases(); note.append(aliases);
      }
    } else note.append(el("strong", "", rec ? "사진 고르는 법" : "만든 뒤에"), el("p", "", rec ? "가로 사진이 잘 맞아요. 글씨가 아래에 올라가니 아래쪽이 너무 밝지 않은 사진을 고르세요." : "AI 하나로 시작하면 간단 편집에서 모델·목표만 정하면 돼요. 단계를 더하면 그래프 편집기로 바뀌어요."));
    sideCol.append(ph, cover.side, note);
    bodyEl.classList.add("wf-dlg-grid");
    bodyEl.append(mainCol, sideCol);
    kind.addEventListener("change", () => cover.refresh());
    const foot = el("div", "dialog-foot");
    const cancel = button("취소", "ghost");
    cancel.type = "submit";
    cancel.value = "cancel";
    const ok = button(proposal ? rec ? "개선안 저장" : "리서치 만들기" : rec ? "저장" : "만들기", "primary");
    ok.type = "submit";
    ok.value = "save";
    if (rec && !proposal) {
      const del = button("지우기", "ghost danger", "trash", async () => { if (await remove(rec)) dialog.close(); });
      del.type = "button";
      foot.append(del, el("span", "grow"));
    }
    foot.append(cancel, ok);
    form.append(head, bodyEl, foot);
    form.addEventListener("submit", async (e) => {
      if (e.submitter?.value !== "save") return;
      e.preventDefault();
      if (kind.value === "schedule" && !crewSchedule.parseCron(cron.value)) { err.textContent = "언제 칸의 형식을 확인해 주세요."; return; }
      const out = { ...c, type: "workflow", name: name.value.trim(), handle: handle.value.trim(), role: role.value.trim(), category: category.value.trim(), model: model.value.trim() };
      if (deepAlias) out.research = { ...c.research, deep_alias: deepAlias.value.trim().replace(/^@/, "") };
      if (kind.value) {
        const sameTrigger = proposal && kind.value === c.trigger?.kind && (kind.value === "schedule" ? cron.value.trim() === c.trigger.cron : label.value.trim() === (c.trigger.label || ""));
        out.trigger = sameTrigger ? c.trigger : kind.value === "schedule" ? { kind: "schedule", cron: cron.value.trim() } : { kind: "webhook", label: label.value.trim() };
        if (!proposal || on.checked !== !!c.enabled) out.enabled = on.checked;
      } else { delete out.trigger; if (!proposal) out.enabled = false; }
      const limits = {};
      if (Number(maxMin.value) > 0) limits.max_minutes = Math.min(1440, Math.round(Number(maxMin.value)));
      if (Number(maxSteps.value) > 0) limits.max_steps = Math.min(500, Math.round(Number(maxSteps.value)));
      if (Object.keys(limits).length) out.limits = limits; else delete out.limits;
      const cv = cover.value();
      if (cv) out.cover = cv; else delete out.cover;
      if (!rec) out.graph = start === "copy" ? c.graph : crewModel.defaultGraph(start, out);
      if (fromEditor) out.graph = crewEditor.ed.graph; // settings saved from the editor keep the graph being edited
      ok.disabled = true;
      try {
        const saved = await save(rec, out);
        dialog.close();
        if (fromEditor) {
          await reload();
          crewEditor.ed.rec = byId(saved.id) || saved;
          crewEditor.ed.dirty = false;
          drawGraph();
          notice("워크플로우 설정과 그래프를 저장했어요.");
          return;
        }
        await reload(saved.id);
        notice(rec ? "저장했어요." : "만들었어요.");
        if (!rec) crewEditor.openGraph(byId(saved.id) || saved, { graphMode: start === "blank" });
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
  const crewEditor = createCrewEditor({
    get closeGraph() { return closeGraph; },
    get graphBody() { return graphBody; },
    get autoTitle() { return autoTitle; },
    get avatarMark() { return avatarMark; },
    get button() { return button; },
    get byId() { return byId; },
    get cronText() { return crewSchedule.cronText; },
    get defaultGraph() { return crewModel.defaultGraph; },
    get el() { return el; },
    get errorText() { return errorText; },
    get field() { return field; },
    get FIELDS() { return crewVocabulary.FIELDS; },
    get getAuto() { return getAuto; },
    get icon() { return icon; },
    get input() { return input; },
    get inspector() { return crewNodes.inspector; },
    get IS_AI() { return crewVocabulary.IS_AI; },
    get isBack() { return crewModel.isBack; },
    get items() { return items; },
    get KIND_OF() { return crewVocabulary.KIND_OF; },
    get kv() { return kv; },
    get modelInput() { return modelInput; },
    get modelList() { return modelList; },
    get modelOf() { return crewModel.modelOf; },
    get modelSelect() { return modelSelect; },
    get NODE_LABEL() { return crewExecution.NODE_LABEL; },
    get nodeBody() { return crewNodes.nodeBody; },
    get nodeDur() { return crewRunView.nodeDur; },
    get nodeIn() { return crewReview.nodeIn; },
    get nodeLine() { return crewNodes.nodeLine; },
    get NODES() { return crewVocabulary.NODES; },
    get nodeTitle() { return crewModel.nodeTitle; },
    get notice() { return notice; },
    get openNodeDialog() { return crewNodes.openNodeDialog; },
    get ownerOf() { return ownerOf; },
    get parseCron() { return crewSchedule.parseCron; },
    get personas() { return personas; },
    get pollRun() { return crewRunView.pollRun; },
    get PORTS() { return crewVocabulary.PORTS; },
    get portsOf() { return crewModel.portsOf; },
    get query() { return query; },
    get readable() { return readable; },
    get reload() { return reload; },
    get render() { return paint; },
    get renderSimple() { return crewNodes.renderSimple; },
    get request() { return request; },
    get reviewPanel() { return crewReview.reviewPanel; },
    get RUN_LABEL() { return crewExecution.RUN_LABEL; },
    get runList() { return crewRunView.runList; },
    set runList(value) { crewRunView.runList = value; },
    get runPanel() { return crewRunView.runPanel; },
    get save() { return save; },
    get select() { return select; },
    get simpleNode() { return crewModel.simpleNode; },
    get spaceDown() { return spaceDown; },
    get spanText() { return spanText; },
    get startRun() { return crewExecution.startRun; },
    get state() { return state; },
    get stepState() { return crewRunView.stepState; },
    get stopPolling() { return crewRunView.stopPolling; },
    get targetsOf() { return crewReview.targetsOf; },
    get WORK() { return crewModel.WORK; },
    get resumeWorker() { return crewWorkers.resumeWorker; },
    get wouldCycle() { return crewModel.wouldCycle; }
  });

  const crewNodes = createCrewNodes({
    get graphBody() { return graphBody; },
    get agents() { return crewWorkers.agents; },
    get aiField() { return crewEditor.aiField; },
    get aiNodes() { return crewModel.aiNodes; },
    get avatarMark() { return avatarMark; },
    get blankAgent() { return crewWorkers.blankAgent; },
    get button() { return button; },
    get byHandle() { return byHandle; },
    get byId() { return byId; },
    get closeX() { return crewEditor.closeX; },
    get confirmBox() { return crewEditor.confirmBox; },
    get cronText() { return crewSchedule.cronText; },
    get deselect() { return crewEditor.deselect; },
    get ed() { return crewEditor.ed; },
    get EFFORTS() { return crewVocabulary.EFFORTS; },
    get el() { return el; },
    get errorText() { return errorText; },
    get field() { return field; },
    get FIELDS() { return crewVocabulary.FIELDS; },
    get HUMAN_KIND() { return crewVocabulary.HUMAN_KIND; },
    get icon() { return icon; },
    get input() { return input; },
    get IS_AI() { return crewVocabulary.IS_AI; },
    get isBack() { return crewModel.isBack; },
    get keepHistory() { return crewEditor.keepHistory; },
    get kv() { return kv; },
    get linkTo() { return crewEditor.linkTo; },
    get modelInput() { return modelInput; },
    get modelList() { return modelList; },
    get MODES() { return crewVocabulary.MODES; },
    get nextRun() { return crewSchedule.nextRun; },
    get nodeDlg() { return crewEditor.nodeDlg; },
    set nodeDlg(value) { crewEditor.nodeDlg = value; },
    get nodeIn() { return crewReview.nodeIn; },
    get nodePanel() { return crewEditor.nodePanel; },
    get NODES() { return crewVocabulary.NODES; },
    get nodeTitle() { return crewModel.nodeTitle; },
    get notice() { return notice; },
    get openSettings() { return openSettings; },
    get ownerOf() { return ownerOf; },
    get personas() { return personas; },
    get PORTS() { return crewVocabulary.PORTS; },
    get redraw() { return crewEditor.redraw; },
    set redraw(value) { crewEditor.redraw = value; },
    get removeNode() { return crewEditor.removeNode; },
    get render() { return paint; },
    get runFromEditor() { return crewRunView.runFromEditor; },
    get save() { return save; },
    get saveGraph() { return crewEditor.saveGraph; },
    get segment() { return segment; },
    get select() { return select; },
    get startRun() { return crewExecution.startRun; },
    get state() { return state; },
    get switchEl() { return switchEl; },
    get syncCard() { return crewEditor.syncCard; },
    get teams() { return crewWorkers.teams; },
    get touch() { return crewEditor.touch; },
    get trigDraft() { return crewEditor.trigDraft; },
    get triggerText() { return crewSchedule.triggerText; },
    get WORK() { return crewModel.WORK; },
    get workerOut() { return crewWorkers.workerOut; },
    get workflows() { return workflows; },
    get wouldCycle() { return crewModel.wouldCycle; }
  });

  const crewReview = createCrewReview({
    get state() { return state; },
    get ed() { return crewEditor.ed; },
    get el() { return el; },
    get icon() { return icon; },
    get isBack() { return crewModel.isBack; },
    get kit() { return kit; },
    get models() { return models; },
    get NODES() { return crewVocabulary.NODES; },
    get notice() { return notice; },
    get render() { return paint; },
    get ruleChecks() { return crewModel.ruleChecks; },
    get touch() { return crewEditor.touch; },
    get uid() { return crewEditor.uid; }
  });

  const crewRunView = createCrewRunView({
    get button() { return button; },
    get decide() { return crewExecution.decide; },
    get ed() { return crewEditor.ed; },
    get el() { return el; },
    get fwd() { return crewModel.fwd; },
    get input() { return input; },
    get kv() { return kv; },
    get levels() { return crewModel.levels; },
    get NODE_LABEL() { return crewExecution.NODE_LABEL; },
    get nodeDlg() { return crewEditor.nodeDlg; },
    set nodeDlg(value) { crewEditor.nodeDlg = value; },
    get nodeIn() { return crewReview.nodeIn; },
    get nodeTitle() { return crewModel.nodeTitle; },
    get PORTS() { return crewVocabulary.PORTS; },
    get query() { return query; },
    get readable() { return readable; },
    get render() { return paint; },
    get request() { return request; },
    get runRequest() { return runRequest; },
    get spanText() { return spanText; },
    get startRun() { return crewExecution.startRun; },
    get state() { return state; },
    get stopRun() { return crewExecution.stopRun; },
    get WORK() { return crewModel.WORK; }
  });


  function graphHead(line) {
    const rec = crewEditor.ed.rec;
    if (!rec) return [];
    if (crewEditor.ed.run) {
      const view = segment([["graph", "그래프"], ["list", "목록"]], crewEditor.ed.runList ? "list" : "graph", (k) => { crewEditor.ed.runList = k === "list"; drawGraph(); });
      view.classList.add("wf-view");
      headTitle(line, button("돌아가기", "ghost", "arrow-left", crewEditor.leaveGraph));
      return [view];
    }
    const save2 = button("저장", "primary g-save", null, crewEditor.saveGraph);
    save2.disabled = !crewEditor.ed.dirty;
    const review = button(`구조 검토${crewEditor.ed.suggestions?.length ? ` · ${crewEditor.ed.suggestions.length}` : ""}`, crewEditor.ed.review ? "active" : "", "sparkle", () => { crewEditor.ed.review = !crewEditor.ed.review; drawGraph(); });
    const testing = ["running", "waiting"].includes(crewEditor.ed.test?.crew_run?.status);
    const trial = button(testing ? "실행 중…" : crewEditor.ed.test ? "다시 실행" : "실행", "g-trial", "play", crewRunView.runFromEditor);
    trial.disabled = crewEditor.ed.dirty || testing;
    trial.title = crewEditor.ed.dirty ? "저장한 뒤에 실행할 수 있어요" : "저장된 워크플로우로 지금 한 번 돌려요";
    const settings = button("설정", "ghost", "gear-six", () => openSettings(crewEditor.ed.rec, null, true));
    settings.title = "이름·$이름·워커 기본 모델·멈추는 조건·일정·웹훅·배경 사진";
    // An owned workflow runs only through its agent (@); the server refuses a manual run.
    headTitle(line, button(crewEditor.ed.fromPopup ? "창으로 돌아가기" : "돌아가기", "ghost", "arrow-left", crewEditor.leaveGraph));
    return [settings, review, ...(ownerOf(rec) ? [] : [trial]), save2];
  }
  // 그래프로 편집하기 always opens in its own window: a data-popout dialog that popup-drag.js moves into a separate OS
  // window as it opens. Where no window can open (the browser preview) the same dialog covers this window instead.
  // It shares this window's JS, so a save reloads the list and cards here as well.
  let graphWin = null;
  const graphBody = () => graphWin?.querySelector(".graph-win-body");
  const paint = (reset) => { if (crewEditor.ed.rec) drawGraph(); else render(reset); }; // what used to redraw the graph page
  function drawGraph() {
    const rec = crewEditor.ed.rec;
    if (!rec) return closeGraph();
    if (!graphWin) {
      graphWin = el("dialog", "graph-win");
      graphWin.append(el("header", "page-head graph-win-head"), el("div", "graph-win-body page-wrap"));
      // Esc, the OS ✕ and a click outside: cancel a pending link first, else leave the way 돌아가기 does (unsaved edits ask)
      graphWin.addEventListener("cancel", (e) => {
        e.preventDefault();
        if (crewEditor.ed.linking) { crewEditor.ed.linking = null; drawGraph(); } else void crewEditor.leaveGraph();
      });
      graphWin.addEventListener("close", function () {
        this.remove();
        if (graphWin !== this) return;
        graphWin = null;
        if (crewEditor.ed.rec) { crewRunView.stopPolling(); Object.assign(crewEditor.ed, { rec: null, run: null }); } // closed from outside
      });
      document.body.append(graphWin);
    }
    Object.assign(graphWin.dataset, { popout: JSON.stringify({ detail: { id: rec.id } }), popoutTitle: rec.crew.name });
    const line = el("div", "title-line"), title = el("h1", "graph-win-title");
    if (crewRunView.rerunOf()) title.textContent = "실패한 곳부터 재실행";
    else title.append(raw(el("span", "", rec.crew.name)), el("small", "g-title-sub", crewEditor.ed.run ? "실행" : "워크플로우 편집"));
    const aside = el("div", "head-aside");
    aside.append(...graphHead(line)); // graphHead puts ← 돌아가기 in the line
    line.append(title);
    graphWin.querySelector(".graph-win-head").replaceChildren(line, aside);
    crewEditor.renderGraph();
    if (!graphWin.open) graphWin.showModal();
  }
  function closeGraph() {
    const w = graphWin;
    graphWin = null;
    if (w?.open) w.close(); else w?.remove();
  }
  // 제목 줄: ← 돌아가기 at the far left, then the workflow's name with what this screen is on a smaller line under it.
  function headTitle(line, back) {
    back.classList.add("page-back", "g-head-back");
    line.prepend(back);
  }
  // Keys reach the document of the window the editor is in: this one, or its popout window (popup-drag.js).
  const inGraph = (e) => !!crewEditor.ed.rec && !!graphWin && graphWin.ownerDocument === e.currentTarget;
  // Space held = hand tool on the graph, as in Figma.
  let spaceDown = false;
  const typing = (d) => ["INPUT", "TEXTAREA", "SELECT"].includes(d.activeElement?.tagName) || d.activeElement?.isContentEditable;
  const setSpace = (on) => { spaceDown = on; graphWin?.querySelector(".g-stage")?.classList.toggle("space", on); };
  function graphKeys(doc) {
    doc.addEventListener("keydown", (e) => {
      if (e.code !== "Space" || !inGraph(e) || typing(doc)) return;
      e.preventDefault();
      if (!spaceDown) setSpace(true);
    });
    doc.addEventListener("keyup", (e) => { if (e.code === "Space") setSpace(false); });
    // before the dialog's own Esc (cancel), which would leave the editor
    doc.addEventListener("keydown", (e) => { if (e.key === "Escape" && crewEditor.ed.linking && inGraph(e)) { e.preventDefault(); crewEditor.ed.linking = null; drawGraph(); } });
    doc.defaultView?.addEventListener("blur", () => setSpace(false));
    // Ctrl/Cmd+Z undoes a canvas edit, Ctrl/Cmd+Shift+Z or Ctrl+Y redoes it; inside a field the text's own undo wins.
    doc.addEventListener("keydown", (e) => {
      if (!inGraph(e) || crewEditor.ed.run || crewEditor.nodeDlg || typing(doc) || [...doc.querySelectorAll("dialog[open]")].some((d) => d !== graphWin)) return;
      if (!(e.ctrlKey || e.metaKey) || e.altKey) return;
      // A non-Latin layout (두벌식: ㅋ) types another letter on the Z key: fall back to the physical key there.
      const k = /^[a-z]$/i.test(e.key) ? e.key.toLowerCase() : (e.code || "").replace(/^Key/, "").toLowerCase();
      if (k === "z") { e.preventDefault(); if (e.shiftKey) crewEditor.redo(); else crewEditor.undo(); }
      else if (k === "y" && e.ctrlKey && !e.shiftKey) { e.preventDefault(); crewEditor.redo(); }
    });
    doc.addEventListener("keydown", (e) => {
      if (!inGraph(e) || crewEditor.ed.run || (!crewEditor.ed.sel && crewEditor.ed.selEdge === null)) return;
      if (["INPUT", "TEXTAREA", "SELECT"].includes(doc.activeElement?.tagName)) return;
      if (e.key === "Delete" || e.key === "Backspace") {
        e.preventDefault();
        if (crewEditor.ed.sel) crewEditor.removeNode(crewEditor.ed.sel);
        else { crewEditor.ed.graph.edges.splice(crewEditor.ed.selEdge, 1); crewEditor.ed.selEdge = null; crewEditor.touch(); drawGraph(); }
      }
    });
  }
  graphKeys(document);
  window.addEventListener("arkism-popout", (e) => graphKeys(e.detail));

  async function explain(prompt) {
    const a = await kit.assist("explain", prompt, { purpose: "skill" });
    if (a.status !== "done") throw new Error(kit.assistError(a));
    return a.output.trim();
  }
  const crewPersonas = createCrewPersonas({
    get button() { return button; },
    get el() { return el; },
    get errorText() { return errorText; },
    get field() { return field; },
    get icon() { return icon; },
    get input() { return input; },
    get kit() { return kit; },
    get notice() { return notice; },
    get personas() { return personas; },
    get reload() { return reload; },
    get render() { return render; },
    get request() { return request; },
    get save() { return save; },
    get state() { return state; },
    get tr() { return tr; },
    get workflows() { return workflows; }
  });

  const crewWorkers = createCrewWorkers({
    get $() { return $; },
    get allWorkflows() { return allWorkflows; },
    get avatarMark() { return avatarMark; },
    get button() { return button; },
    get byId() { return byId; },
    get ctx() { return ctx; },
    get el() { return el; },
    get errorText() { return errorText; },
    get field() { return field; },
    get getAuto() { return getAuto; },
    get flowRuns() { return flowHistory.runs; },
    get icon() { return icon; },
    get input() { return input; },
    get IS_AI() { return crewVocabulary.IS_AI; },
    get isBack() { return crewModel.isBack; },
    get items() { return items; },
    get kit() { return kit; },
    get levels() { return crewModel.levels; },
    get modelInput() { return modelInput; },
    get modelList() { return modelList; },
    get modelMark() { return modelMark; },
    get NODES() { return crewVocabulary.NODES; },
    get nodeTitle() { return crewModel.nodeTitle; },
    get notice() { return notice; },
    get openGraph() { return crewEditor.openGraph; },
    get ownerOf() { return ownerOf; },
    get pickAvatar() { return pickAvatar; },
    get pins() { return pins; },
    get query() { return query; },
    get raw() { return raw; },
    get reload() { return reload; },
    get render() { return render; },
    get request() { return request; },
    get RUN_LABEL() { return crewExecution.RUN_LABEL; },
    get save() { return save; },
    get segment() { return segment; },
    get select() { return select; },
    get state() { return state; },
    get switchEl() { return switchEl; },
    get tr() { return tr; },
    get triggered() { return crewSchedule.triggered; },
    get validAvatar() { return validAvatar; },
    get WORK() { return crewModel.WORK; }
  });

  return {
    reset() {
      flowHistory.reset();
      crewRunView.stopPolling();
      Object.assign(crewEditor.ed, { rec: null, graph: null, run: null, test: null, suggestions: null, reviewAI: null });
      closeGraph();
      Object.assign(crewWorkers.wk, { id: null, draft: null, dirty: false, ai: null, instrAI: null, test: null, status: {}, usage: null, statusAt: 0, teamsPick: new Set() });
    },
    explain, assist: (...a) => kit.assist(...a), assistError: (a) => kit.assistError(a), parseJSON: (t) => kit.parseJSON(t),
    runWorkflow: (rec, text) => crewExecution.startRun(rec, text),
    flowRuns: flowHistory.runs,
    skillExperts: () => null,
    openWorkers: crewWorkers.openWorkers,
    load: () => request(`/v1/crew?${query()}`).then((r) => {
      // ?expert=<id> opens that expert's popup once, right after the first load (Figma 843:59277).
      const want = !crewWorkers.wk.urlDone && new URLSearchParams(location.search).get("expert");
      crewWorkers.wk.urlDone = true;
      if (want) setTimeout(() => { if (byId(want)?.crew.type === "agent") crewWorkers.openWorkers(want); }, 400);
      return r.items || [];
    }, () => []),
    count: () => flows().length,
    settingsTab: crewPersonas.personaTab,
    render: renderList,
    detail: renderDetail,
    head: headActions,
    views: {
      crew: { bare: true, title: "워크플로우", count: () => `${flows().length}개`, sub: () => "역할·협업·자동화를 한곳에서 관리해요." }
    },
    pages: {
      workers: { title: () => "전문가", count: () => `${crewWorkers.agents().length}명`,
        sub: () => "",
        bare: () => true, head: () => [], render: crewWorkers.renderWorkers }
    },
    // exported for tests in a browser console
    _test: { parseCron: crewSchedule.parseCron, nextRun: crewSchedule.nextRun, cronText: crewSchedule.cronText, levels: crewModel.levels, wouldCycle: crewModel.wouldCycle, cliOf: crewVocabulary.cliOf, ruleChecks: crewModel.ruleChecks, simpleNode: crewModel.simpleNode, portsOf: crewModel.portsOf }
  };
};
