// 라우팅 (Figma v2 17·17b) + 설정 › 라우팅 설정 (Figma 07d). The screen shows; the settings tab
// holds every switch (자동 라우팅·페일오버, 모델별 설정). A server without /v1/routing/* gets the "아직 지원하지 않아요" screen.
window.ArkismRouting = (ctx) => {
  "use strict";
  const { state, $, el, icon, button, segment, go, render, request, query, notice, detailShell, agentLogo, ago } = ctx;
  const turns = window.ArkismRoutingTurns?.(ctx);

  const RANGES = [["1h", "1시간"], ["24h", "24시간"], ["7d", "7일"], ["30d", "30일"]];
  const FAMILY_LOGO = { CLAUDE: "img:claude.svg", GPT: "sym:a-codex", GLM: "sym:a-zcode", GEMINI: "sym:a-gemini", XAI: "sym:i-x" };
  const STATUS = { ok: ["ok", "정상"], watch: ["warn", "모니터링"], slow: ["warn", "느림"], key: ["warn", "키 만료 임박"], fixed: ["on", "고정 중"], idle: ["off", "대기"], off: ["off", "사용 안 함"] };
  const EVENT_KINDS = [["all", "전체"], ["incident", "장애"], ["policy", "정책 변경"], ["recovery", "복구"]];
  const EVENT_ICON = { incident: "warning-circle", policy: "sliders-horizontal", recovery: "check-circle" };
  const SVGNS = "http://www.w3.org/2000/svg";

  const S = { range: "24h", doc: null, state: "idle", error: "", eventKind: "all", eventsAll: false, settings: null, settingsState: "idle", selected: "", draft: {}, busy: "",
    groups: null, groupDraft: null, group: "", trial: { text: "", out: null, busy: false } };
  const EVENTS_SHOWN = 5;
  let epoch = 0;

  const num = (n) => (n == null ? "—" : Number(n).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR"));
  const sec1 = (ms) => (ms == null ? "—" : (ms / 1000).toFixed(1));
  const usd = (n) => (n == null ? "—" : n >= 100 ? `$${Math.round(n).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR")}` : `$${Number(n).toFixed(2)}`);
  const pct1 = (n) => (n == null ? "—" : `${Number(n).toFixed(1)}%`);
  const svg = (tag, attrs = {}) => { const n = document.createElementNS(SVGNS, tag); for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, String(v)); return n; };
  const unsupported = (e) => /HTTP 404|not_found|HTTP 501|not_implemented/.test(e?.message || "");

  // ------------------------------------------------------------------ data
  async function load(silent = false) {
    const quiet = silent === true;
    if (quiet && S.state !== "ready") return;
    const ticket = ++epoch;
    if (!quiet) { S.state = "loading"; S.error = ""; paint(); }
    // The skill decision log fills its card when it lands; a server without it (or a failed read) only hides that card.
    const skills = request(`/v1/routing/skills?${query({})}`).then((r) => (Array.isArray(r?.items) ? r.items : undefined), () => undefined);
    try {
      const doc = await request(`/v1/routing/overview?${query({ range: S.range })}`);
      if (ticket !== epoch || (quiet && ctx.canRefresh?.() === false)) return;
      if (doc?.authority !== "data_only_untrusted" || !doc.kpi) throw new Error("invalid_response");
      doc.skill_routes = S.doc?.skill_routes; // keep the card until the new list lands
      if (!quiet || JSON.stringify(S.doc) !== JSON.stringify(doc)) { S.doc = doc; S.state = "ready"; S.at = new Date(); paint(); }
    } catch (e) {
      if (ticket !== epoch || quiet) return;
      S.doc = null; S.state = unsupported(e) ? "unsupported" : "error"; S.error = e.message;
      paint();
      return;
    }
    const items = await skills;
    if (ticket !== epoch || !S.doc || JSON.stringify(S.doc.skill_routes) === JSON.stringify(items)) return;
    S.doc.skill_routes = items; paint();
  }
  const paint = () => { if (state.view === "routing") render(false); };

  // ------------------------------------------------------------------ small parts
  function agentTile(family, name) {
    const spec = FAMILY_LOGO[family];
    if (spec) return agentLogo(spec, name || family);
    const t = el("span", "agent-tile mono-logo", (family || "?").slice(0, 1));
    t.title = name || family;
    return t;
  }
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
  function badge(kind, label) {
    const [cls, text] = STATUS[kind] || STATUS.ok;
    const b = el("span", `rt-status ${cls}`);
    b.append(el("i"), label || text);
    return b;
  }
  function delta(text, kind) {
    return el("span", `rt-delta ${kind}`, text);
  }

  // smooth curve through points (Catmull-Rom → cubic Bézier), same look as the Figma KPI cards
  function curve(pts) {
    let d = `M ${pts[0][0].toFixed(1)} ${pts[0][1].toFixed(1)}`;
    for (let i = 0; i < pts.length - 1; i++) {
      const p0 = pts[i - 1] || pts[i], p1 = pts[i], p2 = pts[i + 1], p3 = pts[i + 2] || p2;
      const c1 = [p1[0] + (p2[0] - p0[0]) / 6, p1[1] + (p2[1] - p0[1]) / 6], c2 = [p2[0] - (p3[0] - p1[0]) / 6, p2[1] - (p3[1] - p1[1]) / 6];
      d += ` C ${c1.map((v) => v.toFixed(1)).join(" ")} ${c2.map((v) => v.toFixed(1)).join(" ")} ${p2.map((v) => v.toFixed(1)).join(" ")}`;
    }
    return d;
  }
  function spark(values, kind) {
    const W = 160, H = 46, box = svg("svg", { class: `rt-spark ${kind}`, viewBox: `0 0 ${W} ${H + 8}`, "aria-hidden": "true" });
    if (!values || values.length < 2) return box;
    const mn = Math.min(...values), mx = Math.max(...values);
    const pts = values.map((v, i) => [(i * (W - 10)) / (values.length - 1), 6 + (H - 8) - ((v - mn) / (mx - mn || 1)) * (H - 8)]);
    const d = curve(pts), last = pts[pts.length - 1];
    box.append(svg("path", { class: "area", d: `${d} L ${last[0]} ${H + 8} L 0 ${H + 8} Z` }), svg("path", { class: "line", d }),
      svg("line", { class: "mark", x1: last[0], x2: last[0], y1: 2, y2: H + 8 }), svg("circle", { class: "dot", cx: last[0], cy: last[1], r: 3.5 }));
    return box;
  }
  function kpiCard(label, value, unit, chip, sub, values, kind) {
    const c = el("div", "rt-kpi");
    const row = el("div", "rt-kpi-value");
    row.append(el("strong", "", value));
    if (unit) row.append(el("span", "", unit));
    if (chip) row.append(delta(chip[0], chip[1]));
    c.append(el("p", "", label), row, el("small", "", sub), spark(values, kind));
    return c;
  }

  // ------------------------------------------------------------------ charts
  function xLabel(p, range) {
    const d = new Date(p.t);
    if (range === "1h") return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
    if (range === "24h") return `${d.getHours()}시`;
    return `${d.getMonth() + 1}/${d.getDate()}`;
  }
  function lineChart(points, lines, yMax, yLabels, range, mark) {
    const W = 383, H = 150, L = 34, bot = H - 20, ph = bot - 6;
    const box = svg("svg", { class: "rt-chart", viewBox: `0 0 ${W} ${H}`, role: "img" });
    const X = (i) => L + (i * (W - L)) / Math.max(1, points.length - 1), Y = (v) => bot - (Math.min(v, yMax) / yMax) * ph;
    yLabels.forEach((lab, i) => {
      const y = bot - (i / (yLabels.length - 1)) * ph;
      box.append(svg("line", { class: i ? "grid" : "grid base", x1: L, x2: W, y1: y, y2: y }));
      const t = svg("text", { class: "axis", x: L - 6, y: y + 3, "text-anchor": "end" }); t.textContent = lab; box.append(t);
    });
    if (!points.length) return box; // no requests in the range yet: axes only
    for (const ln of lines) {
      const pts = points.map((p, i) => [X(i), Y(p[ln.key] ?? 0)]);
      if (ln.area) box.append(svg("path", { class: `area ${ln.cls}`, d: `${pts.map((q, i) => `${i ? "L" : "M"} ${q[0].toFixed(1)} ${q[1].toFixed(1)}`).join(" ")} L ${X(points.length - 1)} ${bot} L ${X(0)} ${bot} Z` }));
      box.append(svg("path", { class: `line ${ln.cls}`, d: pts.map((q, i) => `${i ? "L" : "M"} ${q[0].toFixed(1)} ${q[1].toFixed(1)}`).join(" ") }));
      if (ln.now) { const q = pts[pts.length - 1]; box.append(svg("circle", { class: `now ${ln.cls}`, cx: q[0], cy: q[1], r: 3.5 })); }
    }
    if (mark != null && points[mark.index]) {
      const q = [X(mark.index), Y(points[mark.index][mark.key] ?? 0)];
      box.append(svg("circle", { class: "incident", cx: q[0], cy: q[1], r: 4 }));
      const t = svg("text", { class: "incident-label", x: Math.min(W - 4, Math.max(L + 40, q[0])), y: Math.max(10, q[1] - 9), "text-anchor": "middle" }); t.textContent = mark.label; box.append(t);
    }
    for (let k = 0; k < 5; k++) {
      const i = Math.round((k * (points.length - 1)) / 4);
      const t = svg("text", { class: "axis", x: Math.min(W - 10, Math.max(L, X(i))), y: H - 4, "text-anchor": k === 0 ? "start" : k === 4 ? "end" : "middle" });
      t.textContent = k === 4 ? "지금" : xLabel(points[i], range); box.append(t);
    }
    return box;
  }
  function chartCard(title, summary, sub, chart, legend) {
    const c = el("div", "rt-card");
    const head = el("div", "rt-card-head");
    head.append(el("strong", "", title));
    if (legend) head.append(legend); else head.append(el("span", "", summary));
    c.append(head, el("small", "", sub), chart);
    return c;
  }
  const niceMax = (v, step) => Math.max(step, Math.ceil((v * 1.15) / step) * step);

  // ------------------------------------------------------------------ main view
  function kpis(d) {
    const k = d.kpi, now = k.requests_now_per_min;
    const sign = (n, unit, good) => (n == null ? null : [`${n > 0 ? "▲" : n < 0 ? "▼" : "±"} ${Math.abs(n)}${unit}`, n === 0 ? "neutral" : good(n) ? "ok" : "bad"]);
    const traffic = (d.series?.points || []).map((p) => p.traffic), p50 = (d.series?.points || []).map((p) => p.p50), ok = (d.series?.points || []).map((p) => p.success_rate ?? 100), cost = (d.series?.points || []).map((p) => p.cost ?? 0);
    const sum = []; cost.reduce((a, v, i) => (sum[i] = a + v), 0);
    return [
      kpiCard("요청 수", num(k.requests), "건", sign(k.requests_delta_pct, "%", (n) => n > 0), now != null ? `지금 ${num(now)}건/분` : "", traffic, "ok"),
      kpiCard("지연시간 p50", sec1(k.p50_ms), "초", sign(k.p50_delta_ms == null ? null : Math.round(k.p50_delta_ms / 100) / 10, "초", (n) => n < 0), `p95 ${sec1(k.p95_ms)}초`, p50, "ok"),
      kpiCard("성공률", Number(k.success_rate).toFixed(1), "%", sign(k.success_rate_delta_pp, "%p", (n) => n >= 0), `실패 ${num(k.failures)}건`, ok, k.success_rate_delta_pp < 0 ? "bad" : "ok"),
      kpiCard("비용", usd(k.cost_usd), "", k.cost_delta_pct == null ? null : [`${k.cost_delta_pct > 0 ? "▲" : k.cost_delta_pct < 0 ? "▼" : "±"} ${Math.abs(k.cost_delta_pct)}%`, "warn"], k.budget_usd ? `예산 ${Math.round((k.cost_usd / k.budget_usd) * 100)}% 사용` : "", sum, "warn")
    ];
  }
  function mainView() {
    const d = S.doc, pts = d.series?.points || [];
    const mx = pts.length ? Math.max(...pts.map((p) => p.traffic)) : 0, mean = pts.length ? pts.reduce((a, p) => a + p.traffic, 0) / pts.length : 0;
    const perLabel = { "1h": "건/분", "24h": "건/분", "7d": "건/시간", "30d": "건/일" }[S.range], unitLabel = { "1h": "분당 요청 수", "24h": "분당 요청 수", "7d": "시간당 요청 수", "30d": "하루 요청 수" }[S.range];
    const tMax = niceMax(mx, mx > 5000 ? 5000 : mx > 500 ? 500 : mx > 50 ? 10 : 5);
    const p95Max = pts.length ? Math.max(...pts.map((p) => p.p95)) : 0, lMax = niceMax(p95Max / 1000, p95Max > 20000 ? 10 : 2);
    const traffic = lineChart(pts, [{ key: "traffic", cls: "main", area: true, now: true }], tMax, [0, tMax / 2, tMax].map((v) => (v >= 10000 ? `${v / 10000}만` : num(v))), S.range);
    const secPts = pts.map((p) => ({ ...p, p50: p.p50 / 1000, p95: p.p95 / 1000 }));
    const inc = d.series?.incident; const mark = inc ? { index: inc.index, key: "p95", label: inc.label } : null;
    const latency = lineChart(secPts, [{ key: "p95", cls: "dim dash" }, { key: "p50", cls: "main", now: true }], lMax, [0, lMax / 2, lMax].map(String), S.range, mark);
    const legend = el("span", "rt-legend");
    legend.append(el("i", "lg-main"), "p50", el("i", "lg-dim"), "p95");
    const wrap = el("div", "rt-main");
    const charts = el("div", "rt-grid2");
    charts.append(chartCard("트래픽", `평균 ${mean >= 100 ? num(Math.round(mean)) : mean.toFixed(1)}${perLabel} · 최고 ${num(Math.round(mx))}${perLabel}`, unitLabel, traffic), chartCard("지연시간", "", "응답까지 걸린 시간(초)", latency, legend));
    wrap.append(charts);
    wrap.append(modelsTable(d), distribution(d));
    const gw = turns?.card(d.gateway); // Claude Code 본대화·부속 호출 (routing-turns.js)
    if (gw) wrap.append(gw);
    const sk = turns?.skillCard(d.skill_routes); // route_skills 판정 (routing-turns.js)
    if (sk) wrap.append(sk);
    return wrap;
  }
  function distribution(d) {
    const c = el("div", "rt-card wide");
    const head = el("div", "rt-card-head");
    head.append(el("strong", "", "모델 계열별 분배"), el("span", "", `실제로 간 요청 · ${RANGES.find(([k]) => k === S.range)[1]}`));
    const list = el("div", "rt-dist");
    const rows = [...(d.families || [])].sort((a, b) => b.requests - a.requests), top = Math.max(...rows.map((r) => r.share), 1);
    for (const r of rows) {
      const row = el("div", "rt-dist-row");
      const bar = el("span", "rt-bar"); const fill = el("i"); fill.style.width = `${(r.share / top) * 100}%`; bar.append(fill);
      row.append(agentTile(r.family), el("b", "", r.family), bar, el("em", "", pct1(r.share)), el("small", "", `${num(r.requests)}건`), el("span", "dim", r.note || ""));
      list.append(row);
    }
    c.append(head, list);
    if (d.families_note) { const n = el("p", "rt-note"); n.append(icon("warning-circle"), d.families_note); c.append(n); }
    return c;
  }
  function modelsTable(d) {
    const c = el("div", "rt-card wide table");
    const head = el("div", "rt-card-head");
    const manual = d.policy && d.policy.auto_routing === false;
    head.append(el("strong", "", "모델별 현황"), el("span", "", `${(d.models || []).length}개 모델 · ${RANGES.find(([k]) => k === S.range)[1]}`));
    c.append(head);
    const cols = "minmax(0,1fr) 76px 112px 64px 64px 96px";
    const th = el("div", "rt-row th"); th.style.gridTemplateColumns = cols;
    ["모델", "요청 수", "지연 p50 / p95", "성공률", "비용", "상태"].forEach((t, i) => th.append(el("span", i && i < 5 ? "r" : "", t)));
    c.append(th);
    for (const m of d.models || []) {
      const fixed = manual && m.id === d.policy.default_model;
      const kind = manual ? (fixed ? "fixed" : "idle") : m.status;
      const row = el("div", `rt-row${manual && !fixed ? " dimmed" : ""}`); row.style.gridTemplateColumns = cols;
      const name = el("span", "rt-model"); const t = el("span", "rt-model-text"); t.append(el("b", "", m.name), el("small", "", m.provider || m.family));
      name.append(agentTile(m.family), t);
      const slow = m.status === "slow" || m.status === "watch";
      const cells = [el("span", "r m", num(m.requests)), el("span", `r m${slow ? " warn" : ""}`, `${sec1(m.p50_ms)}s / ${sec1(m.p95_ms)}s`), el("span", `r m${m.status === "watch" ? " warn" : ""}`, pct1(m.success_rate)), el("span", "r m", usd(m.cost_usd))];
      const metrics = el("span", `rt-metrics${slow ? " warn" : ""}`, `${num(m.requests)}건 · ${sec1(m.p50_ms)}s / ${sec1(m.p95_ms)}s · ${pct1(m.success_rate)} · ${usd(m.cost_usd)}`);
      row.append(name, ...cells, badge(kind), metrics);
      c.append(row);
    }
    return c;
  }
  function renderView() {
    const table = $("#table");
    table.className = "rt-wrap";
    $("#chips").replaceChildren();
    $("#controls").replaceChildren(); // the range segment sits in the area bar (Figma 198:372)
    if (S.state === "idle") { S.state = "loading"; queueMicrotask(load); }
    if (S.state !== "ready") {
      $("#stats").replaceChildren();
      const msg = S.state === "unsupported" ? "이 서버는 아직 라우팅을 지원하지 않아요. 서버에 모델 라우터가 연결되면 여기서 요청량·지연시간·성공률·비용을 볼 수 있어요."
        : S.state === "error" ? `라우팅 정보를 불러오지 못했어요 (${S.error}). 다시 시도해 주세요.` : "불러오는 중…";
      const box = el("div", "empty", msg);
      if (S.state === "error") box.append(el("br"), button("다시 시도", "small", "arrows-clockwise", load));
      table.replaceChildren(box);
      detail();
      return;
    }
    const d = S.doc;
    $("#stats").replaceChildren();
    const kp = el("div", "rt-kpis"); kp.append(...kpis(d)); // inside the table area: the stats row is hidden on narrow windows
    const parts = [kp];
    if (d.policy && d.policy.auto_routing === false) {
      const n = el("div", "callout rt-off"); const t = el("div");
      t.append(el("strong", "", "자동 라우팅이 꺼져 있어요"), el("p", "", `모든 요청이 ${(d.models || []).find((m) => m.id === d.policy.default_model)?.name || "고정 모델"}로 가고 있어요. 모델 계열별 분배는 멈췄어요.`));
      n.append(icon("warning-circle"), t, button("라우팅 설정", "small", "gear-six", openSettings));
      parts.push(n);
    }
    parts.push(mainView());
    const inline = el("div", "rt-inline"); inline.append(eventFeed(d)); parts.push(inline); // narrow windows hide the side panel
    table.replaceChildren(...parts);
    detail();
  }

  // ------------------------------------------------------------------ right panel: 제어 · 이벤트
  function kv(k, v) {
    const r = el("div", "rt-kv"); r.append(el("span", "", k)); if (typeof v === "string") r.append(el("b", "", v)); else r.append(v);
    return r;
  }
  const openSettings = () => { state.settingsTab = "routing"; go("settings"); };
  const modelName = (models, id) => (models || []).find((m) => m.id === id)?.name || "";
  // The screen only shows how routing is set; the switches live in 설정 › 라우팅 설정.
  function policyLine(d) {
    const p = d.policy || {};
    const c = el("section", "rt-policy-line");
    const auto = p.auto_routing === false ? `꺼짐 · ${modelName(d.models, p.default_model) || "고정 모델"}로 고정` : (!p.classifier ? (p.default_model ? "켜짐 · 판정 모델 없음, 고정 모델로" : "켜짐 · 판정 모델 없음, 첫 모델로") : p.classifier_ms != null ? `켜짐 · ${p.classifier} 보통 ${sec1(p.classifier_ms)}초` : `켜짐 · ${p.classifier}`);
    const order = (p.failover?.order || []).map((id) => modelName(d.models, id) || id);
    const fail = p.auto_failover === false ? "꺼짐" : `켜짐${order.length ? ` · ${order.join(" → ")}` : " · 라우팅 순서대로"}`;
    const go2 = button("라우팅 설정", "", null, openSettings);
    c.append(el("h3", "", "라우팅 정책"), kv("자동 라우팅", auto), kv("실패 시 넘김", fail), kv("마지막 판정", p.last_decision || "—"), go2);
    return c;
  }
  // 설정 › 라우팅 설정 (Figma 198:458): 자동 라우팅 and 실패하면 다음 모델로 as cards; switches save at once, conditions save with 조건 저장.
  function policyCards(items) {
    const p = S.policy || {}, f = { error_rate_pct: 5, p95_ms: 120000, order: [], ...(p.failover || {}), ...(S.failoverDraft || {}) };
    const on = items.filter((m) => m.enabled !== false);
    const pick = (label, value, onPick, none) => {
      const f2 = el("label", "rt-field"); f2.append(el("span", "", label));
      const sel = el("select");
      sel.append(new Option(none, ""));
      for (const m of on) sel.append(new Option(m.name, m.id, false, m.id === value));
      sel.value = value || "";
      sel.addEventListener("change", () => onPick(sel.value));
      f2.append(sel);
      return f2;
    };
    const head = (title, sw) => { const h = el("div", "rt-ctl-head"); h.append(el("h3", "", title), sw); return h; };
    const a = el("section", "rt-set-card");
    a.append(head("자동 라우팅", switchEl(p.auto_routing !== false, "자동 라우팅", (v) => savePolicySettings({ auto_routing: v }))),
      el("p", "rt-set-hint", p.auto_routing === false ? "꺼져 있어요. 모든 요청이 고정 모델로 가요." : !p.classifier ? (p.default_model ? "판정 모델이 없어서 그룹 밖 요청은 고정 모델로, 그룹 요청은 그룹의 1번 모델로 가요. 판정 모델은 설정 › 에이전트에서 골라요." : "판정 모델이 없어서 그룹 밖 요청은 순서상 첫 모델로, 그룹 요청은 그룹의 1번 모델로 가요. 판정 모델은 설정 › 에이전트에서 골라요.") : "판정 모델이 요청 내용과 모델마다 적은 설명을 보고 알맞은 모델로 보내요."));
    if (p.auto_routing === false) { const n = el("div", "rt-amber"); n.append(el("p", "", "모델 계열별 분배가 멈췄어요. 켜면 바로 다시 나눠요.")); a.append(n); }
    const g0 = el("div", "rt-fields");
    g0.append(pick("고정 모델 (꺼 두거나 판정 모델이 답하지 못할 때)", p.default_model, (v) => savePolicySettings({ default_model: v }), "순서상 첫 모델"));
    a.append(g0);
    if (p.auto_routing !== false) a.append(kv("분류 담당", p.classifier ? `${p.classifier}${p.classifier_ms != null ? ` · 보통 ${sec1(p.classifier_ms)}초` : ""}` : "없음"), kv("마지막 판정", p.last_decision || "—"));

    const b = el("section", "rt-set-card");
    b.append(head("실패하면 다음 모델로", switchEl(p.auto_failover !== false, "자동 페일오버", (v) => savePolicySettings({ auto_failover: v }))),
      el("p", "rt-set-hint", "고른 모델이 응답을 시작하기 전에 거절되면 아래 순서로 넘겨요. 순서를 비우면 라우팅 순서를 따라요."));
    const name = (id) => on.find((m) => m.id === id)?.name || id;
    b.append(kv("넘기는 조건", `오류율 ${f.error_rate_pct}% 넘음 · p95 ${Math.round(f.p95_ms / 1000)}초 넘음`),
      kv("넘기는 순서", [...(f.order || []).map(name), "라우팅 순서대로"].join(" → ")),
      kv("돌아오기", "한도·크레딧·429로 거절된 모델은 잠시 쉬며 맨 뒤로 가고, 요청 하나로 먼저 확인한 뒤 돌아와요"));
    const stageF = (patch) => { S.failoverDraft = { ...(S.failoverDraft || {}), ...patch }; };
    const order = [...(f.order || [])];
    const g2 = el("div", "rt-fields three");
    // Each pick builds on the latest draft, so changing two slots keeps both.
    const slots = [order[0] || "", order[1] || "", order[2] || ""];
    for (let i = 0; i < 3; i++) g2.append(pick(`${i + 1}번째로 넘길 모델`, order[i], (v) => { slots[i] = v || ""; stageF({ order: slots.filter(Boolean) }); }, "라우팅 순서대로"));
    // These only mark models 주의·느림 in the overview; failover itself is per request (gateway).
    const g1 = el("div", "rt-fields");
    g1.append(field("오류율이 이보다 높으면 '주의' 표시", f.error_rate_pct, (v) => stageF({ error_rate_pct: Number(v) }), "%"),
      field("p95가 이보다 느리면 '느림' 표시", f.p95_ms / 1000, (v) => stageF({ p95_ms: Math.round(Number(v) * 1000) }), "초"));
    const foot = el("div", "rt-foot"); foot.append(button("조건 저장", "", null, () => {
      // Read the draft at click time: f is the value from when this card was drawn.
      const now = { error_rate_pct: 5, p95_ms: 120000, order: [], ...((S.policy || {}).failover || {}), ...(S.failoverDraft || {}) };
      void savePolicySettings({ failover: { error_rate_pct: now.error_rate_pct, p95_ms: now.p95_ms, order: now.order || [] } });
    }));
    b.append(g2, g1, foot);
    return [a, b];
  }
  function eventFeed(d) {
    const box = el("div", "rt-events");
    const head = el("div", "rt-card-head"); head.append(el("strong", "", "이벤트"), el("span", "", `${RANGES.find(([k]) => k === S.range)[1]} · ${(d.events || []).length}건`));
    const chips = el("div", "rt-chips");
    for (const [k, l] of EVENT_KINDS) {
      const c = el("button", `chip${S.eventKind === k ? " active" : ""}`, l); c.type = "button";
      c.addEventListener("click", () => { S.eventKind = k; paint(); });
      chips.append(c);
    }
    const list = el("div", "rt-event-list");
    const all = (d.events || []).filter((e) => S.eventKind === "all" || e.kind === S.eventKind);
    const items = S.eventsAll ? all : all.slice(0, EVENTS_SHOWN);
    for (const e of items) {
      const row = el("div", "rt-event");
      row.classList.add(e.kind);
      const ic = el("span", `rt-event-icon ${e.kind}`);
      ic.append(e.family ? agentTile(e.family) : icon(EVENT_ICON[e.kind] || "warning-circle"));
      const tx = el("div"); tx.append(el("b", "", e.title), el("p", "", e.detail || ""), el("small", "", ago(e.at)));
      row.append(ic, tx); list.append(row);
    }
    if (!items.length) list.append(el("div", "empty", "해당하는 이벤트가 없어요."));
    box.append(head, chips, list);
    // 이벤트 전체 보기 (Figma 198:372): the overview holds this range's newest 50; 30일 reaches back furthest.
    const more = el("div", "rt-events-more");
    if (all.length > EVENTS_SHOWN) {
      const b = el("button", "link-btn v4-link", S.eventsAll ? "접기" : `이벤트 전체 보기 (${all.length}건)`); b.type = "button";
      b.addEventListener("click", () => { S.eventsAll = !S.eventsAll; paint(); });
      more.append(b);
    }
    if (S.eventsAll && S.range !== "30d") {
      const b = el("button", "link-btn v4-link", "30일로 넓혀 보기"); b.type = "button";
      b.addEventListener("click", () => { S.range = "30d"; void load(); });
      more.append(b);
    }
    if (S.eventsAll && (d.events || []).length >= 50) more.append(el("small", "", "최근 50건까지 보여요."));
    if (more.childNodes.length) box.append(more);
    return box;
  }
  function detail() {
    const body = detailShell("이벤트");
    body.classList.add("rt-side");
    if (S.state !== "ready") { body.append(el("p", "dim", S.state === "unsupported" ? "서버가 라우팅을 지원하면 장애·정책 변경·복구 이벤트가 여기에 나와요." : "불러오는 중…")); return; }
    body.append(policyLine(S.doc), eventFeed(S.doc));
  }

  // ------------------------------------------------------------------ 설정 › 라우팅 설정
  let settingsEpoch = 0;
  async function loadSettings() {
    const ticket = ++settingsEpoch;
    S.settingsState = "loading";
    try {
      const [doc, ov, groups] = await Promise.all([request(`/v1/routing/models?${query()}`), request(`/v1/routing/overview?${query({ range: "1h" })}`),
        request(`/v1/routing/groups?${query()}`).catch((e) => (unsupported(e) ? null : Promise.reject(e)))]); // older servers have no groups
      if (ticket !== settingsEpoch) return; // left the tab meanwhile
      if (doc?.authority !== "data_only_untrusted" || !Array.isArray(doc.items) || !ov?.policy) throw new Error("invalid_response");
      S.settings = doc; S.policy = ov.policy; S.failoverDraft = null; S.settingsState = "ready";
      S.groups = groups && Array.isArray(groups.items) ? groups : null; S.groupDraft = null;
      if (!S.groups?.items.some((g) => g.id === S.group)) S.group = "";
      if (!doc.items.some((m) => m.id === S.selected)) S.selected = doc.items[0]?.id || "";
    } catch (e) { if (ticket !== settingsEpoch) return; S.settings = null; S.settingsState = unsupported(e) ? "unsupported" : "error"; S.error = e.message; }
    if (state.view === "settings" && state.settingsTab === "routing") render(false);
  }
  async function savePolicySettings(patch) {
    try {
      await request("/v1/routing/policy", { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: S.policy.revision, ...patch }) });
      notice(patch.auto_routing === false ? "자동 라우팅을 껐어요. 모든 요청이 고정 모델로 가요." : patch.auto_routing === true ? "자동 라우팅을 켰어요." : "저장했어요.");
      S.state = "idle"; // the routing screen reads the new policy next time
    } catch (e) { notice(`저장하지 못했어요 (${e.message}).`); }
    await loadSettings();
  }
  const cur = (m) => ({ ...m, ...(S.draft[m.id] || {}) });
  const stage = (m, patch) => { S.draft[m.id] = { ...(S.draft[m.id] || {}), ...patch }; };
  async function saveModel(m) {
    const patch = S.draft[m.id]; if (!patch) return;
    try {
      await request("/v1/routing/models", { method: "POST", body: JSON.stringify({ scope: state.scope, id: m.id, expected_revision: m.revision, ...patch }) });
      delete S.draft[m.id]; notice("저장했어요."); await loadSettings();
    } catch (e) { notice(`저장하지 못했어요 (${e.message}).`); }
  }
  function field(label, value, onInput, suffix) {
    const f = el("label", "rt-field"); f.append(el("span", "", label));
    const i = el("input"); i.value = value ?? ""; i.addEventListener("input", () => onInput(i.value)); f.append(i);
    if (suffix) f.append(el("em", "", suffix));
    return f;
  }
  // The models Clef can pick, in routing order (Figma's model cards: order, logo, name, what it is for, status, on/off).
  function modelList(items) {
    const box = el("section", "rt-set-card");
    const h = el("div", "rt-ctl-head"); h.append(el("h3", "", "라우팅에 쓰는 모델"), el("small", "", "1번이 첫 모델")); box.append(h);
    box.append(el("p", "rt-set-hint", "설정 › 에이전트에서 켠 모델이에요. 누르면 오른쪽에서 설명·순서·한도를 고쳐요."));
    const list = el("div", "rt-mlist");
    const sorted = [...items].sort((x, y) => (cur(x).enabled === false) - (cur(y).enabled === false) || (cur(x).order ?? 99) - (cur(y).order ?? 99));
    const firstOn = sorted.find((m) => cur(m).enabled !== false);
    for (const raw of sorted) {
      const m = cur(raw), on = m.enabled !== false;
      const row = el("button", `rt-mcard${S.selected === raw.id && !S.group ? " selected" : ""}${on ? "" : " dimmed"}`); row.type = "button";
      row.setAttribute("aria-pressed", String(S.selected === raw.id && !S.group));
      if (raw === firstOn) row.append(el("span", "rt-first", "✓ 첫 모델"));
      const t = el("span", "rt-model-text"); t.append(el("b", "mono", m.name), el("small", "", m.provider || m.family || ""));
      const [cls, label] = on ? (STATUS[m.connection?.state] || STATUS.ok) : STATUS.off;
      const conn = el("span", `rt-status ${cls}`); conn.append(el("i"), m.connection?.label || label);
      const sw = switchEl(on, `${m.name} 사용`, (v) => { stage(raw, { enabled: v }); void saveModel(raw); });
      sw.addEventListener("click", (e) => e.stopPropagation());
      row.append(el("span", "rt-order mono", on ? String(m.order ?? "—") : "—"), agentTile(m.family), t, el("span", "rt-when", m.when ? `의도 · ${m.when}` : ""), conn, sw);
      row.addEventListener("click", () => { S.selected = raw.id; S.group = ""; render(false); });
      list.append(row);
    }
    box.append(list);
    return box;
  }
  function modelDetail(raw) {
    const m = cur(raw), box = el("div", "rt-model-detail");
    box.append(el("small", "rt-kind", "모델"));
    const top = el("div", "rt-detail-top"); const t = el("div"); t.append(el("h3", "", m.name), el("small", "", `${m.provider || ""} · ${m.family} 계열`));
    top.append(agentTile(m.family, m.name), t, switchEl(m.enabled !== false, "라우팅에 쓰기", (v) => { stage(raw, { enabled: v }); void saveModel(raw); }));
    const when = el("div", "rt-sec"); when.append(el("h4", "", "판정 모델이 이럴 때 보내요"), el("small", "", "판정 모델이 이 설명을 보고 요청을 나눠요."));
    const ta = el("textarea"); ta.value = m.when || ""; ta.rows = 2; ta.addEventListener("input", () => stage(raw, { when: ta.value })); when.append(ta);
    const mix = el("div", "rt-sec"); mix.append(el("h4", "", "순서"), el("small", "", "고정 모델이 없을 때 먼저 고르고, 막혔을 때 넘기는 순서예요."));
    const g1 = el("div", "rt-fields"); g1.append(field("순서", m.order, (v) => stage(raw, { order: Number(v) }), "번째"));
    mix.append(g1);
    const lim = el("div", "rt-sec"); lim.append(el("h4", "", "한도"), el("small", "", "게이트웨이가 모델마다 지켜요. 넘치면 그룹은 다음 모델로 넘기고, 단일 모델은 429와 Retry-After로 돌려보내요. 비우거나 0이면 제한이 없어요."));
    const g2 = el("div", "rt-fields"); g2.append(field("분당 요청", m.rpm_limit, (v) => stage(raw, { rpm_limit: Number(v) }), "건"), field("동시 요청", m.concurrency, (v) => stage(raw, { concurrency: Number(v) }), "건"));
    lim.append(g2);
    const con = el("div", "rt-sec"); con.append(el("h4", "", "연결"));
    const sb = el("span", `rt-status ${(STATUS[m.connection?.state] || STATUS.ok)[0]}`); sb.append(el("i"), m.connection?.label || "정상");
    con.append(kv("공급자", m.provider || "—"), kv("상태", sb));
    const foot = el("div", "rt-foot");
    foot.append(button("저장", "primary", null, () => saveModel(raw)));
    const note = el("div", "rt-note-card");
    const link = el("button", "link-btn v4-link", "에이전트 설정 열기 ›"); link.type = "button";
    link.addEventListener("click", () => { state.settingsTab = "agents"; render(false); });
    note.append(el("p", "", "여기는 \"어떤 모델을 어떤 순서로\"만 정해요. 모델 켜고 끄기·키·단가는 설정 › 에이전트에서 해요."), link);
    box.append(top, when, mix, lim, con, foot, note);
    return box;
  }
  // ------------------------------------------------------------------ 라우팅 그룹 (Figma 342:25246 · 198:458)
  // GET/POST /v1/routing/groups: a group is an ordered list of routing models with one strategy. An agent uses it by
  // asking for the model "group/…". The server knows two strategies: smart (Clef picks by intent) and ordered.
  const STRATEGIES = [["smart", "의도별"], ["ordered", "순서대로"]];
  const STRATEGY_TEXT = {
    smart: "의도별: 자동 라우팅이 켜져 있고 판정 모델이 있으면 판정 모델이 요청 내용과 모델 설명을 보고 그룹 안에서 골라요. 아니면 1번 모델로 가요.",
    ordered: "순서대로: 늘 1번 모델로 보내고, 실패하면 다음 모델로 넘겨요."
  };
  const groupsNow = () => S.groupDraft || S.groups?.items || [];
  const editGroups = () => { if (!S.groupDraft) S.groupDraft = (S.groups?.items || []).map((g) => ({ ...g, members: [...g.members] })); return S.groupDraft; };
  const groupOf = (id) => editGroups().find((g) => g.id === id);
  const strategyName = (k) => STRATEGIES.find(([x]) => x === k)?.[1] || k;
  const GROUP_ERR = { not_found: "그룹에 꺼진 모델이 있어요. 모델을 켜거나 그룹에서 빼 주세요.", invalid_request: "이름과 모델을 확인해 주세요. 그룹에는 모델이 하나 이상 있어야 해요.", revision_conflict: "다른 곳에서 먼저 바꿨어요. 새로고침한 뒤 다시 해 주세요." };
  async function saveGroups(autoDiscover) {
    const groups = groupsNow().filter((g) => !g.discovered).map(({ id, name, enabled, strategy, members }) => ({ id, name: name.trim(), enabled: enabled !== false, strategy, members }));
    try {
      await request("/v1/routing/groups", { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: S.groups.revision,
        groups, auto_discover: autoDiscover ?? !!S.groups.auto_discover }) });
      notice("그룹을 저장했어요.");
      S.groupDraft = null;
    } catch (e) { notice(`저장하지 못했어요 (${GROUP_ERR[e.message] || e.message}).`); return; }
    await loadSettings();
  }
  function newGroup(items) {
    const first = items.find((m) => cur(m).enabled !== false);
    const g = { id: `group/g-${Date.now().toString(36)}`, name: "새 그룹", enabled: true, strategy: "smart", members: first ? [first.id] : [] };
    editGroups().push(g);
    S.group = g.id; render(false);
  }
  // A group the server found by itself (same model under several accounts) becomes an editable group of its own.
  function keepDiscovered(g) {
    const list = editGroups(), i = list.findIndex((x) => x.id === g.id);
    const copy = { id: `group/g-${Date.now().toString(36)}`, name: g.name, enabled: true, strategy: g.strategy, members: [...g.members] };
    list.splice(i, 1, copy);
    S.group = copy.id; render(false);
  }
  async function tryRoute(groupId) {
    const t = S.trial;
    if (!t.text.trim() || t.busy) return;
    t.busy = true; t.out = null; render(false);
    try {
      const r = await request("/v1/routing/decide", { method: "POST", body: JSON.stringify({ scope: state.scope, task: t.text.trim(), ...(groupId ? { group: groupId } : {}) }) });
      t.out = r?.id ? r : { error: "invalid_response" };
    } catch (e) { t.out = { error: /unavailable|HTTP 503/.test(e.message) ? "쓸 수 있는 모델이 없어요 (그룹이 꺼졌거나 모델이 모두 꺼졌어요)." : e.message }; }
    t.busy = false; render(false);
  }
  function trialBox(items) {
    const box = el("div", "rt-trial");
    const h = el("div", "rt-ctl-head"); h.append(el("h4", "", "시험해 보기"), el("small", "", S.group ? `${groupsNow().find((g) => g.id === S.group)?.name || ""} 안에서 고르기` : "그룹 없이 · 모든 모델 중에서"));
    const row = el("div", "rt-trial-row");
    const input = el("input"); input.value = S.trial.text; input.placeholder = "예: 이 함수에 테스트를 써 줘"; input.setAttribute("aria-label", "시험할 요청");
    input.addEventListener("input", () => { S.trial.text = input.value; });
    input.addEventListener("keydown", (e) => { if (e.key === "Enter") void tryRoute(S.group); });
    row.append(input, button(S.trial.busy ? "고르는 중…" : "어디로 가나 보기", "", null, () => tryRoute(S.group)));
    box.append(h, el("small", "rt-set-hint", "실제로 보내지 않고, 이 요청이면 어느 모델로 갈지만 물어봐요. 자동 라우팅이 켜져 있으면 판정 비용이 조금 들어요."), row);
    const o = S.trial.out;
    if (o?.error) box.append(el("p", "rt-trial-out bad", o.error));
    else if (o) {
      const name = (id) => items.find((m) => m.id === id)?.name || id;
      const how = { clef: `판정 모델이 골랐어요${o.classifier_ms ? ` · ${sec1(o.classifier_ms)}초` : ""}`, default: "판정 모델이 고르지 않았어요", fixed: "고정 모델로" }[o.decision] || o.decision;
      const p = el("div", "rt-trial-out");
      p.append(kv("보낼 모델", name(o.id)), kv("고른 방법", how), kv("실패하면", (o.fallbacks || []).length ? [...o.fallbacks.map(name)].join(" → ") : "넘길 모델 없음"));
      box.append(p);
    }
    return box;
  }
  function groupsCard(items) {
    const box = el("section", "rt-set-card rt-groups");
    if (!S.groups) {
      box.append(el("h3", "rt-h3", "라우팅 그룹"), el("p", "rt-set-hint", "이 서버는 아직 라우팅 그룹을 지원하지 않아요."));
      return box;
    }
    const auto = switchEl(!!S.groups.auto_discover, "같은 모델 자동으로 묶기", (v) => saveGroups(v));
    const h = el("div", "rt-ctl-head"); h.append(el("h3", "", "라우팅 그룹"), el("small", "", "같은 모델 자동으로 묶기"), auto);
    box.append(h, el("p", "rt-set-hint", "같은 방식으로 라우팅되는 모델 묶음이에요. 에이전트가 모델 이름 대신 group/… 을 부르면 그룹 안에서 골라요."));
    const chips = el("div", "rt-group-chips");
    for (const g of groupsNow()) {
      const c = el("button", `rt-group-chip${S.group === g.id ? " active" : ""}${g.enabled === false ? " dimmed" : ""}`); c.type = "button";
      c.setAttribute("aria-pressed", String(S.group === g.id));
      const t = el("span", "rt-model-text"); t.append(el("b", "", g.name), el("small", "", `${g.discovered ? "자동 묶음" : strategyName(g.strategy)} · 모델 ${g.members.length}개${g.enabled === false ? " · 꺼짐" : ""}`));
      c.append(t);
      c.addEventListener("click", () => { S.group = S.group === g.id ? "" : g.id; S.trial.out = null; render(false); });
      chips.append(c);
    }
    const add = el("button", "rt-group-chip add", "+ 새 그룹"); add.type = "button";
    add.addEventListener("click", () => newGroup(items));
    chips.append(add);
    box.append(chips);
    const g = S.group && groupsNow().find((x) => x.id === S.group);
    if (g) {
      const line = el("div", "rt-group-line");
      line.append(el("span", "", "그룹 방식"));
      const seg = segment(STRATEGIES, g.strategy, (k) => { if (g.discovered) return; groupOf(g.id).strategy = k; render(false); });
      seg.classList.add("rt-strategy");
      if (g.discovered) seg.querySelectorAll(".tab").forEach((b) => { b.disabled = true; });
      line.append(seg);
      box.append(line, el("p", "rt-set-hint", STRATEGY_TEXT[g.strategy] || ""));
    }
    if (S.groupDraft) {
      const bar = el("div", "rt-amber rt-dirty"); bar.append(el("p", "", "그룹을 바꿨어요. 저장해야 적용돼요."), button("그룹 저장", "primary", null, () => saveGroups()), button("되돌리기", "ghost", null, () => { S.groupDraft = null; if (!S.groups.items.some((x) => x.id === S.group)) S.group = ""; render(false); }));
      box.append(bar);
    }
    box.append(trialBox(items));
    return box;
  }
  function groupDetail(g, items) {
    const box = el("div", "rt-model-detail rt-group-detail");
    const mine = () => groupOf(g.id);
    box.append(el("small", "rt-kind", g.discovered ? "자동으로 묶은 그룹" : "그룹"));
    const top = el("div", "rt-detail-top"); const t = el("div"); t.append(el("h3", "", g.name), el("small", "mono", g.id));
    top.append(t);
    if (!g.discovered) top.append(switchEl(g.enabled !== false, "이 그룹 쓰기", (v) => { mine().enabled = v; render(false); }));
    box.append(top, el("p", "rt-set-hint", "에이전트는 모델 이름에 위 group/… 을 넣어 이 그룹을 써요."));
    if (g.discovered) {
      const n = el("div", "rt-note-card"); n.append(el("p", "", "같은 모델이 여러 계정에 있어 서버가 자동으로 묶었어요. 고치려면 그룹으로 만들어요."), button("그룹으로 만들기", "", null, () => keepDiscovered(g)));
      box.append(n);
    } else {
      const nm = el("div", "rt-sec"); nm.append(el("h4", "", "이름"));
      const ni = el("input"); ni.value = g.name; ni.maxLength = 100; ni.setAttribute("aria-label", "그룹 이름");
      ni.addEventListener("change", () => { mine().name = ni.value; render(false); });
      nm.append(ni); box.append(nm);
    }
    const sec = el("div", "rt-sec"); sec.append(el("h4", "", "그룹에 든 모델 · 1번이 첫 모델"));
    const list = el("div", "rt-group-members");
    const name = (id) => items.find((m) => m.id === id);
    g.members.forEach((id, i) => {
      const m = name(id), row = el("div", `rt-member${m && cur(m).enabled === false ? " dimmed" : ""}`);
      const tx = el("span", "rt-model-text"); tx.append(el("b", "mono", m?.name || id), el("small", "", m ? `${m.provider || m.family || ""}${m.when ? ` · ${m.when}` : ""}${cur(m).enabled === false ? " · 꺼짐" : ""}` : "목록에 없는 모델"));
      row.append(el("span", "rt-order mono", String(i + 1)), m ? agentTile(m.family) : el("span"), tx);
      if (!g.discovered) {
        const move = (d) => { const arr = mine().members, j = i + d; [arr[i], arr[j]] = [arr[j], arr[i]]; render(false); };
        const up = button("↑", "icon-btn plain rt-move", null, () => move(-1)); up.disabled = i === 0; up.setAttribute("aria-label", "위로");
        const dn = button("↓", "icon-btn plain rt-move", null, () => move(1)); dn.disabled = i === g.members.length - 1; dn.setAttribute("aria-label", "아래로");
        const out = el("button", "link-btn rt-out", "빼기"); out.type = "button";
        out.addEventListener("click", () => { mine().members.splice(i, 1); render(false); });
        row.append(up, dn, out);
      }
      list.append(row);
    });
    if (!g.members.length) list.append(el("p", "dim", "모델이 없어요. 하나 이상 넣어야 저장돼요."));
    sec.append(list);
    if (!g.discovered) {
      const left = items.filter((m) => cur(m).enabled !== false && !g.members.includes(m.id));
      const sel = el("select", "rt-add-member"); sel.setAttribute("aria-label", "모델 넣기");
      sel.append(new Option(left.length ? "+ 모델 넣기" : "넣을 수 있는 모델이 없어요", ""));
      for (const m of left) sel.append(new Option(m.name, m.id));
      sel.disabled = !left.length;
      sel.addEventListener("change", () => { if (sel.value) { mine().members.push(sel.value); render(false); } });
      sec.append(sel);
    }
    box.append(sec);
    if (!g.discovered) {
      const foot = el("div", "rt-foot");
      foot.append(button("그룹 저장", "primary", null, () => saveGroups()), button("그룹 지우기", "ghost", null, async () => {
        if (!await ask(`'${g.name}' 그룹을 지울까요? 이 그룹을 부르던 에이전트는 모델을 못 찾게 돼요.`)) return;
        const list2 = editGroups(); list2.splice(list2.findIndex((x) => x.id === g.id), 1); S.group = ""; void saveGroups();
      }));
      box.append(foot);
    }
    const note = el("div", "rt-note-card");
    const link = el("button", "link-btn v4-link", "에이전트 설정 열기 ›"); link.type = "button";
    link.addEventListener("click", () => { state.settingsTab = "agents"; render(false); });
    note.append(el("p", "", "여기는 \"이 그룹에 어떤 모델을 어떤 순서로\"만 정해요. 모델 켜고 끄기·키·단가는 설정 › 에이전트에서 해요."), link);
    box.append(note);
    return box;
  }
  // 에이전트 연결 (built-in Arkism only): Claude Code, Codex and Gemini CLI send their model requests to the
  // gateway on this computer and use the 자동 group, every model turned on in 설정 › 에이전트. Their logins stay theirs.
  const AGENT_LINKS = [["claude_code", "img:claude.svg", "Claude Code", "기본 모델이 ‘자동’이 돼요. /model에서 다른 모델도 고를 수 있어요."],
    ["codex", "sym:a-codex", "Codex", "~/.codex/config.toml에 Arkism 공급자를 넣고 모델을 ‘자동’으로 바꿔요."],
    ["gemini", "sym:a-gemini", "Gemini CLI", "API 키로 로그인한 Gemini CLI만 돼요. Google 로그인은 주소 설정을 무시해요."]];
  async function agentLinkSet(agent, on) {
    if (S.busy) return;
    S.busy = agent; render(false);
    try { S.links = await window.__TAURI__.core.invoke("agent_link_set", { agent, on }); notice(on ? "연결했어요. 에이전트를 다시 시작하면 적용돼요." : "연결을 끊고 원래 설정으로 돌려놨어요."); }
    catch (e) { notice(String(e).includes("codex_provider_exists") ? "Codex 설정에 이미 arkism 공급자가 있어요. 직접 지운 뒤 다시 해 주세요." : String(e).includes("unreadable") ? "에이전트 설정 파일을 읽지 못했어요." : "연결하지 못했어요."); }
    finally { S.busy = ""; render(false); }
  }
  function agentLinks() {
    const tauri = window.__TAURI__?.core;
    if (!tauri || state.mode !== "local") return null;
    if (!S.links) { S.links = { agents: {} }; tauri.invoke("gateway_info").then((r) => { S.links = r; render(false); }, () => {}); }
    const c = el("div", "rt-card pad rt-links");
    const head = el("div", "rt-card-head");
    head.append(el("strong", "", "에이전트 연결"), el("span", "", S.links.url ? `중계기 ${S.links.url.replace("http://", "")} · 켜짐` : "중계기 꺼짐"));
    c.append(head, el("p", "dim", "연결한 에이전트는 이 PC의 Arkism 중계기로 요청을 보내고, 설정 › 에이전트에서 켠 모델 중에서 자동으로 골라 답해요. 각자 로그인은 그대로 써요."));
    for (const [id, logo, name, hint] of AGENT_LINKS) {
      const a = S.links.agents?.[id] || {};
      const row = el("div", "rt-link-row");
      const t = el("div", "rt-link-text"); t.append(el("strong", "", name), el("small", "", a.installed === false ? "이 PC에서 찾지 못했어요." : hint));
      const state2 = a.current ? ["ok", "연결됨"] : a.connected ? ["warn", "다시 연결 필요"] : ["off", "연결 안 됨"];
      const sb = el("span", `rt-status ${state2[0]}`); sb.append(el("i"), state2[1]);
      const btn = a.connected && a.current ? button("끊기", "small", null, () => agentLinkSet(id, false)) : button(a.connected ? "다시 연결" : "연결", "small primary", null, () => agentLinkSet(id, true));
      btn.disabled = !!S.busy || a.installed === false;
      row.append(agentLogo(logo, name), t, sb, btn);
      c.append(row);
    }
    return c;
  }
  function tab(kit) {
    kit.head(button("새로고침", "ghost", null, loadSettings)); // models come from 설정 › 에이전트; this reads them again
    if (S.settingsState === "idle") queueMicrotask(loadSettings);
    if (S.settingsState !== "ready") {
      kit.side(kit.sideNote("모델을 고르면 여기서 설명·순서·한도를 고쳐요."));
      return [el("div", "empty", S.settingsState === "unsupported" ? "이 서버는 아직 라우팅을 지원하지 않아요." : S.settingsState === "error" ? `불러오지 못했어요 (${S.error}).` : "불러오는 중…")];
    }
    const items = S.settings.items;
    const sel = items.find((m) => m.id === S.selected) || items[0];
    const grp = S.group && groupsNow().find((g) => g.id === S.group);
    kit.side(grp ? groupDetail(grp, items) : sel ? modelDetail(sel) : kit.sideNote("모델을 고르세요."));
    const [auto, failover] = policyCards(items);
    const links = agentLinks();
    return [auto, groupsCard(items), modelList(items), failover, ...(links ? [links] : [])];
  }

  return {
    refresh: () => load(true),
    render: renderView, detail, tab, load, loadSettings, count: () => S.doc?.models?.length || "",
    reset: () => { epoch++; Object.assign(S, { doc: null, state: "idle", settings: null, settingsState: "idle", draft: {} }); },
    leave: () => { if (S.state !== "idle") { epoch++; Object.assign(S, { doc: null, state: "idle" }); } }, // read again next time the screen opens
    // 라우팅 설정 lists the models turned on in 설정 › 에이전트: read them again every time the tab opens.
    leaveSettings: () => { if (S.settingsState !== "idle") { settingsEpoch++; Object.assign(S, { settings: null, settingsState: "idle", draft: {}, failoverDraft: null }); } },
    views: { routing: { title: "라우팅 현황", render: renderView, keepDetail: true,
      sub: () => (S.state === "ready" && S.at ? `실시간 · 10초마다 갱신 · 마지막 ${S.at.toLocaleTimeString(globalThis.ArkismI18n?.locale() || "ko-KR", { hour12: false })}` : "판정 모델이 요청을 나누고, 막히면 다음 모델로 넘겨요"),
      head: () => {
        const again = button("", "icon-btn", "arrows-clockwise", load); again.title = "새로고침"; again.setAttribute("aria-label", "새로고침");
        const seg = segment(RANGES.map(([k, l]) => [k, l]), S.range, (k) => { if (S.range !== k) { S.range = k; void load(); } });
        seg.classList.add("rt-range");
        return [again, seg];
      } } }
  };
};
