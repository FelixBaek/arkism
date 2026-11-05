window.ArkismMemoryTime = (ctx) => {
  "use strict";
  const { state, $, el, icon, button, request, detailShell, openDetail, query: scopeQuery, openRecord } = ctx;
  // 시간순: memories laid out by when they happened, in three lanes; the time words go to the server's parser (POST /v1/search explain)
  const LANES = { all: "전체", facts: "사실", events: "겪은 일", observations: "정리된 관찰" };
  const FOUND = { exact: "같은 문구", keyword: "단어", vector: "비슷한 뜻", relation: "관계", temporal: "시간", graph: "이어진 기억" };
  const laneOf = (h) => h.derived || h.collection !== "memories" ? "observations" : h.kind === "episodic" || h.kind === "raw" || (h.tags || []).some((t) => t.startsWith("session:")) ? "events" : "facts";
  // the server's event-time order (recall_time.go hitEventTime): observed_at, valid_from, then effective_at / created_at
  const eventAt = (h) => {
    const c = h.memory_context || {};
    for (const [v, what] of [[c.observed_at, "일어난 때"], [c.valid_from, "맞기 시작한 때"], [h.effective_at, "적용된 때"], [h.created_at, "저장한 때"], [h.updated_at, "저장한 때"]]) { const t = v ? Date.parse(v) : NaN; if (Number.isFinite(t)) return { t, what }; }
    return { t: 0, what: "" };
  };
  const day = (t) => new Date(t).toLocaleDateString(globalThis.ArkismI18n?.locale() || "ko-KR", { year: "numeric", month: "long", day: "numeric", weekday: "short" });
  const identity = () => JSON.stringify([state.server, state.token, state.scope]);
  const active = () => state.view === "memories" && state.memTab === "past";
  const date = (value) => new Date(value).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR");
  const localNow = () => { const now = new Date(); return new Date(now - now.getTimezoneOffset() * 60000).toISOString().slice(0, 16); };
  const loads = globalThis.ArkismKeep.latest();
  let key = "", rows = [], next = "", busy = false, error = "", selected = null;
  let when = localNow(), term = "", asOf = "", searched = false;
  let mode = "line", ask = "", win = null, lane = "all", opening = "", shownRows = []; // mode line = 시간순, at = 그 시각 상태
  function reset() {
    loads.cancel(); key = ""; rows = []; next = ""; busy = false; error = ""; selected = null;
    when = localNow(); term = ""; asOf = ""; searched = false; mode = "line"; ask = ""; win = null; lane = "all"; opening = "";
  }
  function lineDetail() {
    const body = detailShell("시간순 기억");
    if (!selected) { body.append(el("h2", "", "기억을 골라 확인하세요"), el("p", "lead", "언제 있었던 일인지 순서대로 보여 줘요. 사람이 승인한 기억만 나와요.")); return; }
    const at = eventAt(selected), head = el("div", "d-head");
    head.append(el("p", "d-kind", LANES[laneOf(selected)]), el("h2", "", selected.text || selected.summary || "본문 없음"));
    if (at.t) head.append(el("p", "lead", `${at.what} · ${date(at.t)}`));
    body.append(head);
    if (laneOf(selected) === "observations") body.append(el("p", "meta", `여러 기억을 정리한 관찰이에요. 바탕 기억 ${(selected.source_ids || []).length}개`));
    const more = el("details", "section"), facts = el("dl", "facts");
    more.append(el("summary", "", "자세히"), facts);
    facts.append(el("dt", "", "찾은 방법"), el("dd", "", (selected.lanes || []).map((l) => FOUND[l] || l).join(", ") || "—"));
    if (win) facts.append(el("dt", "", "찾은 기간"), el("dd", "", `${day(Date.parse(win.from))} ~ ${day(Date.parse(win.to) - 1)}`));
    if (selected.created_at) facts.append(el("dt", "", "저장한 때"), el("dd", "", date(selected.created_at)));
    body.append(more);
    if (selected.collection === "memories" && openRecord && scopeQuery) {
      const open = button(opening === selected.id ? "여는 중…" : "기억 화면에서 열기", "small", null, () => void openMemory(selected));
      open.disabled = opening === selected.id; body.append(open);
      if (opening === `!${selected.id}`) body.append(el("p", "meta", "이 기억을 열지 못했어요. 지워졌거나 볼 권한이 없을 수 있어요."));
    }
  }
  async function openMemory(hit) {
    opening = hit.id; lineDetail();
    try { const rec = await request(`/v1/memories/${encodeURIComponent(hit.id)}?${scopeQuery()}`); if (rec?.id !== hit.id) throw new Error("invalid_response"); opening = ""; openRecord(rec); }
    catch (_) { opening = `!${hit.id}`; if (selected === hit && active()) lineDetail(); }
  }
  function detail() {
    if (mode === "line") { lineDetail(); return; }
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
  function paintLine() {
    shownRows = [];
    const table = $("#table"); table.replaceChildren(); table.setAttribute("aria-busy", String(busy));
    const status = el("p", "meta original-notice"); status.setAttribute("role", "status");
    status.textContent = busy ? "시간순 기억을 찾는 중…" : error || (!searched ? "언제 있었던 일인지 적어 보세요. 예: 지난주, 10월 말, 어제 배포" : win ? `‘${win.phrase}’ · ${day(Date.parse(win.from))} ~ ${day(Date.parse(win.to) - 1)}` : "시간 표현을 찾지 못해 내용으로만 찾았어요. 날짜를 같이 적으면 그때 일을 먼저 보여 줘요.");
    table.append(status);
    if (!searched && !busy && !error) {
      const tries = el("div", "chips mem-time-try");
      for (const w of ["오늘", "어제", "지난주", "이번 달"]) tries.append(button(w, "small", null, () => { ask = w; const q = $("#chips .memory-time-form input"); if (q) q.value = w; void load(); }));
      table.append(tries);
    }
    if (error) { const again = button("다시 찾기", "small", null, () => void load()); again.disabled = busy; table.append(again); }
    if (!searched || busy || error) { $("#chips button[type=submit]")?.toggleAttribute("disabled", busy); return; }
    const counts = { all: rows.length, facts: 0, events: 0, observations: 0 }; for (const h of rows) counts[laneOf(h)]++;
    const chips = el("div", "chips mem-time-lanes"); chips.setAttribute("role", "group"); chips.setAttribute("aria-label", "기억 종류");
    for (const [k, label] of Object.entries(LANES)) {
      const c = el("button", `chip mem-kind${lane === k ? " active" : ""}`); c.type = "button"; c.setAttribute("aria-pressed", String(lane === k));
      c.append(el("span", "", label)); if (lane === k) c.append(el("output", "", String(counts[k]))); // the count sits only next to the chosen chip
      c.addEventListener("click", () => { lane = k; paintLine(); }); chips.append(c);
    }
    table.append(chips);
    const shown = rows.filter((h) => lane === "all" || laneOf(h) === lane).map((h) => [h, eventAt(h)]).sort((a, b) => b[1].t - a[1].t);
    if (!shown.length) { table.append(el("p", "meta original-notice", rows.length ? "이 종류로는 찾은 기억이 없어요." : "그때 기억을 찾지 못했어요. 다른 날짜나 낱말로 찾아보세요.")); return; }
    const from = win ? Date.parse(win.from) : 0, to = win ? Date.parse(win.to) : 0;
    let last = ""; shownRows = shown.map(([h]) => h);
    for (const [h, at] of shown) {
      const d = at.t ? day(at.t) : "날짜 모름";
      if (d !== last) { const head = el("h3", "mem-time-day", d); head.setAttribute("data-no-i18n", ""); table.append(head); last = d; }
      const row = el("button", `original-row${selected === h ? " selected" : ""}`); row.type = "button";
      const body = el("span", "original-body"), text = el("strong", "", h.text || h.summary || "본문 없음"); text.setAttribute("data-no-i18n", "");
      const outside = win && at.t && (at.t < from || at.t >= to);
      body.append(text, el("span", "meta", [at.what, outside ? "찾은 기간 밖" : ""].filter(Boolean).join(" · ")));
      row.append(body, el("span", "mem-badge", LANES[laneOf(h)]));
      row.addEventListener("click", () => { selected = h; paintLine(); openDetail(); lineDetail(); const heading = $("#detail h2"); heading.tabIndex = -1; heading.focus(); });
      table.append(row);
    }
  }
  async function loadLine() {
    const turn = loads.begin(), current = identity(); busy = true; error = ""; rows = []; selected = null; win = null; lane = "all"; lineDetail(); paintLine();
    const live = () => active() && mode === "line" && turn.live() && current === identity();
    try {
      const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "";
      const page = await request("/v1/search", { method: "POST", body: JSON.stringify({ scope: state.scope, query: ask, limit: 50, explain: true, ...(tz ? { timezone: tz } : {}) }) });
      if (!live()) return;
      if (page?.authority !== "data_only_untrusted" || !Array.isArray(page.items) || page.items.some((h) => typeof h?.id !== "string" || typeof h.text !== "string")) throw new Error("invalid_response");
      const w = page.explain?.time_window;
      win = w && Number.isFinite(Date.parse(w.from)) && Number.isFinite(Date.parse(w.to)) ? w : null;
      rows = page.items; searched = true;
    } catch (_) {
      if (!live()) return;
      rows = []; win = null; error = "찾지 못했어요. 연결과 접근 권한을 확인하고 다시 찾아 주세요.";
    } finally { if (live()) { busy = false; paintLine(); } }
  }
  function paint() {
    if (mode === "line") { paintLine(); return; }
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
    if (mode === "line") { if (ask) await loadLine(); return; }
    const requested = more ? next : "";
    if (!asOf) return;
    if (!more) { rows = []; next = ""; selected = null; detail(); }
    const turn = loads.begin(), current = identity(); busy = true; error = ""; paint();
    const live = () => active() && turn.live() && current === identity();
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
    if (!changed && $(`#chips .memory-time-form.mt-${mode}`)) { paint(); return; }
    $("#chips").replaceChildren(); $("#stats").replaceChildren();
    const modes = el("div", "mem-time-modes"); modes.setAttribute("role", "group"); modes.setAttribute("aria-label", "보는 방법");
    for (const [k, label] of [["line", "시간순"], ["at", "그 시각 상태"]]) {
      const c = el("button", `chip mem-kind${mode === k ? " active" : ""}`, label); c.type = "button"; c.setAttribute("aria-pressed", String(mode === k));
      c.addEventListener("click", () => { if (mode === k) return; loads.cancel(); mode = k; rows = []; next = ""; busy = false; error = ""; selected = null; searched = false; win = null; draw(); detail(); });
      modes.append(c);
    }
    $("#chips").append(modes);
    if (mode === "line") {
      const form = el("form", "original-search memory-time-form mt-line"), label = el("label", "panel-search");
      const q = el("input"); q.type = "search"; q.value = ask; q.maxLength = 200; q.placeholder = "언제 일이었나요? 예: 지난주 배포, 10월 말"; q.setAttribute("aria-label", "시간으로 기억 찾기");
      label.append(icon("magnifying-glass"), q);
      const go = button("찾기", "primary small"); go.type = "submit";
      form.append(label, go);
      form.addEventListener("submit", (event) => {
        event.preventDefault(); if (busy) return;
        if (!q.value.trim()) { q.setCustomValidity("언제 일인지 적어 주세요."); q.reportValidity(); return; }
        ask = q.value.trim(); void load();
      });
      q.addEventListener("input", () => q.setCustomValidity(""));
      $("#chips").append(form); paint(); return;
    }
    const form = el("form", "original-search memory-time-form mt-at");
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
  function close() { const index = (mode === "line" ? shownRows : rows).indexOf(selected); selected = null; paint(); detail(); if (index >= 0) $("#table").querySelectorAll(".original-row")[index]?.focus(); }
  return { draw, reset, detail, close };
};
