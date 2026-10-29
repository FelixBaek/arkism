// @ts-check
// ROUTER › 라우팅 현황: Claude Code calls through the :8099 gateway (overview `gateway`, see
// internal/memory/routing_gateway_view.go). 본대화 and 부속 호출 (Claude Code's own side calls: titles,
// summaries, compaction …) are counted apart, and the latest agent turns list each call of the turn.
// Also the 스킬 판정 card: recent route_skills decisions.
window.ArkismRoutingTurns = (ctx) => {
  "use strict";
  const { el, ago } = ctx;
  const raw = (n) => { n.setAttribute("data-no-i18n", ""); return n; };
  const tr = (s) => (window.ArkismI18n ? window.ArkismI18n.translate(s) : s);
  const SIDE = { fork: "갈래 작업", query: "짧은 질의", compaction: "대화 압축", auxiliary: "보조 작업", side_reply: "곁가지 답" };
  const num = (n) => Number(n || 0).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR");
  const usd = (n) => `$${Number(n || 0).toFixed(2)}`;
  const open = new Set(); // turns shown open: `${session}:${seq}`

  function tile(title, st, sub) {
    const t = el("div", "rtg-tile");
    const v = el("div", "rtg-value");
    v.append(el("strong", "", num(st.requests)), el("span", "", "건"));
    t.append(el("p", "", title), v, el("small", "", tr(`실패 ${num(st.failures)}건 · 토큰 ${num((st.input_tokens || 0) + (st.output_tokens || 0))} · ${usd(st.cost_usd)}`) + (st.estimated ? ` ${tr("추정")}` : "")));
    if (sub) t.append(sub);
    return t;
  }
  function callRow(c) {
    const r = el("div", `rtg-call${c.ok ? "" : " bad"}`);
    r.append(el("span", c.side ? "rtg-tag side" : "rtg-tag", c.side ? SIDE[c.side] || c.side : "본대화"), raw(el("span", "rtg-model mono", c.model)),
      el("span", "rtg-ok", c.ok ? "성공" : "실패"), el("span", "rtg-num", tr(`${(c.latency_ms / 1000).toFixed(1)}초`)),
      el("span", "rtg-num", tr(`토큰 ${num((c.input_tokens || 0) + (c.output_tokens || 0))}`)));
    if (c.error) r.title = c.error;
    return r;
  }
  function turnRow(t, redraw) {
    const key = `${t.session_id}:${t.turn_seq}`;
    const box = el("div", "rtg-turn");
    const head = el("button", "rtg-turn-head");
    head.type = "button";
    head.setAttribute("aria-expanded", String(open.has(key)));
    const fails = t.calls.filter((c) => !c.ok).length;
    head.append(raw(el("span", "rtg-session mono", t.session_id.slice(0, 8))), el("span", "rtg-seq", tr(`${t.turn_seq}번째 턴`)),
      el("span", "rtg-count", tr(`본대화 ${t.main_calls} · 부속 ${t.side_calls}`)), ...(fails ? [el("span", "rtg-fail", tr(`실패 ${fails}`))] : []),
      el("span", "rtg-when", ago ? ago(t.at) : ""));
    head.addEventListener("click", () => { if (open.has(key)) open.delete(key); else open.add(key); redraw(); });
    box.append(head);
    if (open.has(key)) { const list = el("div", "rtg-calls"); list.append(...t.calls.map(callRow)); box.append(list); }
    return box;
  }
  // The card for the routing main view; null when the server sends no gateway part or nothing went through it.
  function card(g) {
    if (!g || !(g.main?.requests || g.side?.requests)) return null;
    const c = el("div", "rt-card wide rtg-card");
    const draw = () => {
      const head = el("div", "rt-card-head");
      head.append(el("strong", "", "Claude Code 호출"), el("span", "", "본대화와 부속 호출을 따로 세요"));
      const by = el("div", "rtg-sides");
      for (const [k, n] of Object.entries(g.side.by_side || {}).sort((a, b) => b[1] - a[1])) by.append(el("span", "rtg-chip", tr(`${SIDE[k] || k} ${num(n)}`)));
      const tiles = el("div", "rtg-tiles");
      tiles.append(tile("본대화", g.main), tile("부속 호출", g.side, by));
      const kids = [head, el("small", "", "부속 호출은 Claude Code가 제목·요약·대화 압축 같은 일로 따로 보내는 호출이에요. 비용은 공개 가격으로 셈한 추정이에요."), tiles];
      if ((g.turns || []).length) {
        kids.push(el("p", "rtg-label", "최근 턴"));
        const list = el("div", "rtg-turns");
        list.append(...g.turns.map((t) => turnRow(t, draw)));
        kids.push(list);
      }
      c.replaceChildren(...kids);
    };
    draw();
    return c;
  }
  // route_skills decisions (GET /v1/routing/skills): which skill, which judge, and why it fell back.
  const DEGRADED = { jev_unavailable: "판정 모델 없음", jev_uncertain: "확신 낮음", transfer_denied: "보낼 수 없는 스킬" };
  const judge = (p) => (p === "rules" ? "규칙" : /^clef/i.test(p) ? "Clef" : /^jev/i.test(p) ? "Jev" : p);
  const SKILLS_SHOWN = 10;
  // What the agent did with the pick: the run's outcome once reported, else whether it loaded one.
  const OUTCOME = { success: "성공", failure: "실패", cancelled: "취소됨", validation_failed: "검증 실패" };
  const used = (d) => !d.loaded ? "불러오지 않음" : `${d.loaded === d.selected ? "" : "다른 후보 · "}${OUTCOME[d.outcome] || "불러옴"}`;
  function skillRow(d) {
    const r = el("div", `rtg-call rtg-skill${d.confirmed ? "" : " bad"}`);
    const why = (d.degraded || []).map((k) => DEGRADED[k] || k).join(" · ");
    const name = raw(el("span", "rtg-model", d.selected_name || d.selected || tr("고른 스킬 없음")));
    if (d.selected) name.title = d.selected;
    r.append(el("span", "rtg-tag", judge(d.provider)), name, el("span", "rtg-ok", d.selected ? (why || "확정") : why || "후보 없음"),
      el("span", "rtg-num", tr(`후보 ${d.candidates}`)), el("span", "rtg-num", used(d)), el("span", "rtg-num", ago ? ago(d.at) : ""));
    return r;
  }
  // null when the server keeps no decisions yet.
  function skillCard(items) {
    if (!items?.length) return null;
    const c = el("div", "rt-card wide rtg-card");
    const head = el("div", "rt-card-head");
    head.append(el("strong", "", "스킬 판정"), el("span", "", tr(`최근 ${num(items.length)}건`)));
    const by = el("div", "rtg-sides");
    const count = {};
    for (const d of items) for (const k of [judge(d.provider), ...(d.degraded || []).map((x) => DEGRADED[x] || x)]) count[k] = (count[k] || 0) + 1;
    for (const [k, n] of Object.entries(count).sort((a, b) => b[1] - a[1])) by.append(el("span", "rtg-chip", tr(`${k} ${num(n)}`)));
    const list = el("div", "rtg-calls rtg-skills");
    list.append(...items.slice(0, SKILLS_SHOWN).map(skillRow));
    c.append(head, el("small", "", "route_skills가 고른 스킬과 판정기예요. 규칙은 판정 모델 없이 검색 1위를 고른 것이고, 확신 낮음은 판정기가 고르지 않은 것이에요."), by, list);
    return c;
  }
  return { card, skillCard };
};
