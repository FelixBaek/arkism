window.ArkismOriginals = (ctx) => {
  "use strict";
  const { state, $, el, icon, button, request, detailShell, openDetail, ago } = ctx;
  const authority = "data_only_untrusted";
  const origins = { codex: "Codex", claude_code: "Claude", zcode: "Zcode", gemini: "Gemini", grok: "Grok", antigravity: "Antigravity" };
  const identity = () => JSON.stringify([state.server, state.token, state.scope]);
  const post = (path, data) => request(`/v1/session-archives/${path}`, { method: "POST", body: JSON.stringify({ scope: state.scope, ...data }) });
  const valid = (value) => value?.authority === authority;
  const originalView = () => state.view === "memories" && state.memTab === "originals";
  const date = (value) => value ? new Date(value).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR") : "시각 없음";
  let key = "", rows = null, next = "", term = "", error = "", busy = false, run = 0, selection = null, omitted = false;
  function reset() { $("#table").removeAttribute("aria-busy"); key = ""; rows = null; next = ""; term = ""; error = ""; busy = false; selection = null; omitted = false; run++; }
  function messageReader(host, ref, live) {
    let offset = 0, current = ref, loading = false, text = "";
    const content = el("pre", "original-text");
    const status = el("p", "meta"); status.setAttribute("role", "status");
    const more = button("원문 읽기", "small", null, () => load());
    host.append(content, status, more);
    async function load() {
      if (loading || !live()) return;
      loading = true; more.disabled = true; status.textContent = "원문을 읽는 중…";
      try {
        const value = await post("message", { ref: current, text_offset: offset });
        if (!live()) return;
        if (!valid(value) || typeof value.text !== "string" || value.ref?.archive_id !== ref.archive_id || value.text_offset !== offset) throw new Error("invalid_response");
        if (value.next_text_offset != null && (!Number.isInteger(value.next_text_offset) || value.next_text_offset <= offset)) throw new Error("invalid_response");
        text += value.text; content.textContent = text; current = value.ref;
        status.textContent = `${value.role === "user" ? "사용자" : value.role === "assistant" ? "에이전트" : "메시지"} · 조회만으로 보존 기간은 늘어나지 않아요.`;
        more.hidden = value.next_text_offset == null;
        if (!more.hidden) { offset = value.next_text_offset; more.textContent = "메시지 이어 읽기"; }
      } catch (_) {
        if (!live()) return;
        text = ""; offset = 0; current = ref; content.textContent = "";
        status.textContent = "원문을 읽지 못했어요. 만료·삭제되었거나 접근 권한이 바뀌었을 수 있어요.";
        more.hidden = false; more.textContent = "다시 읽기";
      } finally { loading = false; more.disabled = false; }
    }
    load();
  }
  function sourceDetails(host, memory, response) {
    const owner = identity();
    const live = () => host.isConnected && owner === identity() && state.view === "memories" && state.selected === memory.id;
    host.replaceChildren();
    if (!valid(response) || response.memory?.id !== memory.id || response.memory?.revision !== memory.revision || !Array.isArray(response.sources)) {
      host.append(el("p", "meta", "출처를 불러오지 못했어요. 다시 선택해 주세요.")); return;
    }
    if (!response.sources.length) host.append(el("p", "meta", "연결된 출처가 없어요."));
    for (const source of response.sources) {
      const row = el("section", "original-source");
      row.append(el("p", "meta", `${origins[source.origin_product] || source.origin_product || "출처"} · ${date(source.collected_at)}`));
      if (source.origin_agent_id) row.append(el("p", "meta", `원래 실행 에이전트   ${source.origin_agent_id} · 실행기가 기록한 정보`));
      row.append(el("p", "original-excerpt", source.excerpt || "조회할 수 있는 근거 본문이 없어요."));
      const refs = source.archive_messages || [];
      refs.forEach((ref, index) => {
        const panel = el("div", "original-reader"); panel.hidden = true;
        const trigger = button(`연결된 원문 ${refs.length > 1 ? index + 1 : ""} 열기`.replace("  ", " "), "small", null, () => {
          panel.hidden = !panel.hidden; trigger.setAttribute("aria-expanded", String(!panel.hidden));
          panel.replaceChildren();
          if (!panel.hidden) messageReader(panel, ref, () => live() && panel.isConnected && !panel.hidden);
        });
        trigger.setAttribute("aria-expanded", "false"); row.append(trigger, panel);
      });
      if (!refs.length) row.append(el("p", "meta", source.state === "erased" || source.deleted_at ? "원문이 삭제됐어요." : "정확한 대화 위치가 연결되지 않은 출처예요."));
      host.append(row);
    }
  }
  const hasSkipped = (page) => ["oversized_records", "invalid_records", "secret_records", "unsupported_records", "unsupported_archives"].some((name) => page[name] > 0);
  function skipped(page, host) {
    if (hasSkipped(page)) {
      host.append(el("p", "meta original-notice", "읽을 수 없거나 표시할 수 없는 기록이 일부 제외됐어요."));
    }
  }
  function messages(host, archive, live) {
    const list = el("div", "original-messages"), status = el("p", "meta"); status.setAttribute("role", "status");
    const more = button("메시지 더 보기", "small", null, () => load());
    host.append(list, status, more);
    let cursor = "", loading = false, count = 0;
    async function load() {
      if (loading || !live()) return;
      loading = true; more.disabled = true; status.textContent = "메시지를 불러오는 중…";
      try {
        const page = await post("search", { archive_id: archive.id, q: "", after: cursor, limit: 20 });
        if (!live()) return;
        if (!valid(page) || !Array.isArray(page.items) || (page.next_cursor && page.next_cursor === cursor)) throw new Error("invalid_response");
        for (const item of page.items) {
          if (!valid(item) || !item.ref) throw new Error("invalid_response");
          const block = el("section", "original-source"), panel = el("div", "original-reader"); panel.hidden = true;
          block.append(el("p", "meta", item.role === "user" ? "사용자" : "에이전트"), el("p", "original-excerpt", item.text?.slice(0, 240) || "빈 메시지"));
          const trigger = button("메시지 열기", "small", null, () => {
            panel.hidden = !panel.hidden; trigger.setAttribute("aria-expanded", String(!panel.hidden)); panel.replaceChildren();
            if (!panel.hidden) messageReader(panel, item.ref, () => live() && panel.isConnected && !panel.hidden);
          });
          trigger.setAttribute("aria-expanded", "false"); block.append(trigger, panel); list.append(block); count++;
        }
        cursor = page.next_cursor || ""; more.hidden = !cursor; skipped(page, list);
        status.textContent = count ? `${count}개 메시지를 불러왔어요.` : cursor ? "이 구간에 표시할 메시지가 없어요. 다음 구간을 확인할 수 있어요." : "표시할 메시지가 없어요.";
      } catch (_) {
        if (!live()) return;
        list.replaceChildren(); count = 0; cursor = ""; more.hidden = false;
        status.textContent = "메시지를 불러오지 못했어요. 만료 여부와 접근 권한을 확인하고 다시 시도하세요.";
      } finally { loading = false; more.disabled = false; }
    }
    load();
  }
  function detail() {
    const host = detailShell("대화 원문"), selected = selection, owner = identity();
    const live = () => host.isConnected && owner === identity() && originalView() && selection === selected;
    if (!selected) { host.append(el("h2", "", "보존 중인 대화 확인"), el("p", "lead", "목록에서 원본을 고르거나 대화 내용을 검색하세요. 읽기만 해서는 보존 기간이 늘어나지 않아요.")); return; }
    if (selected.ref) {
      host.append(el("h2", "", "검색한 메시지")); messageReader(host, selected.ref, live);
    } else {
      host.append(el("h2", "", `${origins[selected.origin] || selected.origin || "에이전트"} 대화`), el("p", "meta", `${date(selected.observed_at)} · 보존 기한 ${date(selected.expires_at)}`));
      messages(host, selected, live);
    }
  }
  function paint() {
    if (!originalView() || key !== identity()) return;
    const table = $("#table"); table.replaceChildren(); table.setAttribute("aria-busy", String(busy));
    const submit = $(".original-search button[type=submit]"); if (submit) submit.disabled = busy;
    if (omitted) skipped({ unsupported_records: 1 }, table);
    for (const item of rows || []) {
      const row = el("button", `original-row${selection === item ? " selected" : ""}`); row.type = "button";
      const body = el("span", "original-body");
      body.append(el("strong", "", item.ref ? (item.text?.slice(0, 180) || "빈 메시지") : `${origins[item.origin] || item.origin || "에이전트"} · ${item.device || "기기"}`));
      body.append(el("span", "meta", item.ref ? (item.role === "user" ? "사용자 메시지" : "에이전트 메시지") : `${ago(item.observed_at)} · 보존 기한 ${date(item.expires_at)}`));
      row.append(body);
      if (!item.ref) row.append(el("span", `mem-badge${item.state === "active" ? "" : " warn"}`, item.state === "active" ? "보존 중" : "조회 제한"));
      row.addEventListener("click", () => { selection = item; state.selected = "original-selected"; detail(); openDetail(); paint(); const heading = $("#detail h2"); heading.tabIndex = -1; heading.focus(); }); table.append(row);
    }
    if (busy) table.append(el("p", "empty", "원문을 불러오는 중…"));
    else if (error) table.append(el("p", "empty", error), button("다시 불러오기", "small", null, () => load(!!next)));
    else if (!rows?.length) table.append(el("p", "empty", next ? "이 구간에 검색 결과가 없어요. 다음 구간을 확인하세요." : "표시할 원문이 없어요."));
    if (next && !error) { const more = button("원문 더 보기", "small", null, () => load(true)); more.disabled = busy; table.append(more); }
  }
  async function load(more = false) {
    if (busy) return;
    const owner = identity(), current = ++run; busy = true; error = "";
    if (!more) { rows = []; next = ""; selection = null; omitted = false; state.selected = null; detail(); }
    paint();
    try {
      const page = await post(term ? "search" : "list", { ...(term ? { q: term } : {}), after: more ? next : "", limit: 20 });
      if (!originalView() || owner !== identity() || current !== run) return;
      if (!valid(page) || !Array.isArray(page.items) || (page.next_cursor && page.next_cursor === next)) throw new Error("invalid_response");
      rows = [...rows, ...page.items]; next = page.next_cursor || "";
      omitted ||= hasSkipped(page);
    } catch (_) {
      if (originalView() && owner === identity() && current === run) error = "원문을 불러오지 못했어요. 연결과 접근 권한을 확인하세요.";
    } finally { if (current === run) { busy = false; paint(); } }
  }
  function draw() {
    if (key !== identity()) { reset(); key = identity(); }
    $("#chips").replaceChildren(); $("#stats").replaceChildren();
    const form = el("form", "original-search"), label = el("label", "panel-search");
    const input = el("input"); input.type = "search"; input.value = term; input.placeholder = "대화 내용 검색"; input.setAttribute("aria-label", "대화 내용 검색"); input.maxLength = 256; label.append(icon("magnifying-glass"), input);
    form.append(label, button("검색", "primary small")); form.lastChild.type = "submit";
    form.addEventListener("submit", (event) => {
      event.preventDefault(); if (busy) return;
      if (new TextEncoder().encode(input.value.trim()).length > 256) { input.setCustomValidity("검색어를 조금 짧게 입력하세요."); input.reportValidity(); return; }
      input.setCustomValidity(""); term = input.value.trim(); load();
    });
    input.addEventListener("input", () => input.setCustomValidity(""));
    $("#chips").append(form); paint(); if (rows === null) load();
  }
  function close() {
    const index = rows?.indexOf(selection) ?? -1;
    selection = null; paint(); detail();
    if (index >= 0) $("#table").querySelectorAll(".original-row")[index]?.focus();
  }
  return { draw, detail, sourceDetails, reset, close, reload: () => load() };
};
