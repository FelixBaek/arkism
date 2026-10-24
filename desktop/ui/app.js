import { createRemoteCollection } from "./modules/remoteCollection.mjs";
import { createCatalogQuery } from "./modules/catalogQuery.mjs";
import { createSkillSimilarity } from "./modules/skillSimilarity.mjs";
import { retainListFocus } from "./modules/listFocus.mjs";
import { createVisibleSync } from "./modules/visibleSync.mjs";
import { createCollectionPaging } from "./modules/collectionPaging.mjs";
import { createVirtualList } from "./modules/virtualList.mjs";
import { createAppState } from "./modules/state.mjs";
import { createCrew } from "./crew.js";
import { readSSH } from "./modules/storage.mjs";
import { createAppApi, loadFailure } from "./modules/appApi.mjs";
import { createMemoryPaging } from "./modules/memoryPaging.mjs";
import { createMemoryList } from "./modules/memoryList.mjs";
import { createSkillViews } from "./modules/skillViews.mjs";
import { createImageViews } from "./modules/imageViews.mjs";
import { createDesignViews } from "./modules/designViews.mjs";
import { createToolViews } from "./modules/toolViews.mjs";
import { createMemoryDetail } from "./modules/memoryDetail.mjs";
import { createAppConnection } from "./modules/appConnection.mjs";
export function startApp() {
  "use strict";

  const defaults = { workspace_id: "default", project_id: "default", user_id: "management:owner", agent_id: "" };
  const readJSON = (raw) => { try { return JSON.parse(raw || "null"); } catch (_) { return null; } };
  const tauri = window.ArkismSSH(window.__TAURI__?.core);
  const store = (key, value) => { try { value === null ? localStorage.removeItem(key) : localStorage.setItem(key, value); } catch (_) { /* storage off: ask again next time */ } };
  const stored = (key) => { try { return localStorage.getItem(key); } catch (_) { return null; } };
  const emptyData = () => ({ memories: [], candidates: [], conflicted: [], maintenance: [], skills: [], prompts: [], designSystems: [], tools: null, crew: [] });
  const state = createAppState({
    // Build-time defaults come from the desktop shell (app_defaults); a plain browser keeps these.
    defaults: { server: "http://127.0.0.1:8091", ssh_host: "localhost", updates: false, local: false },
    server: stored("arkism.server") || "http://127.0.0.1:8091",
    token: "",
    ssh: readSSH(stored("arkism.ssh")),
    email: stored("arkism.email") || "",
    scope: { ...defaults, ...readJSON(stored("arkism.scope")) },
    caps: null,
    view: "todo",
    menu: "home",
    memTab: "active",
    skillTab: "all",
    skillModel: "", // "" = every model; otherwise an agent key (claude_code, codex, ...)
    skillSort: stored("arkism.skillSort") || "used",
    skillDir: Number(stored("arkism.skillDir")) || 0,
    openCopies: new Set(),
    kind: "all",
    repo: "",
    sort: "recent",
    query: "",
    selected: null,
    data: emptyData(),
    crewTab: "all",
    crewCat: "all",
    toolTab: "mcp",
    settingsTab: "general",
    designTab: "color",
    skillCat: "all",
    imageTab: "image",
    promptRun: null, // 프롬프트 실행 page: the prompt id
    designGroup: stored("arkism.designGroup") === "purpose" ? "purpose" : "product",
    toolAgent: "", // MCP·플러그인 filter: "" = every agent, else an agent key
    toolCat: "all", toolStatus: "all", // MCP·플러그인 분류 chip and 상태 select
    versions: new Map(), // version_id -> skill version record (analysis + inspection), for 품질·보안
    versionsDone: false,
    engine: "all",
    imgCat: "all",
    usage: new Map(),
    usageDone: false,
    usageFailed: false, // GET /v1/memory_usage failed: screens show — instead of 0 (A2)
    memoryTotals: null, // { active, idle, kinds } over every memory on the server, not the loaded first page
    candidateTotal: null, // every candidate on the server, read apart from memoryTotals (#188)
    listErrors: {}, // list name -> loadFailure() for a list that failed while the memories loaded (#186)
    loading: true,
    ready: null,
    pending: new Map() // "skillId|agent" -> "allow" | "block", until 적용
  });

  const $ = (s) => document.querySelector(s);
  function trimSlash(s) {
    let t = s.trim();
    while (t.endsWith("/")) {
      t = t.slice(0, -1);
    }
    return t;
  }
  const DAY = 86400000;
  const KINDS = {
    user: ["선호", "users-three"], procedural: ["작업 규칙", "stack"], decision: ["결정", "git-merge"],
    lesson: ["교훈", "lightbulb"], episodic: ["기록", "clock-counter-clockwise"], semantic: ["사실", "file-text"]
  };
  // Runtimes the skill router knows. Logos come from the agent logo set in the design file.
  const AGENTS = [["claude_code", "Claude Code", "img:claude.svg"], ["codex", "Codex", "sym:a-codex"], ["zcode", "ZCode", "sym:a-zcode"],
    ["gemini", "Gemini CLI", "sym:a-gemini"], ["grok", "Grok", "sym:i-x"], ["opencode", "OpenCode", "sym:i-terminal"], ["cursor", "Cursor", "sym:i-code"]];
  function agentLogo(spec, name) {
    const tile = el("span", "agent-tile");
    tile.title = name;
    const [kind, ref] = spec.split(":");
    if (kind === "img") {
      const img = el("img"); img.src = `img/${ref}`; img.alt = name; tile.append(img);
    } else {
      tile.classList.add("mono-logo");
      const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
      svg.setAttribute("aria-label", name);
      const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
      use.setAttribute("href", `#${ref}`);
      svg.append(use); tile.append(svg);
    }
    return tile;
  }
  const shownAgents = () => pages.shownAgents();
  const agentName = (key) => (AGENTS.find(([k]) => k === key) || [, key])[1];
  // Agents the server reports beyond the skill router's runtimes (ones the owner added by hand). They read only their own
  // skill folder, so the sidebar shows that folder's count and opens 설정 › 에이전트, and the status card shows their connection.
  const extraAgents = () => (state.data.tools?.agents || []).filter((a) => pages.agentWanted(a) && !AGENTS.some(([k]) => k === a.id));
  function syncExtraAgents() {
    const extras = extraAgents();
    const ids = new Set(extras.map((a) => a.id));
    document.querySelectorAll("[data-extra-agent]").forEach((n) => { if (!ids.has(n.dataset.extraAgent)) n.remove(); });
    const row = document.getElementById("status-agents");
    for (const a of extras) {
      if (!document.querySelector(`#status-agents [data-agent="${a.id}"]`)) {
        const tile = pages.logoOf(a);
        tile.dataset.agent = tile.dataset.extraAgent = a.id;
        tile.title = a.name;
        row.append(tile);
      }
    }
  }
  const ORIGINS = { "claude-code": "Claude Code", claude_code: "Claude Code", codex: "Codex", zcode: "ZCode", gemini: "Gemini CLI", grok: "Grok", opencode: "OpenCode", cursor: "Cursor" };

  // ---------------------------------------------------------------- helpers
  function el(tag, cls, text) {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined && text !== null) n.textContent = text;
    return n;
  }
  // 사용자 데이터(기억 본문·스킬 설명 등): 영어 화면에서도 번역하지 않는다 (i18n.js).
  function raw(node) { node.setAttribute("data-no-i18n", ""); return node; }
  // 화면 문구가 사용자 데이터와 한 줄에 섞여 raw()로 감싸야 할 때: 만들 때 미리 번역한다 (한국어 화면이면 그대로).
  const tr = (s) => (window.ArkismI18n ? window.ArkismI18n.translate(s) : s);
  function icon(name, cls = "icon") {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("class", cls);
    svg.setAttribute("aria-hidden", "true");
    const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
    use.setAttribute("href", `#i-${name}`);
    svg.append(use);
    return svg;
  }
  // Category chips and badges (208:17763): categories are the person's own words, so the icon is a guess from them.
  // An icon the person picked wins: crew categories they made (arkism.crewCatIcons, this PC only — the server keeps a crew
  // category as a plain string), then a 설정 › 카테고리 entry with the same name (arkism.prefs), then the guess.
  const guessCatIcon = (c) => (/git|release|릴리/i.test(c) ? "git-branch" : /plan|기획/i.test(c) ? "file-text" : /research|리서/i.test(c) ? "magnifying-glass"
    : /design|디자/i.test(c) ? "palette" : /code|코드/i.test(c) ? "code" : /image|이미지/i.test(c) ? "image" : /기록|비서|doc|note/i.test(c) ? "book-open" : c ? "tag" : "squares-four");
  const CAT_ICON_CHOICES = ["tag", "stack", "code", "terminal", "git-branch", "file-text", "book-open", "palette", "image", "presentation", "magnifying-glass", "lightbulb",
    "brain", "sparkle", "robot", "database", "archive", "chart-bar", "globe", "calendar", "paper-plane", "chat-text", "users-three", "puzzle-piece"];
  let crewCatIcons = {};
  try { crewCatIcons = JSON.parse(stored("arkism.crewCatIcons") || "{}") || {}; } catch (_) { /* start fresh */ }
  let prefsRaw, prefsIcons = new Map();
  function prefsCatIcon(c) {
    const now = stored("arkism.prefs");
    if (now !== prefsRaw) {
      prefsRaw = now; prefsIcons = new Map();
      try { for (const x of JSON.parse(now || "null")?.categories || []) if (x?.name && x.icon) prefsIcons.set(x.name, x.icon); } catch (_) { /* no saved categories */ }
    }
    return prefsIcons.get(c);
  }
  const catIcon = (c) => (c && (crewCatIcons[c] || prefsCatIcon(c))) || guessCatIcon(c);
  // The category chip in a crew popup head: its icon, the name (or 카테고리) and a caret.
  function catChipBody(c) { return [icon(c ? catIcon(c) : "tag"), raw(el("span", "", c || tr("카테고리"))), icon("caret-down")]; }
  // Category menu for the crew popups (expert · team · automation): pick one, clear it, or make a new one with a name and an icon.
  function catMenu(anchor, { list, current, onPick }) {
    document.querySelector(".cat-menu")?.remove();
    if (current && !list.includes(current)) list = [...list, current];
    const m = el("div", "cat-menu");
    m.setAttribute("role", "dialog");
    m.setAttribute("aria-label", "카테고리");
    const close = (refocus) => { m.remove(); document.removeEventListener("pointerdown", away, true); if (refocus && anchor.isConnected) anchor.focus(); };
    const away = (e) => { if (!m.contains(e.target) && !anchor.contains(e.target)) close(); };
    const pickNow = (v) => { close(true); onPick(v); };
    const item = (label, ic, on, fn, mine = true) => {
      const b = el("button", `cat-menu-item${on ? " on" : ""}`);
      b.type = "button";
      b.setAttribute("aria-pressed", String(on));
      b.append(icon(ic), mine ? raw(el("span", "", label)) : el("span", "", label));
      b.addEventListener("click", fn);
      m.append(b);
      return b;
    };
    // Each category: hover shows a pencil on the right that turns the row into the same name + icon form
    for (const c of list) {
      const b = item(c, catIcon(c), c === current, () => pickNow(c)), row = el("div", "cat-menu-row");
      const pen = el("button", "cat-menu-edit"); pen.type = "button"; pen.title = tr("카테고리 고치기"); pen.setAttribute("aria-label", `${tr("카테고리 고치기")}: ${c}`); pen.append(icon("pencil-simple"));
      pen.addEventListener("click", (e) => { e.stopPropagation(); const f = newForm({ name: c, icon: catIcon(c), onOk: (v, ic) => renameCat(c, v, ic) }); row.replaceWith(f); f.querySelector("input").focus(); place(); });
      b.replaceWith(row); row.append(b, pen);
    }
    // A new name moves every expert, team and automation in that category with it; the icon is kept on this PC.
    async function renameCat(old, v, ic) {
      if (v !== old && list.includes(v)) { notice(tr("같은 이름의 카테고리가 있어요.")); return; }
      crewCatIcons[v] = ic;
      let fail = 0;
      if (v !== old) for (const rec of (state.data.crew || []).filter((r) => r.crew?.category === old)) {
        try { const saved = await appApi.request(`/v1/crew/${encodeURIComponent(rec.id)}`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: rec.revision, crew: { ...rec.crew, category: v } }) });
          Object.assign(rec, saved?.crew ? saved : { crew: { ...rec.crew, category: v } }); } catch (_) { fail++; }
      }
      if (v !== old && !fail) delete crewCatIcons[old]; // records that failed to move keep the old name, so keep its icon too
      store("arkism.crewCatIcons", JSON.stringify(crewCatIcons));
      if (fail) notice(`${fail}개는 이름을 바꾸지 못했어요. 다시 해 주세요.`);
      if (current === old) pickNow(v); else { close(true); onPick(current); } // repaint the chip and lists with the new name/icon
    }
    if (current) item("카테고리 없음", "x", false, () => pickNow(""), false);
    const add = item("새 카테고리", "plus", false, () => { const f = newForm(); add.replaceWith(f); f.querySelector("input").focus(); place(); }, false);
    function newForm(edit) { // edit: { name, icon, onOk(name, icon) } for an existing category
      const f = el("div", "cat-new");
      const name = el("input");
      name.maxLength = 40; name.placeholder = "새 카테고리"; name.setAttribute("aria-label", edit ? "카테고리 이름" : "새 카테고리 이름"); if (edit) name.value = edit.name;
      let pick = edit && CAT_ICON_CHOICES.includes(edit.icon) ? edit.icon : "tag", picked = !!edit;
      const grid = el("div", "cat-icons");
      grid.setAttribute("role", "radiogroup"); grid.setAttribute("aria-label", "아이콘");
      const paint = () => grid.querySelectorAll("button").forEach((b) => { const on = b.dataset.icon === pick; b.setAttribute("aria-checked", String(on)); b.tabIndex = on ? 0 : -1; });
      const choose = (n, focus) => { pick = n; picked = true; paint(); if (focus) grid.querySelector(`[data-icon="${n}"]`).focus(); };
      for (const n of CAT_ICON_CHOICES) {
        const b = el("button", "cat-icon");
        b.type = "button"; b.dataset.icon = n; b.title = n;
        b.setAttribute("role", "radio"); b.setAttribute("aria-label", n);
        b.append(icon(n));
        b.addEventListener("click", () => choose(n));
        grid.append(b);
      }
      // Arrow keys move the choice, like any radio group; up/down jump a row of 8.
      grid.addEventListener("keydown", (e) => {
        const step = { ArrowRight: 1, ArrowLeft: -1, ArrowDown: 8, ArrowUp: -8 }[e.key];
        if (!step) return;
        e.preventDefault();
        const i = CAT_ICON_CHOICES.indexOf(pick), n = CAT_ICON_CHOICES.length;
        choose(CAT_ICON_CHOICES[(i + step + n) % n], true);
      });
      // Until an icon is picked, it follows the name (디자인 → palette).
      name.addEventListener("input", () => { if (!picked) { const g = guessCatIcon(name.value.trim()); pick = CAT_ICON_CHOICES.includes(g) ? g : "tag"; paint(); } });
      const ok = () => {
        const v = name.value.trim();
        if (!v) { name.focus(); return; }
        if (edit) { if (!f.inert) { f.inert = true; edit.onOk(v, pick); } return; } // one rename at a time while the moves run
        crewCatIcons[v] = pick;
        store("arkism.crewCatIcons", JSON.stringify(crewCatIcons));
        pickNow(v);
      };
      name.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.isComposing) { e.preventDefault(); ok(); } });
      const foot = el("div", "cat-new-foot");
      foot.append(button("취소", "ghost", null, () => close(true)), button(edit ? "저장" : "추가", "primary", null, ok));
      f.append(name, el("div", "cat-new-label", "아이콘"), grid, foot);
      paint();
      return f;
    }
    m.addEventListener("keydown", (e) => { if (e.key === "Escape" && !e.defaultPrevented) { e.preventDefault(); e.stopPropagation(); close(true); } });
    // Fixed under the chip, inside its dialog (the top layer), kept on screen.
    function place() {
      const r = anchor.getBoundingClientRect(), w = m.offsetWidth, hgt = m.offsetHeight;
      const left = Math.max(8, Math.min(r.left, innerWidth - w - 8)), top = r.bottom + 4 + hgt > innerHeight - 8 ? Math.max(8, r.top - 4 - hgt) : r.bottom + 4;
      m.style.left = `${left}px`; m.style.top = `${top}px`;
      // a dragged dialog (translate) holds its fixed children: move by what the menu actually landed off
      const got = m.getBoundingClientRect();
      m.style.left = `${2 * left - got.left}px`; m.style.top = `${2 * top - got.top}px`;
    }
    (anchor.closest("dialog") || document.body).append(m);
    place();
    document.addEventListener("pointerdown", away, true);
    (m.querySelector(".cat-menu-item.on") || m.querySelector(".cat-menu-item"))?.focus();
  }
  function button(label, cls, iconName, onClick) {
    const b = el("button", `btn ${cls || ""}`.trim());
    b.type = "button";
    if (iconName) b.append(icon(iconName));
    b.append(label);
    if (onClick) b.addEventListener("click", onClick);
    return b;
  }
  const appApi = createAppApi({
    get readJSON() { return readJSON; },
    get state() { return state; },
    get tauri() { return tauri; },
    get trimSlash() { return trimSlash; }
  });

  const memoryPaging = createMemoryPaging({
    get $() { return $; },
    get el() { return el; },
    get generation() { return state.connection.generation; },
    set generation(value) { state.connection.generation = value; },
    get query() { return appApi.query; },
    get render() { return render; },
    get request() { return appApi.request; },
    get state() { return state; }
  });

  const collectionPaging = createCollectionPaging({ state, request: appApi.request, createPager: window.ArkismMemoryPages.create });
  const virtualList = createVirtualList();
  const catalogQuery = createCatalogQuery({ state, request: appApi.request, createPager: window.ArkismMemoryPages.create,
    preferences: () => ({ categories: pages.prefs.categories, agents: shownAgents().map(([id]) => id), kept: keptMemories(), collectUnused: pages.prefs.collectUnused !== false }), render: reset => render(reset) });

  function ago(value) {
    if (!value) return "—";
    const t = new Date(value).getTime();
    if (Number.isNaN(t)) return "—";
    const m = Math.floor((Date.now() - t) / 60000);
    if (m < 1) return "방금";
    if (m < 60) return `${m}분 전`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h}시간 전`;
    const d = Math.floor(h / 24);
    if (d === 1) return "어제";
    if (d < 14) return `${d}일 전`;
    return new Intl.DateTimeFormat(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric" }).format(new Date(value));
  }
  const shortDate = (v) => v ? new Intl.DateTimeFormat(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric" }).format(new Date(v)) : "—";
  const kindLabel = (k) => (KINDS[k] || [k || "기억"])[0];
  const memRepo = (m) => (m.tags || []).find((t) => t.startsWith("repo:"))?.slice(5) || "";
  const tagValue = (item, prefix) => (item.tags || []).find((t) => t.startsWith(prefix))?.slice(prefix.length) || "";
  function agentOf(item) {
    const ev = tagValue(item, "event_id:");
    const key = ev.split(":")[0];
    return ORIGINS[key] || "";
  }
  const withAgent = (item) => (agentOf(item) ? `${originOf(item)} · ${agentOf(item)}` : originOf(item));
  const isReflection = (item) => (item.generator?.prompt_version || "").startsWith("session-reflect");
  function originOf(item) {
    const tags = item.tags || [];
    if (isReflection(item)) return "세션 반성";
    if (tags.includes("basis:explicit_user")) return "사용자 말";
    if (tags.includes("event:backfill")) return "가져옴";
    if (tags.includes("basis:assistant_result")) return "에이전트 결과";
    if (tags.includes("basis:inferred")) return "AI 추측";
    return item.created_by_type === "human" ? "직접 입력" : "가져옴";
  }
  // A captured correction waits for a person; the memory it corrects stays in use until then.
  const isCorrection = (item) => (item.tags || []).includes("review:pending_correction");
  function waitReason(item) {
    if ((item.tags || []).includes("review:instruction_like")) return ["명령처럼 보이는 문장 — 내가 확인", true];
    if (isCorrection(item)) return ["정정 대기 — 내가 확인", true];
    if (item.status === "conflicted") return ["기존 기억과 부딪힘", true];
    if ((item.tags || []).includes("basis:assistant_result")) return ["에이전트 결과 — 확인 필요", false];
    if ((item.tags || []).includes("basis:inferred")) return ["AI 추측 — 사용자가 말하지 않음", false];
    return ["검토 필요", false];
  }
  const standing = (m) => m.kind === "user" || m.kind === "procedural";
  function usageOf(id) {
    if (!state.usageDone) return null;
    const u = state.usage.get(id);
    return { d7: u?.d7 || 0, d30: u?.d30 || 0, label7: String(u?.d7 || 0), last: u?.last || "" };
  }
  // Legacy `used` counts text overlap, not actual answer use. Review suggestions are manual.
  const memUse = () => state.data.tools?.memory_use || null;
  function memStat(id) {
    const u = memUse();
    if (!u) return null;
    if (!u._by) u._by = new Map(u.items.map((x) => [x.id, x]));
    return u._by.get(id) || { injected: 0, used: 0 };
  }
  const KEEP_KEY = "arkism.keptMemories";
  const keptMemories = () => { try { return JSON.parse(localStorage.getItem(KEEP_KEY) || "[]"); } catch (_) { return []; } };
  const overlapReview = (m) => { const s = memStat(m.id); return !!s && s.injected >= 3 && s.used === 0 && !keptMemories().includes(m.id); };
  function savedHow(m) {
    return `${originOf(m)} · ${window.ArkismMemoryManagement.approval(m).label}`;
  }
  const reviewOverdue = (m) => !!m.review_due_at && new Date(m.review_due_at).getTime() <= Date.now();
  const idleFromUsage = (m) => {
    if (pages.prefs.collectUnused !== false && typeof overlapReview === "function" && overlapReview(m)) return true;
    const u = usageOf(m.id);
    return !!u && u.d30 === 0 && !standing(m) && Date.now() - new Date(m.created_at).getTime() > 7 * DAY;
  };
  const idle = m => reviewOverdue(m) || idleFromUsage(m);
  const approvalUnproven = m => window.ArkismMemoryManagement.approval(m).state !== "review_recorded";
  const matches = (text) => !state.query || text.toLowerCase().includes(state.query);

  function setStatus(mode, text) {
    $("#status-dot").className = `dot ${mode}`;
    $("#status-dot").dataset.mode = mode;
    $("#status-text").textContent = text;
    // all well: the rows below already show it in colour, so the summary line only speaks up for a problem
    $("#status-text").parentElement.hidden = mode === "ready";
  }
  function dbLabel() {
    if (state.ready === null) return "확인 중";
    return state.ready ? "Postgres" : "응답 없음";
  }
  // The sidebar card: one line when all is well, the details behind "주의 N ⌄".
  // Each agent's link to Arkism, from the server's minute report (agent_tools): is it installed and read,
  // did the host report lately, and does any of its MCP servers wait for a sign-in.
  function agentHealth(key) {
    const t = state.data.tools;
    if (!t?.collected_at) return state.loading ? ["", "확인 중", "서버 보고를 기다리는 중"] : ["", "알 수 없음", "서버 보고를 받지 못했어요"];
    const age = Math.round((Date.now() - new Date(t.collected_at).getTime()) / 60000);
    if (age > 15) return ["bad", "끊김", `서버 보고가 ${age}분째 없어요`];
    const info = (t.agents || []).find((a) => a.id === key);
    // the report is in, so an agent it leaves out is not still being checked (#189)
    if (!info) return t.agents ? ["bad", "연결 안 됨", "서버 보고에 이 에이전트가 없어요"] : ["", "알 수 없음", "이 서버는 에이전트 상태를 보고하지 않아요"];
    if (!info.found) return ["bad", "설치 안 됨", "서버에 설치돼 있지 않아요"];
    if (!info.enabled) return ["bad", "꺼짐", "Arkism이 읽지 않아요 (설정 › 에이전트)"];
    const login = (t.servers || []).filter((x) => x.agents?.[key] === "on" && x.login?.[key] === "needed").map((x) => x.name);
    if (login.length) return ["warn", "로그인 필요", `MCP 로그인 필요: ${login.join(", ")}`];
    if (age > 3) return ["warn", "느림", `마지막 확인 ${age}분 전`];
    return ["ok", "연결됨", "연결됨"];
  }
  function renderStatusCard() {
    const d = state.data;
    const host = (() => { try { return new URL(state.server).host; } catch (_) { return state.server; } })();
    const skillsUnknown = !!(state.listErrors.skills || state.loadError);
    const items = [
      ["brain", "메모리 DB", dbLabel(), state.ready !== false],
      ["database", "서버", host, true],
      ["git-merge", "스킬 허브", skillsUnknown ? "—" : `${d.skills.length}${collectionPaging.snapshot("skills").cursor ? "+" : ""}개`, !skillsUnknown],
      ["lock-simple", "로그인", state.email || "토큰", true]
    ];
    const failedLists = Object.keys(state.listErrors).length;
    if (failedLists) items.push(["database", "불러오지 못한 목록", `${failedLists}개`, false]); // #186
    // 설정 › 에이전트에서 끈 에이전트는 타일과 목록에서 뺀다.
    syncExtraAgents();
    const tiles = [...document.querySelectorAll("#status-agents [data-agent]")];
    const off = new Set(pages.prefs.agentsOff);
    tiles.forEach((tile) => { tile.hidden = off.has(tile.dataset.agent); });
    // The tiles carry each agent's health (border colour, hover text); no text list repeats it.
    const agents = tiles.filter((tile) => !tile.hidden).map((tile) => {
      const name = tile.title.split(" · ")[0];
      const [cls, label, why] = agentHealth(tile.dataset.agent);
      tile.classList.remove("ok", "warn", "bad");
      if (cls) tile.classList.add(cls);
      tile.title = `${name} · ${why}`;
      // v4 (Figma 198:3468): each agent is a row — logo, name, what to do next, state badge.
      if (!tile.querySelector(".st-name")) tile.append(el("span", "st-name"), el("output", "st-badge"));
      const nameBox = tile.querySelector(".st-name");
      nameBox.replaceChildren(el("span", "", name));
      if (cls === "warn" || cls === "bad") {
        const fix = el("button", "st-fix", label === "로그인 필요" ? "MCP 로그인 ›" : "에이전트 관리 ›");
        fix.type = "button";
        fix.addEventListener("click", () => { card.hidden = true; if (label === "로그인 필요") { state.toolTab = "mcp"; state.toolAgent = tile.dataset.agent; return go("tools"); } state.settingsTab = "agents"; go("settings"); });
        nameBox.append(fix);
      }
      tile.querySelector(".st-badge").textContent = label;
      tile.querySelector(".st-badge").className = `st-badge ${cls || ""}`.trim();
      return cls;
    });
    const issues = items.filter((i) => !i[3]).length + agents.filter((cls) => cls === "warn" || cls === "bad").length;
    const badge = $("#status-toggle");
    // Icon colour follows the worst state: red = server/DB down or an agent missing, amber = needs a look, green = fine.
    const level = $("#status-dot").dataset.mode !== "ready" || items.some((i) => !i[3]) || agents.includes("bad") ? "bad" : issues ? "warn" : "ok";
    badge.classList.remove("ok", "warn", "bad"); badge.classList.add(level);
    $("#status-badge").textContent = issues ? `주의 ${issues}` : "자세히";
    const card = $("#status-card");
    $("#status-list").replaceChildren(...items.map(([ic, label, value, ok]) => {
      const li = el("li", ok ? "" : "warn");
      li.append(icon(ic), el("span", "", label), raw(el("output", "", value)), el("i", `dot ${ok ? "ready" : "error"}`));
      return li;
    }));
    if (!card.querySelector(".st-foot")) {
      const foot = el("div", "st-foot");
      foot.append(el("p", "", "상태 아이콘 색: 초록 정상 · 주황 확인 필요 · 빨강 연결 문제·멈춤"), button("설정", "", null, () => { card.hidden = true; openSettings(); }));
      card.append(foot);
    }
    const newest = d.memories.reduce((t, m) => Math.max(t, new Date(m.created_at).getTime() || 0), 0);
    if (!card.hidden) renderStatusStages(card);
    const last = newest ? ago(new Date(newest)) : "—";
    $("#status-meta").textContent = state.memoryTotals ? `기억 ${memoryPaging.memoryCount()}개 · 마지막 저장 ${last}` : `불러온 기억 ${memoryPaging.memoryCount()}개 · 마지막 저장 ${last}`;
  }
  // 기억 처리 stages in the card: the same stages() the MEMORY › 개요 grid draws (overview.js), so a stopped stage shows here too.
  // Only while the card is open: the sums walk every memory, and overview is set up after the first renders.
  const STAGE_LEVEL = { g: ["ok", "정상"], y: ["warn", "곧 확인"], o: ["warn", "확인 필요"], r: ["bad", "멈춤"] };
  function renderStatusStages(card) {
    void overview.loadSessions(); // cached for a minute; calls renderStatusCard again when it lands
    const list = overview.stages();
    $("#status-stages").replaceChildren(...[["save", "저장"], ["use", "사용"], ["dream", "드리밍"], ["approve", "승인"]].map(([g, label]) => {
      const sec = el("div", "st-stage-group");
      sec.append(el("span", "st-stage-sec", label));
      const ul = el("ul", "status-list");
      for (const s of list.filter((x) => x.group === g)) {
        const [lv, word] = STAGE_LEVEL[s.health] || STAGE_LEVEL.o;
        const b = el("button", `st-stage ${lv}`);
        b.type = "button";
        b.title = `${s.label} · ${word} · ${s.bigSub} ${s.big}`;
        b.append(el("i", `st-dot ${lv}`), el("span", "", s.label), el("output", "", s.big), el("small", "", s.bigSub));
        b.addEventListener("click", () => { card.hidden = true; $("#status-toggle").setAttribute("aria-expanded", "false"); go("overview"); overview.openStage(s.key); });
        const li = el("li");
        li.append(b);
        ul.append(li);
      }
      sec.append(ul);
      return sec;
    }));
    const stopped = list.filter((s) => s.health === "r").length;
    const badge = $("#status-stages-badge");
    badge.className = `st-badge${stopped ? " bad" : ""}`;
    badge.textContent = stopped ? `${stopped}곳 멈춤` : list.some((s) => s.unknown) ? "알 수 없음" : "모두 움직임";
  }
  // 취소·닫기 buttons close their dialog by submitting it with value "cancel": skip the form check,
  // so an empty required field (새 팀·새 자동화의 이름 …) never stops a dialog from closing.
  document.addEventListener("click", (e) => {
    const b = e.target.closest?.('dialog button[value="cancel"]');
    if (b) b.formNoValidate = true;
  }, true);
  // Enter in a dialog's field means its main button (저장·만들기 …), never the ✕ that comes first in the form.
  // Korean input confirms a syllable with Enter too, so that Enter is left alone; so is a field that handles its own
  // Enter (사진 찾기 …): this listens after it and skips an Enter it already took (preventDefault).
  document.addEventListener("keydown", (e) => {
    const t = e.target;
    if (e.key !== "Enter" || e.defaultPrevented || e.isComposing || !(t instanceof HTMLInputElement) || ["button", "submit", "checkbox", "radio", "file"].includes(t.type)) return;
    const form = t.closest("dialog form");
    if (!form) return;
    e.preventDefault();
    const submits = [...form.querySelectorAll("button")].filter((b) => b.type === "submit" && b.value !== "cancel" && !b.disabled);
    const main = submits.find((b) => b.classList.contains("primary")) || submits.at(-1);
    if (main) form.requestSubmit(main);
  });
  // 알림 줄: 문구 하나와 닫기(×). 다른 화면으로 가도 지워진다.
  function notice(text) {
    // An open window (dialog) covers the page, so its messages show inside that window instead.
    const top = [...document.querySelectorAll("dialog[open]")].pop();
    document.querySelectorAll(".dlg-notice").forEach((x) => { if (x.parentElement !== top) x.remove(); });
    let n = $("#notice");
    if (top) {
      n.hidden = true;
      n = top.querySelector(":scope > .dlg-notice") || top.appendChild(el("output", "notice dlg-notice"));
    }
    n.replaceChildren();
    n.classList.remove("st-notice");
    n.hidden = !text;
    if (!text) return;
    const close = el("button", "notice-close");
    close.type = "button";
    close.setAttribute("aria-label", "알림 닫기");
    close.append(icon("x"));
    close.addEventListener("click", () => notice(""));
    n.append(el("span", "", text), close);
  }

  // 알림 자리에 공통 상태 화면 하나 (Figma 198:3554, home-checks.js ArkismState.of), 닫기(×)는 그대로.
  function stateNotice(kind, on, over) {
    notice("");
    const n = $("#notice");
    n.hidden = false;
    n.classList.add("st-notice");
    const close = el("button", "notice-close");
    close.type = "button";
    close.setAttribute("aria-label", "알림 닫기");
    close.append(icon("x"));
    close.addEventListener("click", () => notice(""));
    n.replaceChildren(window.ArkismState.of(kind, on, over), close);
  }
  // 권한 오류(access_denied)는 공통 권한 화면으로; 다른 오류는 그대로 알림.
  function failNotice(e, denied, other) {
    if (e.message !== "access_denied") return notice(other);
    stateNotice("denied", { account: () => { notice(""); go("settings"); } }, { text: denied });
  }
  // ---------------------------------------------------------------- data
  async function loadAll() {
    const g = ++state.connection.generation, t0 = performance.now();
    state.loading = true;
    collectionPaging.reset(); catalogQuery.reset(); similarity.reset();
    memoryPaging.memoryPages.reset(); memoryPaging.memoryResults.reset(); memoryPaging.maintenanceRecords.clear(); memoryPaging.legacyMemorySearch.clear();
    state.usage = new Map();
    state.usageDone = false;
    state.usageFailed = false;
    state.memoryTotals = null;
    state.candidateTotal = null;
    state.listErrors = {};
    state.versions = new Map();
    state.versionsDone = false;
    render();
    try {
      const canReview = !!state.caps?.actions?.review_list;
      void appApi.request("/readyz").then(() => true, () => false).then((ok) => { if (g === state.connection.generation) { state.ready = ok; renderStatusCard(); } });
      const reviewsReq = reviews.load();
      const trashReq = loadTrash();
      const usageReq = appApi.request(`/v1/skills/usage?${appApi.query({ days: 60 })}`).then((r) => new Map((r.items || []).map((u) => [u.skill_id, u])), () => null);
      // Each list fails on its own and keeps its empty rows; only the memory list fails the whole load.
      const listErrors = {}, settle = (name, req, empty) => req.catch((e) => { listErrors[name] = loadFailure(e); return empty; });
      const [memories, candidates, conflicted, maintenance, skills, prompts, designSystems, tools, crewItems, providers, skillUse, reviewItems, skillTrash] = await Promise.all([
        memoryPaging.memoryPages.load(memoryPaging.memoryKey()),
        canReview ? settle("candidates", collectionPaging.read("candidates"), []) : [],
        canReview ? collectionPaging.read("conflicted").catch(() => []) : [],
        canReview ? loadMaintenance() : [],
        settle("skills", collectionPaging.read("skills", true), []),
        appApi.request(`/v1/prompts?${appApi.query()}`).then((r) => r.items || [], () => []),
        appApi.request(`/v1/design_systems?${appApi.query()}`).then((r) => r.items || [], () => null),
        toolViews.loadTools(),
        settle("crew", crew.load(), []),
        models.load(), usageReq, reviewsReq, trashReq
      ]);
      if (g !== state.connection.generation) return;
      state.data = { memories, candidates, conflicted, maintenance, skills, prompts, designSystems, tools, crew: crewItems, providers, skillUse, reviews: reviewItems, skillTrash };
      window.ArkismPerf?.note("load.all", performance.now() - t0, memories.length);
      void memoryPaging.hydrateMaintenance(g);
      state.loading = false;
      state.loadError = "";
      state.listErrors = listErrors;
      if (Object.keys(listErrors).length) setStatus("warn", "일부 목록을 불러오지 못했어요");
      else setStatus("ready", "기억 저장소·검색 정상");
      render();
      renderStatusCard();
      void loadUsage(g);
    } catch (e) {
      if (g !== state.connection.generation) return;
      state.loading = false;
      state.loadError = loadFailure(e); // { code, reached, title, text }
      state.data = emptyData(); // the last good load's agents and skills are not the current state (#187)
      setStatus("error", "연결 확인 필요");
      render();
      stateNotice("loadFailed", { retry: () => { notice(""); void loadAll(); } }, { note: state.loadError.reached && state.view !== "todo" ? state.loadError.text : "연결을 확인하고 다시 시도해 주세요." }); // Home's card already says the reason
    }
  }
  // Server-wide totals (POST /v1/memories/query, one row each): every active memory and the idle ones, with kinds.
  // A server without the query keeps memoryTotals null and screens say "loaded so far" instead.
  async function loadMemoryTotals(g) {
    const read = (s) => appApi.request("/v1/memories/query", { method: "POST", body: JSON.stringify({ scope: state.scope, state: s, limit: 1, kept_ids: keptMemories(), collect_unused: pages.prefs.collectUnused !== false }) })
      .then((r) => (Number.isSafeInteger(r?.total) ? r : null), () => null);
    const candidates = state.caps?.actions?.review_list ? appApi.request("/v1/memory_candidates/query", { method: "POST", body: JSON.stringify({ scope: state.scope, state: "candidate", limit: 1 }) })
      .then((r) => (Number.isSafeInteger(r?.total) ? r.total : null), () => null) : null;
    const [active, idleRows, candidateTotal] = await Promise.all([read("active"), read("idle"), candidates]);
    if (g !== state.connection.generation) return;
    state.candidateTotal = candidateTotal; // kept even when the memory totals failed
    if (active) state.memoryTotals = { active: active.total, idle: idleRows?.total ?? null, kinds: active.facets?.kinds || null };
    render(false);
  }
  // Use counts of every memory in one call (GET /v1/memory_usage: 7·30 days and the last use; unused memories are absent).
  async function loadUsage(g) {
    memoryPaging.lastMemoryUsage = Date.now();
    const t0 = performance.now();
    void loadMemoryTotals(g);
    const items = await appApi.request(`/v1/memory_usage?${appApi.query()}`).then((r) => r.items || [], () => null);
    if (g !== state.connection.generation) return;
    if (!items) { state.usageFailed = true; render(false); overview.usageChanged(); return; } // older server, no read:usage or a timeout: 쓰임 stays "…" instead of calling every memory one by one
    state.usage = new Map(items.map((u) => [u.id, u]));
    state.usageDone = true;
    state.usageFailed = false; // a visibleSync retry on the same connection recovered
    window.ArkismPerf?.note("load.usage", performance.now() - t0, items.length);
    render(false);
    overview.usageChanged();
  }

  // shortcut: one version call per skill (6 at a time) for 품질·보안; add both to the skill list API if this gets slow.
  let versionsLoading = -1;
  async function loadVersions(g) {
    if (g !== state.connection.generation || versionsLoading === g) return;
    versionsLoading = g;
    const versions = state.versions;
    const loadedSkills = [...state.data.skills, ...catalogQuery.snapshot("skills").items, ...catalogQuery.snapshot("skillTrash").items];
    const queue = loadedSkills.filter((sk) => sk.version_id && !versions.has(sk.version_id));
    const worker = async () => {
      while (queue.length && g === state.connection.generation && versions === state.versions) {
        const sk = queue.shift();
        const version = await appApi.request(`/v1/skill_versions/${encodeURIComponent(sk.version_id)}?${appApi.query()}`).catch(() => null);
        if (g !== state.connection.generation) return;
        if (versions !== state.versions) return;
        versions.set(sk.version_id, version);
      }
    };
    await Promise.all(Array.from({ length: 6 }, worker));
    if (g !== state.connection.generation) return;
    if (versions !== state.versions) return;
    versionsLoading = -1;
    state.versionsDone = [...state.data.skills, ...catalogQuery.snapshot("skills").items, ...catalogQuery.snapshot("skillTrash").items].every(sk => !sk.version_id || versions.has(sk.version_id));
    render(false);
  }

  const syncVisible = createVisibleSync({
    state, memoryPaging, collectionPaging, catalogQuery, request: appApi.request, query: appApi.query,
    loadTools: () => toolViews.loadTools(), loadUsage, render,
    get routing() { return routing; }, get digests() { return digests; },
    panels: () => ({ relations, traits, reflections }),
    invalidateVersions: () => { versionsLoading = -1; }
  });
  document.addEventListener("input", (event) => {
    if (event.target.closest("#detail") && event.target.matches("input, textarea, select, [contenteditable=true]")) event.target.dataset.syncDirty = "true";
  });
  setInterval(syncVisible, 10000);
  window.addEventListener("focus", syncVisible);
  // 언어를 바꾸면 미리 번역한 글자(tr)와 날짜 형식도 새로 그린다.
  let drawnLang = window.ArkismI18n?.lang();
  window.addEventListener("arkism-look", () => { const l = window.ArkismI18n?.lang(); if (l !== drawnLang) { drawnLang = l; render(false); } });
  window.addEventListener("online", syncVisible);
  document.addEventListener("visibilitychange", syncVisible);

  // ---------------------------------------------------------------- shell
  let shell = null; // shell.js: left menus and area tabs, set up once the screens exist
  const views = {
    todo: { title: () => todoTitle(), sub: () => todoSub(), count: () => todoCount() },
    memories: { title: () => memoryList.memTitle()[0], sub: () => memoryList.memTitle()[1], count: () => memoryList.memTitle()[2] },
    skills: { title: () => (state.skillPage?.kind === "improve" ? `${(catalogQuery.record(`skill:${state.skillPage.id}`) || state.data.skills.find((x) => x.id === state.skillPage.id))?.name || "스킬"} 개선하기` : { install: "GitHub에서 스킬 설치" }[state.skillPage?.kind] || "스킬"),
      sub: () => ({ improve: "개선안 만들기 → 기존 버전과 비교 → 검토·승인 → 실제 작업에 시험 적용 순서로 진행해요.",
        install: "저장소 주소를 넣으면 안에 있는 스킬을 보여 줘요. 받은 파일은 실행하지 않아요." }[state.skillPage?.kind] || "에이전트가 일할 때 꺼내 쓰는 작업 방법이에요.") },
    design: { title: "디자인 시스템", sub: () => "앱·메일·웹·문서에 쓰는 색·글꼴·규칙 모음이에요. 하나를 고르면 오른쪽에서 자세히 볼 수 있어요." },
    images: {
      title: () => (state.promptRun ? `프롬프트 실행${(state.data.prompts.find((x) => x.id === state.promptRun)?.image_prompt.engine || "").startsWith("comfyui") ? " · ComfyUI" : ""}` : state.imageTab === "slide" ? "슬라이드 프롬프트" : "이미지 프롬프트"),
      sub: () => (state.promptRun ? "변수 값을 채우고 바로 만들거나, ComfyUI 워크플로우로 보내요." : state.imageTab === "slide" ? "발표·보고용 슬라이드 프롬프트와 만드는 스크립트를 모아 둬요. 카드를 고르면 오른쪽에서 고치고 바로 만들 수 있어요." : "자주 쓰는 이미지 프롬프트를 예시 이미지와 함께 모아 둬요. 모델과 카테고리로 골라 보세요.")
    },
    tools: { title: () => (state.toolTab === "plugin" ? "플러그인" : "MCP"), sub: () => (state.toolTab === "plugin" ? "플러그인은 스킬 + MCP + 명령을 한 번에 설치하는 묶음이에요." : "에이전트가 쓰는 외부 도구 서버예요. 플러그인은 스킬·MCP를 묶어서 한 번에 설치해요.") }
  };
  // Everything worth a look, most urgent first; the first three become "지금 할 일".
  // ---------------------------------------------------------------- 할 일: 새 스킬 제안 · 비슷한 스킬 · 안 쓰는 스킬
  const SUGGEST_KEY = "arkism.skillSuggest", SIMILAR_KEY = "arkism.similarDismissed";
  // Function declarations: the skills table reads these before this part of the file has run.
  function readKey(k, d) { try { return JSON.parse(localStorage.getItem(k) || "null") || d; } catch { return d; } } // private window: default
  function writeKey(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch { /* private window: not kept */ } }
  const suggestions = () => { const s = readKey(SUGGEST_KEY, { at: "", items: [], dismissed: [] }); return { ...s, items: s.items.filter((x) => !s.dismissed.includes(x.name) && !state.data.skills.some((sk) => sk.name === x.name)) }; };
  let analyzing = false;
  async function analyze() {
    if (analyzing) return;
    analyzing = true;
    renderHeadActions();
    const waitText = "AI가 최근 기억과 후보를 보고 새 스킬을 찾는 중이에요…";
    stateNotice("aiWait", {}, { text: waitText });
    const slow = setTimeout(() => { if (analyzing && !$("#notice").hidden) stateNotice("aiWait", {}, { text: waitText, slow: true }); }, 60000);
    const d = state.data;
    const lines = [...d.candidates, ...d.memories].filter((m) => ["procedural", "lesson", "decision"].includes(m.kind) || d.candidates.includes(m)).slice(0, 80).map((m) => `- ${(m.text || "").slice(0, 160)}`).join("\n");
    const prompt = `Existing skills: ${d.skills.map((sk) => sk.name).join(", ").slice(0, 4000)}\n\nRecent memories and memory candidates (traces of work the user asked for again and again):\n${lines || "(none)"}`;
    try {
      const a = await crew.assist("suggest", prompt, { purpose: "skill" });
      if (a.status !== "done") throw new Error(crew.assistError(a));
      const bracketText = a.output || "";
      const openAt = bracketText.indexOf("[");
      const closeAt = bracketText.lastIndexOf("]");
      const m = openAt >= 0 && closeAt > openAt ? [bracketText.slice(openAt, closeAt + 1)] : null;
      const items = (m ? JSON.parse(m[0]) : []).filter((x) => /^[a-z0-9][a-z0-9-]{0,63}$/.test(x?.name || "") && x.skill_md).slice(0, 3);
      const prev = readKey(SUGGEST_KEY, { dismissed: [] });
      writeKey(SUGGEST_KEY, { at: new Date().toISOString(), items, dismissed: prev.dismissed || [] });
      notice(items.length ? `새 스킬 제안 ${items.length}개를 찾았어요.` : "지금은 새 스킬로 만들 만한 반복 요청이 없어요.");
    } catch (e) {
      notice(e.message.startsWith("Unexpected") ? "AI 답을 읽지 못했어요. 다시 해 주세요." : e.message);
    } finally {
      clearTimeout(slow);
      analyzing = false;
      render(false);
    }
  }
  const similarity = createSkillSimilarity({ notify: () => { if (!state.token || state.loading) return; if (state.view === "todo") render(false); else updateCounts(); } });
  const similarPairs = () => similarity.read(state.data.skills, readKey(SIMILAR_KEY, []));
  async function setSkillStatus(sk, status) {
    const path = `/v1/skills/${encodeURIComponent(sk.id)}`;
    const cur = await appApi.request(`${path}?${appApi.query()}`);
    await appApi.request(`${path}/availability`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: cur.revision, status, compatibility: cur.skill_compatibility || {} }) });
  }
  // 14 일괄 작업 확인: show the exact targets before moving them.
  function confirmBatch(list, onOk) {
    const dlg = el("dialog", "crew-dialog");
    const form = el("form");
    form.method = "dialog";
    const head = el("div", "dialog-head");
    const t = el("div", "");
    t.append(el("h2", "", `스킬 ${list.length}개를 휴지통으로 옮길까요?`), el("p", "", "이 스킬을 쓰던 에이전트는 더 이상 읽지 못해요. 스킬 › 휴지통에서 언제든 되돌릴 수 있어요."));
    head.append(t);
    const body = el("div", "dialog-body");
    const ul = el("ul", "batch-list");
    for (const sk of list) {
      const li = el("li", "");
      li.append(el("code", "", sk.name), el("small", "dim", `60일 동안 0번 · ${shownAgents().filter(([k]) => skillViews.allowed(sk, k)).length}곳에서 읽음`));
      ul.append(li);
    }
    body.append(ul);
    const foot = el("div", "dialog-foot");
    const cancel = button("취소", "ghost");
    cancel.type = "submit";
    cancel.value = "cancel";
    const ok = button(`${list.length}개 옮기기`, "primary");
    ok.type = "submit";
    ok.value = "ok";
    foot.append(cancel, ok);
    form.append(head, body, foot);
    form.addEventListener("submit", (e) => { if (e.submitter?.value === "ok") onOk(); });
    dlg.append(form);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal();
  }
  // 휴지통 comes from the server (GET /v1/skills/trash) so every device sees it; an older server without that
  // list falls back to what this app remembers moving there.
  const TRASH_KEY = "arkism.skillTrash";
  function loadTrash() {
    return collectionPaging.read("skillTrash", true).catch(() => {
      const previous = collectionPaging.snapshot("skillTrash");
      return previous.ready ? previous.items : null;
    });
  }
  // The server's answer is the whole truth (it leaves out erased and unreadable skills); this device's own
  // records only stand in for a server without the list.
  function trashed() { return state.data.skillTrash ?? readKey(TRASH_KEY, []); }
  async function trashSkills(list) {
    let done = 0;
    for (const sk of list) {
      try {
        await setSkillStatus(sk, "deprecated");
        writeKey(TRASH_KEY, [...readKey(TRASH_KEY, []).filter((x) => x.id !== sk.id), { id: sk.id, name: sk.name, description: sk.description || "", status: "deprecated", skill_compatibility: sk.skill_compatibility || {}, updated_at: new Date().toISOString() }]);
        done++;
      } catch (e) { notice(`${sk.name}를 옮기지 못했어요 (${e.message}).`); }
    }
    state.data.skills = await collectionPaging.read("skills", true).catch(() => state.data.skills);
    state.data.skillTrash = await loadTrash();
    await catalogQuery.refreshVisible();
    notice(`스킬 ${done}개를 휴지통으로 옮겼어요. 스킬 › 휴지통에서 되돌릴 수 있어요.`);
    render(false);
  }
  const AGENT_TARGETS = [["claude_code", "Claude Code", "img:claude.svg"], ["codex", "Codex", "sym:a-codex"], ["gemini", "Gemini CLI", "sym:a-gemini"], ["zcode", "ZCode", "sym:a-zcode"]];
  // 새 스킬 제안 (Figma 210:17783): 이런 요청 · 스킬 이름 · 초안 · 어디에 넣을까요.
  function suggestDetail(body, s) {
    const head = el("div", "home-sum-head");
    head.append(raw(el("h2", "", s.title || s.name)), raw(el("p", "meta", s.why || "")));
    body.append(head);
    if (s.examples?.length) {
      const sec = section("이런 요청이 있었어요");
      const q = el("div", "home-quote");
      s.examples.slice(0, 3).forEach((x) => q.append(raw(el("p", "", `“${x}”`))));
      sec.append(q);
      body.append(sec);
    }
    const name = el("input", "home-input");
    name.value = s.name;
    name.setAttribute("aria-label", "스킬 이름");
    const nf = section("스킬 이름");
    nf.append(name, el("p", "home-hint", "영문 소문자·숫자·하이픈만 써요"));
    const md = el("textarea", "");
    md.rows = 12;
    md.value = s.skill_md;
    md.setAttribute("aria-label", "초안 · SKILL.md");
    const mf = section("초안");
    const box = el("div", "home-md");
    box.append(el("span", "", "SKILL.md"), md);
    mf.append(box);
    const where = section("어디에 넣을까요");
    const picked = new Set(["claude_code", "codex", "gemini"]);
    const row = el("div", "home-targets");
    const count = el("small", "");
    const paint = () => { count.textContent = `쓰는 에이전트 ${picked.size}곳`; };
    for (const [k, n, spec] of AGENT_TARGETS) {
      const b = el("button", `home-target${picked.has(k) ? " on" : ""}`);
      b.type = "button";
      b.title = n;
      b.setAttribute("aria-pressed", String(picked.has(k)));
      b.append(agentLogo(spec, n));
      b.addEventListener("click", () => { picked.has(k) ? picked.delete(k) : picked.add(k); b.classList.toggle("on", picked.has(k)); b.setAttribute("aria-pressed", String(picked.has(k))); paint(); });
      row.append(b);
    }
    paint();
    row.append(count);
    where.append(row);
    const foot = el("div", "actions");
    const make = button("스킬 만들기", "primary", null, async () => {
      if (!/^[a-z0-9][a-z0-9-]{0,63}$/.test(name.value.trim())) { notice("스킬 이름은 영문 소문자·숫자·하이픈만 써요."); return; }
      make.disabled = true;
      try {
        const a = await crew.assist("install_skill", JSON.stringify({ name: name.value.trim(), content: md.value, agents: [...picked] }));
        if (a.status !== "done") throw new Error(crew.assistError(a));
        const paths = JSON.parse(a.output || "[]");
        const st = readKey(SUGGEST_KEY, { items: [], dismissed: [] });
        writeKey(SUGGEST_KEY, { ...st, dismissed: [...(st.dismissed || []), s.name] });
        notice(`만들었어요: ${paths.join(", ")}. 에이전트는 다음 대화부터 읽어요.`);
        render(false);
        renderDetail();
      } catch (e) { notice(e.message); } finally { make.disabled = false; }
    });
    const no = button("스킬이 아니에요", "ghost", null, () => {
      const st = readKey(SUGGEST_KEY, { items: [], dismissed: [] });
      writeKey(SUGGEST_KEY, { ...st, dismissed: [...(st.dismissed || []), s.name] });
      state.selected = null;
      render();
    });
    foot.append(no, make);
    body.append(nf, mf, where, foot);
  }
  // 비슷한 스킬 비교 (Figma 198:2178): 두 카드 · 남길 이름 · AI 안내 · 합치면 이렇게 돼요.
  function similarDetail(body, p) {
    const pct = Math.round(p.score * 100);
    const head = el("div", "home-sum-head");
    head.append(el("h2", "", `두 스킬이 ${pct}% 비슷해요`), el("p", "meta", "하나로 합치면 두 스킬을 쓰던 에이전트가 모두 합친 스킬을 써요."));
    const useOfSkill = (sk) => state.data.skillUse?.get(sk.id)?.count || 0;
    const most = Math.max(1, useOfSkill(p.a), useOfSkill(p.b));
    let keep = useOfSkill(p.b) > useOfSkill(p.a) ? p.b : p.a;
    const where = (sk) => shownAgents().filter(([k]) => skillViews.allowed(sk, k)).length;
    const useText = (sk) => (state.data.skillUse?.get(sk.id) ? `${useOfSkill(sk)}번 사용` : "사용 기록 없음");
    const cards = el("div", "home-compare");
    for (const sk of [p.a, p.b]) {
      const c = el("div", "");
      const use = el("div", "home-use");
      const bar = el("i", ""), fill = el("b", "");
      fill.style.width = `${(useOfSkill(sk) / most) * 100}%`;
      bar.append(fill);
      use.append(el("span", "", useText(sk)), bar);
      c.append(raw(el("code", "", sk.name)), use, raw(el("p", "", sk.description || "")));
      cards.append(c);
    }
    const pick = section("남길 이름");
    const options = el("div", "home-pair");
    const draw = () => options.replaceChildren(...[p.a, p.b].map((sk) => {
      const o = el("button", `home-option${keep === sk ? " on" : ""}`);
      o.type = "button";
      o.setAttribute("aria-pressed", String(keep === sk));
      o.append(el("i", ""), raw(el("code", "", sk.name)), el("small", "", `${useText(sk)} · 에이전트 ${where(sk)}곳`));
      o.addEventListener("click", () => { keep = sk; draw(); plan(); });
      return o;
    }));
    pick.append(options);
    const ai = el("div", "home-card");
    ai.append(el("span", "", "AI가 두 SKILL.md를 하나로 써 줘요"), el("small", "", "합치기 전에 결과를 보고 고칠 수 있어요"));
    const planBox = section("합치면 이렇게 돼요");
    const ul = el("ul", "home-bullets");
    planBox.append(ul);
    const plan = () => {
      const drop = keep === p.a ? p.b : p.a;
      ul.replaceChildren(...[`${keep.name}에 합친 SKILL.md를 써요 (원본 저장소에 붙여 넣어요)`, `${drop.name}는 휴지통으로 가요 (되돌릴 수 있어요)`, `${drop.name}를 쓰던 에이전트는 ${keep.name}를 쓰게 돼요`].map((t) => el("li", "", t)));
    };
    draw();
    plan();
    const out = el("div", "section");
    const foot = el("div", "actions");
    const preview = button("합쳐서 미리 보기", "primary", null, async () => {
      preview.disabled = true;
      out.replaceChildren(el("p", "meta", "AI가 두 SKILL.md를 하나로 쓰는 중이에요…"));
      try {
        const drop = keep === p.a ? p.b : p.a;
        // the server puts each whole SKILL.md in place of {{SKILL.md:path}}, so steps, limits and scripts survive the merge.
        const a = await crew.assist("draft", `Write one SKILL.md that merges the two skills below. Its name is ${keep.name}. Keep every step, constraint and script from both.\n\n===== ${keep.name}/SKILL.md =====\n{{SKILL.md:${keep.source_path}}}\n\n===== ${drop.name}/SKILL.md =====\n{{SKILL.md:${drop.source_path}}}`, { purpose: "skill" });
        if (a.status !== "done") throw new Error(crew.assistError(a));
        const ta = el("textarea", "");
        ta.rows = 12;
        ta.value = a.output.trim();
        const md = el("div", "home-md");
        md.append(el("span", "", `합친 SKILL.md · 원본 저장소에 붙여 넣어 주세요`), ta);
        const row = el("div", "actions");
        row.append(imageViews.copyButton(ta.value), button(`${drop.name} 휴지통으로`, "", "trash", () => confirmBatch([drop], () => trashSkills([drop]))));
        out.replaceChildren(md, row);
      } catch (e) { out.replaceChildren(el("p", "warn-text", e.message)); } finally { preview.disabled = false; }
    });
    const no = button("다른 스킬이에요", "ghost", null, () => { writeKey(SIMILAR_KEY, [...readKey(SIMILAR_KEY, []), p.key]); state.selected = null; render(); });
    foot.append(no, preview);
    body.append(head, cards, pick, ai, planBox, foot, out);
  }

  // 기억 정리 제안: the nightly job pairs memories that say the same thing, or where a
  // newer one contradicts an older one. A proposal holds ids only, so the texts come from the
  // active list; a pair whose memory is gone or changed is hidden (deciding it would go stale).
  function loadMaintenance() {
    return appApi.request(`/v1/maintenance_proposals?${appApi.query({ status: "open" })}`).then((r) => r.items || [], () => []);
  }
  const TIDY_REASON = {
    same_meaning: "두 기억이 같은 말을 해요. 하나만 있어도 에이전트가 똑같이 알아요.",
    more_specific: "남길 기억이 더 자세해요. 다른 하나의 내용이 거기에 들어 있다고 봤어요.",
    newer_replaces_older: "새 기억이 예전 기억과 반대예요. 지금 생각은 새 기억이라고 봤어요."
  };
  function maintenanceShown() {
    const byId = new Map([...memoryPaging.maintenanceRecords.values(), ...state.data.memories].map((m) => [m.id, m]));
    return (state.data.maintenance || []).flatMap((p) => {
      const m = p.maintenance || {};
      const keep = byId.get(m.keep?.id), retire = byId.get(m.retire?.id);
      if (!keep || !retire || keep.revision !== m.keep.revision || retire.revision !== m.retire.revision) return [];
      return [{ p, keep, retire, type: m.type, reason: m.reason }];
    });
  }
  const maintenanceTitle = (t) => t.type === "contradiction" ? "서로 다른 말을 하는 기억, 새 것으로 바꿀까요?" : "비슷한 기억 2개, 하나로 합칠까요?";
  // Figma v2 · 한글 "기억 정리 제안" → v4 198:2264: 남길 기억 · 이력으로 넘길 기억 · 왜 제안했나요 · 합치면 이렇게 돼요.
  function maintenanceDetail(body, t) {
    const all = maintenanceShown();
    const merge = t.type !== "contradiction";
    const at = (m) => m.effective_at || m.created_at;
    const keepNewer = new Date(at(t.keep)) >= new Date(at(t.retire));
    const head = el("div", "home-sum-head");
    head.append(el("h2", "", merge ? "같은 내용을 다른 말로 적었어요" : "서로 다른 말을 하고 있어요"),
      el("p", "meta", merge ? "두 기억이 같은 뜻이에요. 하나만 남겨 두면 AI가 덜 헷갈려요." : "두 기억을 함께 따를 수 없어요. 지금 생각에 맞는 쪽만 남겨요."));
    const card = (m, tag, on) => {
      const c = el("div", `home-mem${on ? " on" : ""}`);
      const meta = el("div", "home-mem-meta");
      meta.append(el("em", "", kindLabel(m.kind)), el("span", "", `${shortDate(at(m))} 저장`));
      const who = agentOf(m);
      if (who) {
        const spec = AGENTS.find(([, n]) => n === who)?.[2];
        meta.append(el("span", "", "·"));
        if (spec) meta.append(agentLogo(spec, who));
        meta.append(el("span", "", `${who} 대화`));
      }
      c.append(el("span", "", tag), raw(el("p", "", m.text)), meta);
      return c;
    };
    const cards = el("div", "home-pair");
    cards.append(card(t.keep, `남길 기억 · ${keepNewer ? "새 것" : "옛 것"}`, true), card(t.retire, `이력으로 넘길 기억 · ${keepNewer ? "옛 것" : "새 것"}`, false));
    const why = el("div", "home-card");
    why.append(el("strong", "", "왜 제안했나요"), el("p", "", TIDY_REASON[t.reason] || ""));
    const plan = section(merge ? "합치면 이렇게 돼요" : "바꾸면 이렇게 돼요");
    const ul = el("ul", "home-bullets");
    [`‘${t.keep.text}’를 계속 써요`, `‘${t.retire.text}’는 기억 이력으로 넘어가요`, "이력에서 언제든 다시 볼 수 있어요"].forEach((x) => ul.append(raw(el("li", "", x))));
    plan.append(ul);
    const decide = (decision, done) => memoryList.reviewOnce(t.p.id, async () => {
      await appApi.request(`/v1/maintenance_proposals/${encodeURIComponent(t.p.id)}/decide`, { method: "POST",
        body: JSON.stringify({ scope: state.scope, expected_revision: t.p.revision, decision }) });
      await memoryList.reloadMemories();
      notice(done);
    });
    const foot = el("div", "actions");
    foot.append(button("둘 다 두기", "ghost", null, () => decide("dismiss", "둘 다 그대로 뒀어요. 같은 제안은 다시 하지 않아요.")),
      button("반대로 남기기", "", null, () => decide("swap", "반대쪽 기억을 남겼어요. 다른 하나는 이력에 남아요.")),
      button(merge ? "제안대로 합치기" : "제안대로 바꾸기", "primary", null, () => decide("accept", merge ? "합쳤어요. 옛 기억은 이력에 남아요." : "바꿨어요. 예전 기억은 이력에 남아요.")));
    body.append(head, cards, why, plan, el("p", "meta", "어느 쪽을 골라도 지우지 않아요. 넘긴 기억은 기억 이력에 남아요." + (all.length > 1 ? ` · 남은 제안 ${all.length - 1}개` : "")), foot);
  }

  // 기억 검토: up to 5 candidates in one view, each set to keep or drop, sent as one review_batch.
  // A sentence that reads like an instruction starts as "버리기"; everything else starts as "저장".
  // The AI's risk call (ADR-0022) only orders the list: high risk first and shown in full, the rest in one line.
  const BATCH = 5;
  const RISK = { high: [0, "주의"], medium: [1, "보통"], low: [2, "낮음"] };
  const riskOf = (c) => RISK[c.classification?.risk]?.[0] ?? 3;
  const byRisk = (a, b) => riskOf(a) - riskOf(b) || new Date(b.created_at) - new Date(a.created_at);
  const riskLabel = (c) => RISK[c.classification?.risk] ? `위험도 ${RISK[c.classification.risk][1]}` : "";
  function reviewBatchDetail(body) {
    const list = [...state.data.candidates].sort(byRisk).slice(0, BATCH);
    const keep = new Map(list.map((c) => [c.id, !(c.tags || []).includes("review:instruction_like")]));
    body.append(el("h2", "", `기억 후보 ${list.length}개`), el("p", "lead", "눌러서 저장·버리기를 바꾸고 한 번에 반영해요. 14일 동안 안 보면 저절로 사라져요."));
    const cards = el("div", "tidy-cards");
    const paint = () => cards.replaceChildren(...list.map((c) => {
      const on = keep.get(c.id);
      const card = el("button", `sim-card${on ? " on" : " off"}`);
      card.type = "button";
      const suspect = (c.tags || []).includes("review:instruction_like");
      const open = riskOf(c) === 0;
      const text = el("p", open ? "" : "one-line", c.text);
      if (!open) text.title = c.text;
      card.append(el("span", on ? "tag" : "dim", on ? "저장" : "버리기"), raw(text),
        el("small", "dim", [kindLabel(c.kind), shortDate(c.created_at), riskLabel(c), suspect ? "지시처럼 보이는 문장" : ""].filter(Boolean).join(" · ")));
      if (open && c.classification.risk_reasons?.length) card.append(el("small", "dim", c.classification.risk_reasons.join(" · ")));
      card.addEventListener("click", () => { keep.set(c.id, !on); paint(); });
      return card;
    }));
    paint();
    const apply = () => memoryList.reviewOnce("batch", async () => {
      const items = list.map((c) => keep.get(c.id) ? { id: c.id, expected_revision: c.revision, action: "approve" }
        : { id: c.id, expected_revision: c.revision, action: "reject", reason: "Arkism 앱 묶음 검토에서 버림" });
      const r = await appApi.request("/v1/memory_candidates/review_batch", { method: "POST", body: JSON.stringify({ scope: state.scope, items }) });
      const failed = (r.items || []).filter((x) => x.status >= 300).length;
      await memoryList.reloadMemories();
      notice(failed ? `${items.length - failed}개 반영, ${failed}개는 다른 곳에서 바뀌어 건너뛰었어요.` : `${items.length}개를 반영했어요.`);
    });
    const foot = el("div", "actions");
    foot.append(button("하나씩 보기", "ghost", null, () => go("memories", "candidate")), button(`${list.length}개 한 번에 반영`, "primary", "check", apply));
    const rest = state.data.candidates.length - list.length;
    body.append(cards, el("p", "meta", rest > 0 ? `반영하면 다음 ${Math.min(rest, BATCH)}개가 이어서 나와요 · 불러온 남은 후보 ${rest}개` : collectionPaging.snapshot("candidates").cursor ? "다음 목록에도 후보가 있어요." : "이게 전부예요."), foot);
  }

  // 업데이트 가능: 서버 pulls each skill repository forward (git pull --ff-only), never merging.
  async function updateSkills(updates) {
    const repos = [...new Set(updates.map((u) => u.repo))];
    if (!await ask(`스킬 ${updates.length}개를 새 버전으로 받을까요?\n\n${repos.join("\n")}\n\n저장소를 앞으로만 당겨요(병합하지 않아요).`)) return;
    try {
      for (const repo of repos) {
        const r = await appApi.request("/v1/agent_tools/requests", { method: "POST", body: JSON.stringify({ scope: state.scope, agent: "shared", kind: "skill_update", name: repo, enabled: true }) });
        state.data.tools.requests = r.agent_tools?.requests || state.data.tools.requests;
      }
      notice("업데이트를 요청했어요. 1분 안에 받아져요.");
      toolViews.pollTools();
    } catch (e) { notice(`요청하지 못했어요 (${e.message}).`); }
  }
  // 먼저 확인 holds three rows; 기억 처리 멈춤 is a fourth row on top while it lasts (Figma 523:35767).
  const firstCount = (items) => (items[0]?.id === "stalled" ? 4 : 3);
  // The Home row for lists that failed while the memories loaded; each failed list is named once (#186).
  const LIST_FAILED = { candidates: "기억 후보 목록을 불러오지 못했어요", skills: "스킬 목록을 불러오지 못했어요", crew: "크루 목록을 불러오지 못했어요" };
  function failedListsItem() {
    const failed = Object.keys(state.listErrors);
    if (!failed.length) return null;
    return { id: "lists", n: failed.length, icon: "warning-circle", label: "불러오지 못한 목록", warn: true,
      title: failed.length === 1 ? LIST_FAILED[failed[0]] : `목록 ${failed.length}개를 불러오지 못했어요`, sub: state.listErrors[failed[0]].text || "연결을 확인하고 다시 시도해 주세요.",
      action: "다시 불러오기", run: () => loadAll(), preview: failed.map((name) => LIST_FAILED[name]) };
  }
  function todoItems() {
    const d = state.data;
    if (state.loadError) return []; // nothing loaded: Home shows the error instead of zeros and a false stall (#187)
    const suspect = d.candidates.filter((c) => (c.tags || []).includes("review:instruction_like"));
    const blocked = [];
    for (const sk of d.skills) {
      for (const [k] of shownAgents()) {
        if (sk.skill_compatibility?.[k] === "unsupported") blocked.push([sk, k]);
      }
    }
    const idleList = d.memories.filter(idle);
    const fresh = d.memories.filter((m) => Date.now() - new Date(m.created_at).getTime() < 3 * DAY);
    const sug = suggestions();
    const pairs = similarPairs();
    const unusedSkills = d.skillUse ? d.skills.filter((sk) => sk.status !== "deprecated" && !d.skillUse.get(sk.id)) : [];
    const first = sug.items[0], pair = pairs[0];
    const tidy = maintenanceShown(), tp = tidy[0];
    const updates = d.tools?.skill_updates || [];
    const ver = (v) => (/^\d/.test(v || "") ? `v${v}` : v || "");
    // the server total while more pages wait, not the first page (#188); once every candidate is loaded the rows are current
    // null while the total is unknown, so the badge waits ("…") instead of showing the first page as the total
    const reviewTotal = collectionPaging.snapshot("candidates").cursor ? state.candidateTotal ?? null : d.candidates.length;
    const items = [
      { id: "updates", n: updates.length, icon: "arrows-clockwise", label: "업데이트 가능", title: updates.slice(0, 2).map((u) => `${u.name} ${ver(u.to)}`).join(" · "),
        sub: "원본 저장소에 새 버전이 있어요", action: "업데이트", run: () => updateSkills(updates), list: updates,
        preview: updates.map((u) => `${u.name} ${ver(u.from)} → ${ver(u.to)}`) },
      { id: "maintenance", n: tidy.length, icon: "git-merge", label: "기억 정리 제안", title: tp ? maintenanceTitle(tp) : "",
        sub: tp ? `어젯밤 정리 · ${kindLabel(tp.keep.kind)} · 고르기 전엔 아무것도 바뀌지 않아요` : "", action: "살펴보기", detail: (body, i) => maintenanceDetail(body, tidy[i] || tp), list: tidy,
        preview: tidy.map((t) => t.keep.text) },
      { id: "suggest", n: sug.items.length, icon: "lightbulb", label: "새 스킬 제안", title: first ? `새 스킬 만들기: ${first.title || first.name}` : "", sub: first?.why || "",
        action: "초안 보기", detail: (body) => suggestDetail(body, first), preview: sug.items.map((x) => x.title || x.name) },
      { id: "similar", n: pairs.length, icon: "git-branch", label: "비슷한 스킬", title: pair ? `${pair.a.name} ↔ ${pair.b.name} ${Math.round(pair.score * 100)}%` : "",
        sub: "하나로 합치면 관리가 쉬워요", action: "비교하기", detail: (body, i) => similarDetail(body, pairs[i] || pair), preview: pairs.map((x) => `${x.a.name} ↔ ${x.b.name} ${Math.round(x.score * 100)}%`) },
      { id: "unused", n: unusedSkills.length, icon: "clock-counter-clockwise", label: "안 쓰는 스킬", title: `60일 동안 안 쓴 스킬 ${unusedSkills.length}개`,
        sub: "휴지통으로 옮기면 에이전트가 더 읽지 않아요", action: "휴지통으로", run: () => confirmBatch(unusedSkills, () => trashSkills(unusedSkills)), preview: unusedSkills.slice(0, 5).map((sk) => sk.name) },
      { id: "review", n: reviewTotal, icon: "tray", label: "기억 검토", title: reviewTotal === null ? `기억 후보 ${d.candidates.length}개 이상 검토` : `기억 후보 ${reviewTotal}개 검토`,
        sub: suspect.length ? `지시처럼 보이는 문장 ${suspect.length}개가 있어요` : "자동으로 저장하지 않은 후보예요", warn: suspect.length > 0,
        action: "한 번에 검토", detail: (body) => reviewBatchDetail(body), preview: d.candidates.slice(0, 5).map((c) => c.text) },
      { id: "conflicted", n: d.conflicted.length, icon: "warning-circle", label: "부딪히는 기억", title: `부딪히는 기억 ${d.conflicted.length}개`,
        sub: "기존 기억과 내용이 달라 어느 쪽이 맞는지 골라야 해요", warn: true, action: "고르기", go: ["memories", "conflicted"],
        preview: d.conflicted.slice(0, 5).map((c) => c.text) },
      { id: "blocked", n: blocked.length, icon: "plug", label: "빠진 에이전트", title: blocked.length ? `${blocked[0][0].name}를 ${agentName(blocked[0][1])}에서 못 써요` : "",
        sub: blocked.length > 1 ? `외 ${blocked.length - 1}곳이 막혀 있어요` : "칸을 눌러 추가하고 적용하면 바로 써요", action: blocked.length ? `${agentName(blocked[0][1])}에 추가` : "",
        run: () => { skillViews.stage(blocked[0][0].id, blocked[0][1], "allow"); go("skills", "missing"); }, go: ["skills", "missing"],
        agent: blocked.length ? [AGENTS.find(([k]) => k === blocked[0][1])?.[2] || "sym:i-plug", agentName(blocked[0][1])] : null,
        preview: blocked.slice(0, 5).map(([sk, k]) => `${sk.name} → ${agentName(k)}`) },
      { id: "idle", n: state.usageDone ? idleList.length : null, icon: "clock-counter-clockwise", label: "호출과 문구 검토", title: `검토할 기억 ${idleList.length}개`,
        sub: "재검토 예정일이 지났거나 최근 호출·문구 겹침이 적어요. 직접 확인해 주세요", action: "보기", go: ["memories", "idle"], preview: idleList.slice(0, 5).map((m) => m.text) },
      { id: "fresh", n: fresh.length, icon: "sparkle", label: "새 기억", title: `최근 3일 새 기억 ${fresh.length}개`, sub: "맞는 내용인지 훑어보세요",
        action: "훑어보기", go: ["memories", "active"], preview: fresh.slice(0, 5).map((m) => m.text) }
    ];
    const ORDER = ["lists", "stalled", "suggest", "review", "blocked", "updates", "conflicted", "maintenance", "similar", "unused", "idle", "fresh"];
    const stalled = homeChecks.stalledItem(state.connection.generation);
    if (stalled) items.push(stalled);
    const lists = failedListsItem();
    if (lists) items.push(lists);
    return items.filter((t) => t.n === null || t.n > 0).sort((a, b) => ORDER.indexOf(a.id) - ORDER.indexOf(b.id));
  }
  function updateCounts() {
    const d = state.data;
    const todo = todoItems().filter((t) => t.n !== 0); // a row whose count is still unknown (null) counts too
    const c = $("#count-todo");
    c.textContent = state.loading ? "" : String(todo.length);
    c.className = todo.some((t) => t.warn) ? "warn" : "";
    $("#count-memories").textContent = state.loading ? "" : memoryPaging.memoryCount();
    $("#count-skills").textContent = state.loading ? "" : state.listErrors.skills || state.loadError ? "—" : `${d.skills.length}${collectionPaging.snapshot("skills").cursor ? "+" : ""}`;
    const promptsOf = (kind) => d.prompts.filter((p) => p.image_prompt.kind === kind).length;
    $("#count-image").textContent = state.loading ? "" : String(promptsOf("image"));
    $("#count-slide").textContent = state.loading ? "" : String(promptsOf("slide"));
    $("#count-design").textContent = state.loading ? "" : String(d.designSystems?.length || 0);
    $("#count-crew").textContent = state.loading ? "" : String(crew.count());
    $("#count-routing").textContent = state.loading ? "" : String(routing.count());
    $("#count-usage").textContent = state.loading ? "" : String(usage.count());
    const t = d.tools;
    const toolCount = $("#count-tools");
    toolCount.textContent = state.loading || !t ? "" : String(t.servers.length + t.plugins.length);
    toolCount.className = t && (t.servers.some((x) => x.note) || toolViews.pendingTool(t)) ? "warn" : "";
    syncExtraAgents();
    shell?.sync();
  }
  // Every screen redraw is timed (설정 → 고급 → 속도 기록); size is how many rows or cards it drew.
  function render(resetDetail = true) {
    const t0 = performance.now(), restoreFocus = retainListFocus(document);
    try { renderNow(resetDetail); restoreFocus(); } finally { window.ArkismPerf?.note(`render.${state.view}`, performance.now() - t0, $("#table").childElementCount); }
  }
  function renderNow(resetDetail) {
    virtualList.clear();
    if (state.view !== "todo") state.start = 0; // 시작 2·3단계 lives on 홈 only
    if (state.view !== "routing") routing.leave();
    if (state.view !== "settings" || state.settingsTab !== "routing") routing.leaveSettings(); // 모델 설정 changes show up next time
    if (state.view !== "memories" || state.memTab !== "originals") originals.reset();
    if (state.view !== "knowledge") knowledge.reset();
    if (state.view !== "memories" || state.memTab !== "past") memoryTime.reset();
    if (state.view !== "memories" || state.memTab !== "reflections") reflections.reset();
    if (state.view !== "memories" || state.memTab !== "traits") traits.reset();
    if (state.view !== "memories" || state.memTab !== "relations") relations.reset();
    if (state.view !== "memories" || state.memTab !== "digests") digests.reset();
    updateCounts();
    renderStatusCard();
    const v = views[state.view];
    $("#title").textContent = typeof v.title === "function" ? v.title() : v.title;
    $("#title").classList.toggle("sr-only", $("#title").textContent === $("#area-tabs").dataset.active); // the tab already names it; screen readers keep the heading
    $("#title-count").textContent = typeof v.count === "function" && !state.loading && !$("#title").classList.contains("sr-only") ? v.count() : ""; // a hidden title takes its count along; the tab badge shows it
    const bare = !state.loading && (typeof v.bare === "function" ? v.bare() : !!v.bare); // the rail and tabs already name the screen
    $(".main > .page-head").classList.toggle("bare", bare);
    $(".main > .page-head").style.display = bare ? "none" : ""; // shown again below if the screen puts buttons in it
    $(".main > .page-head .page-back")?.remove(); // a screen that has a ← link draws it again
    $(".page-head .head-back")?.remove(); $(".page-head .head-aside")?.remove(); // TOOLS screens add them again via headExtras()
    $("#app").classList.toggle("v4-connect", ["tools", "reviews"].includes(state.view));
    const table = $("#table");
    if (state.loading) {
      ["#controls", "#chips", "#stats"].forEach((s) => $(s).replaceChildren());
      table.replaceChildren(...Array.from({ length: 6 }, () => el("div", "skeleton")));
      $("#detail").replaceChildren();
      return;
    }
    if (state.view === "skills" && !state.versionsDone) void loadVersions(state.connection.generation);
    const scroll = table.scrollTop;
    const page = pages.views[state.view] || crew.pages[state.view];
    $("#app").classList.toggle("wide", (!!page && !page.keepDetail) || popupDetail()); // TOOLS lists take the full width; an item opens in a popup
    if (!popupDetail()) itemPop?.dialog.close();
    $("#app").classList.toggle("keep-detail", !!page && !!page.keepDetail); // narrow windows: this detail sits under the list, not in a drawer
    $("#app").classList.toggle("graph-mode", state.view === "graph");
    $("#app").classList.toggle("memory-mode", state.view === "memories");
    $("#app").classList.toggle("knowledge-mode", state.view === "knowledge");
    $("#app").classList.toggle("skills-mode", state.view === "skills");
    $("#app").classList.toggle("gallery-mode", ["images", "design", "tokens"].includes(state.view));
    $("#app").classList.toggle("home-mode", state.view === "todo");
    $("#app").classList.toggle("overview-mode", state.view === "overview");
    table.className = { todo: "todo-wrap", images: "card-grid", crew: "card-grid crew-grid", settings: "page-wrap", design: "page-wrap", tokens: "page-wrap", graph: "page-wrap" }[state.view] || "table";
    renderHeadActions();
    // a bare screen drops its title row, but keeps the row while it carries buttons (팀 › 회의 보기, 사용량 › 새로고침)
    $(".main > .page-head").style.display = bare && !$("#head-actions").childElementCount ? "none" : "";
    skillViews.renderChangeBar();
    if (page) {
      ["#chips", "#stats", "#controls"].forEach((q) => $(q).replaceChildren()); // a page that wants controls draws them in render()
      page.render();
      if (state.view === "overview") appendLoadedNote(table);
      table.scrollTop = scroll;
      return;
    }
    if (state.view === "todo") { renderTodo(); appendLoadedNote(table); }
    if (state.view === "memories") memoryList.renderMemories();
    if (state.view === "skills") skillViews.renderSkills();
    if (state.view === "images") imageViews.renderImages();
    if (state.view === "crew") crew.render();
    if (state.view === "design") designViews.renderLibrary();
    if (state.view === "tools") toolViews.renderTools();
    if (state.view === "reviews") reviews.render();
    table.scrollTop = scroll;
    if (resetDetail || !state.selected) renderDetail();
  }
  function appendLoadedNote(table) {
    if (similarity.busy) table.append(el("p", "meta memory-page-note", "비슷한 스킬을 확인하고 있어요."));
  }
  // Title line extras (Figma v4): a "← back" link above the title, the count after it, buttons at the right of the title row.
  function headExtras({ count = "", back = null, aside = [] } = {}) {
    $("#title-count").textContent = $("#title").classList.contains("sr-only") ? "" : count; // same rule as render(): a hidden title takes its count along (#183)
    const head = $(".page-head");
    head.querySelector(".head-back")?.remove();
    head.querySelector(".head-aside")?.remove();
    if (back) {
      const b = el("button", "head-back", `← ${back[0]}`); b.type = "button"; b.addEventListener("click", back[1]);
      head.prepend(b);
    }
    if (aside.length) { const a = el("div", "head-aside"); a.append(...aside); head.append(a); }
  }
  function renderHeadActions() {
    const box = $("#head-actions");
    const refresh = button("새로고침", "", "arrows-clockwise", loadAll);
    const pageView = pages.views[state.view] || crew.pages[state.view];
    if (pageView?.headInTitle) { // Figma v4 (198:2350 · 325:37545): the buttons sit at the right of the title row, not in the tab bar
      box.replaceChildren();
      headExtras({ count: $("#title-count").textContent, aside: pageView.head() });
    } else if (pageView) {
      box.replaceChildren(...pageView.head());
    } else if (state.view === "crew") {
      box.replaceChildren(...crew.head());
    } else if (state.view === "memories" && ["past", "reflections", "traits", "relations", "candidate", "provenance", "conflicted", "idle", "digests", "merge"].includes(state.memTab)) {
      box.replaceChildren();
    } else if (state.view === "memories" && state.memTab === "originals") {
      box.replaceChildren(button("다시 불러오기", "", "arrows-clockwise", originals.reload));
    } else if (state.view === "memories") {
      box.replaceChildren(button("기억 추가", "primary", null, memoryDetail.openMemoryAdd));
    } else if (state.view === "todo" && state.start) {
      box.replaceChildren();
    } else if (state.view === "todo") {
      const note = el("span", "head-note");
      const at = suggestions().at;
      const again = el("button", "link-btn", analyzing ? "분석 중…" : "다시 분석"); again.type = "button"; again.disabled = analyzing; again.addEventListener("click", analyze);
      const reload = el("button", "link-btn", "다시 불러오기"); reload.type = "button"; reload.addEventListener("click", loadAll);
      note.append(icon("clock-counter-clockwise"), at ? `${ago(at)} 분석 ·` : "아직 분석 안 함 ·", again, "·", reload);
      box.replaceChildren(note);
    } else if (state.view === "images") {
      box.replaceChildren(state.promptRun ? button("이 프롬프트로 만들기", "primary", null, (e) => imageViews.runPrompt(e.currentTarget)) : button("프롬프트 추가", "primary", null, () => imageViews.openPromptEditor(null)));
    } else if (state.view === "design") {
      box.replaceChildren(button("Arkism 토큰", "ghost", null, () => go("tokens")), button("디자인 시스템 추가", "primary", null, () => designViews.openSystemEditor(null)));
    } else if (state.view === "tools" || state.view === "skills") {
      box.replaceChildren(); // 새로고침 · 설치 sit in the list's own toolbar (barActions)
    } else if (state.view === "reviews") {
      box.replaceChildren(...reviews.head());
    } else box.replaceChildren(refresh);
  }
  // The end of the 스킬 · 플러그인 · MCP toolbar: reload, and 설치 — the one way in to every install flow.
  function barActions(reload) {
    return [button("새로고침", "", "arrows-clockwise", reload), button("설치", "", "plus", openInstall)];
  }
  // 설치: every way the app can add a skill, an MCP server or a plugin; the current tab's kind comes first.
  function openInstall() {
    const groups = [
      ["skills", "스킬", [["GitHub에서 가져오기", "저장소 주소를 넣으면 스킬을 미리 보고 고른 곳에 설치해요.", () => { if (state.view !== "skills") go("skills"); skillViews.openSkillPage({ kind: "install" }); }],
        ["AI에게 새 스킬 제안 받기", "최근 기억에서 반복한 요청을 찾아 스킬 초안을 홈의 할 일에 올려요.", analyze]]],
      ["mcp", "MCP 서버", [["MCP 서버 추가", "실행할 프로그램이나 https 주소로 추가해요. 자주 쓰는 서버를 골라 채울 수도 있어요.", toolViews.openAddServer]]],
      ["plugin", "플러그인", [["Claude Code에서 설치", "새 플러그인은 Claude Code에서 /plugin 으로 설치해요.", null]]]];
    const first = state.view === "skills" ? "skills" : state.toolTab === "plugin" ? "plugin" : "mcp";
    groups.sort((a, b) => (b[0] === first) - (a[0] === first));
    const pop = window.ArkismCrewPopup({ el, button }).open({ title: "설치", tabs: [{ key: "pick", label: "설치", render: () => {} }], cls: "install-pop" });
    const list = el("div", "install-list");
    for (const [, title, ways] of groups) {
      const sec = el("section", "install-group");
      sec.append(el("h3", "", title));
      for (const [label, text, run] of ways) {
        const row = el(run ? "button" : "div", "install-way");
        if (run) { row.type = "button"; row.addEventListener("click", () => { pop.dialog.close(); run(); }); }
        row.append(el("strong", "", label), el("span", "", text));
        sec.append(row);
      }
      list.append(sec);
    }
    pop.body.replaceChildren(list);
  }
  function go(view, tab) {
    state.view = view;
    if (view === "todo") state.todoAll = false;
    state.skillPage = null;
    if (view === "usage") usage.leave();
    if (view === "memories" && tab) state.memTab = tab;
    if (view === "skills" && tab) state.skillTab = tab;
    if (view === "images" && tab) state.imageTab = tab;
    if (view === "images") state.promptRun = null;
    gallery.reset();
    if (view === "crew" && tab) { state.crewTab = tab; state.crewCat = "all"; }
    state.kind = "all";
    state.repo = "";
    state.selected = null;
    notice("");
    $("#table").scrollTop = 0;
    render();
    void syncVisible({ type: "navigation" });
  }
  function segment(tabs, current, onPick) {
    const s = el("div", "segment");
    s.setAttribute("role", "tablist");
    for (const [key, label, n, warn] of tabs) {
      const t = el("button", `tab${key === current ? " active" : ""}`);
      t.type = "button";
      t.setAttribute("role", "tab");
      t.setAttribute("aria-selected", String(key === current));
      t.append(label);
      if (n !== undefined) t.append(el("output", warn && n ? "warn" : "", n === null ? "…" : String(n)));
      t.addEventListener("click", () => onPick(key));
      s.append(t);
    }
    return s;
  }
  function stat(label, value, unit, warn) {
    const s = el("div", "stat");
    const strong = el("strong", warn ? "warn" : "", value);
    s.append(el("p", "", label), strong, el("span", "", unit));
    return s;
  }
  function row(id, template, cells, onClick) {
    const r = el("button", `row${state.selected === id ? " selected" : ""}`);
    r.type = "button";
    r.dataset.id = id;
    r.style.gridTemplateColumns = template;
    r.append(...cells);
    r.addEventListener("click", () => { catalogQuery.pin(id); state.selected = id; markSelected(); onClick(); openDetail(); });
    return r;
  }
  function markSelected() {
    document.querySelectorAll(".row[data-id], .cn-row[data-id], .todo-item[data-id], .prompt-card[data-id], .ds-card[data-id], .crew-card[data-id]").forEach((r) => r.classList.toggle("selected", r.dataset.id === state.selected));
  }

  // ---------------------------------------------------------------- 할 일 (Figma v4 · 01 홈: 198:200, 198:2178, 198:2264, 198:3812, 210:17783)
  // 홈 = 맨 위 그림 + 먼저 확인 3가지 + 메뉴별 확인할 것 + 이번 주. 줄을 고르면 그 일의 화면(← 홈)으로, 전체 항목 보기는 표 하나로.
  const TODO_SHORT = { review: "기억 후보", conflicted: "부딪힘", maintenance: "정리 제안", idle: "호출과 문구 검토", fresh: "새로 저장",
    suggest: "새 스킬", similar: "비슷함", unused: "안 씀", updates: "업데이트", blocked: "빠진 에이전트" };
  const TODO_MENUS = [["MEMORY", "기억", ["fresh", "review", "conflicted", "maintenance", "idle"], ["memories", "active"]],
    ["TOOLS", "스킬", ["suggest", "similar", "unused", "updates", "blocked"], ["skills"]]];
  const todoKey = (id) => (id || "").replace(/^todo:/, "").split("#")[0];
  const todoIndex = (id) => Number((id || "").split("#")[1] || 0);
  const todoTotal = (items) => items.reduce((s, t) => s + (t.n || 0), 0);
  // The page a selection opens: 새 스킬 제안 stays on 홈 (Figma 210:17783); the others get their own title and ← 홈.
  function homePage() {
    if (state.start) return { kind: "start" };
    if (state.todoAll) return { kind: "all" };
    const t = state.selected?.startsWith("todo:") ? todoItems().find((x) => x.id === todoKey(state.selected)) : null;
    if (!t) return { kind: "home" };
    return { kind: t.id === "suggest" ? "home" : "item", t };
  }
  function todoTitle() {
    const p = homePage();
    if (p.kind === "start") return home.startTitle();
    if (state.loadError && p.kind === "home") return state.loadError.title;
    if (p.kind === "all") return "전체 항목";
    if (p.kind === "item") return { similar: "비슷한 스킬 비교" }[p.t.id] || p.t.label;
    return "오늘 할 일";
  }
  function todoSub() {
    const p = homePage(), items = todoItems();
    if (p.kind === "start") return home.startSub();
    if (state.loadError && p.kind === "home") return "앱은 계속 쓸 수 있어요. 다시 연결되면 최신 내용을 불러와요.";
    if (p.kind === "all") return "할 일을 모두 펼쳤어요. 줄을 누르면 그 화면으로 가요.";
    if (p.kind === "item" && p.t.id === "maintenance") return "어젯밤 기억을 살펴보고 정리할 것을 골랐어요. 고르기 전엔 아무것도 바뀌지 않아요.";
    if (p.kind === "item" && p.t.id === "similar") {
      const pair = similarPairs()[todoIndex(state.selected)] || similarPairs()[0];
      return pair ? `${pair.a.name}와 ${pair.b.name}가 ${Math.round(pair.score * 100)}% 비슷해요. 하나로 합칠지 골라 주세요.` : "";
    }
    if (p.kind === "item") return p.t.sub;
    if (!items.length) return "지금은 확인할 일이 없어요.";
    return `먼저 확인할 ${Math.min(3, items.length)}가지와 메뉴별로 쌓인 일을 모았어요.`;
  }
  function todoCount() {
    const p = homePage(), items = todoItems();
    if (p.kind === "all") return `${items.length}가지 · ${todoTotal(items)}개`;
    if (p.kind === "item" && p.t.n > 1) return `${p.t.n}개`;
    return "";
  }
  function pickTodo(id) { state.selected = id; render(); openDetail(); }
  function homeBack() { state.selected = null; state.todoAll = false; $("#app").classList.remove("detail-open"); render(); }

  // Parts of the shared frame only 홈 uses: the picture on top, ← 홈, and 전체 항목 보기 on the title row.
  function homeFrame(page) {
    const head = $(".main > .page-head");
    let hero = $("#home-hero");
    if (!hero) {
      hero = el("section", "home-hero");
      hero.id = "home-hero";
      hero.setAttribute("aria-hidden", "true");
      const words = el("div", "home-hero-words");
      const big = el("p", "home-hero-title");
      big.append(el("span", "", "Every thought,"), el("span", "", "a new beginning."));
      words.append(el("p", "home-hero-kicker", "THE ART OF REMEMBERING"), big);
      const art = el("div", "home-hero-art");
      const img = el("img"); img.src = "img/arkism-art-hero.svg"; img.alt = ""; img.width = 310; img.height = 187;
      const cap = el("div", "home-hero-cap");
      cap.append(el("span", "fig", "FIG. 01"), el("span", "", "TRIANGLES OF THOUGHT"));
      art.append(img, cap);
      hero.append(words, art);
      head.before(hero, el("div", "home-hero-line"));
      const back = el("button", "home-back", "← 홈");
      back.type = "button"; back.id = "home-back";
      back.addEventListener("click", homeBack);
      head.prepend(back);
      const all = button("전체 항목 보기", "home-all");
      all.id = "home-all";
      all.addEventListener("click", () => { state.todoAll = true; state.selected = null; $("#app").classList.remove("detail-open"); render(); });
      head.append(all);
    }
    $("#app").classList.toggle("home-sub", page.kind !== "home");
    $("#app").classList.toggle("home-start", page.kind === "start");
  }

  function renderTodo() {
    ["#controls", "#chips", "#stats"].forEach((sel) => $(sel).replaceChildren());
    const page = homePage();
    homeFrame(page);
    const items = todoItems();
    const wrap = $("#table");
    if (page.kind === "start") { wrap.replaceChildren(home.startMain()); return; }
    const lost = page.kind === "home" || page.kind === "all" ? home.lostCard() : null; // 전체 항목 too: a failed load is not "nothing to do"
    if (lost && !items.length) { wrap.replaceChildren(lost); return; }
    if (!items.length) { wrap.replaceChildren(homeEmpty()); return; }
    if (page.kind === "all") { wrap.replaceChildren(todoTable(items)); return; }
    const now = items.slice(0, firstCount(items)), rest = items.slice(firstCount(items));
    const parts = [...(lost ? [lost] : []), homeHead("먼저 확인", el("output", "", String(now.length))), firstList(now)];
    if (!state.selected) {
      const menus = menuRows(rest);
      if (menus) parts.push(homeHead("메뉴별 확인할 것", null, "누르면 그 메뉴로 가요"), menus);
      const week = weekCards();
      if (week) parts.push(homeHead("이번 주", null, "누르면 사용량으로 가요"), week);
    } else if (rest.length) {
      const fold = el("button", "link-btn home-fold", state.todoFolded ? "펼쳐서 보기" : "접기");
      fold.type = "button";
      fold.addEventListener("click", () => { state.todoFolded = !state.todoFolded; render(false); });
      const h = homeHead("나머지", el("output", "", String(rest.length)));
      h.append(fold);
      parts.push(h);
      if (!state.todoFolded) parts.push(restList(rest));
      parts.push(el("p", "home-note", "묶음 안의 \"모두 …\" 작업은 실행 전에 대상 목록을 보여 주고, 실행 뒤 되돌릴 수 있어요."));
    }
    wrap.replaceChildren(...parts);
  }
  function homeEmpty() {
    return window.ArkismState.panel({ title: "지금은 확인할 일이 없어요", text: "새 기억 후보나 스킬 제안이 생기면 여기에 모여요.",
      actions: [["기억 보기", () => go("memories", "active")], ["다시 불러오기", loadAll, "ghost"]] });
  }
  function homeHead(text, count, aside) {
    const h = el("h2", "home-head", text);
    if (count) h.append(count);
    if (aside) h.append(el("span", "home-aside", aside));
    return h;
  }
  function todoAct(t, id) {
    return button(t.action, "small", null, (e) => {
      e.stopPropagation();
      if (t.run) return t.run();
      if (t.detail) return pickTodo(id);
      go(...t.go);
    });
  }
  // 먼저 확인: numbered rows; 기억 정리 제안 opens into one row per proposal under a small group line (Figma 198:2264).
  function firstList(now) {
    const list = el("div", "todo-list home-first");
    let n = 0;
    const row = (t, id, title, sub, warn) => {
      const on = state.selected === id;
      const item = el("div", `todo-item${on ? " selected" : ""}${t.id === "stalled" ? " stalled" : ""}`);
      item.dataset.id = id;
      const cell = el("div", "cell");
      const meta = el("div", `sub${warn ? " warn-text" : ""}`);
      if (t.id === "blocked" && t.agent) meta.append(agentLogo(...t.agent));
      meta.append(sub || "");
      cell.append(raw(el("div", "text", tr(title))), meta);
      item.append(el("span", "todo-num", t.id === "stalled" ? (++n, "!") : String(++n)), cell, todoAct(t, id));
      item.addEventListener("click", () => (t.id === "stalled" ? go(...t.go) : pickTodo(id)));
      list.append(item);
    };
    for (const t of now) {
      if (t.id === "maintenance" && t.list?.length > 1) {
        const g = el("div", "home-group");
        g.append(el("strong", "", t.label), el("output", "", String(t.list.length)), el("span", "", "어젯밤 살펴봤어요 · 고르기 전엔 아무것도 바뀌지 않아요"));
        list.append(g);
        t.list.forEach((x, i) => row(t, `todo:maintenance#${i}`, maintenanceTitle(x), `어젯밤 정리 · ${kindLabel(x.keep.kind)}`));
      } else row(t, `todo:${t.id}`, t.title, t.sub, t.warn);
    }
    return list;
  }
  function restList(rest) {
    const list = el("div", "todo-list todo-rest");
    for (const t of rest) {
      const id = `todo:${t.id}`;
      const item = el("div", `todo-item${todoKey(state.selected) === t.id ? " selected" : ""}`);
      item.dataset.id = id;
      item.append(el("span", "label", t.label), raw(el("span", "sub", t.preview[0] || tr(t.sub))), el("output", `home-badge${t.warn ? " warn" : ""}`, t.n === null ? "…" : String(t.n)), el("span", "home-chev", "›"));
      item.addEventListener("click", () => { if (t.detail || !t.go) pickTodo(id); else go(...t.go); });
      list.append(item);
    }
    return list;
  }
  // 메뉴별 확인할 것: what is left after 먼저 확인, one row per menu; the row opens that menu.
  function menuRows(rest) {
    const list = el("div", "todo-list home-menus");
    const of = (m) => TODO_MENUS.filter(([menu]) => menu === m).map(([menu, name, ids, target]) => [menu, name, rest.filter((t) => ids.includes(t.id) && t.n !== 0), target]);
    const rows = of("MEMORY");
    digests.menuRows(rows); // 병합 제안 · 모아보기 (digests.js)
    rows.push(homeChecks.knowledgeRow(state.connection.generation), ...of("TOOLS"), ...home.menuExtras(state.connection.generation)); // 지식 · 스킬 (TOOLS) · 워크플로우 · 라우팅
    const tools = state.data.tools;
    if (tools) {
      const login = tools.servers.filter((s) => Object.values(s.login || {}).includes("needed"));
      const noted = tools.servers.filter((s) => s.note);
      const parts = [...login.map((s) => ({ label: `${s.name} 로그인 필요`, n: 1 })), ...noted.map((s) => ({ label: `${s.name} 설정 확인`, n: 1 }))];
      if (toolViews.pendingTool(tools)) parts.push({ label: "적용 대기", n: tools.requests.filter((q) => q.status === "pending").length });
      rows.push(["TOOLS", "MCP", parts, ["tools"]]);
    }
    let any = false;
    for (const [menu, name, parts, target] of rows) {
      const n = todoTotal(parts);
      if (!parts.some((t) => t.n === null || t.n > 0)) continue; // an unknown count keeps its row
      any = true;
      const item = el("div", "todo-item");
      const text = parts.map((t) => (t.id ? `${TODO_SHORT[t.id] || t.label} ${t.n ?? "…"}` : t.label)).join(" · ");
      item.append(el("span", "home-menu", menu), el("span", "label", name), raw(el("span", "sub", tr(text))), el("output", "home-badge warn", parts.some((t) => t.n === null) ? (n ? `${n}+` : "…") : String(n)), el("span", "home-chev", "›"));
      item.addEventListener("click", () => go(...target));
      list.append(item);
    }
    return any ? list : null;
  }
  // 이번 주: the server's usage summary for 7 days (GET /v1/usage/summary); hidden when the server has none.
  let week = { g: -1, doc: undefined };
  function weekCards() {
    if (week.g !== state.connection.generation) {
      week = { g: state.connection.generation, doc: undefined };
      const g = state.connection.generation;
      void appApi.request(`/v1/usage/summary?${appApi.query({ range: "7d" })}`).then((d) => (d?.kpi ? d : null), () => null)
        .then((doc) => { if (week.g !== g) return; week.doc = doc; if (state.view === "todo" && !state.selected) render(false); });
    }
    const k = week.doc?.kpi;
    if (!k) return null;
    const f = usage.fmt;
    // Server totals when known; the loaded rows only when they are every memory (older servers).
    const t = state.memoryTotals, mem = t?.active ?? state.data.memories.length;
    const idleN = t ? t.idle : !memoryPaging.partialMemories() && state.usageDone ? state.data.memories.filter(idleFromUsage).length : null;
    const used = mem && idleN != null ? `${Math.round(((mem - idleN) / mem) * 100)}%` : mem === 0 ? "—" : "…";
    const box = el("div", "home-week");
    for (const [label, value, sub] of [["이번 주 세션", f.num(k.sessions), "최근 7일"], ["이번 주 토큰", f.num(k.tokens), `캐시 적중 ${k.cache_hit_pct}%`],
      ["이번 주 비용", f.usd(k.cost_usd, k.priced), "공시 단가로 계산"], ["기억 사용", used, "최근 30일 답변에 쓰인 기억"]]) {
      const c = el("button", "home-stat");
      c.type = "button";
      c.append(el("span", "", label), el("strong", "", value), el("small", "", sub));
      c.addEventListener("click", () => go("usage"));
      box.append(c);
    }
    return box;
  }
  // 전체 항목 (Figma 198:3812): every kind in one table — 항목 · 개수 · 다음 할 일.
  function todoTable(items) {
    const box = el("div", "todo-list home-table");
    const h = el("div", "home-thead");
    h.append(el("span", "", "항목"), el("span", "", "개수"), el("span", "", "다음 할 일"));
    box.append(h);
    for (const t of items) {
      const id = `todo:${t.id}`;
      const item = el("div", `todo-item${todoKey(state.selected) === t.id ? " selected" : ""}`);
      item.dataset.id = id;
      const count = el("span", "home-count");
      count.append(el("output", `home-badge${t.warn ? " warn" : ""}`, t.n === null ? "…" : String(t.n)));
      item.append(el("span", "label", t.label), count, el("span", "sub", t.action || "보기"), el("span", "home-chev", "›"));
      item.addEventListener("click", () => { state.selected = id; markSelected(); renderDetail(); openDetail(); });
      box.append(item);
    }
    const wrapper = el("div", "home-table-wrap");
    wrapper.append(box, el("p", "home-note", "묶음 안의 \"모두 …\" 작업은 실행 전에 대상 목록을 보여 주고, 실행 뒤 되돌릴 수 있어요."));
    return wrapper;
  }
  // The right column on 홈 when nothing is picked (Figma 198:200 Detail).
  function homeSummary() {
    const body = detailShell("오늘 요약");
    const items = todoItems(), now = items.slice(0, firstCount(items)), rest = items.slice(firstCount(items));
    const head = el("div", "home-sum-head");
    head.append(el("h2", "", state.loadError ? "확인할 일을 불러오지 못했어요" : now.length ? `확인할 일 ${now.length}개가 먼저 기다려요` : "지금은 확인할 일이 없어요"),
      el("p", "meta", state.loadError ? "" : rest.length ? `나머지 ${todoTotal(rest)}개는 메뉴별로 모아 두었어요.` : "새 일이 생기면 여기에 모여요."));
    const agents = shownAgents();
    const nums = el("div", "home-nums");
    for (const [label, value, warn] of [["먼저 확인", now.length, true], ["메뉴별 확인할 것", todoTotal(rest)], ["함께 일하는 에이전트", agents.length]]) {
      const r = el("div", "");
      const known = !state.loadError || label === "함께 일하는 에이전트"; // the to-do counts have nothing to count after a failed load
      r.append(el("span", "", label), el("output", known && warn && value ? "warn" : "", known ? String(value) : "—"));
      nums.append(r);
    }
    const team = el("section", "section home-agents");
    team.append(el("h3", "", "함께 일하는 에이전트"));
    const blockedBy = new Map();
    for (const sk of state.data.skills) for (const [k] of agents) if (sk.skill_compatibility?.[k] === "unsupported") blockedBy.set(k, [...(blockedBy.get(k) || []), sk.name]);
    for (const [k, name, spec] of agents) {
      const r = el("div", "home-agent");
      const [cls, label] = agentHealth(k);
      const miss = blockedBy.get(k);
      const side = miss ? `${miss[0]} 빠짐${miss.length > 1 ? ` 외 ${miss.length - 1}개` : ""}` : cls === "ok" ? "기억 · 스킬 연결" : label;
      r.append(agentLogo(spec, name), el("span", "", name), el("small", miss || cls === "warn" || cls === "bad" ? "warn" : "", side));
      team.append(r);
    }
    const links = el("section", "section home-links");
    links.append(el("h3", "", "바로가기"));
    for (const [label, view, tab] of [["기억 보기", "memories", "active"], ["스킬 보기", "skills"], ["라우팅 현황", "routing"], ["사용량", "usage"]]) {
      const b = el("button", "home-link");
      b.type = "button";
      b.append(el("span", "", label), el("span", "home-chev", "›"));
      b.addEventListener("click", () => go(view, tab));
      links.append(b);
    }
    body.append(head, ...(state.loadError && !state.loadError.reached ? [home.lostSide()] : []), nums, team, links, el("p", "meta", "각 줄을 누르면 해당 목록으로 가요. 승인·막기는 실행 전에 무엇이 바뀌는지 보여 줘요."));
  }
  function todoDetail(t) {
    const i = todoIndex(state.selected);
    const label = t.id === "maintenance" && t.n > 1 ? `${t.label} · ${i + 1}/${t.n}` : t.label;
    const body = detailShell(label);
    if (t.detail) return t.detail(body, i);
    const head = el("div", "home-sum-head");
    head.append(raw(el("h2", "", t.title)), el("p", "meta", t.sub));
    body.append(head);
    if (t.id === "updates") {
      for (const u of t.list || []) {
        const v = el("div", "home-version");
        v.append(el("code", "", u.name), el("span", "dim", "지금"), el("span", "mono", ver(u.from)), el("span", "dim", "→"), el("span", "dim", "새 버전"), el("span", "mono accent", ver(u.to)));
        body.append(v);
      }
    } else if (t.preview.length) {
      const s = section("미리 보기");
      const ul = el("ul", "preview");
      t.preview.forEach((p) => ul.append(raw(el("li", "", p))));
      s.append(ul);
      body.append(s);
    }
    const act = el("div", "actions");
    if (t.go && t.run) act.append(button("목록 보기", "ghost", null, () => go(...t.go)));
    else act.append(button("취소", "", null, closeDetail));
    act.append(button(t.id === "updates" ? "업데이트 받기" : t.action, "primary", null, t.run || (() => go(...t.go))));
    body.append(act);
  }
  const ver = (v) => (/^\d/.test(v || "") ? `v${v}` : v || "");
  const memoryList = createMemoryList({
    get memory() { return state.screens.memory; },
    get $() { return $; },
    get ago() { return ago; },
    get approvalUnproven() { return approvalUnproven; },
    get button() { return button; },
    get byRisk() { return byRisk; },
    get currentMemoryPages() { return memoryPaging.currentMemoryPages; },
    get DAY() { return DAY; },
    get digests() { return digests; },
    get el() { return el; },
    get ensureMemoryResults() { return memoryPaging.ensureMemoryResults; },
    get failNotice() { return failNotice; },
    get fetchMoreMemories() { return memoryPaging.fetchMoreMemories; },
    get filteredMemories() { return memoryPaging.filteredMemories; },
    get generation() { return state.connection.generation; },
    set generation(value) { state.connection.generation = value; },
    get go() { return go; },
    get hydrateMaintenance() { return memoryPaging.hydrateMaintenance; },
    get loadMemoryTotals() { return loadMemoryTotals; },
    get icon() { return icon; },
    set icon(value) { icon = value; },
    get idle() { return idle; },
    get inject() { return inject; },
    get kindLabel() { return kindLabel; },
    get KINDS() { return KINDS; },
    get collectionPaging() { return collectionPaging; },
    get catalogQuery() { return catalogQuery; },
    get virtualList() { return virtualList; },
    get loadMaintenance() { return loadMaintenance; },
    get markSelected() { return markSelected; },
    get matches() { return matches; },
    get memoryCount() { return memoryPaging.memoryCount; },
    get memoryKey() { return memoryPaging.memoryKey; },
    get memoryPages() { return memoryPaging.memoryPages; },
    get memoryResults() { return memoryPaging.memoryResults; },
    get memoryTime() { return memoryTime; },
    get memRepo() { return memRepo; },
    get memStat() { return memStat; },
    get memUse() { return memUse; },
    get notice() { return notice; },
    get openDetail() { return openDetail; },
    get originals() { return originals; },
    get originOf() { return originOf; },
    get overlapReview() { return overlapReview; },
    get query() { return appApi.query; },
    get raw() { return raw; },
    get reflections() { return reflections; },
    get relations() { return relations; },
    get render() { return render; },
    get renderDetail() { return renderDetail; },
    get request() { return appApi.request; },
    get reviewOverdue() { return reviewOverdue; },
    get riskLabel() { return riskLabel; },
    get row() { return row; },
    get savedHow() { return savedHow; },
    get segment() { return segment; },
    get standing() { return standing; },
    get state() { return state; },
    get stateNotice() { return stateNotice; },
    get traits() { return traits; },
    get usageOf() { return usageOf; },
    get waitReason() { return waitReason; },
    get withAgent() { return withAgent; }
  });

  const skillViews = createSkillViews({
    get $() { return $; },
    get barActions() { return barActions; },
    get segment() { return segment; },
    get loadAll() { return loadAll; },
    get agentLogo() { return agentLogo; },
    get agentName() { return agentName; },
    get agentNote() { return agentNote; },
    get ago() { return ago; },
    get button() { return button; },
    get catalog() { return catalog; },
    get checkSkillRepo() { return checkSkillRepo; },
    get checksSection() { return checksSection; },
    get confirmBatch() { return confirmBatch; },
    get crew() { return crew; },
    get detailShell() { return detailShell; },
    get el() { return el; },
    get facts() { return facts; },
    get generation() { return state.connection.generation; },
    set generation(value) { state.connection.generation = value; },
    get go() { return go; },
    get icon() { return icon; },
    set icon(value) { icon = value; },
    get collectionPaging() { return collectionPaging; },
    get catalogQuery() { return catalogQuery; },
    get virtualList() { return virtualList; },
    get loadTrash() { return loadTrash; },
    get matches() { return matches; },
    get notice() { return notice; },
    get pageBack() { return imageViews.pageBack; },
    get pages() { return pages; },
    get pcLegend() { return pcLegend; },
    get pcLink() { return pcLink; },
    get query() { return appApi.query; },
    get raw() { return raw; },
    get readKey() { return readKey; },
    get render() { return render; },
    get renderDetail() { return renderDetail; },
    get repoOf() { return repoOf; },
    get request() { return appApi.request; },
    get row() { return row; },
    get section() { return section; },
    get setSkillStatus() { return setSkillStatus; },
    get shownAgents() { return shownAgents; },
    get skillGenerate() { return skillGenerate; },
    get skillReview() { return skillReview; },
    get state() { return state; },
    get store() { return store; },
    get TRASH_KEY() { return TRASH_KEY; },
    get trashed() { return trashed; },
    get trashSkills() { return trashSkills; },
    get updateSkills() { return updateSkills; },
    get writeKey() { return writeKey; }
  });

  const imageViews = createImageViews({
    get $() { return $; },
    get agentLogo() { return agentLogo; },
    get button() { return button; },
    get crew() { return crew; },
    get detailShell() { return detailShell; },
    get el() { return el; },
    get fillList() { return designViews.fillList; },
    get gallery() { return gallery; },
    get icon() { return icon; },
    set icon(value) { icon = value; },
    get markSelected() { return markSelected; },
    get matches() { return matches; },
    get notice() { return notice; },
    get openDetail() { return openDetail; },
    get pages() { return pages; },
    get query() { return appApi.query; },
    get raw() { return raw; },
    get render() { return render; },
    get renderDetail() { return renderDetail; },
    get request() { return appApi.request; },
    get section() { return section; },
    get segment() { return segment; },
    get shortDate() { return shortDate; },
    get state() { return state; },
    get store() { return store; },
    get stored() { return stored; },
    get tr() { return tr; },
    get trimSlash() { return trimSlash; }
  });

  const designViews = createDesignViews({
    get $() { return $; },
    get button() { return button; },
    get callout() { return callout; },
    get copyButton() { return imageViews.copyButton; },
    get copyTitle() { return imageViews.copyTitle; },
    get detailShell() { return detailShell; },
    get el() { return el; },
    get gallery() { return gallery; },
    get go() { return go; },
    get markSelected() { return markSelected; },
    get matches() { return matches; },
    get notice() { return notice; },
    get openDetail() { return openDetail; },
    get paneSearch() { return imageViews.paneSearch; },
    get query() { return appApi.query; },
    get raw() { return raw; },
    get render() { return render; },
    get renderDetail() { return renderDetail; },
    get request() { return appApi.request; },
    get section() { return section; },
    get segment() { return segment; },
    get shortDate() { return shortDate; },
    get state() { return state; },
    get store() { return store; },
    get tr() { return tr; }
  });

  const toolViews = createToolViews({
    get $() { return $; },
    get AGENT_SYNC() { return skillViews.AGENT_SYNC; },
    get agentLogo() { return agentLogo; },
    get agentName() { return agentName; },
    get AGENTS() { return AGENTS; },
    get ago() { return ago; },
    get button() { return button; },
    get catalog() { return catalog; },
    get copyButton() { return imageViews.copyButton; },
    get detailShell() { return detailShell; },
    get el() { return el; },
    get generation() { return state.connection.generation; },
    set generation(value) { state.connection.generation = value; },
    get headExtras() { return headExtras; },
    get icon() { return icon; },
    set icon(value) { icon = value; },
    get matches() { return matches; },
    get notice() { return notice; },
    get openDetail() { return openDetail; },
    get query() { return appApi.query; },
    get raw() { return raw; },
    get render() { return render; },
    get renderDetail() { return renderDetail; },
    get renderStatusCard() { return renderStatusCard; },
    get request() { return appApi.request; },
    get shownAgents() { return shownAgents; },
    get filterMenu() { return skillViews.filterMenu; },
    get barActions() { return barActions; },
    get panelSearch() { return skillViews.panelSearch; },
    get keepFocus() { return skillViews.keepFocus; },
    get state() { return state; },
    get tauri() { return tauri; }
  });

  // ---------------------------------------------------------------- detail
  // TOOLS › 스킬·플러그인·MCP open an item in the crew popup frame (crew-popup.js) instead of the right panel.
  // The skill pages (GitHub에서 설치, 개선 단계) keep the right panel: it explains the page, not an item.
  const popupDetail = () => state.view === "tools" || (state.view === "skills" && !state.skillPage);
  let itemPop = null;
  function detailShell(label) {
    if (popupDetail()) {
      if (itemPop) itemPop.setTitle(label);
      else itemPop = window.ArkismCrewPopup({ el, button }).open({ title: label, tabs: [{ key: "detail", label, render: () => {} }], cls: "item-pop",
        onClose: () => { itemPop = null; if (popupDetail() && state.selected) { state.selected = null; markSelected(); } } });
      const body = el("div", "detail-body");
      itemPop.body.replaceChildren(body);
      return body;
    }
    const d = $("#detail");
    const bar = el("div", "detail-bar");
    const close = el("button", "icon-btn plain detail-close"); close.type = "button"; close.setAttribute("aria-label", "닫기"); close.append(icon("x"));
    close.addEventListener("click", closeDetail);
    bar.append(el("span", "", label), close);
    const body = el("div", "detail-body");
    d.replaceChildren(bar, body);
    return body;
  }
  function section(title, aside) {
    const s = el("section", "section");
    const h = el("h3", "", title);
    if (aside) h.append(el("small", "", aside));
    s.append(h);
    return s;
  }
  function facts(pairs) {
    const dl = el("dl", "facts");
    pairs.filter(([, v]) => v).forEach(([k, v]) => dl.append(el("dt", "", k), el("dd", "", v)));
    return dl;
  }
  function callout(title, text, iconName = "warning-circle") {
    const c = el("div", "callout");
    const t = el("div", "");
    t.append(el("strong", "", title), el("p", "", text));
    c.append(icon(iconName), t);
    return c;
  }
  function renderDetail() {
    if (state.view === "memories" && state.memTab === "traits") return traits.detail();
    if (state.view === "memories" && state.memTab === "relations") return relations.detail();
    if (state.view === "memories" && state.memTab === "reflections") return reflections.detail();
    if (state.view === "memories" && state.memTab === "past") return memoryTime.detail();
    if (state.view === "memories" && state.memTab === "originals") return originals.detail();
    if (state.view === "memories" && ["digests", "merge"].includes(state.memTab)) return digests.detail();
    const id = state.selected;
    if (state.view === "todo") {
      if (state.start) return home.startDetail();
      const t = id && todoItems().find((x) => x.id === todoKey(id));
      return t ? todoDetail(t) : homeSummary();
    }
    if (state.view === "images") {
      const run = state.promptRun && state.data.prompts.find((x) => x.id === state.promptRun);
      if (run) return imageViews.promptRunDetail(run);
      const p = state.data.prompts.find((x) => `prompt:${x.id}` === id);
      return p ? imageViews.promptDetail(p) : imageViews.imagesSummary();
    }
    if (state.view === "crew") return crew.detail();
    if (state.view === "reviews") return reviews.detail();
    if (state.view === "design") {
      const r = (state.data.designSystems || []).find((x) => `ds:${x.id}` === id);
      return r ? designViews.systemDetail(r) : designViews.librarySummary();
    }
    if (state.view === "tools") {
      const t = toolViews.toolEntries().find((x) => `tool:${x.name}` === id);
      return t ? toolViews.toolDetail(t) : itemPop?.dialog.close();
    }
    if (state.view === "skills") {
      if (state.skillPage) return skillViews.skillPageDetail();
      const s = catalogQuery.record(id) || [...trashed(), ...state.data.skills].find((x) => `skill:${x.id}` === id);
      return s ? skillViews.skillDetailV4(s) : itemPop?.dialog.close();
    }
    if (state.view === "memories" && id === "batch") return reviewBatchDetail(detailShell("한 번에 처리"));
    if (state.view === "memories" && id) {
      const all = [...state.data.memories, ...memoryPaging.memoryResults.snapshot().items, ...state.data.candidates, ...state.data.conflicted];
      const m = catalogQuery.record(id) || all.find((x) => x.id === id);
      if (m) return m.status === "active" ? memoryDetail.memoryDetail(m) : memoryDetail.candidateDetail(m);
    }
    summaryDetail();
  }
  function summaryDetail() {
    const body = detailShell("요약");
    const d = state.data;
    body.append(el("h2", "", "Arkism"), el("p", "lead", "에이전트들이 함께 쓰는 기억과 스킬이에요. 목록에서 하나를 고르면 왜 저장됐는지와 언제 쓰였는지 볼 수 있어요."));
    // Kinds across every memory on the server; without its totals, the loaded rows say so in the title.
    const kinds = state.memoryTotals?.kinds;
    const byKind = Object.keys(KINDS).map((k) => [kindLabel(k), `${kinds ? kinds[k] || 0 : d.memories.filter((m) => m.kind === k).length}개`]).filter(([, v]) => v !== "0개");
    const s = section(kinds || !memoryPaging.partialMemories() ? "사용 중인 기억" : `사용 중인 기억 · 불러온 ${d.memories.length}개 기준`); s.append(facts(byKind)); body.append(s);
    const r = section("어떻게 쓰이나요");
    r.append(facts([["대화 시작", "선호·작업 규칙을 모두 넣어요"], ["질문할 때", "관련 있는 기억만 최대 6개"], ["승인 대기", "대화에서 후보를 추출해요. 저장 여부는 사람이 검토해요."], ["저장소", "저장소에서 나온 기억은 그 저장소에서만, 나머지는 어디서나 써요"]]));
    body.append(r);
  }
  const renderMemoryContext = window.ArkismMemoryContext({ el });
  const renderModelGeneration = window.ArkismModelGeneration({ el });
  // Figma 198:630 detail: kind · title · meta → action card → 어디서 왔나요 → 언제 쓰이나요 → 바뀐 이력.
  function detailHead(body, label, title, meta) {
    const top = el("div", "d-head");
    top.append(el("p", "d-kind", label), raw(el("h2", "", title)));
    if (meta) top.append(el("p", "meta", meta));
    body.append(top);
  }
  function actionCard(text, ...buttons) {
    const c = el("div", "d-action");
    c.append(el("p", "", text));
    if (buttons.length) { const row = el("div", "d-buttons"); row.append(...buttons); c.append(row); }
    return c;
  }
  const memoryDetail = createMemoryDetail({
    get actionCard() { return actionCard; },
    get ago() { return ago; },
    get button() { return button; },
    get decide() { return memoryList.decide; },
    get detailHead() { return detailHead; },
    get detailShell() { return detailShell; },
    get el() { return el; },
    get facts() { return facts; },
    get failNotice() { return failNotice; },
    get generation() { return state.connection.generation; },
    set generation(value) { state.connection.generation = value; },
    get idle() { return idle; },
    get isCorrection() { return isCorrection; },
    get KEEP_KEY() { return KEEP_KEY; },
    get keptMemories() { return keptMemories; },
    get kindLabel() { return kindLabel; },
    get memRepo() { return memRepo; },
    get memStat() { return memStat; },
    get memUse() { return memUse; },
    get notice() { return notice; },
    get originals() { return originals; },
    get originOf() { return originOf; },
    get overlapReview() { return overlapReview; },
    get privacy() { return privacy; },
    get query() { return appApi.query; },
    get raw() { return raw; },
    get reloadMemories() { return memoryList.reloadMemories; },
    get render() { return render; },
    get renderDetail() { return renderDetail; },
    get renderMemoryContext() { return renderMemoryContext; },
    get renderModelGeneration() { return renderModelGeneration; },
    get request() { return appApi.request; },
    get resolveConflict() { return memoryList.resolveConflict; },
    get reviewOnce() { return memoryList.reviewOnce; },
    get reviewOverdue() { return reviewOverdue; },
    get savedHow() { return savedHow; },
    get section() { return section; },
    get shortDate() { return shortDate; },
    get standing() { return standing; },
    get state() { return state; },
    get tr() { return tr; },
    get usageOf() { return usageOf; },
    get waitReason() { return waitReason; },
    get withAgent() { return withAgent; }
  });

  function agentNote(sk, k, staged) {
    if (staged) return staged === "allow" ? " · 추가 예정" : " · 뺄 예정";
    return skillViews.compatOf(sk)[k] === "requires_adapter" ? " · 어댑터" : "";
  }
  // PC에서 이 서버로 마지막 통신한 때를 색으로: 24시간 안·밀린 것 없음 초록, 오래됐거나 밀림 주황, 마지막 시도가 실패면 빨강, 기록 없으면 회색.
  const PC_STALE_MS = 24 * 3600 * 1000;
  function pcStatus(l) { // → [색 이름, 설명]
    if (!l?.last_seen || !Number.isFinite(new Date(l.last_seen).getTime())) return ["off", "연결 안 됨"];
    if (l.failing) return ["bad", "최근 통신이 실패했어요"];
    if (l.pending) return ["warn", `보내지 못한 ${l.pending}건이 밀려 있어요`];
    if (Date.now() - new Date(l.last_seen).getTime() > PC_STALE_MS) return ["warn", "오래됐어요"];
    return ["ok", "연결됨"];
  }
  function pcLink(k) {
    const l = (state.data.tools?.pc_links || []).find((x) => x.agent === k);
    const [cls, label] = pcStatus(l);
    const last = l?.last_seen ? ` · 마지막 통신 ${ago(l.last_seen)}` : "";
    const span = el("span", "pc-link " + cls);
    span.append(icon("desktop"));
    span.title = "PC · " + label + last;
    span.setAttribute("role", "img");
    span.setAttribute("aria-label", span.title);
    return span;
  }
  function pcLegend() {
    const row = el("p", "meta pc-legend", "PC 연결");
    for (const [cls, label] of [["ok", "24시간 안에 통신"], ["warn", "오래됨"], ["bad", "실패"], ["off", "연결 안 됨"]]) {
      const item = el("span", "pc-legend-item");
      const i = el("span", `pc-link ${cls}`); i.append(icon("desktop"));
      item.append(i, label);
      row.append(item);
    }
    return row;
  }
  function checksSection(sk) {
    const c = skillViews.skillChecks(sk);
    const sec = section("품질·보안 검사");
    if (!c) { sec.append(el("p", "meta", "불러오는 중…")); return sec; }
    const q = el("span", "check-cell"); q.append(el("span", `grade ${c.quality.cls}`, c.quality.grade), c.quality.score === null ? "" : `${c.quality.score}점`);
    const s2 = el("span", "check-cell"); s2.append(el("span", `sec ${c.security.cls}`, c.security.label), typeof c.security.risk === "number" ? `위험 ${c.security.risk}` : "");
    const scans = c.scans.map((x) => `${agentName(x.agent)} ${x.path}${x.error ? ` (${x.error})` : ""}`);
    const dl = facts([["빠진 설명", c.quality.missing.join(", ")], ["걸린 것", c.security.reasons.map(skillViews.findingText).join(" / ")], ["검사한 폴더", scans.join(", ")], ["검사 시각", c.scans[0] ? shortDate(c.scans[0].scanned_at) : ""], ["독립 검증", c.validated ? "평가 기록 있음" : ""]]);
    const add = (k, node) => { const dd = el("dd", ""); dd.append(node); return [el("dt", "", k), dd]; };
    dl.prepend(...add("품질", q), ...add("보안", s2));
    const tools = state.data.tools?.skill_check_tools;
    // skill_check_tools names the scanners: "arkism-builtin 1" always, NVIDIA's only where installed.
    const nvidia = /skill(evaluator|spector) \d/.test(tools || "");
    let note = "아직 서버 검사가 안 된 스킬이라, 스킬 설명이 얼마나 갖춰졌는지와 가져올 때 검사한 결과로 보여줘요.";
    if (c.source === "host" && nvidia) note = `Arkism 내장 검사와 NVIDIA SkillEvaluator(품질)·SkillSpector(보안)가 서버의 스킬 폴더를 검사한 결과예요. ${tools}`;
    else if (c.source === "host") note = `Arkism 내장 검사(비밀값·원격 실행·삭제 명령·자격 파일·유출·프롬프트 주입, SKILL.md 구성)가 서버의 스킬 폴더를 검사한 결과예요.${tools ? ` ${tools}` : ""}`;
    sec.append(dl, el("p", "meta", note));
    return sec;
  }
  // 서버에게 원본 저장소를 지금 다시 확인하게 한다 (서버는 6시간마다 확인). 1분 안에 결과가 돌아온다.
  async function checkSkillRepo(r) {
    try {
      const res = await appApi.request("/v1/agent_tools/requests", { method: "POST", body: JSON.stringify({ scope: state.scope, agent: "shared", kind: "skill_check", name: r.repo, enabled: true }) });
      state.data.tools.requests = res.agent_tools?.requests || state.data.tools.requests;
      notice("확인을 요청했어요. 1분 안에 갱신돼요.");
      toolViews.pollTools();
      renderDetail();
    } catch (e) { notice(`요청하지 못했어요 (${e.message}).`); }
  }
  // 스킬이 든 저장소: 원본 주소, 지금 커밋, 새 버전이 있는지, 서버가 마지막으로 확인한 때 (서버가 6시간마다 git fetch)
  const repoOf = (sk) => (state.data.tools?.skill_repos || []).filter((r) => (sk.source_path || "").includes(`${r.repo.replace(/^~/, "")}/`))
    .sort((x, y) => y.repo.length - x.repo.length)[0] || null;
  function openDetail() { if (!popupDetail()) $("#app").classList.add("detail-open"); } // a popup item needs no drawer
  function closeDetail() {
    itemPop?.dialog.close();
    $("#app").classList.remove("detail-open"); state.selected = null;
    if (state.view === "todo") { render(); return; } // home's title and body change with the selection too
    const panel = state.view === "memories" && { traits, reflections, relations, past: memoryTime, originals, digests, merge: digests }[state.memTab];
    if (panel) { panel.close(); return; }
    markSelected(); renderDetail();
  }
  const appConnection = createAppConnection({
    get overview() { return overview; },
    get knowledge() { return knowledge; },
    get collectionPaging() { return collectionPaging; },
    get catalogQuery() { return catalogQuery; },
    get virtualList() { return virtualList; },
    get similarity() { return similarity; },
    get $() { return $; },
    get button() { return button; },
    get crew() { return crew; },
    get defaults() { return defaults; },
    get el() { return el; },
    get generation() { return state.connection.generation; },
    set generation(value) { state.connection.generation = value; },
    get go() { return go; },
    get home() { return home; },
    get loadAll() { return loadAll; },
    get maintenanceRecords() { return memoryPaging.maintenanceRecords; },
    get memoryPages() { return memoryPaging.memoryPages; },
    get memoryResults() { return memoryPaging.memoryResults; },
    get notice() { return notice; },
    get pages() { return pages; },
    get query() { return appApi.query; },
    get raw() { return raw; },
    get render() { return render; },
    get request() { return appApi.request; },
    get routing() { return routing; },
    get segment() { return segment; },
    get setStatus() { return setStatus; },
    get state() { return state; },
    get store() { return store; },
    get tauri() { return tauri; },
    get usage() { return usage; }
  });

  const openSettings = () => go("settings");
  window.addEventListener("arkism:open-settings", (e) => { if (e.detail?.tab) state.settingsTab = e.detail.tab; go("settings"); });
  $("#search-toggle").addEventListener("click", () => home.palette());
  document.addEventListener("keydown", (e) => {
    const editing = ["INPUT", "TEXTAREA", "SELECT"].includes(document.activeElement?.tagName);
    if (e.key.toLowerCase() === "k" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); home.palette(); return; }
    if (e.key === "/" && !editing) { e.preventDefault(); const own = $(".panel-search input"); own?.offsetParent ? own.focus() : home.palette(); return; }
    if (e.key === "?" && !editing && !document.querySelector("dialog[open]")) { e.preventDefault(); home.shortcuts(); return; }
    if (e.key === "Escape" && $("#app").classList.contains("detail-open")) { closeDetail(); return; }
    if (!editing && (e.key === "j" || e.key === "k")) {
      const rows = listItems();
      if (!rows.length) return;
      const i = rows.findIndex(isPicked);
      const prev = Math.max(0, i - 1);
      const next = e.key === "j" ? Math.min(rows.length - 1, i + 1) : prev;
      rows[next].click();
      rows[next].scrollIntoView({ block: "nearest" });
    }
  });

  // j/k walk the list in the panel: every row, card or chip there that opens something (data-id, or a button that
  // marks itself picked). Tabs, filter chips and form controls are not list items.
  const LIST_ITEM = ":is(.row, .cn-row, .todo-item, .crew-card, .prompt-card, .ds-card, .rt-mcard, .mp-chip, .wk-row, .wf-row, .tm-team, .tm-meet-row, .tm-urow, button.v4-tr, .cat-head, [data-id])";
  const NOT_ITEM = ".tab, .area-tab, .chip, .segment, [role=tab], input, select, textarea, label, dialog *";
  function listItems() {
    const root = document.querySelector(".home-first") && state.view === "todo" ? document.querySelector(".home-first") : $(".panel-main");
    return [...(root?.querySelectorAll(LIST_ITEM) || [])].filter((n) => !n.matches(NOT_ITEM) && !n.querySelector(LIST_ITEM)
      && (n.matches("button, a, [role=button], [tabindex]") || n.onclick) && n.offsetParent !== null);
  }
  const isPicked = (n) => n.matches(".selected, .active, .on, [aria-selected=true], [aria-current], [aria-pressed=true], [aria-expanded=true]");
  const models = window.ArkismModels({ state, el, icon, button, go, render, request: appApi.request, query: appApi.query, notice, agentLogo, ago });
  const routing = window.ArkismRouting({ canRefresh: () => !syncVisible.blocked(), state, $, el, icon, button, segment, go, render, request: appApi.request, query: appApi.query, notice, detailShell, agentLogo, ago });
  const usage = window.ArkismUsage({ state, $, el, button, segment, render, request: appApi.request, query: appApi.query, agentLogo, AGENTS, ago, go, detailShell,
    openSession: (sid) => window.dispatchEvent(new CustomEvent("arkism:open-overview-window", { detail: { wide: "sessions", args: sid ? [sid] : [] } })), openSettings: (tab) => { state.settingsTab = tab; go("settings"); },
    memory: () => ({ use: memUse(), memories: state.data.memories, kindLabel, often: overlapReview, openIdle: () => { state.menu = "memory"; go("memories", "idle"); },
      openMemory: (id) => { state.menu = "memory"; go("memories", "active"); state.selected = id; renderDetail(); } }),
    skillAgents: (name) => { const by = {}; for (const sk of state.data.skills.filter((x) => x.name === name)) for (const [k, n] of Object.entries(state.data.skillUse?.get(sk.id)?.by_runtime || {})) by[k] = (by[k] || 0) + n; return Object.entries(by).sort((a, b) => b[1] - a[1]).map(([k]) => k); } });
  const gallery = window.ArkismGalleryDelete({ state, $, el, button, icon, raw, render, request: appApi.request, query: appApi.query, notice, confirmPop: toolViews.confirmPop, shortDate });
  const remoteCollection = read => createRemoteCollection({ state, read, createPager: window.ArkismMemoryPages.create });
  const crew = createCrew({ state, $, el, icon, catIcon, catMenu, catChipBody, button, segment, go, render, request: appApi.request, query: appApi.query, notice, detailShell, section, facts, callout, copyButton: imageViews.copyButton, markSelected, openDetail, agentLogo, ago, models,
    allowed: skillViews.allowed, skillCategory: (sk) => pages.categoryOf(sk)?.name || "", skillCategories: () => pages.prefs.categories.map((c) => ({ name: c.name, icon: c.icon })),
    toolServers: () => state.data.tools?.servers || [] });
  window.ArkismAIChat.kit = { assist: crew.assist, assistError: crew.assistError, parseJSON: crew.parseJSON }; // field()'s default ask (ai-chat.js)
  const reviews = window.ArkismReviews({ state, $, el, icon, button, render, request: appApi.request, query: appApi.query, notice, detailShell, section, facts, markSelected, openDetail, ago, stat, go, headExtras, confirmPop: toolViews.confirmPop,
    assist: (kind, prompt) => crew.assist(kind, prompt) });
  const pages = window.ArkismPages({ state, $, el, icon, button, segment, go, render, AGENTS, agentLogo, allowed: skillViews.allowed, notice, store, stored, lock: appConnection.lock, disconnect: appConnection.disconnect, useLocal: appConnection.useLocal, startLocal: appConnection.startLocal, sshFields: appConnection.sshFields, sshTest: appConnection.sshTest, sshConnect: appConnection.sshConnect, sshLabel: appConnection.sshLabel, storageRow: appConnection.storageRow, reopen: appConnection.open, trimSlash, tauri, stat, models, routing, crew, catIconChoices: CAT_ICON_CHOICES,
    request: appApi.request, query: appApi.query, reloadTools: async () => { await toolViews.refreshTools(); toolViews.pollTools(); }, assist: (...a) => crew.assist(...a), privacyGroup: () => privacy.settingsGroup() });
  function openIntelligenceRecord(record) {
    const list = record.status === "active" ? "memories" : record.conflict_id ? "conflicted" : "candidates";
    // The record may be beyond the first list page. Keep the freshly checked revision.
    state.data[list] = [record, ...state.data[list].filter(item => item.id !== record.id)];
    state.query = ""; $("#search").value = "";
    go("memories", list === "memories" ? "active" : list === "candidates" ? "candidate" : "conflicted");
    state.selected = record.id; catalogQuery.pin(record.id, record); markSelected(); renderDetail(); openDetail();
    const heading = $("#detail h2");
    if (heading) { heading.tabIndex = -1; heading.focus(); }
  }
  const skillGenerate = window.ArkismSkillGenerate({ state, el, button, request: appApi.request, query: appApi.query, reload: loadAll });
  const catalog = window.ArkismCatalog({ state, el, button, request: appApi.request, notice, agentLogo, reload: loadAll, pollTools: toolViews.pollTools });
  const skillExecution = window.ArkismSkillExecution({ state, el, button, tauri });
  const skillTrial = window.ArkismSkillTrial({ state, el, button, request: appApi.request, query: appApi.query });
  const skillReview = window.ArkismSkillReview({ state, el, button, request: appApi.request, query: appApi.query, reload: loadAll, execution: skillExecution, trial: skillTrial });
  const relations = window.ArkismIntelligence({ canRefresh: () => !syncVisible.blocked(), state, $, el, icon, button, request: appApi.request, query: appApi.query, detailShell, openDetail, notice }, "relations");
  const traits = window.ArkismIntelligence({ canRefresh: () => !syncVisible.blocked(), state, $, el, icon, button, request: appApi.request, query: appApi.query, detailShell, openDetail, openRecord: openIntelligenceRecord }, "traits");
  const reflections = window.ArkismIntelligence({ canRefresh: () => !syncVisible.blocked(), state, $, el, icon, button, request: appApi.request, query: appApi.query, detailShell, openDetail, openRecord: openIntelligenceRecord });
  const digests = window.ArkismDigests({ canRefresh: () => !syncVisible.blocked(), state, $, el, raw, tr, icon, button, request: appApi.request, query: appApi.query, detailShell, openDetail, notice, ago, render, row, markSelected,
    kindLabel, originOf, memSelect: memoryList.memSelect, section, facts, openRecord: (r) => openIntelligenceRecord(r), reloadMemories: memoryList.reloadMemories });
  const memoryTime = window.ArkismMemoryTime({ state, $, el, icon, button, request: appApi.request, detailShell, openDetail });
  const inject = window.ArkismInject({ state, el, button, request: appApi.request, query: appApi.query, section, facts });
  const originals = window.ArkismOriginals({ state, $, el, icon, button, request: appApi.request, detailShell, openDetail, ago });
  const privacy = window.ArkismPrivacy({ state, el, button, request: appApi.request, query: appApi.query, notice, section, afterChange: async () => { await memoryList.reloadMemories(); renderDetail(); } });
  const knowledge = window.ArkismKnowledge({ remoteCollection, virtualList, state, $, el, button, icon, render, request: appApi.request, query: appApi.query, ago, detailShell, headExtras, confirmPop: toolViews.confirmPop }); // TOOLS › 지식 (knowledge.js)
  const overview = window.ArkismOverview({ remoteCollection, state, $, el, button, icon, request: appApi.request, query: appApi.query, ago, go, render, reloadMemories: () => memoryList.reloadMemories(), maintenanceRecords: memoryPaging.maintenanceRecords, openRecord: openIntelligenceRecord, resolveConflict: (c, choice) => memoryList.resolveConflict(c, choice), memoryCount: () => memoryPaging.memoryCount(), onSessions: () => renderStatusCard() }); // MEMORY › 개요 (overview.js)
  Object.assign(pages.views, routing.views, usage.views, knowledge.views, overview.views);
  const home = window.ArkismHome({ state, $, el, icon, button, go, render, request: appApi.request, query: appApi.query, notice, agentLogo, AGENTS, ago, pages, detailShell, markSelected, renderDetail, openDetail,
    skillCategory: (sk) => pages.categoryOf(sk)?.name || "", isPage: (v) => pages.views[v] || crew.pages[v], settingsTabs: () => shell.MENUS.settings.tabs, openRecord: openIntelligenceRecord,
    openSettings: (tab) => { if (tab) state.settingsTab = tab; go("settings"); }, sshLabel: appConnection.sshLabel, sshErrorText: appConnection.sshErrorText, useLocal: appConnection.useLocal, lock: appConnection.lock, reconnect: appConnection.reconnect });
  const homeChecks = window.ArkismHomeChecks({ state, request: appApi.request, query: appApi.query, go, ago, render }); // 홈 › 기억 처리 멈춤 · 지식 줄 (home-checks.js)
  home.wireSearch();
  shell = window.ArkismShell({ state, $, el, go, render });
  $("#status-toggle").addEventListener("click", () => renderStatusCard()); // after shell.js opened it: fill the 기억 처리 stages
  Object.assign(views, pages.views);
  Object.assign(crew.pages, window.ArkismTeams({ state, $, el, icon, catIcon, catMenu, catChipBody, button, go, render, request: appApi.request, query: appApi.query, notice, ago, headExtras, hasView: (v) => !!views[v] }).pages); // CREW › 팀·회의·디스코드·사용량 (team.js)
  Object.assign(views, crew.views, crew.pages, reviews.views);
  window.ArkismNotice = notice;
  // A separate window saved or closed: show its changes here too.
  window.addEventListener("storage", (e) => { if (e.key === "arkism.popout-saved" && state.token && !document.documentElement.classList.contains("popout")) void loadAll(); });
  void (async () => {
    const defaults = tauri ? await tauri.invoke("app_defaults").catch(() => null) : null;
    if (defaults) Object.assign(state.defaults, defaults);
    // 처음 실행이면(고른 적 없음) 이 PC / SSH를 고르게 한다. 연결 끊기도 고른 것을 지워 이 화면으로 돌아온다.
    let picked = stored("arkism.mode");
    // 예전 판은 이 PC를 쓸 때 아무것도 저장하지 않았다: 기본 폴더에 데이터가 있으면 이미 이 PC를 고른 것.
    if (state.defaults.local && picked === null && tauri && (await tauri.invoke("local_location").catch(() => null))?.default_has_data) { picked = "local"; store("arkism.mode", picked); }
    if (state.defaults.local && picked === "local") return appConnection.startLocal();
    state.mode = state.defaults.local && picked !== "remote" ? "choose" : "remote";
    appConnection.lock();
    // 지난번에 연결한 서버로 다시 붙는다.
    // 비밀번호 방식은 키체인에 저장된 비밀번호로 붙고, 없으면 폼에서 받는다.
    if (state.ssh && stored("arkism.mode") === "remote" && tauri) void appConnection.unlockConnect();
  })();
}
