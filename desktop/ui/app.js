(() => {
  "use strict";

  const defaults = { workspace_id: "default", project_id: "default", user_id: "management:owner", agent_id: "" };
  const readJSON = (raw) => { try { return JSON.parse(raw || "null"); } catch (_) { return null; } };
  const tauri = window.__TAURI__?.core;
  const store = (key, value) => { try { value === null ? localStorage.removeItem(key) : localStorage.setItem(key, value); } catch (_) { /* storage off: ask again next time */ } };
  const stored = (key) => { try { return localStorage.getItem(key); } catch (_) { return null; } };
  const state = {
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
    data: { memories: [], candidates: [], conflicted: [], maintenance: [], skills: [], prompts: [], designSystems: [], tools: null, crew: [] },
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
    versions: new Map(), // version_id -> skill version record (analysis + inspection), for 품질·보안
    versionsDone: false,
    engine: "all",
    imgCat: "all",
    usage: new Map(),
    usageDone: false,
    loading: true,
    ready: null,
    pending: new Map() // "skillId|agent" -> "allow" | "block", until 적용
  };
  let generation = 0;

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
  // The sidebar names each agent by the company behind it.
  const PROVIDERS = { claude_code: "Anthropic", codex: "OpenAI", gemini: "Google", zcode: "Z.ai", grok: "xAI" };
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
  function button(label, cls, iconName, onClick) {
    const b = el("button", `btn ${cls || ""}`.trim());
    b.type = "button";
    if (iconName) b.append(icon(iconName));
    b.append(label);
    if (onClick) b.addEventListener("click", onClick);
    return b;
  }
  const query = (extra = {}) => new URLSearchParams(Object.fromEntries(Object.entries({ ...state.scope, ...extra }).filter(([, v]) => v !== ""))).toString();
  // In the desktop app the Rust side makes the call (no CORS, token stays local);
  // a plain browser (development) talks to the server directly.
  async function request(path, options = {}) {
    let status, text;
    if (tauri) {
      const r = await tauri.invoke("api", { base: trimSlash(state.server), token: state.token, method: options.method || "GET", path, body: options.body ?? null })
        .catch((e) => { throw new Error(String(e)); });
      status = r.status; text = r.body;
    } else {
      const res = await fetch(trimSlash(state.server) + path, { ...options, headers: { Authorization: `Bearer ${state.token}`, "Content-Type": "application/json" } });
      status = res.status; text = await res.text();
    }
    const body = readJSON(text) || {};
    if (status < 200 || status >= 300) throw new Error(body.code || `HTTP ${status}`);
    return body;
  }
  async function listAll(path, extra, cap = 1000) {
    const items = [];
    let cursor = "";
    do {
      const page = await request(`${path}?${query({ ...extra, limit: 100, ...(cursor ? { cursor } : {}) })}`);
      items.push(...(page.items || []));
      cursor = page.next_cursor || "";
    } while (cursor && items.length < cap);
    return items;
  }
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
  function waitReason(item) {
    if ((item.tags || []).includes("review:instruction_like")) return ["명령처럼 보이는 문장 — 내가 확인", true];
    if (item.status === "conflicted") return ["기존 기억과 부딪힘", true];
    if ((item.tags || []).includes("basis:assistant_result")) return ["에이전트 결과 — 확인 필요", false];
    if ((item.tags || []).includes("basis:inferred")) return ["AI 추측 — 사용자가 말하지 않음", false];
    return ["검토 필요", false];
  }
  const standing = (m) => m.kind === "user" || m.kind === "procedural";
  function usageOf(id) {
    const items = state.usage.get(id);
    if (!items) return null;
    const now = Date.now();
    const within = (days) => items.filter((u) => now - new Date(u.created_at).getTime() < days * DAY).length;
    // The server keeps the 10 most recent uses, so a full page means "at least".
    const capped = (n) => (n >= 10 && items.length >= 10 ? "10+" : String(n));
    return { d7: within(7), d30: within(30), label7: capped(within(7)), last: items[0]?.created_at || "" };
  }
  // 들어감 · 쓰임 over the last week, from the server's report (`arkism capture` utility).
  const memUse = () => state.data.tools?.memory_use || null;
  function memStat(id) {
    const u = memUse();
    if (!u) return null;
    if (!u._by) u._by = new Map(u.items.map((x) => [x.id, x]));
    return u._by.get(id) || { injected: 0, used: 0 };
  }
  const KEEP_KEY = "arkism.keptMemories";
  const keptMemories = () => { try { return JSON.parse(localStorage.getItem(KEEP_KEY) || "[]"); } catch (_) { return []; } };
  const unusedOften = (m) => { const s = memStat(m.id); return !!s && s.injected >= 3 && s.used === 0 && !keptMemories().includes(m.id); };
  function savedHow(m) {
    if (originOf(m) === "사용자 말") return "사용자가 직접 말해서 바로 저장";
    return m.review?.by ? `검토 후 승인 (${m.review.by})` : originOf(m);
  }
  const idle = (m) => {
    if (pages.prefs.collectUnused !== false && typeof unusedOften === "function" && unusedOften(m)) return true;
    const u = usageOf(m.id);
    return !!u && u.d30 === 0 && !standing(m) && Date.now() - new Date(m.created_at).getTime() > 7 * DAY;
  };
  const matches = (text) => !state.query || text.toLowerCase().includes(state.query);

  function setStatus(mode, text) {
    $("#status-dot").className = `dot ${mode}`;
    $("#status-text").textContent = text;
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
    if (!t?.collected_at) return ["", "확인 중", "서버 보고를 기다리는 중"];
    const age = Math.round((Date.now() - new Date(t.collected_at).getTime()) / 60000);
    if (age > 15) return ["bad", "끊김", `서버 보고가 ${age}분째 없어요`];
    const info = (t.agents || []).find((a) => a.id === key);
    if (!info) return ["", "확인 중", "서버가 이 에이전트를 아직 보고하지 않았어요"];
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
    const items = [
      ["brain", "메모리 DB", dbLabel(), state.ready !== false],
      ["database", "서버", host, true],
      ["git-merge", "스킬 허브", `${d.skills.length}개`, true],
      ["lock-simple", "로그인", state.email || "토큰", true]
    ];
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
    badge.classList.toggle("warn", issues > 0);
    $("#status-badge").textContent = issues ? `주의 ${issues}` : "자세히";
    const card = $("#status-card");
    card.querySelector(".status-summary").dataset.issues = issues ? `주의 ${issues}` : "";
    $("#status-list").replaceChildren(...items.map(([ic, label, value, ok]) => {
      const li = el("li", ok ? "" : "warn");
      li.append(icon(ic), el("span", "", label), raw(el("output", "", value)), el("i", `dot ${ok ? "ready" : "error"}`));
      return li;
    }));
    if (!card.querySelector(".st-foot")) {
      const foot = el("div", "st-foot");
      foot.append(el("p", "", "위쪽 점은 서버 연결 상태예요. DB 상태는 따로 확인해요."), button("설정", "", null, () => { card.hidden = true; openSettings(); }));
      card.append(foot);
      $("#status-agents").before(el("strong", "st-head", "에이전트"));
    }
    const newest = d.memories.reduce((t, m) => Math.max(t, new Date(m.created_at).getTime() || 0), 0);
    $("#status-meta").textContent = `기억 ${d.memories.length}개 · 마지막 저장 ${newest ? ago(new Date(newest)) : "—"}`;
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
    const n = $("#notice");
    n.replaceChildren();
    n.hidden = !text;
    if (!text) return;
    const close = el("button", "notice-close");
    close.type = "button";
    close.setAttribute("aria-label", "알림 닫기");
    close.append(icon("x"));
    close.addEventListener("click", () => notice(""));
    n.append(el("span", "", text), close);
  }

  // ---------------------------------------------------------------- data
  async function loadAll() {
    const g = ++generation;
    state.loading = true;
    state.usage = new Map();
    state.usageDone = false;
    state.versions = new Map();
    state.versionsDone = false;
    render();
    try {
      const canReview = !!state.caps?.actions?.review_list;
      void request("/readyz").then(() => true, () => false).then((ok) => { if (g === generation) { state.ready = ok; renderStatusCard(); } });
      const reviewsReq = reviews.load();
      const trashReq = loadTrash();
      const usageReq = request(`/v1/skills/usage?${query({ days: 60 })}`).then((r) => new Map((r.items || []).map((u) => [u.skill_id, u])), () => null);
      const [memories, candidates, conflicted, maintenance, skills, prompts, designSystems, tools, crewItems, providers, skillUse, reviewItems, skillTrash] = await Promise.all([
        listAll("/v1/memories", { status: "active" }),
        canReview ? listAll("/v1/memory_candidates", { status: "candidate" }) : [],
        canReview ? listAll("/v1/memory_candidates", { status: "conflicted" }).catch(() => []) : [],
        canReview ? loadMaintenance() : [],
        listAll("/v1/skills", {}, 2000),
        request(`/v1/prompts?${query()}`).then((r) => r.items || [], () => []),
        request(`/v1/design_systems?${query()}`).then((r) => r.items || [], () => null),
        loadTools(),
        crew.load(),
        models.load(), usageReq, reviewsReq, trashReq
      ]);
      if (g !== generation) return;
      state.data = { memories, candidates, conflicted, maintenance, skills, prompts, designSystems, tools, crew: crewItems, providers, skillUse, reviews: reviewItems, skillTrash };
      state.loading = false;
      state.loadError = "";
      setStatus("ready", "기억 저장소·검색 정상");
      render();
      renderStatusCard();
      void loadUsage(g);
    } catch (e) {
      if (g !== generation) return;
      state.loading = false;
      state.loadError = e.message;
      setStatus("error", "연결 확인 필요");
      render();
      notice(`불러오지 못했어요: ${e.message}`);
    }
  }
  // shortcut: one usage call per memory (6 at a time); add a list-level usage summary to the API if this gets slow.
  async function loadUsage(g) {
    const queue = [...state.data.memories];  // one repaint at the end: per-batch repaints of the whole list made startup stutter
    const worker = async () => {
      while (queue.length && g === generation) {
        const m = queue.shift();
        const items = await request(`/v1/memories/${encodeURIComponent(m.id)}/usage?${query()}`).then((r) => r.items || [], () => []);
        if (g !== generation) return;
        state.usage.set(m.id, items);
      }
    };
    await Promise.all(Array.from({ length: 6 }, worker));
    if (g !== generation) return;
    state.usageDone = true;
    render(false);
  }

  // shortcut: one version call per skill (6 at a time) for 품질·보안; add both to the skill list API if this gets slow.
  let versionsLoading = -1;
  async function loadVersions(g) {
    if (g !== generation || versionsLoading === g) return;
    versionsLoading = g;
    const versions = state.versions;
    const queue = state.data.skills.filter((sk) => sk.version_id && !versions.has(sk.version_id));
    const worker = async () => {
      while (queue.length && g === generation && versions === state.versions) {
        const sk = queue.shift();
        const version = await request(`/v1/skill_versions/${encodeURIComponent(sk.version_id)}?${query()}`).catch(() => null);
        if (g !== generation) return;
        if (versions !== state.versions) return;
        versions.set(sk.version_id, version);
      }
    };
    await Promise.all(Array.from({ length: 6 }, worker));
    if (g !== generation) return;
    if (versions !== state.versions) return;
    state.versionsDone = true;
    render(false);
  }

  // Keep the current page fresh without a loading screen or rebuilding unchanged DOM.
  let syncing = false, syncAgain = false;
  const syncPage = () => [state.view, state.memTab, state.skillTab, state.settingsTab].join("|");
  const syncBlocked = () => !state.token || state.loading || document.hidden || state.pending.size > 0 ||
    !!document.querySelector("dialog[open], #detail [data-sync-dirty]") || (document.activeElement?.id !== "search" && !!document.activeElement?.matches("input, textarea, select, [contenteditable=true]"));
  async function syncVisible(event) {
    if (syncing) { syncAgain ||= !!event; return; }
    if (syncBlocked()) return;
    const view = state.view;
    if (!["todo", "memories", "skills", "tools", "crew", "images", "design", "routing", "settings"].includes(view)) return;
    if (view === "memories" && ["originals", "past"].includes(state.memTab)) return;
    if (view === "settings" && state.settingsTab !== "models") return;
    syncing = true;
    const g = generation, page = syncPage(), before = state.data;
    const reads = [];
    const add = (key, promise) => reads.push({ key, old: before[key], promise });
    const items = (path, extra = {}) => request(`${path}?${query(extra)}`).then((r) => r.items || []);
    try {
      if (view === "routing") return await routing.refresh();
      const panel = view === "memories" && { relations, traits, reflections }[state.memTab];
      if (panel) return await panel.refresh();
      if (view === "memories" && ["digests", "merge"].includes(state.memTab)) digests.refresh();
      if (view === "settings") add("providers", request(`/v1/providers?${query()}`).then((r) => r.providers));
      if (["todo", "memories"].includes(view)) {
        add("memories", listAll("/v1/memories", { status: "active" }));
        if (state.caps?.actions?.review_list) {
          add("candidates", listAll("/v1/memory_candidates", { status: "candidate" }));
          add("conflicted", listAll("/v1/memory_candidates", { status: "conflicted" }));
          add("maintenance", items("/v1/maintenance_proposals", { status: "open" }));
        }
      }
      if (["todo", "skills"].includes(view)) {
        add("skills", listAll("/v1/skills", {}, 2000));
        add("skillTrash", listAll("/v1/skills/trash", {}, 100000).then((rows) => rows.map((r) => ({ ...r, status: "deprecated" }))));
        add("skillUse", items("/v1/skills/usage", { days: 60 }).then((rows) => new Map(rows.map((r) => [r.skill_id, r]))));
      }
      if (["todo", "skills", "tools"].includes(view)) add("tools", loadTools());
      if (view === "crew") add("crew", items("/v1/crew"));
      if (view === "images") add("prompts", items("/v1/prompts"));
      if (view === "design") add("designSystems", items("/v1/design_systems"));
      const results = await Promise.allSettled(reads.map((r) => r.promise));
      if (g !== generation || page !== syncPage() || before !== state.data || syncBlocked()) return;
      let changed = false;
      const snapshot = (v) => JSON.stringify(v instanceof Map ? [...v] : v, (key, value) => key === "_by" ? undefined : value);
      results.forEach((result, i) => {
        const { key, old } = reads[i];
        if (result.status !== "fulfilled" || result.value == null || state.data[key] !== old || snapshot(old) === snapshot(result.value)) return;
        if (key === "skills") {
          const previous = new Map((old || []).map((r) => [r.id, r]));
          state.versions = new Map(result.value.filter((r) => r.version_id === previous.get(r.id)?.version_id && r.updated_at === previous.get(r.id)?.updated_at && state.versions.has(r.version_id)).map((r) => [r.version_id, state.versions.get(r.version_id)]));
          state.versionsDone = false; versionsLoading = -1;
        }
        state.data[key] = result.value;
        changed = true;
      });
      if (changed) render();
    } finally {
      syncing = false;
      if (syncAgain) { syncAgain = false; queueMicrotask(syncVisible); }
    }
  }
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
    memories: { title: () => memTitle()[0], sub: () => memTitle()[1], count: () => memTitle()[2] },
    skills: { title: () => (state.skillPage?.kind === "improve" ? `${state.data.skills.find((x) => x.id === state.skillPage.id)?.name || "스킬"} 개선하기` : { trash: "휴지통·중복", install: "GitHub에서 스킬 설치", route: "스킬 고르기 시험" }[state.skillPage?.kind] || "스킬"),
      sub: () => ({ improve: "개선안 만들기 → 기존 버전과 비교 → 검토·승인 → 실제 작업에 시험 적용 순서로 진행해요.", trash: "안 쓰는 스킬은 휴지통으로 옮기고, 여러 곳에 같은 이름으로 있는 스킬을 정리해요.",
        install: "저장소 주소를 넣으면 안에 있는 스킬을 보여 줘요. 받은 파일은 실행하지 않아요.",
        route: "할 일을 적으면 에이전트가 어떤 스킬을 꺼내 쓸지 미리 봐요. 실제로 실행하지는 않아요." }[state.skillPage?.kind] || "에이전트가 일할 때 꺼내 쓰는 작업 방법이에요.") },
    design: { title: "디자인 시스템", sub: () => "앱·메일·웹·문서에 쓰는 색·글꼴·규칙 모음이에요. 하나를 고르면 오른쪽에서 자세히 볼 수 있어요." },
    images: {
      title: () => (state.promptRun ? `프롬프트 실행${(state.data.prompts.find((x) => x.id === state.promptRun)?.image_prompt.engine || "").startsWith("comfyui") ? " · ComfyUI" : ""}` : state.imageTab === "slide" ? "슬라이드 프롬프트" : "이미지 프롬프트"),
      sub: () => (state.promptRun ? "변수 값을 채우고 바로 만들거나, ComfyUI 워크플로우로 보내요." : state.imageTab === "slide" ? "발표·보고용 슬라이드 프롬프트와 만드는 스크립트를 모아 둬요. 카드를 고르면 오른쪽에서 고치고 바로 만들 수 있어요." : "자주 쓰는 이미지 프롬프트를 예시 이미지와 함께 모아 둬요. 모델과 카테고리로 골라 보세요.")
    },
    tools: { title: () => (state.toolTab === "plugin" ? "플러그인" : "MCP 서버"), sub: () => (state.toolTab === "plugin" ? "플러그인은 스킬 + MCP + 명령을 한 번에 설치하는 묶음이에요." : "에이전트가 쓰는 외부 도구 서버예요. 플러그인은 스킬·MCP를 묶어서 한 번에 설치해요.") }
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
    notice("AI가 최근 기억과 후보를 보고 새 스킬을 찾는 중이에요…");
    const d = state.data;
    const lines = [...d.candidates, ...d.memories].filter((m) => ["procedural", "lesson", "decision"].includes(m.kind) || d.candidates.includes(m)).slice(0, 80).map((m) => `- ${(m.text || "").slice(0, 160)}`).join("\n");
    const prompt = `이미 있는 스킬: ${d.skills.map((sk) => sk.name).join(", ").slice(0, 4000)}\n\n최근 기억과 기억 후보(사용자가 반복해서 시킨 일의 흔적):\n${lines || "(없음)"}`;
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
      analyzing = false;
      render(false);
    }
  }
  // Similar skills: letter pairs of the description (Dice, which suits Korean without a tokenizer) or name words, 60% and up.
  const pairsOf = (t) => { const s = (t || "").toLowerCase().replace(/[^a-z0-9가-힣]/g, ""); const out = new Map(); for (let i = 0; i < s.length - 1; i++) { const k = s.slice(i, i + 2); out.set(k, (out.get(k) || 0) + 1); } return out; };
  function dice(a, b) {
    let inter = 0, na = 0, nb = 0;
    for (const v of a.values()) na += v;
    for (const v of b.values()) nb += v;
    for (const [k, v] of a) inter += Math.min(v, b.get(k) || 0);
    return na + nb ? (2 * inter) / (na + nb) : 0;
  }
  const words = (sk) => ({ desc: pairsOf(sk.description), name: new Set(sk.name.toLowerCase().split(/[-_]/).filter((w) => w.length > 1)) });
  function compareStrings(x, y) {
    if (x < y) return -1;
    if (x > y) return 1;
    return 0;
  }
  function nameWordOverlap(wa, wb) {
    let inter = 0;
    for (const w of wa.name) if (wb.name.has(w)) inter++;
    return wa.name.size && wb.name.size ? inter / (wa.name.size + wb.name.size - inter) : 0;
  }
  function similarScore(wa, wb) {
    return Math.max(dice(wa.desc, wb.desc), nameWordOverlap(wa, wb));
  }
  let similarCache = {};
  function similarPairs() {
    const live = state.data.skills.filter((sk) => sk.status !== "deprecated");
    const dismissed = readKey(SIMILAR_KEY, []);
    const key = JSON.stringify([live.map((sk) => [sk.id, sk.name, sk.description]), dismissed]);
    if (similarCache.skills === state.data.skills && similarCache.key === key) return similarCache.items;
    // shortcut: quadratic comparison only when inputs change; index pairs if the catalog grows further.
    const sets = live.map((sk) => [sk, words(sk)]);
    const out = [];
    for (let i = 0; i < sets.length; i++) {
      for (let j = i + 1; j < sets.length; j++) {
        const [a, wa] = sets[i], [b, wb] = sets[j];
        const score = similarScore(wa, wb);
        const key = [a.id, b.id].sort(compareStrings).join("|");
        if (score >= 0.6 && !dismissed.includes(key)) out.push({ a, b, score, key });
      }
    }
    out.sort((x, y) => y.score - x.score);
    similarCache = { skills: state.data.skills, key, items: out.slice(0, 20) };
    return similarCache.items;
  }
  async function setSkillStatus(sk, status) {
    const path = `/v1/skills/${encodeURIComponent(sk.id)}`;
    const cur = await request(`${path}?${query()}`);
    await request(`${path}/availability`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: cur.revision, status, compatibility: cur.skill_compatibility || {} }) });
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
      li.append(el("code", "", sk.name), el("small", "dim", `60일 동안 0번 · ${shownAgents().filter(([k]) => allowed(sk, k)).length}곳에서 읽음`));
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
    return listAll("/v1/skills/trash", {}, 100000).then((items) => items.map((x) => ({ ...x, status: "deprecated" })), () => null);
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
    state.data.skills = await listAll("/v1/skills", {}, 2000).catch(() => state.data.skills);
    state.data.skillTrash = await loadTrash();
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
    const where = (sk) => shownAgents().filter(([k]) => allowed(sk, k)).length;
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
        const a = await crew.assist("draft", `두 스킬을 하나로 합친 SKILL.md를 써 줘. 이름은 ${keep.name}. 두 스킬의 단계·제약·스크립트를 빠짐없이 살려.\n\n===== ${keep.name}/SKILL.md =====\n{{SKILL.md:${keep.source_path}}}\n\n===== ${drop.name}/SKILL.md =====\n{{SKILL.md:${drop.source_path}}}`, { purpose: "skill" });
        if (a.status !== "done") throw new Error(crew.assistError(a));
        const ta = el("textarea", "");
        ta.rows = 12;
        ta.value = a.output.trim();
        const md = el("div", "home-md");
        md.append(el("span", "", `합친 SKILL.md · 원본 저장소에 붙여 넣어 주세요`), ta);
        const row = el("div", "actions");
        row.append(copyButton(ta.value), button(`${drop.name} 휴지통으로`, "", "trash", () => confirmBatch([drop], () => trashSkills([drop]))));
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
    return request(`/v1/maintenance_proposals?${query({ status: "open" })}`).then((r) => r.items || [], () => []);
  }
  const TIDY_REASON = {
    same_meaning: "두 기억이 같은 말을 해요. 하나만 있어도 에이전트가 똑같이 알아요.",
    more_specific: "남길 기억이 더 자세해요. 다른 하나의 내용이 거기에 들어 있다고 봤어요.",
    newer_replaces_older: "새 기억이 예전 기억과 반대예요. 지금 생각은 새 기억이라고 봤어요."
  };
  function maintenanceShown() {
    const byId = new Map(state.data.memories.map((m) => [m.id, m]));
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
    const decide = (decision, done) => reviewOnce(t.p.id, async () => {
      await request(`/v1/maintenance_proposals/${encodeURIComponent(t.p.id)}/decide`, { method: "POST",
        body: JSON.stringify({ scope: state.scope, expected_revision: t.p.revision, decision }) });
      await reloadMemories();
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
  const BATCH = 5;
  function reviewBatchDetail(body) {
    const list = [...state.data.candidates].sort((a, b) => new Date(b.created_at) - new Date(a.created_at)).slice(0, BATCH);
    const keep = new Map(list.map((c) => [c.id, !(c.tags || []).includes("review:instruction_like")]));
    body.append(el("h2", "", `기억 후보 ${list.length}개`), el("p", "lead", "눌러서 저장·버리기를 바꾸고 한 번에 반영해요. 14일 동안 안 보면 저절로 사라져요."));
    const cards = el("div", "tidy-cards");
    const paint = () => cards.replaceChildren(...list.map((c) => {
      const on = keep.get(c.id);
      const card = el("button", `sim-card${on ? " on" : " off"}`);
      card.type = "button";
      const suspect = (c.tags || []).includes("review:instruction_like");
      card.append(el("span", on ? "tag" : "dim", on ? "저장" : "버리기"), raw(el("p", "", c.text)),
        el("small", "dim", `${kindLabel(c.kind)} · ${shortDate(c.created_at)}${suspect ? " · 지시처럼 보이는 문장" : ""}`));
      card.addEventListener("click", () => { keep.set(c.id, !on); paint(); });
      return card;
    }));
    paint();
    const apply = () => reviewOnce("batch", async () => {
      const items = list.map((c) => keep.get(c.id) ? { id: c.id, expected_revision: c.revision, action: "approve" }
        : { id: c.id, expected_revision: c.revision, action: "reject", reason: "Arkism 앱 묶음 검토에서 버림" });
      const r = await request("/v1/memory_candidates/review_batch", { method: "POST", body: JSON.stringify({ scope: state.scope, items }) });
      const failed = (r.items || []).filter((x) => x.status >= 300).length;
      await reloadMemories();
      notice(failed ? `${items.length - failed}개 반영, ${failed}개는 다른 곳에서 바뀌어 건너뛰었어요.` : `${items.length}개를 반영했어요.`);
    });
    const foot = el("div", "actions");
    foot.append(button("하나씩 보기", "ghost", null, () => go("memories", "candidate")), button(`${list.length}개 한 번에 반영`, "primary", "check", apply));
    const rest = state.data.candidates.length - list.length;
    body.append(cards, el("p", "meta", rest > 0 ? `반영하면 다음 ${Math.min(rest, BATCH)}개가 이어서 나와요 · 남은 후보 ${rest}개` : "이게 전부예요."), foot);
  }

  // 업데이트 가능: 서버 pulls each skill repository forward (git pull --ff-only), never merging.
  async function updateSkills(updates) {
    const repos = [...new Set(updates.map((u) => u.repo))];
    if (!confirm(`스킬 ${updates.length}개를 새 버전으로 받을까요?\n\n${repos.join("\n")}\n\n저장소를 앞으로만 당겨요(병합하지 않아요).`)) return;
    try {
      for (const repo of repos) {
        const r = await request("/v1/agent_tools/requests", { method: "POST", body: JSON.stringify({ scope: state.scope, agent: "shared", kind: "skill_update", name: repo, enabled: true }) });
        state.data.tools.requests = r.agent_tools?.requests || state.data.tools.requests;
      }
      notice("업데이트를 요청했어요. 1분 안에 받아져요.");
      pollTools();
    } catch (e) { notice(`요청하지 못했어요 (${e.message}).`); }
  }
  function todoItems() {
    const d = state.data;
    const suspect = d.candidates.filter((c) => (c.tags || []).includes("review:instruction_like"));
    const blocked = [];
    for (const sk of d.skills) {
      for (const [k] of shownAgents()) {
        if (sk.skill_compatibility?.[k] === "unsupported") blocked.push([sk, k]);
      }
    }
    const adapter = d.skills.filter((sk) => Object.values(sk.skill_compatibility || {}).includes("requires_adapter"));
    const idleList = d.memories.filter(idle);
    const fresh = d.memories.filter((m) => Date.now() - new Date(m.created_at).getTime() < 3 * DAY);
    const sug = suggestions();
    const pairs = similarPairs();
    const unusedSkills = d.skillUse ? d.skills.filter((sk) => sk.status !== "deprecated" && !d.skillUse.get(sk.id)) : [];
    const first = sug.items[0], pair = pairs[0];
    const tidy = maintenanceShown(), tp = tidy[0];
    const updates = d.tools?.skill_updates || [];
    const ver = (v) => (/^\d/.test(v || "") ? `v${v}` : v || "");
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
      { id: "review", n: d.candidates.length, icon: "tray", label: "기억 검토", title: `기억 후보 ${d.candidates.length}개 검토`,
        sub: suspect.length ? `지시처럼 보이는 문장 ${suspect.length}개가 있어요` : "자동으로 저장하지 않은 후보예요", warn: suspect.length > 0,
        action: "한 번에 검토", detail: (body) => reviewBatchDetail(body), preview: d.candidates.slice(0, 5).map((c) => c.text) },
      { id: "conflicted", n: d.conflicted.length, icon: "warning-circle", label: "부딪히는 기억", title: `부딪히는 기억 ${d.conflicted.length}개`,
        sub: "기존 기억과 내용이 달라 어느 쪽이 맞는지 골라야 해요", warn: true, action: "고르기", go: ["memories", "conflicted"],
        preview: d.conflicted.slice(0, 5).map((c) => c.text) },
      { id: "blocked", n: blocked.length, icon: "plug", label: "빠진 에이전트", title: blocked.length ? `${blocked[0][0].name}를 ${agentName(blocked[0][1])}에서 못 써요` : "",
        sub: blocked.length > 1 ? `외 ${blocked.length - 1}곳이 막혀 있어요` : "칸을 눌러 추가하고 적용하면 바로 써요", action: blocked.length ? `${agentName(blocked[0][1])}에 추가` : "",
        run: () => { stage(blocked[0][0].id, blocked[0][1], "allow"); go("skills", "missing"); }, go: ["skills", "missing"],
        agent: blocked.length ? [AGENTS.find(([k]) => k === blocked[0][1])?.[2] || "sym:i-plug", agentName(blocked[0][1])] : null,
        preview: blocked.slice(0, 5).map(([sk, k]) => `${sk.name} → ${agentName(k)}`) },
      { id: "idle", n: state.usageDone ? idleList.length : null, icon: "clock-counter-clockwise", label: "안 쓰는 기억", title: `30일 동안 안 쓰인 기억 ${idleList.length}개`,
        sub: "질문에 한 번도 들어가지 않았어요", action: "보기", go: ["memories", "idle"], preview: idleList.slice(0, 5).map((m) => m.text) },
      { id: "fresh", n: fresh.length, icon: "sparkle", label: "새 기억", title: `최근 3일 새 기억 ${fresh.length}개`, sub: "맞는 내용인지 훑어보세요",
        action: "훑어보기", go: ["memories", "active"], preview: fresh.slice(0, 5).map((m) => m.text) },
      { id: "adapter", n: adapter.length, icon: "stack", label: "어댑터 필요", title: `어댑터가 필요한 스킬 ${adapter.length}개`, sub: "그대로는 일부 에이전트에서 제대로 안 돌 수 있어요",
        action: "보기", go: ["skills", "adapter"], preview: adapter.slice(0, 5).map((sk) => sk.name) }
    ];
    const ORDER = ["suggest", "review", "blocked", "updates", "conflicted", "maintenance", "similar", "unused", "idle", "fresh", "adapter"];
    return items.filter((t) => t.n === null || t.n > 0).sort((a, b) => ORDER.indexOf(a.id) - ORDER.indexOf(b.id));
  }
  function updateCounts() {
    const d = state.data;
    const todo = todoItems().filter((t) => t.n);
    const c = $("#count-todo");
    c.textContent = state.loading ? "" : String(todo.length);
    c.className = todo.some((t) => t.warn) ? "warn" : "";
    $("#count-memories").textContent = state.loading ? "" : String(d.memories.length);
    $("#count-skills").textContent = state.loading ? "" : String(d.skills.length);
    const promptsOf = (kind) => d.prompts.filter((p) => p.image_prompt.kind === kind).length;
    $("#count-image").textContent = state.loading ? "" : String(promptsOf("image"));
    $("#count-slide").textContent = state.loading ? "" : String(promptsOf("slide"));
    $("#count-design").textContent = state.loading ? "" : String(d.designSystems?.length || 0);
    $("#count-crew").textContent = state.loading ? "" : String(crew.count());
    $("#count-routing").textContent = state.loading ? "" : String(routing.count());
    $("#count-usage").textContent = state.loading ? "" : String(usage.count());
    $("#count-sessions").textContent = state.loading ? "" : String(sessions.count());
    const t = d.tools;
    const toolCount = $("#count-tools");
    toolCount.textContent = state.loading || !t ? "" : String(t.servers.length + t.plugins.length);
    toolCount.className = t && (t.servers.some((x) => x.note) || pendingTool(t)) ? "warn" : "";
    syncExtraAgents();
    shell?.sync();
  }
  function render(resetDetail = true) {
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
    $("#title-count").textContent = typeof v.count === "function" && !state.loading ? v.count() : "";
    $("#subtitle").textContent = state.loading ? "불러오는 중…" : v.sub();
    $(".main > .page-head .page-back")?.remove(); // a screen that has a ← link draws it again
    $(".page-head .head-back")?.remove(); $(".page-head .head-aside")?.remove(); // TOOLS screens add them again via headExtras()
    $("#app").classList.toggle("v4-connect", ["tools", "git", "reviews"].includes(state.view));
    const table = $("#table");
    if (state.loading) {
      ["#controls", "#chips", "#stats"].forEach((s) => $(s).replaceChildren());
      table.replaceChildren(...Array.from({ length: 6 }, () => el("div", "skeleton")));
      $("#detail").replaceChildren();
      return;
    }
    if (state.view === "skills" && !state.versionsDone) void loadVersions(generation);
    const scroll = table.scrollTop;
    const page = pages.views[state.view] || crew.pages[state.view];
    $("#app").classList.toggle("wide", !!page && !page.keepDetail);
    $("#app").classList.toggle("keep-detail", !!page && !!page.keepDetail); // narrow windows: this detail sits under the list, not in a drawer
    $("#app").classList.toggle("graph-mode", state.view === "graph");
    $("#app").classList.toggle("git-mode", state.view === "git");
    $("#app").classList.toggle("memory-mode", state.view === "memories");
    $("#app").classList.toggle("knowledge-mode", state.view === "knowledge");
    $("#app").classList.toggle("skills-mode", state.view === "skills");
    $("#app").classList.toggle("gallery-mode", ["images", "design", "tokens"].includes(state.view));
    $("#app").classList.toggle("home-mode", state.view === "todo");
    table.className = { todo: "todo-wrap", images: "card-grid", crew: "card-grid crew-grid", settings: "page-wrap", design: "page-wrap", tokens: "page-wrap", graph: "page-wrap" }[state.view] || "table";
    renderHeadActions();
    renderChangeBar();
    if (page) {
      ["#chips", "#stats"].forEach((q) => $(q).replaceChildren());
      page.render();
      table.scrollTop = scroll;
      return;
    }
    if (state.view === "todo") renderTodo();
    if (state.view === "memories") renderMemories();
    if (state.view === "skills") renderSkills();
    if (state.view === "images") renderImages();
    if (state.view === "crew") crew.render();
    if (state.view === "design") renderLibrary();
    if (state.view === "tools") renderTools();
    if (state.view === "reviews") reviews.render();
    table.scrollTop = scroll;
    if (resetDetail || !state.selected) renderDetail();
  }
  // Title line extras (Figma v4): "← 저장소" above the title, the count after it, buttons at the right of the title row.
  function headExtras({ count = "", back = null, aside = [] } = {}) {
    $("#title-count").textContent = count;
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
    if (pageView) {
      box.replaceChildren(...pageView.head());
    } else if (state.view === "crew") {
      box.replaceChildren(...crew.head());
    } else if (state.view === "memories" && ["past", "reflections", "traits", "relations", "candidate", "conflicted", "idle", "digests", "merge"].includes(state.memTab)) {
      box.replaceChildren();
    } else if (state.view === "memories" && state.memTab === "originals") {
      box.replaceChildren(button("다시 불러오기", "", "arrows-clockwise", originals.reload));
    } else if (state.view === "memories") {
      box.replaceChildren(button("기억 추가", "primary", null, openMemoryAdd));
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
      box.replaceChildren(state.promptRun ? button("이 프롬프트로 만들기", "primary", null, (e) => runPrompt(e.currentTarget)) : button("프롬프트 추가", "primary", null, () => openPromptEditor(null)));
    } else if (state.view === "design") {
      box.replaceChildren(button("Arkism 토큰", "ghost", null, () => go("tokens")), button("디자인 시스템 추가", "primary", null, () => openSystemEditor(null)));
    } else if (state.view === "tools") {
      box.replaceChildren(button("다시 읽기", "ghost", null, refreshTools), state.toolTab === "plugin"
        ? button("플러그인 찾기", "primary", null, () => { $("#search").focus(); notice("이름이나 설명으로 찾아요. 새 플러그인은 Claude Code에서 /plugin 으로 설치해요."); })
        : button("MCP 서버 추가", "primary", null, openAddServer));
    } else if (state.view === "reviews") {
      box.replaceChildren(...reviews.head());
    } else if (state.view === "skills") {
      const trash = button("휴지통", "ghost", null, () => openSkillPage({ kind: "trash", tab: "trash" }));
      const install = button("GitHub에서 설치", "", null, () => openSkillPage({ kind: "install" }));
      const route = button("고르기 시험", "ghost", null, () => openSkillPage({ kind: "route" }));
      route.title = "할 일을 적으면 어떤 스킬이 뽑히는지 미리 봐요";
      refresh.classList.add("ghost");
      box.replaceChildren(...(state.skillPage ? [] : [refresh, route, trash, install]));
    } else box.replaceChildren(refresh);
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
  // cols: 글자 또는 [글자, 정렬키]. sort = { key, dir, pick(key) } 이면 정렬키가 있는 열이 눌린다.
  function header(cols, template, sort) {
    const h = el("div", "thead");
    h.style.gridTemplateColumns = template;
    cols.forEach((c) => {
      const [label, key] = Array.isArray(c) ? c : [c];
      if (!sort || !key) return h.append(el("span", "", label));
      const on = sort.key === key;
      const b = el("button", `th-sort${on ? " on" : ""}`, label);
      b.type = "button";
      const order = sort.dir > 0 ? "ascending" : "descending";
      b.setAttribute("aria-sort", on ? order : "none");
      if (on) b.append(el("i", "th-arrow", sort.dir > 0 ? "▲" : "▼"));
      b.addEventListener("click", () => sort.pick(key));
      h.append(b);
    });
    return h;
  }
  function row(id, template, cells, onClick) {
    const r = el("button", `row${state.selected === id ? " selected" : ""}`);
    r.type = "button";
    r.dataset.id = id;
    r.style.gridTemplateColumns = template;
    r.append(...cells);
    r.addEventListener("click", () => { state.selected = id; markSelected(); onClick(); openDetail(); });
    return r;
  }
  function markSelected() {
    document.querySelectorAll(".row[data-id], .cn-row[data-id], .todo-item[data-id], .prompt-card[data-id], .ds-card[data-id], .crew-card[data-id]").forEach((r) => r.classList.toggle("selected", r.dataset.id === state.selected));
  }

  // ---------------------------------------------------------------- 할 일 (Figma v4 · 01 홈: 198:200, 198:2178, 198:2264, 198:3812, 210:17783)
  // 홈 = 맨 위 그림 + 먼저 확인 3가지 + 메뉴별 확인할 것 + 이번 주. 줄을 고르면 그 일의 화면(← 홈)으로, 전체 항목 보기는 표 하나로.
  const TODO_SHORT = { review: "기억 후보", conflicted: "부딪힘", maintenance: "정리 제안", idle: "안 쓰임", fresh: "새로 저장",
    suggest: "새 스킬", similar: "비슷함", unused: "안 씀", updates: "업데이트", adapter: "어댑터 필요", blocked: "빠진 에이전트" };
  const TODO_MENUS = [["MEMORY", "기억", ["fresh", "review", "conflicted", "maintenance", "idle"], ["memories", "active"]],
    ["TOOLS", "스킬", ["suggest", "similar", "unused", "updates", "adapter", "blocked"], ["skills"]]];
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
    if (state.loadError && p.kind === "home") return "서버 연결이 끊겼어요";
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
    const lost = page.kind === "home" ? home.lostCard() : null;
    if (lost && !items.length) { wrap.replaceChildren(lost); return; }
    if (!items.length) { wrap.replaceChildren(homeEmpty()); return; }
    if (page.kind === "all") { wrap.replaceChildren(todoTable(items)); return; }
    const now = items.slice(0, 3), rest = items.slice(3);
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
    const box = el("div", "home-empty");
    box.append(el("strong", "", "지금은 확인할 일이 없어요"), el("p", "", "새 기억 후보나 스킬 제안이 생기면 여기에 모여요."));
    const row = el("div", "actions");
    row.append(button("기억 보기", "", null, () => go("memories", "active")), button("다시 불러오기", "ghost", null, loadAll));
    box.append(row);
    return box;
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
      const item = el("div", `todo-item${on ? " selected" : ""}`);
      item.dataset.id = id;
      const cell = el("div", "cell");
      const meta = el("div", `sub${warn ? " warn-text" : ""}`);
      if (t.id === "blocked" && t.agent) meta.append(agentLogo(...t.agent));
      meta.append(sub || "");
      cell.append(raw(el("div", "text", tr(title))), meta);
      item.append(el("span", "todo-num", String(++n)), cell, todoAct(t, id));
      item.addEventListener("click", () => pickTodo(id));
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
    const of = (m) => TODO_MENUS.filter(([menu]) => menu === m).map(([menu, name, ids, target]) => [menu, name, rest.filter((t) => ids.includes(t.id) && t.n), target]);
    const rows = of("MEMORY");
    digests.menuRows(rows); // 병합 제안 · 모아보기 (digests.js)
    rows.push(...home.menuExtras(generation), ...of("TOOLS")); // 워크플로우 · 라우팅, then 스킬 (TOOLS)
    const tools = state.data.tools;
    if (tools) {
      const login = tools.servers.filter((s) => Object.values(s.login || {}).includes("needed"));
      const noted = tools.servers.filter((s) => s.note);
      const parts = [...login.map((s) => ({ label: `${s.name} 로그인 필요`, n: 1 })), ...noted.map((s) => ({ label: `${s.name} 설정 확인`, n: 1 }))];
      if (pendingTool(tools)) parts.push({ label: "적용 대기", n: tools.requests.filter((q) => q.status === "pending").length });
      rows.push(["TOOLS", "MCP", parts, ["tools"]]);
    }
    let any = false;
    for (const [menu, name, parts, target] of rows) {
      const n = todoTotal(parts);
      if (!n) continue;
      any = true;
      const item = el("div", "todo-item");
      const text = parts.map((t) => (t.id ? `${TODO_SHORT[t.id] || t.label} ${t.n ?? "…"}` : t.label)).join(" · ");
      item.append(el("span", "home-menu", menu), el("span", "label", name), raw(el("span", "sub", tr(text))), el("output", "home-badge warn", String(n)), el("span", "home-chev", "›"));
      item.addEventListener("click", () => go(...target));
      list.append(item);
    }
    return any ? list : null;
  }
  // 이번 주: the server's usage summary for 7 days (GET /v1/usage/summary); hidden when the server has none.
  let week = { g: -1, doc: undefined };
  function weekCards() {
    if (week.g !== generation) {
      week = { g: generation, doc: undefined };
      const g = generation;
      void request(`/v1/usage/summary?${query({ range: "7d" })}`).then((d) => (d?.kpi ? d : null), () => null)
        .then((doc) => { if (week.g !== g) return; week.doc = doc; if (state.view === "todo" && !state.selected) render(false); });
    }
    const k = week.doc?.kpi;
    if (!k) return null;
    const f = usage.fmt;
    const mem = state.data.memories.length;
    const used = state.usageDone && mem ? `${Math.round(((mem - state.data.memories.filter(idle).length) / mem) * 100)}%` : "…";
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
    const items = todoItems(), now = items.slice(0, 3), rest = items.slice(3);
    const head = el("div", "home-sum-head");
    head.append(el("h2", "", now.length ? `확인할 일 ${now.length}개가 먼저 기다려요` : "지금은 확인할 일이 없어요"),
      el("p", "meta", rest.length ? `나머지 ${todoTotal(rest)}개는 메뉴별로 모아 두었어요.` : "새 일이 생기면 여기에 모여요."));
    const agents = shownAgents();
    const nums = el("div", "home-nums");
    for (const [label, value, warn] of [["먼저 확인", now.length, true], ["메뉴별 확인할 것", todoTotal(rest)], ["함께 일하는 에이전트", agents.length]]) {
      const r = el("div", "");
      r.append(el("span", "", label), el("output", warn && value ? "warn" : "", String(value)));
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
    body.append(head, ...(state.loadError ? [home.lostSide()] : []), nums, team, links, el("p", "meta", "각 줄을 누르면 해당 목록으로 가요. 승인·막기는 실행 전에 무엇이 바뀌는지 보여 줘요."));
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

  // ---------------------------------------------------------------- 기억
  // Figma v4 Brain › 기억 (198:630): 확인해 주세요 card → search + dropdowns → kind chips → list.
  // 확인할 것 (검토 대기·확인 필요·안 쓰임) and 모아보기 (작업 관계·사용자 특성·반성 교훈·과거 시점·대화) are sub-screens with "← 기억".
  const MEM_CHECK = [["candidate", "새로 저장"], ["conflicted", "헷갈림"], ["idle", "안 쓰임"], ["merge", "병합 제안"]]; // merge: digests.js
  const MEM_LOOK = [["digests", "주제 요약"], ["relations", "작업 관계"], ["traits", "사용자 특성"], ["reflections", "반성 교훈"], ["past", "과거 시점"], ["originals", "대화"]];
  const memPanels = () => ({ relations, traits, reflections, past: memoryTime, originals });
  function checkCounts() {
    const d = state.data;
    return { candidate: d.candidates.length, conflicted: d.conflicted.length, idle: state.usageDone ? d.memories.filter(idle).length : null, merge: digests.mergeCount() };
  }
  const checkTotal = () => Object.values(checkCounts()).reduce((a, n) => a + (n || 0), 0);
  // [title, one line under it, count next to the title]
  function memTitle() {
    const t = state.memTab;
    const check = { candidate: "AI가 대화에서 새로 뽑은 기억이에요. 저장할지 버릴지 정해 주세요.", conflicted: "이미 있는 기억과 다른 내용이 들어왔어요. 어느 쪽이 맞는지 골라 주세요.",
      idle: "답변에 안 쓰이거나 오래 안 불린 기억이에요. 아직 맞는지 확인해 주세요.", merge: "새로 들어온 기억이 이미 있는 기억과 비슷해요. AI 제안을 보고 저장할지, 합칠지, 건너뛸지 골라 주세요." }[t];
    if (check) return ["확인해 주세요", check, `${checkTotal()}개`];
    if (t === "digests") return ["모아보기", "비슷한 기억을 주제로 묶고 AI가 요약해요. 요약이 바뀌면 바로 쓰지 않고 먼저 확인을 받아요.", digests.title()];
    const look = { relations: "같은 작업에서 이어서 쓰인 스킬 순서예요. 관찰한 것일 뿐, 실행 허가는 아니에요.", traits: "대화에서 관찰한 사용자 성향이에요. 지시나 실행 승인으로 쓰지 않아요.",
      reflections: "작업하면서 얻은 해석이에요. 사실이나 실행 승인으로 쓰지 않아요.", past: "정한 시각에 쓰이던 기억을 그대로 보여 줘요. 지금 지시로는 쓰지 않아요.",
      originals: "에이전트와 나눈 대화예요. 7일 보관하고, 기억이 실제로 쓰이면 그때부터 다시 7일을 세요." }[t];
    if (look) return [MEM_LOOK.find(([k]) => k === t)[1], look, ""];
    const w = memUse();
    const rate = w?.injections ? Math.round((w.used / w.injections) * 100) : null;
    return ["기억", `AI가 답할 때 참고하는 내용이에요.${rate === null ? "" : ` 그중 ${rate}%가 실제 답변에 쓰였어요.`}`, `${state.data.memories.length}개`];
  }
  function memorySource() {
    const d = state.data;
    if (state.memTab === "candidate") return d.candidates;
    if (state.memTab === "conflicted") return d.conflicted;
    if (state.memTab === "idle") return d.memories.filter(idle);
    return d.memories;
  }
  // "← 기억" above the title on every sub-screen.
  function memBack() {
    let b = $("#mem-back");
    if (!b) {
      b = el("button", "mem-back"); b.id = "mem-back"; b.type = "button";
      b.append(icon("arrow-left"), "기억");
      b.addEventListener("click", () => go("memories", "active"));
      $(".main > .page-head").prepend(b);
    }
    b.hidden = state.memTab === "active";
  }
  // One badge per row: how often it is used, or why it needs a look.
  function memBadge(m) {
    const badge = (text, cls = "") => el("span", `mem-badge${cls ? ` ${cls}` : ""}`, text);
    if (standing(m)) return badge("항상 적용");
    if (state.usageDone && idle(m)) return badge("안 쓰임", "warn");
    const fresh = Date.now() - new Date(m.created_at).getTime() < DAY;
    const s = memStat(m.id);
    if (s) return s.injected ? badge(`${s.used}번 쓰임`) : badge(fresh ? "새 기억" : "—", fresh ? "new" : "");
    const u = usageOf(m.id);
    if (!u) return badge("…");
    if (!u.d30) return badge(fresh ? "새 기억" : "안 불림", fresh ? "new" : "");
    return badge(`${u.d30 >= 10 ? "10+" : u.d30}번 참고`);
  }
  function memCell(m, meta) {
    const cell = el("div", "cell mem-cell");
    cell.append(raw(el("div", "text", m.text)), el("div", "mem-meta", meta.filter(Boolean).join(" · ")));
    return cell;
  }
  // A select that reads "이름: 값 ▾" like the Figma dropdowns.
  function memSelect(label, options, value, onPick) {
    const s = el("select", "mem-select");
    s.setAttribute("aria-label", label);
    for (const [v, l] of options) { const o = el("option", "", l); o.value = v; o.selected = v === value; s.append(o); }
    s.addEventListener("change", () => onPick(s.value));
    return s;
  }
  function renderMemories() {
    memBack();
    if (state.memTab === "digests") { digests.draw(); return; }
    const panel = memPanels()[state.memTab];
    if (panel) {
      const seg = segment(MEM_LOOK, state.memTab, (k) => go("memories", k)); seg.classList.add("mem-seg");
      $("#controls").replaceChildren(seg);
      panel.draw(); return;
    }
    if (MEM_CHECK.some(([k]) => k === state.memTab)) { renderMemoryCheck(); return; }

    const source = memorySource();
    // 확인해 주세요: what is waiting, and one button that starts with the first non-empty list.
    const n = checkCounts(), total = checkTotal();
    const top = [];
    if (total) {
      const card = el("div", "mem-check");
      const txt = el("div", "mem-check-text");
      const line = el("p", "mem-check-title"); line.append("확인해 주세요", el("strong", "", String(total)));
      txt.append(line, el("p", "mem-check-sub", `새로 저장 ${n.candidate} · 내용이 헷갈림 ${n.conflicted} · 안 쓰이는 중 ${n.idle ?? "…"}`));
      if (n.merge) txt.lastChild.append(` · 병합 제안 ${n.merge}`);
      const first = MEM_CHECK.find(([k]) => n[k])?.[0] || "candidate";
      card.append(txt, button("하나씩 확인하기", "primary small", null, () => go("memories", first)));
      top.push(card);
    }
    const tools = el("div", "mem-tools");
    const search = el("label", "panel-search");
    const q = el("input"); q.type = "search"; q.placeholder = "기억 검색"; q.setAttribute("aria-label", "기억 검색"); q.value = $("#search").value;
    q.addEventListener("input", () => {
      $("#search").value = q.value; state.query = q.value.trim().toLowerCase();
      const at = q.selectionStart;
      render(false);
      const again = $(".panel-search input"); again?.focus(); again?.setSelectionRange(at, at);
    });
    search.append(icon("magnifying-glass"), q);
    tools.append(search);
    const repos = [...new Set(source.map(memRepo).filter(Boolean))].sort((a, b) => a.localeCompare(b));
    if (state.repo && state.repo !== "shared" && !repos.includes(state.repo)) state.repo = "";
    if (repos.length) {
      const shared = source.filter((m) => !memRepo(m)).length;
      tools.append(memSelect("저장소", [["", "저장소: 모두"], ["shared", `저장소: 공용 ${shared}`], ...repos.map((r) => [r, `저장소: ${r.split("/").pop()} ${source.filter((m) => memRepo(m) === r).length}`])],
        state.repo || "", (v) => { state.repo = v; render(false); }));
    }
    tools.append(memSelect("모아보기", [["", "모아보기: 전체"], ...MEM_LOOK.map(([k, l]) => [k, `모아보기: ${l}`])], "", (v) => { if (v) go("memories", v); }));
    tools.append(memSelect("정렬", [["recent", "최근 순"], ["used", "많이 불린 순"], ["old", "오래된 순"]], state.sort, (v) => { state.sort = v; render(false); }));
    tools.append(inject.button());
    $("#controls").replaceChildren(...top, tools);

    const counts = {};
    source.forEach((m) => { counts[m.kind] = (counts[m.kind] || 0) + 1; });
    const chips = [["all", "전체", source.length], ...Object.keys(KINDS).filter((k) => counts[k]).map((k) => [k, kindLabel(k), counts[k]])];
    $("#chips").replaceChildren(...chips.map(([k, label, num]) => {
      const c = el("button", `chip mem-kind${state.kind === k ? " active" : ""}`); c.type = "button";
      c.append(el("span", "", label), el("output", "", String(num)));
      c.addEventListener("click", () => { state.kind = k; render(false); });
      return c;
    }));
    $("#stats").replaceChildren();

    const repoOk = (m) => !state.repo || (state.repo === "shared" ? !memRepo(m) : memRepo(m) === state.repo);
    const items = source.filter((m) => (state.kind === "all" || m.kind === state.kind) && repoOk(m) && matches(`${m.text} ${m.summary || ""}`));
    const time = (m) => new Date(m.created_at).getTime();
    if (state.sort === "recent") items.sort((a, b) => time(b) - time(a));
    if (state.sort === "old") items.sort((a, b) => time(a) - time(b));
    if (state.sort === "used") items.sort((a, b) => (usageOf(b.id)?.d30 || 0) - (usageOf(a.id)?.d30 || 0));
    const tpl = "minmax(0,1fr) auto";
    const shown = memPage(items);
    $("#table").replaceChildren(...shown.map((m) => row(m.id, tpl, [memCell(m, [kindLabel(m.kind), originOf(m), ago(m.created_at), memRepo(m).split("/").pop()]), memBadge(m)], renderDetail)));
    if (!items.length) $("#table").append(el("div", "empty", state.query ? "검색과 맞는 기억이 없어요." : "여기에 표시할 기억이 없어요."));
    memMore(items.length, shown.length);
  }
  // "380개 중 50개 표시 · 더 보기" (Figma 198:630): long lists draw 50 rows at a time; a new filter starts over.
  const MEM_PAGE = 50;
  let memPaging = { key: "", n: MEM_PAGE };
  function memPage(items) {
    const key = [state.memTab, state.kind, state.repo, state.sort, state.query].join("|");
    if (memPaging.key !== key) memPaging = { key, n: MEM_PAGE };
    const n = Math.max(memPaging.n, items.findIndex((m) => m.id === state.selected) + 1);
    return items.slice(0, n);
  }
  function memMore(total, shown) {
    if (total <= shown) return;
    const foot = el("div", "mem-more");
    const more = el("button", "link-btn", "더 보기"); more.type = "button";
    more.addEventListener("click", () => { memPaging.n = shown + MEM_PAGE; render(false); });
    foot.append(el("span", "", `${total}개 중 ${shown}개 표시`), " · ", more);
    $("#table").append(foot);
  }
  // 확인해 주세요 (198:716 · 198:1748 · 198:3984): one 3-way segment, rows with a reason badge, ✓ ✕ on new ones.
  function renderMemoryCheck() {
    const d = state.data, n = checkCounts();
    const seg = segment(MEM_CHECK.map(([k, l]) => [k, l, n[k], true]), state.memTab, (k) => go("memories", k)); seg.classList.add("mem-seg");
    $("#controls").replaceChildren(seg);
    if (state.memTab === "merge") { digests.drawMerge(); return; }
    $("#stats").replaceChildren();
    const time = (m) => new Date(m.created_at).getTime();
    const items = memorySource().filter((m) => matches(`${m.text} ${m.summary || ""}`)).sort((a, b) => time(b) - time(a));
    const canDecide = !!state.caps?.actions?.review_decide;
    if (state.memTab === "candidate" && items.length) {
      const hint = el("div", "mem-hint");
      const p = el("p", "");
      p.append(`${items.length}개 · `, icon("check"), "저장 ", icon("x"), "버리기");
      hint.append(p);
      if (canDecide && d.candidates.length > 1) hint.append(button("여러 개 한 번에 처리", "small", null, () => { state.selected = "batch"; markSelected(); renderDetail(); openDetail(); }));
      const stale = canDecide ? staleCandidates() : [];
      if (stale.length) hint.append(button(`30일 지난 후보 정리 ${stale.length}`, "ghost small", null, (e) => expireStale(stale.length, e.currentTarget)));
      $("#chips").replaceChildren(hint);
    } else $("#chips").replaceChildren();
    const table = $("#table");
    const shown = memPage(items);
    if (state.memTab === "idle") {
      const w = memUse();
      table.replaceChildren(...shown.map((m) => {
        const s = memStat(m.id), often = unusedOften(m);
        const use = often && w ? `최근 ${w.days}일 ${s.injected}번 참고 · ${s.used}번 사용` : "30일 넘게 참고 안 됨";
        return row(m.id, "minmax(0,1fr) auto", [memCell(m, [kindLabel(m.kind), use]), el("span", `mem-badge${often ? " warn" : ""}`, often ? "안 쓰임" : "오래됨")], renderDetail);
      }));
    } else {
      const quickCol = state.memTab === "candidate" && canDecide;
      const tpl = quickCol ? "minmax(0,1fr) auto auto" : "minmax(0,1fr) auto";
      table.replaceChildren(...shown.map((c) => {
        const [why, warn] = waitReason(c);
        const cells = [memCell(c, [kindLabel(c.kind), withAgent(c), ago(c.created_at), memRepo(c).split("/").pop()]), el("span", `mem-badge${warn ? " warn" : ""}`, why.split(" — ")[0])];
        if (quickCol) {
          const quick = el("div", "quick");
          const ok = el("button", "icon-btn"); ok.type = "button"; ok.title = "저장"; ok.setAttribute("aria-label", "저장"); ok.append(icon("check"));
          ok.addEventListener("click", (e) => {
            e.stopPropagation();
            if (c.conflict_id) { state.selected = c.id; markSelected(); renderDetail(); openDetail(); return; }
            void decide(c, "approve", "");
          });
          const no = el("button", "icon-btn"); no.type = "button"; no.title = "버리기"; no.setAttribute("aria-label", "버리기"); no.append(icon("x"));
          no.addEventListener("click", (e) => {
            e.stopPropagation();
            state.selected = c.id; markSelected(); renderDetail(); openDetail(); $("#reject-reason")?.focus();
          });
          quick.append(ok, no);
          cells.push(quick);
        }
        return row(c.id, tpl, cells, renderDetail);
      }));
    }
    if (!items.length) table.append(el("div", "empty", state.query ? "검색과 맞는 기억이 없어요." : "확인할 기억이 없어요."));
    memMore(items.length, shown.length);
  }

  // 30일 지난 후보 정리: the server's own candidate expiry (POST /v1/memory_candidates/expire), at most 100 at a time.
  const STALE_DAYS = 30;
  function staleCandidates() {
    const before = Date.now() - STALE_DAYS * DAY;
    return state.data.candidates.filter((c) => !c.conflict_id && new Date(c.created_at).getTime() <= before && new Date(c.updated_at || c.created_at).getTime() <= before);
  }
  async function expireStale(n, trigger) {
    if (!confirm(`${STALE_DAYS}일 넘게 결정하지 않은 후보 ${n}개를 정리할까요?\n\n저장되지 않고 만료돼요. 에이전트는 쓰지 않아요.`)) return;
    trigger.disabled = true;
    try {
      const before = new Date(Date.now() - STALE_DAYS * DAY).toISOString();
      const r = await request("/v1/memory_candidates/expire", { method: "POST", body: JSON.stringify({ scope: state.scope, before, limit: 100 }) });
      const done = r?.expired?.length ?? 0;
      notice(done ? `후보 ${done}개를 정리했어요.` : "정리할 후보가 없었어요.");
      state.selected = null;
      await reloadMemories();
    } catch (e) { notice(e.message === "access_denied" ? "이 계정은 후보를 정리할 수 없어요." : `정리하지 못했어요 (${e.message}).`); trigger.disabled = false; }
  }
  async function reloadMemories() {
    const [candidates, conflicted, memories, maintenance] = await Promise.all([
      listAll("/v1/memory_candidates", { status: "candidate" }), listAll("/v1/memory_candidates", { status: "conflicted" }).catch(() => []), listAll("/v1/memories", { status: "active" }),
      loadMaintenance()
    ]);
    Object.assign(state.data, { candidates, conflicted, memories, maintenance });
    render();
  }
  // Conflict resolution and candidate review commit together on the server.
  const CONFLICT_REASON = "Arkism 앱에서 사용자가 선택";
  // One review action per item at a time: a second click on the same item would only meet the first one's new revision.
  const reviewing = new Set();
  async function reviewOnce(id, work) {
    if (reviewing.has(id)) return;
    reviewing.add(id);
    try { await work(); } catch (e) {
      notice(e.message === "revision_conflict" ? "이미 처리됐거나 다른 곳에서 바뀌었어요. 새로 불러왔어요." : `처리하지 못했어요: ${e.message}`);
      await reloadMemories().catch(() => {});
    } finally { reviewing.delete(id); }
  }
  function resolveConflict(c, choice) {
    return reviewOnce(c.id, async () => {
      const rel = await request(`/v1/memory_relations/${encodeURIComponent(c.conflict_id)}?${query()}`);
      const memory = rel.relations?.find((r) => r.collection === "memories");
      if (!memory) throw new Error("revision_conflict");
      const payload = { scope: state.scope, expected_revision: c.revision,
        action: choice === "keep_old" ? "reject" : "approve",
        ...(choice === "keep_old" ? { reason: CONFLICT_REASON } : {}),
        conflict: { id: rel.id, expected_revision: rel.revision, memory,
          resolution: { keep_old: "keep_old", both: "keep_both", new: "replace_old" }[choice], reason: CONFLICT_REASON } };
      await request(`/v1/memory_candidates/${encodeURIComponent(c.id)}/review`, {
        method: "POST", body: JSON.stringify(window.ArkismMemoryEvaluation.reviewPayload(c.id, payload)) });
      notice({ keep_old: "기존 기억을 그대로 뒀어요.", both: "둘 다 기억으로 남겼어요.", new: "새 내용으로 바꿨어요. 옛 기억은 이력에 남아요." }[choice]);
      state.selected = null;
      await reloadMemories();
    });
  }
  function decide(item, action, reason) {
    return reviewOnce(item.id, async () => {
      await request(`/v1/memory_candidates/${encodeURIComponent(item.id)}/review`, {
        method: "POST", body: JSON.stringify(window.ArkismMemoryEvaluation.reviewPayload(item.id, { scope: state.scope, expected_revision: item.revision, action, ...(reason ? { reason } : {}) }))
      });
      notice(action === "approve" ? "기억으로 저장했어요." : "후보를 버렸어요.");
      state.selected = null;
      await reloadMemories();
    });
  }

  // ---------------------------------------------------------------- 스킬
  const compatOf = (sk) => sk.skill_compatibility || {};
  // Only "unsupported" stops the router; no label means every agent gets the skill.
  const allowed = (sk, key) => compatOf(sk)[key] !== "unsupported";
  const blockedAny = (sk) => shownAgents().some(([k]) => !allowed(sk, k));
  const categoryFilter = (sk) => state.skillCat === "all" || (state.skillCat === "none" ? !pages.categoryOf(sk) : pages.categoryOf(sk)?.id === state.skillCat);
  // 최근 60일 사용 (skill.usage traces); null while the server has no usage API.
  const useOf = (sk) => state.data.skillUse?.get(sk.id) || null;
  const unused = (sk) => !!state.data.skillUse && sk.status !== "deprecated" && !useOf(sk);
  const fromPlugin = (sk) => /\/plugins?\//.test(sk.source_path || "");
  const SKILL_SORTS = [["used", "많이 쓴 순"], ["recent", "최근 쓴 순"], ["name", "이름 순"], ["updated", "최근 바뀐 순"], ["quality", "품질 순"], ["security", "보안 검사 순"], ["agents", "에이전트 많은 순"]];
  const SORT_DIR0 = { name: 1 }; // 나머지는 큰 값이 먼저
  const sortDirOf = (key) => SORT_DIR0[key] || -1;
  const skillDir = () => state.skillDir || sortDirOf(state.skillSort);
  function pickSkillSort(key) { // 같은 열을 다시 누르면 방향이 뒤집힌다
    state.skillDir = key === state.skillSort ? -skillDir() : 0;
    state.skillSort = key;
    store("arkism.skillSort", key); store("arkism.skillDir", String(state.skillDir));
    render(false);
  }
  function sortSkills(list) {
    const time = (v) => new Date(v || 0).getTime();
    const val = {
      used: (sk) => useOf(sk)?.count || 0,
      recent: (sk) => time(useOf(sk)?.last_used_at),
      name: (sk) => sk.name,
      updated: (sk) => time(sk.updated_at),
      quality: (sk) => skillChecks(sk)?.quality.score ?? -1,
      security: (sk) => ({ ok: 2, warn: 1, bad: 0 }[skillChecks(sk)?.security.cls] ?? -1),
      agents: (sk) => shownAgents().filter(([k]) => allowed(sk, k)).length,
    }[state.skillSort];
    if (!val) return list;
    const cmp = (a, b) => { const x = val(a), y = val(b); return typeof x === "string" ? x.localeCompare(y) : x - y; };
    return list.slice().sort((a, b) => cmp(a, b) * skillDir() || a.name.localeCompare(b.name));
  }
  // 경로만 다른 같은 이름 스킬(도구별 사본·플러그인 캐시)은 한 줄로 묶고 눌러서 펼친다.
  const COPY_PATH = /\/(codex|copilot|build|\.claude-plugin|\.agents|plugins?|cache|marketplaces)\//;
  function groupSkills(list) { // 대표 사본을 먼저 고른 뒤, 대표 기준으로 정렬한다
    const groups = new Map();
    for (const sk of list) groups.set(sk.name, [...(groups.get(sk.name) || []), sk]);
    const byRep = new Map();
    for (const g of groups.values()) {
      const rep = g.find((sk) => !COPY_PATH.test(sk.source_path || "")) || g[0];
      byRep.set(rep, [rep, ...sortSkills(g.filter((sk) => sk !== rep))]);
    }
    return sortSkills([...byRep.keys()]).map((rep) => byRep.get(rep));
  }
  // 원본 저장소에 새 버전이 있는 스킬 (서버가 6시간마다 git fetch로 확인한 목록, 폴더 이름과 저장소 경로가 모두 맞아야 한다)
  const skillFolder = (sk) => (sk.source_path || "").split("/").slice(-2, -1)[0] || sk.name; // 표시 이름이 아니라 SKILL.md가 든 폴더 이름
  const updatesOf = (sk) => (state.data.tools?.skill_updates || []).filter((u) => u.name === skillFolder(sk) && (sk.source_path || "").includes(`${(u.repo || "").replace(/^~/, "")}/`));
  function skillFilter(sk) {
    if (state.skillModel && allowed(sk, state.skillModel.replace("!", "")) === state.skillModel.startsWith("!")) return false; // "!key" = 못 쓰는 스킬
    if (state.skillTab === "trash") return sk.status === "deprecated";
    if (sk.status === "deprecated") return false;
    if (state.skillTab === "unused") return unused(sk);
    if (state.skillTab === "plugin") return fromPlugin(sk);
    if (state.skillTab === "update") return updatesOf(sk).length > 0;
    if (state.skillTab === "missing") return blockedAny(sk);
    if (state.skillTab === "adapter") return Object.values(compatOf(sk)).includes("requires_adapter");
    if (state.skillTab === "caution") return ["warn", "bad"].includes(skillChecks(sk)?.security.cls);
    return true;
  }
  function stage(skillId, key, want) {
    const sk = state.data.skills.find((x) => x.id === skillId);
    const k = `${skillId}|${key}`;
    if (state.pending.has(k) || (want === "allow") === allowed(sk, key)) state.pending.delete(k);
    else state.pending.set(k, want);
    render(false);
    if (state.selected === `skill:${skillId}`) renderDetail();
  }
  // 품질·보안 come from Arkism's built-in check (always) plus, where installed, NVIDIA SkillEvaluator
  // (quality-check, 0–100 and A–F) and SkillSpector (--no-llm: SAFE / CAUTION / DO_NOT_INSTALL),
  // which arkism-skill-checks.timer runs on the host's
  // skill folders and arkism-agents.timer reports with the agent tools. A skill no scan covers yet
  // falls back to what Arkism records for the active version: how complete the analysis is, and
  // what the import inspection found.
  const QUALITY_PARTS = [["problem", "풀려는 문제"], ["use_when", "이럴 때 써요"], ["avoid_when", "이럴 땐 쓰지 마세요"], ["output_format", "결과 형식"], ["success_criteria", "성공 기준"], ["verification", "확인 방법"]];
  const filled = (v) => (Array.isArray(v) ? v.length > 0 : !!(v && String(v).trim()));
  const VERDICTS = { SAFE: 0, CAUTION: 1, DO_NOT_INSTALL: 2 };
  const SECURITY = [{ label: "통과", cls: "ok" }, { label: "주의", cls: "warn" }, { label: "막음", cls: "bad" }];
  const gradeCls = (score) => (score >= 75 ? "ok" : score >= 40 ? "warn" : "bad");
  // Every agent folder holding a skill of this name; the scan with the worst verdict speaks for the skill.
  function hostChecks(sk) {
    const all = (state.data.tools?.skill_checks || []).filter((c) => c.name === sk.name);
    return all.sort((a, b) => (VERDICTS[b.verdict] ?? -1) - (VERDICTS[a.verdict] ?? -1) || (a.quality_score ?? 101) - (b.quality_score ?? 101));
  }
  function derivedChecks(v) {
    const a = v?.analysis;
    if (!a) return { quality: { score: null, grade: "—", cls: "off", missing: [] }, security: { label: "확인 못 함", cls: "off", reasons: ["분석 기록이 없어요"] } };
    const missing = QUALITY_PARTS.filter(([k]) => !filled(a[k])).map(([, l]) => l);
    const score = Math.round(((QUALITY_PARTS.length - missing.length) / QUALITY_PARTS.length) * 100);
    const grade = score >= 90 ? "A" : score >= 75 ? "B" : score >= 60 ? "C" : score >= 40 ? "D" : "F";
    const reasons = [];
    let level = 0;
    if (v.inspection?.redacted) { reasons.push("본문에서 비밀값을 찾아 가렸어요"); level = 2; }
    if (v.inspection?.personal_data) { reasons.push("개인정보가 들어 있어요"); level = Math.max(level, 1); }
    if (a.irreversible_actions?.length) { reasons.push(`되돌릴 수 없는 동작: ${a.irreversible_actions.join(", ")}`); level = Math.max(level, 1); }
    if (a.external_transfer) { reasons.push("밖으로 자료를 보내요"); level = Math.max(level, 1); }
    if (a.secret_names?.length) { reasons.push(`비밀값이 필요해요: ${a.secret_names.join(", ")}`); level = Math.max(level, 1); }
    return { quality: { score, grade, cls: gradeCls(score), missing }, security: { ...SECURITY[level], reasons } };
  }
  function skillChecks(sk) { // null while nothing is known yet
    const v = state.versions.get(sk.version_id);
    const host = hostChecks(sk);
    if (v === undefined && !host.length) return null;
    const derived = derivedChecks(v);
    const out = { ...derived, validated: !!v?.validation_id, source: "app", scans: host };
    const q = host.find((c) => typeof c.quality_score === "number");
    if (q) out.quality = { score: Math.round(q.quality_score), grade: q.quality_grade || "—", cls: gradeCls(q.quality_score), missing: derived.quality.missing };
    const s = host.find((c) => c.verdict in VERDICTS);
    if (s) {
      const reasons = s.top_findings?.length ? [...s.top_findings] : [];
      if (s.findings > reasons.length) reasons.push(`발견 ${s.findings}건`);
      out.security = { ...SECURITY[VERDICTS[s.verdict]], reasons, risk: s.risk_score };
    }
    if (q || s) out.source = "host";
    return out;
  }
  // v4 · 스킬 (Figma 198:802): 확인할 것 카드 → 검색·상태·정렬 → 분류 → 목록, 오른쪽은 고른 스킬.
  // state.skillPage opens a page inside the same view: 개선하기 (4 steps), 휴지통·중복, GitHub에서 설치.
  const SKILL_STATES = [["all", "전체"], ["missing", "빠진 곳 있음"], ["unused", "안 씀"], ["adapter", "어댑터 필요"], ["caution", "보안 주의"], ["update", "업데이트 있음"], ["plugin", "플러그인 스킬"], ["trash", "휴지통"]];
  const IMPROVE_STEPS = [["개선안", "무엇을 고칠까요?", "관련 교훈을 찾아 고를 관측을 선택하고, 평가 과제 파일을 넣어 주세요."],
    ["비교 평가", "기존 버전과 비교해요", "같은 평가 과제로 두 버전을 돌려 점수·비용·시간을 비교해요."],
    ["검토·승인", "무엇이 바뀌었나요", "후보 버전의 원문과 평가를 보고 승인하거나 제외해요."],
    ["시험 적용", "실제 작업에 시험 적용해요", "정한 프로그램에서 일정 기간 새 버전을 써 보고 결과를 모아요."]];
  let skillPageCache = null; // { key, main, side }: a page with forms survives the 10-second refresh
  const skillPageKey = () => JSON.stringify(state.skillPage);
  const liveSkills = () => state.data.skills.filter((sk) => sk.status !== "deprecated" && !trashed().some((x) => x.id === sk.id));
  function openSkillPage(page) {
    state.skillPage = page;
    if (page.id) state.selected = `skill:${page.id}`; else state.selected = null;
    skillPageCache = null;
    $("#table").scrollTop = 0;
    render();
  }
  const skBadge = (text, cls = "") => el("span", `v4s-badge ${cls}`.trim(), text);
  function skillBadge(sk) {
    const c = skillChecks(sk);
    if (["warn", "bad"].includes(c?.security.cls)) return skBadge(`보안 ${c.security.label}`, "amber");
    const off = shownAgents().find(([k]) => !allowed(sk, k));
    if (off) { const b = skBadge(`${off[1]}에 없음`, "amber has-dot"); return b; }
    if (updatesOf(sk).length) return skBadge("업데이트 있음");
    if (Object.values(compatOf(sk)).includes("requires_adapter")) return skBadge("어댑터 필요");
    return null;
  }
  function agentDots(sk) {
    const box = el("span", "v4s-agents");
    for (const [k, n, logo] of shownAgents()) if (allowed(sk, k)) box.append(agentLogo(logo, n));
    return box;
  }
  function skSelect(label, options, current, onPick) {
    const s = el("select", "v4s-select");
    s.setAttribute("aria-label", label);
    for (const [k, l] of options) { const o = el("option", "", l); o.value = k; o.selected = k === current; s.append(o); }
    s.addEventListener("change", () => onPick(s.value));
    return s;
  }
  function panelSearch(placeholder) {
    const box = el("label", "v4s-search");
    const input = el("input"); input.type = "search"; input.placeholder = placeholder; input.value = $("#search").value; input.setAttribute("aria-label", placeholder);
    input.addEventListener("input", () => { $("#search").value = input.value; state.query = input.value.trim().toLowerCase(); state.skillLimit = 0; render(false); });
    box.append(icon("magnifying-glass"), input);
    return box;
  }
  function keepFocus(sel) {
    const was = document.activeElement?.closest?.(sel);
    return () => { if (!was) return; const n = $(`${sel} input`); if (n) { n.focus(); n.setSelectionRange(n.value.length, n.value.length); } };
  }
  function renderSkills() {
    if (state.skillPage) return renderSkillPage();
    const kept = state.data.skills.filter((sk) => !trashed().some((x) => x.id === sk.id));
    const skills = [...kept, ...trashed()];
    const live = skills.filter((sk) => sk.status !== "deprecated");
    const versionsKnown = state.versionsDone;
    const n = {
      missing: skills.filter(blockedAny).length, unused: state.data.skillUse ? skills.filter(unused).length : null,
      adapter: live.filter((sk) => Object.values(compatOf(sk)).includes("requires_adapter")).length,
      caution: versionsKnown ? live.filter((sk) => ["warn", "bad"].includes(skillChecks(sk)?.security.cls)).length : null,
      update: live.filter((sk) => updatesOf(sk).length).length, plugin: skills.filter(fromPlugin).length, trash: skills.length - live.length
    };
    $("#title-count").textContent = `${live.length}개`;
    const restore = keepFocus(".v4s-search");
    // 확인해 주세요: what needs a look, the first kind with any opens with 하나씩 확인하기.
    const parts = [];
    const todo = [["caution", "보안 주의"], ["update", "업데이트"], ["adapter", "어댑터 필요"]].filter(([k]) => n[k]);
    if (state.skillTab === "all" && todo.length) {
      const card = el("div", "v4s-attn");
      const text = el("div", "v4s-attn-text");
      const line = el("div", "v4s-attn-line"); line.append(el("strong", "", "확인해 주세요"), el("output", "", String(todo.reduce((a, [k]) => a + n[k], 0))));
      text.append(line, el("p", "", todo.map(([k, l]) => `${l} ${n[k]}`).join(" · ")));
      card.append(text, button("하나씩 확인하기", "primary", null, () => go("skills", todo[0][0])));
      parts.push(card);
    }
    const bar = el("div", "v4s-bar");
    const states = SKILL_STATES.filter(([k]) => k === "all" || k === state.skillTab || n[k]).map(([k, l]) => [k, `상태: ${l}${k === "all" ? "" : ` ${n[k] ?? "…"}`}`]);
    const modelKeys = shownAgents().map(([k]) => k);
    if (state.skillModel && !modelKeys.includes(state.skillModel.replace("!", ""))) state.skillModel = "";
    const models = [["", "모든 에이전트"], ...modelKeys.flatMap((k) => [[k, `${agentName(k)} 쓰는 스킬 ${live.filter((sk) => allowed(sk, k)).length}`], [`!${k}`, `${agentName(k)} 못 쓰는 스킬 ${live.filter((sk) => !allowed(sk, k)).length}`]])];
    const dir = el("button", "v4s-dir", skillDir() > 0 ? "↑" : "↓"); dir.type = "button"; dir.title = "정렬 방향 바꾸기"; dir.setAttribute("aria-label", dir.title);
    dir.addEventListener("click", () => pickSkillSort(state.skillSort));
    bar.append(panelSearch("스킬 검색"), skSelect("상태", states, state.skillTab, (k) => go("skills", k)), skSelect("정렬", SKILL_SORTS, state.skillSort, pickSkillSort), dir,
      skSelect("에이전트", models, state.skillModel, (k) => { state.skillModel = k; render(false); }));
    $("#controls").replaceChildren(...parts, bar);
    // 분류 chips: 전체 + the owner's categories (설정 › 카테고리).
    const cats = pages.prefs.categories;
    const source = state.skillTab === "trash" ? skills.filter((sk) => sk.status === "deprecated") : live;
    const chip = (id, label, count) => {
      const c = el("button", `chip${state.skillCat === id ? " active" : ""}`); c.type = "button";
      c.append(label, el("output", "", String(count)));
      c.addEventListener("click", () => { state.skillCat = id; state.skillLimit = 0; render(false); });
      return c;
    };
    const none = source.filter((sk) => !pages.categoryOf(sk)).length;
    $("#chips").replaceChildren(...(cats.length ? [chip("all", "전체", source.length), ...cats.map((c) => chip(c.id, c.name, source.filter((sk) => pages.categoryOf(sk)?.id === c.id).length)), ...(none ? [chip("none", "분류 안 됨", none)] : [])] : []));
    $("#chips").classList.add("v4s-cats");
    $("#stats").replaceChildren();
    const items = skills.filter((sk) => skillFilter(sk) && categoryFilter(sk) && matches(`${sk.name} ${sk.id} ${sk.description || ""}`));
    const groups = groupSkills(items);
    const limit = state.skillLimit || 50;
    if (!state.selected && groups[0] && window.matchMedia("(min-width: 1100px)").matches) state.selected = `skill:${groups[0][0].id}`;
    const list = el("div", "v4s-list");
    for (const copies of groups.slice(0, limit)) {
      const open = state.openCopies.has(copies[0].name);
      (open ? copies : copies.slice(0, 1)).forEach((sk, i) => {
        const cell = el("div", "v4s-text");
        // 이름이 제목, 설명은 그 아래 작은 줄 (분류는 앞에 그대로).
        cell.append(raw(el("div", "v4s-title v4s-name", sk.name)));
        const meta = el("div", "v4s-meta");
        const cat = pages.categoryOf(sk)?.name;
        if (cat) meta.append(el("span", "v4s-cat", cat));
        if (sk.description) meta.append(raw(el("span", "v4s-desc", firstSentence(sk.description))));
        if (!i && copies.length > 1) {
          const c = el("button", `copy-chip${open ? " open" : ""}`, `같은 이름 ${copies.length}개`); c.type = "button";
          c.title = open ? "접기" : "경로가 다른 같은 이름 스킬을 펼쳐요";
          c.addEventListener("click", (e) => { e.stopPropagation(); state.openCopies[open ? "delete" : "add"](sk.name); render(false); });
          meta.append(c);
        }
        if (i) meta.append(el("span", "mono copy-path", sk.source_path || ""));
        cell.append(meta);
        const u = useOf(sk);
        const uses = el("span", "v4s-uses", !state.data.skillUse ? "…" : u ? `${u.count}번` : "안 씀");
        const side = [skillBadge(sk), agentDots(sk), uses].filter(Boolean);
        const r = row(`skill:${sk.id}`, "", [cell, ...side], renderDetail);
        r.classList.add("v4s-row");
        if (i) r.classList.add("copy");
        list.append(r);
      });
    }
    const table = $("#table");
    table.className = "v4s-wrap";
    table.replaceChildren(list);
    if (!items.length) list.append(el("div", "empty", state.query ? "검색과 맞는 스킬이 없어요." : "조건에 맞는 스킬이 없어요."));
    if (groups.length > limit) {
      const more = el("p", "v4s-foot", `${groups.length}개 중 ${limit}개 표시 · `);
      const b = el("button", "link-btn", "더 보기"); b.type = "button";
      b.addEventListener("click", () => { state.skillLimit = limit + 50; render(false); });
      more.append(b); table.append(more);
    }
    restore();
  }

  // ---------------------------------------------------------------- 스킬 상세 (v4)
  function skFacts(rows, wide) {
    const dl = el("dl", `v4s-facts${wide ? " wide" : ""}`);
    for (const [k, v, cls] of rows) { if (!v) continue; const dd = el("dd", cls || ""); dd.append(v); dl.append(el("dt", "", k), dd); }
    return dl;
  }
  function skSection(title, aside) {
    const s = el("section", "v4s-sec");
    const h = el("div", "v4s-sec-head"); h.append(el("h3", "", title));
    if (aside) h.append(el("span", "mono", aside));
    s.append(h);
    return s;
  }
  // 쓰는 에이전트: a chip per agent; clicking stages add/remove, the card under it applies or cancels.
  function skillAgentsSection(sk) {
    const shown = shownAgents();
    const sec = skSection("쓰는 에이전트", `${shown.filter(([k]) => allowed(sk, k)).length} / ${shown.length}`);
    const grid = el("div", "v4s-agent-chips");
    for (const [k, n, logo] of shown) {
      const staged = state.pending.get(`${sk.id}|${k}`);
      const on = allowed(sk, k);
      const a = el("button", `v4s-agent${on ? " on" : " off"}${staged ? " staged" : ""}`); a.type = "button";
      a.append(agentLogo(logo, n), el("span", "", staged ? n : n + agentNote(sk, k)));
      a.title = `${staged ? "누르면 예정을 취소해요" : on ? `${n}에서 써요 · 누르면 뺄 예정` : `${n}에서 못 써요 · 누르면 추가 예정`} · ${pcLink(k).title}`;
      a.addEventListener("click", () => toggleCell(sk, k));
      grid.append(a);
    }
    sec.append(grid);
    const mine = [...state.pending.entries()].filter(([key]) => key.startsWith(`${sk.id}|`));
    if (mine.length) {
      const names = (want) => mine.filter(([, w]) => w === want).map(([key]) => agentName(key.split("|")[1])).join("·");
      const ask = [names("allow") && `${names("allow")}에 추가`, names("block") && `${names("block")}에서 빼기`].filter(Boolean).join(", ");
      const card = el("div", "v4s-confirm");
      card.append(el("p", "", `${ask}할까요?`),
        button("취소", "ghost", null, () => { mine.forEach(([key]) => state.pending.delete(key)); render(false); renderDetail(); }),
        button("적용", "primary", null, applyChanges));
      sec.append(card);
    }
    return sec;
  }
  async function restoreSkill(sk) {
    try {
      await setSkillStatus(sk, "active");
      writeKey(TRASH_KEY, readKey(TRASH_KEY, []).filter((x) => x.id !== sk.id));
      state.data.skills = await listAll("/v1/skills", {}, 2000);
      state.data.skillTrash = await loadTrash();
      state.selected = null;
      notice(`${sk.name}를 되돌렸어요.`);
      render(false);
      renderDetail();
    } catch (e) { notice(`되돌리지 못했어요 (${e.message}).`); }
  }
  async function skillDetailV4(sk) {
    const cat = pages.categoryOf(sk)?.name || "분류 안 됨";
    const body = detailShell(cat);
    const top = el("div", "v4s-head");
    top.append(raw(el("h2", "v4s-name", sk.name)));
    if (sk.description) top.append(raw(el("p", "v4s-lead", sk.description)));
    const explain = el("div", "");
    const aiBtn = el("button", "link-btn", "AI로 쉽게 설명"); aiBtn.type = "button";
    aiBtn.addEventListener("click", async () => {
      aiBtn.disabled = true;
      explain.replaceChildren(el("p", "dim", "AI가 쉬운 말로 풀어 쓰는 중이에요…"));
      try {
        const text = await crew.explain(`스킬 이름: ${sk.name}\n설명: ${sk.description || ""}\n\n이 스킬이 무엇을 언제 하는지 쉬운 한국어 두세 문장으로 설명해 줘.`);
        explain.replaceChildren(Object.assign(el("div", "quote"), { textContent: text }));
      } catch (e) { explain.replaceChildren(el("p", "warn-text", e.message)); } finally { aiBtn.disabled = false; }
    });
    const c = skillChecks(sk), u = useOf(sk);
    const chips = el("div", "v4s-chips");
    if (c) chips.append(el("span", `v4s-pill${c.quality.cls === "ok" ? "" : " amber"}`, `품질 ${c.quality.grade}${c.quality.score === null ? "" : ` · ${c.quality.score}`}`), el("span", `v4s-pill${c.security.cls === "ok" ? "" : " amber"}`, `보안 ${c.security.label}`));
    if (state.data.skillUse) chips.append(el("span", "v4s-pill muted", u ? `${u.count}번 사용 · ${ago(u.last_used_at)}` : "최근 60일 안 씀"));
    top.append(chips, aiBtn, explain);
    const parts = [top];
    if (sk.status === "deprecated") {
      const card = el("div", "v4s-attn small");
      const t = el("div", "v4s-attn-text"); t.append(el("strong", "", "휴지통에 있어요"), el("p", "", "에이전트가 읽지 않아요. 되돌리면 바로 다시 받아요."));
      card.append(t, button("되돌리기", "primary", null, () => restoreSkill(sk)));
      parts.push(card);
    } else parts.push(skillAgentsSection(sk));
    const when = skSection("언제 쓰나요");
    const whenRows = el("div", ""); when.append(whenRows, el("p", "meta", "불러오는 중…"));
    parts.push(when);
    const crewSec = crew.skillExperts(sk);
    if (crewSec) parts.push(crewSec);
    const r = repoOf(sk), ups = updatesOf(sk);
    const ver = (v) => (/^\d/.test(v || "") ? `v${v}` : v || "");
    const src = skSection("출처와 업데이트");
    const version = sk.version ? `${ver(sk.version)}${ups.length ? ` → ${ver(ups[0].to)} 있음` : r ? (r.behind ? ` · 새 커밋 ${r.behind}개` : " · 최신") : ""}` : "";
    src.append(skFacts([["출처", r ? (r.remote || r.repo) : "", "mono"], ["버전", version, "mono"], ["위치", (sk.source_path || "").replace(/\/SKILL\.md$/, ""), "mono"], ["업데이트", r ? `마지막 확인 ${ago(r.checked_at)}` : ""]]));
    parts.push(src);
    const act = el("div", "v4s-actions");
    if (sk.status !== "deprecated") {
      act.append(button("AI로 개선", "", null, () => openSkillPage({ kind: "improve", id: sk.id, step: 0 })));
      if (ups.length) act.append(button("업데이트 받기", "", null, () => updateSkills(ups)));
      else if (r) {
        const checking = (state.data.tools?.requests || []).some((q) => q.kind === "skill_check" && q.name === r.repo && q.status === "pending");
        const b = button(checking ? "확인 중…" : "업데이트 확인", "", null, () => checkSkillRepo(r)); b.disabled = checking; act.append(b);
      }
      act.append(button("휴지통으로", "ghost", null, () => confirmBatch([sk], () => trashSkills([sk]))));
      parts.push(act);
      const hist = el("button", "v4s-history", `버전 이력${sk.version_history?.length ? ` ${sk.version_history.length}건` : ""} ›`); hist.type = "button";
      hist.addEventListener("click", () => openSkillPage({ kind: "improve", id: sk.id, step: 2 }));
      parts.push(hist);
    }
    // 품질·보안 검사와 에이전트별 사용 기록은 접어 둔다.
    const more = el("details", "v4s-more");
    more.append(el("summary", "", "검사·사용 자세히"), checksSection(sk));
    if (u) { const s = section("최근 60일 사용", `마지막 ${ago(u.last_used_at)}`); s.append(facts(Object.entries(u.by_runtime || {}).sort((a, b) => b[1] - a[1]).map(([rt, n]) => [agentName(rt) || rt, `${n}번`]))); more.append(s); }
    const pcs = skSection("에이전트 PC 연결");
    const pcRow = el("div", "v4s-chips");
    for (const [k, n] of shownAgents()) { const t = el("span", "v4s-pc"); t.append(pcLink(k), n); pcRow.append(t); }
    pcs.append(pcRow, pcLegend());
    more.append(pcs);
    parts.push(more);
    body.append(...parts);
    const g = generation;
    const v = await request(`/v1/skill_versions/${encodeURIComponent(sk.version_id)}?${query()}`).catch(() => null);
    if (g !== generation || state.selected !== `skill:${sk.id}` || !when.isConnected) return;
    const a = v?.analysis || {};
    const join = (x) => (Array.isArray(x) ? x.join(" · ") : x || "");
    const rows = [["이럴 때", join(a.use_when)], ["쓰지 마세요", join(a.avoid_when)], ["쓰는 도구", join(a.tools)]];
    when.replaceChildren(when.firstChild, ...(rows.some(([, x]) => x) ? [skFacts(rows, true)] : [el("p", "meta", "스킬 설명에 쓰는 때가 적혀 있지 않아요.")]));
  }

  // ---------------------------------------------------------------- 스킬 안의 페이지: 개선하기 · 휴지통·중복 · GitHub에서 설치
  function renderSkillPage() {
    const p = state.skillPage;
    ["#controls", "#chips", "#stats"].forEach((q) => $(q).replaceChildren());
    $("#chips").classList.remove("v4s-cats");
    const table = $("#table");
    table.className = "v4s-wrap v4s-page";
    pageBack("스킬", () => go("skills"));
    const key = skillPageKey();
    const fresh = !skillPageCache || skillPageCache.key !== key;
    if (p.kind === "improve") {
      const sk = state.data.skills.find((x) => x.id === p.id);
      if (!sk) { state.skillPage = null; return renderSkills(); }
      if (fresh) skillPageCache = { key, main: improveMain(sk, p.step) };
    } else if (p.kind === "install") {
      if (fresh) { const side = el("div", "v4s-side"); skillPageCache = { key, side, main: catalog.installPage(side, () => skillPageCache?.key === key && state.view === "skills") }; }
    } else if (p.kind === "trash") {
      skillPageCache = { key, main: trashMain(p) };
    } else if (p.kind === "route") {
      if (fresh) { const side = el("div", "v4s-side"); skillPageCache = { key, side, main: routeMain(side) }; }
    }
    table.replaceChildren(skillPageCache.main);
  }
  function improveMain(sk, step) {
    const wrap = el("div", "v4s-improve");
    const steps = el("ol", "v4s-steps");
    IMPROVE_STEPS.forEach(([label], i) => {
      const li = el("li", i < step ? "done" : i === step ? "current" : "");
      const b = el("button", "", ""); b.type = "button";
      b.append(el("i", "", i < step ? "✓" : String(i + 1)), el("span", "", label));
      if (i === step) b.setAttribute("aria-current", "step");
      b.addEventListener("click", () => { if (i !== step) openSkillPage({ kind: "improve", id: sk.id, step: i }); });
      li.append(b);
      steps.append(li);
    });
    const [, title, lead] = IMPROVE_STEPS[step];
    const card = el("section", "v4s-card");
    card.append(el("h3", "", title), el("p", "v4s-card-lead", lead));
    const part = step === 0 ? skillGenerate(sk) : skillReview(sk, ["compare", "review", "trial"][step - 1]);
    part.open = true;
    card.append(part);
    const next = el("div", "v4s-actions");
    if (step > 0) next.append(button(`← ${IMPROVE_STEPS[step - 1][0]}`, "ghost", null, () => openSkillPage({ kind: "improve", id: sk.id, step: step - 1 })));
    if (step < 3) next.append(button(`다음: ${IMPROVE_STEPS[step + 1][0]}`, "", null, () => openSkillPage({ kind: "improve", id: sk.id, step: step + 1 })));
    wrap.append(steps, card, next);
    return wrap;
  }
  const IMPROVE_SIDE = [
    ["개선 목표", "이만큼 좋아지면 통과예요", "필요한 점수 향상·비용 상한·시간 상한을 정해요. 아직 검토하지 않은 교훈은 독립 관측이 3개 이상일 때만 써요.", "생성 결과는 ‘검토·승인’에서 확인해요."],
    ["지금 상태", "평가 서버에서 비교해요", "앱의 API 연결과 같은 평가 서버에서, 등록된 고정 과제와 실행 조건만 써요. 실행만으로 승인·적용되지는 않아요.", "저장된 평가로 다음 단계에서 후보를 검토해요."],
    ["결정", "후보 버전을 승인할까요?", "다른 사람이 만든 통과 평가가 있어야 승인할 수 있어요. 승인한 뒤 ‘이 버전 적용’으로 바꿔요.", "관리 계정에서만 쓸 수 있어요."],
    ["시험 상태", "정한 기간만 시험해요", "기본 버전은 그대로 두고, 고른 프로그램에서만 승인된 후보를 써요. 끝나는 시각이 지나면 기본 버전으로 돌아가요.", "연결이 끊겨도 요청은 보관돼요. 다시 연결한 뒤 상태를 확인하세요."]];
  function skillPageDetail() {
    const p = state.skillPage;
    if (p.kind === "install") { const body = detailShell("GitHub에서 설치"); body.append(skillPageCache?.side || ""); return; }
    if (p.kind === "route") {
      const picked = state.data.skills.find((x) => `skill:${x.id}` === state.selected);
      if (picked) return skillDetailV4(picked);
      const body = detailShell("고르기 시험"); body.append(skillPageCache?.side || ""); return;
    }
    if (p.kind === "trash") {
      const s = [...trashed(), ...state.data.skills].find((x) => `skill:${x.id}` === state.selected);
      if (s) return skillDetailV4(s);
      if (p.tab === "copies") return copiesDetail();
      const body = detailShell("휴지통");
      body.append(el("h2", "", "휴지통에 있는 스킬은 에이전트가 읽지 않아요"), el("p", "v4s-lead", "되돌리면 바로 다시 받아요. 60일 동안 한 번도 쓰이지 않은 스킬은 ‘안 씀’에서 한꺼번에 옮길 수 있어요."));
      return;
    }
    const sk = state.data.skills.find((x) => x.id === p.id);
    const [label, title, text, foot] = IMPROVE_SIDE[p.step];
    const body = detailShell(label);
    body.append(el("h2", "", title), el("p", "v4s-lead", text));
    const s = skSection("이 스킬");
    if (sk) s.append(skFacts([["이름", sk.name, "mono"], ["버전", sk.version || "", "mono"], ["위치", (sk.source_path || "").replace(/\/SKILL\.md$/, ""), "mono"]]));
    body.append(s, el("p", "meta", foot));
  }
  // 스킬 고르기 시험: POST /v1/skills/route — the same search + judge an agent's skill_search uses, nothing runs.
  function routeMain(side) {
    const wrap = el("div", "v4s-route");
    const card = el("section", "v4s-card");
    const ask = el("textarea", "v4s-route-ask"); ask.rows = 3; ask.placeholder = "예: 이 PR 바뀐 부분 리뷰해 줘"; ask.setAttribute("aria-label", "할 일");
    const agents = skSelect("에이전트", [["", "모든 에이전트"], ...shownAgents().map(([k, n]) => [k, n])], "", () => {});
    const go = button("어떤 스킬을 고를지 보기", "primary small", null, run);
    const bar = el("div", "v4s-actions"); bar.append(agents, go);
    card.append(el("h3", "", "에이전트에게 할 말"), el("p", "v4s-card-lead", "에이전트가 스킬을 찾을 때와 같은 방법으로 후보를 뽑고 점수를 매겨요."), ask, bar);
    const out = el("div", "v4s-list v4s-route-out"); out.hidden = true;
    const status = el("p", "v4s-foot"); status.setAttribute("role", "status");
    wrap.append(card, status, out);
    const explain = () => side.replaceChildren(el("h2", "", "실제로 실행하지 않아요"),
      el("p", "v4s-lead", "후보는 설명·‘이럴 때 써요’가 할 일과 얼마나 맞는지로 뽑고, 판정 모델이 맞음 정도를 매겨요. 기대와 다른 스킬이 뽑히면 그 스킬의 설명을 고쳐 주세요."));
    explain();
    let busy = false;
    async function run() {
      const q = ask.value.trim();
      if (!q || busy) { if (!q) ask.focus(); return; }
      busy = go.disabled = true; status.textContent = "고르는 중…"; out.hidden = true;
      try {
        const r = await request("/v1/skills/route", { method: "POST", body: JSON.stringify({ scope: state.scope, query: q, limit: 8, ...(agents.value ? { runtime: agents.value } : {}) }) });
        if (r?.authority !== "data_only_untrusted" || !Array.isArray(r.judgments)) throw new Error("invalid_response");
        const byVer = new Map(state.data.skills.map((sk) => [sk.version_id, sk]));
        const list = [...r.judgments].sort((a, b) => (b.id === r.selected_id) - (a.id === r.selected_id) || (b.suitability || 0) - (a.suitability || 0));
        out.replaceChildren(...list.map((j) => {
          const sk = byVer.get(j.id);
          const cell = el("div", "v4s-text");
          cell.append(raw(el("div", "v4s-title v4s-name", sk?.name || j.id)), raw(el("div", "v4s-meta", [pages.categoryOf(sk || {})?.name, firstSentence(sk?.description)].filter(Boolean).join(" · ") || "목록에 없는 버전")));
          const badge = j.id === r.selected_id ? skBadge("고름") : j.conflict ? skBadge("충돌", "amber") : j.needs_human_review ? skBadge("확인 필요", "amber") : null;
          const score = el("span", "v4s-uses", r.provider === "rules" ? "—" : `${Math.round((j.suitability || 0) * 100)}점`);
          const rr = row(sk ? `skill:${sk.id}` : `route:${j.id}`, "", [cell, ...(badge ? [badge] : []), score], renderDetail);
          rr.classList.add("v4s-row");
          return rr;
        }));
        if (!list.length) out.append(el("div", "empty", "맞는 스킬이 없어요. 에이전트는 스킬 없이 일해요."));
        out.hidden = false;
        const picked = byVer.get(r.selected_id);
        status.textContent = picked ? `${picked.name}를 골라요 · 후보 ${list.length}개` : list.length ? `후보 ${list.length}개 · 확실한 하나를 고르지 못했어요` : "";
        side.replaceChildren(el("h2", "", picked ? picked.name : "고른 스킬 없음"),
          el("p", "v4s-lead", picked ? (picked.description || "") : "판정 모델이 확신하지 못했거나 맞는 후보가 없어요. 에이전트는 후보를 보고 직접 골라요."),
          skFacts([["판정", r.provider === "rules" ? "규칙 (판정 모델 없음)" : r.provider], ["후보", `${list.length}개`], ["에이전트", agents.value ? agentName(agents.value) : "모두"],
            ["참고", (r.degraded || []).length ? `판정 모델을 못 써서 규칙으로 골랐어요 (${r.degraded.join(", ")})` : ""]]));
      } catch (e) {
        status.textContent = /HTTP 404|not_found/.test(e.message) ? "이 서버는 아직 스킬 고르기 시험을 지원하지 않아요." : `고르지 못했어요 (${e.message}).`;
        explain();
      } finally { busy = go.disabled = false; }
    }
    ask.addEventListener("keydown", (e) => { if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) void run(); });
    return wrap;
  }
  // 휴지통·중복 (Figma 198:4500)
  function trashMain(p) {
    const wrap = el("div", "v4s-trash");
    const gone = trashed();
    const idle = state.data.skillUse ? liveSkills().filter(unused) : [];
    const copies = groupSkills(liveSkills()).filter((g) => g.length > 1);
    const tab = p.tab || "trash";
    const tabs = el("div", "v4s-seg"); tabs.setAttribute("role", "tablist");
    for (const [k, l, n] of [["trash", "휴지통", gone.length], ["unused", "안 씀", idle.length], ["copies", "중복", copies.length]]) {
      const b = el("button", k === tab ? "on" : "", l); b.type = "button"; b.setAttribute("role", "tab"); b.setAttribute("aria-selected", String(k === tab));
      b.append(el("output", "", String(n)));
      b.addEventListener("click", () => { state.selected = null; state.skillPage = { kind: "trash", tab: k }; render(); });
      tabs.append(b);
    }
    wrap.append(tabs);
    const reads = (sk) => shownAgents().filter(([k]) => allowed(sk, k)).length;
    const line = (sk, action) => {
      const cell = el("div", "v4s-text");
      const u = useOf(sk);
      cell.append(raw(el("div", "v4s-title v4s-name", sk.name)), raw(el("div", "v4s-meta", [firstSentence(sk.description), `최근 60일 ${u?.count || 0}번 사용 · ${reads(sk)}곳에서 읽힘`].filter(Boolean).join(" · "))));
      const r = row(`skill:${sk.id}`, "", [cell, action], renderDetail);
      r.classList.add("v4s-row");
      return r;
    };
    const list = el("div", "v4s-list");
    if (tab === "trash") {
      list.append(...gone.map((sk) => { const b = el("span", "v4s-link", "되돌리기"); b.addEventListener("click", (e) => { e.stopPropagation(); void restoreSkill(sk); }); return line(sk, b); }));
      if (!gone.length) list.append(el("div", "empty", "휴지통이 비어 있어요."));
    } else if (tab === "unused") {
      list.append(...idle.map((sk) => { const b = el("span", "v4s-link", "휴지통으로"); b.addEventListener("click", (e) => { e.stopPropagation(); confirmBatch([sk], () => trashSkills([sk])); }); return line(sk, b); }));
      if (!idle.length) list.append(el("div", "empty", state.data.skillUse ? "60일 동안 안 쓴 스킬이 없어요." : "이 서버는 아직 사용 기록을 모으지 않아요."));
    } else {
      for (const g of copies) {
        const cell = el("div", "v4s-text");
        cell.append(raw(el("div", "v4s-title v4s-name", g[0].name)), raw(el("div", "v4s-meta", [firstSentence(g[0].description), `${g.length}곳에 있어요`].filter(Boolean).join(" · "))));
        const r = row(`copies:${g[0].name}`, "", [cell, el("span", "v4s-uses", `${g.length}곳`)], renderDetail);
        r.classList.add("v4s-row");
        list.append(r);
      }
      if (!copies.length) list.append(el("div", "empty", "같은 이름으로 여러 곳에 있는 스킬이 없어요."));
    }
    wrap.append(list);
    if (idle.length && tab !== "copies" && !p.dismissed) {
      const card = el("section", "v4s-card");
      card.append(el("h3", "", `안 쓰는 스킬 ${idle.length}개를 휴지통으로 옮길까요?`), el("p", "v4s-card-lead", "60일 동안 한 번도 쓰이지 않았어요. 휴지통에서 언제든 되돌릴 수 있어요."),
        el("p", "v4s-names mono", idle.slice(0, 6).map((sk) => sk.name).join(" · ") + (idle.length > 6 ? ` 외 ${idle.length - 6}개` : "")));
      const act = el("div", "v4s-actions");
      act.append(button(`${idle.length}개 옮기기`, "primary", null, () => trashSkills(idle)), button("취소", "ghost", null, () => { state.skillPage = { ...p, dismissed: true }; render(false); }));
      card.append(act);
      wrap.append(card);
    }
    return wrap;
  }
  function copiesDetail() {
    const name = (state.selected || "").replace(/^copies:/, "");
    const g = groupSkills(liveSkills()).find((x) => x[0].name === name && x.length > 1);
    const body = detailShell("중복");
    if (!g) { body.append(el("p", "v4s-lead", "왼쪽에서 중복된 스킬을 골라요.")); return; }
    body.append(el("h2", "", `${name}가 ${g.length}곳에 있어요`), el("p", "v4s-lead", "에이전트마다 다른 쪽을 읽을 수 있어요. 한 곳을 원본으로 두고, 다른 쪽은 지우거나 에이전트에서 빼 주세요."));
    const s = skSection("위치");
    g.forEach((sk, i) => {
      const r = el("button", "v4s-path"); r.type = "button";
      r.append(el("span", "mono", (sk.source_path || "").replace(/\/SKILL\.md$/, "")), i ? el("span", "v4s-path-tag", "사본") : skBadge("원본"));
      r.title = "누르면 이 사본의 상세를 봐요";
      r.addEventListener("click", () => { state.selected = `skill:${sk.id}`; renderDetail(); });
      s.append(r);
    });
    body.append(s, skillAgentsSection(g[0]), el("p", "meta", "칩을 누르면 원본을 그 에이전트에 넣거나 빼요. 위치를 누르면 그 사본을 보고 휴지통으로 옮길 수 있어요."));
  }
  const firstSentence = (t) => (t || "").split(/(?<=[.다요])\s/)[0];
  function renderChangeBar() {
    const bar = $("#change-bar");
    const changes = [...state.pending.entries()];
    bar.hidden = state.view !== "skills" || !changes.length;
    if (bar.hidden) return;
    const [[k, want]] = changes;
    const [id, agent] = k.split("|");
    const sk = state.data.skills.find((x) => x.id === id);
    $("#change-count").textContent = `바뀐 칸 ${changes.length}개`;
    const what = `${sk?.name || id} → ${agentName(agent)}${want === "allow" ? "에 추가" : "에서 빼기"}`;
    $("#change-text").textContent = changes.length > 1 ? `${what} 외 ${changes.length - 1}개` : what;
  }
  // One availability call per skill: the owner's labels are replaced as a whole, so
  // read the current ones first and change only the staged agents.
  async function applyChanges() {
    const bySkill = new Map();
    for (const [k, want] of state.pending) {
      const [id, agent] = k.split("|");
      if (!bySkill.has(id)) bySkill.set(id, []);
      bySkill.get(id).push([agent, want]);
    }
    const btn = $("#change-apply");
    btn.disabled = true;
    let done = 0;
    try {
      for (const [id, changes] of bySkill) {
        const path = `/v1/skills/${encodeURIComponent(id)}`;
        const sk = await request(`${path}?${query()}`);
        const compat = { ...sk.skill_compatibility };
        for (const [agent, want] of changes) compat[agent] = want === "allow" ? "supported" : "unsupported";
        await request(`${path}/availability`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: sk.revision, status: sk.status, compatibility: compat }) });
        changes.forEach(([agent]) => state.pending.delete(`${id}|${agent}`));
        done++;
      }
      notice(`스킬 ${done}개에 적용했어요. 에이전트가 바로 받아요.`);
    } catch (e) {
      notice(`적용하지 못했어요 (${e.message}). 남은 칸은 그대로 둘게요.`);
    } finally {
      btn.disabled = false;
      state.data.skills = await listAll("/v1/skills", {}, 2000).catch(() => state.data.skills);
      render(false);
      renderDetail();
    }
  }
  $("#change-apply").addEventListener("click", applyChanges);
  $("#change-revert").addEventListener("click", () => { state.pending.clear(); render(false); renderDetail(); });

  // ---------------------------------------------------------------- MCP·플러그인 (TOOLS)
  // Which agents' tool servers the host sync reads; the rest are only filtered by what the server reported.
  const AGENT_SYNC = { claude_code: "서버의 ~/.claude.json을 1분마다 읽어요", codex: "서버의 codex mcp list를 1분마다 읽어요" };
  function openTool(e) {
    state.toolTab = toolKind(e);
    go("tools");
    state.selected = `tool:${e.name}`;
    render();
  }

  // ---------------------------------------------------------------- 이미지
  const ENGINES = { gemini: "Gemini", gpt: "GPT", claude: "Claude", grok: "Grok", comfyui: "ComfyUI", other: "기타" };
  const ENGINE_LOGOS = { gemini: "sym:a-gemini", gpt: "sym:a-codex", claude: "img:claude.svg", grok: "sym:i-x" };
  // Older records said comfyui_api or codex; they read and save as the model they meant.
  const engineOf = (ip) => ({ comfyui_api: "comfyui", codex: "gpt" })[ip.engine] || ip.engine;
  const SCRIPT_CHIP = "\u0000script";
  const variablesOf = (text) => [...new Set((text.match(/\{[^{}\s]{1,30}\}/g) || []))];
  function engineLogo(k) {
    if (!ENGINE_LOGOS[k]) return null;
    const t = agentLogo(ENGINE_LOGOS[k], ENGINES[k]);
    t.classList.add("eng-logo");
    return t;
  }
  // A model name with its logo (tabs, card meta, the editor's model pills).
  function engineLabel(k, text) {
    const s = el("span", "eng-label");
    const logo = engineLogo(k);
    if (logo) s.append(logo);
    s.append(text ?? (ENGINES[k] || k));
    return s;
  }
  // ← 이미지 above the title. render() takes it away again, so only the screen that draws it shows it.
  function pageBack(label, onClick) {
    const b = el("button", "page-back");
    b.type = "button";
    b.append(icon("arrow-left"), label);
    b.addEventListener("click", onClick);
    $(".main > .page-head").prepend(b);
  }
  // 검색 inside the panel: the same query as the top search box, but only the list redraws so typing keeps its place.
  function paneSearch(placeholder, redraw) {
    const l = el("label", "pane-search");
    const i = el("input");
    i.type = "search"; i.placeholder = placeholder; i.value = $("#search").value; i.setAttribute("aria-label", placeholder);
    i.addEventListener("input", () => { $("#search").value = i.value; state.query = i.value.trim().toLowerCase(); redraw(); });
    l.append(icon("magnifying-glass"), i, el("kbd", "", "/"));
    return l;
  }
  function promptTile(p, big) {
    const t = el("div", `prompt-tile${big ? " big" : ""}${p.image_prompt.kind === "slide" ? " slide" : ""}`);
    const src = p.image_prompt.images?.[0];
    if (src) { const img = el("img"); img.src = src; img.alt = ""; t.append(img); return t; }
    t.append(icon(p.image_prompt.kind === "slide" ? "presentation" : "image"));
    return t;
  }
  function promptMeta(ip) {
    const m = el("div", "pc-meta");
    if (ip.kind === "slide") {
      m.classList.add("mono");
      m.append([ip.size, ip.script ? "스크립트 연결됨" : "프롬프트"].filter(Boolean).join(" · "));
      return m;
    }
    m.append(engineLabel(engineOf(ip), raw(el("span", "", [...(ip.tags || []), ENGINES[engineOf(ip)] || ip.engine].join(" · ")))));
    return m;
  }
  function renderImages() {
    const all = state.data.prompts;
    const run = state.promptRun && all.find((p) => p.id === state.promptRun);
    if (state.promptRun && !run) state.promptRun = null;
    if (run) return renderPromptRun(run);
    if (gallery.isOpen("prompts")) { $("#controls").replaceChildren(); $("#chips").replaceChildren(gallery.archiveChip("prompts")); return gallery.renderArchive("prompts", state.imageTab); }
    const ofKind = all.filter((p) => p.image_prompt.kind === state.imageTab);
    // 모델 tabs (only models in use) and 카테고리 chips; a filter whose last prompt left falls back to 전체.
    const models = Object.keys(ENGINES).filter((k) => ofKind.some((p) => engineOf(p.image_prompt) === k));
    if (!models.includes(state.engine)) state.engine = "all";
    $("#controls").replaceChildren(...(models.length > 1 ? [segment([["all", "전체 모델"], ...models.map((k) => [k, engineLabel(k)])], state.engine, (k) => { state.engine = k; render(false); })] : []));
    const cats = [...new Set(ofKind.flatMap((p) => p.image_prompt.tags || []))].sort((a, b) => a.localeCompare(b, "ko"));
    const filters = [["all", "전체"], ...cats.map((c) => [c, c]), ...(ofKind.some((p) => p.image_prompt.script) ? [[SCRIPT_CHIP, "스크립트 있음"]] : [])];
    if (!filters.some(([k]) => k === state.imgCat)) state.imgCat = "all";
    const chips = filters.length > 1 ? filters.map(([k, label]) => {
      const c = el("button", `chip${state.imgCat === k ? " active" : ""}`, label); c.type = "button";
      c.addEventListener("click", () => { state.imgCat = k; render(false); });
      return c;
    }) : [];
    const keep = (p) => {
      const ip = p.image_prompt;
      if (state.engine !== "all" && engineOf(ip) !== state.engine) return false;
      if (state.imgCat === SCRIPT_CHIP) return !!ip.script;
      return state.imgCat === "all" || (ip.tags || []).includes(state.imgCat);
    };
    const grid = $("#table");
    const slide = state.imageTab === "slide";
    const draw = () => {
      const items = sortPrompts(ofKind.filter((p) => keep(p) && matches(`${p.image_prompt.title} ${p.image_prompt.prompt} ${(p.image_prompt.tags || []).join(" ")}`)));
      if (!items.length) {
        grid.replaceChildren(el("div", "empty", all.length ? "조건에 맞는 프롬프트가 없어요." : "아직 프롬프트가 없어요. 오른쪽 위 ‘프롬프트 추가’로 넣어 보세요."));
        return;
      }
      grid.replaceChildren(...items.map((p, i) => {
        const card = el("button", state.selected === "prompt:" + p.id ? "prompt-card selected" : "prompt-card");
        card.type = "button";
        card.dataset.id = `prompt:${p.id}`;
        const head = el("div", "pc-head");
        head.append(raw(el("strong", "", p.image_prompt.title)));
        if (!slide) head.append(el("span", "pc-num", String(i + 1).padStart(2, "0")));
        card.append(promptTile(p), head, promptMeta(p.image_prompt));
        card.addEventListener("click", () => { state.selected = `prompt:${p.id}`; markSelected(); renderDetail(); openDetail(); });
        return gallery.cardShortcut("prompts", p, card);
      }));
    };
    const search = paneSearch(slide ? "프롬프트 이름 · 스크립트 검색" : "프롬프트 이름 · 도구 검색", draw);
    $("#chips").replaceChildren(search, promptSortSelect(draw), ...chips, gallery.archiveChip("prompts"));
    $("#stats").replaceChildren();
    draw();
  }
  // 정렬 (Figma 198:1404 "최근 수정순"): the server lists newest first; this re-orders the cards only.
  const PROMPT_SORTS = [["updated", "최근 수정순"], ["created", "최근 추가순"], ["name", "이름순"]];
  let promptSort = PROMPT_SORTS.some(([k]) => k === stored("arkism.promptSort")) ? stored("arkism.promptSort") : "updated";
  function sortPrompts(list) {
    const t = (x, k) => Date.parse(x[k] || x.updated_at || "") || 0;
    if (promptSort === "name") return list.slice().sort((a, b) => a.image_prompt.title.localeCompare(b.image_prompt.title, "ko"));
    const k = promptSort === "created" ? "created_at" : "updated_at";
    return list.slice().sort((a, b) => t(b, k) - t(a, k));
  }
  function promptSortSelect(redraw) {
    const s = el("select", "v4g-sort");
    s.setAttribute("aria-label", "정렬");
    for (const [v, l] of PROMPT_SORTS) { const o = el("option", "", l); o.value = v; o.selected = v === promptSort; s.append(o); }
    s.addEventListener("change", () => { promptSort = s.value; store("arkism.promptSort", promptSort); redraw(); });
    return s;
  }
  // 복제: a new record with the same fields, named "… 사본"; the copy opens selected so it can be edited right away.
  const copyTitle = (t, max) => { const tail = " 사본"; return [...t].slice(0, max - tail.length).join("") + tail; };
  async function duplicatePrompt(p) {
    const prompt = { ...p.image_prompt, title: copyTitle(p.image_prompt.title, 120) };
    try {
      const saved = await request("/v1/prompts", { method: "POST", body: JSON.stringify({ scope: state.scope, prompt }) });
      state.data.prompts = (await request(`/v1/prompts?${query()}`)).items || [];
      state.promptRun = null;
      state.selected = saved?.id ? `prompt:${saved.id}` : null;
      render();
      notice("복제했어요. 사본을 고쳐서 쓰세요.");
    } catch (err) {
      notice({ secret_detected: "키나 토큰처럼 보이는 값이 있어 복제하지 않았어요.", invalid_request: "복제하지 못했어요. 이름이나 이미지 크기를 확인해 주세요." }[err.message] || `복제하지 못했어요 (${err.message}).`);
    }
  }
  function copyButton(text, label = "복사", cls = "ghost small") {
    const b = button(label, cls, cls.includes("ghost") ? "copy" : null, async () => {
      try {
        await navigator.clipboard.writeText(text);
        b.lastChild.textContent = "복사됨";
      } catch (err) {
        b.lastChild.textContent = "복사 안 됨";
        notice(`복사하지 못했어요 (${err.message || err}).`);
      }
    });
    return b;
  }
  function imagesSummary() {
    const body = detailShell(state.imageTab === "slide" ? "슬라이드" : "이미지");
    body.append(el("h2", "", "프롬프트 보관함"), el("p", "lead", "자주 쓰는 이미지·슬라이드 프롬프트를 예시 이미지와 함께 모아 둬요. 카드를 고르면 프롬프트를 복사하거나 바로 만들 수 있어요."));
  }
  // {변수} values typed on the detail or the 실행 page, kept per prompt until the app closes.
  const promptVals = new Map();
  // Everything a prompt can do: fill its {변수}, hand it to its workflow, send it to ComfyUI, download its script.
  function promptKit(p) {
    const ip = p.image_prompt;
    const vars = variablesOf(ip.prompt);
    if (!promptVals.has(p.id)) promptVals.set(p.id, {});
    const vals = promptVals.get(p.id);
    const filled = () => vars.reduce((t, v) => t.split(v).join((vals[v] || "").trim() || v), ip.prompt);
    // 쓰는 워크플로우: the prompt's first live $이름 runs with the filled prompt as its task.
    const flowRec = (state.data.crew || []).find((r) => r.crew.type === "workflow" && (ip.experts || []).some((e) => e.replace(/^\$/, "") === r.crew.handle || e === r.crew.name));
    const comfy = (ip.engine || "").startsWith("comfyui");
    async function comfyUrl(ask) {
      let url = pages.prefs.comfyUrl;
      if (!url) {
        url = trimSlash(prompt(ask, "http://127.0.0.1:8188") || "");
        if (!url) return "";
        pages.prefs.comfyUrl = url;
        pages.savePrefs();
      }
      return url;
    }
    return {
      ip, vars, vals, filled, flowRec, comfy,
      // 이 프롬프트로 만들기: the workflow that uses this prompt gets it (or it is copied).
      async run(btn, out) {
        const text = filled();
        const say = (cls, t) => (out ? out.replaceChildren(el("p", cls, t)) : notice(t));
        if (!flowRec) {
          try { await navigator.clipboard.writeText(text); } catch (_) { /* clipboard blocked */ }
          notice("쓰는 워크플로우가 없어서 채운 프롬프트를 복사했어요. 쓰는 워크플로우를 정하면 바로 맡길 수 있어요.");
          return;
        }
        const scriptPart = ip.script ? `\n\n참고할 스크립트 (${ip.script_name || "script"}):\n${ip.script}` : "";
        const sizePart = ip.size ? `\n크기: ${ip.size}` : "";
        const countPart = ip.count ? `\n개수: ${ip.count}` : "";
        if (btn) btn.disabled = true;
        const r = await crew.runWorkflow(flowRec, `${text}${scriptPart}${sizePart}${countPart}`);
        if (btn) btn.disabled = false;
        say(r ? "dim" : "warn-text", r ? `$${flowRec.crew.handle}에 맡겼어요. 워크플로우 › 최근 실행에서 결과를 봐요.` : "맡기지 못했어요.");
      },
      // ComfyUI로 만들기: 서버 queues the API-format workflow on the ComfyUI address and keeps the images.
      async comfyMake(btn) {
        const url = await comfyUrl("ComfyUI 주소를 넣어 주세요. 서버에서 열 수 있는 주소여야 해요.");
        if (!url) return;
        if (btn) btn.disabled = true;
        notice("ComfyUI에 맡겼어요. 다 그리면 알려 드려요.");
        try {
          // {prompt} sits inside a JSON string in the workflow: put it in JSON-escaped so quotes and new lines keep it valid.
          const a = await crew.assist("comfy", JSON.stringify({ url, workflow: ip.script.split("{prompt}").join(JSON.stringify(filled()).slice(1, -1)) }));
          notice(a.status === "done" ? a.output.replaceAll("\n", " ") : `만들지 못했어요: ${a.error || "알 수 없음"}`);
        } catch (e) { notice(`만들지 못했어요 (${e.message}).`); } finally { if (btn) btn.disabled = false; }
      },
      async comfyOpen() {
        const url = await comfyUrl("ComfyUI 주소를 넣어 주세요.");
        if (!url) return;
        if (ip.script) { try { await navigator.clipboard.writeText(ip.script.split("{prompt}").join(filled())); notice("워크플로를 복사했어요. ComfyUI 화면에 붙여 넣으면 열려요."); } catch (_) { /* clipboard blocked */ } }
        window.open(url, "_blank");
      },
      download() {
        const a = document.createElement("a");
        a.href = URL.createObjectURL(new Blob([ip.script], { type: "text/plain" }));
        a.download = ip.script_name || (ip.kind === "slide" ? "make_slide.py" : "workflow.json");
        a.click();
        URL.revokeObjectURL(a.href);
      }
    };
  }
  // A small label over a control or a read-only value box (the detail's 설정, the 실행 page).
  function labeled(label, node, tag = "div") {
    const f = el(tag, "v4g-field");
    f.append(el("span", "v4g-label", label), node);
    return f;
  }
  const valueBox = (text, cls = "") => raw(el("div", `v4g-box ${cls}`.trim(), text));
  function varInput(kit, v, onChange) {
    const i = el("input");
    i.placeholder = v; i.value = kit.vals[v] || "";
    i.addEventListener("input", () => { kit.vals[v] = i.value; onChange?.(); });
    return i;
  }
  function promptDetail(p) {
    const kit = promptKit(p);
    const ip = kit.ip;
    const body = detailShell(ip.kind === "slide" ? "슬라이드" : "이미지");
    body.classList.add("v4g-detail");
    const top = el("div", "section v4g-top");
    top.append(engineLabel(engineOf(ip), `프롬프트 · ${ENGINES[engineOf(ip)] || ip.engine}${ip.kind === "slide" && ip.script ? " · 스크립트 연결됨" : ""}`), raw(el("h2", "", ip.title)));
    const big = promptTile(p, true);
    top.append(big);
    if ((ip.images || []).length > 1) {
      // The small pictures swap into the big one.
      const strip = el("div", "prompt-thumbs");
      ip.images.forEach((src, i) => {
        const b = el("button", i ? "" : "active"); b.type = "button"; b.setAttribute("aria-label", `이미지 ${i + 1}`);
        const img = el("img"); img.src = src; img.alt = ""; b.append(img);
        b.addEventListener("click", () => { big.querySelector("img").src = src; strip.querySelectorAll("button").forEach((x) => x.classList.toggle("active", x === b)); });
        strip.append(b);
      });
      top.append(strip);
    }
    const acts = el("div", "actions");
    acts.append(button("이 프롬프트로 만들기  ↗", "primary small", null, () => { state.promptRun = p.id; render(); }));
    if (kit.comfy) acts.append(button("ComfyUI에서 열기", "small", null, kit.comfyOpen));
    else if (ip.script) acts.append(button(ip.kind === "slide" ? "스크립트 내려받기 (PPTX)" : "스크립트 내려받기", "small", null, kit.download));
    top.append(acts);
    body.append(top);
    const pr = section("프롬프트");
    pr.querySelector("h3").append(copyButton(ip.prompt, "프롬프트 복사", "small"));
    pr.append(valueBox(ip.prompt, "v4g-quote"));
    body.append(pr);
    const opts = section("설정");
    kit.vars.forEach((v) => opts.append(labeled(`변수 · ${v}`, varInput(kit, v), "label")));
    if (ip.negative) opts.append(labeled("제외", valueBox(ip.negative)));
    if (ip.size || ip.count) {
      const row = el("div", "v4g-grid2");
      if (ip.size) row.append(labeled("크기", valueBox(ip.size)));
      if (ip.count) row.append(labeled("한 번에", valueBox(tr(`${ip.count}장`))));
      opts.append(row);
    }
    if (ip.seed) opts.append(labeled("시드", valueBox(ip.seed)));
    if (opts.children.length > 1) body.append(opts);
    if (ip.script || ip.experts?.length) {
      const sc = section("스크립트");
      if (ip.script) {
        const where = [kit.comfy && "ComfyUI API", kit.flowRec ? `$${kit.flowRec.crew.handle} 워크플로우 연결됨` : "연결된 워크플로우 없음"].filter(Boolean).join(" · ");
        const meta = el("div", "v4g-mono");
        meta.append(raw(el("div", "", ip.script_name || "script")), el("div", "", where));
        sc.append(meta);
      }
      if (ip.experts?.length) {
        const row = el("div", "v4g-chips");
        ip.experts.forEach((e) => row.append(raw(el("span", "v4g-chip", e))));
        sc.append(row);
      }
      if (ip.script) {
        sc.querySelector("h3").append(copyButton(ip.script));
        const code = el("pre", "code-box");
        code.append(raw(el("div", "code-name", ip.script_name || "script")), raw(el("code", "", ip.script)));
        sc.append(code);
        const a = el("div", "actions");
        a.append(kit.comfy
          ? button("ComfyUI로 만들기", "small", null, (e) => kit.comfyMake(e.currentTarget))
          : button("스크립트 내려받기", "small", null, kit.download));
        // 스크립트 실행: the linked workflow gets the filled prompt with this script and runs it (the app itself runs no code).
        const ran = el("div", "v4g-out");
        if (kit.flowRec && !kit.comfy) a.prepend(button("스크립트 실행", "small", null, (e) => kit.run(e.currentTarget, ran)));
        sc.append(a, ran);
        if (!kit.flowRec && !kit.comfy) sc.append(el("p", "v4g-hint", "쓰는 워크플로우($이름)를 정하면 그 워크플로우가 스크립트를 실행해요."));
      }
      body.append(sc);
    }
    const foot = el("div", "v4g-foot");
    const links = el("div", "actions");
    links.append(button("고치기", "ghost", null, () => openPromptEditor(p)), button("복제", "ghost", null, () => duplicatePrompt(p)), button("보관함에서 빼기", "ghost", null, () => archivePrompt(p)), gallery.deleteButton("prompts", p, "ghost"));
    foot.append(links, el("p", "meta", `${shortDate(p.updated_at)} 수정 · ${p.created_by || ""}`));
    body.append(foot);
  }
  // 프롬프트 실행: fill the {변수}, see the finished prompt, then make it here or send it to ComfyUI.
  function renderPromptRun(p) {
    const kit = promptKit(p);
    const ip = kit.ip;
    pageBack(ip.kind === "slide" ? "슬라이드" : "이미지", () => { state.promptRun = null; render(); });
    ["#controls", "#chips", "#stats"].forEach((q) => $(q).replaceChildren());
    const page = $("#table");
    page.className = "v4g-run";
    const vars = el("section", "v4g-card");
    vars.append(el("h3", "", "변수 채우기"), el("p", "v4g-hint", kit.vars.length ? `${kit.vars.join("·")} 자리에 들어갈 값을 적어요.` : "이 프롬프트에는 변수가 없어요. 그대로 써요."));
    const done = valueBox(kit.filled(), "v4g-filled");
    if (kit.vars.length) {
      const grid = el("div", "v4g-grid2");
      kit.vars.forEach((v) => grid.append(labeled(v.slice(1, -1), varInput(kit, v, () => { done.textContent = kit.filled(); }), "label")));
      vars.append(grid);
    }
    vars.append(labeled("완성된 프롬프트", done));
    const out = el("div", "v4g-out");
    out.id = "prompt-run-out";
    vars.append(out);
    const cards = [vars];
    if (kit.comfy) {
      const c = el("section", "v4g-card v4g-comfy");
      const head = el("div", "v4g-card-head");
      const url = pages.prefs.comfyUrl;
      head.append(el("strong", "", "ComfyUI"), el("span", `v4g-badge${url ? " on" : ""}`, url ? "주소 저장됨" : "주소 없음"), button("연결 설정", "small", null, () => {
        const next = trimSlash(prompt("ComfyUI 주소를 넣어 주세요. 서버에서 열 수 있는 주소여야 해요.", url || "http://127.0.0.1:8188") ?? url ?? "");
        if (next === (url || "")) return;
        pages.prefs.comfyUrl = next;
        pages.savePrefs();
        render(false);
      }));
      const acts = el("div", "actions");
      if (ip.script) acts.append(button("ComfyUI로 만들기", "small", null, (e) => kit.comfyMake(e.currentTarget)));
      acts.append(button("ComfyUI에서 열기", "small", null, kit.comfyOpen));
      c.append(head, el("p", "v4g-hint", url ? `${url} · 워크플로우에 프롬프트를 넣어 대기열에 올려요.` : "ComfyUI 주소를 넣으면 워크플로우에 프롬프트를 넣어 대기열에 올려요."), acts);
      cards.push(c);
    }
    page.replaceChildren(...cards);
  }
  function promptRunDetail(p) {
    const kit = promptKit(p);
    const ip = kit.ip;
    const body = detailShell("실행");
    body.classList.add("v4g-detail");
    const s = section("스크립트와 결과");
    if (ip.script) {
      const a = el("div", "v4g-stack");
      a.append(copyButton(ip.script, "스크립트 복사", "small"), button(ip.kind === "slide" ? "스크립트 내려받기 (PPTX 만들기)" : "스크립트 내려받기", "small", null, kit.download));
      s.append(valueBox(ip.script_name || "script", "mono"), a);
    } else s.append(el("p", "v4g-hint", "이 프롬프트에는 스크립트가 없어요."));
    s.append(el("div", "v4g-note", kit.flowRec ? `$${kit.flowRec.crew.handle} 워크플로우에 맡겨요.` : "연결된 워크플로우가 없으면 프롬프트를 복사해요."));
    const k = section("보관함");
    const acts = el("div", "actions");
    acts.append(button("고치기", "small", null, () => openPromptEditor(p)), button("복제", "small", null, () => duplicatePrompt(p)), button("보관함에서 빼기", "small", null, () => archivePrompt(p)), gallery.deleteButton("prompts", p));
    k.append(acts, el("p", "meta", `수정 날짜 · ${shortDate(p.updated_at)} / 만든 사람 · ${p.created_by || ""}`));
    body.append(s, k);
  }
  // The 이 프롬프트로 만들기 button on top of the 실행 page.
  function runPrompt(btn) {
    const p = state.data.prompts.find((x) => x.id === state.promptRun);
    if (p) void promptKit(p).run(btn, $("#prompt-run-out"));
  }
  const promptDialog = $("#prompt-dialog");
  let editing = null;
  // 이미지: up to four pictures, shrunk here so each data URL stays under the server's 512 KiB.
  const MAX_IMAGES = 4, MAX_IMAGE_CHARS = 512 * 1024;
  let dialogImages = [];
  function drawDialogImages() {
    const box = $("#p-images");
    box.replaceChildren(...dialogImages.map((src, i) => {
      const t = el("div", "img-thumb");
      const img = el("img"); img.src = src; img.alt = `이미지 ${i + 1}`;
      const x = el("button", "icon-btn plain"); x.type = "button"; x.setAttribute("aria-label", "이미지 빼기"); x.append(icon("x"));
      x.addEventListener("click", () => { dialogImages.splice(i, 1); drawDialogImages(); });
      t.append(img, x);
      return t;
    }));
    if (dialogImages.length < MAX_IMAGES) {
      const add = el("button", "img-add"); add.type = "button"; add.append(el("span", "", "파일 선택 또는 이미지 붙여 넣기"));
      add.addEventListener("click", () => $("#p-image-file").click());
      box.append(add);
    }
  }
  async function shrinkImage(file) {
    const bmp = await createImageBitmap(file);
    try {
      for (const side of [1280, 1024, 768, 512]) {
        const k = Math.min(1, side / Math.max(bmp.width, bmp.height));
        const c = document.createElement("canvas");
        c.width = Math.round(bmp.width * k); c.height = Math.round(bmp.height * k);
        c.getContext("2d").drawImage(bmp, 0, 0, c.width, c.height);
        const url = c.toDataURL("image/webp", 0.85);
        if (url.length <= MAX_IMAGE_CHARS) return url;
      }
      throw new Error("too_large");
    } finally { bmp.close(); }
  }
  async function addImages(files) {
    const pics = [...files].filter((f) => f.type.startsWith("image/"));
    // Shrinking is slow: a later pick or another prompt opened meanwhile must not push past four or into it.
    const list = dialogImages;
    $("#prompt-error").textContent = pics.length > MAX_IMAGES - list.length ? `이미지는 ${MAX_IMAGES}장까지 넣을 수 있어요.` : "";
    for (const f of pics) {
      if (list !== dialogImages || list.length >= MAX_IMAGES) return;
      try {
        const url = await shrinkImage(f);
        if (list === dialogImages && list.length < MAX_IMAGES) list.push(url);
      } catch (_) { $("#prompt-error").textContent = `${f.name || "이미지"}를 줄이지 못해 넣지 않았어요.`; }
      drawDialogImages();
    }
  }
  $("#p-image-file").addEventListener("change", (e) => { void addImages(e.target.files); e.target.value = ""; });

  // 반짝이 (이름 옆): AI reads the prompt and the fields already filled, names the prompt, and fills the empty
  // category·what to leave out·size; the size comes from the first image's shape when there is one. Only on click.
  const SIZES = [[1, "1024×1024"], [2 / 3, "1024×1536"], [3 / 2, "1536×1024"], [3 / 4, "768×1024"], [4 / 3, "1024×768"], [9 / 16, "1080×1920"], [16 / 9, "1920×1080"]];
  const empty = (id) => !$(id).value.trim();
  const autoNote = (t) => { $("#prompt-auto").textContent = t; };
  function imageSize(src) {
    return new Promise((done) => {
      const img = new Image();
      img.onload = () => {
        const r = img.naturalWidth / img.naturalHeight;
        done(SIZES.reduce((a, b) => (Math.abs(Math.log(b[0] / r)) < Math.abs(Math.log(a[0] / r)) ? b : a), SIZES[0])[1]);
      };
      img.onerror = () => done("");
      img.src = src;
    });
  }
  let nameSeq = 0;
  async function nameWithAI() {
    const text = $("#p-prompt").value.trim();
    if (!text) { autoNote("프롬프트를 먼저 넣어 주세요."); $("#p-prompt").focus(); return; }
    const btn = $("#p-title-ai");
    const seq = ++nameSeq, titleBefore = $("#p-title").value, firstImage = dialogImages[0];
    btn.disabled = true;
    autoNote("AI가 이름을 정하는 중이에요…");
    const v = (id) => $(id).value.trim();
    const known = [...new Set(state.data.prompts.flatMap((x) => x.image_prompt.tags || []))].slice(0, 60);
    const ask = `이미지·슬라이드 생성 프롬프트를 라이브러리에 저장한다. 아래 내용을 읽고 이름을 정하고, 비어 있는 칸만 채운다.\n`
      + `답은 JSON 객체 하나만: {"title": "내용이 드러나는 한국어 이름 30자 이내", "tags": ["카테고리 1~3개"], "negative": "프롬프트가 피하라고 한 것을 영어 쉼표 목록으로, 없으면 빈 문자열", "size": "프롬프트에 크기·비율이 있으면 1024×1536 꼴, 없으면 빈 문자열"}\n`
      + `카테고리는 가능하면 기존 것 중에서: ${known.join(", ") || "(없음)"}\n\n`
      + `종류: ${v("#p-kind")}\n모델: ${v("#p-engine")}\n카테고리: ${v("#p-tags") || "(비어 있음)"}\n제외할 것: ${v("#p-negative") || "(비어 있음)"}\n크기: ${v("#p-size") || "(비어 있음)"}\n\n프롬프트:\n${text.slice(0, 6000)}`;
    try {
      const [a, size] = await Promise.all([
        crew.assist("explain", ask, { persona: { crew: { instructions: "너는 이미지 프롬프트 라이브러리 정리 도우미다. 프롬프트를 읽고 저장 칸만 채운다." } } }),
        firstImage && empty("#p-size") ? imageSize(firstImage) : ""
      ]);
      if (seq !== nameSeq || !promptDialog.open) return; // the dialog was closed or reopened meanwhile
      if ($("#p-prompt").value.trim() !== text) { autoNote("기다리는 동안 프롬프트가 바뀌어 답을 쓰지 않았어요. 다시 눌러 주세요."); return; }
      const out = a.status === "done" ? crew.parseJSON(a.output) : null;
      if (!out?.title) { autoNote(a.status === "done" ? "AI가 이름을 정하지 못했어요. 직접 넣어 주세요." : crew.assistError(a)); return; }
      if ($("#p-title").value === titleBefore) $("#p-title").value = String(out.title).slice(0, 120); // typed meanwhile: keep it
      const put = (id, val) => { if (val && empty(id)) $(id).value = String(val).slice(0, Number($(id).maxLength) || 200); };
      put("#p-tags", Array.isArray(out.tags) ? out.tags.join(", ") : out.tags);
      put("#p-negative", out.negative);
      put("#p-size", (dialogImages[0] === firstImage && size) || out.size); // the first image changed meanwhile: not its shape
      autoNote("AI가 이름과 빈 칸을 채웠어요. 맞게 고쳐서 저장하세요.");
    } catch (e) {
      if (seq === nameSeq) autoNote(`AI가 답하지 못했어요 (${e.message}). 직접 넣어 주세요.`);
    } finally {
      if (seq === nameSeq) btn.disabled = false;
    }
  }
  $("#p-title-ai").addEventListener("click", nameWithAI);
  promptDialog.addEventListener("paste", (e) => {
    const files = [...(e.clipboardData?.files || [])];
    if (!files.some((f) => f.type.startsWith("image/"))) return;
    e.preventDefault();
    void addImages(files);
  });
  // 종류·모델 as pills over the two selects (the form still reads the selects).
  function pillsFor(select, box, label) {
    const draw = () => box.replaceChildren(...[...select.options].map((o) => {
      const b = el("button", `pill-opt${o.value === select.value ? " active" : ""}`);
      b.type = "button";
      b.setAttribute("role", "radio");
      b.setAttribute("aria-checked", String(o.value === select.value));
      b.append(label(o));
      b.addEventListener("click", () => { select.value = o.value; draw(); });
      return b;
    }));
    select.addEventListener("change", draw);
    return draw;
  }
  const drawKindPills = pillsFor($("#p-kind"), $("#p-kind-pills"), (o) => o.textContent);
  const drawEnginePills = pillsFor($("#p-engine"), $("#p-engine-pills"), (o) => engineLabel(o.value, ENGINES[o.value] || o.textContent));
  function openPromptEditor(p) {
    editing = p;
    const ip = p?.image_prompt || { kind: state.imageTab, engine: "gemini" };
    $("#prompt-dialog-title").textContent = p ? "프롬프트 고치기" : "프롬프트 추가";
    $("#prompt-dialog-back span").textContent = state.imageTab === "slide" ? "슬라이드" : "이미지";
    const set = (id, v) => { $(id).value = v ?? ""; };
    set("#p-title", ip.title); set("#p-kind", ip.kind); set("#p-engine", engineOf(ip)); set("#p-tags", (ip.tags || []).join(", "));
    fillList("#p-tag-list", state.data.prompts.flatMap((x) => x.image_prompt.tags || []));
    dialogImages = [...(ip.images || [])];
    drawDialogImages(); set("#p-prompt", ip.prompt); set("#p-negative", ip.negative);
    set("#p-size", ip.size); set("#p-count", ip.count || ""); set("#p-seed", ip.seed); set("#p-script-name", ip.script_name); set("#p-script", ip.script);
    set("#p-experts", (ip.experts || []).join(", "));
    $("#prompt-error").textContent = "";
    nameSeq++; autoNote(""); $("#p-title-ai").disabled = false;
    drawKindPills(); drawEnginePills();
    promptDialog.showModal();
  }
  $("#prompt-form").addEventListener("submit", async (e) => {
    if (e.submitter?.value !== "save") return;
    e.preventDefault();
    const val = (id) => $(id).value.trim();
    const prompt = {
      title: val("#p-title"), kind: val("#p-kind"), engine: val("#p-engine"), prompt: val("#p-prompt"), negative: val("#p-negative"),
      size: val("#p-size"), count: Number(val("#p-count")) || 0, seed: val("#p-seed"), script_name: val("#p-script-name"), script: $("#p-script").value,
      experts: val("#p-experts").split(",").map((x) => x.trim()).filter(Boolean),
      tags: [...new Set(val("#p-tags").split(",").map((x) => x.trim()).filter(Boolean))], images: dialogImages
    };
    try {
      const saved = editing
        ? await request(`/v1/prompts/${encodeURIComponent(editing.id)}`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: editing.revision, prompt }) })
        : await request("/v1/prompts", { method: "POST", body: JSON.stringify({ scope: state.scope, prompt }) });
      promptDialog.close();
      state.data.prompts = (await request(`/v1/prompts?${query()}`)).items || [];
      state.imageTab = saved.image_prompt.kind;
      state.selected = `prompt:${saved.id}`;
      render();
      notice("저장했어요.");
    } catch (err) {
      $("#prompt-error").textContent = { secret_detected: "키나 토큰처럼 보이는 값이 있어 저장하지 않았어요.", invalid_request: "이름·프롬프트·카테고리(40자까지)를 확인해 주세요.", revision_conflict: "다른 곳에서 먼저 고쳤어요. 닫고 다시 열어 주세요." }[err.message] || `저장하지 못했어요 (${err.message}).`;
    }
  });
  async function archivePrompt(p) {
    if (!confirm(`'${p.image_prompt?.title || "이 프롬프트"}'를 보관함에서 뺄까요? 목록에서 사라지지만 기록은 남아요.`)) return;
    try {
      await request(`/v1/prompts/${encodeURIComponent(p.id)}`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: p.revision, archive: true }) });
      state.data.prompts = (await request(`/v1/prompts?${query()}`)).items || [];
      state.selected = null;
      render();
      notice("보관함에서 뺐어요. 기록은 남아 있어요.");
    } catch (err) {
      notice(`처리하지 못했어요: ${err.message}`);
    }
  }

  // ---------------------------------------------------------------- 디자인 시스템 라이브러리
  // Arkism's own tokens are not a stored system: they open from the 'Arkism 토큰' button above the list.
  const PURPOSES = ["앱 화면", "웹사이트", "슬라이드", "브랜드", "문서·인쇄"];
  const paintable = (v) => !!v && window.CSS?.supports?.("color", v);
  function renderLibrary() {
    const seg = segment([["product", "제품별"], ["purpose", "목적별"]], state.designGroup, (k) => { state.designGroup = k; store("arkism.designGroup", k); render(false); });
    seg.classList.add("v4g-group-seg");
    ["#chips", "#stats"].forEach((q) => $(q).replaceChildren());
    if (gallery.isOpen("design_systems")) { $("#controls").replaceChildren(...gallery.libraryButtons()); return gallery.renderArchive("design_systems"); }
    const draw = () => {
      const all = (state.data.designSystems || []).filter(({ design_system: ds }) =>
        matches(`${ds.title} ${ds.product || ""} ${ds.purpose || ""} ${ds.description || ""} ${(ds.tags || []).join(" ")}`));
      const page = el("div", "ds-library");
      if (!all.length) {
        const empty = el("div", "empty");
        if (state.data.designSystems?.length) empty.append("조건에 맞는 디자인 시스템이 없어요.");
        else empty.append(el("p", "", "아직 디자인 시스템이 없어요. 제품의 색·글꼴·규칙을 하나씩 추가해 보세요."), button("디자인 시스템 추가", "primary", null, () => openSystemEditor(null)));
        page.append(empty); $("#table").replaceChildren(page); return;
      }
      const groups = new Map();
      for (const r of all) {
        const key = (r.design_system[state.designGroup] || "").trim();
        if (!groups.has(key)) groups.set(key, []);
        groups.get(key).push(r);
      }
      // The group holding Arkism first, then by name, unnamed last.
      const rank = (k) => (groups.get(k).some((r) => r.builtin) ? 0 : k ? 1 : 2);
      const keys = [...groups.keys()].sort((a, b) => rank(a) - rank(b) || a.localeCompare(b, "ko"));
      let n = 0;
      for (const k of keys) {
        const g = el("section", "ds-group");
        const h = el("h2", "", k || (state.designGroup === "product" ? "제품 정하지 않음" : "목적 정하지 않음"));
        h.append(el("small", "", `${groups.get(k).length}개`));
        const grid = el("div", "ds-grid");
        for (const r of groups.get(k)) grid.append(systemCard(r, ++n));
        g.append(h, grid);
        page.append(g);
      }
      if (state.data.designSystems === null) page.append(el("p", "pg-hint", "서버가 아직 디자인 시스템 저장을 몰라요. 서버를 새 버전으로 올리면 여기에 모을 수 있어요."));
      $("#table").replaceChildren(page);
    };
    $("#controls").replaceChildren(paneSearch("디자인 시스템 찾기", draw), seg, ...gallery.libraryButtons());
    draw();
  }
  // "Orbit Mail" → "Orbit Mail." — the gallery's serif names end with a full stop.
  const serifName = (t) => (/[.!?。]$/.test(t) ? t : `${t}.`);
  function swatches(colors, n = 4) {
    const row = el("div", "ds-swatches");
    for (const c of (colors || []).filter((x) => paintable(x.value)).slice(0, n)) { const i = el("i"); i.style.background = c.value; i.title = `${c.name} ${c.value}`; row.append(i); }
    return row;
  }
  function systemCard(r, n) {
    const id = `ds:${r.id}`;
    const ds = r.design_system;
    const card = el("button", state.selected === id ? "ds-card selected" : "ds-card");
    card.type = "button";
    card.dataset.id = id;
    const art = el("div", "ds-art");
    // The darkest #hex colour of the system is the ground under the line art.
    const lum = (v) => { const m = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(v.trim()); if (!m) return 2; const h = m[1].length === 3 ? [...m[1]].map((x) => x + x).join("") : m[1]; return [0, 2, 4].reduce((s, i, k) => s + parseInt(h.slice(i, i + 2), 16) * [0.2126, 0.7152, 0.0722][k], 0) / 255; };
    const ground = (ds.colors || []).filter((c) => lum(c.value) < 0.35).sort((x, y) => lum(x.value) - lum(y.value))[0];
    if (ground) art.style.background = ground.value;
    const img = el("img"); img.src = "img/arkism-art.svg"; img.alt = ""; art.append(img);
    const meta = [r.builtin ? "이 앱" : "", ds.product, ds.purpose].filter(Boolean).join(" · ");
    card.append(el("span", "ds-num", `COLLECTION ${String(n).padStart(2, "0")}`), raw(el("strong", "ds-name", serifName(r.builtin ? "arkism" : ds.title))), art, swatches(ds.colors), raw(el("span", "ds-meta", meta)));
    card.addEventListener("click", () => {
      if (gallery.pick(r, card)) return;
      if (r.builtin) { go("tokens"); return; }
      state.selected = id; markSelected(); renderDetail(); openDetail();
    });
    if (gallery.picking()) return gallery.markCard(r, card);
    return r.builtin ? card : gallery.cardShortcut("design_systems", r, card);
  }
  function librarySummary() {
    const body = detailShell("디자인 시스템");
    body.classList.add("v4g-detail");
    body.append(el("h2", "", "디자인 시스템 라이브러리"),
      el("p", "lead", "제품별·목적별로 디자인 시스템을 모아 둬요. 오른쪽 위 ‘디자인 시스템 추가’로 넣고, 이 앱의 실제 색·글자·부품은 ‘Arkism 토큰’에서 봐요."));
    if (state.data.designSystems === null) body.append(callout("서버 업데이트가 필요해요", "Arkism 서버를 새 버전으로 올리면 디자인 시스템을 저장할 수 있어요."));
  }
  function systemDetail(r) {
    const ds = r.design_system;
    const body = detailShell("선택한 컬렉션");
    body.classList.add("v4g-detail", "ds-detail");
    const top = el("div", "section v4g-top");
    top.append(el("p", "v4g-kicker", "선택한 컬렉션"), raw(el("h2", "ds-big", serifName(ds.title))));
    const sub = [ds.product, ds.purpose].filter(Boolean).join(" / ");
    if (sub) top.append(raw(el("p", "ds-sub", sub)));
    if (ds.description) top.append(raw(el("p", "lead", ds.description)));
    const acts = el("div", "actions");
    acts.append(button("디자인 시스템 고치기", "primary small", null, () => openSystemEditor(r)), button("복제", "small", null, () => duplicateSystem(r)), gallery.deleteButton("design_systems", r));
    top.append(acts);
    body.append(top);
    if (ds.colors?.length) {
      const s = section("팔레트", `${ds.colors.length}개`);
      s.append(swatches(ds.colors, 6), el("p", "ds-names", ds.colors.slice(0, 6).map((c) => c.name.toUpperCase()).join(" · ")));
      body.append(s);
    }
    if (ds.fonts?.length) {
      const s = section("글꼴");
      ds.fonts.forEach((f, i) => {
        const line = raw(el("p", ["ds-font-a", "ds-font-b", "ds-font-c"][Math.min(i, 2)], [`Aa  ${f}`, `가나다 · ${f}`, `012345 / ${f}`][Math.min(i, 2)]));
        line.style.fontFamily = `"${f.replaceAll('"', "")}", ${["serif", "var(--sans)", "var(--mono)"][Math.min(i, 2)]}`;
        s.append(line);
      });
      body.append(s);
    }
    if (ds.guide) {
      const s = section("쓰는 법·규칙");
      s.querySelector("h3").append(copyButton(ds.guide));
      const ol = el("ol", "ds-rules");
      ds.guide.split("\n").map((x) => x.trim()).filter(Boolean).forEach((line, i) => {
        const li = el("li");
        li.append(el("span", "", String(i + 1).padStart(2, "0")), raw(el("span", "", line)));
        ol.append(li);
      });
      s.append(ol);
      body.append(s);
    }
    if (ds.colors?.length) {
      const s = section("색상 값");
      const dl = el("dl", "ds-values");
      ds.colors.forEach((c) => dl.append(raw(el("dt", "", c.name)), raw(el("dd", "", c.value))));
      s.append(dl);
      body.append(s);
    }
    if (ds.source) {
      const s = el("div", "ds-source-row");
      s.append(raw(el("span", "ds-source", `${tr("출처")} · ${ds.source}`)));
      if (/^https?:\/\//.test(ds.source)) s.append(button("원본에서 열기  ↗", "ghost small", null, () => window.open(ds.source, "_blank")));
      s.append(copyButton(ds.source));
      body.append(s);
    }
    const foot = el("div", "v4g-foot");
    const links = el("div", "actions");
    links.append(button("보관함에서 빼기", "ghost", null, () => archiveSystem(r)));
    foot.append(links, el("p", "meta", `${shortDate(r.updated_at)} 수정 · ${r.created_by || ""}`));
    body.append(foot);
  }
  const systemDialog = $("#ds-dialog");
  let editingSystem = null;
  function fillList(id, values) {
    $(id).replaceChildren(...[...new Set(values.filter(Boolean))].map((v) => { const o = el("option"); o.value = v; return o; }));
  }
  function openSystemEditor(r) {
    if (!state.data.designSystems) { notice("서버를 새 버전으로 올린 뒤에 추가할 수 있어요."); return; }
    editingSystem = r;
    const ds = r?.design_system || { product: "", purpose: "" };
    $("#ds-dialog-title").textContent = r ? "디자인 시스템 고치기" : "디자인 시스템 추가";
    $("#ds-meta").hidden = !r;
    $("#ds-meta").textContent = r ? `수정 날짜 · ${shortDate(r.updated_at)} / 작성자 · ${r.created_by || ""}` : "";
    const set = (id, v) => { $(id).value = v ?? ""; };
    set("#ds-title", ds.title); set("#ds-product", ds.product); set("#ds-purpose", ds.purpose); set("#ds-description", ds.description); set("#ds-source", ds.source);
    set("#ds-colors", (ds.colors || []).map((c) => `${c.name} ${c.value}`).join("\n"));
    set("#ds-fonts", (ds.fonts || []).join(", ")); set("#ds-guide", ds.guide);
    const saved = state.data.designSystems.map((x) => x.design_system);
    fillList("#ds-products", ["Arkism", ...saved.map((x) => x.product)]);
    fillList("#ds-purposes", [...PURPOSES, ...saved.map((x) => x.purpose)]);
    $("#ds-error").textContent = "";
    drawColorChips();
    systemDialog.showModal();
  }
  // Under the 색 box: each line as a chip with its color, so a typo shows right away.
  function drawColorChips() {
    $("#ds-color-chips").replaceChildren(...parseColors($("#ds-colors").value).slice(0, 64).map((c) => {
      const chip = el("span", "color-chip");
      const dot = el("i");
      if (paintable(c.value)) dot.style.background = c.value; else chip.classList.add("bad");
      chip.append(dot, raw(el("span", "", c.name)));
      chip.title = c.value;
      return chip;
    }));
  }
  $("#ds-colors").addEventListener("input", drawColorChips);
  // One color per line: "name value", "name: value", or just a value.
  function parseColors(text) {
    return text.split("\n").map((l) => l.trim()).filter(Boolean).map((l, i) => {
      const m = !paintable(l) && /^([^\s:=]+)[\s:=]+([^\s:=].*)$/.exec(l);
      return m ? { name: m[1], value: m[2].trim() } : { name: `color-${i + 1}`, value: l };
    });
  }
  $("#ds-form").addEventListener("submit", async (e) => {
    if (e.submitter?.value !== "save") return;
    e.preventDefault();
    const val = (id) => $(id).value.trim();
    const system = {
      title: val("#ds-title"), product: val("#ds-product"), purpose: val("#ds-purpose"), description: val("#ds-description"), source: val("#ds-source"),
      colors: parseColors($("#ds-colors").value), fonts: val("#ds-fonts").split(",").map((x) => x.trim()).filter(Boolean), guide: $("#ds-guide").value.trim(),
      tags: editingSystem?.design_system.tags || []
    };
    try {
      const saved = editingSystem
        ? await request(`/v1/design_systems/${encodeURIComponent(editingSystem.id)}`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: editingSystem.revision, system }) })
        : await request("/v1/design_systems", { method: "POST", body: JSON.stringify({ scope: state.scope, system }) });
      systemDialog.close();
      state.data.designSystems = (await request(`/v1/design_systems?${query()}`)).items || [];
      state.selected = `ds:${saved.id}`;
      render();
      notice("저장했어요.");
    } catch (err) {
      $("#ds-error").textContent = { secret_detected: "키나 토큰처럼 보이는 값이 있어 저장하지 않았어요.", invalid_request: "이름과 색 줄(이름 값)을 확인해 주세요. 색은 64개까지예요.", revision_conflict: "다른 곳에서 먼저 고쳤어요. 닫고 다시 열어 주세요." }[err.message] || `저장하지 못했어요 (${err.message}).`;
    }
  });
  async function duplicateSystem(r) {
    if (!state.data.designSystems) return;
    const system = { ...r.design_system, title: copyTitle(r.design_system.title, 80) };
    try {
      const saved = await request("/v1/design_systems", { method: "POST", body: JSON.stringify({ scope: state.scope, system }) });
      state.data.designSystems = (await request(`/v1/design_systems?${query()}`)).items || [];
      state.selected = saved?.id ? `ds:${saved.id}` : null;
      render();
      notice("복제했어요. 사본을 고쳐서 쓰세요.");
    } catch (err) {
      notice({ secret_detected: "키나 토큰처럼 보이는 값이 있어 복제하지 않았어요.", invalid_request: "복제하지 못했어요. 이름과 색을 확인해 주세요." }[err.message] || `복제하지 못했어요 (${err.message}).`);
    }
  }
  async function archiveSystem(r) {
    if (!confirm(`'${r.design_system?.title || r.title || "이 디자인 시스템"}'을 보관함에서 뺄까요? 목록에서 사라지지만 기록은 남아요.`)) return;
    try {
      await request(`/v1/design_systems/${encodeURIComponent(r.id)}`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: r.revision, archive: true }) });
      state.data.designSystems = (await request(`/v1/design_systems?${query()}`)).items || [];
      state.selected = null;
      render();
      notice("보관함에서 뺐어요. 기록은 남아 있어요.");
    } catch (err) {
      notice(`처리하지 못했어요: ${err.message}`);
    }
  }

  // ---------------------------------------------------------------- MCP·플러그인
  // The host tool (arkism-agents.timer) applies a switch within a minute and reports back.
  const SWITCHABLE = { mcp: ["claude_code", "codex", "gemini", "zcode", "grok", "opencode", "cursor"], plugin: ["claude_code"] };
  async function loadTools() {
    try {
      const r = await request(`/v1/agent_tools?${query()}`);
      const t = r.agent_tools || {};
      return { servers: t.servers || [], plugins: t.plugins || [], requests: t.requests || [], collected_at: t.collected_at || "", skill_checks: t.skill_checks || [], skill_check_tools: t.skill_check_tools || "",
        agents: t.agents || null, shared_dirs: t.shared_dirs || null, memory_use: t.memory_use || null, skill_updates: t.skill_updates || [], pc_links: t.pc_links || [], skill_repos: t.skill_repos || [] };
    } catch (err) {
      // null tells renderTools to explain that the host sync has not reported (older server or stopped timer).
      console.warn("agent tools unavailable", err.message);
      return null;
    }
  }
  const pendingTool = (t) => (t?.requests || []).some((q) => q.status === "pending");
  const toolKind = (entry) => (entry.transport === "plugin" ? "plugin" : "mcp");
  function toolEntries() {
    const t = state.data.tools;
    return t ? [...t.servers, ...t.plugins] : [];
  }
  function requestsFor(entry) {
    return (state.data.tools?.requests || []).filter((q) => q.name === entry.name && q.kind === toolKind(entry));
  }
  const needsLogin = (entry, agent) => entry.agents?.[agent] === "on" && entry.login?.[agent] === "needed";
  function toolState(entry) {
    const pending = requestsFor(entry).filter((q) => q.status === "pending");
    if (pending.length) return ["적용 대기", "warn"];
    const login = Object.keys(entry.login || {}).filter((k) => needsLogin(entry, k));
    if (login.length) return [`${login.map(agentName).join("·")} 로그인 필요`, "warn"];
    if (entry.note) return [entry.note, "warn"];
    return Object.values(entry.agents).includes("on") ? ["정상", "ok"] : ["꺼짐", "off"];
  }
  async function refreshTools() {
    state.data.tools = await loadTools();
    renderStatusCard();
    render(false);
    renderDetail();
  }
  // 연결 상태 card: re-read the server's report every minute, so a host that stops reporting turns amber, then red.
  setInterval(async () => {
    if (!state.token || document.hidden) return;
    const g = generation; // a server or scope change while this was out makes the answer stale
    const t = await loadTools().catch(() => null);
    if (g !== generation) return;
    if (t) state.data.tools = t;
    renderStatusCard();
  }, 60000);
  let toolPoll = null;
  function pollTools() {
    clearTimeout(toolPoll);
    let left = 14; // about two minutes
    const tick = async () => {
      await refreshTools();
      if (pendingTool(state.data.tools) && --left > 0) toolPoll = setTimeout(tick, 9000);
    };
    toolPoll = setTimeout(tick, 9000);
  }
  async function switchTool(entry, agent, enabled) {
    try {
      const r = await request("/v1/agent_tools/requests", { method: "POST", body: JSON.stringify({ scope: state.scope, agent, kind: toolKind(entry), name: entry.name, enabled }) });
      state.data.tools.requests = r.agent_tools?.requests || state.data.tools.requests;
      render(false);
      renderDetail();
      notice(`${agentName(agent)}에서 ${entry.name} ${enabled ? "켜기" : "끄기"}를 요청했어요. 1분 안에 적용돼요.`);
      pollTools();
    } catch (err) {
      notice(`요청하지 못했어요: ${err.message}`);
    }
  }
  // 12 MCP 서버 추가: a program to run or an https address, added with each chosen agent's own CLI on the server.
  function splitArgs(line) {
    const out = [];
    const re = /"([^"]*)"|'([^']*)'|(\S+)/g;
    let m;
    while ((m = re.exec(line))) out.push(m[1] ?? m[2] ?? m[3]);
    return out;
  }
  function openAddServer() {
    const dlg = el("dialog", "crew-dialog mcp-add");
    const form = el("form");
    form.method = "dialog";
    const head = el("div", "dialog-head");
    const t = el("div", "");
    t.append(el("h2", "", "MCP 서버 추가"), el("p", "", "키나 토큰은 넣지 마세요. 로그인이 필요한 서버는 추가한 뒤 에이전트에서 인증해요."));
    head.append(t);
    const name = el("input", "mono");
    name.placeholder = "github";
    const how = el("select");
    for (const [k, l] of [["stdio", "프로그램 실행 (stdio)"], ["http", "주소 (http)"]]) { const o = el("option", "", l); o.value = k; how.append(o); }
    const cmd = el("input", "mono");
    cmd.placeholder = "npx -y @modelcontextprotocol/server-github";
    const url = el("input", "mono");
    url.placeholder = "https://mcp.example.com/mcp";
    const f = (label, input, hint) => { const l = el("label", "field", label); l.append(input); if (hint) l.append(el("small", "", hint)); return l; };
    const cmdF = f("실행할 프로그램", cmd), urlF = f("주소", url, "HTTP 연결은 https 주소만 사용할 수 있어요.");
    // 연결 방식: two pill buttons drive the select (the preset picker sets the select and calls sync).
    const howSeg = el("div", "segment mcp-how");
    const howBtns = [["stdio", "stdio", "프로그램 실행"], ["http", "http", "https 주소"]].map(([k, code, sub]) => {
      const b = el("button", "tab"); b.type = "button"; b.dataset.how = k;
      b.append(el("code", "", code), el("small", "", sub));
      b.addEventListener("click", () => { how.value = k; sync(); });
      howSeg.append(b);
      return b;
    });
    const sync = () => { cmdF.hidden = how.value !== "stdio"; urlF.hidden = how.value !== "http"; howBtns.forEach((b) => b.classList.toggle("active", b.dataset.how === how.value)); };
    how.addEventListener("change", sync);
    sync();
    const picked = new Set(["claude_code", "codex"]);
    const agents = el("div", "mcp-agents");
    for (const [k, n, spec] of AGENTS.filter(([k]) => SWITCHABLE.mcp.includes(k))) {
      const r = el("div", "mcp-agent");
      const tt = el("div", "");
      tt.append(el("span", "", n), el("small", "", "각 에이전트의 CLI로 서버에 추가해요"));
      const sw = el("button", "switch");
      sw.type = "button";
      sw.setAttribute("role", "switch");
      sw.setAttribute("aria-label", n);
      sw.append(el("i"));
      const draw = () => sw.setAttribute("aria-checked", String(picked.has(k)));
      draw();
      sw.addEventListener("click", () => { picked.has(k) ? picked.delete(k) : picked.add(k); draw(); });
      r.append(agentLogo(spec, n), tt, sw);
      agents.append(r);
    }
    // 빠른 선택: the catalog's presets as chips; picking one runs the catalog's own fill (name, method, program or address).
    const preset = catalog.presetField({ name, how, cmd, url, sync });
    const pick = preset.querySelector("select");
    const chips = el("div", "mcp-presets");
    for (const o of [...pick.options].filter((x) => x.value)) {
      const p = (window.ArkismCatalog.PRESETS || []).find((x) => x.id === o.value);
      const c = el("button", "mcp-preset"); c.type = "button";
      c.append(el("code", "", p?.name || o.value), el("small", "", p?.how === "http" ? "주소" : "프로그램"));
      c.addEventListener("click", () => { pick.value = o.value; pick.dispatchEvent(new Event("change")); chips.querySelectorAll(".mcp-preset").forEach((x) => x.classList.toggle("on", x === c)); });
      chips.append(c);
    }
    pick.hidden = true;
    preset.firstChild.textContent = "빠른 선택";
    preset.insertBefore(chips, pick);
    preset.append(el("small", "", "고르면 이름·방식·프로그램(또는 주소)이 한 번에 채워져요."));
    const howF = el("div", "field");
    howF.append("연결 방식", howSeg);
    const agentsF = el("div", "field");
    agentsF.append("넣을 에이전트", agents);
    const main = el("div", "mcp-main");
    main.append(preset, f("이름", name, "영문·숫자·@._/- · 최대 128자"), howF, urlF, cmdF, agentsF, el("p", "mcp-warn", "키와 토큰은 이 입력칸에 넣지 마세요."));
    // Right column: the add requests sent lately, and what the two methods mean.
    const side = el("aside", "mcp-side");
    const reqs = (state.data.tools?.requests || []).filter((q) => ["mcp_add", "mcp", "mcp_login"].includes(q.kind)).slice(-5).reverse();
    const sideHead = (title, aside) => { const h = el("div", "cn-sec-head"); h.append(el("h3", "", title)); if (aside) h.append(el("small", "", aside)); return h; };
    side.append(sideHead("최근 요청", reqs.length ? `${reqs.length}건` : ""));
    if (reqs.length) {
      const box = el("div", "cn-sublist");
      const label = { pending: ["적용 대기", "warn"], done: ["적용됨", ""], failed: ["실패", "warn"], waiting: ["로그인 대기", "warn"] };
      for (const q of reqs) {
        const r = el("div", "cn-subrow");
        const what = q.kind === "mcp_add" ? "추가" : q.kind === "mcp_login" ? "로그인" : q.enabled ? "켜기" : "끄기";
        const [l, c] = label[q.status] || [q.status, ""];
        const tt = el("div", "cn-subrow-body"); tt.append(raw(el("span", "", `${q.name} ${what}`)));
        r.append(tt, cnBadge(q.error ? `${l} · ${q.error}` : l, c));
        box.append(r);
      }
      side.append(box, el("p", "cn-note", "적용될 때까지 상태를 다시 읽어요."));
    } else side.append(el("p", "cn-note", "아직 보낸 요청이 없어요."));
    const ways = el("dl", "mcp-ways");
    ways.append(el("dt", "", "stdio"), el("dd", "", "서버 컴퓨터에서 프로그램을 실행해요. Node.js(npx)나 uv(uvx)가 필요할 수 있어요."),
      el("dt", "", "http"), el("dd", "", "원격 서버 주소로 연결해요. 로그인이 필요하면 추가한 뒤 인증해요."));
    side.append(sideHead("방식 고르기"), ways);
    const body = el("div", "dialog-body");
    body.append(main, side);
    const foot = el("div", "dialog-foot");
    const cancel = button("취소", "");
    cancel.type = "submit";
    cancel.value = "cancel";
    const ok = button("추가 요청", "primary");
    ok.type = "submit";
    ok.value = "ok";
    foot.append(cancel, ok);
    form.append(head, body, foot);
    form.addEventListener("submit", async (e) => {
      if (e.submitter?.value !== "ok") return;
      e.preventDefault();
      const n = name.value.trim();
      const extra = how.value === "http" ? { url: url.value.trim() } : { command: splitArgs(cmd.value.trim()) };
      if (!/^[A-Za-z0-9@._/-]{1,128}$/.test(n) || (how.value === "http" ? !extra.url.startsWith("https://") : !extra.command.length) || !picked.size) {
        notice("이름, 프로그램이나 https 주소, 에이전트를 확인해 주세요.");
        return;
      }
      ok.disabled = true;
      try {
        let r;
        for (const agent of picked) r = await request("/v1/agent_tools/requests", { method: "POST", body: JSON.stringify({ scope: state.scope, agent, kind: "mcp_add", name: n, enabled: true, ...extra }) });
        state.data.tools.requests = r.agent_tools?.requests || state.data.tools.requests;
        dlg.close();
        notice(`${n} 추가를 요청했어요. 서버가 1분 안에 추가해요.`);
        render(false);
        pollTools();
        catalog.watchAdd(r, n);
      } catch (error_) { notice(error_.message === "secret_detected" ? "키나 토큰처럼 보이는 값이 있어요. 빼고 다시 넣어 주세요." : `요청하지 못했어요 (${error_.message}).`); ok.disabled = false; }
    });
    dlg.append(form);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal();
  }
  const CONFIG_FILES = { claude_code: "~/.claude.json", codex: "~/.codex/config.toml" };
  function toolLogos(entry) {
    const on = AGENTS.filter(([k]) => entry.agents[k] === "on");
    const box = el("span", "cn-logos");
    if (!on.length) box.append(el("span", "cn-quiet", "에이전트 없음"));
    on.forEach(([, n, logo]) => box.append(agentLogo(logo, n)));
    return box;
  }
  // v4 · 연결 (Figma 05): small builders shared by 도구·에이전트 screens.
  function cnTile(label, value, unit, warn, onClick) {
    const t = el(onClick ? "button" : "div", `cn-tile${onClick ? " link" : ""}`);
    if (onClick) { t.type = "button"; t.addEventListener("click", onClick); }
    const v = el("div", "cn-tile-value");
    v.append(el("strong", warn ? "warn" : "", value));
    if (unit) v.append(el("span", "", unit));
    t.append(el("p", "", label), v);
    return t;
  }
  function cnBadge(text, cls = "") { return el("span", `cn-badge ${cls}`.trim(), text); }
  // A list row: div (it can hold its own buttons), Enter/Space open it like a click.
  function cnRow(id, parts, onOpen) {
    const r = el("div", `cn-row${state.selected === id ? " selected" : ""}`);
    r.tabIndex = 0;
    r.setAttribute("role", "button");
    r.dataset.id = id;
    r.append(...parts);
    const open = () => { state.selected = id; document.querySelectorAll(".cn-row[data-id]").forEach((x) => x.classList.toggle("selected", x.dataset.id === id)); onOpen(); openDetail(); };
    r.addEventListener("click", (e) => { if (!e.target.closest(".cn-mini")) open(); });
    r.addEventListener("keydown", (e) => { if ((e.key === "Enter" || e.key === " ") && e.target === r) { e.preventDefault(); open(); } });
    return r;
  }
  function cnRowBody(title, meta, extra) {
    const b = el("div", "cn-row-body");
    const t = el("div", "cn-row-title");
    t.append(...[].concat(title));
    b.append(t);
    const line = (cls, v) => { const d = el("div", cls); d.append(v); return d; };
    if (extra) b.append(line("cn-row-desc", extra));
    if (meta) b.append(line("cn-row-meta", meta));
    return b;
  }
  function cnMini(label, onClick) {
    const b = el("button", "cn-mini", label);
    b.type = "button";
    b.addEventListener("click", (e) => { e.stopPropagation(); onClick(); });
    return b;
  }
  const BADGE = { ok: "", warn: "warn", off: "off" };
  // Which summary tile a server counts under (Figma: 정상 · 로그인 필요 · 끊김 · 꺼짐).
  function toolGroup(entry) {
    if (AGENTS.some(([k]) => needsLogin(entry, k))) return "login";
    if (entry.note) return "broken";
    return Object.values(entry.agents || {}).includes("on") ? "ok" : "off";
  }
  function renderTools() {
    const t = state.data.tools;
    const plugin = state.toolTab === "plugin";
    const table = $("#table");
    ["#controls", "#chips"].forEach((sel) => $(sel).replaceChildren());
    if (!t) {
      headExtras();
      $("#stats").replaceChildren();
      table.replaceChildren(el("div", "empty", "MCP 목록을 불러오지 못했어요. 서버의 arkism-agents.timer가 돌고 있는지 확인해 주세요."));
      return;
    }
    const seg = segment([["plugin", "플러그인", t.plugins.length], ["mcp", "MCP 서버", t.servers.length]], state.toolTab, (k) => { state.toolTab = k; state.selected = null; render(); });
    headExtras({ count: `${(plugin ? t.plugins : t.servers).length}개`, aside: [seg] });
    const age = t.collected_at ? Date.now() - new Date(t.collected_at).getTime() : Infinity;
    if (age > 5 * 60000) notice("서버에서 최근 5분 동안 목록이 올라오지 않았어요. arkism-agents.timer를 확인해 주세요.");
    if (plugin) $("#stats").replaceChildren();
    else {
      const n = (g) => t.servers.filter((e) => toolGroup(e) === g).length;
      const tiles = el("div", "cn-tiles");
      tiles.append(cnTile("정상", String(n("ok")), "개"), cnTile("로그인 필요", String(n("login")), "개", n("login") > 0), cnTile("끊김", String(n("broken")), "개", n("broken") > 0), cnTile("꺼짐", String(n("off")), "개"));
      $("#stats").replaceChildren(tiles);
    }
    // 에이전트: only what that agent has on.
    const keys = shownAgents().map(([k]) => k);
    if (!keys.includes(state.toolAgent)) state.toolAgent = "";
    const who = state.toolAgent;
    $("#controls").replaceChildren(skSelect("에이전트", [["", "모든 에이전트"], ...keys.map((k) => [k, `${agentName(k)} ${(plugin ? t.plugins : t.servers).filter((e) => e.agents?.[k] === "on").length}`])], who, (k) => { state.toolAgent = k; state.selected = null; render(); }));
    if (who && !AGENT_SYNC[who]) $("#chips").replaceChildren(el("p", "cn-note", `${agentName(who)}의 MCP 설정은 아직 읽지 않아요. 켜고 끄기는 Claude Code·Codex만 돼요.`));
    const list = (plugin ? t.plugins : t.servers).filter((e) => (!who || e.agents?.[who] === "on") && matches(`${e.name} ${e.target || ""} ${e.desc || ""}`));
    if (!state.selected && list.length) state.selected = `tool:${(list.find((e) => toolState(e)[1] === "warn") || list[0]).name}`;
    table.className = "cn-list";
    table.replaceChildren(...list.map((e) => {
      const [label, cls] = toolState(e);
      const ic = el("span", "cn-row-icon"); ic.append(icon(plugin ? "puzzle-piece" : e.name === "arkism" ? "brain" : "plug"));
      const tools = (e.tools || []).length;
      const users = toolUsers(e).length;
      const parts = [ic];
      if (plugin) {
        const counts = {};
        (e.contents || []).forEach((it) => { const k = ITEM_KIND[it.kind] || it.kind; counts[k] = (counts[k] || 0) + 1; });
        const title = [raw(el("span", "", e.name))];
        if (e.version) title.push(el("span", "cn-ver", `v${e.version}`));
        parts.push(cnRowBody(title, Object.entries(counts).map(([k, c]) => `${k} ${c}`).join(" · ") || "", e.desc ? raw(el("span", "", e.desc)) : null));
      } else {
        parts.push(cnRowBody(raw(el("span", "mono", e.name)), [e.desc, tools ? `도구 ${tools}개` : "", users ? `워크플로우 ${users}개` : ""].filter(Boolean).join(" · ")));
      }
      parts.push(toolLogos(e));
      const loginFor = AGENTS.map(([k]) => k).find((k) => needsLogin(e, k));
      if (loginFor) parts.push(cnMini("로그인", () => toolAgentPopup(e, toolKind(e), loginFor, true)));
      else if (!plugin && e.note && reconnectAgents(e).length) parts.push(cnMini("다시 연결", () => reconnectTool(e)));
      parts.push(cnBadge(label, BADGE[cls]));
      return cnRow(`tool:${e.name}`, parts, renderDetail);
    }));
    if (!list.length) table.append(el("div", "empty", "여기에 표시할 항목이 없어요."));
  }
  function toolsSummary() {
    const t = state.data.tools;
    const body = detailShell("MCP·플러그인");
    const d = el("div", "cn-detail");
    d.append(el("p", "cn-kind", "MCP·플러그인"), el("h2", "cn-title", "도구 서버와 플러그인"), el("p", "cn-desc", "서버의 Claude Code·Codex 설정을 1분마다 읽어 보여 줘요. 항목을 고르면 에이전트별로 켜고 끌 수 있어요."));
    if (t?.collected_at) d.append(el("p", "cn-desc", `마지막 확인 ${ago(t.collected_at)}`));
    body.append(d);
  }
  // A styled yes/no box (Figma 확인 팝업 253:18954) in place of the browser's confirm().
  function confirmPop(title, text, okLabel) {
    return new Promise((resolve) => {
      const dlg = el("dialog", "cn-confirm");
      const form = el("form");
      form.method = "dialog";
      const row = el("div", "cn-confirm-btns");
      const no = button("취소", "");
      no.value = "no"; no.type = "submit";
      const ok = button(okLabel, "danger");
      ok.value = "ok"; ok.type = "submit";
      row.append(no, ok);
      form.append(el("h2", "", title), el("p", "", text), row);
      dlg.append(form);
      dlg.addEventListener("close", () => { resolve(dlg.returnValue === "ok"); dlg.remove(); });
      document.body.append(dlg);
      dlg.showModal();
      no.focus();
    });
  }
  function switchNote(waiting, can, login) {
    if (waiting) return waiting.enabled ? " · 켜는 중" : " · 끄는 중";
    if (login) return " · 로그인 필요";
    return can ? "" : " · 지원 안 함";
  }
  function switchTitle(can, on) {
    if (!can) return "이 에이전트는 아직 켜고 끌 수 없어요";
    return on ? "누르면 꺼요" : "누르면 켜요";
  }
  function toolSwitches(entry, kind) {
    const pendingFor = (agent) => requestsFor(entry).find((q) => q.status === "pending" && q.agent === agent);
    const ag = cnSection(kind === "plugin" ? "켜 둔 에이전트" : "에이전트", `${AGENTS.filter(([k]) => entry.agents[k] === "on").length} / ${AGENTS.length}`);
    const grid = el("div", "cn-chips");
    for (const [k, n, logo] of AGENTS) {
      const can = SWITCHABLE[kind].includes(k);
      const on = entry.agents[k] === "on";
      const waiting = pendingFor(k);
      const login = needsLogin(entry, k);
      const cls = ["cn-chip", on ? "on" : "off", (waiting || login) && "staged"].filter(Boolean);
      const a = el("button", cls.join(" "));
      a.type = "button";
      a.disabled = !can;
      a.append(agentLogo(logo, n), n + switchNote(waiting, can, login));
      a.title = can ? "누르면 어떻게 적용되는지 보여 줘요" : switchTitle(can, on);
      if (can) a.addEventListener("click", () => toolAgentPopup(entry, kind, k));
      grid.append(a);
    }
    ag.append(grid, el("p", "cn-note", "에이전트를 누르면 켜기·끄기·로그인을 해요. 노란 칸은 할 일이 남았어요."));
    return ag;
  }
  // A small window that says what clicking an agent does, where it lands on the server, and does it.
  function applyPopup(title, lead, rows, actions) {
    const dlg = el("dialog", "apply-pop");
    const form = el("form");
    form.method = "dialog";
    const head = el("div", "dialog-head");
    const ht = el("div", "");
    ht.append(el("h2", "", title), el("p", "", lead));
    const close = el("button", "icon-btn plain");
    close.value = "cancel";
    close.setAttribute("aria-label", "닫기");
    close.append(icon("x"));
    head.append(ht, close);
    const body = el("div", "dialog-body");
    const steps = el("ol", "apply-steps");
    rows.forEach((r) => steps.append(el("li", "", r)));
    const status = el("p", "apply-status");
    status.setAttribute("role", "status");
    status.hidden = true;
    body.append(steps, status);
    const foot = el("div", "dialog-foot");
    foot.append(...actions(dlg, (text, cls = "") => { status.hidden = !text; status.className = `apply-status ${cls}`; status.textContent = text || ""; }));
    form.append(head, body, foot);
    dlg.append(form);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal();
    return { dlg, status, body };
  }
  function toolAgentPopup(entry, kind, agent, startLogin = false) {
    const n = agentName(agent);
    const on = entry.agents[agent] === "on";
    const login = needsLogin(entry, agent);
    const remote = kind === "mcp" && entry.transport !== "stdio";
    const file = kind === "plugin" ? "~/.claude/plugins" : CONFIG_FILES[agent];
    let now = `지금 ${n}에서 꺼져 있어요.`;
    if (on) now = login ? `지금 ${n}에서 켜져 있어요. 그런데 로그인이 안 돼서 아직 못 써요.` : `지금 ${n}에서 켜져 있어요.`;
    const rows = [
      now,
      `켜고 끄면 서버의 ${file}에 넣고 빼요. 1분 안에 적용되고, ${n}을 새로 열면 바로 써요.`,
      remote ? "이 서버는 로그인이 필요할 수 있어요. 로그인을 누르면 브라우저가 열리고, 허용하면 서버에 자동으로 적용돼요." : null
    ].filter(Boolean);
    let busy = false;
    const pop = applyPopup(`${n} · ${entry.name}`, kind === "plugin" ? "플러그인" : "MCP 서버", rows, (dlg, show) => {
      const out = [];
      const sw = button(on ? "끄기" : "켜기", on ? "ghost" : "primary", null, async () => { dlg.close(); await switchTool(entry, agent, !on); });
      if (remote && on) {
        const lg = button(login ? "로그인" : "다시 로그인", login ? "primary" : "", "lock-simple", async () => {
          if (busy) return;
          busy = true;
          lg.disabled = sw.disabled = true;
          try {
            await mcpLogin(entry, agent, show, pop);
            show(`${n}에 로그인했어요. 이제 ${entry.name}을 써요.`, "ok");
            await refreshTools();
            setTimeout(() => dlg.close(), 1500);
          } catch (e) {
            show(`로그인하지 못했어요: ${e.message}`, "error");
          } finally {
            busy = false;
            lg.disabled = sw.disabled = false;
          }
        });
        out.push(lg);
      }
      out.push(sw);
      return out;
    });
    if (startLogin && remote && on) pop.dlg.querySelector(".dialog-foot .btn")?.click();
  }
  async function waitRequest(id, done, seconds) {
    const end = Date.now() + seconds * 1000;
    while (Date.now() < end) {
      const t = await loadTools();
      const q = (t?.requests || []).find((x) => x.id === id);
      if (q && done(q)) return q;
      await new Promise((r) => setTimeout(r, 2000));
    }
    throw new Error("서버가 답하지 않아요. 잠시 뒤 다시 해 주세요.");
  }
  // MCP sign-in: the server runs "<agent> mcp login NAME --no-browser"; this app opens the sign-in page and
  // catches the redirect on the PC (the CLI's loopback port), then hands it back for the server to replay.
  async function mcpLogin(entry, agent, show, pop) {
    show("서버에 로그인 준비를 부탁했어요. 1분 안에 로그인 창이 열려요…");
    const r = await request("/v1/agent_tools/requests", { method: "POST", body: JSON.stringify({ scope: state.scope, agent, kind: "mcp_login", name: entry.name, enabled: true }) });
    const mine = (r.agent_tools?.requests || []).findLast((q) => q.kind === "mcp_login" && q.agent === agent && q.name === entry.name);
    if (!mine) throw new Error("요청을 찾지 못했어요");
    const ready = await waitRequest(mine.id, (q) => q.status !== "pending", 120);
    if (ready.status !== "waiting") throw new Error(ready.error || "로그인 창을 열지 못했어요");
    let target;
    if (tauri) {
      show("브라우저에서 로그인하고 허용을 눌러 주세요. 끝나면 이 창으로 돌아와요.");
      target = await tauri.invoke("mcp_login", { authUrl: ready.auth_url }).catch((e) => {
        const m = String(e);
        if (m.includes("port_busy")) throw new Error("이 PC에서 로그인 주소를 받을 칸이 이미 쓰이고 있어요");
        throw new Error(m.includes("timeout") ? "10분 안에 로그인하지 않았어요" : m);
      });
    } else {
      target = await pastedRedirect(pop, ready.auth_url, show);
    }
    show("로그인을 마무리하는 중이에요…");
    await request(`/v1/agent_tools/requests/${encodeURIComponent(mine.id)}/login`, { method: "POST", body: JSON.stringify({ scope: state.scope, callback: target }) });
    const end = await waitRequest(mine.id, (q) => q.status === "done" || q.status === "failed", 90);
    if (end.status === "failed") throw new Error(end.error || "실패");
  }
  // A plain browser (development) cannot listen on the PC: open the page, paste the address it ends on.
  function pastedRedirect(pop, authUrl, show) {
    return new Promise((resolve, reject) => {
      const a = el("a", "", "로그인 창 열기");
      a.href = authUrl;
      a.target = "_blank";
      a.rel = "noreferrer";
      const input = el("input");
      input.placeholder = "허용 후 주소창의 http://localhost:… 주소를 붙여 넣으세요";
      const go = button("확인", "primary", null, () => {
        const u = URL.canParse(input.value.trim()) ? new URL(input.value.trim()) : null;
        if (!u || !["localhost", "127.0.0.1"].includes(u.hostname)) {
          show("http://localhost: 로 시작하는 주소를 붙여 넣어 주세요.", "error");
          return;
        }
        box.remove();
        resolve(u.pathname + u.search);
      });
      const box = el("div", "apply-paste");
      box.append(a, input, go);
      pop.body.append(box);
      pop.dlg.addEventListener("close", () => reject(new Error("창을 닫았어요")));
      show("로그인 창을 열고 허용한 뒤, 주소를 아래에 붙여 넣어 주세요.");
    });
  }
  function cnSection(title, aside) {
    const s = el("section", "cn-sec");
    const h = el("div", "cn-sec-head");
    h.append(el("h3", "", title));
    if (aside) h.append(el("small", "", aside));
    s.append(h);
    return s;
  }
  function cnFacts(pairs) {
    const dl = el("dl", "cn-facts");
    pairs.filter(([, v]) => v).forEach(([k, v, cls]) => dl.append(el("dt", "", k), el("dd", cls || "", v)));
    return dl;
  }
  function cnCallout(title, text, buttons = []) {
    const c = el("div", "cn-callout");
    const p = el("p", "");
    if (title) p.append(el("strong", "", title), " ");
    if (text) p.append(text);
    c.append(p);
    if (buttons.length) { const b = el("div", "cn-btns"); b.append(...buttons); c.append(b); }
    return c;
  }
  function toolHistory(entry) {
    const hist = requestsFor(entry).slice(-5).reverse();
    if (!hist.length) return null;
    const sec = cnSection("최근 요청", `${hist.length}건`);
    const label = { pending: ["적용 대기", "warn"], done: ["적용됨", ""], failed: ["실패", "warn"] };
    const box = el("div", "cn-sublist");
    hist.forEach((q) => {
      const r = el("div", "cn-subrow");
      const t = el("div", "cn-subrow-body");
      t.append(el("span", "", `${agentName(q.agent)} ${q.enabled ? "켜기" : "끄기"}`), el("small", "", [ago(q.requested_at), q.error].filter(Boolean).join(" · ")));
      const [l, c] = label[q.status] || [q.status, ""];
      r.append(t, cnBadge(l, c));
      box.append(r);
    });
    sec.append(box);
    return sec;
  }
  function toolDetail(entry) {
    const kind = toolKind(entry);
    const [label] = toolState(entry);
    const body = detailShell(kind === "plugin" ? "플러그인" : "MCP 서버");
    const d = el("div", "cn-detail");
    const head = el("div", "cn-head");
    head.append(el("p", "cn-kind", [kind === "plugin" ? "플러그인" : "MCP 서버", entry.version ? `v${entry.version}` : "", label].filter(Boolean).join(" · ")),
      raw(el("h2", `cn-title${kind === "plugin" ? "" : " mono"}`, entry.name)));
    if (entry.desc) head.append(raw(el("p", "cn-desc", entry.desc)));
    const parts = [head];
    const loginFor = AGENTS.map(([k]) => k).filter((k) => needsLogin(entry, k));
    if (loginFor.length) {
      parts.push(cnCallout(`${loginFor.map(agentName).join("·")} 로그인 필요`, "누르면 로그인 창이 열려요. Cloudflare 같은 서비스에서 허용하면 서버의 에이전트에 자동으로 적용돼요.",
        loginFor.map((k) => button(`${agentName(k)} 로그인`, "primary", null, () => toolAgentPopup(entry, kind, k, true)))));
    } else if (entry.note) {
      parts.push(cnCallout(entry.note, kind === "plugin" ? "Claude Code에서 /plugin 으로 다시 확인해 주세요." : "‘다시 연결’을 누르거나, 서버의 에이전트에서 /mcp 를 열어 다시 인증해 주세요. 끝나면 1분 안에 여기 반영돼요.",
        kind === "mcp" && reconnectAgents(entry).length ? [button("다시 연결", "primary", null, () => reconnectTool(entry))] : []));
    }
    const btns = el("div", "cn-btns");
    const files = kind === "plugin" ? "~/.claude/plugins" : [...new Set(Object.keys(entry.agents || {}).filter((k) => entry.agents[k] === "on").map((k) => CONFIG_FILES[k]).filter(Boolean))].join("\n");
    if (kind === "mcp" && reconnectAgents(entry).length && (loginFor.length || !entry.note)) btns.append(button("다시 연결", "", null, () => reconnectTool(entry)));
    // The config sits on the server, so the app hands over its path (copy) rather than opening it here.
    if (files) { const cp = copyButton(files, "설정 파일 경로 복사", ""); cp.title = files; btns.append(cp); }
    if (btns.childElementCount) parts.push(btns);
    const conn = cnSection("연결");
    const seen = state.data.tools?.collected_at;
    conn.append(cnFacts([["방식", kind === "plugin" ? "Claude Code 플러그인" : [entry.transport, entry.target].filter(Boolean).join(" · "), "mono"],
      ["설정 파일", kind === "plugin" ? "~/.claude/plugins" : Object.keys(entry.agents || {}).map((k) => CONFIG_FILES[k]).filter(Boolean).join(" · "), "mono"],
      ["마지막 확인", seen ? ago(seen) : "—"]]));
    if (kind === "plugin") parts.push(pluginContents(entry), toolSwitches(entry, kind));
    else parts.push(conn, toolSwitches(entry, kind), serverTools(entry), serverCalls(entry));
    if (kind === "plugin") parts.push(conn);
    parts.push(toolExperts(entry));
    const hist = toolHistory(entry);
    if (hist) parts.push(hist);
    const on = Object.entries(entry.agents || {}).filter(([, v]) => v === "on").map(([k]) => k);
    const foot = el("div", "cn-foot");
    if (on.length) foot.append(button(kind === "plugin" ? "끄기" : "연결 끊기", "cn-link", null, async () => {
      if (!await confirmPop(kind === "plugin" ? "플러그인을 끌까요?" : "연결 끊기할까요?", `${entry.name}를 ${on.map(agentName).join("·")}에서 끌까요? 설정은 보관돼서 다시 켜면 그대로 돌아와요.`, kind === "plugin" ? "끄기" : "연결 끊기")) return;
      for (const k of on) await switchTool(entry, k, false);
    }));
    if (kind === "plugin") foot.append(el("span", "cn-grow"), button("제거", "cn-link", null, async () => {
      if (!await confirmPop("플러그인을 제거할까요?", `${entry.name} 플러그인을 Claude Code에서 지울까요? 다시 쓰려면 새로 설치해야 해요.`, "제거")) return;
      try {
        const r = await request("/v1/agent_tools/requests", { method: "POST", body: JSON.stringify({ scope: state.scope, agent: "claude_code", kind: "plugin_remove", name: entry.name, enabled: false }) });
        state.data.tools.requests = r.agent_tools?.requests || state.data.tools.requests;
        notice(`${entry.name} 제거를 요청했어요. 1분 안에 지워져요.`);
        render(false);
        pollTools();
      } catch (e) { notice(`요청하지 못했어요 (${e.message}).`); }
    }));
    if (foot.childElementCount) parts.push(foot);
    d.append(...parts);
    body.append(d);
  }
  // 다시 연결: a server that signs in is signed in again (mcp_login); a program server is taken out of each
  // agent's config and put back (two switches, one after the other), so the agent starts it fresh next time.
  const reconnectAgents = (entry) => Object.keys(entry.agents || {}).filter((k) => entry.agents[k] === "on" && SWITCHABLE.mcp.includes(k));
  async function sendSwitch(entry, agent, enabled) {
    const r = await request("/v1/agent_tools/requests", { method: "POST", body: JSON.stringify({ scope: state.scope, agent, kind: "mcp", name: entry.name, enabled }) });
    state.data.tools.requests = r.agent_tools?.requests || state.data.tools.requests;
    const mine = (r.agent_tools?.requests || []).findLast((q) => q.kind === "mcp" && q.agent === agent && q.name === entry.name && q.enabled === enabled);
    if (!mine) throw new Error("요청을 찾지 못했어요");
    const end = await waitRequest(mine.id, (q) => q.status === "done" || q.status === "failed", 120);
    if (end.status === "failed") throw new Error(end.error || "실패");
  }
  function reconnectTool(entry) {
    const on = reconnectAgents(entry);
    if (entry.transport !== "stdio") return toolAgentPopup(entry, "mcp", on.find((k) => needsLogin(entry, k)) || on[0], true);
    const names = on.map(agentName).join("·");
    let busy = false;
    applyPopup(`다시 연결 · ${entry.name}`, "MCP 서버", [
      `${names} 설정에서 ${entry.name}을 뺐다가 다시 넣어요. 설정 내용은 그대로예요.`,
      "한 번에 1분쯤 걸려요. 끝나면 에이전트를 새로 열어야 다시 붙어요."
    ], (dlg, show) => {
      const go_ = button("다시 연결", "primary", null, async () => {
        if (busy) return;
        busy = true;
        go_.disabled = true;
        try {
          for (const k of on) {
            show(`${agentName(k)}에서 빼는 중…`);
            await sendSwitch(entry, k, false);
            show(`${agentName(k)}에 다시 넣는 중…`);
            await sendSwitch(entry, k, true);
          }
          show(`다시 연결했어요. ${names}을 새로 열면 바로 써요.`, "ok");
          await refreshTools();
          setTimeout(() => dlg.close(), 1500);
        } catch (e) {
          show(`다시 연결하지 못했어요: ${e.message}`, "error");
          void refreshTools();
        } finally {
          busy = false;
          go_.disabled = false;
        }
      });
      return [go_];
    });
  }
  // 12b 들어 있는 것: the skills, MCP servers and commands a plugin bundles (read from its folder on the server).
  const ITEM_KIND = { skill: "스킬", mcp: "MCP", command: "명령" };
  function itemRows(list) {
    const box = el("div", "cn-sublist");
    for (const it of list) {
      const r = el("div", "cn-subrow");
      if (it.label) r.append(el("span", "cn-kindtag", it.label));
      const t = el("div", "cn-subrow-body");
      t.append(raw(el("code", "", it.name)));
      if (it.desc) t.append(raw(el("small", "", it.desc)));
      r.append(t);
      if (it.risky) r.append(cnBadge("확인", "warn"));
      box.append(r);
    }
    return box;
  }
  function pluginContents(entry) {
    const items = entry.contents || [];
    const s = cnSection("들어 있는 것", items.length ? `${items.length}개` : "");
    s.append(items.length ? itemRows(items.map((it) => ({ ...it, label: ITEM_KIND[it.kind] || it.kind })))
      : el("p", "cn-note", "서버가 아직 이 플러그인 폴더를 읽지 않았어요."));
    return s;
  }
  // 12 도구: what the server lists, ones that change or send something first; 최근 호출: today's calls in the transcripts.
  function serverTools(entry) {
    const tools = [...(entry.tools || [])].sort((a, b) => Number(!!b.risky) - Number(!!a.risky));
    const risky = tools.filter((t) => t.risky).length;
    const s = cnSection("제공 도구", tools.length ? `${tools.length}개` : "");
    if (!tools.length) {
      const why = entry.transport === "stdio" ? "서버가 하루에 한 번 도구 목록을 물어봐요. 아직 답을 받지 못했어요." : "주소로 연결하는 서버는 로그인이 필요해 도구 목록을 읽지 않아요.";
      s.append(el("p", "cn-note", why));
      return s;
    }
    if (risky) s.append(el("p", "cn-note", `‘확인’ 표시는 바꾸거나 보내는 도구예요 · ${risky}개`));
    const list = itemRows(tools.slice(0, 5));
    s.append(list);
    if (tools.length > 5) {
      const more = button(`${tools.length - 5}개 더 보기`, "cn-link");
      more.addEventListener("click", () => { list.replaceWith(itemRows(tools)); more.remove(); });
      s.append(more);
    }
    return s;
  }
  function serverCalls(entry) {
    const c = entry.calls;
    const s = cnSection("최근 호출", c ? `오늘 ${c.today}번` : "");
    if (!c?.recent?.length) { s.append(el("p", "cn-note", "오늘은 이 서버의 도구를 부르지 않았어요.")); return s; }
    for (const call of c.recent) {
      const r = el("div", "cn-call");
      const who = AGENTS.find(([k]) => k === call.agent);
      if (who) r.append(agentLogo(who[2], who[1]));
      r.append(el("code", "", call.tool), el("span", "cn-grow"), el("span", call.ok ? "cn-muted" : "cn-warn", call.ok ? "성공" : "실패"), el("span", "cn-quiet", ago(call.at)));
      s.append(r);
    }
    return s;
  }
  // 쓰는 워크플로우: workflows with an AI step whose 울타리 or tools name this server or plugin.
  function toolUsers(entry) {
    const uses = (n) => ["ai", "ai_once"].includes(n.type) && `${n.config?.tools || ""} ${n.config?.fence || ""}`.includes(entry.name);
    return (state.data.crew || []).filter((r) => r.crew?.type === "workflow" && (r.crew.graph?.nodes || []).some(uses));
  }
  function toolExperts(entry) {
    const users = toolUsers(entry);
    const s = cnSection("쓰는 워크플로우");
    s.append(users.length ? el("p", "cn-text", users.map((r) => `$${r.crew.handle}`).join(", ")) : el("p", "cn-note", "아직 없어요 · AI 노드의 울타리에 도구 이름을 적으면 여기 보여요"));
    return s;
  }
  // ---------------------------------------------------------------- detail
  function detailShell(label) {
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
      if (run) return promptRunDetail(run);
      const p = state.data.prompts.find((x) => `prompt:${x.id}` === id);
      return p ? promptDetail(p) : imagesSummary();
    }
    if (state.view === "crew") return crew.detail();
    if (state.view === "reviews") return reviews.detail();
    if (state.view === "design") {
      const r = (state.data.designSystems || []).find((x) => `ds:${x.id}` === id);
      return r ? systemDetail(r) : librarySummary();
    }
    if (state.view === "tools") {
      const t = toolEntries().find((x) => `tool:${x.name}` === id);
      return t ? toolDetail(t) : toolsSummary();
    }
    if (state.view === "skills") {
      if (state.skillPage) return skillPageDetail();
      const s = [...trashed(), ...state.data.skills].find((x) => `skill:${x.id}` === id);
      return s ? skillDetailV4(s) : summaryDetail();
    }
    if (state.view === "memories" && id === "inject") return inject.detail();
    if (state.view === "memories" && id === "batch") return reviewBatchDetail(detailShell("한 번에 처리"));
    if (state.view === "memories" && id) {
      const all = [...state.data.memories, ...state.data.candidates, ...state.data.conflicted];
      const m = all.find((x) => x.id === id);
      if (m) return m.status === "active" ? memoryDetail(m) : candidateDetail(m);
    }
    summaryDetail();
  }
  function summaryDetail() {
    const body = detailShell("요약");
    const d = state.data;
    body.append(el("h2", "", "Arkism"), el("p", "lead", "에이전트들이 함께 쓰는 기억과 스킬이에요. 목록에서 하나를 고르면 왜 저장됐는지와 언제 쓰였는지 볼 수 있어요."));
    const byKind = Object.keys(KINDS).map((k) => [kindLabel(k), `${d.memories.filter((m) => m.kind === k).length}개`]).filter(([, v]) => v !== "0개");
    const s = section("사용 중인 기억"); s.append(facts(byKind)); body.append(s);
    const r = section("어떻게 쓰이나요");
    r.append(facts([["대화 시작", "선호·작업 규칙을 모두 넣어요"], ["질문할 때", "관련 있는 기억만 최대 6개"], ["자동 저장", "내가 직접 말한 것 · AI가 추론한 교훈·결정(시험 적용)"], ["저장소", "저장소에서 나온 기억은 그 저장소에서만, 나머지는 어디서나 써요"]]));
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
  async function memoryDetail(m) {
    const body = detailShell("기억");
    const u = usageOf(m.id), s = memStat(m.id);
    detailHead(body, kindLabel(m.kind), m.text, `${originOf(m)} · ${shortDate(m.created_at)} 저장 · ${memRepo(m) ? `${memRepo(m).split("/").pop()} 저장소에서만` : "모든 에이전트"}`);
    const contextPanel = renderMemoryContext(m);
    if (contextPanel) body.append(contextPanel);
    const modelPanel = renderModelGeneration(m);
    if (modelPanel) body.append(modelPanel);
    const fix = () => button("고치기", "small", null, () => openMemoryFix(m));
    if (unusedOften(m)) {
      body.append(actionCard(`최근 ${memUse().days}일 동안 ${s.injected}번 참고됐지만, 답변에는 한 번도 쓰이지 않았어요. 아직 맞는 내용인가요?`,
        button("맞아요, 유지", "primary small", null, () => {
          try { localStorage.setItem(KEEP_KEY, JSON.stringify([...keptMemories(), m.id])); } catch (_) { /* private window */ }
          notice("유지할게요. 이 기억은 다시 묻지 않아요.");
          render(false);
          renderDetail();
        }), fix(), button("잊기", "ghost small", null, () => forgetMemory(m))));
    } else if (idle(m)) body.append(actionCard("30일 동안 한 번도 참고되지 않았어요. 질문과 관련된 적이 없어서예요. 아직 필요한 기억인가요?", ...(state.caps?.actions?.propose ? [fix()] : []), button("잊기", "ghost small", null, () => forgetMemory(m))));
    else if (state.caps?.actions?.propose) body.append(button("고치기", "small", "pencil-simple", () => openMemoryFix(m)));
    const why = section("어디서 왔나요");
    const quoteBox = el("div", "quote");
    quoteBox.append(el("div", "", "근거를 불러오는 중…"));
    why.append(quoteBox);
    body.append(why);
    const use = section("언제 쓰이나요", u?.last ? `마지막 ${ago(u.last)}` : "");
    const uses = standing(m) ? "대화를 시작할 때마다 들어가서 따로 세지 않아요"
      : s && memUse() ? `${s.injected}번 참고 · ${s.used}번 사용` : u ? `${u.label7}번 참고 · 30일 ${u.d30}번` : "불러오는 중…";
    use.append(facts([
      ["들어가는 때", standing(m) ? "대화를 시작할 때마다" : "질문과 관련 있을 때만"],
      ["쓰는 곳", m.scope?.agent_id ? `에이전트 ${m.scope.agent_id}` : "모든 에이전트 · 내 기억"],
      [standing(m) ? "횟수" : `최근 ${memUse()?.days || 7}일`, uses],
      ["저장 방식", savedHow(m)]
    ]));
    if (!standing(m) && u) use.append(el("p", "meta", "질문에 자료로 들어간 횟수예요. 최근 10번까지 기록해요."));
    body.append(use);
    window.ArkismMemoryEvaluation.mount({ container: body, record: m, scope: { ...state.scope },
      request, el, section, facts, button });
    // 변경 이력 N건 › (folded) · 이전으로 되돌리기 (Figma 198:630): the lineage walk fills both once it answers.
    const hist = el("details", "d-history");
    const histHead = el("summary", "", "변경 이력 1건");
    const list = el("ul", "plain history");
    list.append(historyItem(m.created_at, "저장", tr(withAgent(m))));
    const histRow = el("div", "d-history-row");
    histRow.append(hist);
    hist.append(histHead, list);
    body.append(histRow);
    body.append(...privacy.memorySections(m));
    const g = generation;
    const [sources, lineage] = await Promise.all([
      request(`/v1/memories/${encodeURIComponent(m.id)}/sources?${query()}`).catch(() => null),
      request(`/v1/memories/${encodeURIComponent(m.id)}/lineage?${query()}`).catch(() => null)
    ]);
    if (g !== generation || state.selected !== m.id) return;
    originals.sourceDetails(quoteBox, m, sources);
    for (const link of lineage?.links || []) {
      if (link.relation === "proposal") continue;
      const label = { supersedes: "옛 기억을 대체함", superseded_by: "새 기억으로 바뀜", conflict: "다른 기억과 부딪힘" }[link.relation] || link.relation;
      list.prepend(historyItem(link.record?.updated_at || link.record?.created_at, label, link.record?.text ? `“${link.record.text}”` : ""));
    }
    histHead.textContent = `변경 이력 ${list.children.length}건`;
    // The newest memory this one replaced; going back saves its sentence again the same way 고치기 does.
    const prev = (lineage?.links || []).filter((l) => l.relation === "supersedes" && l.record?.text)
      .sort((a, b) => new Date(b.record.updated_at || b.record.created_at || 0) - new Date(a.record.updated_at || a.record.created_at || 0))[0];
    if (prev && m.status === "active" && state.caps?.actions?.propose) {
      const back = el("button", "link-btn d-revert", "이전으로 되돌리기"); back.type = "button";
      back.addEventListener("click", () => revertMemory(m, prev.record.text, back));
      histRow.append(back);
    }
  }
  async function revertMemory(m, text, trigger) {
    if (!confirm(`이전 내용으로 되돌릴까요?\n\n지금: “${m.text}”\n이전: “${text}”\n\n지금 내용은 변경 이력에 남아요.`)) return;
    trigger.disabled = true;
    try {
      const item = await captureMemory(text, m.kind, m, "Arkism 앱에서 사용자가 이전 내용으로 되돌림");
      const saved = ["accepted", "merged", "approved"].includes(item.status) || item.memory_id;
      notice(saved ? "이전 내용으로 되돌렸어요." : "검토 대기에 들어갔어요. 기억 › 확인해 주세요에서 저장하면 되돌려져요.");
      state.selected = null;
      await reloadMemories();
    } catch (e) { notice(`되돌리지 못했어요 (${e.message}).`); trigger.disabled = false; }
  }
  async function forgetMemory(m) {
    if (!confirm(`이 기억을 잊을까요?\n\n“${m.text}”\n\n모든 에이전트가 더 이상 쓰지 않아요.`)) return;
    try {
      await request(`/v1/memories/${encodeURIComponent(m.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope, expected_revision: m.revision }) });
      notice("잊었어요.");
      state.selected = null;
      await reloadMemories();
    } catch (e) { notice(e.message === "access_denied" ? "이 계정은 기억을 지울 수 없어요." : `잊지 못했어요 (${e.message}).`); }
  }
  // 직접 추가·고치기 go through the same path as a sentence the user said in a chat (explicit_remember):
  // the server applies its own rules, and a fix names the memory it replaces.
  async function captureMemory(text, kind, replaces, why) {
    const claim = { subject: "general", text, kind, basis: "explicit_user", reason: why || (replaces ? "Arkism 앱에서 사용자가 고침" : "Arkism 앱에서 사용자가 직접 추가") };
    if (replaces) claim.supersedes = { collection: "memories", id: replaces.id, revision: replaces.revision };
    const ev = { id: `desktop:${Date.now().toString(36)}:${crypto.randomUUID().slice(0, 8)}`, type: replaces ? "user_corrected_fact" : "explicit_remember",
      origin_product: "arkism_desktop", occurred_at: new Date().toISOString() };
    const r = await request("/v1/capture", { method: "POST", body: JSON.stringify({ scope: state.scope, generator: { model: "user", prompt_version: "desktop-v1", pipeline_version: "arkism-desktop" }, claims: [claim], event: ev }) });
    const item = (r.items || [])[0] || {};
    if (item.status === "rejected") throw new Error(item.reason === "secret_detected" ? "비밀값이 들어 있어요" : item.reason || "저장 거절");
    return item;
  }
  function openMemoryAdd() { openMemoryFix(null); }
  // 고쳐서 저장: the corrected sentence is saved as the user's own, and the candidate is set aside with that reason.
  function openCandidateFix(c) {
    const text = prompt("고친 문장을 쓰세요. 내가 직접 말한 기억으로 저장돼요.", c.text);
    if (!text?.trim() || text.trim() === c.text) return;
    void (async () => {
      try {  // edit_approve keeps the candidate's sources and kind
        await request(`/v1/memory_candidates/${encodeURIComponent(c.id)}/review`, {
          method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: c.revision, action: "edit_approve", content: text.trim() })
        });
        notice("고쳐서 저장했어요.");
        state.selected = null;
        await reloadMemories();
      } catch (e) { notice(`저장하지 못했어요 (${e.message}).`); }
    })();
  }
  // 고치기 (m) or 직접 추가 (no m).
  function openMemoryFix(m) {
    const dlg = el("dialog", "crew-dialog");
    const form = el("form");
    form.method = "dialog";
    const head = el("div", "dialog-head");
    const t = el("div", "");
    t.append(el("h2", "", m ? "기억 고치기" : "기억 직접 추가"), el("p", "", m ? "고친 문장이 새 기억이 되고, 옛 기억은 바뀐 이력에 남아 되돌릴 수 있어요." : "내가 직접 말한 것처럼 저장돼요. 모든 에이전트가 자료로 써요."));
    head.append(t);
    const ta = el("textarea");
    ta.rows = 4;
    ta.value = m?.text || "";
    ta.maxLength = 2000;
    ta.placeholder = "보고는 결론부터 짧게 쓴다.";
    const f = el("label", "field", "기억");
    f.append(ta);
    const kind = el("select");
    for (const [k, l] of [["user", "선호"], ["procedural", "작업 규칙"], ["decision", "결정"], ["lesson", "교훈"]]) { const o = el("option", "", l); o.value = k; o.selected = k === (m?.kind || "procedural"); kind.append(o); }
    kind.disabled = !!m;
    const kf = el("label", "field", "종류");
    kf.append(kind);
    const body = el("div", "dialog-body");
    body.append(f, kf);
    const foot = el("div", "dialog-foot");
    const cancel = button("취소", "ghost");
    cancel.type = "submit";
    cancel.value = "cancel";
    const ok = button("저장", "primary");
    ok.type = "submit";
    ok.value = "ok";
    foot.append(cancel, ok);
    form.append(head, body, foot);
    form.addEventListener("submit", async (e) => {
      if (e.submitter?.value !== "ok" || !ta.value.trim() || ta.value.trim() === m?.text) return;
      e.preventDefault();
      ok.disabled = true;
      try {
        const item = await captureMemory(ta.value.trim(), m?.kind || kind.value, m);
        dlg.close();
        const saved = ["accepted", "merged", "approved"].includes(item.status) || item.memory_id;
        if (saved) {
          notice(m ? "고쳤어요. 옛 기억은 바뀐 이력에 남아요." : "저장했어요.");
        } else {
          const reasonText = item.reason ? ` (${item.reason})` : "";
          notice(`검토 대기에 들어갔어요${reasonText}. 기억 › 검토 대기에서 승인하면 저장돼요.`);
        }
        state.selected = null;
        await reloadMemories();
      } catch (error_) { notice(`저장하지 못했어요 (${error_.message}).`); ok.disabled = false; }
    });
    dlg.append(form);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal();
  }
  function historyItem(when, title, desc) {
    const li = el("li", "");
    const t = el("time", "", shortDate(when));
    const c = el("div", "");
    c.append(el("strong", "", title), raw(el("span", "", desc || ""))); // desc: 스킬 설명·기억 본문 같은 데이터
    li.append(t, c);
    return li;
  }
  // Figma 198:716 (새 후보) · 198:1748 (헷갈림): kind · title · meta → action card → 어디서 왔나요 → 자동 저장 규칙.
  async function candidateDetail(c) {
    const body = detailShell("기억 후보");
    const contextPanel = renderMemoryContext(c);
    if (contextPanel) body.append(contextPanel);
    const modelPanel = renderModelGeneration(c);
    if (modelPanel) body.append(modelPanel);
    const [why] = waitReason(c);
    const rules = el("details", "d-rules");
    rules.append(el("summary", "", "자동 저장 규칙 보기"), facts([["바로 저장", "내가 직접 말한 선호·결정·규칙"], ["시험 적용", "AI가 추론한 교훈·결정 (5개 세션에서 안 쓰이면 내림)"], ["항상 확인", "명령 같은 문장 · AI가 추론한 선호·작업 규칙"]]));
    const reasonSection = () => {
      if (!c.summary) return null;
      const s = section("어디서 왔나요");
      const q = el("div", "quote");
      q.append(raw(el("div", "", c.summary)), el("footer", "", withAgent(c)));
      s.append(q);
      return s;
    };
    const instructionLike = (c.tags || []).includes("review:instruction_like");
    const canDecide = !!state.caps?.actions?.review_decide;
    if (c.conflict_id) {
      // 헷갈림: 지금 기억 → 새로 들어온 내용 → 이유 → 고르기.
      detailHead(body, `${kindLabel(c.kind)} · 내용이 달라요`, "기존 기억과 내용이 달라요", "어느 쪽이 맞나요?");
      if (instructionLike) body.append(actionCard("AI에게 명령하는 문장처럼 보여요. 붙여 넣은 글이나 웹 문서에서 섞여 들어온 문장일 수 있어요."));
      const cmp = section("지금 기억");
      const oldBox = el("div", "quote");
      oldBox.append(el("div", "meta", "불러오는 중…"));
      cmp.append(oldBox);
      const fresh = section("새로 들어온 내용");
      const newBox = el("div", "quote");
      const foot = el("footer", "");
      foot.append(`${ago(c.created_at)} · ${withAgent(c)}`);
      newBox.append(raw(el("div", "", `“${c.text}”`)), foot);
      fresh.append(newBox);
      const parts = [cmp, fresh];
      if (c.summary) parts.push(raw(el("p", "meta", c.summary)));
      if (canDecide) {
        const act = el("div", "d-buttons");
        act.append(button("새 내용으로 바꾸기", "primary small", null, () => resolveConflict(c, "new")),
          button("지금 기억 유지", "small", null, () => resolveConflict(c, "keep_old")),
          button("둘 다 맞아요", "ghost small", null, () => resolveConflict(c, "both")));
        parts.push(el("p", "meta", "선택한 내용과 기존 기억의 변경을 함께 저장해요. 응답을 못 받은 경우 같은 선택으로 다시 시도할 수 있어요."), act);
      }
      body.append(...parts, rules);
      request(`/v1/memory_relations/${encodeURIComponent(c.conflict_id)}?${query()}`)
        .then((rel) => request(`/v1/memories/${encodeURIComponent(rel.relations.find((r) => r.collection === "memories").id)}?${query()}`))
        .then((old) => { const f = el("footer", "", `${shortDate(old.created_at)} 저장 · ${withAgent(old)}`); oldBox.replaceChildren(raw(el("div", "", `“${old.text}”`)), f); })
        .catch(() => oldBox.replaceChildren(el("div", "meta", "기존 기억을 불러오지 못했어요.")));
      return;
    }
    detailHead(body, `${kindLabel(c.kind)} · 새 후보`, c.text, `${withAgent(c)}에서 뽑음 · ${ago(c.created_at)}`);
    const text = instructionLike ? "AI에게 명령하는 문장처럼 보여요. 저장하면 다른 에이전트가 이 말을 따를 수 있어요."
      : `${why}. 저장하면 모든 에이전트가 자료로 써요.`;
    if (canDecide) {
      const reason = el("textarea"); reason.id = "reject-reason"; reason.rows = 2; reason.placeholder = "버리는 이유 (버릴 때만 필요해요)"; reason.setAttribute("aria-label", "버리는 이유");
      const ok = button("그대로 저장", instructionLike ? "ghost small" : "primary small", null, () => { ok.disabled = no.disabled = true; void decide(c, "approve", ""); });
      const fix = button("고쳐서 저장", "small", null, () => openCandidateFix(c));
      const no = button("버리기", instructionLike ? "primary small" : "ghost small", null, () => {
        if (!reason.value.trim()) { reason.focus(); notice("버리는 이유를 적어 주세요."); return; }
        ok.disabled = no.disabled = true; void decide(c, "reject", reason.value.trim());
      });
      const card = actionCard(text, ...(instructionLike ? [no, fix, ok] : [ok, fix, no]));
      card.insertBefore(reason, card.lastChild);
      if (instructionLike) card.setAttribute("role", "note");
      body.append(card);
    } else body.append(actionCard(text));
    const r = reasonSection();
    if (r) body.append(r);
    body.append(rules);
  }
  function agentNote(sk, k, staged) {
    if (staged) return staged === "allow" ? " · 추가 예정" : " · 뺄 예정";
    return compatOf(sk)[k] === "requires_adapter" ? " · 어댑터" : "";
  }
  function blockedCallout(sk) {
    const off = AGENTS.filter(([k]) => !allowed(sk, k) && !state.pending.has(`${sk.id}|${k}`));
    if (!off.length) return null;
    const [k, n] = off[0];
    const more = off.slice(1).map(([, x]) => x).join("·");
    const c = callout(`${n}에서 이 스킬을 못 써요`, more ? `${more}도 막혀 있어요.` : "추가하고 적용하면 바로 받아요.");
    c.append(button(`${n}에 추가`, "", "plus", () => stage(sk.id, k, "allow")));
    return c;
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
  function agentGrid(sk) {
    const ag = section("에이전트", `${AGENTS.filter(([k]) => allowed(sk, k)).length} / ${AGENTS.length}`);
    const grid = el("div", "agents");
    for (const [k, n, logo] of AGENTS) {
      const staged = state.pending.get(`${sk.id}|${k}`);
      const cls = ["agent", "clickable", allowed(sk, k) ? "on" : "none"];
      if (staged) cls.push("staged");
      const a = el("button", cls.join(" "));
      a.type = "button";
      a.append(agentLogo(logo, n), el("span", "agent-label", n + agentNote(sk, k, staged)), pcLink(k));
      a.title = "누르면 어떻게 적용되는지 보여 줘요";
      a.addEventListener("click", () => skillAgentPopup(sk, k, n));
      grid.append(a);
    }
    ag.append(grid, el("p", "meta", "테두리가 진하면 쓰는 중, 점선이면 안 써요."), pcLegend());
    return ag;
  }
  function skillAgentPopup(sk, k, n) {
    const on = allowed(sk, k);
    const staged = state.pending.get(`${sk.id}|${k}`);
    const rows = [
      on ? `지금 ${n}에서 이 스킬을 써요.` : `지금 ${n}에서는 이 스킬을 못 써요.`,
      `${on ? "빼면" : "추가하면"} Arkism이 ${n}에 이 스킬을 ${on ? "막아요" : "열어 줘요"}. ‘적용’을 눌러야 실제로 바뀌고, ${n}은 다음 대화부터 바로 받아요.`,
    ];
    if (staged) rows.push(`지금 ‘${staged === "allow" ? "추가" : "빼기"} 예정’으로 표시돼 있어요. 위쪽 ‘바뀐 칸 → 적용’에서 한꺼번에 적용할 수도 있어요.`);
    const others = state.pending.size - (staged ? 1 : 0);
    if (others > 0) rows.push(`‘지금’을 누르면 예정으로 둔 다른 칸 ${others}개도 함께 적용돼요.`);
    applyPopup(`${n} · ${sk.name}`, "스킬", rows, (dlg) => {
      const now = button(on ? "지금 빼기" : "지금 추가", "primary", null, async () => {
        if (!staged) toggleCell(sk, k);
        dlg.close();
        await applyChanges();
      });
      let laterLabel = on ? "뺄 예정으로" : "추가 예정으로";
      if (staged) laterLabel = "예정 취소";
      const later = button(laterLabel, "ghost", null, () => { toggleCell(sk, k); dlg.close(); });
      return [later, now];
    });
  }
  function checksSection(sk) {
    const c = skillChecks(sk);
    const sec = section("품질·보안 검사");
    if (!c) { sec.append(el("p", "meta", "불러오는 중…")); return sec; }
    const q = el("span", "check-cell"); q.append(el("span", `grade ${c.quality.cls}`, c.quality.grade), c.quality.score === null ? "" : `${c.quality.score}점`);
    const s2 = el("span", "check-cell"); s2.append(el("span", `sec ${c.security.cls}`, c.security.label), typeof c.security.risk === "number" ? `위험 ${c.security.risk}` : "");
    const scans = c.scans.map((x) => `${agentName(x.agent)} ${x.path}${x.error ? ` (${x.error})` : ""}`);
    const dl = facts([["빠진 설명", c.quality.missing.join(", ")], ["걸린 것", c.security.reasons.join(" · ")], ["검사한 폴더", scans.join(", ")], ["검사 시각", c.scans[0] ? shortDate(c.scans[0].scanned_at) : ""], ["독립 검증", c.validated ? "평가 기록 있음" : ""]]);
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
  function bulletSection(title, items) {
    if (!items?.length) return null;
    const sec = section(title);
    const ul = el("ul", "bullets");
    items.forEach((i) => ul.append(el("li", "", i)));
    sec.append(ul);
    return sec;
  }
  // 서버에게 원본 저장소를 지금 다시 확인하게 한다 (서버는 6시간마다 확인). 1분 안에 결과가 돌아온다.
  async function checkSkillRepo(r) {
    try {
      const res = await request("/v1/agent_tools/requests", { method: "POST", body: JSON.stringify({ scope: state.scope, agent: "shared", kind: "skill_check", name: r.repo, enabled: true }) });
      state.data.tools.requests = res.agent_tools?.requests || state.data.tools.requests;
      notice("확인을 요청했어요. 1분 안에 갱신돼요.");
      pollTools();
      renderDetail();
    } catch (e) { notice(`요청하지 못했어요 (${e.message}).`); }
  }
  // 스킬이 든 저장소: 원본 주소, 지금 커밋, 새 버전이 있는지, 서버가 마지막으로 확인한 때 (서버가 6시간마다 git fetch)
  const repoOf = (sk) => (state.data.tools?.skill_repos || []).filter((r) => (sk.source_path || "").includes(`${r.repo.replace(/^~/, "")}/`))
    .sort((x, y) => y.repo.length - x.repo.length)[0] || null;
  function updateSection(sk) {
    const r = repoOf(sk);
    if (!r) return null;
    const sec = section("업데이트");
    const checking = (state.data.tools?.requests || []).some((q) => q.kind === "skill_check" && q.name === r.repo && q.status === "pending");
    const check = button(checking ? "확인 중…" : "업데이트 확인", "ghost small", "arrows-clockwise", () => checkSkillRepo(r));
    check.disabled = checking;
    sec.querySelector("h3").append(check);
    const row = el("div", "update-row");
    row.append(icon("link"), el("span", "mono update-remote", r.remote || r.repo), el("span", "mono update-head", r.head || ""),
      el("span", "update-state " + (r.behind ? "warn" : "ok"), r.behind ? `새 커밋 ${r.behind}개` : "✓ 최신"));
    sec.append(row, el("p", "meta", "마지막 확인 · " + ago(r.checked_at)));
    const ups = (state.data.tools?.skill_updates || []).filter((u) => u.repo === r.repo);
    if (r.behind && ups.length) sec.append(button("업데이트 받기", "", "arrows-clockwise", () => updateSkills(ups)));
    return sec;
  }
  function openDetail() { $("#app").classList.add("detail-open"); }
  function closeDetail() {
    $("#app").classList.remove("detail-open"); state.selected = null;
    if (state.view === "todo") { render(); return; } // home's title and body change with the selection too
    const panel = state.view === "memories" && { traits, reflections, relations, past: memoryTime, originals, digests, merge: digests }[state.memTab];
    if (panel) { panel.close(); return; }
    markSelected(); renderDetail();
  }

  // ---------------------------------------------------------------- auth, settings, keys
  function unlocked(on) {
    $("#unlock").hidden = on;
    $("#app").inert = !on;
    on ? $("#app").removeAttribute("aria-hidden") : $("#app").setAttribute("aria-hidden", "true");
  }
  function lock() {
    git.reset();
    routing.reset();
    usage.reset();
    sessions.reset();
    generation++;
    state.token = "";
    state.caps = null;
    store("arkism.token", null);
    store("arkism.email", null);
    unlocked(false);
    setStatus("", "연결 끊김");
    unlockMode();
    unlockSSH().focus();
  }
  async function open() {
    state.caps = await request(`/v1/capabilities?${query()}`);
    unlocked(true);
    await loadAll();
    pages.checkUpdate(true);
  }
  // 서버 연결은 SSH 주소 하나로: 앱이 그 로그인으로 서버의 접속 정보(주소·토큰·범위)를 받아 오고
  // API를 이 PC로 끌어온다. 토큰·Google 로그인·워크스페이스를 사용자가 넣을 일은 없다.
  const SSH_ERRORS = {
    ssh_address_invalid: "주소를 확인하세요. user@서버주소 또는 user@서버주소:포트 처럼 적어요.",
    ssh_missing: "이 PC에서 ssh 프로그램을 찾지 못했어요. OpenSSH(또는 Git for Windows)를 설치해 주세요.",
    ssh_auth_failed: "SSH 로그인이 거부됐어요. 이 PC에서 ‘ssh 주소’가 비밀번호 없이(SSH 키로) 되는지 확인하세요.",
    ssh_host_key_changed: "서버의 SSH 키가 예전과 달라요. 맞는 서버라면 known_hosts에서 옛 키를 지운 뒤 다시 연결하세요.",
    ssh_unreachable: "서버에 닿지 못했어요. 주소·포트와 네트워크 연결을 확인하세요.",
    arkism_not_found: "서버에 로그인은 됐지만 Arkism이 설치돼 있지 않아요.",
    arkism_too_old: "서버의 Arkism이 오래돼서 이 연결 방식을 몰라요. 서버의 Arkism을 업데이트해 주세요."
  };
  const sshErrorText = (code) => SSH_ERRORS[code] || (code.startsWith("arkism_connect_failed") ? `서버의 Arkism이 접속 정보를 주지 못했어요 (${code.split(": ")[1] || "알 수 없음"}).` : `연결하지 못했어요 (${code}).`);
  // 저장된 SSH 연결: {host, user, port, key, bin}. 예전 판은 주소 문자열 하나만 저장했다.
  function readSSH(v) {
    if (!v) return null;
    try { const o = JSON.parse(v); if (o && typeof o.host === "string") return o; } catch (_) { /* a plain address */ }
    return { host: v };
  }
  const sshLabel = (f) => f?.host ? (f.user && !f.host.includes("@") ? `${f.user}@${f.host}` : f.host) + (f.port ? `:${f.port}` : "") : "";
  let sshFormSeq = 0;
  // SSH 입력 칸: 연결 화면과 설정 › 고급 › 연결 방식이 같이 쓴다. 빈 칸은 ssh가 정한다(~/.ssh/config, ssh-agent).
  function sshFields(saved) {
    const box = el("div", "ssh-rows");
    const f = saved || {};
    const row = (label, hint, ph, value) => {
      const r = el("label", "ssh-row");
      const text = el("span", "ssh-row-text");
      text.append(el("strong", "", label), el("small", "", hint));
      const i = el("input");
      i.placeholder = ph; i.value = value || ""; i.autocomplete = "off"; i.spellcheck = false; i.setAttribute("autocapitalize", "off");
      r.append(text, i);
      box.append(r);
      return i;
    };
    const host = row("Host", "user@host 또는 ~/.ssh/config의 Host 별칭", "server.example.com", f.host);
    const user = row("User", "비우면 ~/.ssh/config 또는 지금 사용자", "root", f.user);
    const port = row("Port", "비우면 22 또는 ~/.ssh/config의 포트", "22", f.port ? String(f.port) : "");
    port.inputMode = "numeric";
    const key = row("Identity file", "개인 키 경로. 비우면 ssh-agent 또는 ~/.ssh/config", "~/.ssh/id_ed25519", f.key);
    const bin = row("Arkism 경로 (선택)", "서버의 arkism 프로그램 전체 경로. 비우면 자동으로 찾아요", "자동", f.bin);
    const list = el("datalist");
    list.id = `ssh-keys-${++sshFormSeq}`;
    key.setAttribute("list", list.id);
    box.append(list);
    tauri?.invoke("ssh_keys").then((keys) => { for (const k of keys || []) { const o = el("option"); o.value = k; list.append(o); } }).catch(() => {});
    const read = () => {
      const p = port.value.trim();
      return { host: host.value.trim(), user: user.value.trim() || null, port: p ? Number(p) : null, key: key.value.trim() || null, bin: bin.value.trim() || null };
    };
    return { node: box, read, focus: () => host.focus() };
  }
  function sshFormError(form) {
    if (!form.host) return "Host를 넣어 주세요.";
    if (form.port !== null && !(Number.isInteger(form.port) && form.port > 0 && form.port < 65536)) return "Port는 1~65535 사이 숫자예요.";
    return "";
  }
  // SSH 테스트: 로그인하고 서버의 Arkism이 접속 정보를 주는지만 본다. 연결은 바꾸지 않는다.
  async function sshTest(form) {
    const bad = sshFormError(form);
    if (bad) return { ok: false, text: bad };
    if (!tauri) return { ok: false, text: "서버 연결은 Arkism 앱에서만 돼요." };
    try {
      const r = await tauri.invoke("ssh_test", { form });
      return { ok: true, text: `SSH 로그인과 서버의 Arkism 응답을 확인했어요 (${r.host}).` };
    } catch (err) {
      return { ok: false, text: sshErrorText(String(err?.message || err)) };
    }
  }
  // 연결에 실패하면 지금 쓰던 연결을 그대로 둔다.
  async function sshConnect(form) {
    const bad = sshFormError(form);
    if (bad) throw new Error(bad);
    if (!tauri) throw new Error("서버 연결은 Arkism 앱에서만 돼요.");
    const before = { mode: state.mode, server: state.server, token: state.token, scope: state.scope, sshHost: state.defaults.ssh_host };
    let r;
    try {
      r = await tauri.invoke("ssh_connect", { form });
    } catch (err) {
      const code = String(err?.message || err);
      throw Object.assign(new Error(/^(ssh|arkism)_/.test(code) ? sshErrorText(code) : `연결하지 못했어요 (${code}).`), { code });
    }
    try {
      state.mode = "remote";
      state.server = r.server;
      state.token = r.token;
      state.email = "";
      state.scope = { ...defaults, workspace_id: r.workspace_id, project_id: r.project_id };
      // 서버의 Git·스킬 실행도 같은 SSH 주소로 (포트·키가 따로 있으면 ~/.ssh/config 별칭을 쓴다).
      if (!r.ssh_port && !r.ssh_key) state.defaults.ssh_host = r.ssh_host;
      await open();
    } catch (_) {
      Object.assign(state, { mode: before.mode, server: before.server, token: before.token, scope: before.scope });
      state.defaults.ssh_host = before.sshHost;
      throw new Error("연결은 됐지만 서버의 Arkism을 열지 못했어요. 서버에서 Arkism이 켜져 있는지 확인하세요.");
    }
    state.ssh = form;
    store("arkism.ssh", JSON.stringify(form));
    store("arkism.mode", "remote");
    store("arkism.server", null);
    store("arkism.scope", null);
  }
  let unlockForm = null;
  function unlockSSH() {
    if (!unlockForm) {
      unlockForm = sshFields(state.ssh);
      $("#ssh-form").replaceChildren(unlockForm.node);
    }
    return unlockForm;
  }
  // manual: the person pressed 연결하기, so 시작 2·3단계 (에이전트 선택 · 연결 확인) follow on 홈.
  async function unlockConnect(manual) {
    const b = $("#ssh-button");
    $("#unlock-error").textContent = "";
    $("#ssh-test-result").textContent = "";
    home.sshError(null);
    b.disabled = true;
    b.lastChild.textContent = "연결하는 중…";
    const form = unlockSSH().read();
    try {
      await sshConnect(form);
      if (manual === true) { state.start = 2; go("todo"); }
    } catch (err) {
      state.token = "";
      $("#unlock-error").textContent = err.message;
      home.sshError(err.code || "error", err.message, form);
      unlocked(false);
      unlockMode();
    } finally {
      b.disabled = false;
      b.lastChild.textContent = "연결하기";
    }
  }
  $("#ssh-test").addEventListener("click", async (e) => {
    const b = e.currentTarget, out = $("#ssh-test-result");
    $("#unlock-error").textContent = "";
    b.disabled = true;
    out.textContent = "확인하는 중…";
    const r = await sshTest(unlockSSH().read());
    out.textContent = r.text;
    out.classList.toggle("warn", !r.ok);
    b.disabled = false;
  });
  function unlockMode() {
    // 내장 Arkism을 켜는 중이거나 켜지 못했을 때는 연결 칸 대신 진행 상태만 보여 준다.
    const builtIn = state.mode === "local";
    for (const id of ["#ssh-form", "#ssh-actions", "#ssh-hint", "#ssh-test-result"]) $(id).hidden = builtIn;
    $("#local-retry").hidden = !builtIn || !state.localError;
    $("#remote-link").hidden = !builtIn || !state.localError;
    $("#local-link").hidden = builtIn || !state.defaults.local;
    $("#unlock-intro").textContent = builtIn
      ? (state.localError ? `이 PC의 Arkism을 켜지 못했어요 (${state.localError}). 다시 시도하거나 서버에 연결할 수 있어요.` : "이 PC의 Arkism을 준비하고 있어요…")
      : "SSH로 접속하듯 서버 주소만 넣으면 돼요. 토큰이나 로그인은 필요 없어요.";
  }
  // 기본은 이 PC의 내장 Arkism(로그인 없음). 서버 연결은 사용자가 고를 때만 쓴다.
  async function startLocal() {
    tauri?.invoke("ssh_disconnect").catch(() => {});
    state.mode = "local";
    // The built-in Arkism registers its tokens for the default scope only.
    state.scope = { ...defaults };
    state.localError = "";
    unlocked(false);
    unlockMode();
    try {
      const r = await tauri.invoke("local_start");
      state.server = r.server;
      state.token = r.token;
      await open();
    } catch (err) {
      state.token = "";
      state.localError = String(err?.message || err);
      unlocked(false);
      unlockMode();
    }
  }
  function useServer() {
    state.mode = "remote";
    lock();
  }
  function useLocal() {
    tauri?.invoke("ssh_disconnect").catch(() => {});
    store("arkism.mode", null);
    store("arkism.token", null);
    location.reload();
  }
  // 연결 끊기: 서버 연결을 닫고 주소 입력으로 돌아간다. 다음 시작 때 자동으로 다시 붙지 않는다.
  function disconnect() {
    tauri?.invoke("ssh_disconnect").catch(() => {});
    store("arkism.mode", null);
    if (state.defaults.local) { location.reload(); return; }
    lock();
  }
  // 서버 연결 끊김 카드의 지금 다시 연결: 쓰던 연결 방식 그대로 한 번 더.
  async function reconnect(b) {
    if (b) b.disabled = true;
    try {
      if (state.mode === "remote" && tauri && state.ssh) await sshConnect(state.ssh);
      else if (state.mode === "local" && tauri) await startLocal();
      else await loadAll();
    } catch (e) { notice(e.message); } finally { if (b?.isConnected) b.disabled = false; }
  }
  $("#local-retry").addEventListener("click", startLocal);
  $("#remote-link").addEventListener("click", useServer);
  $("#local-link").addEventListener("click", useLocal);
  $("#unlock-form").addEventListener("submit", (e) => {
    e.preventDefault();
    void unlockConnect(true);
  });
  document.querySelectorAll("[data-view]").forEach((b) => b.addEventListener("click", () => go(b.dataset.view, b.dataset.tab)));
  $("#search").addEventListener("input", (e) => {
    if (home.onInput()) return; // 홈·설정 같은 화면: 전체 검색 (home.js)
    state.query = e.target.value.trim().toLowerCase();
    if (state.view === "todo" || pages.views[state.view] || crew.pages[state.view]) state.view = "memories";
    render(false);
  });
  const openSettings = () => go("settings");
  document.addEventListener("keydown", (e) => {
    const editing = ["INPUT", "TEXTAREA", "SELECT"].includes(document.activeElement?.tagName);
    if ((e.key === "/" && !editing) || (e.key.toLowerCase() === "k" && (e.ctrlKey || e.metaKey))) { e.preventDefault(); const own = $(".panel-search input"); (own?.offsetParent ? own : $("#search")).focus(); return; }
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
  const LIST_ITEM = ":is(.row, .cn-row, .todo-item, .crew-card, .prompt-card, .ds-card, .git-row, .rt-mcard, .mp-chip, .wk-row, .wf-row, .tm-team, .tm-meet-row, .tm-urow, button.v4-tr, .cat-head, [data-id])";
  const NOT_ITEM = ".tab, .area-tab, .chip, .segment, [role=tab], input, select, textarea, label, dialog *";
  function listItems() {
    const root = document.querySelector(".home-first") && state.view === "todo" ? document.querySelector(".home-first") : $(".panel-main");
    return [...(root?.querySelectorAll(LIST_ITEM) || [])].filter((n) => !n.matches(NOT_ITEM) && !n.querySelector(LIST_ITEM)
      && (n.matches("button, a, [role=button], [tabindex]") || n.onclick) && n.offsetParent !== null);
  }
  const isPicked = (n) => n.matches(".selected, .active, .on, [aria-selected=true], [aria-current], [aria-pressed=true], [aria-expanded=true]");
  const models = window.ArkismModels({ state, el, icon, button, go, render, request, query, notice, agentLogo, ago });
  const routing = window.ArkismRouting({ canRefresh: () => !syncBlocked(), state, $, el, icon, button, segment, go, render, request, query, notice, detailShell, agentLogo, ago });
  const usage = window.ArkismUsage({ state, $, el, button, segment, render, request, query, agentLogo, AGENTS, ago, go, detailShell,
    openSession: (id) => sessions.open(id), openSettings: (tab) => { state.settingsTab = tab; go("settings"); },
    memory: () => ({ use: memUse(), memories: state.data.memories, kindLabel, often: unusedOften, openIdle: () => { state.menu = "memory"; go("memories", "idle"); },
      openMemory: (id) => { state.menu = "memory"; go("memories", "active"); state.selected = id; renderDetail(); } }),
    skillAgents: (name) => { const by = {}; for (const sk of state.data.skills.filter((x) => x.name === name)) for (const [k, n] of Object.entries(state.data.skillUse?.get(sk.id)?.by_runtime || {})) by[k] = (by[k] || 0) + n; return Object.entries(by).sort((a, b) => b[1] - a[1]).map(([k]) => k); } });
  const gallery = window.ArkismGalleryDelete({ state, $, el, button, icon, raw, render, request, query, notice, confirmPop, shortDate });
  const sessions = window.ArkismSessions({ state, $, el, button, segment, render, request, query, agentLogo, AGENTS, ago, detailShell, usage, go });
  const crew = window.ArkismCrew({ state, $, el, icon, button, segment, go, render, request, query, notice, detailShell, section, facts, callout, copyButton, markSelected, openDetail, agentLogo, ago, models,
    allowed, skillCategory: (sk) => pages.categoryOf(sk)?.name || "", skillCategories: () => pages.prefs.categories.map((c) => ({ name: c.name, icon: c.icon })),
    toolServers: () => state.data.tools?.servers || [] });
  const reviews = window.ArkismReviews({ state, $, el, icon, button, render, request, query, notice, detailShell, section, facts, markSelected, openDetail, ago, stat, go, headExtras, confirmPop,
    assist: (kind, prompt) => crew.assist(kind, prompt) });
  const pages = window.ArkismPages({ state, $, el, icon, button, segment, go, render, AGENTS, agentLogo, allowed, notice, store, stored, lock, disconnect, useServer, useLocal, startLocal, sshFields, sshTest, sshConnect, sshLabel, reopen: open, trimSlash, tauri, stat, models, routing, crew,
    request, query, reloadTools: async () => { await refreshTools(); pollTools(); }, assist: (...a) => crew.assist(...a), privacyGroup: () => privacy.settingsGroup() });
  function openIntelligenceRecord(record) {
    const list = record.status === "active" ? "memories" : record.conflict_id ? "conflicted" : "candidates";
    // The record may be beyond the first list page. Keep the freshly checked revision.
    state.data[list] = [record, ...state.data[list].filter(item => item.id !== record.id)];
    state.query = ""; $("#search").value = "";
    go("memories", list === "memories" ? "active" : list === "candidates" ? "candidate" : "conflicted");
    state.selected = record.id; markSelected(); renderDetail(); openDetail();
    const heading = $("#detail h2");
    if (heading) { heading.tabIndex = -1; heading.focus(); }
  }
  const skillGenerate = window.ArkismSkillGenerate({ state, el, button, request, query, reload: loadAll });
  const catalog = window.ArkismCatalog({ state, el, button, request, notice, agentLogo, reload: loadAll, pollTools });
  const skillExecution = window.ArkismSkillExecution({ state, el, button, tauri });
  const skillTrial = window.ArkismSkillTrial({ state, el, button, request, query });
  const skillReview = window.ArkismSkillReview({ state, el, button, request, query, reload: loadAll, execution: skillExecution, trial: skillTrial });
  const relations = window.ArkismIntelligence({ canRefresh: () => !syncBlocked(), state, $, el, icon, button, request, query, detailShell, openDetail, notice }, "relations");
  const traits = window.ArkismIntelligence({ canRefresh: () => !syncBlocked(), state, $, el, icon, button, request, query, detailShell, openDetail, openRecord: openIntelligenceRecord }, "traits");
  const reflections = window.ArkismIntelligence({ canRefresh: () => !syncBlocked(), state, $, el, icon, button, request, query, detailShell, openDetail, openRecord: openIntelligenceRecord });
  const digests = window.ArkismDigests({ canRefresh: () => !syncBlocked(), state, $, el, raw, tr, icon, button, request, query, detailShell, openDetail, notice, ago, render, row, markSelected,
    kindLabel, originOf, memSelect, section, facts, openRecord: (r) => openIntelligenceRecord(r), reloadMemories });
  const memoryTime = window.ArkismMemoryTime({ state, $, el, icon, button, request, detailShell, openDetail });
  const inject = window.ArkismInject({ state, el, button, request, query, detailShell, openDetail, section, facts });
  const originals = window.ArkismOriginals({ state, $, el, icon, button, request, detailShell, openDetail, ago });
  const privacy = window.ArkismPrivacy({ state, el, button, request, query, notice, section, afterChange: async () => { await reloadMemories(); renderDetail(); } });
  const codegraph = window.ArkismCodeGraph ? window.ArkismCodeGraph({ state, el, raw, button, request, query, notice, ago }) : null; // TOOLS › 저장소 › 코드 그래프 (codegraph.js)
  const git = window.ArkismGit({ state, $, el, button, icon, tauri, ago, notice, go, headExtras, codegraph,
    openReview: (key) => { go("reviews"); state.selected = key; markSelected(); reviews.detail(); openDetail(); } });
  const knowledge = window.ArkismKnowledge({ state, $, el, button, render, request, query, ago, detailShell, headExtras, confirmPop }); // MEMORY › 지식 (knowledge.js)
  Object.assign(pages.views, git.views, routing.views, usage.views, sessions.views, knowledge.views);
  const home = window.ArkismHome({ state, $, el, icon, button, go, render, request, query, notice, agentLogo, AGENTS, ago, pages, detailShell, markSelected, renderDetail, openDetail,
    skillCategory: (sk) => pages.categoryOf(sk)?.name || "", isPage: (v) => pages.views[v] || crew.pages[v], openRecord: openIntelligenceRecord,
    openSettings: (tab) => { if (tab) state.settingsTab = tab; go("settings"); }, sshLabel, sshErrorText, useLocal, lock, reconnect });
  home.wireSearch();
  shell = window.ArkismShell({ state, $, el, go, render });
  Object.assign(views, pages.views);
  Object.assign(crew.pages, window.ArkismTeams({ state, $, el, icon, button, go, render, request, query, notice, ago, headExtras, hasView: (v) => !!views[v] }).pages); // CREW › 팀·회의·디스코드·사용량 (team.js)
  Object.assign(views, crew.views, crew.pages, reviews.views);
  void (async () => {
    const defaults = tauri ? await tauri.invoke("app_defaults").catch(() => null) : null;
    if (defaults) Object.assign(state.defaults, defaults);
    if (state.defaults.local && stored("arkism.mode") !== "remote") return startLocal();
    state.mode = "remote";
    lock();
    // 지난번에 연결한 서버로 다시 붙는다.
    if (state.ssh && stored("arkism.mode") === "remote" && tauri) void unlockConnect();
  })();
})();
