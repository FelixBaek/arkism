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
  function mount({ container, record, scope, request, el, section, facts, onFix }) {
    const panel = section("기억 평가"); panel.classList.add("memory-evaluation");
    panel.setAttribute("aria-live", "polite");
    const content = el("div"); panel.append(content); container.append(panel);
    const target = { collection: "memories", id: record.id, revision: record.revision };
    const loads = globalThis.ArkismKeep.latest();
    function action(label, run) {
      const b = el("button", "btn ghost small", label); b.type = "button";
      b.addEventListener("click", run); return b;
    }
    function policyView() {
      const box = el("div"), note = el("p", "meta"), policyLoads = globalThis.ArkismKeep.latest();
      const show = action("현재 순위 기준 보기", async () => {
        const turn = policyLoads.begin(); show.disabled = true;
        try {
          const result = await request(`/v1/memories/${encodeURIComponent(record.id)}/signal-policy?${new URLSearchParams(scope)}`, { signal: turn.signal });
          if (!turn.live() || !box.isConnected) return;
          if (result.authority !== "data_only_untrusted" || result.memory?.id !== record.id || result.memory.revision !== record.revision || !["legacy", "recent_first_trace", "disabled"].includes(result.mode) || !Number.isFinite(result.effective_factor)) throw new Error("invalid_policy");
          const recent = result.mode === "recent_first_trace";
          note.textContent = result.mode === "disabled" ? "현재 이 기억에는 순위 가중치를 적용하지 않아요." : recent ? "현재 내용의 기간·종류가 맞는 평가에서 같은 작업의 첫 결과만 반영해요." : "기존 전체 이력 정책이에요. 저장된 신호를 기준으로 계산해요.";
          const values = [["기준 시각", new Date(result.as_of).toLocaleString()], ["전체 평가 이력", `${result.historical_total}건`]];
          if (recent) values.push(["평가 종류", result.policy?.dimension === "work_task" ? "업무 결과" : "개인 규칙 준수"], ["평가 기간", `${result.policy?.window_days}일`], ["현재 내용·기간의 이벤트", `${result.window_events}건`], ["중복·귀속 부족으로 제외", `${result.excluded_window_events}건`], ["적용 표본", `${result.eligible_total}건 · 통과 ${result.eligible_passed}건`], ["최소 표본", `${result.policy?.min_samples}건 · ${result.minimum_met ? "충족" : "미충족 (평가 가중치 1)"}`]);
          values.push(["평가 가중치", String(result.evaluation_factor)], ["업무 관측 가중치", String(result.work_factor)], ["합산 가중치", String(result.effective_factor)]);
          box.replaceChildren(note, facts(values), el("p", "meta", "일반 검색의 기준값이에요. 다른 기준 시각이나 별도 동적 순위는 다를 수 있어요. 반환·전달·실제 사용·성공 원인은 서로 달라요."), show);
          show.textContent = "순위 기준 새로고침";
        } catch (_) { if (turn.live() && box.isConnected) { note.textContent = "순위 기준을 불러오지 못했어요."; show.textContent = "순위 기준 다시 시도"; } }
        finally { if (turn.live() && box.isConnected) show.disabled = false; }
      });
      box.append(note, show); return box;
    }
    function historyView() {
      const box = el("div"), list = el("div"), note = el("p", "meta");
      const historyLoads = globalThis.ArkismKeep.latest();
      let cursor = "";
      const next = action("실제 평가 이력 보기", async () => {
        const turn = historyLoads.begin(); next.disabled = true;
        note.textContent = "평가 이력을 불러오는 중…";
        const query = new URLSearchParams({ ...scope, limit: "20", ...(cursor ? { cursor } : {}) });
        try {
          const page = await request(`/v1/memories/${encodeURIComponent(record.id)}/outcomes?${query}`, { signal: turn.signal });
          if (!turn.live() || !box.isConnected) return;
          if (page.authority !== "data_only_untrusted" || page.memory?.id !== record.id || page.memory.collection !== "memories" || !Array.isArray(page.items) || page.items.length > 20 || (page.next_cursor != null && typeof page.next_cursor !== "string")) throw new Error("invalid_outcomes");
          const entries = page.items.map(item => {
            if (item.memory?.id !== record.id || item.memory.collection !== "memories" || !Number.isInteger(item.memory.revision) || typeof item.passed !== "boolean" || !["work_task", "personal_compliance"].includes(item.dimension) || !Number.isFinite(Date.parse(item.judged_at))) throw new Error("invalid_outcome");
            const entry = el("div", "d-rules");
            entry.append(el("p", "", `${item.passed ? "통과" : "실패"} · ${item.dimension === "work_task" ? "업무 결과" : "개인 규칙 준수"} · 버전 ${item.memory.revision}${item.memory.revision === record.revision ? " (현재)" : " (과거)"}`), el("p", "meta", new Date(item.judged_at).toLocaleString()));
            if (item.trace?.collection === "execution_traces" && typeof item.trace.id === "string" && Number.isInteger(item.trace.revision)) {
              const traceNote = el("p", "meta"), traceLoads = globalThis.ArkismKeep.latest();
              const open = action("작업 근거 보기", async () => {
                open.disabled = true; const traceTurn = traceLoads.begin();
                try {
                  const trace = await request(`/v1/execution_traces/${encodeURIComponent(item.trace.id)}?${new URLSearchParams(scope)}`, { signal: traceTurn.signal });
                  if (!traceTurn.live() || !entry.isConnected) return;
                  if (trace.id !== item.trace.id || trace.revision !== item.trace.revision || !trace.scope || Object.entries(scope).some(([key, value]) => (trace.scope[key] || "") !== (value || ""))) throw new Error("changed_trace");
                  traceNote.textContent = typeof trace.text === "string" && trace.text ? trace.text.slice(0, 4000) + (trace.text.length > 4000 ? "… (일부만 표시)" : "") : "이 작업에는 표시할 설명이 없어요.";
                } catch (_) { if (traceTurn.live() && entry.isConnected) traceNote.textContent = "작업 근거를 열지 못했어요. 다시 시도해 주세요."; }
                finally { if (entry.isConnected) open.disabled = false; }
              }); entry.append(open, traceNote);
            }
            return entry;
          });
          if (!cursor) list.replaceChildren();
          list.append(...entries); cursor = page.next_cursor || "";
          next.hidden = !cursor; next.textContent = "평가 더 보기";
          note.textContent = entries.length ? "과거 기록 버전의 평가도 포함돼요. 버전은 내용 외 정보가 바뀌어도 달라질 수 있어요." : "독립된 작업 평가가 아직 없어요.";
        } catch (_) { if (turn.live() && box.isConnected) { note.textContent = "평가 이력을 불러오지 못했어요."; next.textContent = "다시 시도"; } }
        finally { if (turn.live() && box.isConnected) next.disabled = false; }
      });
      box.append(note, list, next);
      if (onFix) box.append(action("기억 내용 고치기", onFix));
      return box;
    }
    async function load(again = false) {
      const turn = loads.begin();
      if (!again) content.replaceChildren(el("p", "meta", "관측 결과를 불러오는 중…"));
      try {
        const summary = await request("/v1/evaluations/summary", { method: "POST", body: JSON.stringify({ scope, target }), signal: turn.signal });
        if (!turn.live() || !panel.isConnected) return;
        const result = describe(summary, target);
        const more = el("details", "d-rules"); more.append(el("summary", "", "자세히"), facts([["평가 추적", result.tracking], ["독립된 관측", `${result.independent}건`], ["모델 자기평가", `${result.self}건`]]));
        content.replaceChildren(el("span", `tag ${result.tone}`, result.label), el("p", "meta", result.note), more, policyView(), historyView());
      } catch (_) {
        if (!turn.live() || !panel.isConnected) return;
        // Retry once quietly, then offer an explicit retry without guessing the cause.
        if (!again) { await new Promise((r) => setTimeout(r, 1500)); if (turn.live() && panel.isConnected) return load(true); return; }
        content.replaceChildren(el("p", "meta", "평가를 불러오지 못했어요."), action("다시 시도", () => load()));
      }
    }
    void load();
    return panel;
  }
  return { reviewPayload, describe, mount };
})();
