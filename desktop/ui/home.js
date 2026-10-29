// 홈 영역의 공통 부품 (Figma v4 · 01 홈, 08 공통, 09 시작):
//  - 전체 검색 (#search, 198:3468): 기억·스킬·워크플로우·프롬프트를 한 번에. 기억은 서버 검색(POST /v1/search)도 같이 쓴다.
//  - 단축키 창 (198:6478): ? 키.
//  - 서버 연결 끊김 카드 (198:3296), SSH 오류 안내 (198:3382).
//  - 시작 2·3단계 (198:6650 에이전트 선택, 198:6736 연결 확인): 연결 화면에서 직접 연결한 뒤에만.
//  - 메뉴별 확인할 것의 워크플로우·라우팅 줄 (198:200).
// 서버나 앱에 새 기능을 더하지 않는다. 이미 있는 요청과 화면으로만 만든다.
window.ArkismHome = function (ctx) {
  "use strict";
  const { state, $, el, button, go, render, request, query, notice, agentLogo, AGENTS } = ctx;

  // ------------------------------------------------------------ 전체 검색
  const MAX_EACH = 4;
  let box = null, hits = [], active = 0, serverHits = [], serverQ = "", timer = 0;
  const searches = globalThis.ArkismKeep.latest(); // a slower search for an older query never replaces a newer one
  const has = (text, q) => String(text || "").toLowerCase().includes(q);
  function popover() {
    if (box) return box;
    box = el("div", "gsearch");
    box.id = "gsearch";
    box.hidden = true;
    box.setAttribute("role", "listbox");
    box.setAttribute("aria-label", "전체 검색 결과");
    $(".area-bar .search").append(box);
    // A click inside keeps the input's focus decisions to the row handlers.
    box.addEventListener("mousedown", (e) => e.preventDefault());
    return box;
  }
  // Results in Figma order: 기억 · 스킬 · 워크플로우 · 프롬프트, each row = title + where it lives + one badge.
  function collect(q, each = MAX_EACH) {
    const d = state.data;
    const out = [];
    const mems = [];
    const seen = new Set();
    for (const h of serverHits) if ((!h.collection || h.collection === "memories") && h.text && !seen.has(h.id)) { seen.add(h.id); mems.push(h); }
    for (const m of d.memories) if (!seen.has(m.id) && has(m.text, q)) { seen.add(m.id); mems.push(m); }
    mems.slice(0, each).forEach((m) => out.push({ kind: "memory", id: m.id, title: m.text, where: "MEMORY › 기억", badge: "기억", item: m }));
    d.skills.filter((s) => has(s.name, q) || has(s.description, q)).slice(0, each)
      .forEach((s) => out.push({ kind: "skill", id: s.id, title: s.name, mono: true, where: `스킬${ctx.skillCategory(s) ? ` › ${ctx.skillCategory(s)}` : ""}`, badge: "스킬" }));
    // 크루: 전문가 · 팀 · 자동화 (personas and templates are not things you open from search).
    const CREW_WHERE = { agent: "전문가", team: "팀", workflow: "자동화" };
    (d.crew || []).filter((r) => CREW_WHERE[r.crew?.type] && (has(r.crew.name, q) || has(r.crew.handle, q) || has(r.crew.role, q))).slice(0, each)
      .forEach((r) => out.push({ kind: "crew", type: r.crew.type, id: r.id, title: r.crew.type === "workflow" ? `$${r.crew.handle || r.crew.name}` : r.crew.name || r.crew.handle, where: `FLOW › ${CREW_WHERE[r.crew.type]}`, badge: CREW_WHERE[r.crew.type] }));
    const tools = d.tools || {};
    (tools.servers || []).filter((x) => has(x.name, q) || has(x.desc, q)).slice(0, each)
      .forEach((x) => out.push({ kind: "mcp", id: x.name, title: x.name, mono: true, where: "TOOLS › MCP", badge: "MCP" }));
    (tools.plugins || []).filter((x) => has(x.name, q) || has(x.desc, q)).slice(0, each)
      .forEach((x) => out.push({ kind: "plugin", id: x.name, title: x.name, mono: true, where: "TOOLS › 플러그인", badge: "플러그인" }));
    ctx.settingsTabs().filter(([, name]) => has(name, q) || has("설정", q)).slice(0, each)
      .forEach(([key, name]) => out.push({ kind: "setting", id: key, title: name, where: "설정", badge: "설정" }));
    // GALLERY 보류 중: 숨긴 메뉴로 가는 검색 결과는 내지 않는다.
    if (!document.querySelector('.nav-item[data-menu="gallery"]')?.hidden) (d.prompts || []).filter((p) => has(p.title, q) || has(p.prompt, q)).slice(0, each)
      .forEach((p) => out.push({ kind: "prompt", id: p.id, title: p.title || "프롬프트", where: `GALLERY › ${p.kind === "slide" ? "슬라이드" : "프롬프트"}`, badge: "프롬프트", item: p }));
    return out;
  }
  // Server search: exact + full text + meaning, like the agents' own recall; an embedder outage keeps the lexical part.
  function askServer(q) {
    clearTimeout(timer);
    if (q.length < 2) { serverHits = []; serverQ = q; return; }
    timer = setTimeout(async () => {
      const t = searches.begin();
      const body = { scope: state.scope, query: q, modes: ["exact", "fts", "vector"], purpose: "answer", destination: "internal", limit: MAX_EACH };
      let items;
      try { items = (await request("/v1/search", { method: "POST", body: JSON.stringify(body), signal: t.signal }))?.items || []; } catch (_) {
        try { items = (await request("/v1/search", { method: "POST", body: JSON.stringify({ ...body, modes: ["exact", "fts"] }), signal: t.signal }))?.items || []; } catch (_) { items = []; }
      }
      if (!t.live()) return;
      serverHits = items; serverQ = q;
      if (!box?.hidden) paint();
      if (pal?.open) paintPal();
    }, 250);
  }
  const query$ = () => $("#search").value.trim().toLowerCase();
  function paint() {
    const q = query$();
    const b = popover();
    if (!q) { b.hidden = true; return; }
    hits = collect(q);
    active = Math.min(active, Math.max(0, hits.length - 1));
    const head = el("div", "gs-head");
    head.append(el("strong", "", "전체 검색"), el("span", "", "Ctrl / ⌘ K"));
    const parts = [head];
    if (!hits.length) {
      parts.push(window.ArkismState.of("noResults", { clear }, serverQ !== q && q.length >= 2 ? { text: "서버에서도 찾는 중이에요…" } : {}));
    } else {
      const list = el("div", "gs-list");
      hits.forEach((h, i) => {
        const r = el("button", `gs-row${i === active ? " active" : ""}`);
        r.type = "button";
        r.setAttribute("role", "option");
        r.setAttribute("aria-selected", String(i === active));
        const cell = el("span", "gs-cell");
        const title = el("span", `gs-title${h.mono ? " mono" : ""}`, h.title);
        title.setAttribute("data-no-i18n", "");
        cell.append(title, el("small", "", h.where));
        r.append(cell, el("output", "gs-badge", h.badge));
        r.addEventListener("mouseenter", () => { active = i; mark(); });
        r.addEventListener("click", () => openHit(h));
        list.append(r);
      });
      parts.push(list);
    }
    const foot = el("div", "gs-foot");
    const more = el("button", "link-btn", "기억 목록에서 걸러 보기 ›");
    more.type = "button";
    more.addEventListener("click", () => { const v = $("#search").value; hide(); state.query = v.trim().toLowerCase(); go("memories", "active"); $("#search").value = v; state.query = v.trim().toLowerCase(); render(false); });
    foot.append(el("span", "", "↑↓로 고르고 Enter로 열어요. Esc로 닫아요. ? 단축키"), more);
    parts.push(foot);
    b.replaceChildren(...parts);
    b.hidden = false;
  }
  function mark() {
    box?.querySelectorAll(".gs-row").forEach((r, i) => { r.classList.toggle("active", i === active); r.setAttribute("aria-selected", String(i === active)); });
    box?.querySelectorAll(".gs-row")[active]?.scrollIntoView({ block: "nearest" });
  }
  function hide() { if (box) box.hidden = true; }

  // ------------------------------------------------------------ 통합 검색 창 (설정 줄의 검색 아이콘 · Ctrl K)
  // One dialog over every screen: type tabs, results grouped by kind, recent memories and quick actions while empty.
  const TABS = [["all", "전체"], ["memory", "기억"], ["skill", "스킬"], ["crew", "FLOW"], ["mcp", "MCP"], ["plugin", "플러그인"], ["setting", "설정"]];
  const KIND_ICON = { memory: "brain", skill: "puzzle-piece", crew: "users-three", prompt: "image", mcp: "plug", plugin: "stack", setting: "gear-six", action: "arrow-right" };
  let pal = null, palTab = "all", palHits = [], palActive = 0;
  const palQ = () => pal.querySelector(".pal-input").value.trim().toLowerCase();
  function palRow(h, i) {
    const r = el("button", `pal-row${i === palActive ? " active" : ""}`);
    r.type = "button";
    r.setAttribute("role", "option");
    const title = el("span", `pal-title${h.mono ? " mono" : ""}`, h.title);
    if (h.kind !== "action") title.setAttribute("data-no-i18n", "");
    r.append(ctx.icon(KIND_ICON[h.kind] || "file-text"), title, el("small", "", h.where || ""));
    r.addEventListener("mouseenter", () => { palActive = i; palMark(); });
    r.addEventListener("click", () => palOpen(h));
    return r;
  }
  function paintPal() {
    const q = palQ();
    pal.querySelectorAll(".pal-tab").forEach((t) => t.setAttribute("aria-selected", String(t.dataset.tab === palTab)));
    const groups = [];
    if (!q) {
      const recent = [...state.data.memories].sort((a, b) => String(b.updated_at || b.created_at || "").localeCompare(String(a.updated_at || a.created_at || ""))).slice(0, 5)
        .map((m) => ({ kind: "memory", id: m.id, title: m.text, where: "기억", item: m }));
      if (palTab === "all" || palTab === "memory") groups.push(["최근 기억", recent]);
      if (palTab === "all") groups.push(["작업", [
        { kind: "action", title: "기억 후보 검토", run: () => { state.menu = "memory"; go("memories", "candidate"); } },
        { kind: "action", title: "연결 상태 보기", run: () => { const t = $("#status-toggle"); if ($("#status-card").hidden) t.click(); } },
        { kind: "action", title: "설정 열기", run: () => ctx.openSettings() }]]);
    } else {
      const hits = collect(q, palTab === "all" ? MAX_EACH : 20).filter((h) => palTab === "all" || h.kind === palTab);
      for (const [key, name] of TABS.slice(1)) { const g = hits.filter((h) => h.kind === key); if (g.length) groups.push([name, g]); }
    }
    palHits = groups.flatMap(([, g]) => g);
    palActive = Math.min(palActive, Math.max(0, palHits.length - 1));
    const body = pal.querySelector(".pal-body");
    let i = 0;
    const parts = groups.filter(([, g]) => g.length).map(([name, g]) => {
      const sec = el("section", "pal-group");
      sec.append(el("h3", "", name), ...g.map((h) => palRow(h, i++)));
      return sec;
    });
    if (!parts.length) parts.push(el("p", "pal-empty", q.length >= 2 && serverQ !== q ? "서버에서도 찾는 중이에요…" : "찾는 결과가 없어요."));
    body.replaceChildren(...parts);
  }
  function palMark() {
    pal.querySelectorAll(".pal-row").forEach((r, i) => r.classList.toggle("active", i === palActive));
    pal.querySelectorAll(".pal-row")[palActive]?.scrollIntoView({ block: "nearest" });
  }
  function palOpen(h) {
    pal.close();
    if (h.kind === "action") return h.run();
    void openHit(h);
  }
  function palette() {
    if (!pal) {
      pal = el("dialog", "palette");
      pal.setAttribute("aria-label", "통합 검색");
      const head = el("div", "pal-head");
      const input = el("input", "pal-input");
      input.type = "search"; input.placeholder = "검색"; input.setAttribute("aria-label", "검색");
      const x = el("button", "pal-close"); x.type = "button"; x.setAttribute("aria-label", "닫기"); x.append(ctx.icon("x"));
      x.addEventListener("click", () => pal.close());
      head.append(input, x);
      const tabs = el("div", "pal-tabs"); tabs.setAttribute("role", "tablist");
      for (const [key, name] of TABS) {
        const t = el("button", "pal-tab", name); t.type = "button"; t.dataset.tab = key; t.setAttribute("role", "tab");
        t.addEventListener("click", () => { palTab = key; palActive = 0; paintPal(); input.focus(); });
        tabs.append(t);
      }
      const foot = el("div", "pal-foot");
      for (const [what, keys] of [["닫기", ["Esc"]], ["유형 변경", ["Tab"]], ["고르기", ["↑", "↓"]], ["열기", ["Enter"]]]) {
        const s = el("span"); s.append(el("span", "", what), ...keys.map((k) => el("kbd", "", k))); foot.append(s);
      }
      pal.append(head, tabs, el("div", "pal-body"), foot);
      input.addEventListener("input", () => { palActive = 0; askServer(palQ()); paintPal(); });
      input.addEventListener("keydown", (e) => {
        if (e.isComposing) return;
        if (e.key === "ArrowDown" || e.key === "ArrowUp") { e.preventDefault(); if (palHits.length) { palActive = (palActive + (e.key === "ArrowDown" ? 1 : palHits.length - 1)) % palHits.length; palMark(); } }
        else if ((e.key === "ArrowLeft" || e.key === "ArrowRight") && !input.value) { e.preventDefault(); const i = TABS.findIndex(([k]) => k === palTab); palTab = TABS[(i + (e.key === "ArrowRight" ? 1 : TABS.length - 1)) % TABS.length][0]; palActive = 0; paintPal(); }
        else if (e.key === "Tab") { e.preventDefault(); const i = TABS.findIndex(([k]) => k === palTab); palTab = TABS[(i + (e.shiftKey ? TABS.length - 1 : 1)) % TABS.length][0]; palActive = 0; paintPal(); }
        else if (e.key === "Enter") { e.preventDefault(); if (palHits[palActive]) palOpen(palHits[palActive]); }
      });
      pal.addEventListener("click", (e) => { if (e.target === pal) pal.close(); });
      document.body.append(pal);
    }
    if (!pal.open) { pal.querySelector(".pal-input").value = ""; palTab = "all"; palActive = 0; serverHits = []; paintPal(); pal.showModal(); }
    pal.querySelector(".pal-input").focus();
  }
  function clear() { $("#search").value = ""; state.query = ""; serverHits = []; hide(); $("#search").focus(); }
  async function openHit(h) {
    hide();
    $("#search").value = ""; state.query = "";
    $("#search").blur();
    if (h.kind === "memory") {
      let m = state.data.memories.find((x) => x.id === h.id);
      if (!m) m = await request(`/v1/memories/${encodeURIComponent(h.id)}?${query()}`).catch(() => null);
      if (!m) { notice("이 기억을 열지 못했어요. 지워졌거나 권한이 없어요."); return; }
      ctx.openRecord(m);
      return;
    }
    if (h.kind === "setting") return ctx.openSettings(h.id);
    if (h.kind === "mcp" || h.kind === "plugin") { state.toolTab = h.kind; go("tools"); state.selected = `tool:${h.id}`; ctx.markSelected(); ctx.renderDetail(); ctx.openDetail(); return; }
    if (h.kind === "skill") go("skills");
    if (h.kind === "crew" && h.type === "agent") { window.dispatchEvent(new CustomEvent("arkism:open-expert", { detail: { id: h.id } })); return; }
    if (h.kind === "crew" && h.type === "team") { window.dispatchEvent(new CustomEvent("arkism:open-team", { detail: { id: h.id } })); return; }
    if (h.kind === "crew") go("crew");
    if (h.kind === "prompt") go("images", h.item.kind === "slide" ? "slide" : "image");
    state.selected = `${h.kind}:${h.id}`;
    ctx.markSelected(); ctx.renderDetail(); ctx.openDetail();
  }
  // Global search runs where the box does not filter a list of its own (홈, 설정, 사용량 …). Elsewhere it keeps filtering that list.
  const globalHere = () => state.view === "todo" || !!ctx.isPage(state.view);
  function onInput() {
    if (!globalHere()) { hide(); return false; }
    state.query = "";
    active = 0;
    askServer(query$());
    paint();
    return true;
  }
  function onKey(e) {
    if (!box || box.hidden) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!hits.length) return;
      active = (active + (e.key === "ArrowDown" ? 1 : hits.length - 1)) % hits.length;
      mark();
    } else if (e.key === "Enter" && !e.isComposing) {
      e.preventDefault();
      if (hits[active]) void openHit(hits[active]);
    } else if (e.key === "Escape") {
      e.preventDefault(); e.stopPropagation();
      hide(); $("#search").blur();
    }
  }
  function wireSearch() {
    const input = $("#search");
    input.addEventListener("keydown", onKey);
    input.addEventListener("focus", () => { if (globalHere() && query$()) paint(); });
    input.addEventListener("blur", () => setTimeout(hide, 120));
  }

  // ------------------------------------------------------------ 단축키 (198:6478)
  const KEYS = [[["Ctrl / ⌘ K", "/"], "검색"], [["Esc"], "상세 패널 닫기"], [["j", "k"], "다음 / 이전 줄"], [["Enter"], "대화상자 기본 동작"],
    [["Ctrl +", "Ctrl −", "Ctrl 0"], "확대 / 축소 / 기본값"], [["Alt Shift O"], "창을 앱 밖 새 창으로"], [["?"], "이 단축키 보기"]];
  function shortcuts() {
    let d = $("#keys-dialog");
    if (!d) {
      d = el("dialog", "keys-dialog");
      d.id = "keys-dialog";
      const form = el("form");
      form.method = "dialog";
      const head = el("div", "keys-head");
      head.append(el("h2", "", "단축키"), el("p", "", "어느 화면에서나 같은 키로 움직여요."));
      const table = el("div", "keys-table");
      const th = el("div", "keys-row th");
      th.append(el("span", "", "키"), el("span", "", "동작"));
      table.append(th);
      for (const [keys, what] of KEYS) {
        const r = el("div", "keys-row");
        const k = el("span", "keys-keys");
        keys.forEach((key, i) => { if (i) k.append(el("i", "", i === 1 && keys[0].startsWith("Ctrl / ") ? "또는" : "/")); k.append(el("kbd", "", key)); });
        r.append(k, el("span", "", what));
        table.append(r);
      }
      const note = el("p", "keys-note", "한글을 조합하는 동안 Enter는 실행하지 않아요.");
      const act = el("div", "actions");
      const close = button("닫기", "", null);
      close.type = "submit"; close.value = "cancel";
      act.append(close);
      form.append(head, table, note, act);
      d.append(form);
      document.body.append(d);
    }
    if (!d.open) d.showModal();
  }

  // ------------------------------------------------------------ 연결 끊김 (198:3296)
  const REASONS = ["PC가 잠자기에서 깨어났어요", "네트워크(와이파이·VPN)가 바뀌었어요", "서버가 재시작 중이에요"];
  function lostCard() {
    if (!state.loadError) return null;
    const card = el("div", "home-lost");
    const where = state.mode === "remote" ? `${ctx.sshLabel(state.ssh) || "서버"}에 SSH로` : state.mode === "local" ? "이 PC의 Arkism에" : "서버에";
    card.append(el("p", "", state.loadError.reached ? state.loadError.text : `${where} 연결하지 못했어요.`));
    const row = el("div", "actions");
    row.append(button("지금 다시 연결", "primary small", null, (e) => ctx.reconnect(e.currentTarget)));
    row.append(button("연결 설정", "small", null, () => ctx.openSettings("connect")));
    if (state.mode === "remote" && state.defaults.local) {
      const local = el("button", "link-btn", "이 PC에서 쓰기"); local.type = "button"; local.addEventListener("click", ctx.useLocal); row.append(local);
    }
    card.append(row);
    return card;
  }
  function lostSide() {
    const s = el("section", "section home-reasons");
    s.append(el("h3", "", "자주 생기는 이유"));
    const ul = el("ul", "");
    REASONS.forEach((r) => ul.append(el("li", "", r)));
    const all = el("button", "link-btn accent", "연결 방법 다시 보기 ›");
    all.type = "button";
    all.addEventListener("click", () => ctx.openSettings("connect"));
    s.append(ul, all);
    return s;
  }

  // ------------------------------------------------------------ SSH 오류 안내 (198:3382)
  const SSH_KINDS = [["ssh_auth_failed", "SSH 로그인이 거부됐어요"], ["ssh_password_rejected", "비밀번호 로그인이 거부됐어요"], ["ssh_unreachable", "서버에 닿지 못했어요"], ["ssh_missing", "ssh 프로그램이 없어요"],
    ["ssh_host_key_changed", "서버의 SSH 키가 바뀌었어요"], ["arkism_not_found", "서버에 Arkism이 없어요"], ["arkism_too_old", "서버의 Arkism이 오래됐어요"], ["ssh_address_invalid", "주소 형식이 틀렸어요"]];
  function sshCommand(f) {
    if (!f?.host) return "";
    const user = f.user && !f.host.includes("@") ? `${f.user}@` : "";
    return ["ssh", f.port ? `-p ${f.port}` : "", f.key && f.auth !== "password" ? `-i ${f.key}` : "", `${user}${f.host}`].filter(Boolean).join(" ");
  }
  // Shows what went wrong in the right column of the connect screen; null puts the usual "이렇게 연결돼요" back.
  function sshError(code, text, form) {
    const side = $("#ssh-hint");
    let pane = $("#ssh-error-side");
    if (!code) { if (pane) pane.hidden = true; [...side.children].forEach((n) => { if (n !== pane) n.hidden = false; }); return; }
    if (!pane) { pane = el("div", "ssh-error-side"); pane.id = "ssh-error-side"; side.append(pane); }
    [...side.children].forEach((n) => { n.hidden = n !== pane; });
    pane.hidden = false;
    const known = SSH_KINDS.find(([k]) => code === k || code.startsWith(`${k}:`));
    const parts = [el("small", "ssh-err-kicker", "지금 오류"), el("h2", "", known ? known[1] : "연결하지 못했어요"), el("p", "", text)];
    const cmd = sshCommand(form);
    if (cmd && code !== "ssh_missing" && code !== "ssh_address_invalid") {
      parts.push(el("p", "", form?.auth === "password" ? "이 PC의 터미널에서 아래 명령으로 같은 비밀번호로 접속되는지 먼저 확인하세요." : "이 PC의 터미널에서 아래 명령이 비밀번호 없이 되는지 먼저 확인하세요."));
      const pre = el("code", "ssh-err-cmd", cmd); pre.setAttribute("data-no-i18n", ""); parts.push(pre);
    }
    const row = el("div", "unlock-actions");
    row.append(button("다시 시도", "primary", null, () => $("#unlock-form").requestSubmit($("#ssh-button"))), button("SSH 테스트", "", null, () => $("#ssh-test").click()));
    parts.push(row);
    const list = el("details", "ssh-err-list");
    list.append(el("summary", "", "다른 오류 종류"));
    for (const [k, title] of SSH_KINDS) {
      const r = el("div", `ssh-err-row${known?.[0] === k ? " on" : ""}`);
      r.append(el("strong", "", title), el("span", "", ctx.sshErrorText(k)));
      list.append(r);
    }
    parts.push(list);
    pane.replaceChildren(...parts);
  }

  // ------------------------------------------------------------ 시작 2·3단계 (198:6650, 198:6736)
  const STEPS = ["SSH 연결", "에이전트 선택", "연결 확인"];
  function stepper(n) {
    const bar = el("div", "start-steps");
    STEPS.forEach((label, i) => {
      if (i) bar.append(el("span", "start-line"));
      const s = el("span", `start-step${i + 1 < n ? " done" : i + 1 === n ? " on" : ""}`);
      s.append(el("i", "", i + 1 < n ? "✓" : String(i + 1)), label);
      bar.append(s);
    });
    return bar;
  }
  const host = () => state.ssh?.host?.split("@").pop() || (() => { try { return new URL(state.server).hostname; } catch (_) { return "서버"; } })();
  // Agents the server found, with the folders it reads; the switch only changes what this PC shows (설정 › 에이전트 does the same).
  function startAgents() {
    const reported = (state.data.tools?.agents || []).filter((a) => a.found !== false && AGENTS.some(([k]) => k === a.id));
    return reported.length ? reported : AGENTS.slice(0, 5).map(([id, name]) => ({ id, name }));
  }
  const isOn = (id) => !ctx.pages.prefs.agentsOff.includes(id);
  function switchEl(on, label, onFlip) {
    const s = el("button", "switch");
    s.type = "button";
    s.setAttribute("role", "switch");
    s.setAttribute("aria-checked", String(on));
    s.setAttribute("aria-label", label);
    s.append(el("i"));
    s.addEventListener("click", () => onFlip(!on));
    return s;
  }
  const startTitle = () => "서버의 Arkism에 연결";
  const startSub = () => (state.start === 2 ? "서버가 찾은 에이전트 중 이 PC에서 보여 줄 것을 골라요." : "마지막으로 연결이 잘 됐는지 확인해요.");
  function startMain() {
    const wrap = el("div", "start-wrap");
    wrap.append(stepper(state.start));
    if (state.start === 2) {
      const agents = startAgents();
      const card = el("div", "start-card");
      const head = el("div", "start-card-head");
      head.append(el("strong", "", "이 PC에 표시"), el("small", "", `서버 ${host()}가 찾은 에이전트 ${agents.length}개`));
      card.append(head);
      const paths = ctx.pages.agentPaths || {};
      for (const a of agents) {
        const spec = (AGENTS.find(([k]) => k === a.id) || [])[2];
        const r = el("div", "start-agent");
        const text = el("span", "start-agent-text");
        const dirs = [...(a.skills_dirs || []), ...(a.chats_dir ? [a.chats_dir] : [])];
        const where = el("small", "mono", (dirs.length ? dirs : (paths[a.id] || []).filter(Boolean)).join("  ·  "));
        where.setAttribute("data-no-i18n", "");
        text.append(el("strong", "", a.name), where);
        r.append(spec ? agentLogo(spec, a.name) : el("span", "agent-tile"), text, switchEl(isOn(a.id), `${a.name} 표시`, (next) => {
          const prefs = ctx.pages.prefs;
          if (!next && agents.filter((x) => isOn(x.id)).length === 1) { notice("에이전트가 하나는 켜져 있어야 해요."); return; }
          prefs.agentsOff = next ? prefs.agentsOff.filter((k) => k !== a.id) : [...prefs.agentsOff, a.id];
          ctx.pages.savePrefs();
          render(false);
        }));
        card.append(r);
      }
      const more = el("button", "link-btn accent start-more", "에이전트 관리 · 직접 추가 ›");
      more.type = "button";
      more.addEventListener("click", () => { state.start = 0; ctx.openSettings("agents"); });
      card.append(more);
      const act = el("div", "actions");
      act.append(button("다음 · 연결 확인", "primary", null, () => { state.start = 3; render(); }), button("이전", "", null, () => { state.start = 0; ctx.lock(); }));
      wrap.append(card, act);
      return wrap;
    }
    const ok = el("div", "start-card start-ok");
    const okText = el("span", "start-agent-text");
    const who = el("small", "mono", ctx.sshLabel(state.ssh) || state.server);
    who.setAttribute("data-no-i18n", "");
    okText.append(el("strong", "", "SSH로 연결됐어요"), who);
    ok.append(el("i", "start-check", "✓"), okText, el("output", "start-badge ok", "연결됨"));
    const list = el("div", "start-card");
    const listHead = el("div", "start-card-head");
    listHead.append(el("strong", "", "연결 상태"));
    list.append(listHead);
    const line = (title, sub, side) => {
      const r = el("div", "start-row");
      const t = el("span", "start-agent-text");
      const s = el("small", "", sub);
      t.append(el("strong", "", title), s);
      r.append(t, ...side);
      list.append(r);
      return s;
    };
    const ready = state.ready === null ? ["", "확인 중"] : state.ready ? ["ok", "응답 확인"] : ["warn", "응답 없음"];
    line("서버", ctx.sshLabel(state.ssh) || state.server, [el("output", `start-badge ${ready[0]}`, ready[1])]).classList.add("mono");
    line("에이전트", "최근에 보고된 연결 상태를 확인해요.", [button("연결 상태 보기", "small", null, () => { const t = $("#status-toggle"); if ($("#status-card").hidden) t.click(); })]);
    line("모델", "사용할 계정과 모델을 골라 연결을 시험해요.", [button("모델 연결 시험", "small", null, () => { state.start = 0; ctx.openSettings("agents"); })]);
    const act = el("div", "actions");
    act.append(button("홈으로 시작하기", "primary", null, () => { state.start = 0; state.selected = null; render(); }), button("이전", "", null, () => { state.start = 2; render(); }));
    wrap.append(ok, list, act);
    return wrap;
  }
  function startDetail() {
    const body = ctx.detailShell(`시작 ${state.start}/3`);
    const kv = (rows) => {
      const box = el("div", "start-kv");
      for (const [k, v, plain] of rows) { if (!v) continue; const r = el("div", ""); const val = el("span", plain ? "" : "mono", v); val.setAttribute("data-no-i18n", ""); r.append(el("span", "", k), val); box.append(r); }
      return box;
    };
    if (state.start === 2) {
      const agents = startAgents();
      body.append(el("h2", "", "고르는 기준"), el("p", "", "표시 선택은 이 PC에만 적용돼요. 서버가 스킬과 대화를 모으는 설정은 바뀌지 않아요."),
        el("p", "meta", "서버 수집 설정과 직접 추가는 설정 › 에이전트에서 바꿔요."), kv([["서버", host()], ["켠 에이전트", `${agents.length}개 중 ${agents.filter((a) => isOn(a.id)).length}개`, true]]));
      return;
    }
    const f = state.ssh || {};
    body.append(el("h2", "", "연결 정보"), kv([["방식", "SSH 터널", true], ["Host", f.host], ["User", f.user || ""], ["Port", f.port ? String(f.port) : "22"], ["키 파일", f.key || "ssh 기본 키", !f.key], ["서버 경로", f.bin || "자동", !f.bin]]),
      el("p", "meta", "연결 정보는 설정 › 연결에서 바꿀 수 있어요."));
  }

  // ------------------------------------------------------------ 메뉴별 확인할 것: 워크플로우 · 라우팅 (198:200)
  // One GET each per load (the crew runs list and the routing overview the other menus already use); hidden when the server has none.
  const RUN_LIMIT = 100;
  let extra = { g: -1, runs: undefined, route: undefined };
  function menuExtras(generation) {
    if (extra.g !== generation) {
      extra = { g: generation, runs: undefined, route: undefined };
      const g = generation;
      const again = () => { if (extra.g === g && state.view === "todo" && !state.selected && !state.todoAll) render(false); };
      // by status, not the latest 30 runs: an approval or failure older than those still counts (#465)
      const runs = (status) => request(`/v1/crew/runs?${query({ status, limit: RUN_LIMIT })}`).then((r) => r.items || []);
      Promise.all([runs("waiting"), runs("failed")]).then(([waiting, failed]) => ({ waiting, failed }), () => null).then((v) => { if (extra.g === g) { extra.runs = v; again(); } });
      request(`/v1/routing/overview?${query({ range: "30d" })}`).then((d) => (d?.kpi ? d : null), () => null).then((v) => { if (extra.g === g) { extra.route = v; again(); } });
    }
    const rows = [];
    if (extra.runs) {
      const week = Date.now() - 7 * 86400000;
      const { count } = ctx, full = (list) => list.length >= RUN_LIMIT; // a full page may have more behind it
      const waiting = count(extra.runs.waiting.filter((x) => x.crew_run?.status === "waiting" && x.crew_run.waiting?.kind === "approval"), { more: full(extra.runs.waiting) });
      const recent = (x) => new Date(x.created_at).getTime() > week; // newest first: a full page that reaches past the week is complete
      const failed = count(extra.runs.failed.filter((x) => x.crew_run?.status === "failed" && recent(x)), { more: full(extra.runs.failed) && extra.runs.failed.every(recent) });
      rows.push(["FLOW", "워크플로우", [{ label: `확인 대기 ${waiting.text}`, ...waiting }, { label: `실패 ${failed.text}`, ...failed }].filter((p) => p.min), ["crew"]]);
    }
    if (extra.route) {
      const parts = (extra.route.models || []).filter((m) => m.status === "watch" || m.status === "slow")
        .map((m) => ({ label: m.status === "slow" ? `${m.name} 느림` : `${m.name} 지연 모니터링`, n: 1 }));
      const k = extra.route.kpi;
      if (parts.length && k.budget_usd) parts.push({ label: `이번 달 예산 ${Math.round((k.cost_usd / k.budget_usd) * 100)}%`, n: 0 });
      rows.push(["ROUTER", "라우팅", parts, ["routing"]]);
    }
    return rows;
  }

  return { onInput, wireSearch, hide, palette, shortcuts, lostCard, lostSide, sshError, startTitle, startSub, startMain, startDetail, menuExtras };
};
