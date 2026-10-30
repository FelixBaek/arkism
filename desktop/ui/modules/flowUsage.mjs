// @ts-check
// 기록·사용량 (Figma 1404:16985): one workflow's runs, success, cost and time over a period, its runs by day and
// the steps that fail most. GET /v1/crew/usage?workflow_id= sums them on the server; 다시 실행 reuses startRun.
const RANGES = [["today", "오늘"], ["7d", "7일"], ["30d", "30일"], ["90d", "90일"], ["all", "전체"]];
const DAYS = { today: 1, "7d": 7, "30d": 30, "90d": 90 };

export const usageDuration = (ms) => {
  const s = Math.round((ms || 0) / 1000);
  return s >= 60 ? `${Math.floor(s / 60)}분 ${String(s % 60).padStart(2, "0")}초` : `${s}초`;
};
export const usageTokens = (n) => {
  n = Number(n || 0);
  return n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e3 ? `${(n / 1e3).toFixed(1)}K` : String(n);
};
const usd = (n) => `$${Number(n || 0).toFixed(2)}`;

export function createFlowUsage({ el, request, query, ask, notice, startRun, runRequest }) {
  let range = "30d";
  function card(cls, title, sub) {
    const c = el("section", `tm-card ${cls}`);
    const h = el("div", "tm-card-head");
    h.append(el("h3", "", title), el("p", "", sub));
    c.append(h);
    return c;
  }
  function link(text, onClick) { const b = el("button", "tm-link", text); b.type = "button"; b.addEventListener("click", onClick); return b; }
  const rangeLabel = () => (range === "today" ? "오늘" : range === "all" ? "전체 기간" : `최근 ${RANGES.find(([k]) => k === range)?.[1]}`);

  // 실패한 곳부터 다시: the newest failed run (where that step failed), with its done steps kept.
  async function retry(rec, nodeId) {
    let runs = [];
    try { runs = (await request(`/v1/crew/runs?${query({ crew_id: rec.id, status: "failed", limit: 30 })}`)).items || []; } catch (_) { /* none */ }
    const run = runs.find((r) => r.crew_run?.crew_id === rec.id && (!nodeId || r.crew_run.nodes?.[nodeId]?.status === "failed"));
    if (!run) { notice("다시 돌릴 실패 기록을 찾지 못했어요."); return; }
    if (!await ask("마지막 실패 실행을 실패한 곳부터 다시 돌릴까요? 끝난 단계는 결과를 그대로 써요.")) return;
    await startRun(rec, runRequest(run.crew_run), null, run.crew_run);
  }
  function dayChart(days) {
    const ok = days.reduce((s, x) => s + (x.workflow ?? x.ok ?? 0), 0), bad = days.reduce((s, x) => s + (x.failed || 0), 0);
    const c = card("tm-chart", "날짜별 실행", `${rangeLabel()} · 성공 ${ok} · 실패 ${bad}`);
    const legend = el("div", "tm-legend"); legend.append(el("span", "ok", "성공"), el("span", "bad", "실패")); c.firstChild.append(legend);
    const top = Math.ceil(Math.max(1, ...days.map((x) => (x.workflow ?? x.ok ?? 0) + (x.failed || 0))) / 10) * 10;
    const plot = el("div", "tm-plot");
    const axis = el("div", "tm-axis"); [top, Math.round(top / 2), 0].forEach((v) => axis.append(el("span", "mono", String(v))));
    const cols = el("div", "tm-cols");
    const step = Math.max(1, Math.ceil(days.length / 5));
    days.forEach((x, i) => {
      const good = x.workflow ?? x.ok ?? 0, fail = x.failed || 0;
      const col = el("div", "tm-col");
      for (const [cls, v] of [["bad", fail], ["auto", good]]) { const b = el("i", cls); b.style.height = `${(v / top) * 100}%`; col.append(b); }
      const [, mm, dd] = String(x.date || "").split("-");
      col.append(el("small", "mono", i % step === 0 || i === days.length - 1 ? `${Number(mm)}.${Number(dd)}` : ""));
      col.title = `${`${Number(mm)}월 ${Number(dd)}일`} · 성공 ${good} · 실패 ${fail}`;
      col.setAttribute("aria-label", col.title);
      cols.append(col);
    });
    if (!days.length) cols.append(el("p", "tm-empty", "이 기간에는 실행 기록이 없어요."));
    plot.append(axis, cols);
    c.append(plot);
    return c;
  }
  function failing(rec, rows) {
    const total = rows.reduce((s, f) => s + f.failures, 0);
    const c = card("cu-card cu-fail", "실패가 많은 단계", `${rangeLabel()} · 실패 ${total}번이 ${rows.length}곳에서`);
    if (rows.length) c.firstChild.append(link("실패한 곳부터 다시 →", () => retry(rec)));
    for (const f of rows) {
      const node = (rec.crew.graph?.nodes || []).find((n) => n.id === f.node_id);
      const r = el("div", "tm-ulist");
      const t = el("div", ""); t.append(el("b", "", node?.title || f.node_id));
      const right = el("div", "tm-ulist-right"); right.append(el("span", "amber mono", `${f.failures}번`), link("다시 실행", () => retry(rec, f.node_id)));
      r.append(t, right); c.append(r);
    }
    if (!rows.length) c.append(el("p", "tm-empty", "실패한 단계가 없어요."));
    return c;
  }
  function paint(box, rec, d) {
    const w = (d.workflows || []).find((x) => x.id === rec.id) || {};
    const days = d.by_day || [];
    const done = days.reduce((s, x) => s + (x.workflow ?? x.ok ?? 0) + (x.failed || 0), 0), bad = days.reduce((s, x) => s + (x.failed || 0), 0);
    const runs = d.kpi?.runs || 0, cost = Number(d.kpi?.cost_usd || 0);
    const kp = el("div", "tm-ukpis");
    const tile = (value, label, sub) => { const c = el("div", "tm-ukpi"); c.append(el("strong", "", value), el("p", "", label), el("small", "", sub)); return c; };
    kp.append(tile(String(runs), "실행", rangeLabel()),
      tile(w.success_rate == null ? "—" : `${Number(w.success_rate).toFixed(1)}%`, "성공률", `끝난 ${done}번 중 실패 ${bad}번`),
      // ≈: list-price estimate. Someone on a subscription is not billed this, so the tile says so in words.
      tile(`≈${usd(cost)}`, "API 요금으로 쳤을 때", runs ? `실제 청구액이 아니에요 · 실행당 ${usd(cost / runs)} · 토큰 ${usageTokens(w.tokens)}` : `실제 청구액이 아니에요 · 토큰 ${usageTokens(w.tokens)}`),
      tile(w.avg_ms ? usageDuration(w.avg_ms) : "—", "평균 시간", w.max_ms ? `가장 긴 실행 ${usageDuration(w.max_ms)}` : "끝난 실행이 없어요"));
    box.replaceChildren(kp, dayChart(days), failing(rec, d.failing_nodes || []));
  }
  // The section loads itself; a reply that comes after the popup closed or the period changed is dropped.
  function section(rec) {
    const sec = el("section", "auto-sec tm-usage flow-usage");
    const head = el("div", "wf-sec-head");
    head.append(el("strong", "", "사용량"), el("span", "grow"), el("small", "auto-dim", "이 워크플로우 실행만 세요. 테스트 실행은 빼요."));
    const ctl = el("div", "tm-uctl");
    const box = el("div", "flow-usage-body");
    let ticket = 0;
    const load = async () => {
      const mine = ++ticket;
      const seg = el("div", "tm-seg"); seg.setAttribute("role", "tablist");
      for (const [k, l] of RANGES) {
        const t = el("button", `tm-seg-tab${k === range ? " active" : ""}`, l); t.type = "button"; t.setAttribute("role", "tab"); t.setAttribute("aria-selected", String(k === range));
        t.addEventListener("click", () => { if (range !== k) { range = k; void load(); } });
        seg.append(t);
      }
      const span = el("span", "tm-urange mono");
      const n = DAYS[range];
      if (n) { const f = (x) => `${x.getMonth() + 1}월 ${x.getDate()}일`; const to = new Date(); span.textContent = n === 1 ? f(to) : `${f(new Date(Date.now() - (n - 1) * 86400000))} – ${f(to)}`; }
      ctl.replaceChildren(seg, span);
      box.replaceChildren(el("p", "auto-hint", "불러오는 중…"));
      let d = null, err = "";
      try { d = await request(`/v1/crew/usage?${query({ range, kind: "workflow", workflow_id: rec.id })}`); } catch (e) { err = e.message; }
      if (mine !== ticket || !sec.isConnected) return;
      if (!d?.kpi) { box.replaceChildren(el("p", "auto-hint auto-empty", err ? `사용량을 불러오지 못했어요 (${err}).` : "사용량을 불러오지 못했어요.")); return; }
      paint(box, rec, d);
    };
    sec.append(head, ctl, box);
    queueMicrotask(load);
    return sec;
  }
  return { section };
}
