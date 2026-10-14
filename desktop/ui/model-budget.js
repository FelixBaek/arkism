// Server reservations are exposure, not the provider's final invoice.
window.ArkismModelBudget = (() => {
  const fields = ["monthly_limit_pico_usd", "general_limit_pico_usd", "daily_soft_target_pico_usd", "monthly_committed_pico_usd", "general_committed_pico_usd", "daily_committed_pico_usd", "outstanding_pico_usd"];
  const dollars = n => `$${(n / 1e12).toFixed(2)}`;
  function validate(doc) {
    if (!doc || !/^\d{4}-\d{2}$/.test(doc.month) || fields.some(k => !Number.isSafeInteger(doc[k]) || doc[k] < 0)) throw new Error("invalid_budget");
    if (typeof doc.daily_soft_target_exceeded !== "boolean" || (doc.daily_limit_pico_usd !== undefined && (!Number.isSafeInteger(doc.daily_limit_pico_usd) || doc.daily_limit_pico_usd < 0))) throw new Error("invalid_budget");
    return doc;
  }
  function mount({ container, scope, request, el, button }) {
    const panel = el("section", "us-card model-budget"); panel.setAttribute("aria-live", "polite");
    panel.append(el("h3", "", "Claude API 월 예산"));
    const body = el("div"); panel.append(body); container.prepend(panel);
    let serial = 0;
    async function load() {
      const seq = ++serial;
      body.replaceChildren(el("p", "meta", "예산을 불러오는 중…"));
      try {
        const doc = validate(await request("/v1/model-budget/status", { method: "POST", body: JSON.stringify({ scope }) }));
        if (seq !== serial || !panel.isConnected) return;
        const rows = el("dl", "facts");
        for (const [label, used, limit] of [["월 전체", doc.monthly_committed_pico_usd, doc.monthly_limit_pico_usd], ["일반 처리", doc.general_committed_pico_usd, doc.general_limit_pico_usd], ["오늘 목표", doc.daily_committed_pico_usd, doc.daily_soft_target_pico_usd]]) {
          rows.append(el("dt", "", label), el("dd", "", `${dollars(used)} / ${dollars(limit)}`));
        }
        if (doc.daily_limit_pico_usd > 0) rows.append(el("dt", "", "오늘 상한"), el("dd", "", dollars(doc.daily_limit_pico_usd)));
        rows.append(el("dt", "", "미정산 예약"), el("dd", "", dollars(doc.outstanding_pico_usd)));
        body.replaceChildren(el("p", "meta", `${doc.month} · UTC 기준`), rows,
          el("p", "meta", "사용 확정액과 미정산 예약을 함께 계산해요. 실제 청구 금액과 다를 수 있어요."));
        body.append(el("p", "meta", doc.daily_soft_target_exceeded ? "오늘 목표를 넘었어요. 목표 초과만으로 처리를 차단하지 않으며 월 예산과 별도 설정한 상한은 계속 적용해요." : "오늘 목표는 지출 추이를 보는 기준이에요. 월 예산과 별도 설정한 상한은 계속 적용해요."));
      } catch (_) {
        if (seq !== serial || !panel.isConnected) return;
        body.replaceChildren(el("p", "meta", "예산 설정을 확인할 수 없어요. 서버 설정과 조회 권한을 확인해 주세요."));
      }
      if (seq === serial && panel.isConnected) body.append(button("예산 다시 조회", "ghost small", null, load));
    }
    void load();
    return panel;
  }
  return { validate, mount };
})();
