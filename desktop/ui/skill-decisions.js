// @ts-check
// TOOLS › 관리: recent skill decisions for route_skills.
window.ArkismSkillDecisions = (ctx) => {
  "use strict";
  const { el, ago } = ctx;
  const raw = (n) => { n.setAttribute("data-no-i18n", ""); return n; };
  const tr = (s) => (window.ArkismI18n ? window.ArkismI18n.translate(s) : s);
  const num = (n) => Number(n || 0).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR");

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
  // Pairs the judge found both fitting the same question (d.rival), seen twice or more, most frequent first.
  function overlaps(items) {
    const pairs = new Map();
    for (const d of items) {
      if (!d.rival || !d.selected) continue;
      const [a, b] = [[d.selected, d.selected_name], [d.rival, d.rival_name]].sort((x, y) => (x[0] < y[0] ? -1 : 1));
      const p = pairs.get(a[0] + "|" + b[0]) || { a, b, n: 0, wins: 0 };
      p.n++; if (d.selected === a[0]) p.wins++;
      pairs.set(a[0] + "|" + b[0], p);
    }
    return [...pairs.values()].filter((p) => p.n > 1).sort((x, y) => y.n - x.n).slice(0, 5);
  }
  function overlapRow(p) {
    const r = el("div", "rtg-call rtg-skill");
    const name = ([id, n]) => { const s = raw(el("span", "rtg-model", n || id)); s.title = id; return s; };
    r.append(name(p.a), el("span", "rtg-num", "↔"), name(p.b), el("span", "rtg-num", tr(`${num(p.n)}번 겹침`)),
      el("span", "rtg-num", tr(`${num(p.wins)} : ${num(p.n - p.wins)}`)));
    return r;
  }
  // null when the server keeps no decisions yet.
  function skillCard(items) {
    if (!items?.length) return null;
    const c = el("div", "mg-box rtg-card");
    const head = el("div", "mg-head");
    head.append(el("h3", "", "스킬 판정"), el("span", "", tr(`최근 ${num(items.length)}건`)));
    const by = el("div", "rtg-sides");
    const count = {};
    const bad = new Set();
    for (const d of items) for (const k of [judge(d.provider), ...(d.degraded || []).map((x) => (bad.add(DEGRADED[x] || x), DEGRADED[x] || x))]) count[k] = (count[k] || 0) + 1;
    for (const [k, n] of Object.entries(count).sort((a, b) => b[1] - a[1])) by.append(el("span", bad.has(k) ? "rtg-chip warn" : "rtg-chip", tr(`${k} ${num(n)}`)));
    const list = el("div", "rtg-calls rtg-skills");
    list.append(...items.slice(0, SKILLS_SHOWN).map(skillRow));
    c.append(head, el("small", "", "route_skills가 고른 스킬과 판정기예요. 규칙은 판정 모델 없이 검색 1위를 고른 것이고, 확신 낮음은 판정기가 고르지 않은 것이에요."), by, list);
    const pairs = overlaps(items);
    if (pairs.length) {
      const o = el("div", "rtg-calls rtg-skills");
      o.append(...pairs.map(overlapRow));
      c.append(el("p", "rtg-label", "겹치는 스킬"), el("small", "", "같은 질문에 둘 다 맞다고 판정된 스킬이에요. 숫자는 각각 뽑힌 횟수예요. 자주 겹치면 설명에서 쓰는 때를 나누거나 하나로 합치세요."), o);
    }
    return c;
  }
  return skillCard;
};
