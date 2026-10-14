// 사용량 (magpie Usage: 개요·요청·세션·시간대), GET /v1/usage/summary. The host collector
// reports count-only rows per agent session; 요청 are routed calls.
// One view serves every menu's 사용량 tab (Figma v4: 198:1490 · 5446 · 1576 · 5532 · 325:35181 · 37545 · 34690);
// state.menu picks what the 개요 puts first: ROUTER models·cost, MEMORY skills·hours, CREW agents, TOOLS tools.
window.ArkismUsage = (ctx) => {
  "use strict";
  const { state, $, el, button, segment, render, request, query, agentLogo, AGENTS, ago } = ctx;

  const TABS = [["overview", "개요"], ["requests", "요청"], ["sessions", "세션"], ["hours", "시간대"]];
  const RANGES = [["today", "오늘"], ["7d", "7일"], ["30d", "30일"], ["90d", "90일"], ["all", "전체"]];
  const DAY_METRICS = [["tokens", "토큰"], ["cost_usd", "비용"], ["sessions", "세션"], ["output", "출력"], ["messages", "메시지"]];
  const TOP_BY = [["tokens", "토큰"], ["cost_usd", "비용"], ["active_seconds", "활동"]];
  const SHAPE = ["1~5", "6~15", "16~30", "31~60", "61~120", "121개 넘음"];
  const DAYS = ["월", "화", "수", "목", "금", "토", "일"];
  const S = { tab: "overview", range: "30d", source: "", day: "tokens", top: "tokens", doc: null, state: "idle", error: "", session: null, ov: null, rday: "traffic", flowModel: "" };
  // ROUTER counts only calls that went through the Arkism router (routing_calls: /v1/usage/summary .requests and
  // /v1/routing/overview). Agent sessions read from transcripts are MEMORY·CREW·TOOLS usage, never router usage.
  const OV_RANGE = { today: "24h", "7d": "7d", "30d": "30d" };
  const DECISIONS = { clef: "Clef가 고름", fixed: "고정 모델", failover: "장애 때 넘김", "": "—" };
  const decisionLabel = (k) => DECISIONS[k ?? ""] || k;
  let epoch = 0;

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
  const tabs = () => (menu() === "router" ? TABS.filter(([k]) => k === "overview" || k === "requests") : TABS.filter(([k]) => k !== "requests"));
  const loadKey = () => (menu() === "router" ? `router|${S.range}` : `sessions|${S.range}|${S.source}`);
  const modelLabel = (id) => S.ov?.models?.find((m) => m.id === id)?.name || String(id || "").replace(/^[^/]+\//, "");
  const days = () => Math.max(1, { today: 1, "7d": 7, "30d": 30, "90d": 90 }[S.range] || S.doc?.by_day?.length || 1);

  async function load(silent = false) {
    const quiet = silent === true;
    if (quiet && S.state !== "ready") return;
    const ticket = ++epoch, key = loadKey();
    if (!quiet) { S.state = "loading"; S.error = ""; paint(); }
    try {
      const router = menu() === "router", ovRange = OV_RANGE[S.range];
      const [doc, ov] = await Promise.all([request(`/v1/usage/summary?${query({ range: S.range, ...(S.source && !router ? { source: S.source } : {}) })}`),
        router && ovRange ? request(`/v1/routing/overview?${query({ range: ovRange })}`).catch(() => null) : Promise.resolve(null)]);
      if (ticket !== epoch || (quiet && ctx.canRefresh?.() === false)) return;
      if (doc?.authority !== "data_only_untrusted" || !doc.kpi) throw new Error("invalid_response");
      const next = ov?.authority === "data_only_untrusted" ? ov : null;
      const same = JSON.stringify(S.doc) === JSON.stringify(doc) && JSON.stringify(S.ov) === JSON.stringify(next);
      S.loadedFor = key;
      if (quiet && same) return;
      S.doc = doc; S.ov = next; S.state = "ready";
    } catch (e) {
      if (ticket !== epoch || quiet) return;
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
  const toSessions = () => link("세션 목록 보기 ›", () => ctx.go?.("sessions"));
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
    if (menu() === "connect") {
      const calls = d.tools.reduce((a, s) => a + s.count, 0);
      const servers = [...new Set(d.tools.map((t) => toolServer(t.name)).filter(Boolean))];
      const t = el("div", "v4u-table v4u-tools");
      const head = el("div", "v4u-tr th"); ["도구", "서버", "", "호출", "세션"].forEach((h) => head.append(el("span", "", h))); t.append(head);
      const max = Math.max(1, ...d.tools.map((x) => x.count));
      for (const x of d.tools) {
        const row = el("div", "v4u-tr"), bar = el("span", "v4u-bar"), fill = el("i"); fill.style.width = `${(x.count / max) * 100}%`; bar.append(fill);
        row.append(raw(el("span", "v4u-mono", toolName(x.name))), el("span", "", toolServer(x.name) || "내장"), bar, el("span", "v4u-num", num(x.count)), el("span", "v4u-num", num(x.sessions)));
        t.append(row);
      }
      if (!d.tools.length) t.append(el("p", "v4u-empty", "이 기간에 기록된 도구 호출이 없어요."));
      return [kpiRow([["도구 호출", num(calls), `도구 ${d.tools.length}개`], ["MCP 서버", num(servers.length), servers.slice(0, 3).join(" · ") || "기록 없음"],
        ["세션", num(k.sessions), `하루 평균 ${num(k.sessions / days())}`], ["많이 쓴 도구", d.tools[0] ? toolName(d.tools[0].name) : "—", d.tools[0] ? `${num(d.tools[0].count)}회` : ""]]),
        card("많이 쓴 도구", `상위 ${d.tools.length}개 · 세션에서 센 호출 수`, t)];
    }
    return routerOverview(d);
  }

  // ------------------------------------------------------------------ ROUTER 개요: routed calls only (198:1490 · 342:35440)
  function routerEmpty() {
    const c = el("section", "v4u-card v4u-none");
    const act = el("div", "v4u-actions");
    act.append(button("라우팅 설정 열기", "primary small", null, () => ctx.openSettings?.("routing")), link("라우팅 현황 보기 ›", () => ctx.go?.("routing")));
    c.append(el("h3", "", "아직 라우터를 거친 요청이 없어요"),
      el("p", "v4u-hint", "여기에는 Arkism 라우터가 모델을 골라 보낸 요청만 쌓여요. 라우팅을 켜고 워크플로우·하위 작업이 라우터로 모델을 고르면 요청 수·토큰·비용이 보여요."),
      el("p", "v4u-hint", "에이전트가 직접 쓴 토큰은 MEMORY·CREW의 사용량에서 봐요."), act);
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
      const fmtT = (t) => { const x = new Date(t); return S.range === "30d" ? `${x.getMonth() + 1}-${String(x.getDate()).padStart(2, "0")}` : x.toLocaleTimeString(globalThis.ArkismI18n?.locale() || "ko-KR", { hour: "2-digit", minute: "2-digit", hour12: false }); };
      parts.push(card(S.range === "30d" ? "날짜별" : "시간별", seg, columns(pts, (p) => p[S.rday] || 0, (p) => fmtT(p.t), S.rday === "cost" ? usd : num),
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
      el("p", "v4u-hint", "에이전트 세션이 열린 시간대예요. 세션 하나하나는 MEMORY › 세션에서 볼 수 있어요."));
    const skillCard = card("많이 쓴 스킬", `· ${d.skills.length}`, skillRows(d.skills));
    const toolCard = card("많이 쓴 도구", `· ${d.tools.length}`, bars(d.tools.slice(0, 8), (s) => s.count, (s) => toolName(s.name), (s) => toolServer(s.name) || `${s.sessions}세션`, (s) => `${num(s.count)}회`));
    if (!w) {
      return [kpiRow([["세션", num(k.sessions), `하루 평균 ${num(k.sessions / days())}`], ["활동 시간", dur(k.active_seconds), `${k.active_days}일 동안`],
        ["스킬 사용", num(skillUses), `스킬 ${d.skills.length}개`, "accent"], ["캐시 적중", `${k.cache_hit_pct}%`, `${num(k.cache_read)} 읽기`]]),
        el("p", "v4u-hint", "기억이 답변에 얼마나 쓰였는지는 PC의 Arkism 수집기가 보고하면 여기에 보여요."), heat, two(skillCard, toolCard)];
    }
    const byId = new Map((m.memories || []).map((x) => [x.id, x]));
    const injectedItems = w.items.filter((x) => x.injected > 0);
    const tidy = (m.memories || []).filter((x) => m.often?.(x));
    const rate = w.injections ? Math.round((w.used / w.injections) * 100) : null;
    const tidyKpi = kpi("정리할 기억", `${tidy.length}개`, "자주 들어가지만 답변엔 안 쓰임", tidy.length ? "amber" : "");
    if (tidy.length) { const go = link("정리하기 →", () => m.openIdle?.()); go.classList.add("v4u-kpi-link"); tidyKpi.append(go); }
    const kp = el("div", "v4u-kpis");
    kp.append(kpi("답변에 쓰인 비율", rate === null ? "—" : `${rate}%`, `최근 ${w.days}일 · 들어간 ${num(w.injections)}번 중 ${num(w.used)}번`, "accent"),
      kpi("들어간 기억", `${num(w.injections)}회`, `기억 ${num(injectedItems.length)}개 · 하루 ${(w.injections / Math.max(1, w.days)).toFixed(1)}회`), tidyKpi,
      kpi("세션", num(k.sessions), w.inject_sessions ? `기억을 받은 세션 ${num(w.inject_sessions)}` : `하루 평균 ${num(k.sessions / days())}`));
    // 기억 흐름: saved in the window → put into answers → used by the answer.
    const since = Date.now() - w.days * 86400000;
    const saved = (m.memories || []).filter((x) => new Date(x.created_at).getTime() >= since).length;
    const flow = [["저장", saved, `${num(saved)}개`, ""], ["답변에 들어감", injectedItems.length, `${num(w.injections)}회 · 기억 ${num(injectedItems.length)}개`, ""],
      ["답변에 쓰임", w.items.filter((x) => x.used > 0).length, `${num(w.items.filter((x) => x.used > 0).length)}개`, rate === null ? "" : `${rate}% 쓰임`]];
    const fmax = Math.max(1, ...flow.map((f) => f[1]));
    const fbox = el("div", "v4u-bars v4u-flowbars");
    for (const [label, n, text, side] of flow) {
      const row = el("div", "v4u-bar-row"), bar = el("span", "v4u-bar"), fill = el("i"); fill.style.width = `${Math.max(1, (n / fmax) * 100)}%`; bar.append(fill);
      row.append(el("span", "v4u-bar-name", label), bar, el("span", "v4u-num", text), el("span", "v4u-side", side)); fbox.append(row);
    }
    const flowCard = card("기억 흐름", `최근 ${w.days}일 · 저장한 기억이 답변까지 가는 길`, fbox);
    // 종류별 쓰임률: used / injected per kind.
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
    const kindCard = card("종류별 쓰임률", "들어간 기억 중 답변에 쓰인 비율", kbox);
    const tbox = el("div", "v4u-rows");
    for (const x of tidy.slice(0, 5)) {
      const s = w.items.find((y) => y.id === x.id) || { injected: 0, used: 0 };
      const row = el("button", "v4u-tidy"); row.type = "button";
      const t = el("div", "v4u-session-text"); t.append(raw(el("b", "", x.text)), el("small", "", `${s.injected}번 들어감 · ${s.used}번 쓰임`));
      row.append(t, el("span", "v4u-link", "정리"));
      row.addEventListener("click", () => m.openMemory?.(x.id));
      tbox.append(row);
    }
    if (!tidy.length) tbox.append(el("p", "v4u-empty", "정리할 기억이 없어요."));
    const tidyCard = card("정리할 기억", tidy.length ? link("모두 보기 →", () => m.openIdle?.()) : "", el("p", "v4u-hint", `들어가기만 하고 쓰이지 않음 · ${tidy.length}`), tbox);
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

  // ------------------------------------------------------------------ 세션 상세 (198:6822): one session's numbers, the dialogue stays in MEMORY › 세션
  function sessionPage(r) {
    const kp = kpiRow([["토큰", num(r.tokens), `입력 ${num(r.input)} · 출력 ${num(r.output)}`], ["비용", usd(r.cost_usd, r.priced), r.priced ? "공시 단가로 계산" : "단가 없음"],
      ["캐시", `${num(r.cached)} 읽기`, ""], ["메시지", num(r.messages), dur(r.active_seconds)]]);
    const used = card("쓴 것", "", facts([["모델", (r.models || []).join(", ") || "—"], ["프로젝트", r.project || "—"], ["장치", r.device || ""], ["에이전트", agentName(r.agent)]]));
    return [kp, used, el("p", "v4u-hint", "이 화면은 사용량만 보여 줘요. 대화 내용은 MEMORY › 세션에서 볼 수 있어요.")];
  }
  function sessionSide(r) {
    const open = link("대화 내용 보기 (MEMORY › 세션) ›", () => ctx.openSession?.(r.id));
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
    if (S.doc?.agents?.length && S.tab !== "requests" && menu() !== "router") {
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
    const note = el("p", "v4u-hint v4u-at", menu() === "router" ? "Arkism 라우터를 거친 요청만 세요. 에이전트가 직접 쓴 토큰은 MEMORY·CREW 사용량에서 봐요." : at ? `1분마다 모아요 · 마지막 ${new Date(at).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false })}` : "");
    table.replaceChildren(...(at || menu() === "router" ? [note] : []), ...({ overview, requests, sessions, hours: hoursTab }[S.tab])(d));
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
    sessions: "세션을 프로젝트·모델·스킬별로 모아 봐요. 세션 하나하나는 MEMORY › 세션에서 봐요.",
    hours: "언제 많이 일하는지 요일·시간별로 봐요."
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
    reset: () => { epoch++; Object.assign(S, { doc: null, state: "idle", session: null }); },
    leave: () => { S.session = null; },
    views: { usage: { title: () => (S.session ? S.session.title || "(제목 없음)" : "사용량"), sub, render: renderView,
      get keepDetail() { return !!S.session || S.tab !== "overview"; },
      head: () => [button("새로고침", "ghost", "arrows-clockwise", load)] } }
  };
};
