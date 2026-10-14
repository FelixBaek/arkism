window.ArkismMemoryTime = (ctx) => {
  "use strict";
  const { state, $, el, icon, button, request, detailShell, openDetail } = ctx;
  const identity = () => JSON.stringify([state.server, state.token, state.scope]);
  const active = () => state.view === "memories" && state.memTab === "past";
  const date = (value) => new Date(value).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR");
  const localNow = () => { const now = new Date(); return new Date(now - now.getTimezoneOffset() * 60000).toISOString().slice(0, 16); };
  let key = "", epoch = 0, rows = [], next = "", busy = false, error = "", selected = null;
  let when = localNow(), term = "", asOf = "", searched = false;
  function reset() {
    epoch++; key = ""; rows = []; next = ""; busy = false; error = ""; selected = null;
    when = localNow(); term = ""; asOf = ""; searched = false;
  }
  function detail() {
    const body = detailShell("과거 기억");
    if (!selected) { body.append(el("h2", "", "날짜를 골라 확인하세요"), el("p", "lead", "과거에 저장된 자료예요. 지금도 유효한 지시나 승인을 뜻하지 않아요.")); return; }
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", `${date(asOf)} 기준`), el("h2", "", selected.reason ? "과거 상태를 확인할 수 없음" : selected.text || selected.summary || "본문 없음"));
    body.append(head);
    if (selected.reason) {
      body.append(el("p", "lead", selected.reason === "content_unavailable" ? "당시 본문이 보존되어 있지 않아 확인할 수 없어요." : "이 시각의 상태 이력이 기록되지 않아 확인할 수 없어요."));
      return;
    }
    head.append(el("p", "lead", "그 시각에 저장돼 있던 내용이에요. 지금도 맞는지는 따로 확인해야 해요."));
    const s = el("section", "section"), facts = el("dl", "facts");
    facts.append(el("dt", "", "당시 판"), el("dd", "", `${selected.historical_revision} (지금은 ${selected.current_revision})`), el("dt", "", "상태 기록"), el("dd", "", date(selected.observed_at)));
    s.append(el("h3", "", "기록"), facts);
    body.append(s, el("p", "meta", "지금 기억과 다르면 기억 화면에서 고치세요."));
  }
  function paint() {
    const table = $("#table"); table.replaceChildren(); table.setAttribute("aria-busy", String(busy));
    const status = el("p", "meta original-notice"); status.setAttribute("role", "status");
    status.textContent = busy ? "과거 기억을 불러오는 중…" : error || (!searched ? "날짜와 시간을 골라 조회하세요. 기기의 현지 시각을 사용해요." : rows.length ? `${rows.length}개 항목 · ${date(asOf)} 기준` : "해당 시각에 표시할 기억이 없어요.");
    table.append(status);
    for (const item of rows) {
      const row = el("button", `original-row${selected === item ? " selected" : ""}`); row.type = "button";
      const body = el("span", "original-body");
      body.append(el("strong", "", item.reason ? "과거 상태를 확인할 수 없음" : item.text || item.summary || "본문 없음"));
      body.append(el("span", "meta", item.reason === "history_not_recorded" ? "상태 기록 없음" : item.reason ? "당시 본문을 확인할 수 없어요" : `당시 판 ${item.historical_revision}${item.current_revision !== item.historical_revision ? ` (지금 판 ${item.current_revision})` : ""}`));
      row.append(body, el("span", `mem-badge${item.reason ? " warn" : ""}`, item.reason ? "확인 불가" : "당시 사용 중"));
      row.addEventListener("click", () => { selected = item; paint(); openDetail(); detail(); const heading = $("#detail h2"); heading.tabIndex = -1; heading.focus(); });
      table.append(row);
    }
    if (next || error) {
      const more = button(error ? "다시 조회" : "과거 기억 더 보기", "small", null, () => load(!error));
      more.disabled = busy; table.append(more);
    }
    $("#chips button[type=submit]")?.toggleAttribute("disabled", busy);
  }
  async function load(more = false) {
    if (busy || !active()) return;
    const requested = more ? next : "";
    if (!asOf) return;
    if (!more) { rows = []; next = ""; selected = null; detail(); }
    const ticket = ++epoch, current = identity(); busy = true; error = ""; paint();
    const live = () => active() && ticket === epoch && current === identity();
    try {
      const page = await request("/v1/memories/at", { method: "POST", body: JSON.stringify({ scope: state.scope, as_of: asOf, q: term, limit: 20, cursor: requested }) });
      if (!live()) return;
      if (page?.authority !== "data_only_untrusted" || new Date(page.as_of).getTime() !== new Date(asOf).getTime() || !Array.isArray(page.items) || !Array.isArray(page.unknown) || typeof page.next_cursor !== "string" || (page.next_cursor && page.next_cursor === requested)) throw new Error("invalid_response");
      if (page.items.some((item) => typeof item.id !== "string" || typeof item.text !== "string") || page.unknown.some((item) => typeof item.id !== "string" || !["history_not_recorded", "content_unavailable"].includes(item.reason))) throw new Error("invalid_response");
      rows.push(...page.items, ...page.unknown); next = page.next_cursor; searched = true;
    } catch (_) {
      if (!live()) return;
      rows = []; selected = null; next = ""; error = "불러오지 못했어요. 연결과 접근 권한을 확인하고 다시 조회하세요."; detail();
    } finally { if (live()) { busy = false; paint(); } }
  }
  function draw() {
    const changed = key !== identity();
    if (changed) { reset(); key = identity(); }
    if (!changed && $("#chips .memory-time-form")) { paint(); return; }
    $("#chips").replaceChildren(); $("#stats").replaceChildren();
    const form = el("form", "original-search memory-time-form");
    const timeLabel = el("label", "mem-when");
    timeLabel.setAttribute("aria-label", "조회 시각 (현지 시간)");
    const time = el("input"); time.type = "datetime-local"; time.required = true; time.value = when; time.max = localNow(); timeLabel.append(time);
    const queryLabel = el("label", "panel-search");
    const query = el("input"); query.type = "search"; query.value = term; query.maxLength = 256; query.placeholder = "그때 기억에서 찾기"; query.setAttribute("aria-label", "과거 기억 검색 (선택)");
    queryLabel.append(icon("magnifying-glass"), query);
    const submit = button("과거 조회", "primary small"); submit.type = "submit";
    form.append(timeLabel, queryLabel, submit);
    form.addEventListener("submit", (event) => {
      event.preventDefault(); if (busy) return;
      const parsed = new Date(time.value);
      if (!Number.isFinite(parsed.getTime()) || parsed > new Date()) { time.setCustomValidity("현재보다 이전 시각을 입력하세요."); time.reportValidity(); return; }
      if (new TextEncoder().encode(query.value.trim()).length > 256) { query.setCustomValidity("검색어를 조금 짧게 입력하세요."); query.reportValidity(); return; }
      when = time.value; term = query.value.trim(); asOf = parsed.toISOString(); void load();
    });
    time.addEventListener("input", () => time.setCustomValidity("")); query.addEventListener("input", () => query.setCustomValidity(""));
    $("#chips").append(form); paint();
  }
  function close() { const index = rows.indexOf(selected); selected = null; paint(); detail(); if (index >= 0) $("#table").querySelectorAll(".original-row")[index]?.focus(); }
  return { draw, reset, detail, close };
};
