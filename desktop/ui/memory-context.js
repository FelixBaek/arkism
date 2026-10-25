// @ts-check
/* Canonical evidence metadata: labels describe usage, never access rights. */
(function () {
  "use strict";
  window.ArkismMemoryContext = function ({ el }) {
    const domains = { personal: "개인", work: "업무" };
    const evidence = { user_statement: "사용자 진술", tool_observation: "도구 관측", agent_statement: "에이전트 진술", inference: "추론" };
    const kinds = { fact: "사실", preference: "선호", correction: "정정", lesson: "교훈", failure_recovery: "실패 복구" };
    return function render(record) {
      const context = record?.memory_context;
      if (!context || !Array.isArray(context.domains)) return null;
      const panel = el("section", "memory-context-panel");
      panel.setAttribute("aria-label", "기억 맥락");
      panel.append(el("p", "", "기억 맥락"), el("p", "", context.domains.map(x => domains[x] || "미분류").join(" · ")));
      panel.append(el("p", "meta", "함께 적용되는 상황을 기억해요."));
      const at = context.observed_at ? new Date(context.observed_at) : null;
      const rows = [
        ["적용 조건", context.applicability || "조건이 기록되지 않았어요."],
        ["근거 유형", evidence[context.evidence_kind] || "미분류"],
        ["기억 유형", kinds[context.semantic_kind] || "미분류"],
        ["관측 시각", at && Number.isFinite(at.getTime()) ? at.toLocaleString("ko-KR") : "기록되지 않았어요."],
        ["적용 시점", context.valid_from || "기록되지 않았어요."],
        ["불확실성", context.uncertainty || "별도 설명이 없어요."],
      ];
      for (const [label, value] of rows) panel.append(el("p", "meta", `${label}   ${value}`));
      return panel;
    };
  };
})();
