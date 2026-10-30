// @ts-check
// Server reservations are exposure, not the provider's final invoice.
window.ArkismModelBudget = (() => {
  const fields = ["monthly_limit_pico_usd", "general_limit_pico_usd", "daily_soft_target_pico_usd", "monthly_committed_pico_usd", "general_committed_pico_usd", "daily_committed_pico_usd", "outstanding_pico_usd"];
  const dollars = n => `$${(n / 1e12).toFixed(2)}`;
  const usd = n => `$${n.toFixed(2)}`;
  // The server's month starts at 00:00 UTC; say when that is on the reader's clock instead of naming a time zone.
  const resetLine = () => {
    const d = new Date(Date.UTC(2000, 1, 1)), t = d.toLocaleTimeString(globalThis.ArkismI18n?.locale() || "ko-KR", { hour: "2-digit", minute: "2-digit", hour12: false });
    return d.getDate() === 1 ? `매달 1일 ${t}에 새로 세요` : `매달 마지막 날 ${t}에 새로 세요`;
  };
  function validate(doc) {
    if (doc && doc.configured === false) {
      if (!/^\d{4}-\d{2}$/.test(doc.month) || [doc.routing_budget_usd, doc.routing_month_cost_usd, doc.routing_revision].some(n => n !== undefined && (typeof n !== "number" || !(n >= 0)))) throw new Error("invalid_budget");
      return doc;
    }
    if (!doc || !/^\d{4}-\d{2}$/.test(doc.month) || fields.some(k => !Number.isSafeInteger(doc[k]) || doc[k] < 0)) throw new Error("invalid_budget");
    if (typeof doc.daily_soft_target_exceeded !== "boolean" || (doc.daily_limit_pico_usd !== undefined && (!Number.isSafeInteger(doc.daily_limit_pico_usd) || doc.daily_limit_pico_usd < 0))) throw new Error("invalid_budget");
    return doc;
  }
  function mount({ container, scope, request, el, button }) {
    const panel = el("section", "us-card model-budget"); panel.setAttribute("aria-live", "polite");
    const title = el("h3", "", "Claude API 월 예산"); panel.append(title);
    const body = el("div"); panel.append(body); container.prepend(panel);
    const loads = globalThis.ArkismKeep.latest();
    async function load() {
      const turn = loads.begin();
      body.replaceChildren(el("p", "meta", "예산을 불러오는 중…"));
      try {
        const doc = validate(await request("/v1/model-budget/status", { method: "POST", body: JSON.stringify({ scope }), signal: turn.signal }));
        if (!turn.live() || !panel.isConnected) return;
        if (doc.configured === false) { title.textContent = "월 예산"; noBudget(doc); return; }
        title.textContent = "Claude API 월 예산";
        const rows = el("dl", "facts");
        for (const [label, used, limit] of [["월 전체", doc.monthly_committed_pico_usd, doc.monthly_limit_pico_usd], ["일반 처리", doc.general_committed_pico_usd, doc.general_limit_pico_usd], ["오늘 목표", doc.daily_committed_pico_usd, doc.daily_soft_target_pico_usd]]) {
          rows.append(el("dt", "", label), el("dd", "", `${dollars(used)} / ${dollars(limit)}`));
        }
        if (doc.daily_limit_pico_usd > 0) rows.append(el("dt", "", "오늘 상한"), el("dd", "", dollars(doc.daily_limit_pico_usd)));
        rows.append(el("dt", "", "미정산 예약"), el("dd", "", dollars(doc.outstanding_pico_usd)));
        // Subscription CLI calls are recorded only; they never count against the API budget above.
        if (["subscription_requests", "subscription_estimated_pico_usd"].every(k => Number.isSafeInteger(doc[k]) && doc[k] >= 0)) rows.append(el("dt", "", "구독"), el("dd", "", `${doc.subscription_requests}회 · API 환산 ${dollars(doc.subscription_estimated_pico_usd)} (예산 밖)`));
        body.replaceChildren(el("p", "meta", `${doc.month} · ${resetLine()}`), rows,
          el("p", "meta", "사용 확정액과 미정산 예약을 함께 계산해요. 실제 청구 금액과 다를 수 있어요."));
        body.append(el("p", "meta", doc.daily_soft_target_exceeded ? "오늘 목표를 넘었어요. 목표 초과만으로 처리를 차단하지 않으며 월 예산과 별도 설정한 상한은 계속 적용해요." : "오늘 목표는 지출 추이를 보는 기준이에요. 월 예산과 별도 설정한 상한은 계속 적용해요."));
      } catch (e) {
        if (!turn.live() || !panel.isConnected) return;
        // No budget set up here (most people never use the API key): no card at all.
        if (e?.message === "not_found") { panel.remove(); return; }
        body.replaceChildren(el("p", "meta", "예산을 불러오지 못했어요. 잠시 뒤 다시 조회해 주세요."));
      }
      if (turn.live() && panel.isConnected) body.append(button("예산 다시 조회", "ghost small", null, load));
    }
    // No server budget policy: one monthly amount the person sets, kept with the routing policy. It is shown, never enforced.
    function noBudget(doc) {
      const budget = doc.routing_budget_usd || 0, cost = doc.routing_month_cost_usd || 0;
      const input = el("input"); input.type = "number"; input.min = "0"; input.step = "1"; input.inputMode = "decimal";
      input.value = budget ? String(budget) : ""; input.placeholder = "예: 20"; input.setAttribute("aria-label", "월 예산 (달러)");
      const msg = el("p", "meta");
      const save = button("저장", "primary small", null, async () => {
        const v = input.value.trim() === "" ? 0 : Number(input.value);
        if (!Number.isFinite(v) || v < 0 || v > 100000) { msg.textContent = "0에서 100000 사이 금액을 넣어 주세요."; return; }
        save.disabled = true;
        try {
          await request("/v1/routing/policy", { method: "POST", body: JSON.stringify({ scope, expected_revision: doc.routing_revision || 0, monthly_budget_usd: v }) });
          return load();
        } catch (_) { msg.textContent = "저장하지 못했어요. 다른 곳에서 먼저 바꿨을 수 있어요. 다시 조회한 뒤 해 주세요."; save.disabled = false; }
      });
      const lab = el("label"); lab.append(el("span", "", "월 예산 ($)"), input);
      const row = el("div", "model-budget-input"); row.append(lab, save);
      body.replaceChildren(
        el("p", "", budget ? `이번 달 약 ${usd(cost)} / 월 예산 ${usd(budget)}` : `아직 월 예산이 없어요. 이번 달 약 ${usd(cost)}를 썼어요.`),
        row, msg, el("p", "meta", `${doc.month} · ${resetLine()} · 예산을 넘어도 요청을 막지 않고 알려만 줘요.`));
    }
    void load();
    return panel;
  }
  return { validate, mount };
})();
