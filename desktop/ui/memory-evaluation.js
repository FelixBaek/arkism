// @ts-check
// scheduling is not evidence that a memory helped.
window.ArkismMemoryEvaluation = (() => {
  const operations = new Map();
  function reviewPayload(id, payload) {
    const key = JSON.stringify([id, payload]);
    if (!operations.has(key)) {
      if (operations.size >= 200) operations.delete(operations.keys().next().value);
      operations.set(key, crypto.randomUUID());
    }
    return { ...payload, operation_id: operations.get(key) };
  }
  function describe(summary, target) {
    if (!summary || summary.authority !== "data_only_untrusted" || summary.target?.collection !== target.collection ||
      summary.target.id !== target.id || summary.target.revision !== target.revision) throw new Error("invalid_evaluation_summary");
    const independent = ["user_rating", "automated_check"].reduce((n, k) => n + (summary.groups?.[k]?.count || 0), 0);
    const self = summary.groups?.model_self?.count || 0;
    const observed = independent > 0;
    let label = "관측 없음", tone = "", note = "이 버전의 기억이 도움이 됐는지 아직 확인되지 않았어요.";
    if (observed && summary.passed === false) { label = "관측에서 실패"; tone = "bad"; note = "사용자 평가 또는 자동 검사에 실패 결과가 있어요. 근거와 내용을 확인해 주세요."; }
    else if (observed && summary.passed === true) { label = "관측에서 통과"; tone = "ok"; note = "사용자 평가 또는 자동 검사에 통과했어요. 모든 상황에서 유용하다는 뜻은 아니에요."; }
    else if (observed) { label = "판정 없는 관측"; note = "관측은 남았지만 통과·실패 판정은 아직 없어요."; }
    else if (self) { label = "모델 자기평가만 있음"; note = "만든 모델의 자기평가예요. 독립된 사용 결과는 아직 없어요."; }
    else if (summary.tracking?.status === "pending") label = "관측 대기";
    const tracking = summary.tracking?.status === "pending" ? "관측 대기" : summary.tracking?.status === "done" ? "관측 기록됨" : "추적 기록 없음";
    return { label, tone, note, tracking, independent, self };
  }
  function mount({ container, record, scope, request, el, section, facts, button }) {
    const panel = section("기억 평가"); panel.classList.add("memory-evaluation");
    panel.setAttribute("aria-live", "polite");
    const content = el("div"); panel.append(content); container.append(panel);
    const target = { collection: "memories", id: record.id, revision: record.revision };
    const loads = globalThis.ArkismKeep.latest();
    async function load(again = false) {
      const turn = loads.begin();
      if (!again) content.replaceChildren(el("p", "meta", "관측 결과를 불러오는 중…"));
      try {
        const summary = await request("/v1/evaluations/summary", { method: "POST", body: JSON.stringify({ scope, target }), signal: turn.signal });
        if (!turn.live() || !panel.isConnected) return;
        const result = describe(summary, target);
        const more = el("details", "d-rules"); more.append(el("summary", "", "자세히"), facts([["평가 추적", result.tracking], ["독립된 관측", `${result.independent}건`], ["모델 자기평가", `${result.self}건`]]));
        content.replaceChildren(el("span", `tag ${result.tone}`, result.label), el("p", "meta", result.note), more);
      } catch (_) {
        if (!turn.live() || !panel.isConnected) return;
        // one quiet retry first; only a second failure says so, with no guess about why
        if (!again) { await new Promise((r) => setTimeout(r, 1500)); if (turn.live() && panel.isConnected) return load(true); return; }
        content.replaceChildren(el("p", "meta", "평가를 불러오지 못했어요."), button("다시 시도", "ghost small", null, () => load()));
      }
    }
    void load();
    return panel;
  }
  return { reviewPayload, describe, mount };
})();
