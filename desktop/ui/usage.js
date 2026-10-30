// @ts-check
// 사용량 (magpie Usage: 개요·요청·세션·시간대), GET /v1/usage/summary. The host collector
// reports count-only rows per agent session; 요청 are routed calls.
// One view serves every menu's 사용량 tab (Figma v4: 198:1490 · 5446 · 1576 · 5532 · 325:35181 · 37545 · 34690);
// state.menu picks what the 개요 puts first: ROUTER models·cost, MEMORY skills·hours, CREW agents, TOOLS tools.
window.ArkismUsage = (ctx) => {
  "use strict";
  const { state, $, el, button, segment, render, request, query, agentLogo, AGENTS, ago } = ctx;

  const TABS = [["overview", "개요"], ["requests", "요청"], ["sessions", "세션"], ["hours", "시간대"], ["decisions", "결정"]];
  const RANGES = [["today", "오늘"], ["7d", "7일"], ["30d", "30일"], ["90d", "90일"], ["all", "전체"]];
  const DAY_METRICS = [["tokens", "토큰"], ["cost_usd", "비용"], ["sessions", "세션"], ["output", "출력"], ["messages", "메시지"]];
  const TOP_BY = [["tokens", "토큰"], ["cost_usd", "비용"], ["active_seconds", "활동"]];
  const SHAPE = ["1~5", "6~15", "16~30", "31~60", "61~120", "121개 넘음"];
  const DAYS = ["월", "화", "수", "목", "금", "토", "일"];
  const S = { tab: "overview", range: "30d", source: "", day: "tokens", top: "tokens", doc: null, state: "idle", error: "", session: null, ov: null, rday: "traffic", flowModel: "" };
  // ROUTER counts only calls that went through the Arkism router (routing_calls: /v1/usage/summary .requests and
  // /v1/routing/overview). Agent sessions read from transcripts are MEMORY·CREW·TOOLS usage, never router usage.
  const OV_RANGE = { today: "24h", "7d": "7d", "30d": "30d" };
  const DECISIONS = { clef: "판정 모델이 고름", fixed: "고정 모델", failover: "장애 때 넘김", "": "—" };
  const decisionLabel = (k) => DECISIONS[k ?? ""] || k;
  const loads = globalThis.ArkismKeep.latest();

  const num = (n) => {
    n = Number(n || 0);
    if (n >= 1e9) return `${(n / 1e9).toFixed(2)}B`;
    if (n >= 1e6) return `${(n / 1e6).toFixed(n >= 1e7 ? 0 : 1)}M`;
    if (n >= 1e3) return `${(n / 1e3).toFixed(n >= 1e4 ? 0 : 1)}K`;
    return String(Math.round(n));
  };
  const usd = (n, priced = true) => (!priced && !n ? "—" : n >= 100 ? `≈$${Math.round(n).toLocaleString("en-US")}` : `≈$${Number(n || 0).toFixed(2)}`);
  const dur = (s) => { s = Math.round(s || 0); const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60); return h >= 10 ? `${h}시간` : h ? `${h}시간 ${m}분` : `${m}분`; };
  const pct = (a, b) => (b ? `${Math.round((a / b) * 1000) / 10}%` : "—");
  const agentName = (id) => AGENTS.find(([k]) => k === id)?.[1] || id;
  const logo = (id) => { const a = AGENTS.find(([k]) => k === id); if (a) return agentLogo(a[2], a[1]); const t = el("span", "agent-tile mono-logo", (id || "?").slice(0, 1).toUpperCase()); t.title = id; return t; };
  const unsupported = (e) => /HTTP 404|not_found|HTTP 501|not_implemented/.test(e?.message || "");
  const raw = (n) => { n.dataset.noI18n = ""; return n; };
  // 화면 문구가 데이터와 한 줄에 섞여 raw()로 감쌀 때: 만들 때 번역한다 (한국어 화면이면 그대로).
  const tr = (s) => (window.ArkismI18n ? window.ArkismI18n.translate(s) : s);
  const menu = () => (["memory", "agent", "router", "connect"].includes(state.menu) ? state.menu : "router");
  const tabs = () => (menu() === "router" ? TABS.filter(([k]) => k === "overview" || k === "requests" || k === "decisions") : TABS.filter(([k]) => k !== "requests"));
  const loadKey = () => (menu() === "router" ? `router|${S.range}` : `sessions|${S.range}|${S.source}`);
  const modelLabel = (id) => S.ov?.models?.find((m) => m.id === id)?.name || String(id || "").replace(/^[^/]+\//, "");
  const days = () => Math.max(1, { today: 1, "7d": 7, "30d": 30, "90d": 90 }[S.range] || S.doc?.by_day?.length || 1);

  async function load(silent = false) {
    const quiet = silent === true;
    if (!quiet) DEC.key = ""; // 새로고침 also asks for the decision counts again
    if (quiet && S.state !== "ready") return;
    const ticket = loads.begin(), key = loadKey();
    if (!quiet) { S.state = "loading"; S.error = ""; paint(); }
    try {
      const router = menu() === "router", ovRange = OV_RANGE[S.range];
      const [doc, ov] = await Promise.all([request(`/v1/usage/summary?${query({ range: S.range, ...(S.source && !router ? { source: S.source } : {}) })}`),
        router && ovRange ? request(`/v1/routing/overview?${query({ range: ovRange })}`).catch(() => null) : Promise.resolve(null)]);
      if (!ticket.live() || (quiet && ctx.canRefresh?.() === false)) return;
      if (doc?.authority !== "data_only_untrusted" || !doc.kpi) throw new Error("invalid_response");
      const next = ov?.authority === "data_only_untrusted" ? ov : null;
      const same = JSON.stringify(S.doc) === JSON.stringify(doc) && JSON.stringify(S.ov) === JSON.stringify(next);
      S.loadedFor = key;
      if (quiet && same) return;
      S.doc = doc; S.ov = next; S.state = "ready";
    } catch (e) {
      if (!ticket.live() || quiet) return;
      S.doc = null; S.state = unsupported(e) ? "unsupported" : "error"; S.error = e.message;
    }
    paint();
  }
  const paint = () => { if (state.view === "usage") render(false); };

  // ------------------------------------------------------------------ parts
  function card(title, aside, ...kids) {
    const c = el("section", "v4u-card");
    if (title) { const h = el("div", "v4u-card-head"); h.append(el("h3", "", title)); if (aside) h.append(typeof aside === "string" ? el("span", "v4u-aside", aside) : aside); c.append(h); }
    c.append(...kids);
    return c;
  }
  function kpi(label, value, sub, tone) { const k = el("div", `v4u-kpi${tone ? ` ${tone}` : ""}`); k.append(el("p", "", label), el("strong", "", value)); if (sub) k.append(el("small", "", sub)); return k; }
  function kpiRow(items) { const r = el("div", "v4u-kpis"); r.append(...items.map((a) => kpi(...a))); return r; }
  function bars(items, valueOf, labelOf, sideOf, valueText = (it) => num(valueOf(it))) {
    const box = el("div", "v4u-bars"), max = Math.max(1, ...items.map(valueOf));
    for (const it of items) {
      const row = el("div", "v4u-bar-row"), bar = el("span", "v4u-bar"), fill = el("i");
      fill.style.width = `${Math.max(1, (valueOf(it) / max) * 100)}%`; bar.append(fill);
      const name = raw(el("span", "v4u-bar-name", labelOf(it))); name.title = labelOf(it);
      row.append(name, bar, el("span", "v4u-num", valueText(it)), el("span", "v4u-side", sideOf ? sideOf(it) : ""));
      box.append(row);
    }
    if (!items.length) box.append(el("p", "v4u-empty", "아직 기록이 없어요."));
    return box;
  }
  function columns(points, valueOf, labelOf, fmt = num) {
    const box = el("div", "v4u-cols"), max = Math.max(1, ...points.map(valueOf));
    const step = Math.max(1, Math.ceil(points.length / 8));
    points.forEach((p, i) => {
      const c = el("div", `v4u-col${valueOf(p) === max ? " top" : ""}`), b = el("i");
      b.style.height = `${Math.max(1, (valueOf(p) / max) * 100)}%`;
      c.title = `${labelOf(p)} · ${fmt(valueOf(p))}`;
      c.append(b, el("small", "", i % step === 0 ? labelOf(p) : ""));
      box.append(c);
    });
    if (!points.length) box.append(el("p", "v4u-empty", "아직 기록이 없어요."));
    return box;
  }
  function heatmap(hours) {
    const box = el("div", "v4u-heat"), max = Math.max(1, ...hours);
    DAYS.forEach((d, di) => {
      box.append(el("span", "v4u-heat-day", d));
      for (let h = 0; h < 24; h++) {
        const n = hours[di * 24 + h] || 0, c = el("i");
        c.style.setProperty("--a", n ? 0.18 + 0.82 * (n / max) : 0);
        c.title = `${d} ${h}시 · 메시지 ${n}`;
        box.append(c);
      }
    });
    box.append(el("span", ""), ...Array.from({ length: 24 }, (_, h) => el("small", "v4u-heat-hour", h % 6 === 0 ? `${h}시` : "")));
    return box;
  }
  const busiest = (hours) => { let b = 0; hours.forEach((n, i) => { if (n > hours[b]) b = i; }); return hours[b] ? b : -1; };
  function sessionRow(r, metric) {
    const row = el("button", "v4u-session"); row.type = "button";
    const t = el("div", "v4u-session-text");
    t.append(raw(el("b", "", r.title || "(제목 없음)")), el("small", "", [r.project || "", agentName(r.agent), (r.models || []).join(", "), r.ended_at ? ago(r.ended_at) : ""].filter(Boolean).join(" · ")));
    const v = metric === "cost_usd" ? usd(r.cost_usd, r.priced) : metric === "active_seconds" ? dur(r.active_seconds) : num(r.tokens);
    const side = el("div", "v4u-session-side");
    side.append(el("strong", "", v), el("small", "", metric === "cost_usd" ? `${num(r.tokens)} 토큰` : `${num(r.input)} 입력 · ${num(r.output)} 출력 · ${num(r.cached)} 캐시`));
    row.append(logo(r.agent), t, side);
    row.title = "누르면 이 세션의 사용량을 봐요";
    row.addEventListener("click", () => { S.session = r; $("#table").scrollTop = 0; render(); });
    return row;
  }
  function link(text, onClick) { const b = el("button", "v4u-link", text); b.type = "button"; b.addEventListener("click", onClick); return b; }
  const toSessions = () => link("세션 목록 보기 ›", () => ctx.openSession?.());
  function facts(rows) { const dl = el("dl", "v4u-facts"); for (const [k, v] of rows) if (v) dl.append(el("dt", "", k), el("dd", "", v)); return dl; }
  function sideSection(title, aside, ...kids) { const s = el("section", "v4u-sec"); const h = el("div", "v4u-sec-head"); h.append(el("h3", "", title)); if (aside) h.append(el("span", "", aside)); s.append(h, ...kids); return s; }
  const toolServer = (name) => (/^mcp__([^_]+(?:_[^_]+)*?)__/.exec(name) || [])[1] || "";
  const toolName = (name) => name.replace(/^mcp__.+?__/, "");
  function dayCard(d, title = "날짜별") {
    const metric = DAY_METRICS.find(([m]) => m === S.day) || DAY_METRICS[0];
    const seg = segment(DAY_METRICS.slice(0, 3).concat(DAY_METRICS.slice(3).filter(([m]) => m === S.day)), S.day, (m) => { S.day = m; render(false); });
    seg.classList.add("v4u-seg");
    return card(title, seg, columns(d.by_day, (p) => p[metric[0]] || 0, (p) => p.date.slice(5), metric[0] === "cost_usd" ? usd : num));
  }

  // ------------------------------------------------------------------ 개요: what comes first depends on the menu
  function overview(d) {
    const k = d.kpi;
    if (menu() === "memory") return memoryOverview(d);
    if (menu() === "agent") {
      const list = el("div", "v4u-rows");
      for (const a of d.by_agent) {
        const r = el("div", "v4u-agent-row");
        const t = el("div", "v4u-session-text"); t.append(el("b", "", agentName(a.name)), el("small", "", `토큰 ${num(a.tokens)} · ${usd(a.cost_usd, a.priced)}`));
        const side = el("div", "v4u-session-side"); side.append(el("strong", "", `${num(a.sessions)}세션`), el("small", "v4u-accent", `세션당 ${num(a.tokens / Math.max(1, a.sessions))} 토큰`));
        r.append(logo(a.name), t, side);
        list.append(r);
      }
      if (!d.by_agent.length) list.append(el("p", "v4u-empty", "이 기간에 기록된 세션이 없어요."));
      return [kpiRow([["세션", num(k.sessions), `하루 평균 ${num(k.sessions / days())}`], ["토큰", num(k.tokens), `${num(k.input)} 입력 · ${num(k.output)} 출력`],
        ["비용", usd(k.cost_usd, k.priced), `세션당 ${usd(k.cost_usd / Math.max(1, k.sessions), k.priced)}`], ["활동 시간", dur(k.active_seconds), `${k.active_days}일 동안`]]),
        dayCard(d), card("에이전트별", `${d.by_agent.length}개`, list)];
    }
    if (menu() === "connect") return toolsOverview(d);
    return routerOverview(d);
  }

  // ------------------------------------------------------------------ TOOLS 개요 (325:34690): MCP 호출의 응답 시간·실패 (tool_health, Claude Code 기록에서
  // tool_use와 그 결과를 짝지은 것), 서버 상태 (agent_tools 보고), 많이 쓴 도구, 최근 실패, 저장소 활동 (code_reviews 보고).
  const SLOW_MS = 1500;
  const CALL_ERRS = { timeout: "시간 초과", permission: "권한 없음", auth: "인증 만료", not_found: "찾을 수 없음", conflict: "충돌", rate_limit: "한도 초과", error: "오류" };
  const sec = (ms) => (ms == null ? "—" : `${(ms / 1000).toFixed(1)}초`);
  const normServer = (n) => String(n || "").replace(/[^A-Za-z0-9_-]/g, "_");
  // 지난달보다 +6%: the same count over the window of equal length just before (tool_calls_change_pct, nil when it is empty)
  const PREV_WINDOW = { today: (p) => `어제보다 ${p}`, "7d": (p) => `지난주보다 ${p}`, "30d": (p) => `지난달보다 ${p}`, "90d": (p) => `이전 90일보다 ${p}` };
  const median = (xs) => { const v = xs.filter((x) => x != null).sort((a, b) => a - b); return v.length ? v[Math.floor(v.length / 2)] : null; };
  // The chart's one-line reading (325:34690): the slowest day, the server that slowed most against its own usual p95, and the usual day.
  function healthNote(days) {
    const timed = days.filter((p) => p.p95_ms != null);
    if (timed.length < 2) return null;
    const peak = timed.reduce((a, b) => (b.p95_ms > a.p95_ms ? b : a)), rest = timed.filter((p) => p !== peak);
    const usualMs = median(rest.map((p) => p.p95_ms)), usual = Math.max(0.5, Math.round(usualMs / 500) / 2);
    if (peak.p95_ms < usualMs * 1.5) return [`날마다 ${usual}초 안팎이에요.`];
    const date = peak.date.slice(5), moreFails = peak.failed > 2 * Math.max(1, median(rest.map((p) => p.failed)));
    const slow = (peak.servers || []).filter((x) => x.p95_ms != null)
      .map((x) => [x.name, x.p95_ms / (median(rest.map((p) => p.servers?.find((y) => y.name === x.name)?.p95_ms)) || x.p95_ms)])
      .sort((a, b) => b[1] - a[1]).find(([, r]) => r >= 1.5)?.[0];
    const head = slow ? (moreFails ? `${date} ${slow} 서버가 느려지며 실패가 함께 늘었어요.` : `${date} ${slow} 서버가 느려졌어요.`)
      : moreFails ? `${date}에 응답이 가장 느렸고 실패도 많았어요.` : `${date}에 응답이 가장 느렸어요.`;
    return [head, `나머지 날은 ${usual}초 안팎이에요.`];
  }
  const rangeLabel = () => RANGES.find(([x]) => x === S.range)?.[1] || "";
  const stamp = (t) => { const x = new Date(t), p2 = (n) => String(n).padStart(2, "0"); return `${p2(x.getMonth() + 1)}-${p2(x.getDate())} ${p2(x.getHours())}:${p2(x.getMinutes())}`; };
  // One row per MCP server: what agent_tools lists (login state) joined with the calls tool_health measured.
  function mcpServers(h) {
    const listed = state.data?.tools?.servers || [], rows = new Map();
    for (const s of listed) rows.set(normServer(s.name), { name: s.name, id: s.name, login: Object.values(s.login || {}).includes("needed"), calls: 0, failed: 0, p95_ms: null });
    for (const x of h?.servers || []) rows.set(x.name, { ...(rows.get(x.name) || { name: x.name, login: false }), calls: x.calls, failed: x.failed, p95_ms: x.p95_ms });
    return [...rows.values()].sort((a, b) => b.calls - a.calls || a.name.localeCompare(b.name));
  }
  const serverState = (r) => (r.login ? ["로그인 필요", "amber"] : r.p95_ms != null && r.p95_ms >= SLOW_MS ? ["느림", "amber"] : r.calls ? ["정상", ""] : ["호출 없음", "soft"]);
  const openServer = (r) => { state.toolTab = "mcp"; ctx.go?.("tools"); if (r.id) { state.selected = `tool:${r.id}`; render(); } };
  function toolsOverview(d) {
    const h = d.tool_health || null, servers = mcpServers(h);
    const calls = d.tool_calls ?? d.tools.reduce((a, s) => a + s.count, 0), kinds = d.tool_kinds ?? d.tools.length;
    const change = d.tool_calls_change_pct, prev = PREV_WINDOW[S.range];
    const rate = h?.calls ? (h.failed / h.calls) * 100 : null;
    const slowest = servers.filter((r) => r.p95_ms != null).sort((a, b) => b.p95_ms - a.p95_ms)[0];
    const login = servers.filter((r) => r.login);
    const loginKpi = kpi("로그인 필요", `${login.length}개`, login.map((r) => r.name).join(" · ") || "없음", login.length ? "amber" : "");
    if (login.length) { const go = link("로그인 →", () => openServer(login[0])); go.classList.add("v4u-kpi-link"); loginKpi.append(go); }
    const kp = el("div", "v4u-kpis");
    kp.append(kpi("도구 호출", `${num(calls)}회`, change != null && prev ? `${rangeLabel()} · ${prev(`${change > 0 ? "+" : ""}${change}%`)}` : `${rangeLabel()} · 도구 ${num(kinds)}개`),
      kpi("실패율", rate === null ? "—" : `${Math.round(rate * 10) / 10}%`, !h?.calls ? "응답 시간을 잰 MCP 호출이 없어요" : rate > 1 ? `실패 ${num(h.failed)}회 · 기준 1% 넘음` : `실패 ${num(h.failed)}회 · MCP 호출 ${num(h.calls)}회 중`, rate > 1 ? "amber-text" : ""),
      kpi("p95 응답", sec(h?.p95_ms), !h?.calls ? "응답 시간을 잰 MCP 호출이 없어요" : slowest ? `평균 ${sec(h.avg_ms)} · 가장 느림 ${slowest.name}` : `평균 ${sec(h.avg_ms)}`), loginKpi);
    return [kp, timingCard(h), serverCard(servers), two(topToolsCard(d.tools, kinds), failsCard(h)), repoCard()];
  }
  // 응답 시간과 실패: p95 line per day over the failure count per day.
  function timingCard(h) {
    const days = h?.by_day || [];
    const legend = el("span", "v4u-legend-line");
    legend.append(el("span", "v4u-aside", days.length ? `${rangeLabel()} · ${days[0].date.slice(5)} ~ ${days[days.length - 1].date.slice(5)}` : rangeLabel()), el("i", "v4u-key-line"), el("span", "", "p95 응답"), el("i", "v4u-key-bar"), el("span", "", "실패 수"));
    if (!days.some((p) => p.calls)) return card("응답 시간과 실패", legend, el("p", "v4u-empty", h ? "이 기간에 응답 시간을 잰 MCP 호출이 없어요." : "이 서버는 아직 도구 응답 시간을 보고하지 않아요."));
    const n = days.length, top = Math.max(1, Math.ceil(Math.max(...days.map((p) => p.p95_ms || 0)) / 1000)), fmax = Math.max(1, ...days.map((p) => p.failed));
    const x = (i) => ((i + 0.5) / n) * 100, y = (ms) => 100 - (ms / (top * 1000)) * 100;
    // a day without calls breaks the line
    let line = "", area = "", run = [];
    const flush = () => { if (!run.length) return; line += run.map(([i, ms], k) => `${k ? "L" : "M"}${x(i).toFixed(2)} ${y(ms).toFixed(2)}`).join(" ") + " "; area += `M${x(run[0][0]).toFixed(2)} 100 ` + run.map(([i, ms]) => `L${x(i).toFixed(2)} ${y(ms).toFixed(2)}`).join(" ") + ` L${x(run[run.length - 1][0]).toFixed(2)} 100 Z `; run = []; };
    days.forEach((p, i) => { if (p.p95_ms == null) flush(); else run.push([i, p.p95_ms]); });
    flush();
    const plot = el("div", "v4u-rt-plot");
    for (const v of [top, top / 2, 0]) { const g = el("div", "v4u-rt-grid"); g.style.top = `${y(v * 1000)}%`; g.append(el("small", "", `${Math.round(v * 10) / 10}초`)); plot.append(g); }
    const ns = "http://www.w3.org/2000/svg", svg = document.createElementNS(ns, "svg");
    svg.setAttribute("viewBox", "0 0 100 100"); svg.setAttribute("preserveAspectRatio", "none"); svg.setAttribute("aria-hidden", "true");
    for (const [cls, dd] of [["v4u-rt-area", area], ["v4u-rt-line", line]]) { const path = document.createElementNS(ns, "path"); path.setAttribute("class", cls); path.setAttribute("d", dd); svg.append(path); }
    const peak = days.reduce((b, p, i) => (p.p95_ms != null && (b < 0 || p.p95_ms > days[b].p95_ms) ? i : b), -1);
    const box = el("div", "v4u-rt-area-box"); box.append(svg);
    const dot = el("i", "v4u-rt-dot"); dot.style.left = `${x(peak)}%`; dot.style.top = `${y(days[peak].p95_ms)}%`;
    const tag = el("small", "v4u-rt-tag", `${sec(days[peak].p95_ms)} · 실패 ${days[peak].failed} (${days[peak].date.slice(5)})`); tag.style.left = `${x(peak)}%`; tag.style.top = `${y(days[peak].p95_ms)}%`;
    box.append(dot, tag); plot.append(box);
    const bars = el("div", "v4u-rt-bars"), fails = el("div", "v4u-rt-fails");
    fails.append(el("small", "v4u-rt-axis", String(fmax)), el("small", "v4u-rt-axis zero", "0"));
    days.forEach((p, i) => { const b = el("i", i === peak ? "top" : ""); b.style.height = `${p.failed ? Math.max(4, (p.failed / fmax) * 100) : 0}%`; b.title = `${p.date} · 호출 ${p.calls} · 실패 ${p.failed} · p95 ${sec(p.p95_ms)}`; bars.append(b); });
    fails.append(bars);
    const xs = el("div", "v4u-rt-x"), step = Math.max(1, Math.ceil(n / 5));
    days.forEach((p, i) => { if (i % step === 0) { const t = el("small", "", p.date.slice(5)); t.style.left = `${x(i)}%`; xs.append(t); } });
    const note = el("p", "v4u-rt-note"); (healthNote(days) || []).forEach((t, i) => note.append(i ? " " : "", el("span", "", t)));
    return card("응답 시간과 실패", legend, plot, fails, xs, note, el("p", "v4u-hint", "Claude Code 기록에서 MCP 도구를 부른 때부터 결과가 온 때까지 재요. 사용자가 거절한 호출은 세지 않아요."));
  }
  function serverCard(servers) {
    const t = el("div", "v4u-table v4u-mcp");
    const head = el("div", "v4u-tr th"); ["서버", "호출", "실패율", "p95 응답", "상태"].forEach((x) => head.append(el("span", "", x))); t.append(head);
    for (const r of servers) {
      const row = el("button", "v4u-tr v4u-row-btn"); row.type = "button"; row.title = "서버 설정으로 가요";
      const name = el("span", "v4u-srv"); name.append(window.ArkismBrand?.mark(r.name, "v4u-srv-logo") || el("span", "v4u-srv-letter", r.name.slice(0, 1).toUpperCase()), raw(el("span", "v4u-mono", r.name)));
      const fr = r.calls ? (r.failed / r.calls) * 100 : null, [label, tone] = serverState(r);
      row.append(name, el("span", "v4u-num", r.calls.toLocaleString("en-US")), el("span", `v4u-num${fr > 2 ? " amber" : ""}`, fr === null ? "—" : `${fr.toFixed(1)}%`),
        el("span", `v4u-num${r.p95_ms >= SLOW_MS ? " amber" : ""}`, sec(r.p95_ms)), el("span", ""));
      row.children[row.children.length - 1].append(el("span", `v4u-badge${tone ? ` ${tone}` : ""}`, label));
      row.addEventListener("click", () => openServer(r));
      t.append(row);
    }
    if (!servers.length) t.append(el("p", "v4u-empty", "서버가 보고한 MCP 서버가 없어요."));
    return card("MCP 서버 상태", `${servers.length}개 · 행을 누르면 서버 설정으로 가요`, t);
  }
  function topToolsCard(tools, kinds) {
    const list = tools.filter((x) => toolServer(x.name)).slice(0, 6), box = el("div", "v4u-top"), max = Math.max(1, ...list.map((x) => x.count));
    for (const x of list) {
      const row = el("div", "v4u-top-row"), bar = el("span", "v4u-bar"), fill = el("i"); fill.style.width = `${(x.count / max) * 100}%`; bar.append(fill);
      row.append(raw(el("span", "v4u-mono", toolName(x.name))), raw(el("span", "v4u-side-l", toolServer(x.name))), bar, el("span", "v4u-num", x.count.toLocaleString("en-US")));
      box.append(row);
    }
    if (!list.length) box.append(el("p", "v4u-empty", "이 기간에 기록된 MCP 도구 호출이 없어요."));
    return card("많이 쓴 도구", `상위 ${list.length} · 도구 ${num(kinds)}개`, box);
  }
  function failsCard(h) {
    const box = el("div", "v4u-rows");
    for (const f of h?.fails || []) {
      const row = el("div", "v4u-fail"), t = el("div", "v4u-session-text");
      const why = tr(CALL_ERRS[f.err] || f.err) + (f.err === "timeout" && f.ms ? ` (${tr(`${Math.round(f.ms / 1000)}초`)})` : "");
      t.append(raw(el("b", "v4u-mono", toolName(f.tool))), raw(el("small", "", `${toolServer(f.tool)} · ${why}`)));
      row.append(t, el("span", "v4u-time", stamp(f.at)));
      box.append(row);
    }
    if (!h?.fails?.length) box.append(el("p", "v4u-empty", "이 기간에 실패한 MCP 호출이 없어요."));
    return card("최근 실패", h?.failed ? `${rangeLabel()} 실패 ${num(h.failed)}회` : "", box);
  }
  // 저장소 활동: the PRs the code review collector reported (GET /v1/code_reviews), counted over the range by when the review ran.
  function repoCard() {
    const dayCount = { today: 1, "7d": 7, "30d": 30, "90d": 90 }[S.range], since = dayCount ? Date.now() - dayCount * 864e5 : 0;
    const items = (state.data?.reviews?.items || []).filter((it) => !since || new Date(it.started_at).getTime() >= since);
    const aside = el("span", "v4u-legend-line");
    aside.append(el("span", "v4u-aside", `${rangeLabel()} · 저장소 ${new Set(items.map((it) => it.repo)).size}개`), link("저장소로 →", () => ctx.go?.("reviews")));
    if (!items.length) return card("저장소 활동", aside, el("p", "v4u-empty", "이 기간에 리뷰한 PR이 없어요."));
    const merged = items.filter((it) => it.pr_state === "merged").length, reviewed = items.filter((it) => it.state === "done");
    const tiles = el("div", "v4u-repo");
    for (const [n, unit, sub] of [[items.length, "PR", `열림 ${items.filter((it) => it.pr_state === "open").length}`], [merged, "병합", `병합률 ${pct(merged, items.length)}`],
      [items.reduce((a, it) => a + (it.commits || 0), 0), "커밋", "리뷰한 PR의 커밋"], [reviewed.length, "AI 리뷰", `고칠 점 ${reviewed.reduce((a, it) => a + it.findings.length, 0)}개`]]) {
      const c = el("div", "v4u-repo-tile"), v = el("div", "");
      v.append(el("strong", "", num(n)), el("span", "", unit)); c.append(v, el("small", "", sub)); tiles.append(c);
    }
    return card("저장소 활동", aside, tiles);
  }

  // ------------------------------------------------------------------ ROUTER 개요: routed calls only (198:1490 · 342:35440)
  function routerEmpty() {
    const c = el("section", "v4u-card v4u-none");
    const act = el("div", "v4u-actions");
    act.append(button("라우팅 설정 열기", "primary small", null, () => ctx.openSettings?.("routing")), link("라우팅 현황 보기 ›", () => ctx.go?.("routing")));
    c.append(el("h3", "", "아직 라우터를 거친 요청이 없어요"),
      el("p", "v4u-hint", "여기에는 Arkism 라우터가 모델을 골라 보낸 요청만 쌓여요. 라우팅을 켜고 워크플로우·하위 작업이 라우터로 모델을 고르면 요청 수·토큰·비용이 보여요."),
      el("p", "v4u-hint", "에이전트가 직접 쓴 토큰은 MEMORY·FLOW의 사용량에서 봐요."), act);
    return c;
  }
  function routerOverview(d) {
    const r = d.requests;
    if (!r.count) return [routerEmpty()];
    const k = S.ov?.kpi;
    const replaced = r.items.filter((it) => it.replaced).length;
    const parts = [kpiRow([["요청", num(r.count), `성공 ${pct(r.count - r.failed, r.count)} · 실패 ${num(r.failed)}`], ["토큰", num(r.input + r.output), `${num(r.input)} 입력 · ${num(r.output)} 출력`],
      ["비용", usd(r.cost_usd), "공시 단가로 계산"],
      k && k.p95_ms != null ? ["응답 p95", `${(k.p95_ms / 1000).toFixed(1)}초`, `p50 ${((k.p50_ms || 0) / 1000).toFixed(1)}초`] : ["대신 받은 요청", num(replaced), "장애·한도 때 다른 모델로", replaced ? "amber" : ""]])];
    const pts = S.ov?.series?.points || [];
    if (pts.length) {
      const seg = segment([["traffic", "요청"], ["cost", "비용"]], S.rday, (m) => { S.rday = m; render(false); }); seg.classList.add("v4u-seg");
      // Label by the points' own spacing, not the asked range: daily points get a date, sub-day points over several days get date and time.
      const at = (i) => new Date(pts[i]?.t).getTime(), daily = pts.length > 1 && at(1) - at(0) >= 864e5, days = at(pts.length - 1) - at(0) > 864e5;
      const fmtT = (t) => { const x = new Date(t), date = `${x.getMonth() + 1}-${String(x.getDate()).padStart(2, "0")}`; if (daily) return date; const time = x.toLocaleTimeString(globalThis.ArkismI18n?.locale() || "ko-KR", { hour: "2-digit", minute: "2-digit", hour12: false }); return days ? `${date} ${time}` : time; };
      parts.push(card(daily ? "날짜별" : "시간별", seg, columns(pts, (p) => p[S.rday] || 0, (p) => fmtT(p.t), S.rday === "cost" ? usd : num),
        el("p", "v4u-hint", S.rday === "traffic" ? `라우팅 현황 기준 · 막대 = ${S.range === "30d" ? "하루" : S.range === "7d" ? "6시간마다 시간당" : "분당"} 요청` : "라우팅 현황 기준 · 막대 = 그 구간 비용")));
    } else if (!OV_RANGE[S.range]) parts.push(el("p", "v4u-hint", "날짜별 그래프는 오늘·7일·30일에서 볼 수 있어요."));
    parts.push(flowCard(r));
    const models = [...r.models].sort((a, b) => (b.cost_usd || 0) - (a.cost_usd || 0));
    const total = models.reduce((a, m) => a + (m.cost_usd || 0), 0);
    const share = el("div", "v4u-share");
    const stack = el("div", "v4u-stack"), legend = el("div", "v4u-legend");
    const shown = models.slice(0, 3), rest = models.slice(3);
    [...shown, ...(rest.length ? [{ name: `기타 ${rest.length}개`, other: true, cost_usd: rest.reduce((a, m) => a + (m.cost_usd || 0), 0) }] : [])].forEach((m, i) => {
      const w = total ? (m.cost_usd / total) * 100 : 0;
      const seg = el("i", `s${i}`); seg.style.width = `${w}%`; stack.append(seg);
      const l = el("div", "v4u-legend-row"); l.append(el("i", `s${i}`), raw(el("span", "", m.other ? m.name : modelLabel(m.name))), el("span", "v4u-num", `${Math.round(w)}%`)); legend.append(l);
    });
    share.append(el("p", "v4u-hint", `${RANGES.find(([x]) => x === S.range)?.[1] || ""} 비용 ${usd(total)} 중`), stack, legend);
    const t = el("div", "v4u-table v4u-route-table");
    const head = el("div", "v4u-tr th"); ["모델", "요청", "토큰", "비용", "성공률"].forEach((h) => head.append(el("span", "", h))); t.append(head);
    for (const m of models) {
      const row = el("div", "v4u-tr"), o = S.ov?.models?.find((x) => x.id === m.name);
      const ok = typeof o?.success_rate === "number" ? o.success_rate : null;
      row.append(raw(el("span", "v4u-mono", modelLabel(m.name))), el("span", "v4u-num", num(m.requests)), el("span", "v4u-num", num(m.tokens)), el("span", "v4u-num", usd(m.cost_usd, m.priced)),
        el("span", `v4u-num${ok !== null && ok < 98 ? " amber" : ""}`, ok === null ? "—" : `${ok}%`));
      t.append(row);
    }
    const mix = el("div", "v4u-mix"); mix.append(share, t);
    parts.push(card("모델별 비용 비중", "", mix));
    return parts;
  }
  // 라우팅 흐름: how the router decided (Clef·고정·장애 때 넘김·그룹) and which model got each call, from the recent calls the server lists.
  function flowCard(r) {
    const items = r.items;
    const order = [...new Set(r.models.map((m) => m.name).concat(items.map((it) => it.model)))];
    const tone = (m) => `s${Math.min(3, order.indexOf(m))}`;
    if (S.flowModel && !order.includes(S.flowModel)) S.flowModel = "";
    const basis = r.count > items.length ? `최근 ${items.length}건 기준 (전체 ${num(r.count)}건)` : `${num(items.length)}건`;
    const chips = el("div", "v4u-chips");
    for (const [id, label] of [["", "전체 모델"], ...order.map((m) => [m, modelLabel(m)])]) {
      const c = el("button", `v4u-chip${S.flowModel === id ? " on" : ""}`); c.type = "button";
      if (id) c.append(el("i", `v4u-dot ${tone(id)}`));
      c.append(raw(el("span", "", id ? label : tr(label))));
      c.addEventListener("click", () => { S.flowModel = id; render(false); });
      chips.append(c);
    }
    const kids = [chips];
    if (S.flowModel) {
      const mine = items.filter((it) => it.model === S.flowModel), agg = r.models.find((m) => m.name === S.flowModel);
      const byDec = {}; mine.forEach((it) => { byDec[it.decision] = (byDec[it.decision] || 0) + 1; });
      const top = Object.entries(byDec).sort((a, b) => b[1] - a[1])[0];
      const stood = mine.filter((it) => it.replaced).length, gaveUp = items.filter((it) => it.replaced === S.flowModel).length;
      const strip = el("div", "v4u-flow-pick");
      const cell = (a, b) => { const x = el("div", ""); x.append(el("b", "", a), el("small", "", b)); return x; };
      strip.append(raw(el("strong", "", modelLabel(S.flowModel))),
        cell(`${num(agg?.requests ?? mine.length)}건`, `받은 요청 · ${pct(agg?.requests ?? mine.length, r.count)}`),
        cell(top ? `${decisionLabel(top[0])} ${pct(top[1], mine.length)}` : "—", "주로 받은 경로"),
        cell(`${stood}건 · ${gaveUp}건`, "대신 받음 · 다른 모델로 넘김"),
        cell(agg ? usd(agg.cost_usd, agg.priced) : "—", `${RANGES.find(([x]) => x === S.range)?.[1] || ""} 비용`));
      kids.push(strip);
    }
    const groups = {};
    for (const it of items) (groups[it.decision] ||= []).push(it);
    const rows = Object.entries(groups).sort((a, b) => b[1].length - a[1].length);
    const max = Math.max(1, ...rows.map(([, l]) => l.length));
    const table = el("div", "v4u-flow");
    const head = el("div", "v4u-flow-row th"); head.append(el("span", "", "고른 방식"), el("span", "", S.flowModel ? `요청 수 · ${modelLabel(S.flowModel)}만 진하게` : "요청 수"), el("span", "", "받은 모델")); table.append(head);
    for (const [dec, list] of rows) {
      const row = el("div", "v4u-flow-row");
      const name = el("div", "v4u-flow-name"); name.append(raw(el("b", "", tr(decisionLabel(dec)))), el("small", "", `${list.length}건 · ${pct(list.length, items.length)}`));
      const bar = el("div", "v4u-flow-bar"); bar.style.width = `${(list.length / max) * 100}%`;
      const per = {}; list.forEach((it) => { per[it.model] = (per[it.model] || 0) + 1; });
      const got = Object.entries(per).sort((a, b) => b[1] - a[1]);
      for (const [m, n] of got) { const seg = el("i", `${tone(m)}${S.flowModel && S.flowModel !== m ? " dim" : ""}`); seg.style.flex = String(n); seg.title = `${modelLabel(m)} · ${n}건`; bar.append(seg); }
      const wrap = el("div", "v4u-flow-track"); wrap.append(bar);
      const recv = el("div", "v4u-flow-recv");
      for (const [m, n] of got.slice(0, 3)) {
        if (S.flowModel && S.flowModel !== m) continue;
        const x = el("span", ""); x.append(el("i", `v4u-dot ${tone(m)}`), raw(el("b", "", modelLabel(m))), el("small", "", ` ${pct(n, list.length)} · ${n}`)); recv.append(x);
      }
      if (!recv.childNodes.length) recv.append(el("small", "", "—"));
      row.append(name, wrap, recv);
      table.append(row);
    }
    kids.push(table);
    const replaced = items.filter((it) => it.replaced);
    if (replaced.length) {
      const pairs = {}; replaced.forEach((it) => { const k = `${modelLabel(it.replaced)} → ${modelLabel(it.model)}`; pairs[k] = (pairs[k] || 0) + 1; });
      const [pair] = Object.entries(pairs).sort((a, b) => b[1] - a[1]);
      kids.push(el("p", "v4u-flow-note", `다른 모델로 넘김 ${replaced.length}건 · 대부분 ${pair[0]}`));
    }
    return card("라우팅 흐름", `${basis} · 막대 길이 = 요청 수, 색 = 받은 모델`, ...kids);
  }
  // ------------------------------------------------------------------ MEMORY 개요 (325:35181): 기억이 답변에 들어가고 쓰인 정도 (arkism capture가 올린
  // memory_use, 최근 며칠 고정) + 세션 시간대 + 많이 쓴 스킬·도구. A host that has not reported memory_use keeps the session numbers.
  function memoryOverview(d) {
    const k = d.kpi, m = ctx.memory?.() || {};
    const w = m.use;
    const b = busiest(d.hours);
    const skillUses = d.skills.reduce((a, s) => a + s.count, 0);
    const heat = card("세션 시간대 (서울)", b < 0 ? "" : `${num(k.sessions)} 세션 · ${dur(k.active_seconds)} 활동 · 가장 바쁜 때: ${DAYS[Math.floor(b / 24)]}요일 ${b % 24}시`, heatmap(d.hours),
      el("p", "v4u-hint", "에이전트 세션이 열린 시간대예요. 세션 하나하나는 MEMORY › 개요 › 세션 기록에서 볼 수 있어요."));
    const skillCard = card("많이 쓴 스킬", `· ${d.skills.length}`, skillRows(d.skills));
    const toolCard = card("많이 쓴 도구", `· ${d.tools.length}`, bars(d.tools.slice(0, 8), (s) => s.count, (s) => toolName(s.name), (s) => toolServer(s.name) || `${s.sessions}세션`, (s) => `${num(s.count)}회`));
    if (!w) {
      return [kpiRow([["세션", num(k.sessions), `하루 평균 ${num(k.sessions / days())}`], ["활동 시간", dur(k.active_seconds), `${k.active_days}일 동안`],
        ["스킬 사용", num(skillUses), `스킬 ${d.skills.length}개`, "accent"], ["캐시 적중", `${k.cache_hit_pct}%`, `${num(k.cache_read)} 읽기`]]),
        el("p", "v4u-hint", "답변과 기억의 문구 겹침은 PC 수집기가 보고하면 여기에 보여요. 실제 활용 여부는 측정하지 않아요."), heat, two(skillCard, toolCard)];
    }
    const byId = new Map((m.memories || []).map((x) => [x.id, x]));
    const injectedItems = w.items.filter((x) => x.injected > 0);
    const tidy = (m.memories || []).filter((x) => m.often?.(x));
    const rate = w.injections ? Math.round((w.used / w.injections) * 100) : null;
    const tidyKpi = kpi("정리할 기억", `${tidy.length}개`, "답변 문구 겹침 미확인 · 직접 검토", tidy.length ? "amber" : "");
    if (tidy.length) { const go = link("정리하기 →", () => m.openIdle?.()); go.classList.add("v4u-kpi-link"); tidyKpi.append(go); }
    const kp = el("div", "v4u-kpis");
    kp.append(kpi("답변 문구 겹침 비율", rate === null ? "—" : `${rate}%`, `최근 ${w.days}일 · 들어간 ${num(w.injections)}번 중 ${num(w.used)}번`, "accent"),
      kpi("들어간 기억", `${num(w.injections)}회`, `기억 ${num(injectedItems.length)}개 · 하루 ${(w.injections / Math.max(1, w.days)).toFixed(1)}회`), tidyKpi,
      kpi("세션", num(k.sessions), w.inject_sessions ? `기억을 받은 세션 ${num(w.inject_sessions)}` : `하루 평균 ${num(k.sessions / days())}`));
    // 기억 흐름: saved in the window → put into answers → used by the answer.
    const since = Date.now() - w.days * 86400000;
    const saved = (m.memories || []).filter((x) => new Date(x.created_at).getTime() >= since).length;
    const flow = [["저장", saved, `${num(saved)}개`, ""], ["답변에 들어감", injectedItems.length, `${num(w.injections)}회 · 기억 ${num(injectedItems.length)}개`, ""],
      ["문구 겹침 확인", w.items.filter((x) => x.used > 0).length, `${num(w.items.filter((x) => x.used > 0).length)}개`, rate === null ? "" : `${rate}% 문구 겹침`]];
    const fmax = Math.max(1, ...flow.map((f) => f[1]));
    const fbox = el("div", "v4u-bars v4u-flowbars");
    for (const [label, n, text, side] of flow) {
      const row = el("div", "v4u-bar-row"), bar = el("span", "v4u-bar"), fill = el("i"); fill.style.width = `${Math.max(1, (n / fmax) * 100)}%`; bar.append(fill);
      row.append(el("span", "v4u-bar-name", label), bar, el("span", "v4u-num", text), el("span", "v4u-side", side)); fbox.append(row);
    }
    const flowCard = card("기억 흐름", `최근 ${w.days}일 · 실제 활용 여부는 미측정`, fbox);
    // 종류별 문구 겹침: used / injected per kind.
    const kinds = {};
    for (const x of injectedItems) { const kind = byId.get(x.id)?.kind; if (!kind) continue; const a = (kinds[kind] ||= { inj: 0, used: 0, n: 0 }); a.inj += x.injected; a.used += x.used; a.n++; }
    const kindRows = Object.entries(kinds).sort((a, b) => b[1].used / b[1].inj - a[1].used / a[1].inj);
    const kbox = el("div", "v4u-bars");
    for (const [kind, a] of kindRows) {
      const r = Math.round((a.used / Math.max(1, a.inj)) * 100);
      const row = el("div", "v4u-bar-row"), bar = el("span", "v4u-bar"), fill = el("i"); fill.style.width = `${Math.max(1, r)}%`; bar.append(fill);
      row.append(el("span", "v4u-bar-name", m.kindLabel ? m.kindLabel(kind) : kind), bar, el("span", `v4u-num${r < 20 ? " amber" : ""}`, `${r}%`), el("span", "v4u-side", `${a.n}개`)); kbox.append(row);
    }
    if (!kindRows.length) kbox.append(el("p", "v4u-empty", "아직 답변에 들어간 기억이 없어요."));
    const kindCard = card("종류별 문구 겹침", "전달한 기억 중 문구 겹침이 확인된 비율", kbox);
    const tbox = el("div", "v4u-rows");
    for (const x of tidy.slice(0, 5)) {
      const s = w.items.find((y) => y.id === x.id) || { injected: 0, used: 0 };
      const row = el("button", "v4u-tidy"); row.type = "button";
      const t = el("div", "v4u-session-text"); t.append(raw(el("b", "", x.text)), el("small", "", `${s.injected}번 들어감 · 문구 겹침 ${s.used}번`));
      row.append(t, el("span", "v4u-link", "정리"));
      row.addEventListener("click", () => m.openMemory?.(x.id));
      tbox.append(row);
    }
    if (!tidy.length) tbox.append(el("p", "v4u-empty", "정리할 기억이 없어요."));
    const tidyCard = card("정리할 기억", tidy.length ? link("모두 보기 →", () => m.openIdle?.()) : "", el("p", "v4u-hint", `문구 겹침 미확인 · ${tidy.length}개 · 실제 활용 여부는 미측정`), tbox);
    return [kp, flowCard, heat, two(kindCard, tidyCard), two(skillCard, toolCard)];
  }
  // 많이 쓴 스킬 with the agents that ran it (GET /v1/skills/usage by_runtime) when the skill is known.
  function skillRows(list) {
    const box = el("div", "v4u-rows");
    for (const s of list) {
      const row = el("div", "v4u-skill-row");
      const logos = el("span", "v4u-logos");
      for (const a of ctx.skillAgents?.(s.name) || []) logos.append(logo(a));
      row.append(raw(el("span", "v4u-mono", s.name)), logos, el("span", "v4u-num", `${num(s.count)}회`));
      box.append(row);
    }
    if (!list.length) box.append(el("p", "v4u-empty", "아직 기록이 없어요."));
    return box;
  }
  function two(a, b) { const w = el("div", "v4u-two"); w.append(a, b); return w; }

  // ------------------------------------------------------------------ 요청 (routed calls)
  function requests(d) {
    const r = d.requests;
    const replaced = r.items.filter((it) => it.replaced).length;
    const parts = [kpiRow([["요청", num(r.count), r.count ? `성공 ${pct(r.count - r.failed, r.count)}` : ""], ["토큰", num(r.input + r.output), `${num(r.input)} 입력 · ${num(r.output)} 출력`],
      ["비용", usd(r.cost_usd), "공시 단가로 계산"], ["대신 받은 요청", num(replaced), "장애·한도 때 다른 모델로", replaced ? "amber" : ""]])];
    const list = el("div", "v4u-list");
    for (const it of r.items) {
      const row = el("div", "v4u-req");
      const text = el("div", "v4u-session-text");
      text.append(raw(el("b", "", it.replaced ? `${modelLabel(it.replaced)} → ${modelLabel(it.model)}` : modelLabel(it.model))), el("small", "", `${decisionLabel(it.decision)} · ${num(it.input)} / ${it.output ? num(it.output) : "—"} · ${(it.latency_ms / 1000).toFixed(1)}초`));
      const badge = el("span", `v4u-badge${!it.ok ? " amber" : it.replaced ? " amber" : ""}`, !it.ok ? (it.error ? `실패 · ${it.error}` : "실패") : it.replaced ? "대신 받음" : "성공");
      row.append(el("span", "v4u-time", new Date(it.at).toLocaleTimeString(globalThis.ArkismI18n?.locale() || "ko-KR", { hour: "2-digit", minute: "2-digit", hour12: false })), text, badge, el("span", "v4u-num", it.ok && it.cost_usd ? usd(it.cost_usd).replace("≈", "") : "—"));
      list.append(row);
    }
    if (!r.items.length) list.append(el("p", "v4u-empty", "이 기간에 Arkism 라우터를 거친 요청이 없어요. 워크플로우·하위 작업이 라우터로 모델을 고르면 여기에 쌓여요."));
    parts.push(list, el("p", "v4u-hint", `최근 ${r.items.length}건 표시 · 요청 원문은 저장하지 않아요.`));
    return parts;
  }
  function requestsSide(d) {
    const r = d.requests;
    const fails = r.items.filter((it) => !it.ok), replaced = r.items.filter((it) => it.replaced);
    const most = (list, key) => { const m = {}; list.forEach((x) => { m[x[key]] = (m[x[key]] || 0) + 1; }); return Object.entries(m).sort((a, b) => b[1] - a[1])[0]; };
    const f = most(fails, "model"), rp = most(replaced, "model");
    const o = (m) => S.ov?.models?.find((x) => x.id === m.name);
    const speed = S.ov?.kpi;
    return [sideSection("모델별 요청", "", bars(r.models, (m) => m.requests, (m) => modelLabel(m.name), (m) => (o(m)?.p95_ms != null ? `p95 ${(o(m).p95_ms / 1000).toFixed(1)}초` : ""), (m) => num(m.requests))),
      sideSection("실패·대체", "", facts([["실패", `${num(r.failed)}건 · ${pct(r.failed, r.count)}${f ? ` · ${modelLabel(f[0])} ${f[1]}` : ""}`], ["대체", `${replaced.length}건${rp ? ` · 대부분 ${modelLabel(rp[0])}` : ""}`]]), el("p", "v4u-hint", "실패율은 기간 전체, 모델 이름은 최근 기록 기준이에요.")),
      ...(speed && speed.requests ? [sideSection("응답 속도", { today: "오늘", "7d": "7일", "30d": "30일" }[S.range] || "", facts([["p50", speed.p50_ms != null ? `${(speed.p50_ms / 1000).toFixed(1)}초` : "—"], ["p95", speed.p95_ms != null ? `${(speed.p95_ms / 1000).toFixed(1)}초` : "—"], ["성공률", speed.success_rate != null ? `${speed.success_rate}%` : "—"]]))] : []),
      link("라우팅 현황에서 보기 ›", () => ctx.go?.("routing")), button("새로고침", "", null, load)];
  }

  // ------------------------------------------------------------------ 세션
  function sessions(d) {
    const k = d.kpi;
    const parts = [kpiRow([["세션", num(k.sessions), `하루 평균 ${num(k.sessions / days())} · 중앙값 ${num(k.median_tokens)} 토큰`], ["활동 시간", dur(k.active_seconds), `세션당 평균 ${dur(k.active_seconds / Math.max(1, k.sessions))}`],
      ["캐시 읽기", num(k.cache_read), `적중률 ${k.cache_hit_pct}%`, "accent"], ["프로젝트", num(k.projects), k.sessions ? `root ${Math.round((k.root_sessions / k.sessions) * 100)}%` : ""]])];
    parts.push(card(`프로젝트별 · ${d.projects.length}개`, "토큰 · 비용", bars(d.projects.slice(0, 8), (p) => p.tokens, (p) => p.name, (p) => usd(p.cost_usd, p.priced))),
      card(`모델별 · ${d.models.length}개`, "토큰", bars(d.models.slice(0, 8), (m) => m.tokens, (m) => m.name, (m) => usd(m.cost_usd, m.priced))));
    const top = [...d.top_sessions].sort((a, b) => (b[S.top] || 0) - (a[S.top] || 0));
    const seg = segment(TOP_BY, S.top, (m) => { S.top = m; render(false); }); seg.classList.add("v4u-seg");
    const topCard = card("많이 쓴 세션", seg, ...top.map((r) => sessionRow(r, S.top)));
    if (!top.length) topCard.append(el("p", "v4u-empty", "아직 기록이 없어요."));
    const latest = card("최근 세션", toSessions(), ...d.latest.slice(0, 20).map((r) => sessionRow(r, "tokens")));
    if (!d.latest.length) latest.append(el("p", "v4u-empty", "아직 기록이 없어요."));
    parts.push(two(topCard, latest));
    return parts;
  }
  function sessionsSide(d) {
    const total = d.tools.reduce((a, s) => a + s.count, 0);
    const shape = d.shape.map((n, i) => ({ n, l: SHAPE[i] }));
    return [sideSection("세션 길이", "메시지 수", bars(shape, (p) => p.n, (p) => p.l, null, (p) => num(p.n))),
      sideSection("많이 쓴 스킬", "", facts(d.skills.slice(0, 5).map((s) => [s.name, `${num(s.count)}번`]))),
      sideSection("많이 쓴 도구", "", facts(d.tools.slice(0, 5).map((s) => [toolName(s.name), pct(s.count, total)]))),
      toSessions()];
  }

  // ------------------------------------------------------------------ 시간대
  function hoursTab(d) {
    const recent = [...d.by_day].reverse().slice(0, 3);
    const list = el("div", "v4u-days");
    for (const p of recent) { const r = el("div", "v4u-day"); const [, m, dd] = p.date.split("-"); r.append(el("span", "", `${Number(m)}월 ${Number(dd)}일`), el("span", "v4u-num", num(p.tokens)), el("span", "v4u-side", `${num(p.sessions)} 세션`)); list.append(r); }
    if (!recent.length) list.append(el("p", "v4u-empty", "아직 기록이 없어요."));
    return [card("요일 × 시간", "메시지 기준 · 진할수록 많이 씀", heatmap(d.hours)), card("날짜별", `최근 ${recent.length}일`, list)];
  }
  function hoursSide(d) {
    const h = d.hours, sum = (f) => h.reduce((a, n, i) => a + (f(i % 24, Math.floor(i / 24)) ? n : 0), 0), all = sum(() => true);
    const b = busiest(h);
    const share = (f) => pct(sum(f), all);
    const weekday = sum((_, di) => di < 5) / 5, weekend = sum((_, di) => di >= 5) / 2;
    const s = el("section", "v4u-sec");
    s.append(el("h3", "", "가장 바쁜 때"), el("p", "v4u-big", b < 0 ? "기록 없음" : `${DAYS[Math.floor(b / 24)]}요일 ${b % 24}시`),
      facts([["평균", b < 0 ? "" : `그 시간 메시지 ${num(h[b])}개`], ["주로", d.by_agent[0] ? agentName(d.by_agent[0].name) : ""]]));
    return [s, sideSection("일하는 패턴", "", facts([["오전", `9~12시 · ${share((hr) => hr >= 9 && hr < 12)}`], ["오후", `13~18시 · ${share((hr) => hr >= 13 && hr < 18)}`],
      ["밤", `20~2시 · ${share((hr) => hr >= 20 || hr < 2)}`], ["주말", weekday ? `평일의 ${Math.round((weekend / weekday) * 100)}%` : ""]])), toSessions()];
  }

  // ------------------------------------------------------------------ 결정 (#466): what people and the AI decided in the range,
  // counted by the server per day (GET /v1/decisions/summary), so the counts outlive the audit log they come from.
  const DECISION_KINDS = {
    skill_trashed: "스킬을 휴지통으로", skill_restored: "스킬을 되돌림", skill_rolled_back: "스킬 이전 버전으로 복귀",
    skill_version_rejected: "스킬 후보 제외", skill_improvement_rejected: "AI 스킬 개선 제외",
    candidate_approve: "후보 승인", candidate_edit_approve: "고쳐서 승인", candidate_reject: "후보 버림", candidate_expired: "후보 기한 지남",
    dreaming_accept: "정리 제안 받아들임", dreaming_swap: "정리 제안 반대로", dreaming_dismiss: "정리 제안 둘 다 둠", dreaming_forget_both: "정리 제안 둘 다 잊음",
    merge_accept: "병합 제안 받아들임", merge_dismiss: "병합 제안 넘김", conflict_resolved: "충돌 정리",
    wiki_approve: "위키 초안 승인", wiki_reject: "위키 초안 거절", flow_approved: "흐름 승인", flow_rejected: "흐름 거절", flow_cancelled: "흐름 중단",
    skill_routed: "스킬 골라 줌", skill_route_loaded: "고른 스킬을 불러옴", role_model_changed: "역할별 모델 바꿈",
    agent_tool_on: "도구 켜기·설치", agent_tool_off: "도구 끄기·제거", ai_help_accepted: "AI 도움대로 함", ai_help_discarded: "AI 도움과 다르게 함"
  };
  const DECISION_REASONS = { "": "이유 없음", automatic: "자동", pick: "라우터가 고른 것", other: "다른 후보", rules: "규칙", judge: "판정 모델" };
  const DEC_DAYS = { today: 1, "7d": 7, "30d": 30, "90d": 90, all: 366 };
  const DEC = { key: "", rows: [], state: "idle", error: "" };
  async function loadDecisions(key) {
    try {
      const doc = await request(`/v1/decisions/summary?${query({ period: "month", days: DEC_DAYS[key] || 30 })}`);
      if (DEC.key !== key) return;
      if (doc?.authority !== "data_only_untrusted" || !Array.isArray(doc.rows)) throw new Error("invalid_response");
      Object.assign(DEC, { rows: doc.rows, state: "ready" });
    } catch (e) {
      if (DEC.key !== key) return;
      Object.assign(DEC, { state: unsupported(e) ? "unsupported" : "error", error: e.message });
    }
    paint();
  }
  function decisionsTab() {
    if (DEC.key !== S.range) { Object.assign(DEC, { key: S.range, state: "loading" }); const key = S.range; queueMicrotask(() => loadDecisions(key)); }
    if (DEC.state !== "ready") {
      return [el("div", "empty", DEC.state === "unsupported" ? "이 서버는 아직 결정 집계를 지원하지 않아요." : DEC.state === "error" ? `결정 수를 불러오지 못했어요 (${DEC.error}).` : "불러오는 중…")];
    }
    // One row per decision and reason, summed over the range's months.
    const sums = new Map();
    for (const r of DEC.rows) { const k = `${r.kind}\u0000${r.reason_code}`; sums.set(k, { kind: r.kind, reason: r.reason_code, count: (sums.get(k)?.count || 0) + r.count }); }
    const list = el("div", "v4u-list");
    for (const it of [...sums.values()].sort((a, b) => b.count - a.count || a.kind.localeCompare(b.kind))) {
      const row = el("div", "v4u-req"), text = el("div", "v4u-session-text");
      const reason = DECISION_REASONS[it.reason];
      text.append(DECISION_KINDS[it.kind] ? el("b", "", DECISION_KINDS[it.kind]) : raw(el("b", "", it.kind)), reason ? el("small", "", reason) : raw(el("small", "", it.reason)));
      row.append(text, el("span", "v4u-num", num(it.count)));
      list.append(row);
    }
    if (!sums.size) list.append(el("p", "v4u-empty", "이 기간에 센 결정이 없어요."));
    return [list];
  }

  // ------------------------------------------------------------------ 세션 상세 (198:6822): one session's numbers, the dialogue opens in the 세션 기록 window
  function sessionPage(r) {
    const kp = kpiRow([["토큰", num(r.tokens), `입력 ${num(r.input)} · 출력 ${num(r.output)}`], ["비용", usd(r.cost_usd, r.priced), r.priced ? "공시 단가로 계산" : "단가 없음"],
      ["캐시", `${num(r.cached)} 읽기`, ""], ["메시지", num(r.messages), dur(r.active_seconds)]]);
    const used = card("쓴 것", "", facts([["모델", (r.models || []).join(", ") || "—"], ["프로젝트", r.project || "—"], ["장치", r.device || ""], ["에이전트", agentName(r.agent)]]));
    return [kp, used, el("p", "v4u-hint", "이 화면은 사용량만 보여 줘요. 대화 내용은 MEMORY › 개요 › 세션 기록에서 볼 수 있어요.")];
  }
  function sessionSide(r) {
    const open = link("대화 내용 보기 (세션 기록) ›", () => ctx.openSession?.(r.session_id || r.id));
    const act = el("div", "v4u-actions");
    act.append(button("모델 관리", "", null, () => ctx.openSettings?.("models")), button("도구 관리", "", null, () => ctx.go?.("tools")), button("스킬 관리", "", null, () => ctx.go?.("skills")));
    return [sideSection("이 세션 더 보기", "", open), sideSection("관리", "", act)];
  }

  // ------------------------------------------------------------------ view
  function side(title, kids) {
    const body = ctx.detailShell("");
    body.append(...kids);
  }
  function renderView() {
    const table = $("#table");
    table.className = "v4u-wrap";
    $("#stats").replaceChildren();
    $("#chips").replaceChildren();
    if (S.session) {
      $("#controls").replaceChildren();
      const r = S.session;
      const back = el("button", "page-back", "← 사용량 · 세션"); back.type = "button";
      back.addEventListener("click", () => { S.session = null; render(); });
      $(".main > .page-head").prepend(back);
      table.replaceChildren(...sessionPage(r));
      side("세션", sessionSide(r));
      return;
    }
    if (!tabs().some(([k]) => k === S.tab)) S.tab = "overview";
    const controls = el("div", "v4u-controls");
    const tabSeg = segment(tabs(), S.tab, (k) => { S.tab = k; render(); }); tabSeg.classList.add("v4u-box");
    const rangeSeg = segment(RANGES, S.range, (k) => { if (S.range !== k) { S.range = k; void load(); } }); rangeSeg.classList.add("v4u-box");
    controls.append(tabSeg, el("span", "v4u-gap"), rangeSeg);
    if (S.doc?.agents?.length && menu() === "connect") { // 325:34690: agent chips in place of the select
      const agents = segment([["", "전체"], ...S.doc.agents.map((a) => [a.name, agentName(a.name)])], S.source, (v) => { if (S.source !== v) { S.source = v; void load(); } });
      [...agents.children].slice(1).forEach((b, i) => b.prepend(logo(S.doc.agents[i].name)));
      agents.classList.add("v4u-box", "v4u-agents"); agents.setAttribute("aria-label", "에이전트");
      controls.append(agents);
    } else if (S.doc?.agents?.length && S.tab !== "requests" && menu() !== "router") {
      const pick = el("select", "v4u-select"); pick.setAttribute("aria-label", "에이전트");
      for (const [v, l] of [["", "전체"], ...S.doc.agents.map((a) => [a.name, `${agentName(a.name)}${a.all_sessions ? ` ${num(a.all_sessions)}` : ""}`])]) { const o = el("option", "", l); o.value = v; o.selected = v === S.source; pick.append(o); }
      pick.addEventListener("change", () => { S.source = pick.value; void load(); });
      controls.append(pick);
    }
    $("#controls").replaceChildren(controls);
    if (S.state === "idle") { S.state = "loading"; queueMicrotask(load); }
    if (S.state !== "ready") {
      const msg = S.state === "unsupported" ? "이 서버는 아직 사용량을 지원하지 않아요. 서버를 새 버전으로 올리면 여기서 세션·토큰·비용을 볼 수 있어요."
        : S.state === "error" ? `사용량을 불러오지 못했어요 (${S.error}).` : "불러오는 중…";
      const box = el("div", "empty", msg);
      if (S.state === "error") box.append(el("br"), button("다시 시도", "small", "arrows-clockwise", load));
      table.replaceChildren(box);
      window.ArkismModelBudget.mount({ container: table, scope: { ...state.scope }, request, el, button });
      $("#detail").replaceChildren();
      return;
    }
    if (S.loadedFor && S.loadedFor !== loadKey()) queueMicrotask(() => load(true));
    const d = S.doc;
    const at = d.updated_at || d.collected_at;
    const note = el("p", "v4u-hint v4u-at", menu() === "router" ? "Arkism 라우터를 거친 요청만 세요. 에이전트가 직접 쓴 토큰은 MEMORY·FLOW 사용량에서 봐요." : at ? `1분마다 모아요 · 마지막 ${new Date(at).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false })}` : "");
    table.replaceChildren(...(at || menu() === "router" ? [note] : []), ...({ overview, requests, sessions, hours: hoursTab, decisions: decisionsTab }[S.tab])(d));
    if (S.tab === "overview") window.ArkismModelBudget.mount({ container: table, scope: { ...state.scope }, request, el, button });
    if (S.tab === "requests") side("요청", requestsSide(d));
    else if (S.tab === "sessions") side("세션", sessionsSide(d));
    else if (S.tab === "hours") side("시간대", hoursSide(d));
    else $("#detail").replaceChildren();
  }
  const SUBS = {
    memory: "기억이 답변에 얼마나 들어가고 쓰였는지, 스킬·도구와 일한 시간대를 봐요.",
    agent: "에이전트마다 세션·토큰·비용을 얼마나 썼는지 봐요.",
    connect: "에이전트가 쓴 도구가 얼마나 불렸는지 봐요.",
    overview: "Arkism 라우터가 보낸 요청이 어느 모델로 갔고 토큰·비용이 얼마인지 봐요.",
    requests: "라우터를 거친 요청의 토큰·비용을 봐요. 모델별 성능은 라우팅 현황에서 봐요.",
    sessions: "세션을 프로젝트·모델·스킬별로 모아 봐요. 세션 하나하나는 MEMORY › 개요 › 세션 기록에서 봐요.",
    hours: "언제 많이 일하는지 요일·시간별로 봐요.",
    decisions: "사람과 AI가 내린 결정을 종류·이유별로 세요. 감사 기록이 지워져도 수는 남아요."
  };
  const sub = () => {
    if (S.session) { const r = S.session; return [agentName(r.agent), r.device, r.started_at && `${new Date(r.started_at).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false })}–${new Date(r.ended_at).toLocaleTimeString(globalThis.ArkismI18n?.locale() || "ko-KR", { hour: "2-digit", minute: "2-digit", hour12: false })}`].filter(Boolean).join(" · "); }
    return S.tab === "overview" && menu() !== "router" ? SUBS[menu()] : SUBS[S.tab];
  };

  return {
    fmt: { num, usd, dur, logo, agentName, bars },
    agents: () => (S.doc?.agents || []).map((a) => a.name),
    agentCounts: () => (S.doc?.agents || []).map((a) => [a.name, a.all_sessions]),
    load, refresh: () => load(true), count: () => (S.doc?.requests?.count ? num(S.doc.requests.count) : ""), // only the ROUTER 사용량 tab shows it
    reset: () => { loads.cancel(); Object.assign(S, { doc: null, state: "idle", session: null }); },
    leave: () => { S.session = null; },
    views: { usage: { title: () => (S.session ? S.session.title || "(제목 없음)" : "사용량"), sub, render: renderView,
      get keepDetail() { return !!S.session || S.tab !== "overview"; },
      head: () => [button("새로고침", "ghost", "arrows-clockwise", load)] } }
  };
};
