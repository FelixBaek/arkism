// Settings and the Arkism design-token pages. app.js passes the shared helpers in once, so these screens use the same state and builders.
window.ArkismPages = (ctx) => {
  "use strict";
  const { state, $, el, icon, button, segment, go, render, AGENTS, agentLogo, allowed, notice, store, stored, disconnect, useLocal, startLocal, sshFields, sshTest, sshConnect, sshLabel, storageRow, tauri, models, routing, crew, request, reloadTools, assist } = ctx;

  // ---------------------------------------------------------------- saved choices (this PC only)
  const PREFS_KEY = "arkism.prefs";
  const DEFAULT_CATEGORIES = [
    { id: "code", name: "개발·코드", icon: "code", rules: [["path", "engineering"], ["name", "*review*"], ["name", "tdd"]] },
    { id: "writing", name: "글쓰기·블로그", icon: "file-text", rules: [["path", "blog/"], ["name", "writing-*"]] },
    { id: "browser", name: "브라우저·수집", icon: "database", rules: [["path", "browser/"], ["name", "*crawl*"]] },
    { id: "docs", name: "문서·PDF", icon: "archive", rules: [["path", "pdf"], ["name", "*docx*"]] }
  ];
  function loadPrefs() {
    let saved = null;
    try { saved = JSON.parse(stored(PREFS_KEY) || "null"); } catch (_) { /* start fresh */ }
    return { agentsOff: saved?.agentsOff || [], categories: saved?.categories || structuredClone(DEFAULT_CATEGORIES), collectUnused: saved?.collectUnused !== false, comfyUrl: saved?.comfyUrl || "" };
  }
  const prefs = loadPrefs();
  const savePrefs = () => store(PREFS_KEY, JSON.stringify(prefs));
  const shownAgents = () => AGENTS.filter(([k]) => !prefs.agentsOff.includes(k));

  // ---------------------------------------------------------------- categories: a skill belongs to the first category with a matching rule
  const RULE_KINDS = [["name", "이름이"], ["path", "경로에"], ["desc", "설명에"]];
  const CAT_ICONS = ctx.catIconChoices; // the same choices as a crew category's icon (app.js)
  const globRe = (g) => new RegExp(g.split("*").map((p) => p.replace(/[.+?^${}()|[\]\\]/g, "\\$&")).join(".*"), "i");
  function ruleHit([kind, value], sk) {
    const v = value.trim();
    if (!v) return false;
    if (kind === "name") return new RegExp(`^${globRe(v).source}$`, "i").test(sk.name || "");
    return globRe(v).test(kind === "path" ? `${sk.id || ""} ${sk.source_path || ""}` : sk.description || "");
  }
  const inCategory = (cat, sk) => cat.rules.some((r) => ruleHit(r, sk));
  function categoryOf(sk, cats = prefs.categories) {
    return cats.find((c) => inCategory(c, sk)) || null;
  }
  const tr = (s) => (window.ArkismI18n ? window.ArkismI18n.translate(s) : s); // 규칙 값(사용자 입력) 앞의 말만 번역
  const ruleSummary = (cat) => cat.rules.filter(([, v]) => v.trim()).map(([k, v]) => `${tr(RULE_KINDS.find(([x]) => x === k)[1])} ${v}`).join(" · ") || "규칙 없음";

  // ---------------------------------------------------------------- small builders
  function sw(on, label, onToggle) {
    const b = el("button", "switch");
    b.type = "button";
    b.setAttribute("role", "switch");
    b.setAttribute("aria-checked", String(on));
    b.setAttribute("aria-label", label);
    b.append(el("i"));
    b.addEventListener("click", () => onToggle(!on));
    return b;
  }
  const group = (title, hint) => {
    const g = el("section", "pg-group");
    g.append(el("h2", "", title));
    if (hint) g.append(el("p", "pg-hint", hint));
    return g;
  };
  const line = (title, hint, right) => {
    const r = el("div", "setting");
    const t = el("div");
    t.append(el("strong", "", title));
    if (hint) t.append(el("small", "", hint));
    r.append(t, right);
    return r;
  };
  const pill = (text, on) => el("span", `pill${on ? " on" : ""}`, text);

  // ---------------------------------------------------------------- app update (desktop only)
  const update = { info: null, busy: false, text: "아직 확인하지 않았어요", label: "업데이트 확인", current: "" };
  // The running version is known at start (Tauri app API), before any update check answers.
  void window.__TAURI__?.app?.getVersion?.().then((v) => { update.current = v; if (state.view === "settings" && state.settingsTab === "info") render(false); }, () => {});
  function setUpdate(text, label) {
    update.text = text; update.label = label;
    $("#settings-button").classList.toggle("has-update", !!update.info?.newer);
    if (state.view === "settings" && state.settingsTab === "info") render(false);
  }
  async function checkUpdate(quiet) {
    if (!tauri || update.busy) return;
    if (!state.defaults.updates) {
      if (!quiet) setUpdate("이 빌드에는 업데이트 주소가 없어요. 새 버전은 직접 설치해 주세요.", "다시 확인");
      return;
    }
    update.busy = true;
    if (!quiet) setUpdate("확인하는 중…", "확인 중");
    try {
      update.info = await tauri.invoke("update_check");
      update.busy = false;
      if (update.info.newer) {
        setUpdate(`새 버전 ${update.info.latest}이 있어요. 설치하면 앱이 잠깐 닫혔다 다시 열려요.`, "지금 업데이트");
        if (quiet) notice(`Arkism ${update.info.latest} 새 버전이 있어요. 설정 › 일반에서 설치할 수 있어요.`);
      } else setUpdate("최신 버전이에요.", "다시 확인");
    } catch (err) {
      update.busy = false;
      update.info = null;
      if (!quiet) setUpdate(String(err?.message || err).includes("unreachable") ? "업데이트 서버에 연결하지 못했어요. 네트워크 연결을 확인하세요." : "업데이트를 확인하지 못했어요.", "다시 확인");
    }
  }
  async function installUpdate() {
    update.busy = true;
    setUpdate("내려받는 중… 끝나면 앱이 다시 열려요.", "설치 중");
    try {
      await tauri.invoke("update_install");
    } catch (err) {
      update.busy = false;
      const code = String(err?.message || err);
      if (code.includes("update_manual")) setUpdate(`새 버전 ${update.info?.latest || ""}은 GitHub Releases에서 내려받아 바꿔 주세요. 기억은 그대로 남아요.`, "다시 확인");
      else setUpdate(code.includes("hash") ? "내려받은 파일이 맞지 않아 설치하지 않았어요." : "설치하지 못했어요. 잠시 뒤 다시 눌러 주세요.", "다시 시도");
    }
  }

  // ---------------------------------------------------------------- 설정
  const messenger = window.ArkismMessenger(ctx);
  // 설정 pages (Figma 07 · 설정, 03 · 모델): [title, one-line description]. The tabs themselves are the area tabs in shell.js.
  const SETTINGS_TABS = {
    general: ["일반", "대화 수집과 앱 시작을 정해요."],
    connect: ["연결", "이 PC의 Arkism을 쓸지, 서버의 Arkism에 이을지 정해요."],
    messenger: ["메신저", "디스코드·텔레그램 같은 메신저에 봇을 연결해요. 메신저마다 봇은 1개예요."],
    appearance: ["모양", "바꾸면 바로 적용돼요."],
    agents: ["에이전트", "스킬과 대화를 읽어 올 에이전트를 정해요."],
    workflow: ["페르소나", "AI 노드가 맡을 역할과 말투예요."],
    categories: ["카테고리", "스킬을 규칙에 따라 묶어요. 위에 있는 카테고리가 먼저 맞춰져요."],
    advanced: ["고급", "백업·진단·데이터 지우기처럼 자주 바꾸지 않는 설정이에요."],
    info: ["정보", "앱 버전과 Arkism이 기억을 다루는 규칙이에요."],
    models: ["모델 설정", "공급자 → 계정 → 모델 순서로 설정해요."],
    routing: ["라우팅 설정", "질문마다 어떤 모델로 보낼지, 실패하면 어디로 넘길지 정해요."]
  };
  // The right column and the area-bar buttons of the settings page being drawn (renderSettings fills them in).
  let sideParts = [], headParts = [];
  const toSide = (...nodes) => { sideParts.push(...nodes); };
  const toHead = (...nodes) => { headParts.push(...nodes); };
  function sideSec(title, ...nodes) {
    const s = el("section", "v4-side-sec");
    if (title) s.append(el("h3", "", title));
    s.append(...nodes);
    return s;
  }
  function kvRow(k, v) {
    const r = el("div", "v4-kv");
    r.append(el("span", "", k), typeof v === "string" ? el("b", "", v) : v);
    return r;
  }
  const sideNote = (text) => el("p", "v4-side-note", text);
  const actionsRow = (...buttons) => { const r = el("div", "v4-side-actions"); r.append(...buttons); return r; };
  // A settings group moved to the right column: its title becomes the section title.
  const asSide = (grp) => sideSec(grp.querySelector("h2")?.textContent || "", ...[...grp.childNodes].filter((n) => n.tagName !== "H2"));
  const AGENT_PATHS = {
    claude_code: ["~/.claude/skills", "~/.claude/projects"],
    codex: ["~/.codex/skills · ~/.agents/skills", "~/.codex/sessions"],
    zcode: ["~/.zcode/skills", "~/.zcode/sessions"],
    gemini: ["~/.gemini/skills · ~/.agents/skills", "~/.gemini/tmp"],
    grok: ["~/.grok/skills · ~/.claude/skills", "~/.grok/sessions"]
  };

  const openInjectPreview = () => window.dispatchEvent(new Event("arkism:inject-preview")); // a popup over 설정, no page change
  // 설정 › 정보: what the owner cannot change (the server's memory rules) and the app version.
  function tabInfo() {
    // 이 규칙은 서버가 정한다. 바꿀 수 있는 것처럼 보이지 않게 스위치·알약 대신 글자로만 보여 준다.
    const fixed = (text) => el("output", "", text);
    const a = group("기억이 쓰이는 방식", "Arkism이 정한 규칙이라 바꿀 수 없어요.");
    a.append(line("선호·작업 규칙", "말투·보고 방식·작업 절차처럼 늘 지킬 것", fixed("대화 시작마다")),
      line("결정·교훈", "질문과 관련 있을 때만 넣어요", fixed("질문할 때")));
    const c = group("자동으로 저장하는 방식", "저장한 기억은 참고 자료로만 쓰고, 지시로 따르지 않아요.");
    c.append(line("내가 직접 말한 것", "“앞으로 ~해”처럼 말한 선호·결정·규칙", fixed("바로 저장")),
      line("AI가 추론한 교훈·결정", "내가 승인해야 쓰여요", fixed("검토 후 저장")),
      line("명령처럼 보이는 문장", "붙여 넣은 글이나 웹 문서에 섞인 지시", fixed("항상 내가 확인")));
    toSide(sideSec("이 앱", ...(tauri ? [kvRow("버전", update.info?.current || update.current || "—")] : []), kvRow("연결", state.mode === "local" ? "이 PC" : state.mode === "remote" ? "SSH" : "서버 주소")),
      sideNote("기억 규칙은 연결한 Arkism 서버가 정해요. 앱을 업데이트해도 저장한 기억은 그대로예요."));
    return tauri ? [updateGroup(), a, c] : [a, c];
  }
  function tabGeneral() {
    toSide(sideSec("새 세션이 받는 기억", actionsRow(button("미리보기 열기", "", null, openInjectPreview))),
      sideNote("저장소마다 달라요. 미리보기에서 저장소를 골라 보세요."));
    const d = group("정리");
    d.append(line("답에 안 드러난 기억 모으기", "7일 동안 3번 넘게 넣었는데 답과 겹치지 않으면 검토 목록에 모아요",
      sw(prefs.collectUnused !== false, "답에 안 드러난 기억 모으기", (on) => { prefs.collectUnused = on; savePrefs(); render(false); })));
    if (!tauri) return [d];
    const capture = captureGroup();
    // 앱이 떠 있어야 수집·정리가 돈다: 로그인할 때 트레이로 시작(기본 켜짐).
    const start = group("시작");
    const slot = el("span");
    const startLine = line("로그인할 때 Arkism 시작", "창 없이 트레이에서 시작해 대화를 계속 저장해요.", slot);
    const draw = (on) => slot.replaceChildren(sw(on, "로그인할 때 Arkism 시작", (next) => tauri.invoke("autostart_set", { on: next })
      .then(() => { draw(next); notice(next ? "로그인할 때 Arkism이 트레이에서 시작해요." : "로그인할 때 시작하지 않아요. 앱이 꺼져 있으면 수집도 멈춰요."); })
      .catch(() => notice("시작 설정을 바꾸지 못했어요."))));
    tauri.invoke("autostart_get").then(draw, () => slot.replaceChildren(el("output", "", "확인 못 함")));
    start.append(startLine);
    return [...capture, d, start];
  }

  // 07 설정 › 에이전트: what the server found plus the owner's own switch for this app's tables.
  const AGENT_LOGOS = { claude_code: "img:claude.svg", codex: "sym:a-codex", gemini: "sym:a-gemini", zcode: "sym:a-zcode", grok: "sym:i-x" };
  function monogram(name) {
    const t = el("span", "agent-tile mono-tile");
    t.textContent = (name || "?").replace(/[^A-Za-z0-9가-힣]/g, "").slice(0, 2).toUpperCase();
    t.title = name;
    return t;
  }
  const logoOf = (a) => (AGENT_LOGOS[a.id] ? agentLogo(AGENT_LOGOS[a.id], a.name) : monogram(a.name));
  async function agentRequest(body, okText) {
    try {
      await request("/v1/agent_tools/requests", { method: "POST", body: JSON.stringify({ scope: state.scope, ...body }) });
      notice(okText);
      await reloadTools();
    } catch (e) {
      notice(`보내지 못했어요 (${e.message}).`);
    }
  }
  const pendingAgent = (id) => (state.data.tools?.requests || []).filter((q) => q.status === "pending" && q.kind === "agent" && q.agent === id).pop();
  // A switch shows what the owner asked for at once; the server applies it within a minute ("적용 대기" until then).
  const agentWanted = (a) => { const q = pendingAgent(a.id); return q ? !!q.enabled : !!a.enabled; };
  // 설정 › 에이전트 (Figma 1300:5254): the agents on the left, the chosen one's accounts, models and link to Arkism on the right.
  // An agent with a subscription login and a stored API key gets two rows ("claude_code" and "claude_code:api").
  let selAgent = null;
  const AGENT_SITES = { anthropic: ["claude.ai", "https://claude.ai"], openai: ["chatgpt.com", "https://chatgpt.com"], google: ["gemini.google.com", "https://gemini.google.com"], zai: ["z.ai", "https://z.ai"], xai: ["grok.com", "https://grok.com"] };
  function tabAgents() {
    const tools = state.data.tools;
    const host = tools?.agents || [];
    const servers = tools?.servers || [];
    // Arkism reads these: the host's enabled agents.
    const agents = host.filter(agentWanted);
    if (!host.length) for (const [key, name] of AGENTS) agents.push({ id: key, name, skills_dirs: [(AGENT_PATHS[key] || [])[0]], chats_dir: (AGENT_PATHS[key] || [])[1], enabled: true, skills: null });
    const entries = [];
    for (const a of agents) {
      const src = models.agentSource(a.id);
      if (!src || src.accounts.length || !src.keys.length) entries.push({ key: a.id, a, src, api: false });
      if (src?.keys.length) entries.push({ key: `${a.id}:api`, a, src, api: true });
    }
    const cur = entries.find((e) => e.key === selAgent) || entries[0] || null;
    selAgent = cur?.key || null;
    const isOn = (a) => {
      const shownKey = AGENTS.some(([k]) => k === a.id) ? a.id : null;
      return shownKey ? !prefs.agentsOff.includes(shownKey) : agentWanted(a);
    };
    const toggle = (a, next) => {
      const shownKey = AGENTS.some(([k]) => k === a.id) ? a.id : null;
      if (shownKey) {
        if (!next && shownAgents().length === 1) { notice("에이전트가 하나는 켜져 있어야 해요."); return; }
        prefs.agentsOff = next ? prefs.agentsOff.filter((k) => k !== shownKey) : [...prefs.agentsOff, shownKey];
        savePrefs();
        render(false);
      } else {
        void agentRequest({ agent: a.id, kind: "agent", name: a.name, enabled: next, skills_dir: next ? (a.skills_dirs || [])[0] : undefined }, next ? `${a.name}를 켰어요.` : `${a.name}를 껐어요. 스킬과 대화를 더 읽지 않아요.`);
      }
    };
    // Arkism's own MCP server on for this agent = its memory and skill tools are linked.
    const arkismOn = (id) => servers.some((x) => x.name === "arkism" && x.agents?.[id] === "on");
    const mcpOn = (id) => servers.some((x) => x.agents?.[id] === "on");
    const skillCount = (a) => (a.skills === null || a.skills === undefined ? state.data.skills.filter((sk) => allowed(sk, a.id)).length : a.skills);

    const list = el("section", "ag-list");
    for (const e of entries) {
      const { a, src } = e;
      const on = isOn(a);
      const row = el("div", `ag-row${e === cur ? " on" : ""}${on ? "" : " off"}`);
      const info = el("button", "ag-row-main");
      info.type = "button";
      const top = el("div", "ag-row-top");
      top.append(el("strong", "", a.name));
      const plan = e.api ? "API" : models.planName(src?.main?.plan);
      if (plan) top.append(el("span", "ag-plan", plan));
      if (!e.api && src) top.append(...models.cliBadge(src));
      if (pendingAgent(a.id)) top.append(el("span", "tag warn", "적용 대기"));
      if (a.custom) top.append(el("span", "tag", "직접 추가"));
      const sub = [arkismOn(a.id) ? "기억 연결됨" : "", `스킬 ${skillCount(a)}`, mcpOn(a.id) ? "MCP" : ""].filter(Boolean).join(" · ");
      info.append(top, el("small", "", sub));
      info.addEventListener("click", () => { selAgent = e.key; render(false); });
      const go = el("button", "icon-btn plain ag-chev");
      go.type = "button";
      go.setAttribute("aria-label", `${a.name} 자세히`);
      go.append(icon("caret-right"));
      go.addEventListener("click", () => { selAgent = e.key; render(false); });
      row.append(logoOf(a), info, sw(on, `${a.name} 켜기`, (next) => toggle(a, next)), go);
      list.append(row);
    }
    const add = el("button", "ag-add-agent");
    add.type = "button";
    add.append(icon("plus"), el("span", "", "에이전트 추가"));
    add.addEventListener("click", () => openAddAgent());
    list.append(add);
    const out = [list];
    if (!tools?.agents) out.push(el("p", "pg-hint", "서버가 아직 에이전트 목록을 보내지 않았어요. 잠시 뒤 다시 열어 보세요."));

    // 함께 읽는 공용 폴더
    const shared = group("함께 읽는 공용 폴더", "여러 에이전트가 함께 읽는 스킬 폴더예요. 한 번 넣으면 모두 써요.");
    const sbox = el("div", "pg-card");
    for (const dir of tools?.shared_dirs || ["~/.agents/skills"]) {
      const row = el("div", "shared-line");
      const readers = host.filter((a) => agentWanted(a) && (a.skills_dirs || []).includes(dir));
      const logos = el("span", "crew-logos");
      readers.forEach((a) => logos.append(logoOf(a)));
      row.append(el("span", "mono", dir), icon("arrow-right"), logos);
      if (readers.length) row.append(el("span", "shared-readers", `${readers.map((a) => a.name).join(" · ")}가 읽음`));
      if (dir !== "~/.agents/skills") {
        const del = el("button", "icon-btn plain");
        del.type = "button";
        del.setAttribute("aria-label", `${dir} 빼기`);
        del.append(icon("x"));
        del.addEventListener("click", () => agentRequest({ agent: "shared", kind: "shared", name: dir, enabled: false }, "공용 폴더를 뺐어요."));
        row.append(del);
      }
      sbox.append(row);
    }
    const addDir = el("div", "shared-add");
    const dirIn = el("input");
    dirIn.placeholder = "~/team/skills";
    dirIn.className = "mono";
    addDir.append(dirIn, button("폴더 추가", "ghost small", "plus", () => {
      const d = dirIn.value.trim();
      if (!/^(~\/|\/)/.test(d) || d.includes("..")) { notice("~/ 나 / 로 시작하는 폴더를 써 주세요."); return; }
      void agentRequest({ agent: "shared", kind: "shared", name: d, enabled: true }, "공용 폴더를 넣었어요. 1분 안에 반영돼요.");
    }));
    sbox.append(addDir);
    shared.append(sbox);

    const found = host.filter((a) => a.found && !agentWanted(a));
    let foundBar = null;
    if (found.length) {
      foundBar = el("button", "found-bar");
      foundBar.type = "button";
      const logos = el("span", "crew-logos");
      found.forEach((a) => logos.append(logoOf(a)));
      foundBar.append(el("span", "", "이 PC에서 찾은 새 에이전트"), logos, el("strong", "", "추가하기 ›"));
      foundBar.addEventListener("click", () => openAddAgent(found[0].id));
    }
    const [roles, kit] = agentRoleCards(foundBar);
    out.push(roles, shared, kit);

    if (cur) toSide(...agentDetail(cur, { isOn, toggle, arkismOn, tools }));
    return out;
  }
  // The right column for the chosen row: who it is, its accounts or keys, its models, and how it is linked to Arkism.
  function agentDetail(e, { isOn, toggle, arkismOn, tools }) {
    const { a, src } = e;
    const head = el("div", "ag-detail-head");
    head.append(logoOf(a), el("strong", "", e.api ? `${a.name} API` : a.name));
    const site = src && AGENT_SITES[src.p.id];
    if (site) {
      const link = el("a", "ag-site", `${site[0]} ↗`);
      link.href = site[1]; link.target = "_blank"; link.rel = "noreferrer";
      head.append(link);
    }
    head.append(sw(isOn(a), `${a.name} 켜기`, (next) => toggle(a, next)));
    const parts = [head];
    if (src) parts.push(e.api ? models.agentKeys(src) : models.agentAccounts(src, a.name), models.modelsCard(src.p, true));
    const link = el("section", "ag-card ag-link");
    link.append(el("strong", "", "Arkism 연결"));
    const age = tools?.collected_at ? Math.round((Date.now() - new Date(tools.collected_at).getTime()) / 60000) : null;
    const dot = (ok, text) => { const s = el("span", `ag-state${ok ? " ok" : ""}`); s.append(el("i"), el("span", "", text)); return s; };
    link.append(kvRow("MCP", dot(arkismOn(a.id), arkismOn(a.id) ? "연결됨 · 기억·스킬 도구" : "연결 안 됨")),
      kvRow("대화 수집", isOn(a) ? `켜짐${age == null ? "" : ` · 마지막 ${age < 1 ? "방금" : `${age}분 전`}`}` : "꺼짐"));
    const dirs = [...(a.skills_dirs || []).map((d) => kvRow("스킬", el("span", "mono", d))), ...(a.chats_dir ? [kvRow("대화", el("span", "mono", a.chats_dir))] : [])];
    if (dirs.length) link.append(el("strong", "ag-sub", "폴더"), ...dirs);
    parts.push(link);
    if (tools?.agents) {
      const rm = el("button", "link-btn ag-remove", "에이전트 제거");
      rm.type = "button";
      rm.addEventListener("click", async () => { if (await ask(`${a.name}를 목록에서 뺄까요? 에이전트 쪽 파일은 그대로 둬요.`)) void agentRequest({ agent: a.id, kind: "agent", name: a.name, enabled: false }, `${a.name}를 뺐어요.`); });
      parts.push(rm);
    }
    return parts;
  }
  // 08 에이전트 추가
  function openAddAgent(preselect) {
    const host = state.data.tools?.agents || [];
    const choices = host.filter((a) => !agentWanted(a) && !a.custom);
    let sel = preselect || choices.find((a) => a.found)?.id || null;
    const dlg = el("dialog", "crew-dialog add-agent");
    const form = el("form");
    form.method = "dialog";
    const head = el("div", "dialog-head");
    const ht = el("div", "");
    ht.append(el("h2", "", "에이전트 추가"), el("p", "", "목록에서 고르거나, 스킬 폴더를 직접 지정하세요."));
    const close = el("button", "icon-btn plain");
    close.value = "cancel";
    close.setAttribute("aria-label", "닫기");
    close.append(icon("x"));
    head.append(ht, close);
    const q = el("input");
    q.placeholder = "에이전트 이름으로 찾기";
    const grid = el("div", "agent-choices");
    const name = el("input");
    name.placeholder = "예: Aider";
    const skillsDir = el("input");
    skillsDir.className = "mono";
    skillsDir.placeholder = "~/.aider/skills";
    const chatsDir = el("input");
    chatsDir.className = "mono";
    chatsDir.placeholder = "~/.aider/history";
    const ok = button("추가", "primary");
    ok.type = "submit";
    ok.value = "ok";
    const draw = () => {
      const term = q.value.trim().toLowerCase();
      grid.replaceChildren(...choices.filter((a) => !term || a.name.toLowerCase().includes(term)).map((a) => {
        const b = el("button", `agent-choice${sel === a.id ? " on" : ""}`);
        b.type = "button";
        const t = el("div", "");
        const h = el("strong", "", a.name);
        if (a.found) h.append(el("small", "tag", "찾음"));
        t.append(h, el("small", "mono dim", (a.skills_dirs || [])[0] || ""));
        b.append(logoOf(a), t);
        b.addEventListener("click", () => { sel = a.id; name.value = ""; draw(); });
        return b;
      }));
      if (!choices.length) grid.append(el("p", "dim", "목록의 에이전트는 모두 켜져 있어요."));
      const manual = name.value.trim();
      if (manual) ok.textContent = `${manual} 추가`;
      else if (sel) ok.textContent = `${choices.find((a) => a.id === sel)?.name || ""} 추가`;
      else ok.textContent = "추가";
    };
    q.addEventListener("input", draw);
    name.addEventListener("input", () => {
      if (name.value.trim()) {
        sel = null;
      }
      draw();
    });
    const manualBox = el("div", "agent-manual");
    const mh = el("div", "agent-manual-head");
    mh.append(icon("plus"), el("strong", "", "직접 추가"), el("small", "dim", "목록에 없는 에이전트"));
    const f = (label, input) => { const l = el("label", "field", label); l.append(input); return l; };
    manualBox.append(mh, f("이름", name), f("스킬 폴더", skillsDir), f("대화 기록 폴더 (선택)", chatsDir));
    const body = el("div", "dialog-body");
    body.append(q, grid, manualBox);
    const foot = el("div", "dialog-foot");
    const cancel = button("취소", "ghost");
    cancel.type = "submit";
    cancel.value = "cancel";
    foot.append(cancel, ok);
    form.append(head, body, foot);
    form.addEventListener("submit", (e) => {
      if (e.submitter?.value !== "ok") return;
      const manual = name.value.trim();
      if (manual) {
        const slug = manual.toLowerCase().replace(/[^a-z0-9]+/g, "-"); // runs collapse to one dash, so no "--" remains
        const id = slug.replace(/^-|-$/g, "").replace(/^([^a-z])/, "a$1").slice(0, 40);
        const dir = skillsDir.value.trim();
        if (!id || !/^(~\/|\/)/.test(dir) || dir.includes("..")) { e.preventDefault(); notice("영문 이름과 ~/ 로 시작하는 스킬 폴더를 써 주세요."); return; }
        void agentRequest({ agent: id, kind: "agent", name: manual, enabled: true, skills_dir: dir, chats_dir: chatsDir.value.trim() || undefined }, `${manual}를 추가했어요. 1분 안에 반영돼요.`);
      } else if (sel) {
        const a = choices.find((x) => x.id === sel);
        void agentRequest({ agent: a.id, kind: "agent", name: a.name, enabled: true, skills_dir: (a.skills_dirs || [])[0] }, `${a.name}를 추가했어요. 1분 안에 반영돼요.`);
      }
    });
    dlg.append(form);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    draw();
    dlg.showModal();
  }

  // Categories are edited in a draft and saved together.
  let draft = null;
  const catTalk = {}; // AI로 다시 분류 talk (ai-chat.js state)
  let open = null;
  const dirty = () => draft && JSON.stringify(draft) !== JSON.stringify(prefs.categories);
  // Figma 198:6220: a table of categories on the left, the open one's editor on the right.
  function tabCategories() {
    draft ||= structuredClone(prefs.categories);
    const skills = state.data.skills;
    if (!draft.some((c) => c.id === open)) open = draft[0]?.id || null;
    const g = el("section", "pg-group v4-plain");
    const list = el("div", "v4-table cat-table");
    const th = el("div", "v4-tr th");
    th.append(el("span", "", "카테고리"), el("span", "", "스킬"), el("span", "", "규칙"));
    list.append(th);
    draft.forEach((cat, i) => {
      const mine = skills.filter((sk) => categoryOf(sk, draft)?.id === cat.id);
      const row = el("button", `v4-tr${open === cat.id ? " selected" : ""}`);
      row.type = "button";
      row.setAttribute("aria-pressed", String(open === cat.id));
      const name = el("strong", "");
      name.append(icon(cat.icon, "icon dim"), cat.name || "이름 없음");
      row.append(name, el("span", "mono", String(mine.length)), el("span", "cat-rules", ruleSummary(cat)));
      row.addEventListener("click", () => { open = cat.id; render(false); });
      list.append(row);
      if (open === cat.id) toSide(catEditor(cat, i, mine));
    });
    if (!draft.length) { list.append(el("p", "pg-hint", "카테고리가 없어요. ‘새 카테고리’로 만들어요.")); toSide(sideNote("카테고리를 고르면 여기서 이름과 규칙을 고쳐요.")); }
    const add = button("새 카테고리", "", "plus", () => {
      const id = `c${Date.now().toString(36)}`;
      draft.push({ id, name: "새 카테고리", icon: "stack", rules: [["name", ""]] });
      open = id;
      render(false);
    });
    const rest = skills.filter((sk) => !categoryOf(sk, draft)).length;
    // AI로 전부 다시 분류 (ai-chat.js): the category persona puts each unsorted skill in one category; each category
    // it fills is a proposal (name rules there), nothing is kept before 저장.
    const ai = window.ArkismAIChat.button({ state: catTalk, label: "AI로 전부 다시 분류", title: "AI로 다시 분류", sub: "분류 안 된 스킬을 맞는 카테고리에 넣어요",
      greeting: "분류 안 된 스킬을 지금 카테고리에 나눠 넣을게요. 기준이 있으면 말해 주세요.", placeholder: "예: 깃 관련은 전부 릴리스로",
      quick: ["전부 분류해 줘", "확실한 것만 넣어 줘"],
      ask: async (want) => {
        const todo = skills.filter((sk) => !categoryOf(sk, draft || prefs.categories));
        const cats = draft || prefs.categories;
        if (!todo.length || !cats.length) return { say: cats.length ? "분류 안 된 스킬이 없어요." : "카테고리를 먼저 만들어 주세요." };
        const skillLines = todo.slice(0, 150).map((sk) => `${sk.name}: ${(sk.description || "").slice(0, 100)}`).join("\n");
        const prompt = `Categories: ${cats.map((c) => c.name).join(", ")}\nThe owner says: ${want}\n\nSkills (name: description):\n${skillLines}\n\nShape: {"skill name": "category name"}, using only the categories above. Leave out a skill no category fits.`;
        const a = await assist("json", prompt, { purpose: "category" });
        if (a.status !== "done") throw new Error(a.error || "AI가 답하지 못했어요");
        const text = a.output || "";
        const start = text.indexOf("{");
        const end = text.lastIndexOf("}");
        const map = start >= 0 && end > start ? JSON.parse(text.slice(start, end + 1)) : {};
        const by = new Map();
        for (const [skill, catName] of Object.entries(map)) {
          const cat = cats.find((c) => c.name === catName);
          if (!cat || !todo.some((sk) => sk.name === skill)) continue;
          by.set(cat.id, [...(by.get(cat.id) || []), skill]);
        }
        const props = [...by].map(([id, names]) => ({ label: cats.find((c) => c.id === id).name, from: [], to: names, apply: () => {
          draft ||= structuredClone(prefs.categories);
          const cat = draft.find((c) => c.id === id);
          if (!cat) return false;
          cat.rules = [...cat.rules.filter(([, v]) => v.trim()), ...names.filter((n) => !cat.rules.some(([k, v]) => k === "name" && v === n)).map((n) => ["name", n])];
          render(false);
        } }));
        const n = props.reduce((t, p) => t + p.to.length, 0);
        return { say: n ? `${n}개를 ${props.length}개 카테고리에 나눠 넣자고 제안해요. 아래에서 보고 적용한 뒤 저장해 주세요.` : "AI가 맞는 카테고리를 찾지 못했어요.", props };
      } });
    toHead(add);
    const restBar = el("div", "v4-bar");
    const restText = el("div", "");
    restText.append(el("span", "", `분류 안 된 스킬 ${rest}개`), el("small", "", "AI로 다시 분류하면 맞는 곳에 넣어요 · 규칙을 바꾸면 숫자가 바로 바뀌어요"));
    restBar.append(restText, ai.dock); // AI at the right end of this row; its purple list below it
    g.append(list, restBar, ai);
    const out = [g];
    if (dirty()) {
      const bar = el("div", "pg-save");
      bar.append(el("span", "", "저장하지 않은 변경이 있어요."),
        button("되돌리기", "ghost", null, () => { draft = null; render(false); }),
        button("저장", "primary", null, () => {
          draft.forEach((c) => { c.rules = c.rules.filter(([, v]) => v.trim()); c.name = c.name.trim() || "이름 없음"; });
          prefs.categories = draft; draft = null; savePrefs();
          // A filter on a category that no longer exists would hide every skill with no chip left to clear it.
          if (state.skillCat !== "all" && !(state.skillCat === "none" ? prefs.categories.length : prefs.categories.some((c) => c.id === state.skillCat))) state.skillCat = "all"; notice("카테고리를 저장했어요."); render(false);
        }));
      out.push(bar);
    }
    return out;
  }
  function catEditor(cat, i, mine) {
    const box = el("div", "cat-body");
    box.append(el("h2", "v4-side-title", cat.name || "이름 없음"));
    const name = el("input"); name.value = cat.name; name.maxLength = 30; name.setAttribute("aria-label", "카테고리 이름");
    name.addEventListener("input", () => { cat.name = name.value; });
    name.addEventListener("change", () => render(false));
    const nameField = el("label", "field", "이름"); nameField.append(name);
    const icons = el("div", "icon-pick");
    for (const n of CAT_ICONS) {
      const b = el("button", `icon-btn${n === cat.icon ? " on" : ""}`);
      b.type = "button"; b.setAttribute("aria-label", n); b.setAttribute("aria-pressed", String(n === cat.icon));
      b.append(icon(n));
      b.addEventListener("click", () => { cat.icon = n; render(false); });
      icons.append(b);
    }
    const iconField = el("div", "field", "아이콘"); iconField.append(icons);
    const top = el("div", "cat-top"); top.append(nameField, iconField);
    box.append(top, el("div", "pg-sub", "자동 분류 규칙 · 하나라도 맞으면"));
    cat.rules.forEach((rule, ri) => {
      const r = el("div", "rule");
      const kind = el("select");
      kind.setAttribute("aria-label", "규칙 종류");
      for (const [k, label] of RULE_KINDS) { const o = el("option", "", label); o.value = k; o.selected = k === rule[0]; kind.append(o); }
      kind.addEventListener("change", () => { rule[0] = kind.value; render(false); });
      const val = el("input", "mono"); val.value = rule[1]; val.placeholder = rule[0] === "name" ? "commit-*" : "git"; val.setAttribute("aria-label", "규칙 값");
      val.addEventListener("change", () => { rule[1] = val.value; render(false); });
      const del = el("button", "icon-btn plain"); del.type = "button"; del.setAttribute("aria-label", "조건 지우기"); del.append(icon("x"));
      del.addEventListener("click", () => { cat.rules.splice(ri, 1); render(false); });
      r.append(kind, val, del);
      box.append(r);
    });
    box.append(button("조건 추가", "ghost", "plus", () => { cat.rules.push(["name", ""]); render(false); }));
    const now = el("div", "match");
    now.append(icon("check"), "지금 맞는 스킬:");
    if (!mine.length) now.append(el("span", "pg-hint", "없어요"));
    for (const sk of mine.slice(0, 12)) now.append(el("code", "", sk.name));
    if (mine.length > 12) now.append(el("span", "pg-hint", `외 ${mine.length - 12}개`));
    box.append(now);
    const foot = el("div", "cat-foot");
    const up = button("위로", "ghost", null, () => { if (i) { [draft[i - 1], draft[i]] = [draft[i], draft[i - 1]]; render(false); } });
    up.disabled = i === 0;
    foot.append(up, button("카테고리 지우기", "ghost danger", "trash", () => { draft.splice(i, 1); open = null; render(false); }));
    box.append(foot);
    return box;
  }

  // 설정 › 일반 › 업데이트 (was its own tab, Figma 198:6306): the version line with 다시 확인 / 지금 업데이트. Desktop only.
  function updateGroup() {
    const g = group("업데이트"); // a build without an update feed says so in the status line (checkUpdate)
    const row = el("div", "v4-version");
    const left = el("div", "v4-version-text");
    const top = el("div", "v4-version-top");
    top.append(el("span", "", update.text));
    if (update.info?.newer) top.append(el("span", "tag warn", "새 버전"));
    const ver = el("div", "v4-version-num");
    ver.append(el("span", "", "Arkism"), el("strong", "mono", update.info ? (update.info.newer ? update.info.latest : update.info.current) : update.current || "—"));
    left.append(top, ver);
    if (update.info?.newer) left.append(el("small", "", `현재 버전 ${update.info.current}`));
    const acts = el("div", "v4-version-actions");
    const again = button(update.info?.newer ? "다시 확인" : update.label, update.info?.newer ? "" : "primary", null, () => checkUpdate(false));
    again.disabled = update.busy;
    acts.append(again);
    if (update.info?.newer) { const now = button("지금 업데이트", "primary", null, installUpdate); now.disabled = update.busy; acts.append(now); }
    row.append(left, acts);
    g.append(row);
    if (!update.info && !update.busy && update.label === "업데이트 확인") void checkUpdate(false);
    return g;
  }

  // 설정 › 모양: 언어·테마·글자 크기·화면 배율. 저장과 적용은 look.js가 맡는다.
  function tabAppearance() {
    const look = window.ArkismLook;
    const cur = look.get();
    const pick = (patch) => { look.set(patch); render(false); };
    const card = el("section", "pg-group v4-rows");
    const langs = segment([["ko", "한국어"], ["en", "English"]], cur.lang, (k) => pick({ lang: k }));
    langs.setAttribute("data-no-i18n", ""); // 언어 이름은 늘 그 언어로 보여 준다
    const zoom = el("div", "zoom-control");
    zoom.append(button("", "", "minus", () => pick({ zoom: cur.zoom - look.ZOOM_STEP })),
      el("output", "", `${Math.round(cur.zoom * 100)}%`),
      button("", "", "plus", () => pick({ zoom: cur.zoom + look.ZOOM_STEP })),
      button("기본값", "", null, () => pick({ zoom: 1 })));
    const THEMES = [["system", "시스템"], ["dark", "어둡게"], ["light", "밝게"]], FONTS = [["sm", "작게"], ["md", "보통"], ["lg", "크게"], ["xl", "아주 크게"]];
    card.append(line("화면 언어", "", langs),
      line("테마", "‘시스템’은 Windows 설정을 따라요.", segment(THEMES, cur.theme, (k) => pick({ theme: k }))),
      line("글자 크기", "글자만 키우거나 줄여요", segment(FONTS, cur.font, (k) => pick({ font: k }))),
      line("화면 배율", "화면 전체를 확대·축소해요", zoom));
    const keys = el("div", "v4-table");
    const th = el("div", "v4-tr th two"); th.append(el("span", "", "단축키"), el("span", "", "동작")); keys.append(th);
    for (const [k, v] of [["Ctrl +", "확대"], ["Ctrl −", "축소"], ["Ctrl 0", "기본값으로"]]) { const r = el("div", "v4-tr two"); r.append(el("span", "mono", k), el("span", "", v)); keys.append(r); }
    toSide(sideSec("화면 배율 단축키", keys));
    return [card];
  }
  // Edit models.json through set_model_roles (설정 › 에이전트: 역할별 모델 and 스킬·MCP·플러그인).
  const roleField = (value, label, placeholder, secret) => {
    const i = el("input", "mono");
    i.value = value || ""; i.placeholder = placeholder || ""; i.setAttribute("aria-label", label);
    if (secret) { i.type = "password"; i.autocomplete = "off"; }
    return i;
  };
  const roleSub = (label, input) => { const r = el("label", "v4-subfield"); r.append(el("span", "", label), input); return r; };
  const roleBlock = (row, ...extra) => { const b = el("div", "v4-role"); b.append(row, ...extra); return b; };
  const pick = (list, value, label, onChange) => {
    const s = el("select", "ag-select");
    s.setAttribute("aria-label", label);
    for (const [k, name] of list) { const o = el("option", "", name); o.value = k; o.selected = k === value; s.append(o); }
    s.addEventListener("change", () => onChange(s.value));
    return s;
  };
  const JUDGE = [["fixed", "고정 규칙"], ["chat", "내 모델"], ["clef", "Clef"], ["jev", "Jev"]];
  // Saves `patch` over the loaded roles and restarts the built-in Arkism. Empty secrets keep the saved ones.
  async function saveRoles(cur, patch) {
    notice("저장하고 Arkism을 다시 시작하는 중이에요.");
    try {
      const all = { ...cur, cloudflare_token: "", jev_key: "", picker_key: "", ...patch };
      const roles = Object.fromEntries(Object.entries(all).filter(([k]) => !k.endsWith("_saved")));
      await tauri.invoke("set_model_roles", { roles });
      await startLocal();
      notice("저장했어요.");
      return true;
    } catch (e) { notice(String(e).includes("invalid") ? "모델 이름이나 주소를 확인해 주세요." : "저장하지 못했어요."); return false; }
  }
  // 라우팅 판정 모델: its select, the selected judge's fields, and read() → the patch, or null after telling what is missing.
  function judgeBlock(cur) {
    const savedHint = (k, placeholder) => (cur[`${k}_saved`] ? "저장됨 · 바꿀 때만 입력" : placeholder);
    const account = roleField(cur.cloudflare_account, "Cloudflare 계정 ID", "account id");
    const token = roleField("", "Cloudflare API 토큰", savedHint("cloudflare_token", "API token"), true);
    const jevKey = roleField("", "Jev API 키", savedHint("jev_key", "API key"), true);
    const pickerUrl = roleField(cur.picker_url, "판별 모델 주소", "http://127.0.0.1:11434/v1");
    const pickerModel = roleField(cur.picker_model, "판별 모델", "qwen3:4b");
    const pickerKey = roleField("", "판별 모델 API 키", savedHint("picker_key", "없으면 비워 두기"), true);
    // Only the selected judge's settings are shown; the others keep their saved values.
    const judgeFields = {
      chat: [roleSub("주소", pickerUrl), roleSub("모델", pickerModel), roleSub("API 키", pickerKey)],
      clef: [roleSub("Cloudflare 계정 ID", account), roleSub("Cloudflare API 토큰", token)],
      jev: [roleSub("Jev API 키", jevKey)],
    };
    let judge = cur.judge || "fixed";
    const judgeBox = el("div", "v4-judge-fields");
    const showJudge = (k) => judgeBox.replaceChildren(...(judgeFields[k] || []));
    showJudge(judge);
    const node = roleBlock(line("라우팅 판정", "요청마다 맞는 모델과 스킬·MCP를 골라요.",
      pick(JUDGE, judge, "라우팅 판정 모델", (k) => { judge = k; showJudge(k); })), judgeBox);
    const read = () => {
      const next = { judge, cloudflare_account: account.value.trim(), cloudflare_token: token.value.trim(), jev_key: jevKey.value.trim(),
        picker_url: pickerUrl.value.trim(), picker_model: pickerModel.value.trim(), picker_key: pickerKey.value.trim() };
      const has = (k) => next[k] || cur[`${k}_saved`];
      if (judge === "clef" && (!next.cloudflare_account || !has("cloudflare_token"))) { notice("Clef를 쓰려면 Cloudflare 계정 ID와 토큰이 필요해요."); return null; }
      if (judge === "jev" && !has("jev_key")) { notice("Jev를 쓰려면 API 키가 필요해요."); return null; }
      if (judge === "chat" && (!next.picker_url || !next.picker_model)) { notice("내 모델로 판별하려면 주소와 모델 이름이 필요해요."); return null; }
      if (judge === "chat" && !/^(https:\/\/|http:\/\/(127\.0\.0\.1|localhost)[:/])/.test(next.picker_url)) { notice("주소는 https이거나 이 PC(127.0.0.1)여야 해요."); return null; }
      return next;
    };
    return { node, read };
  }
  // 역할별 모델 and 스킬·MCP·플러그인 share one models.json load and one 저장 (at the bottom of the second card, as in Figma).
  // Kept across re-renders of the tab so typed values survive; dropped when the tab changes.
  let kitCard = null;
  const ROOTS = [["skills_root", "스킬 폴더"], ["mcp_root", "MCP 폴더"], ["plugins_root", "플러그인 폴더"]];
  const ROLE_MODELS = [["haiku", "Claude Haiku"], ["sonnet", "Claude Sonnet"], ["opus", "Claude Opus"]];
  const EMBED = [["auto", "자동 (Ollama가 있으면)"], ["ollama", "Ollama"], ["none", "끄기"]];
  function agentRoleCards(foundBar) {
    if (kitCard) { kitCard[1].querySelector(".found-bar")?.remove(); if (foundBar) kitCard[1].querySelector(".v4-kit-body")?.before(foundBar); return kitCard; }
    const roles = group("역할별 모델");
    roles.classList.add("ag-roles");
    const kit = group("스킬·MCP·플러그인", "에이전트가 함께 쓰는 스킬·MCP·플러그인 폴더예요.");
    kitCard = [roles, kit];
    // AI 도구 saves on its own (모델 설정); servers without 모델 설정 have nothing to pick.
    if (state.data.providers != null) roles.append(roleBlock(line("AI 도구", "프롬프트 칸 채우기, 글 다듬기, 분류에 써요.", models.assistPicker())));
    const ROWS = [["라우팅 판정", "요청마다 맞는 모델과 스킬·MCP를 골라요."],
      ["기억 추출", "대화에서 기억 후보를 뽑아요. 가벼운 모델이 좋아요."],
      ["반성·검토", "3시간마다 교훈을 정리하고 검토할 기억을 골라요."],
      ["임베딩", "기억을 뜻으로 찾을 때 써요. 바꾸면 기억을 다시 색인해요."]];
    const kitBody = el("div", "v4-kit-body");
    if (foundBar) kit.append(foundBar);
    kit.append(kitBody);
    if (!(tauri && state.mode === "local")) {
      // No server API reports its role models (they are env on the host), so say only that the server decides.
      for (const [t, hint] of ROWS) roles.append(roleBlock(line(t, hint, pill("서버가 정함"))));
      kitBody.append(el("p", "pg-hint", "서버에 연결해 쓰는 동안에는 서버가 정한 위치와 모델을 써요."));
      return kitCard;
    }
    const roleBody = el("div", "v4-roles-body");
    roles.append(roleBody);
    let busy = false;
    const draw = (cur) => {
      const next = {};
      const judge = judgeBlock(cur);
      const embedModel = roleField(cur.embed_model, "임베딩 모델", "qwen3-embedding:0.6b");
      const embedSub = roleSub("Ollama 모델 이름", embedModel);
      embedSub.hidden = cur.embedder === "none";
      roleBody.replaceChildren(judge.node,
        roleBlock(line(...ROWS[1], pick(ROLE_MODELS, cur.extract_model || "haiku", "기억 추출 모델", (k) => { next.extract_model = k; }))),
        roleBlock(line(...ROWS[2], pick(ROLE_MODELS, cur.reflect_model || "sonnet", "반성·검토 모델", (k) => { next.reflect_model = k; }))),
        roleBlock(line(...ROWS[3], pick(EMBED, cur.embedder || "auto", "임베딩", (k) => { next.embedder = k; embedSub.hidden = k === "none"; })), embedSub));
      const inputs = ROOTS.map(([k, label]) => [k, roleField(cur[k], label, "기본 위치")]);
      const folders = roleBlock(line("폴더 위치", "비워 두면 기본 위치를 써요", el("span")), ...inputs.map(([, i], n) => roleSub(ROOTS[n][1], i)));
      const actions = el("div", "pg-actions");
      actions.append(button("저장", "primary", null, async () => {
        if (busy) return;
        const patch = judge.read();
        if (!patch) return;
        for (const [k, i] of inputs) {
          const v = i.value.trim();
          if (v && !/^(\/|[A-Za-z]:[\\/])/.test(v)) { notice("폴더 위치는 / 또는 드라이브 문자(C:\\)로 시작하는 전체 경로여야 해요."); return; }
          patch[k] = v;
        }
        Object.assign(patch, next, { embed_model: embedModel.value.trim() || "qwen3-embedding:0.6b" });
        busy = true;
        try { if (await saveRoles(cur, patch)) load(); } finally { busy = false; }
      }));
      kitBody.replaceChildren(folders, actions);
    };
    const load = () => tauri.invoke("model_roles").then(draw, () => roleBody.replaceChildren(el("p", "pg-hint", "모델 설정을 읽지 못했어요.")));
    load();
    return kitCard;
  }

  // 설정 › 연결 (Figma 293:18786): 연결 방식 on the left; what is connected now, switching back and 진단 on the right.
  function tabConnect() {
    // 연결 방식: 이 PC(내장) 또는 SSH. SSH는 서버의 Arkism을 터널로 잇는다 — 토큰·주소를 넣을 일이 없다.
    const g = group("연결 방식");
    const modes = [];
    if (state.defaults.local) modes.push(["local", "desktop", "이 PC", "이 PC에서 Arkism을 실행해요. 인터넷 없이도 돼요."]);
    modes.push(["ssh", "terminal", "SSH로 연결", "서버의 Arkism을 SSH로 이어요. 서버에서 따로 할 일은 없어요."]);
    let pick = state.mode === "local" ? "local" : "ssh";
    const cards = el("div", "mode-cards"), body = el("div", "mode-body"), actions = el("div", "pg-actions mode-actions");
    const form = sshFields(state.ssh);
    const out = el("p", "pg-hint mode-out");
    out.setAttribute("role", "status");
    const say = (text, bad) => { out.textContent = text; out.classList.toggle("warn", !!bad); };
    const test = button("SSH 테스트", "ghost", null, async () => {
      test.disabled = true;
      say("확인하는 중…");
      const r = await sshTest(form.read());
      say(r.text, !r.ok);
      test.disabled = false;
    });
    // '이 PC' 카드 아래: 저장 위치 (처음 실행 화면과 같은 줄, modules/appConnection.mjs 의 storageRow).
    const storage = tauri && state.defaults.local ? storageRow() : null;
    const save = button("저장하고 다시 연결", "primary", null, async () => {
      if (pick === "local") {
        // changed() reads false until the lookup ends, so a folder typed meanwhile would be dropped.
        // A failed lookup already shows its error in the storage row.
        if (storage && !(await storage.ready)) return;
        if (state.mode !== "local") {
          if (storage?.changed() && !(await storage.save())) return;
          useLocal();
          return;
        }
        if (!storage?.changed()) { say("이미 이 PC의 Arkism을 쓰고 있어요."); return; }
        if (!confirm(`저장 위치를 바꿀까요?\n\n지금 데이터는 옮기지 않고 ${storage.loc.path}에 그대로 둬요. 새 폴더가 비어 있으면 빈 Arkism으로 시작해요.`)) return;
        const l = await storage.save();
        if (l) say(l.next === l.path ? "저장 위치를 바꿨어요." : "앱을 다시 열면 새 위치에서 시작해요.");
        return;
      }
      save.disabled = true;
      say("연결하는 중…");
      try {
        await sshConnect(form.read());
        say(`${sshLabel(state.ssh)}에 연결했어요.`);
      } catch (e) {
        say(e.message, true);
      } finally {
        save.disabled = false;
      }
    });
    const off = button("연결 끊기", "ghost", null, disconnect);
    const draw = () => {
      cards.replaceChildren(...modes.map(([key, ic, title, text]) => {
        const c = el("button", `mode-card${key === pick ? " active" : ""}`);
        c.type = "button";
        c.setAttribute("aria-pressed", String(key === pick));
        const h = el("span", "mode-card-title");
        h.append(icon(ic), el("span", "", title));
        if (key === pick) h.append(icon("check"));
        c.append(h, el("span", "mode-card-text", text));
        c.addEventListener("click", () => { pick = key; say(""); draw(); });
        return c;
      }));
      // 고른 카드 아래에는 그 모드의 설정만: SSH 폼 또는 저장 위치.
      body.replaceChildren(...(pick === "ssh" ? [form.node, el("p", "pg-hint", "SSH 키 또는 비밀번호로 접속해요. 비밀번호는 이 PC의 키체인에 저장해 다시 묻지 않고, 연결 끊기를 누르면 지금 연결된 서버의 비밀번호를 지워요.")] : storage ? [storage.node] : []));
      // 연결 끊기: only while on a server; next launch goes back to the first-run choice.
      actions.replaceChildren(...(pick === "ssh" ? [test, save] : [save]), ...(pick === "ssh" && state.mode !== "local" ? [off] : []));
    };
    draw();
    g.append(cards, body, out, actions);
    // 모델 중계기 (gateway-token.js): its own group under 연결 방식, so the page needs no right column.
    const gw = window.ArkismGatewayToken?.({ el, button, tauri, notice })?.side((_, ...nodes) => { const d = el("div", "gw-sec"); d.append(...nodes); return d; });
    if (!gw) return [g];
    const relay = group("모델 중계기");
    relay.append(...gw);
    return [g, relay];
  }
  // 설정 › 고급 (Figma 198:6392): 수집·데이터·지우기 on the left; 진단, 설정 옮기기, ComfyUI on the right.
  function tabAdvanced() {
    const comfy = group("ComfyUI", "이미지 프롬프트의 ‘ComfyUI에서 열기’가 여는 주소예요.");
    const cu = el("input");
    cu.type = "url";
    cu.placeholder = "http://127.0.0.1:8188";
    cu.value = prefs.comfyUrl || "";
    cu.addEventListener("change", () => {
      let url = cu.value.trim();
      while (url.endsWith("/")) url = url.slice(0, -1); // strips trailing slashes without a backtracking regex
      prefs.comfyUrl = url;
      savePrefs();
      notice("ComfyUI 주소를 저장했어요.");
    });
    const cf = el("label", "field wide", "주소");
    cf.append(cu);
    comfy.append(cf);
    // 가져오기: 파일을 고르면 내용을 검사하고, 하나라도 틀리면 아무것도 바꾸지 않는다.
    const pickFile = () => new Promise((resolve) => {
      const input = el("input"); input.type = "file"; input.accept = "application/json,.json";
      input.addEventListener("change", () => { const f = input.files?.[0]; if (!f || f.size > 1048576) return resolve(null); f.text().then(resolve, () => resolve(null)); });
      input.click();
    });
    const currentAccount = async () => {
      const owner = JSON.stringify([state.server, state.token, state.scope]);
      const query = new URLSearchParams(Object.entries(state.scope).filter(([, v]) => v !== "")).toString();
      const caps = await request(`/v1/capabilities?${query}`);
      if (owner !== JSON.stringify([state.server, state.token, state.scope])) throw new Error("changed_identity");
      return { ...caps, scope: { ...state.scope } };
    };
    const importError = (e) => ({ other_account: "다른 계정에서 내보낸 파일이에요. 그 계정으로 로그인한 뒤 가져와 주세요.", other_scope: "다른 작업 범위에서 내보낸 파일이에요. 설정 › 고급 › 연결의 범위를 맞춰 주세요." })[e?.message] || "가져오지 못했어요. Arkism에서 내보낸 파일인지 확인해 주세요. 기존 설정은 그대로예요.";
    const transfer = group("PC 설정 옮기기", "화면·정렬·분류 설정을 파일로 옮겨요.");
    transfer.append(button("PC 설정 내보내기", "", null, () => {
      let url;
      try {
        const text = window.ArkismPreferenceTransfer.export((key) => localStorage.getItem(key));
        url = URL.createObjectURL(new Blob([text], { type: "application/json" }));
        const link = el("a"); link.href = url; link.download = "arkism-preferences.json"; link.click();
        notice("설정 파일을 저장했어요. 다른 PC의 설정 › 고급에서 가져오세요.");
      } catch (_) { notice("설정 파일을 만들지 못했어요. 기존 설정은 그대로예요."); }
      finally { if (url) setTimeout(() => URL.revokeObjectURL(url), 1000); }
    }), button("PC 설정 가져오기", "", null, async () => {
      const text = await pickFile();
      if (!text) return;
      try {
        window.ArkismPreferenceTransfer.import(text, (k) => localStorage.getItem(k), (k, v) => localStorage.setItem(k, v));
        notice("설정을 가져왔어요. 화면을 다시 불러와요.");
        setTimeout(() => location.reload(), 600);
      } catch (e) { notice(importError(e)); }
    }));
    const memoryTransfer = group("기억 정리 설정 옮기기", "‘유지’한 기억과 정리 설정을 옮겨요. 같은 계정에서만 가져올 수 있어요.");
    memoryTransfer.append(button("기억 정리 설정 내보내기", "", null, async () => {
      let url;
      const owner = JSON.stringify([state.server, state.token, state.scope]);
      try {
        const query = new URLSearchParams(Object.entries(state.scope).filter(([, v]) => v !== "")).toString();
        const caps = await request(`/v1/capabilities?${query}`);
        if (owner !== JSON.stringify([state.server, state.token, state.scope]) || ["workspace_id", "project_id", "user_id", "agent_id"].some(k => (caps.scope?.[k] || "") !== (state.scope[k] || ""))) throw new Error("changed_identity");
        const text = window.ArkismPreferenceTransfer.exportMemory(k => localStorage.getItem(k), { ...caps, server: state.server });
        url = URL.createObjectURL(new Blob([text], { type: "application/json" }));
        const link = el("a"); link.href = url; link.download = "arkism-memory-preferences.json"; link.click();
        notice("기억 정리 설정을 저장했어요. 같은 계정으로 로그인한 앱의 설정 › 고급에서 가져오세요.");
      } catch (_) { notice("기억 정리 설정을 내보내지 못했어요. 로그인과 연결 상태를 확인해 주세요."); }
      finally { if (url) setTimeout(() => URL.revokeObjectURL(url), 1000); }
    }), button("기억 정리 설정 가져오기", "", null, async () => {
      const text = await pickFile();
      if (!text) return;
      try {
        const n = window.ArkismPreferenceTransfer.importMemory(text, await currentAccount(), (k) => localStorage.getItem(k), (k, v) => localStorage.setItem(k, v));
        notice(`기억 정리 설정을 가져왔어요. 유지하기로 한 기억 ${n}개를 더했어요. 화면을 다시 불러와요.`);
        setTimeout(() => location.reload(), 600);
      } catch (e) { notice(importError(e)); }
    }));
    const skillTransfer = group("스킬 제안 옮기기", "스킬 제안 초안과 숨긴 제안을 옮겨요. 같은 계정에서만 가져올 수 있고, 초안 본문이 들어 있어요.");
    skillTransfer.append(button("스킬 제안 내보내기", "", null, async () => {
      let url;
      const owner = JSON.stringify([state.server, state.token, state.scope]);
      try {
        const query = new URLSearchParams(Object.entries(state.scope).filter(([, v]) => v !== "")).toString();
        const caps = await request(`/v1/capabilities?${query}`);
        if (owner !== JSON.stringify([state.server, state.token, state.scope]) || ["workspace_id", "project_id", "user_id", "agent_id"].some(k => (caps.scope?.[k] || "") !== (state.scope[k] || ""))) throw new Error("changed_identity");
        const text = window.ArkismPreferenceTransfer.exportSkills(k => localStorage.getItem(k), { ...caps, server: state.server });
        url = URL.createObjectURL(new Blob([text], { type: "application/json" }));
        const link = el("a"); link.href = url; link.download = "arkism-skill-preferences.json"; link.click();
        notice("스킬 제안을 저장했어요. 같은 계정으로 로그인한 앱의 설정 › 고급에서 가져오세요.");
      } catch (_) { notice("스킬 제안을 내보내지 못했어요. 로그인과 연결 상태를 확인해 주세요."); }
      finally { if (url) setTimeout(() => URL.revokeObjectURL(url), 1000); }
    }), button("스킬 제안 가져오기", "", null, async () => {
      const text = await pickFile();
      if (!text) return;
      try {
        const n = window.ArkismPreferenceTransfer.importSkills(text, await currentAccount(), (k) => localStorage.getItem(k), (k, v) => localStorage.setItem(k, v));
        notice(`스킬 제안 초안 ${n}개를 가져왔어요. 할 일에서 볼 수 있어요.`);
        setTimeout(() => location.reload(), 600);
      } catch (e) { notice(importError(e)); }
    }));
    // Right column: the same groups, flattened into short sections (the long notes stay as one line under each title).
    toSide(...doctorGroup().map(asSide), sideSec("설정 옮기기", ...[transfer, memoryTransfer, skillTransfer].map((grp) => {
      const box = el("div", "v4-transfer");
      box.append(el("strong", "", grp.querySelector("h2").textContent), ...[...grp.childNodes].filter((n) => n.tagName !== "H2"));
      return box;
    })), asSide(comfy));
    return [...dataGroup(), ctx.privacyGroup(), speedGroup()];
  }
  // 속도 기록 (perf.js): the slowest kinds first; 100ms or more is what a person notices as a stall.
  function speedGroup() {
    const g = group("속도 기록", "화면을 불러오고 그린 시간이에요. 100ms를 넘으면 느리게 느껴져요.");
    const rows = Object.entries(window.ArkismPerf?.summary() || {}).sort((a, b) => b[1].p95 - a[1].p95).slice(0, 15);
    if (!rows.length) g.append(el("p", "pg-hint", "아직 기록이 없어요."));
    for (const [kind, x] of rows) {
      g.append(line(kind, `보통 ${x.p50}ms · 느릴 때 ${x.p95}ms · 가장 느림 ${x.max}ms · ${x.n}번${x.size ? ` · 최대 ${x.size}개` : ""}`, el("span", x.p95 >= 100 ? "badge warn" : "badge", `${x.p95}ms`)));
    }
    if (rows.length) g.append(button("기록 지우기", "", null, () => { window.ArkismPerf.clear(); notice("속도 기록을 지웠어요."); render(false); }));
    return g;
  }

  // 수집: 이 PC의 에이전트 훅이 읽는 ~/.config/arkism/capture.json. 내장 모드·서버 연결 모두 같다.
  function captureGroup() {
    if (!tauri) return [];
    const g = group("수집", "멈추거나 뺀 폴더에서는 대화를 저장하지 않고 기억도 넣지 않아요. 저장소에 .arkism-ignore 파일을 둬도 돼요.");
    const body = el("div");
    g.append(body);
    let current = { paused: false, exclude: [] };
    const save = async (next, done) => {
      try { await tauri.invoke("set_capture_settings", { settings: next }); current = next; draw(); if (done) notice(done); }
      catch (_) { notice("수집 설정을 저장하지 못했어요."); }
    };
    function draw() {
      const rows = [line("수집 켜기", current.paused ? "멈춰 있어요. 다시 켜면 다음 대화부터 저장해요." : "에이전트 대화를 저장하고 기억을 넣어요",
        sw(!current.paused, "수집", (on) => save({ ...current, paused: !on }, on ? "수집을 다시 켰어요." : "수집을 멈췄어요.")))];
      for (const folder of current.exclude) {
        rows.push(line(folder, "이 폴더와 그 안의 폴더", button("빼기 취소", "", null, () => save({ ...current, exclude: current.exclude.filter((f) => f !== folder) }, "폴더를 다시 수집해요."))));
      }
      const input = el("input"); input.placeholder = "C:\\work\\secret-repo"; input.setAttribute("aria-label", "뺄 폴더 경로");
      const add = button("폴더 빼기", "", null, () => {
        const folder = input.value.trim();
        if (!folder) return;
        if (current.exclude.includes(folder)) { notice("이미 뺀 폴더예요."); return; }
        void save({ ...current, exclude: [...current.exclude, folder] }, "이 폴더는 수집하지 않아요.");
      });
      input.addEventListener("keydown", (e) => { if (e.key === "Enter") add.click(); });
      const f = el("label", "field wide", "수집하지 않을 폴더");
      f.append(input);
      const actions = el("div", "pg-actions");
      actions.append(add);
      // 무엇을 기억할지에 대한 내 메모: 추출·반성이 규칙 아래에 덧붙여 읽는다(규칙을 느슨하게 하지는 못함).
      const note = el("textarea");
      note.maxLength = 2000; note.rows = 3; note.value = current.instructions || "";
      note.placeholder = "예: 회사 고객 이름은 기억하지 마. 배포·테스트 규칙은 꼭 기억해.";
      note.setAttribute("aria-label", "기억할 것에 대한 메모");
      note.addEventListener("change", () => save({ ...current, instructions: note.value.trim() }, "메모를 저장했어요. 다음 수집부터 써요."));
      const nf = el("label", "field wide", "무엇을 기억할지 메모");
      nf.append(note, el("small", "", "Arkism이 기억을 고를 때 참고해요. 비밀값·개인정보 규칙보다 앞서지는 않아요."));
      body.replaceChildren(...rows, f, actions, nf);
    }
    tauri.invoke("capture_settings").then((r) => { current = { paused: !!r.paused, exclude: r.exclude || [], instructions: r.instructions || "" }; draw(); }, () => body.replaceChildren(el("p", "pg-hint", "수집 설정을 읽지 못했어요.")));
    return [g];
  }

  // 진단: arkism doctor. 고치기는 에이전트 다시 연결·실패한 수집 재시도만 한다.
  function doctorGroup() {
    if (!tauri) return [];
    const g = group("진단", "연결·에이전트·수집·디스크 상태를 점검해요.");
    const out = el("div", "doctor-list");
    const mark = { ok: "정상", warn: "확인 필요", fail: "문제" };
    const show = (r) => {
      const rows = (r.checks || []).filter((c) => c.status !== "ok").map((c) => line(c.message, c.fix || "", el("span", c.status === "fail" ? "pill warn" : "pill", mark[c.status] || c.status)));
      const sum = r.summary || {};
      out.replaceChildren(el("p", "pg-hint", `정상 ${sum.ok || 0} · 확인 필요 ${sum.warn || 0} · 문제 ${sum.fail || 0}`), ...rows);
      for (const f of r.fixes || []) out.append(el("p", "pg-hint", f.message));
    };
    let busy = false;
    const run = (fix) => async () => {
      if (busy) return;
      busy = true;
      out.replaceChildren(el("p", "pg-hint", fix ? "고치는 중…" : "점검하는 중…"));
      try { show(await tauri.invoke("doctor", { fix })); } catch (_) { out.replaceChildren(el("p", "pg-hint", "점검하지 못했어요.")); }
      busy = false;
    };
    const actions = el("div", "pg-actions");
    actions.append(button("점검", "", null, run(false)), button("고치기", "", null, run(true)));
    g.append(out, actions);
    return [g];
  }

  // 데이터: 이 PC의 Arkism(내장 모드)만. 백업은 문서 › Arkism Backups에 폴더로 남는다.
  function dataGroup() {
    if (state.mode !== "local" || !tauri) return [];
    const g = group("데이터", "이 PC에 저장한 기억·세션·스킬 기록을 백업하고 되돌려요. 백업에는 모든 기억이 들어 있으니 안전한 곳에 두세요.");
    const list = el("div", "backup-list");
    const when = (stamp) => {
      const m = /^(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})(\d{2})$/.exec(stamp || "");
      if (!m) return stamp || "";
      const d = new Date(Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]));
      return new Intl.DateTimeFormat(globalThis.ArkismI18n?.locale() || "ko-KR", { year: "numeric", month: "long", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(d);
    };
    const size = (n) => n >= 1048576 ? `${(n / 1048576).toFixed(1)}MB` : `${Math.max(1, Math.round(n / 1024))}KB`;
    let busy = false;
    const guard = (fn) => async () => {
      if (busy) return;
      busy = true;
      try { await fn(); } finally { busy = false; }
    };
    const errorText = (e) => {
      const code = String(e?.message || e || "");
      if (code.startsWith("local_restore_failed: ")) return `되돌리지 못했어요. 바꾸기 전 데이터는 ${code.split(": ")[1]} 백업에 있어요.`;
      return ({ local_backup_nothing: "아직 백업할 데이터가 없어요.", local_backup_missing: "그 백업을 찾지 못했어요.", local_backup_damaged: "백업 파일이 손상됐어요.",
        local_backup_newer: "더 새 버전의 Arkism에서 만든 백업이에요. 앱을 업데이트한 뒤 되돌려 주세요.", local_busy: "다른 작업이 끝난 뒤 다시 시도해 주세요." })[code] || "작업하지 못했어요. 잠시 뒤 다시 시도해 주세요.";
    };
    async function refresh() {
      let r;
      try { r = await tauri.invoke("local_backups"); } catch (_) { list.replaceChildren(el("p", "pg-hint", "백업 목록을 읽지 못했어요.")); return; }
      if (!r.backups.length) { list.replaceChildren(el("p", "pg-hint", "아직 백업이 없어요.")); return; }
      list.replaceChildren(...r.backups.slice(0, 10).map((b) => line(when(b.created), `${b.auto ? "자동 · " : ""}${size(b.bytes)}${b.app_version ? ` · Arkism ${b.app_version}` : ""}`, button("이 백업으로 되돌리기", "", null, guard(async () => {
        if (!await ask(`${when(b.created)} 백업으로 되돌릴까요?\n\n지금 데이터는 먼저 새 백업으로 남겨 둬요. 그 뒤의 기억·세션 기록은 사라져요.`)) return;
        notice("되돌리는 중이에요. 잠시 기다려 주세요.");
        try {
          const done = await tauri.invoke("local_restore", { name: b.name });
          await startLocal();
          notice(done.safety_copy ? `되돌렸어요. 바꾸기 전 데이터는 ${when(done.safety_copy.replace(/^arkism-/, ""))} 백업에 있어요.` : "되돌렸어요.");
        } catch (e) { notice(errorText(e)); await startLocal(); }
      })))));
      if (r.backups.length > 10) list.append(el("p", "pg-hint", `오래된 백업 ${r.backups.length - 10}개는 백업 폴더에서 볼 수 있어요.`));
    }
    const search = line("검색 방식", "확인하는 중…", el("span"));
    tauri.invoke("search_mode").then((mode) => {
      const on = String(mode).startsWith("ollama");
      search.querySelector("small").textContent = on
        ? "뜻과 키워드로 함께 찾아요."
        : "키워드로 찾아요. Ollama를 설치하고 'ollama pull qwen3-embedding:0.6b'를 실행한 뒤 앱을 다시 열면 의미 검색이 켜져요.";
      search.lastChild.replaceWith(el("span", on ? "pill on" : "pill", on ? "의미 검색" : "키워드 검색"));
    }, () => {});
    g.append(search);
    // 쓰던 에이전트의 기억 파일(CLAUDE.md·Claude 메모리·AGENTS.md·GEMINI.md)을 검토 대기로 가져온다.
    g.append(line("에이전트 기억 가져오기", "CLAUDE.md·AGENTS.md·GEMINI.md의 규칙을 검토 대기로 가져와요",
      button("가져오기", "", null, guard(() => window.ArkismAgentMemoryImport.run({ invoke: (cmd, args) => tauri.invoke(cmd, args), ask, notice })))));
    g.append(line("지금 백업", "문서 › Arkism Backups · 하루 한 번 자동 (최근 7개)", button("지금 백업", "primary", null, guard(async () => {
      notice("백업하는 중이에요.");
      try { const b = await tauri.invoke("local_backup"); notice(`백업했어요 · ${size(b.bytes)}`); await refresh(); } catch (e) { notice(errorText(e)); }
    }))), list, el("div", "pg-actions"));
    g.lastChild.append(button("백업 폴더 열기", "", null, () => tauri.invoke("open_backups").catch(() => notice("폴더를 열지 못했어요."))));
    const danger = group("모든 데이터 지우기", "이 PC의 기억·세션·스킬 기록·설정을 모두 지워요. 백업 폴더와 에이전트 파일은 남아요. 되돌릴 수 없어요.");
    danger.append(line("처음 상태로 되돌리기", "지운 뒤 빈 Arkism으로 다시 시작해요.", button("모든 데이터 지우기", "danger", null, guard(async () => {
      if (!await ask("이 PC의 Arkism 데이터를 모두 지울까요?\n\n백업하지 않은 기억은 되찾을 수 없어요.")) return;
      if (!await ask("정말 지울까요? 이 작업은 되돌릴 수 없어요.")) return;
      try { await tauri.invoke("local_erase"); notice("모두 지웠어요. 빈 Arkism으로 다시 시작해요."); } catch (e) { notice(errorText(e)); }
      await startLocal();
    }))));
    void refresh();
    return [g, danger];
  }

  // Figma 07 · 설정 / 03 · 모델: the area tabs pick the page; each page fills the main column, the right column and the area-bar buttons.
  let shownTab = null;
  function renderSettings() {
    if (!SETTINGS_TABS[state.settingsTab]) state.settingsTab = "general";
    const tab = state.settingsTab;
    if (shownTab !== tab) { draft = null; open = null; kitCard = null; shownTab = tab; }
    $("#controls").replaceChildren();
    sideParts = []; headParts = [];
    const kit = { side: toSide, head: toHead, sideSec, kvRow, sideNote };
    const body = { general: tabGeneral, connect: tabConnect, messenger: () => messenger.tab(kit), appearance: tabAppearance, agents: tabAgents, models: () => models.tab(kit), routing: () => routing.tab(kit),
      workflow: () => crew.settingsTab({ group }), categories: tabCategories, advanced: tabAdvanced, info: tabInfo }[tab]();
    if (tab === "workflow") toSide(sideNote("목록을 누르면 펼쳐서 고쳐요. 노드에서 쓰는 페르소나는 지울 수 없어요."));
    const content = el("div", `pg-content v4-set v4-set-${tab}`);
    content.append(...body);
    $("#table").replaceChildren(content);
    const side = el("div", "detail-body v4-side");
    side.append(...sideParts);
    $("#detail").replaceChildren(side);
    $("#head-actions").replaceChildren(...headParts);
  }

  // ---------------------------------------------------------------- Arkism 디자인 시스템 (reads the live tokens, so it never drifts from tokens.css)
  const DESIGN_TABS = [["color", "색"], ["type", "글자"], ["space", "간격·모서리"], ["parts", "부품"]];
  function rootTokens() {
    const out = new Map();
    for (const sheet of document.styleSheets) {
      let rules;
      try { rules = sheet.cssRules; } catch (_) { continue; }
      for (const r of rules) {
        if (r.selectorText !== ":root" || !r.style) continue;
        for (const name of r.style) if (name.startsWith("--")) out.set(name, r.style.getPropertyValue(name).trim());
      }
    }
    return out;
  }
  const computed = (name) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  function swatch(name) {
    const card = el("div", "sw");
    const chip = el("i");
    chip.style.background = `var(${name})`;
    card.append(chip, el("code", "", name.slice(2)), el("small", "", computed(name)));
    card.title = `${name}: ${computed(name)}`;
    return card;
  }
  function designColor(tokens) {
    const semantic = /^--(bg|text|border|accent|warn|status|logo|paper|focus|avatar)/;
    const sem = group("화면에서 쓰는 색", "화면은 이 이름만 써요. 라이트·다크가 자동으로 바뀌어요.");
    const grid = el("div", "sw-grid");
    for (const [n] of tokens) if (semantic.test(n)) grid.append(swatch(n));
    sem.append(grid);
    const prim = group("원색 단계", "직접 쓰지 말고 위의 이름으로 가져다 써요. neutral은 클수록 어두워요.");
    for (const fam of ["neutral", "amber", "green", "red", "blue", "purple", "teal", "brand"]) {
      const names = [...tokens.keys()].filter((n) => n.startsWith(`--${fam}-`));
      if (!names.length) continue;
      const row = el("div", "ramp-row");
      row.append(el("span", "ramp-name", fam));
      const ramp = el("div", "ramp");
      for (const n of names) {
        const c = el("div", "ramp-cell");
        const chip = el("i"); chip.style.background = `var(${n})`;
        c.append(chip, el("small", "", n.slice(fam.length + 3)));
        c.title = `${n}: ${computed(n)}`;
        ramp.append(c);
      }
      row.append(ramp);
      prim.append(row);
    }
    return [sem, prim];
  }
  function designType(tokens) {
    const g = group("글자 스타일", "font: var(--이름) 으로 쓰고, heading은 자간 -0.01em을 더해요.");
    const sample = { heading: "기억은 모든 에이전트가 함께 써요", label: "스킬 적용 · 업데이트 확인", body: "에이전트가 무엇을 기억하는지 직접 봐요.", caption: "2시간 전 · 마지막 저장", mono: "claude_code · ~/.claude/skills" };
    const list = el("div", "pg-card");
    for (const [n, v] of tokens) {
      if (!n.startsWith("--type-") || n.endsWith("tracking")) continue;
      const kind = n.slice(7).split("-")[0];
      const r = el("div", "type-row");
      const s = el("div", "type-sample", sample[kind] || "Arkism");
      s.style.font = `var(${n})`;
      r.append(el("code", "", n.slice(2)), s, el("small", "", v.replace(/var\(--font-family-(\w+)\)/, "$1")));
      list.append(r);
    }
    g.append(list);
    const fam = group("글꼴");
    for (const n of ["--font-family-sans", "--font-family-mono", "--font-family-latin"]) {
      const r = el("div", "type-row");
      const s = el("div", "type-sample", n.includes("mono") ? "Aa 0123 mcp.github" : "가나다 Aa 0123 Arkism");
      s.style.fontFamily = `var(${n})`;
      r.append(el("code", "", n.slice(2)), s, el("small", "", computed(n).split(",")[0].replaceAll('"', "")));
      fam.append(r);
    }
    return [g, fam];
  }
  function designSpace(tokens) {
    const sp = group("간격", "4의 배수를 기본으로 해요.");
    for (const [n] of tokens) {
      if (!n.startsWith("--space-")) continue;
      const r = el("div", "bar-row");
      const bar = el("i"); bar.style.width = `var(${n})`;
      r.append(el("code", "", n.slice(2)), bar, el("small", "", computed(n)));
      sp.append(r);
    }
    const rd = group("모서리");
    const rg = el("div", "radius-grid");
    for (const [n] of tokens) {
      if (!n.startsWith("--radius-")) continue;
      const c = el("div", "radius-cell");
      const box = el("i"); box.style.borderRadius = `var(${n})`;
      c.append(box, el("code", "", n.slice(9)), el("small", "", computed(n)));
      rg.append(c);
    }
    rd.append(rg);
    const sh = group("그림자");
    const sg = el("div", "radius-grid");
    for (const [n] of tokens) {
      if (!n.startsWith("--shadow-")) continue;
      const c = el("div", "radius-cell");
      const box = el("i"); box.style.boxShadow = `var(${n})`;
      c.append(box, el("code", "", n.slice(9)));
      sg.append(c);
    }
    sh.append(sg);
    return [sp, rd, sh];
  }
  function designParts() {
    const out = [];
    const btns = group("버튼");
    const r1 = el("div", "demo-row");
    const dis = button("비활성", "", null); dis.disabled = true;
    r1.append(button("기본", "primary", null), button("보조", "", null), button("아이콘", "", "plus"), button("조용한", "ghost", null), dis);
    btns.append(r1);
    const sel = group("선택·상태");
    const r2 = el("div", "demo-row");
    r2.append(segment([["a", "전체", 24], ["b", "빠진 곳 있음", 3, true], ["c", "어댑터 필요", 1]], "a", () => {}),
      el("span", "chip active", "활성 칩"), el("span", "chip", "기본 칩"), pill("시작마다", true), pill("질문별"),
      el("span", "tag", "스킬 8개"), el("span", "tag warn", "어댑터 필요 2"));
    const r3 = el("div", "demo-row");
    let on = true;
    const toggle = () => { on = !on; s2.replaceWith(s2 = sw(on, "예시 스위치", () => toggle())); };
    let s2 = sw(on, "예시 스위치", () => toggle());
    r3.append(s2, el("span", "pg-hint", "스위치 — 눌러 보세요"));
    sel.append(r2, r3);
    const agents = group("에이전트 로고");
    const r4 = el("div", "demo-row");
    for (const [, n, logo] of AGENTS) r4.append(agentLogo(logo, n));
    agents.append(r4);
    const form = group("입력·설정 줄");
    const f = el("label", "field", "서버 주소"); const i = el("input"); i.value = "http://127.0.0.1:8091"; f.append(i);
    const card = el("div", "pg-card");
    card.append(line("선호", "보고 방식·말투 같은 내 취향", pill("시작마다", true)), line("최대 개수", "관련 있는 것이 적으면 더 적게", el("output", "", "6")));
    form.append(f, card);
    const stat = group("숫자 카드");
    const sg = el("div", "stats");
    sg.append(ctx.stat("쓰이는 기억", "94", "개"), ctx.stat("검토 대기", "3", "개", true), ctx.stat("반영률", "14", "%"));
    stat.append(sg);
    out.push(btns, sel, agents, form, stat);
    return out;
  }
  function renderDesign() {
    const back = el("button", "page-back");
    back.type = "button";
    back.append(icon("arrow-left"), "디자인 시스템");
    back.addEventListener("click", () => go("design"));
    $(".main > .page-head").prepend(back);
    $("#controls").replaceChildren(segment(DESIGN_TABS.map(([k, l]) => [k, l]), state.designTab, (k) => { state.designTab = k; render(false); }));
    const tokens = rootTokens();
    const parts = { color: () => designColor(tokens), type: () => designType(tokens), space: () => designSpace(tokens), parts: designParts }[state.designTab]();
    const page = el("div", "design-page");
    if (state.designTab === "color") {
      // The colours every screen starts from, then the type at a glance; the full lists follow.
      const keyColors = [["bg-window", "bg/window"], ["bg-sidebar", "bg/sidebar"], ["bg-muted", "bg/muted"], ["accent", "accent"], ["text-1", "text/primary"], ["border", "border"]];
      const main = el("section", "tk-card");
      const h = el("h2", "", "화면 색");
      h.append(el("small", "", `${keyColors.length}개`));
      main.append(h);
      for (const [n, label] of keyColors) {
        const r = el("div", "tk-color");
        const chip = el("i"); chip.style.background = `var(--${n})`;
        r.append(chip, el("code", "", label), el("small", "", computed(`--${n}`)));
        r.title = `--${n}`;
        main.append(r);
      }
      const type = el("section", "tk-card tk-type");
      type.append(el("h2", "", "글자"), el("p", "tk-h", "화면 제목"), el("p", "tk-body", `본문 · ${computed("--font-family-sans").split(",")[0].replaceAll('"', "")}`), el("p", "tk-mono", `mono / ${computed("--font-family-mono").split(",")[0].replaceAll('"', "")}`));
      page.append(main, type);
    }
    page.append(...parts);
    $("#table").replaceChildren(page);
    // Right column: spacing at a glance and a few live parts.
    const side = el("div", "detail-body tk-side");
    const spaceTable = el("div", "tk-table");
    const nums = (prefix) => [...tokens.keys()].filter((n) => n.startsWith(prefix)).map((n) => computed(n).replace("px", "")).filter((v) => v && v !== "0" && Number(v) % 4 === 0);
    const head = el("div", "tk-row head");
    head.append(el("span", "", "토큰"), el("span", "", "값"));
    spaceTable.append(head);
    for (const [k, v] of [["space", nums("--space-").join(" · ")], ["radius", nums("--radius-").filter((x) => Number(x) < 100).join(" · ")], ["shadow", [...tokens.keys()].filter((n) => n.startsWith("--shadow-")).map((n) => n.slice(9)).join(" · ")]]) {
      const r = el("div", "tk-row");
      r.append(el("code", "", k), el("span", "", v));
      spaceTable.append(r);
    }
    const sp = el("section", "section");
    sp.append(el("h3", "", "간격·모서리"), spaceTable);
    const demo = el("section", "section");
    const btns = el("div", "actions");
    btns.append(button("기본 버튼", "primary small", null), button("보조 버튼", "small", null));
    let on = true;
    let toggle = sw(on, "스위치 견본", function flip() { on = !on; const next = sw(on, "스위치 견본", flip); toggle.replaceWith(next); toggle = next; });
    const swRow = el("div", "tk-switch");
    swRow.append(el("span", "", "스위치"), toggle);
    const input = el("label", "field", "입력");
    const i = el("input"); i.placeholder = "예시"; input.append(i);
    demo.append(el("h3", "", "부품 견본"), btns, segment([["a", "선택됨"], ["b", "기본"]], "a", () => {}), swRow, input, el("p", "tk-note", "부품 견본은 미리보기예요. 실제 화면과 조금 다를 수 있어요."));
    side.append(sp, demo);
    $("#detail").replaceChildren(side);
  }
  // 테마는 설정 › 모양과 같은 값(look.js)을 쓴다. 이 버튼은 지금 보이는 테마의 반대로 고정한다.
  function themeButton() {
    const cur = document.documentElement.dataset.theme || (matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark");
    return button(cur === "light" ? "다크로 보기" : "라이트로 보기", "", null, () => {
      window.ArkismLook.set({ theme: cur === "light" ? "dark" : "light" });
      render(false);
    });
  }
  // 예전 앱이 따로 저장하던 테마(arkism.theme)는 모양 설정으로 한 번 옮기고 지운다.
  const saved = stored("arkism.theme");
  if (saved === "light" || saved === "dark") { window.ArkismLook.set({ theme: saved }); store("arkism.theme", null); }

  return {
    prefs, savePrefs, shownAgents, categoryOf, checkUpdate, logoOf, agentWanted, agentPaths: AGENT_PATHS,
    views: {
      settings: { title: () => SETTINGS_TABS[state.settingsTab]?.[0] || "설정", sub: () => SETTINGS_TABS[state.settingsTab]?.[1] || "", head: () => [], render: renderSettings, keepDetail: true },
      tokens: { title: "Arkism 토큰", sub: () => "이 앱 화면에 쓰는 색·글자·간격 값이에요. 부품 견본으로 실제 모양을 미리 볼 수 있어요.", head: () => [themeButton(), button("라이브러리로", "", null, () => go("design"))], render: renderDesign, keepDetail: true }
    }
  };
};
