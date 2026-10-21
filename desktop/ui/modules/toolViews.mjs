// Owns MCP connections and login. Dependencies are explicit live ports, never a global scope.
export function createToolViews(deps) {


  // ---------------------------------------------------------------- MCP·플러그인
  // The host tool (arkism-agents.timer) applies a switch within a minute and reports back.
  const SWITCHABLE = { mcp: ["claude_code", "codex", "gemini", "zcode", "grok", "opencode", "cursor"], plugin: ["claude_code"] };

  async function loadTools() {
    try {
      const r = await deps.request(`/v1/agent_tools?${deps.query()}`);
      const t = r.agent_tools || {};
      return { servers: t.servers || [], plugins: t.plugins || [], requests: t.requests || [], collected_at: t.collected_at || "", skill_checks: t.skill_checks || [], skill_check_tools: t.skill_check_tools || "",
        agents: t.agents || null, shared_dirs: t.shared_dirs || null, memory_use: t.memory_use || null, skill_updates: t.skill_updates || [], pc_links: t.pc_links || [], skill_repos: t.skill_repos || [] };
    } catch {
      // null tells renderTools to explain that the host sync has not reported (older server or stopped timer).
      console.warn("agent tools unavailable");
      return null;
    }
  }

  const pendingTool = (t) => (t?.requests || []).some((q) => q.status === "pending");

  const toolKind = (entry) => (entry.transport === "plugin" ? "plugin" : "mcp");

  function toolEntries() {
    const t = deps.state.data.tools;
    return t ? [...t.servers, ...t.plugins] : [];
  }

  function requestsFor(entry) {
    return (deps.state.data.tools?.requests || []).filter((q) => q.name === entry.name && q.kind === toolKind(entry));
  }

  const needsLogin = (entry, agent) => entry.agents?.[agent] === "on" && entry.login?.[agent] === "needed";

  function toolState(entry) {
    const pending = requestsFor(entry).filter((q) => q.status === "pending");
    if (pending.length) return ["적용 대기", "warn"];
    const login = Object.keys(entry.login || {}).filter((k) => needsLogin(entry, k));
    if (login.length) return [`${login.map(value => deps.agentName(value)).join("·")} 로그인 필요`, "warn"];
    if (entry.note) return [entry.note, "warn"];
    return Object.values(entry.agents).includes("on") ? ["정상", "ok"] : ["꺼짐", "off"];
  }

  async function refreshTools() {
    deps.state.data.tools = await loadTools();
    deps.renderStatusCard();
    deps.render(false);
    deps.renderDetail();
  }

  // 연결 상태 card: re-read the server's report every minute, so a host that stops reporting turns amber, then red.
  setInterval(async () => {
    if (!deps.state.token || document.hidden) return;
    const g = deps.generation; // a server or scope change while this was out makes the answer stale
    const t = await loadTools().catch(() => null);
    if (g !== deps.generation) return;
    if (t) deps.state.data.tools = t;
    deps.renderStatusCard();
  }, 60000);

  let toolPoll = null;

  function pollTools() {
    clearTimeout(toolPoll);
    let left = 14; // about two minutes
    const tick = async () => {
      await refreshTools();
      if (pendingTool(deps.state.data.tools) && --left > 0) toolPoll = setTimeout(tick, 9000);
    };
    toolPoll = setTimeout(tick, 9000);
  }

  async function switchTool(entry, agent, enabled) {
    try {
      const r = await deps.request("/v1/agent_tools/requests", { method: "POST", body: JSON.stringify({ scope: deps.state.scope, agent, kind: toolKind(entry), name: entry.name, enabled }) });
      deps.state.data.tools.requests = r.agent_tools?.requests || deps.state.data.tools.requests;
      deps.render(false);
      deps.renderDetail();
      deps.notice(`${deps.agentName(agent)}에서 ${entry.name} ${enabled ? "켜기" : "끄기"}를 요청했어요. 1분 안에 적용돼요.`);
      pollTools();
    } catch (err) {
      deps.notice(`요청하지 못했어요: ${err.message}`);
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
    const dlg = deps.el("dialog", "crew-dialog mcp-add");
    const form = deps.el("form");
    form.method = "dialog";
    const head = deps.el("div", "dialog-head");
    const t = deps.el("div", "");
    t.append(deps.el("h2", "", "MCP 서버 추가"), deps.el("p", "", "키나 토큰은 넣지 마세요. 로그인이 필요한 서버는 추가한 뒤 에이전트에서 인증해요."));
    head.append(t);
    const name = deps.el("input", "mono");
    name.placeholder = "github";
    const how = deps.el("select");
    for (const [k, l] of [["stdio", "프로그램 실행 (stdio)"], ["http", "주소 (http)"]]) { const o = deps.el("option", "", l); o.value = k; how.append(o); }
    const cmd = deps.el("input", "mono");
    cmd.placeholder = "npx -y @modelcontextprotocol/server-github";
    const url = deps.el("input", "mono");
    url.placeholder = "https://mcp.example.com/mcp";
    const f = (label, input, hint) => { const l = deps.el("label", "field", label); l.append(input); if (hint) l.append(deps.el("small", "", hint)); return l; };
    const cmdF = f("실행할 프로그램", cmd), urlF = f("주소", url, "HTTP 연결은 https 주소만 사용할 수 있어요.");
    // 연결 방식: two pill buttons drive the select (the preset picker sets the select and calls sync).
    const howSeg = deps.el("div", "segment mcp-how");
    const howBtns = [["stdio", "stdio", "프로그램 실행"], ["http", "http", "https 주소"]].map(([k, code, sub]) => {
      const b = deps.el("button", "tab"); b.type = "button"; b.dataset.how = k;
      b.append(deps.el("code", "", code), deps.el("small", "", sub));
      b.addEventListener("click", () => { how.value = k; sync(); });
      howSeg.append(b);
      return b;
    });
    const sync = () => { cmdF.hidden = how.value !== "stdio"; urlF.hidden = how.value !== "http"; howBtns.forEach((b) => b.classList.toggle("active", b.dataset.how === how.value)); };
    how.addEventListener("change", sync);
    sync();
    const picked = new Set(["claude_code", "codex"]);
    const agents = deps.el("div", "mcp-agents");
    for (const [k, n, spec] of deps.AGENTS.filter(([k]) => SWITCHABLE.mcp.includes(k))) {
      const r = deps.el("div", "mcp-agent");
      const tt = deps.el("div", "");
      tt.append(deps.el("span", "", n), deps.el("small", "", "각 에이전트의 CLI로 서버에 추가해요"));
      const sw = deps.el("button", "switch");
      sw.type = "button";
      sw.setAttribute("role", "switch");
      sw.setAttribute("aria-label", n);
      sw.append(deps.el("i"));
      const draw = () => sw.setAttribute("aria-checked", String(picked.has(k)));
      draw();
      sw.addEventListener("click", () => { picked.has(k) ? picked.delete(k) : picked.add(k); draw(); });
      r.append(deps.agentLogo(spec, n), tt, sw);
      agents.append(r);
    }
    // 빠른 선택: the catalog's presets as chips; picking one runs the catalog's own fill (name, method, program or address).
    const preset = deps.catalog.presetField({ name, how, cmd, url, sync });
    const pick = preset.querySelector("select");
    const chips = deps.el("div", "mcp-presets");
    for (const o of [...pick.options].filter((x) => x.value)) {
      const p = (window.ArkismCatalog.PRESETS || []).find((x) => x.id === o.value);
      const c = deps.el("button", "mcp-preset"); c.type = "button";
      c.append(deps.el("code", "", p?.name || o.value), deps.el("small", "", p?.how === "http" ? "주소" : "프로그램"));
      c.addEventListener("click", () => { pick.value = o.value; pick.dispatchEvent(new Event("change")); chips.querySelectorAll(".mcp-preset").forEach((x) => x.classList.toggle("on", x === c)); });
      chips.append(c);
    }
    pick.hidden = true;
    preset.firstChild.textContent = "빠른 선택";
    preset.insertBefore(chips, pick);
    preset.append(deps.el("small", "", "고르면 이름·방식·프로그램(또는 주소)이 한 번에 채워져요."));
    const howF = deps.el("div", "field");
    howF.append("연결 방식", howSeg);
    const agentsF = deps.el("div", "field");
    agentsF.append("넣을 에이전트", agents);
    const main = deps.el("div", "mcp-main");
    main.append(preset, f("이름", name, "영문·숫자·@._/- · 최대 128자"), howF, urlF, cmdF, agentsF, deps.el("p", "mcp-warn", "키와 토큰은 이 입력칸에 넣지 마세요."));
    // Right column: the add requests sent lately, and what the two methods mean.
    const side = deps.el("aside", "mcp-side");
    const reqs = (deps.state.data.tools?.requests || []).filter((q) => ["mcp_add", "mcp", "mcp_login"].includes(q.kind)).slice(-5).reverse();
    const sideHead = (title, aside) => { const h = deps.el("div", "cn-sec-head"); h.append(deps.el("h3", "", title)); if (aside) h.append(deps.el("small", "", aside)); return h; };
    side.append(sideHead("최근 요청", reqs.length ? `${reqs.length}건` : ""));
    if (reqs.length) {
      const box = deps.el("div", "cn-sublist");
      const label = { pending: ["적용 대기", "warn"], done: ["적용됨", ""], failed: ["실패", "warn"], waiting: ["로그인 대기", "warn"] };
      for (const q of reqs) {
        const r = deps.el("div", "cn-subrow");
        const what = q.kind === "mcp_add" ? "추가" : q.kind === "mcp_login" ? "로그인" : q.enabled ? "켜기" : "끄기";
        const [l, c] = label[q.status] || [q.status, ""];
        const tt = deps.el("div", "cn-subrow-body"); tt.append(deps.raw(deps.el("span", "", `${q.name} ${what}`)));
        r.append(tt, cnBadge(q.error ? `${l} · ${q.error}` : l, c));
        box.append(r);
      }
      side.append(box, deps.el("p", "cn-note", "적용될 때까지 상태를 다시 읽어요."));
    } else side.append(deps.el("p", "cn-note", "아직 보낸 요청이 없어요."));
    const ways = deps.el("dl", "mcp-ways");
    ways.append(deps.el("dt", "", "stdio"), deps.el("dd", "", "서버 컴퓨터에서 프로그램을 실행해요. Node.js(npx)나 uv(uvx)가 필요할 수 있어요."),
      deps.el("dt", "", "http"), deps.el("dd", "", "원격 서버 주소로 연결해요. 로그인이 필요하면 추가한 뒤 인증해요."));
    side.append(sideHead("방식 고르기"), ways);
    const body = deps.el("div", "dialog-body");
    body.append(main, side);
    const foot = deps.el("div", "dialog-foot");
    const cancel = deps.button("취소", "");
    cancel.type = "submit";
    cancel.value = "cancel";
    const ok = deps.button("추가 요청", "primary");
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
        deps.notice("이름, 프로그램이나 https 주소, 에이전트를 확인해 주세요.");
        return;
      }
      ok.disabled = true;
      try {
        let r;
        for (const agent of picked) r = await deps.request("/v1/agent_tools/requests", { method: "POST", body: JSON.stringify({ scope: deps.state.scope, agent, kind: "mcp_add", name: n, enabled: true, ...extra }) });
        deps.state.data.tools.requests = r.agent_tools?.requests || deps.state.data.tools.requests;
        dlg.close();
        deps.notice(`${n} 추가를 요청했어요. 서버가 1분 안에 추가해요.`);
        deps.render(false);
        pollTools();
        deps.catalog.watchAdd(r, n);
      } catch (error_) { deps.notice(error_.message === "secret_detected" ? "키나 토큰처럼 보이는 값이 있어요. 빼고 다시 넣어 주세요." : `요청하지 못했어요 (${error_.message}).`); ok.disabled = false; }
    });
    dlg.append(form);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal();
  }

  const CONFIG_FILES = { claude_code: "~/.claude.json", codex: "~/.codex/config.toml" };

  function toolLogos(entry) {
    const on = deps.AGENTS.filter(([k]) => entry.agents[k] === "on");
    const box = deps.el("span", "cn-logos");
    if (!on.length) box.append(deps.el("span", "cn-quiet", "에이전트 없음"));
    on.forEach(([, n, logo]) => box.append(deps.agentLogo(logo, n)));
    return box;
  }

  // v4 · 연결 (Figma 05): small builders shared by 도구·에이전트 screens.
  function cnTile(label, value, unit, warn, onClick) {
    const t = deps.el(onClick ? "button" : "div", `cn-tile${onClick ? " link" : ""}`);
    if (onClick) { t.type = "button"; t.addEventListener("click", onClick); }
    const v = deps.el("div", "cn-tile-value");
    v.append(deps.el("strong", warn ? "warn" : "", value));
    if (unit) v.append(deps.el("span", "", unit));
    t.append(deps.el("p", "", label), v);
    return t;
  }

  function cnBadge(text, cls = "") { return deps.el("span", `cn-badge ${cls}`.trim(), text); }

  // A list row: div (it can hold its own buttons), Enter/Space open it like a click.
  function cnRow(id, parts, onOpen) {
    const r = deps.el("div", `cn-row${deps.state.selected === id ? " selected" : ""}`);
    r.tabIndex = 0;
    r.setAttribute("role", "button");
    r.dataset.id = id;
    r.append(...parts);
    const open = () => { deps.state.selected = id; document.querySelectorAll(".cn-row[data-id]").forEach((x) => x.classList.toggle("selected", x.dataset.id === id)); onOpen(); deps.openDetail(); };
    r.addEventListener("click", (e) => { if (!e.target.closest(".cn-mini")) open(); });
    r.addEventListener("keydown", (e) => { if ((e.key === "Enter" || e.key === " ") && e.target === r) { e.preventDefault(); open(); } });
    return r;
  }

  function cnRowBody(title, meta, extra) {
    const b = deps.el("div", "cn-row-body");
    const t = deps.el("div", "cn-row-title");
    t.append(...[].concat(title));
    b.append(t);
    const line = (cls, v) => { const d = deps.el("div", cls); d.append(v); return d; };
    if (extra) b.append(line("cn-row-desc", extra));
    if (meta) b.append(line("cn-row-meta", meta));
    return b;
  }

  function cnMini(label, onClick) {
    const b = deps.el("button", "cn-mini", label);
    b.type = "button";
    b.addEventListener("click", (e) => { e.stopPropagation(); onClick(); });
    return b;
  }

  const BADGE = { ok: "", warn: "warn", off: "off" };

  // Which summary tile a server counts under (Figma: 정상 · 로그인 필요 · 끊김 · 꺼짐).
  function toolGroup(entry) {
    if (deps.AGENTS.some(([k]) => needsLogin(entry, k))) return "login";
    if (entry.note) return "broken";
    return Object.values(entry.agents || {}).includes("on") ? "ok" : "off";
  }

  function renderTools() {
    const t = deps.state.data.tools;
    const plugin = deps.state.toolTab === "plugin";
    const table = deps.$("#table");
    ["#controls", "#chips"].forEach((sel) => deps.$(sel).replaceChildren());
    if (!t) {
      deps.headExtras();
      deps.$("#stats").replaceChildren();
      table.replaceChildren(deps.el("div", "empty", "MCP 목록을 불러오지 못했어요. 서버의 arkism-agents.timer가 돌고 있는지 확인해 주세요."));
      return;
    }
    deps.headExtras({ count: `${(plugin ? t.plugins : t.servers).length}개` }); // 플러그인 · MCP are area tabs already
    const age = t.collected_at ? Date.now() - new Date(t.collected_at).getTime() : Infinity;
    if (age > 5 * 60000) deps.notice("서버에서 최근 5분 동안 목록이 올라오지 않았어요. arkism-agents.timer를 확인해 주세요.");
    if (plugin) deps.$("#stats").replaceChildren();
    else {
      const n = (g) => t.servers.filter((e) => toolGroup(e) === g).length;
      const tiles = deps.el("div", "cn-tiles");
      tiles.append(cnTile("정상", String(n("ok")), "개"), cnTile("로그인 필요", String(n("login")), "개", n("login") > 0), cnTile("끊김", String(n("broken")), "개", n("broken") > 0), cnTile("꺼짐", String(n("off")), "개"));
      deps.$("#stats").replaceChildren(tiles);
    }
    // 에이전트: only what that agent has on.
    const keys = deps.shownAgents().map(([k]) => k);
    if (!keys.includes(deps.state.toolAgent)) deps.state.toolAgent = "";
    const who = deps.state.toolAgent;
    deps.$("#controls").replaceChildren(deps.skSelect("에이전트", [["", "모든 에이전트"], ...keys.map((k) => [k, `${deps.agentName(k)} ${(plugin ? t.plugins : t.servers).filter((e) => e.agents?.[k] === "on").length}`])], who, (k) => { deps.state.toolAgent = k; deps.state.selected = null; deps.render(); }));
    if (who && !deps.AGENT_SYNC[who]) deps.$("#chips").replaceChildren(deps.el("p", "cn-note", `${deps.agentName(who)}의 MCP 설정은 아직 읽지 않아요. 켜고 끄기는 Claude Code·Codex만 돼요.`));
    const list = (plugin ? t.plugins : t.servers).filter((e) => (!who || e.agents?.[who] === "on") && deps.matches(`${e.name} ${e.target || ""} ${e.desc || ""}`));
    if (!deps.state.selected && list.length) deps.state.selected = `tool:${(list.find((e) => toolState(e)[1] === "warn") || list[0]).name}`;
    table.className = "cn-list";
    table.replaceChildren(...list.map((e) => {
      const [label, cls] = toolState(e);
      const ic = deps.el("span", "cn-row-icon"); ic.append(deps.icon(plugin ? "puzzle-piece" : e.name === "arkism" ? "brain" : "plug"));
      const tools = (e.tools || []).length;
      const users = toolUsers(e).length;
      const parts = [ic];
      if (plugin) {
        const counts = {};
        (e.contents || []).forEach((it) => { const k = ITEM_KIND[it.kind] || it.kind; counts[k] = (counts[k] || 0) + 1; });
        const title = [deps.raw(deps.el("span", "", e.name))];
        if (e.version) title.push(deps.el("span", "cn-ver", `v${e.version}`));
        parts.push(cnRowBody(title, Object.entries(counts).map(([k, c]) => `${k} ${c}`).join(" · ") || "", e.desc ? deps.raw(deps.el("span", "", e.desc)) : null));
      } else {
        parts.push(cnRowBody(deps.raw(deps.el("span", "mono", e.name)), [e.desc, tools ? `도구 ${tools}개` : "", users ? `워크플로우 ${users}개` : ""].filter(Boolean).join(" · ")));
      }
      parts.push(toolLogos(e));
      const loginFor = deps.AGENTS.map(([k]) => k).find((k) => needsLogin(e, k));
      if (loginFor) parts.push(cnMini("로그인", () => toolAgentPopup(e, toolKind(e), loginFor, true)));
      else if (!plugin && e.note && reconnectAgents(e).length) parts.push(cnMini("다시 연결", () => reconnectTool(e)));
      parts.push(cnBadge(label, BADGE[cls]));
      return cnRow(`tool:${e.name}`, parts, deps.renderDetail);
    }));
    if (!list.length) table.append(deps.el("div", "empty", "여기에 표시할 항목이 없어요."));
  }

  function toolsSummary() {
    const t = deps.state.data.tools;
    const body = deps.detailShell("MCP·플러그인");
    const d = deps.el("div", "cn-detail");
    d.append(deps.el("p", "cn-kind", "MCP·플러그인"), deps.el("h2", "cn-title", "도구 서버와 플러그인"), deps.el("p", "cn-desc", "서버의 Claude Code·Codex 설정을 1분마다 읽어 보여 줘요. 항목을 고르면 에이전트별로 켜고 끌 수 있어요."));
    if (t?.collected_at) d.append(deps.el("p", "cn-desc", `마지막 확인 ${deps.ago(t.collected_at)}`));
    body.append(d);
  }

  // A styled yes/no box (Figma 확인 팝업 253:18954) in place of the browser's await ask().
  function confirmPop(title, text, okLabel) {
    return new Promise((resolve) => {
      const dlg = deps.el("dialog", "cn-confirm");
      const form = deps.el("form");
      form.method = "dialog";
      const row = deps.el("div", "cn-confirm-btns");
      const no = deps.button("취소", "");
      no.value = "no"; no.type = "submit";
      const ok = deps.button(okLabel, "danger");
      ok.value = "ok"; ok.type = "submit";
      row.append(no, ok);
      form.append(deps.el("h2", "", title), deps.el("p", "", text), row);
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
    const ag = cnSection(kind === "plugin" ? "켜 둔 에이전트" : "에이전트", `${deps.AGENTS.filter(([k]) => entry.agents[k] === "on").length} / ${deps.AGENTS.length}`);
    const grid = deps.el("div", "cn-chips");
    for (const [k, n, logo] of deps.AGENTS) {
      const can = SWITCHABLE[kind].includes(k);
      const on = entry.agents[k] === "on";
      const waiting = pendingFor(k);
      const login = needsLogin(entry, k);
      const cls = ["cn-chip", on ? "on" : "off", (waiting || login) && "staged"].filter(Boolean);
      const a = deps.el("button", cls.join(" "));
      a.type = "button";
      a.disabled = !can;
      a.append(deps.agentLogo(logo, n), n + switchNote(waiting, can, login));
      a.title = can ? "누르면 어떻게 적용되는지 보여 줘요" : switchTitle(can, on);
      if (can) a.addEventListener("click", () => toolAgentPopup(entry, kind, k));
      grid.append(a);
    }
    ag.append(grid, deps.el("p", "cn-note", "에이전트를 누르면 켜기·끄기·로그인을 해요. 노란 칸은 할 일이 남았어요."));
    return ag;
  }

  // A small window that says what clicking an agent does, where it lands on the server, and does it.
  function applyPopup(title, lead, rows, actions) {
    const dlg = deps.el("dialog", "apply-pop");
    const form = deps.el("form");
    form.method = "dialog";
    const head = deps.el("div", "dialog-head");
    const ht = deps.el("div", "");
    ht.append(deps.el("h2", "", title), deps.el("p", "", lead));
    const close = deps.el("button", "icon-btn plain");
    close.value = "cancel";
    close.setAttribute("aria-label", "닫기");
    close.append(deps.icon("x"));
    head.append(ht, close);
    const body = deps.el("div", "dialog-body");
    const steps = deps.el("ol", "apply-steps");
    rows.forEach((r) => steps.append(deps.el("li", "", r)));
    const status = deps.el("p", "apply-status");
    status.setAttribute("role", "status");
    status.hidden = true;
    body.append(steps, status);
    const foot = deps.el("div", "dialog-foot");
    foot.append(...actions(dlg, (text, cls = "") => { status.hidden = !text; status.className = `apply-status ${cls}`; status.textContent = text || ""; }));
    form.append(head, body, foot);
    dlg.append(form);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal();
    return { dlg, status, body };
  }

  function toolAgentPopup(entry, kind, agent, startLogin = false) {
    const n = deps.agentName(agent);
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
      const sw = deps.button(on ? "끄기" : "켜기", on ? "ghost" : "primary", null, async () => { dlg.close(); await switchTool(entry, agent, !on); });
      if (remote && on) {
        const lg = deps.button(login ? "로그인" : "다시 로그인", login ? "primary" : "", "lock-simple", async () => {
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
    const r = await deps.request("/v1/agent_tools/requests", { method: "POST", body: JSON.stringify({ scope: deps.state.scope, agent, kind: "mcp_login", name: entry.name, enabled: true }) });
    const mine = (r.agent_tools?.requests || []).findLast((q) => q.kind === "mcp_login" && q.agent === agent && q.name === entry.name);
    if (!mine) throw new Error("요청을 찾지 못했어요");
    const ready = await waitRequest(mine.id, (q) => q.status !== "pending", 120);
    if (ready.status !== "waiting") throw new Error(ready.error || "로그인 창을 열지 못했어요");
    let target;
    if (deps.tauri) {
      show("브라우저에서 로그인하고 허용을 눌러 주세요. 끝나면 이 창으로 돌아와요.");
      target = await deps.tauri.invoke("mcp_login", { authUrl: ready.auth_url }).catch((e) => {
        const m = String(e);
        if (m.includes("port_busy")) throw new Error("이 PC에서 로그인 주소를 받을 칸이 이미 쓰이고 있어요");
        throw new Error(m.includes("timeout") ? "10분 안에 로그인하지 않았어요" : m);
      });
    } else {
      target = await pastedRedirect(pop, ready.auth_url, show);
    }
    show("로그인을 마무리하는 중이에요…");
    await deps.request(`/v1/agent_tools/requests/${encodeURIComponent(mine.id)}/login`, { method: "POST", body: JSON.stringify({ scope: deps.state.scope, callback: target }) });
    const end = await waitRequest(mine.id, (q) => q.status === "done" || q.status === "failed", 90);
    if (end.status === "failed") throw new Error(end.error || "실패");
  }

  // A plain browser (development) cannot listen on the PC: open the page, paste the address it ends on.
  function pastedRedirect(pop, authUrl, show) {
    return new Promise((resolve, reject) => {
      const a = deps.el("a", "", "로그인 창 열기");
      a.href = authUrl;
      a.target = "_blank";
      a.rel = "noreferrer";
      const input = deps.el("input");
      input.placeholder = "허용 후 주소창의 http://localhost:… 주소를 붙여 넣으세요";
      const go = deps.button("확인", "primary", null, () => {
        const u = URL.canParse(input.value.trim()) ? new URL(input.value.trim()) : null;
        if (!u || !["localhost", "127.0.0.1"].includes(u.hostname)) {
          show("http://localhost: 로 시작하는 주소를 붙여 넣어 주세요.", "error");
          return;
        }
        box.remove();
        resolve(u.pathname + u.search);
      });
      const box = deps.el("div", "apply-paste");
      box.append(a, input, go);
      pop.body.append(box);
      pop.dlg.addEventListener("close", () => reject(new Error("창을 닫았어요")));
      show("로그인 창을 열고 허용한 뒤, 주소를 아래에 붙여 넣어 주세요.");
    });
  }

  function cnSection(title, aside) {
    const s = deps.el("section", "cn-sec");
    const h = deps.el("div", "cn-sec-head");
    h.append(deps.el("h3", "", title));
    if (aside) h.append(deps.el("small", "", aside));
    s.append(h);
    return s;
  }

  function cnFacts(pairs) {
    const dl = deps.el("dl", "cn-facts");
    pairs.filter(([, v]) => v).forEach(([k, v, cls]) => dl.append(deps.el("dt", "", k), deps.el("dd", cls || "", v)));
    return dl;
  }

  function cnCallout(title, text, buttons = []) {
    const c = deps.el("div", "cn-callout");
    const p = deps.el("p", "");
    if (title) p.append(deps.el("strong", "", title), " ");
    if (text) p.append(text);
    c.append(p);
    if (buttons.length) { const b = deps.el("div", "cn-btns"); b.append(...buttons); c.append(b); }
    return c;
  }

  function toolHistory(entry) {
    const hist = requestsFor(entry).slice(-5).reverse();
    if (!hist.length) return null;
    const sec = cnSection("최근 요청", `${hist.length}건`);
    const label = { pending: ["적용 대기", "warn"], done: ["적용됨", ""], failed: ["실패", "warn"] };
    const box = deps.el("div", "cn-sublist");
    hist.forEach((q) => {
      const r = deps.el("div", "cn-subrow");
      const t = deps.el("div", "cn-subrow-body");
      t.append(deps.el("span", "", `${deps.agentName(q.agent)} ${q.enabled ? "켜기" : "끄기"}`), deps.el("small", "", [deps.ago(q.requested_at), q.error].filter(Boolean).join(" · ")));
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
    const body = deps.detailShell(kind === "plugin" ? "플러그인" : "MCP 서버");
    const d = deps.el("div", "cn-detail");
    const head = deps.el("div", "cn-head");
    head.append(deps.el("p", "cn-kind", [kind === "plugin" ? "플러그인" : "MCP 서버", entry.version ? `v${entry.version}` : "", label].filter(Boolean).join(" · ")),
      deps.raw(deps.el("h2", `cn-title${kind === "plugin" ? "" : " mono"}`, entry.name)));
    if (entry.desc) head.append(deps.raw(deps.el("p", "cn-desc", entry.desc)));
    const parts = [head];
    const loginFor = deps.AGENTS.map(([k]) => k).filter((k) => needsLogin(entry, k));
    if (loginFor.length) {
      parts.push(cnCallout(`${loginFor.map(value => deps.agentName(value)).join("·")} 로그인 필요`, "누르면 로그인 창이 열려요. Cloudflare 같은 서비스에서 허용하면 서버의 에이전트에 자동으로 적용돼요.",
        loginFor.map((k) => deps.button(`${deps.agentName(k)} 로그인`, "primary", null, () => toolAgentPopup(entry, kind, k, true)))));
    } else if (entry.note) {
      parts.push(cnCallout(entry.note, kind === "plugin" ? "Claude Code에서 /plugin 으로 다시 확인해 주세요." : "‘다시 연결’을 누르거나, 서버의 에이전트에서 /mcp 를 열어 다시 인증해 주세요. 끝나면 1분 안에 여기 반영돼요.",
        kind === "mcp" && reconnectAgents(entry).length ? [deps.button("다시 연결", "primary", null, () => reconnectTool(entry))] : []));
    }
    const btns = deps.el("div", "cn-btns");
    const files = kind === "plugin" ? "~/.claude/plugins" : [...new Set(Object.keys(entry.agents || {}).filter((k) => entry.agents[k] === "on").map((k) => CONFIG_FILES[k]).filter(Boolean))].join("\n");
    if (kind === "mcp" && reconnectAgents(entry).length && (loginFor.length || !entry.note)) btns.append(deps.button("다시 연결", "", null, () => reconnectTool(entry)));
    // The config sits on the server, so the app hands over its path (copy) rather than opening it here.
    if (files) { const cp = deps.copyButton(files, "설정 파일 경로 복사", ""); cp.title = files; btns.append(cp); }
    if (btns.childElementCount) parts.push(btns);
    const conn = cnSection("연결");
    const seen = deps.state.data.tools?.collected_at;
    conn.append(cnFacts([["방식", kind === "plugin" ? "Claude Code 플러그인" : [entry.transport, entry.target].filter(Boolean).join(" · "), "mono"],
      ["설정 파일", kind === "plugin" ? "~/.claude/plugins" : Object.keys(entry.agents || {}).map((k) => CONFIG_FILES[k]).filter(Boolean).join(" · "), "mono"],
      ["마지막 확인", seen ? deps.ago(seen) : "—"]]));
    if (kind === "plugin") parts.push(pluginContents(entry), toolSwitches(entry, kind));
    else parts.push(conn, toolSwitches(entry, kind), serverTools(entry), serverCalls(entry));
    if (kind === "plugin") parts.push(conn);
    parts.push(...toolSkillSections(entry), toolExperts(entry));
    const hist = toolHistory(entry);
    if (hist) parts.push(hist);
    const on = Object.entries(entry.agents || {}).filter(([, v]) => v === "on").map(([k]) => k);
    const foot = deps.el("div", "cn-foot");
    if (on.length) foot.append(deps.button(kind === "plugin" ? "끄기" : "연결 끊기", "cn-link", null, async () => {
      if (!await confirmPop(kind === "plugin" ? "플러그인을 끌까요?" : "연결 끊기할까요?", `${entry.name}를 ${on.map(value => deps.agentName(value)).join("·")}에서 끌까요? 설정은 보관돼서 다시 켜면 그대로 돌아와요.`, kind === "plugin" ? "끄기" : "연결 끊기")) return;
      for (const k of on) await switchTool(entry, k, false);
    }));
    if (kind === "plugin") foot.append(deps.el("span", "cn-grow"), deps.button("제거", "cn-link", null, async () => {
      if (!await confirmPop("플러그인을 제거할까요?", `${entry.name} 플러그인을 Claude Code에서 지울까요? 다시 쓰려면 새로 설치해야 해요.`, "제거")) return;
      try {
        const r = await deps.request("/v1/agent_tools/requests", { method: "POST", body: JSON.stringify({ scope: deps.state.scope, agent: "claude_code", kind: "plugin_remove", name: entry.name, enabled: false }) });
        deps.state.data.tools.requests = r.agent_tools?.requests || deps.state.data.tools.requests;
        deps.notice(`${entry.name} 제거를 요청했어요. 1분 안에 지워져요.`);
        deps.render(false);
        pollTools();
      } catch (e) { deps.notice(`요청하지 못했어요 (${e.message}).`); }
    }));
    if (foot.childElementCount) parts.push(foot);
    d.append(...parts);
    body.append(d);
  }

  // 다시 연결: a server that signs in is signed in again (mcp_login); a program server is taken out of each
  // agent's config and put back (two switches, one after the other), so the agent starts it fresh next time.
  const reconnectAgents = (entry) => Object.keys(entry.agents || {}).filter((k) => entry.agents[k] === "on" && SWITCHABLE.mcp.includes(k));

  async function sendSwitch(entry, agent, enabled) {
    const r = await deps.request("/v1/agent_tools/requests", { method: "POST", body: JSON.stringify({ scope: deps.state.scope, agent, kind: "mcp", name: entry.name, enabled }) });
    deps.state.data.tools.requests = r.agent_tools?.requests || deps.state.data.tools.requests;
    const mine = (r.agent_tools?.requests || []).findLast((q) => q.kind === "mcp" && q.agent === agent && q.name === entry.name && q.enabled === enabled);
    if (!mine) throw new Error("요청을 찾지 못했어요");
    const end = await waitRequest(mine.id, (q) => q.status === "done" || q.status === "failed", 120);
    if (end.status === "failed") throw new Error(end.error || "실패");
  }

  function reconnectTool(entry) {
    const on = reconnectAgents(entry);
    if (entry.transport !== "stdio") return toolAgentPopup(entry, "mcp", on.find((k) => needsLogin(entry, k)) || on[0], true);
    const names = on.map(value => deps.agentName(value)).join("·");
    let busy = false;
    applyPopup(`다시 연결 · ${entry.name}`, "MCP 서버", [
      `${names} 설정에서 ${entry.name}을 뺐다가 다시 넣어요. 설정 내용은 그대로예요.`,
      "한 번에 1분쯤 걸려요. 끝나면 에이전트를 새로 열어야 다시 붙어요."
    ], (dlg, show) => {
      const go_ = deps.button("다시 연결", "primary", null, async () => {
        if (busy) return;
        busy = true;
        go_.disabled = true;
        try {
          for (const k of on) {
            show(`${deps.agentName(k)}에서 빼는 중…`);
            await sendSwitch(entry, k, false);
            show(`${deps.agentName(k)}에 다시 넣는 중…`);
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
    const box = deps.el("div", "cn-sublist");
    for (const it of list) {
      const r = deps.el("div", "cn-subrow");
      if (it.label) r.append(deps.el("span", "cn-kindtag", it.label));
      const t = deps.el("div", "cn-subrow-body");
      t.append(deps.raw(deps.el("code", "", it.name)));
      if (it.desc) t.append(deps.raw(deps.el("small", "", it.desc)));
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
      : deps.el("p", "cn-note", "서버가 아직 이 플러그인 폴더를 읽지 않았어요."));
    return s;
  }

  // 12 도구: what the server lists, ones that change or send something first; 최근 호출: today's calls in the transcripts.
  function serverTools(entry) {
    const tools = [...(entry.tools || [])].sort((a, b) => Number(!!b.risky) - Number(!!a.risky));
    const risky = tools.filter((t) => t.risky).length;
    const s = cnSection("제공 도구", tools.length ? `${tools.length}개` : "");
    if (!tools.length) {
      const why = entry.transport === "stdio" ? "서버가 하루에 한 번 도구 목록을 물어봐요. 아직 답을 받지 못했어요." : "주소로 연결하는 서버는 로그인이 필요해 도구 목록을 읽지 않아요.";
      s.append(deps.el("p", "cn-note", why));
      return s;
    }
    if (risky) s.append(deps.el("p", "cn-note", `‘확인’ 표시는 바꾸거나 보내는 도구예요 · ${risky}개`));
    const list = itemRows(tools.slice(0, 5));
    s.append(list);
    if (tools.length > 5) {
      const more = deps.button(`${tools.length - 5}개 더 보기`, "cn-link");
      more.addEventListener("click", () => { list.replaceWith(itemRows(tools)); more.remove(); });
      s.append(more);
    }
    return s;
  }

  function serverCalls(entry) {
    const c = entry.calls;
    const s = cnSection("최근 호출", c ? `오늘 ${c.today}번` : "");
    if (!c?.recent?.length) { s.append(deps.el("p", "cn-note", "오늘은 이 서버의 도구를 부르지 않았어요.")); return s; }
    for (const call of c.recent) {
      const r = deps.el("div", "cn-call");
      const who = deps.AGENTS.find(([k]) => k === call.agent);
      if (who) r.append(deps.agentLogo(who[2], who[1]));
      r.append(deps.el("code", "", call.tool), deps.el("span", "cn-grow"), deps.el("span", call.ok ? "cn-muted" : "cn-warn", call.ok ? "성공" : "실패"), deps.el("span", "cn-quiet", deps.ago(call.at)));
      s.append(r);
    }
    return s;
  }

  // 연결된 스킬·스크립트: hub skills whose name or description names this server; their scripts/ files come from the resolved bundle.
  const norm = (t) => String(t || "").toLowerCase().replace(/[^a-z0-9가-힣]+/g, "");
  function toolSkills(entry) {
    const key = norm(entry.name).replace(/mcp$/, "");
    if (key.length < 3) return [];
    return (deps.state.data.skills || []).filter((sk) => sk.status !== "deprecated" && (norm(sk.name).replace(/mcp$/, "") === key || norm(`${sk.name} ${sk.description}`).includes(key)));
  }
  const scriptCache = new Map();
  let scriptContext = "";
  const scriptContextKey = () => JSON.stringify([deps.state.server, deps.state.scope, deps.generation]);
  function skillScripts(sk) {
    const context = scriptContextKey();
    if (context !== scriptContext) { scriptCache.clear(); scriptContext = context; }
    if (!scriptCache.has(sk.id)) {
      scriptCache.set(sk.id, deps.request("/v1/skills/resolve", { method: "POST", body: JSON.stringify({ scope: deps.state.scope, refs: [`${sk.id}@latest-approved`] }) })
        .then((r) => (r.items || r.skills || r.resolved || [r]).flatMap((x) => x?.bundle?.files || []).map((f) => f.path).filter((f) => /(^|\/)scripts\//.test(f)))
        .catch(() => []));
    }
    return scriptCache.get(sk.id).then(paths => context === scriptContextKey() ? paths : []);
  }
  function toolSkillSections(entry) {
    const list = toolSkills(entry);
    if (!list.length) return [];
    const s = cnSection("연결된 스킬", `${list.length}개`);
    s.append(itemRows(list.map((sk) => ({ name: sk.name, desc: sk.description, label: ITEM_KIND.skill }))));
    const sc = cnSection("스크립트");
    sc.hidden = true;
    void Promise.all(list.map((sk) => skillScripts(sk).then((fs) => fs.map((f) => ({ name: f, desc: sk.name, label: "스크립트" }))))).then((rows) => {
      const all = rows.flat();
      if (!all.length) return;
      sc.querySelector("h3 small")?.remove();
      sc.append(itemRows(all));
      sc.hidden = false;
    });
    return [s, sc];
  }
  // 쓰는 워크플로우: workflows with an AI step whose 울타리 or tools name this server or plugin.
  function toolUsers(entry) {
    const uses = (n) => ["ai", "ai_once"].includes(n.type) && `${n.config?.tools || ""} ${n.config?.fence || ""}`.includes(entry.name);
    return (deps.state.data.crew || []).filter((r) => r.crew?.type === "workflow" && (r.crew.graph?.nodes || []).some(uses));
  }

  function toolExperts(entry) {
    const users = toolUsers(entry);
    const s = cnSection("쓰는 워크플로우");
    s.append(users.length ? deps.el("p", "cn-text", users.map((r) => `$${r.crew.handle}`).join(", ")) : deps.el("p", "cn-note", "아직 없어요 · AI 노드의 울타리에 도구 이름을 적으면 여기 보여요"));
    return s;
  }

  return {
    get confirmPop() { return confirmPop; },
    get loadTools() { return loadTools; },
    get openAddServer() { return openAddServer; },
    get pendingTool() { return pendingTool; },
    get pollTools() { return pollTools; },
    get refreshTools() { return refreshTools; },
    get renderTools() { return renderTools; },
    get toolDetail() { return toolDetail; },
    get toolEntries() { return toolEntries; },
    get toolsSummary() { return toolsSummary; }
  };
}
